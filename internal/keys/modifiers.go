package keys

import "fmt"

// Mods 是修饰键状态位掩码（左右不区分，用于普通键的 shift 判断）。
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
)

// ModSide 标识物理修饰键的左右。
type ModSide int

const (
	SideNone ModSide = iota
	SideShiftLeft
	SideShiftRight
	SideCtrlLeft
	SideCtrlRight
	SideAltLeft
	SideAltRight
	SideSuperLeft
	SideSuperRight
)

// Mapping 修饰键角色映射：Mac 修饰键映射到远程哪个修饰键。
// 取值 "super" | "alt" | "ctrl" | "none"。
type Mapping struct {
	Command string // Mac Command 键，默认 "super"
	Option  string // Mac Option 键，默认 "alt"
	Control string // Mac Control 键，默认 "ctrl"
}

// DefaultMapping 返回默认映射：Cmd→super，Option→alt，Control→ctrl。
func DefaultMapping() Mapping {
	return Mapping{Command: "super", Option: "alt", Control: "ctrl"}
}

var validTargets = map[string]bool{
	"super": true,
	"alt":   true,
	"ctrl":  true,
	"none":  true,
}

// Validate 校验映射值合法且互不冲突（两个非 none 键不能映射到同一目标）。
func (m Mapping) Validate() error {
	vals := []struct {
		name string
		v    string
	}{
		{"command", m.Command},
		{"option", m.Option},
		{"control", m.Control},
	}
	used := map[string]string{}
	for _, f := range vals {
		if !validTargets[f.v] {
			return fmt.Errorf("keys: %s 的映射值 %q 非法，允许 super/alt/ctrl/none", f.name, f.v)
		}
		if f.v == "none" {
			continue
		}
		if prev, dup := used[f.v]; dup {
			return fmt.Errorf("keys: %s 与 %s 都映射到 %q，冲突", prev, f.name, f.v)
		}
		used[f.v] = f.name
	}
	return nil
}

// Event 一个待发送的 RFB keysym 事件。
type Event struct {
	Keysym uint32
	Down   bool
}

// 8 个物理修饰键（HID 0xE0-0xE7）的左右侧别。
var hidModSides = map[uint32][2]ModSide{
	0xE0: {SideCtrlLeft, SideCtrlRight},
	0xE1: {SideShiftLeft, SideShiftRight},
	0xE2: {SideAltLeft, SideAltRight},
	0xE3: {SideSuperLeft, SideSuperRight},
	0xE4: {SideCtrlLeft, SideCtrlRight},
	0xE5: {SideShiftLeft, SideShiftRight},
	0xE6: {SideAltLeft, SideAltRight},
	0xE7: {SideSuperLeft, SideSuperRight},
}

// 每个映射目标的左右 keysym。
var targetKeysyms = map[string][2]uint32{
	"shift": {KeysymShiftL, KeysymShiftR},
	"ctrl":  {KeysymControlL, KeysymControlR},
	"alt":   {KeysymAltL, KeysymAltR},
	"super": {KeysymSuperL, KeysymSuperR},
}

// Translator 把物理键事件序列翻译成 RFB keysym 事件序列。
// 有状态：跟踪哪些键当前按下，支持 ReleaseAll 防远程"卡键"。
type Translator struct {
	mapping Mapping

	// modCounts: 映射后目标 → 按下计数（左右同目标时引用计数，如左右 Cmd 都映射 super）。
	modCounts map[string]int

	// normalDown: 普通键 keysym → 按下计数。
	normalDown map[uint32]int
}

// NewTranslator 创建 Translator；映射非法时返回 error。
func NewTranslator(m Mapping) (*Translator, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &Translator{
		mapping:    m,
		modCounts:  map[string]int{},
		normalDown: map[uint32]int{},
	}, nil
}

// modTarget 返回物理修饰键侧别经映射后的目标角色与 keysym。
// 第二个返回值为 false 表示该修饰键被映射为 none（事件应被吞掉）。
func (t *Translator) modTarget(side ModSide) (target string, keysym uint32, ok bool) {
	var right bool
	switch side {
	case SideShiftLeft:
		return "shift", KeysymShiftL, true
	case SideShiftRight:
		return "shift", KeysymShiftR, true
	case SideCtrlLeft:
		target = t.mapping.Control
	case SideCtrlRight:
		target, right = t.mapping.Control, true
	case SideAltLeft:
		target = t.mapping.Option
	case SideAltRight:
		target, right = t.mapping.Option, true
	case SideSuperLeft:
		target = t.mapping.Command
	case SideSuperRight:
		target, right = t.mapping.Command, true
	default:
		return "", 0, false
	}
	if target == "none" {
		return "", 0, false
	}
	if right {
		return target, targetKeysyms[target][1], true
	}
	return target, targetKeysyms[target][0], true
}

// KeyChange 报告一个物理键的按下/松开，返回需要发送给远端的事件序列（可能为空）。
//   - hid: USB HID usage；side: hid 为 8 个修饰键之一时的左右，否则 SideNone
//   - mods: 当前按下的修饰键（映射前的物理状态，用于普通键 shift 判断）
//   - repeat: 自动重复（仅普通键有意义；修饰键忽略）
func (t *Translator) KeyChange(hid uint32, down bool, side ModSide, mods Mods, repeat bool) []Event {
	// 修饰键：0xE0-0xE7（side 缺失时按 hid 默认取左/右）
	if pair, isMod := hidModSides[hid]; isMod {
		if side == SideNone {
			if hid == 0xE4 || hid == 0xE5 || hid == 0xE6 || hid == 0xE7 {
				side = pair[1]
			} else {
				side = pair[0]
			}
		}
		if repeat {
			return nil // 修饰键忽略自动重复
		}
		return t.modKeyChange(side, down)
	}

	// 普通键
	ks, ok := KeysymForHIDWithShift(hid, mods&ModShift != 0)
	if !ok {
		return nil
	}
	if down {
		if repeat {
			// 自动重复：直接再发一次 down（不做防御性 up）
			t.normalDown[ks]++
			return []Event{{Keysym: ks, Down: true}}
		}
		var evs []Event
		// 同一个 keysym 已按下又收到 down（非 repeat）：先补 up 再发 down（防御异常序列）
		if t.normalDown[ks] > 0 {
			evs = append(evs, Event{Keysym: ks, Down: false})
			t.normalDown[ks]--
		}
		t.normalDown[ks]++
		return append(evs, Event{Keysym: ks, Down: true})
	}
	// 松开：仅在我们跟踪过该键按下时发 up
	if t.normalDown[ks] > 0 {
		t.normalDown[ks]--
		if t.normalDown[ks] == 0 {
			delete(t.normalDown, ks)
		}
		return []Event{{Keysym: ks, Down: false}}
	}
	return nil
}

// modKeyChange 处理修饰键侧别的按下/松开（引用计数语义）。
func (t *Translator) modKeyChange(side ModSide, down bool) []Event {
	target, keysym, ok := t.modTarget(side)
	if !ok {
		// 映射为 none：整个吞掉，不发任何事件
		return nil
	}
	if down {
		t.modCounts[target]++
		if t.modCounts[target] == 1 {
			return []Event{{Keysym: keysym, Down: true}}
		}
		return nil // 同目标已按下（如左右 Cmd 都映射 super）：不重复发 down
	}
	if t.modCounts[target] > 0 {
		t.modCounts[target]--
		if t.modCounts[target] == 0 {
			delete(t.modCounts, target)
			return []Event{{Keysym: keysym, Down: false}}
		}
	}
	return nil
}

// ReleaseAll 返回释放所有已跟踪按下的键的事件序列（普通键先、修饰键后），并清空状态。
// 用于窗口失焦、断开连接、释放键盘捕获时调用，防卡键。
func (t *Translator) ReleaseAll() []Event {
	var evs []Event
	for ks, n := range t.normalDown {
		for i := 0; i < n; i++ {
			evs = append(evs, Event{Keysym: ks, Down: false})
		}
		delete(t.normalDown, ks)
	}
	for target, n := range t.modCounts {
		ks := targetKeysyms[target][0]
		for i := 0; i < n; i++ {
			evs = append(evs, Event{Keysym: ks, Down: false})
		}
		delete(t.modCounts, target)
	}
	return evs
}

// PressedCount 当前按下的键数量（普通键计数 + 修饰键引用计数之和），调试用。
func (t *Translator) PressedCount() int {
	n := 0
	for _, c := range t.normalDown {
		n += c
	}
	for _, c := range t.modCounts {
		n += c
	}
	return n
}
