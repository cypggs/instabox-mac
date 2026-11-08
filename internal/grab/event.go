// Package grab 在 macOS 上提供全局键盘捕获（"键盘捕获模式"）：
// 通过 CGEventTap 拦截本机按键，交给 VNC 客户端翻译成远程桌面事件，
// 让 Cmd+Tab、Cmd+Space 等系统组合键作用于远程桌面而不是本机 macOS。
//
// 重要限制（使用时必须告知用户）：
//
//   - 需要"辅助功能"（Accessibility）权限。授权后通常需要重启应用才生效。
//   - 无法捕获"安全事件"：密码输入框、锁屏界面、强制退出对话框
//     （Cmd+Opt+Esc）等系统安全输入上下文中的按键不会发给 tap。
//   - 左右修饰键的区分：CGEventFlags 公开位里没有设备左右位
//     （NX_DEVICEL* 掩码属 IOKit 内部，CGEventGetFlags 不暴露），
//     因此用 flagsChanged 事件自带的 keycode 判断是哪个物理修饰键
//     （kVK_Shift=0x38 左 / kVK_RightShift=0x3C 右，以此类推）。
//   - 回调运行在专用的 runloop goroutine 中，不在主线程；
//     调用方负责与其他 goroutine 的同步。
//   - 事件 tap 可能被系统禁用（如事件处理超时），Manager 对超时
//     会自动重新启用 tap。
package grab

import "errors"

// Event 一个被捕获的键盘事件。
type Event struct {
	HID    uint32 // USB HID usage ID（已转换，直接可用；修饰键为 0xE0-0xE7）
	Down   bool   // keyDown=true / keyUp=false；flagsChanged 时为该修饰键当前是否按下
	Repeat bool   // 系统自动重复（仅普通键有意义）
	Mods   uint64 // CGEventFlags 风格位掩码原样透传（见下面 FlagMask* 常量）
}

// macOS CGEventFlags 位（照抄 CGEventTypes.h 公开定义，不依赖 SDK 头文件）。
const (
	// FlagMaskAlphaShift 大写锁定（CapsLock）。
	FlagMaskAlphaShift = 1 << 16
	// FlagMaskShift Shift（左右不区分，侧别看 Event.HID）。
	FlagMaskShift = 1 << 17
	// FlagMaskControl Control（左右不区分，侧别看 Event.HID）。
	FlagMaskControl = 1 << 18
	// FlagMaskAlternate Option（左右不区分，侧别看 Event.HID）。
	FlagMaskAlternate = 1 << 19
	// FlagMaskCommand Command（左右不区分，侧别看 Event.HID）。
	FlagMaskCommand = 1 << 20
)

// ErrUnsupported 在非 macOS 平台上调用 Start 返回的错误。
var ErrUnsupported = errors.New("grab: 键盘捕获仅在 macOS 上可用")

// CGEvent 事件类型（CGEventTypes.h）。
const (
	evKeyDown      uint64 = 10 // kCGEventKeyDown
	evKeyUp        uint64 = 11 // kCGEventKeyUp
	evFlagsChanged uint64 = 12 // kCGEventFlagsChanged

	// tap 被禁用伪事件：系统发给回调，不是真实键盘事件。
	evTapDisabledByTimeout   uint64 = 0xFFFFFFFE // kCGEventTapDisabledByTimeout
	evTapDisabledByUserInput uint64 = 0xFFFFFFFF // kCGEventTapDisabledByUserInput
)

// CGEvent 字段 ID（CGEventField 枚举）。
const (
	fieldKeyboardEventKeycode    uint32 = 9  // kCGKeyboardEventKeycode：macOS virtual keycode
	fieldKeyboardEventAutorepeat uint32 = 12 // kCGKeyboardEventAutorepeat
)

// eventMaskBit 对应头文件里的 CGEventMaskBit(eventType) = 1 << eventType。
func eventMaskBit(eventType uint64) uint64 { return uint64(1) << eventType }

// vkFlagMask 把 flagsChanged 事件的修饰键 keycode 映射到 CGEventFlags 位。
// 左右侧修饰键共享同一个位；左右身份由 keycode 本身（及查表得到的 HID）区分。
var vkFlagMask = map[uint32]uint64{
	0x36: FlagMaskCommand,    // kVK_RightCommand
	0x37: FlagMaskCommand,    // kVK_Command
	0x38: FlagMaskShift,      // kVK_Shift
	0x3C: FlagMaskShift,      // kVK_RightShift
	0x3A: FlagMaskAlternate,  // kVK_Option
	0x3D: FlagMaskAlternate,  // kVK_RightOption
	0x3B: FlagMaskControl,    // kVK_Control
	0x3E: FlagMaskControl,    // kVK_RightControl
	0x39: FlagMaskAlphaShift, // kVK_CapsLock
}

// modifierDown 比较前后两个 flags 状态里某位修饰键的变化。
// down 报告该修饰键当前是否按下（cur 中该位是否置位），
// changed 报告该位相对 prev 是否发生变化。
// 纯函数，便于单测。
func modifierDown(prev, cur, mask uint64) (down bool, changed bool) {
	down = cur&mask != 0
	changed = prev&mask != cur&mask
	return down, changed
}

// translateState 是事件翻译的跨事件状态。
// 只由 runloop goroutine 访问（或单测），无需加锁。
type translateState struct {
	// modPressed 记录每个修饰键 keycode 的当前按下状态。
	// 同一 keycode 的 flagsChanged 事件按"按一次、松一次"交替解释：
	// 当同侧修饰键（如左右 Shift）都按住时，CGEventFlags 对应位不变化，
	// 只有逐 keycode 计数才能区分是哪一侧的动作。
	modPressed map[uint32]bool
}

// translate 把一个 CGEvent 的关键字段翻译成 Event。
// ok=false 表示这个事件不应拦截（keycode 不在转换表、非修饰键的
// flagsChanged 等），调用方应原样放过（回调返回事件本身）。
// 纯逻辑，不碰系统调用。
func translate(st *translateState, eventType uint64, keycode uint32, flags uint64, autorepeat bool) (Event, bool) {
	switch eventType {
	case evKeyDown, evKeyUp:
		hid, ok := HIDForVK(keycode)
		if !ok {
			return Event{}, false
		}
		return Event{
			HID:    hid,
			Down:   eventType == evKeyDown,
			Repeat: autorepeat,
			Mods:   flags,
		}, true

	case evFlagsChanged:
		mask, ok := vkFlagMask[keycode]
		if !ok {
			// 非已知修饰键的 flagsChanged（理论上罕见），原样放过。
			return Event{}, false
		}
		if st.modPressed == nil {
			st.modPressed = make(map[uint32]bool)
		}
		var down bool
		if pressed, seen := st.modPressed[keycode]; seen {
			// 有历史：本次是该键的一次新动作，与上次状态相反。
			down = !pressed
		} else {
			// 首次见到该键：flags 位是真实状态（与 0 比较即查位）。
			down, _ = modifierDown(0, flags, mask)
		}
		st.modPressed[keycode] = down
		hid, _ := HIDForVK(keycode)
		return Event{HID: hid, Down: down, Mods: flags}, true

	default:
		return Event{}, false
	}
}
