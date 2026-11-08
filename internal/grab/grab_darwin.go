//go:build darwin

package grab

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// 本文件的所有系统调用（CoreGraphics / CoreFoundation / HIServices）
// 都集中在 syms 这个函数指针表里，经 purego 动态绑定，不依赖 SDK 头文件，
// 也不使用 cgo（项目硬性要求 CGO_ENABLED=0）。
//
// 事件翻译的纯逻辑（translate / modifierDown）在 event.go，可脱离系统单测。

// 框架路径。HIServices 是 ApplicationServices 的子框架，
// AXIsProcessTrustedWithOptions 与 kAXTrustedCheckOptionPrompt 在其中。
const (
	coreGraphicsPath   = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	hiservicesPath     = "/System/Library/Frameworks/ApplicationServices.framework/Frameworks/HIServices.framework/HIServices"
	appServicesPath    = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	systemPrefsURL     = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
)

// syms 集中全部 C 符号地址，由 load 惰性填充一次。
var (
	loadOnce sync.Once
	loadErr  error

	libCG, libCF, libHIServices uintptr

	syms struct {
		// CoreGraphics
		cgEventTapCreate            uintptr // CGEventTapCreate
		cgEventTapEnable            uintptr // CGEventTapEnable
		cgEventGetIntegerValueField uintptr // CGEventGetIntegerValueField
		cgEventGetFlags             uintptr // CGEventGetFlags
		cgDisplayHideCursor         uintptr // CGDisplayHideCursor
		cgDisplayShowCursor         uintptr // CGDisplayShowCursor

		// CoreFoundation
		cfMachPortCreateRunLoopSource uintptr // CFMachPortCreateRunLoopSource
		cfRunLoopGetCurrent           uintptr // CFRunLoopGetCurrent
		cfRunLoopAddSource            uintptr // CFRunLoopAddSource
		cfRunLoopRunInMode            uintptr // CFRunLoopRunInMode
		cfRelease                     uintptr // CFRelease
		cfDictionaryCreate            uintptr // CFDictionaryCreate

		// CoreFoundation 导出常量
		kCFRunLoopCommonModes           uintptr
		kCFRunLoopDefaultMode           uintptr
		kCFBooleanTrue                  uintptr
		kCFTypeDictionaryKeyCallBacks   uintptr
		kCFTypeDictionaryValueCallBacks uintptr

		// HIServices
		axIsProcessTrustedWithOptions uintptr // AXIsProcessTrustedWithOptions
		kAXTrustedCheckOptionPrompt   uintptr // kAXTrustedCheckOptionPrompt（CFStringRef 常量）
	}
)

// load 惰性加载框架与符号。重复调用返回第一次的结果。
func load() error {
	loadOnce.Do(func() {
		if err := loadLibs(); err != nil {
			loadErr = err
			return
		}
		loadErr = loadSyms()
	})
	return loadErr
}

func loadLibs() error {
	var err error
	if libCG, err = purego.Dlopen(coreGraphicsPath, purego.RTLD_NOW); err != nil {
		return fmt.Errorf("grab: 打开 CoreGraphics: %w", err)
	}
	if libCF, err = purego.Dlopen(coreFoundationPath, purego.RTLD_NOW); err != nil {
		return fmt.Errorf("grab: 打开 CoreFoundation: %w", err)
	}
	// HIServices 优先按子框架路径打开；个别系统布局下退回 ApplicationServices
	// 伞框架，再不行退回 RTLD_DEFAULT（符号可能已被其他框架重导出）。
	for _, p := range []string{hiservicesPath, appServicesPath} {
		if libHIServices, err = purego.Dlopen(p, purego.RTLD_NOW); err == nil {
			return nil
		}
	}
	libHIServices = purego.RTLD_DEFAULT
	return nil
}

// derefConst 读取导出常量（CFStringRef / CFBooleanRef / 回调结构体）的值：
// 符号地址处存的就是常量值本身。
func derefConst(p uintptr) uintptr {
	return **(**uintptr)(unsafe.Pointer(&p))
}

func loadSyms() error {
	bind := func(lib uintptr, name string, dst *uintptr) error {
		p, err := purego.Dlsym(lib, name)
		if err != nil {
			return fmt.Errorf("grab: 找不到符号 %s: %w", name, err)
		}
		*dst = p
		return nil
	}
	consts := func(lib uintptr, name string, dst *uintptr) error {
		p, err := purego.Dlsym(lib, name)
		if err != nil {
			return fmt.Errorf("grab: 找不到常量 %s: %w", name, err)
		}
		*dst = derefConst(p)
		return nil
	}

	type binding struct {
		lib     uintptr
		name    string
		dst     *uintptr
		isConst bool
	}
	for _, b := range []binding{
		{libCG, "CGEventTapCreate", &syms.cgEventTapCreate, false},
		{libCG, "CGEventTapEnable", &syms.cgEventTapEnable, false},
		{libCG, "CGEventGetIntegerValueField", &syms.cgEventGetIntegerValueField, false},
		{libCG, "CGEventGetFlags", &syms.cgEventGetFlags, false},
		{libCG, "CGDisplayHideCursor", &syms.cgDisplayHideCursor, false},
		{libCG, "CGDisplayShowCursor", &syms.cgDisplayShowCursor, false},
		{libCF, "CFMachPortCreateRunLoopSource", &syms.cfMachPortCreateRunLoopSource, false},
		{libCF, "CFRunLoopGetCurrent", &syms.cfRunLoopGetCurrent, false},
		{libCF, "CFRunLoopAddSource", &syms.cfRunLoopAddSource, false},
		{libCF, "CFRunLoopRunInMode", &syms.cfRunLoopRunInMode, false},
		{libCF, "CFRelease", &syms.cfRelease, false},
		{libCF, "CFDictionaryCreate", &syms.cfDictionaryCreate, false},
		{libCF, "kCFRunLoopCommonModes", &syms.kCFRunLoopCommonModes, true},
		{libCF, "kCFRunLoopDefaultMode", &syms.kCFRunLoopDefaultMode, true},
		{libCF, "kCFBooleanTrue", &syms.kCFBooleanTrue, true},
		{libCF, "kCFTypeDictionaryKeyCallBacks", &syms.kCFTypeDictionaryKeyCallBacks, true},
		{libCF, "kCFTypeDictionaryValueCallBacks", &syms.kCFTypeDictionaryValueCallBacks, true},
		{libHIServices, "AXIsProcessTrustedWithOptions", &syms.axIsProcessTrustedWithOptions, false},
		{libHIServices, "kAXTrustedCheckOptionPrompt", &syms.kAXTrustedCheckOptionPrompt, true},
	} {
		var err error
		if b.isConst {
			err = consts(b.lib, b.name, b.dst)
		} else {
			err = bind(b.lib, b.name, b.dst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Available 报告当前进程是否已被授予辅助功能权限。
// 不弹系统对话框；系统服务不可用时返回 false。
func Available() bool {
	if load() != nil {
		return false
	}
	r, _, _ := purego.SyscallN(syms.axIsProcessTrustedWithOptions, 0)
	return r != 0
}

// RequestAccess 请求辅助功能权限：弹出系统授权对话框（若尚未决定），
// 并返回当前是否已授权。用户勾选后通常需要重启应用才生效。
func RequestAccess() bool {
	if load() != nil {
		return false
	}
	// options = {kAXTrustedCheckOptionPrompt: kCFBooleanTrue}
	keys := [1]uintptr{syms.kAXTrustedCheckOptionPrompt}
	vals := [1]uintptr{syms.kCFBooleanTrue}
	dict, _, _ := purego.SyscallN(syms.cfDictionaryCreate,
		0,
		uintptr(unsafe.Pointer(&keys[0])),
		uintptr(unsafe.Pointer(&vals[0])),
		1,
		syms.kCFTypeDictionaryKeyCallBacks,
		syms.kCFTypeDictionaryValueCallBacks,
	)
	if dict == 0 {
		return Available()
	}
	defer purego.SyscallN(syms.cfRelease, dict)
	r, _, _ := purego.SyscallN(syms.axIsProcessTrustedWithOptions, dict)
	return r != 0
}

// OpenSystemPrefs 打开系统设置的"隐私与安全性 > 辅助功能"面板。
func OpenSystemPrefs() {
	// 系统 URL scheme，由 macOS 的 `open` 命令处理。
	_ = exec.Command("open", systemPrefsURL).Run()
}

// --- 光标隐藏（捕获输入模式用） ---
//
// CGDisplayHideCursor/ShowCursor 是应用级计数：隐藏几次就要显示几次。
// 这里用计数器兜底，ShowCursorAll 用于失焦/异常路径的强制恢复。

var cursorMu sync.Mutex
var cursorHideCount int

// HideCursor 隐藏本机光标。不需要辅助功能权限。
func HideCursor() {
	if load() != nil {
		return
	}
	purego.SyscallN(syms.cgDisplayHideCursor, 0) // kCGDirectMainDisplay = 0
	cursorMu.Lock()
	cursorHideCount++
	cursorMu.Unlock()
}

// ShowCursor 恢复一次隐藏；计数为零时是 no-op。
func ShowCursor() {
	cursorMu.Lock()
	if cursorHideCount == 0 {
		cursorMu.Unlock()
		return
	}
	cursorHideCount--
	cursorMu.Unlock()
	if load() != nil {
		return
	}
	purego.SyscallN(syms.cgDisplayShowCursor, 0)
}

// ShowCursorAll 强制恢复光标（把欠的计数全部还清）。
func ShowCursorAll() {
	for {
		cursorMu.Lock()
		if cursorHideCount == 0 {
			cursorMu.Unlock()
			return
		}
		cursorHideCount--
		cursorMu.Unlock()
		if load() == nil {
			purego.SyscallN(syms.cgDisplayShowCursor, 0)
		}
	}
}

// Manager 管理一个 CGEventTap 的生命周期。
// 回调在专用的 runloop goroutine 中调用，调用方负责同步。
type Manager struct {
	mu      sync.Mutex
	running bool

	cb     func(Event) bool // 仅 runloop goroutine 在运行期读取
	tap    uintptr          // CGEventTap（CFMachPort）
	rl     uintptr          // runloop goroutine 的 CFRunLoop
	handle uintptr          // 回调 userInfo 里回填的 *Manager 引用（见 managerHandles）
	stop   chan struct{}    // 关闭即请求 runloop 退出

	ready chan error    // runloop goroutine 完成启动时给出结果
	done  chan struct{} // runloop goroutine 退出时关闭

	tstate translateState // 仅 runloop goroutine 访问
}

// managerHandles 把 *Manager 暴露给 C 回调（userInfo 指针）。
// purego 无 cgo.Handle 等价物，这里用一个小的进程内注册表，
// key 从 1 递增（0 保留给 nil）。
var (
	managerHandlesMu  sync.Mutex
	managerHandles            = map[uintptr]*Manager{}
	nextManagerHandle uintptr = 1
)

func registerManager(m *Manager) uintptr {
	managerHandlesMu.Lock()
	defer managerHandlesMu.Unlock()
	h := nextManagerHandle
	nextManagerHandle++
	managerHandles[h] = m
	return h
}

func lookupManager(h uintptr) *Manager {
	managerHandlesMu.Lock()
	defer managerHandlesMu.Unlock()
	return managerHandles[h]
}

func unregisterManager(h uintptr) {
	managerHandlesMu.Lock()
	defer managerHandlesMu.Unlock()
	delete(managerHandles, h)
}

// tapCallback 是 CGEventTap 的 C 回调（purego 注册的 trampoline）。
// 签名对应 CGEventTapCallBack：
//
//	CGEventRef callback(CGEventTapProxy proxy, CGEventType type,
//	                    CGEventRef event, void *userInfo)
var tapCallback = purego.NewCallback(func(proxy, eventType, event, userInfo uintptr) uintptr {
	m := lookupManager(userInfo)
	if m == nil {
		return event
	}
	t := uint64(eventType)
	if t == evTapDisabledByTimeout {
		// 事件处理超时被系统禁用：自动恢复。
		purego.SyscallN(syms.cgEventTapEnable, m.tap, 1)
		return 0
	}
	if t == evTapDisabledByUserInput {
		// 用户（或系统）主动禁用（如在设置里取消勾选）：不恢复，由 Stop 清理。
		return 0
	}
	if event == 0 {
		return 0
	}
	keycode, _, _ := purego.SyscallN(syms.cgEventGetIntegerValueField, event, uintptr(fieldKeyboardEventKeycode))
	flags, _, _ := purego.SyscallN(syms.cgEventGetFlags, event)
	var autorepeat bool
	if t == evKeyDown {
		rep, _, _ := purego.SyscallN(syms.cgEventGetIntegerValueField, event, uintptr(fieldKeyboardEventAutorepeat))
		autorepeat = rep != 0
	}
	if m.handleEvent(t, uint32(keycode), uint64(flags), autorepeat) {
		return 0 // 吞掉：返回 NULL
	}
	return event // 放过：原样返回
})

// handleEvent 是 tap 回调里的纯决策部分（不含系统调用，可单测）：
// 翻译事件并调用用户回调，返回 true 表示应吞掉该事件。
func (m *Manager) handleEvent(eventType uint64, keycode uint32, flags uint64, autorepeat bool) bool {
	ev, ok := translate(&m.tstate, eventType, keycode, flags, autorepeat)
	if !ok || m.cb == nil {
		return false
	}
	return m.cb(ev)
}

// Start 安装事件 tap 并启动专用 runloop goroutine。
// cb 在 runloop goroutine 中调用；返回 true 表示吞掉该事件，
// false 原样放过。重复调用会先 Stop 再重新安装。
//
// 无辅助功能权限或 tap 创建失败时返回 error。
// 注意：授权后通常需要重启应用，tap 才开始收到事件。
func (m *Manager) Start(cb func(Event) (swallow bool)) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		m.Stop()
		m.mu.Lock()
	}
	defer m.mu.Unlock()

	if err := load(); err != nil {
		return err
	}
	if !availableNoLock() {
		return errors.New("grab: 缺少辅助功能权限（Accessibility）；请在 系统设置 > 隐私与安全性 > 辅助功能 中勾选本应用并重启应用（可用 grab.OpenSystemPrefs() 打开该面板）")
	}

	mask := eventMaskBit(evKeyDown) | eventMaskBit(evKeyUp) | eventMaskBit(evFlagsChanged)
	m.cb = cb
	m.ready = make(chan error, 1)
	m.done = make(chan struct{})
	m.stop = make(chan struct{})
	m.rl = 0
	m.handle = registerManager(m)

	// CGEventTapCreate(kCGHIDEventTap, kCGHeadInsertEventTap,
	//                  kCGEventTapOptionDefault, mask, tapCallback, handle)
	tap, _, _ := purego.SyscallN(syms.cgEventTapCreate,
		0, // kCGHIDEventTap：接收系统级 HID 事件（能拦 Cmd+Tab 等）
		0, // kCGHeadInsertEventTap：插在事件流最前
		0, // kCGEventTapOptionDefault：不监听仅监听位；要吞事件就不能用 ListenOnly
		uintptr(mask),
		tapCallback,
		uintptr(m.handle),
	)
	if tap == 0 {
		unregisterManager(m.handle)
		m.handle = 0
		return errors.New("grab: CGEventTapCreate 返回 nil（辅助功能权限未授予，或被系统禁用）")
	}
	m.tap = tap

	go m.runLoop(tap)
	// 持有 mu 等待 goroutine 把 source 挂到 runloop，保证 Stop 之后
	// 调用时 runloop 已就绪（goroutine 不访问 mu，无死锁）。
	if err := <-m.ready; err != nil {
		<-m.done
		m.running = false
		return err
	}
	m.running = true
	return nil
}

// runLoop 在专用 goroutine 里跑一个 CFRunLoop 接收 tap 事件。
//
// 注意：这里用 CFRunLoopRunInMode 带超时的轮询，而不是 CFRunLoopRun +
// CFRunLoopStop 组合——实测在纯 Go 创建的线程上 CFRunLoopRun 的稳定等待
// 路径（arm64e 库内部代码）会崩溃（SIGSEGV，跳转到带 PAC 标记的地址），
// 而 RunInMode 的超时路径在本机验证稳定。轮询间隔 50ms，Stop 延迟同量级。
func (m *Manager) runLoop(tap uintptr) {
	defer close(m.done)

	rl, _, _ := purego.SyscallN(syms.cfRunLoopGetCurrent)
	src, _, _ := purego.SyscallN(syms.cfMachPortCreateRunLoopSource, 0, tap, 0)
	if rl == 0 || src == 0 {
		if src != 0 {
			purego.SyscallN(syms.cfRelease, src)
		}
		if rl != 0 {
			purego.SyscallN(syms.cfRelease, tap)
		}
		m.ready <- errors.New("grab: 创建 CFRunLoop source 失败")
		return
	}
	purego.SyscallN(syms.cfRunLoopAddSource, rl, src, syms.kCFRunLoopCommonModes)
	// source 就位后再启用 tap。
	purego.SyscallN(syms.cgEventTapEnable, tap, 1)

	m.rl = rl
	m.ready <- nil

	// kCFRunLoopRunTimedOut = 3；pollIntervalNs 见下。
	poll := uint64(50 * 1000 * 1000)
	for {
		select {
		case <-m.stop:
			// 清理：禁用 tap，释放 source 与 tap。
			purego.SyscallN(syms.cgEventTapEnable, tap, 0)
			purego.SyscallN(syms.cfRelease, src)
			purego.SyscallN(syms.cfRelease, tap)
			return
		default:
		}
		purego.SyscallN(syms.cfRunLoopRunInMode, syms.kCFRunLoopDefaultMode, uintptr(poll), 0)
	}
}

// Stop 停止 tap 并释放相关资源；不在运行时是 no-op。
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	close(m.stop)
	done := m.done
	m.mu.Unlock()

	<-done

	m.mu.Lock()
	m.running = false
	m.cb = nil
	m.tap = 0
	m.rl = 0
	h := m.handle
	m.handle = 0
	m.stop = nil
	m.mu.Unlock()
	if h != 0 {
		unregisterManager(h)
	}
}

// Running 报告当前是否工作中。
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// availableNoLock 供已持有语义路径内部复用；不加锁，因为底层无共享状态。
func availableNoLock() bool {
	r, _, _ := purego.SyscallN(syms.axIsProcessTrustedWithOptions, 0)
	return r != 0
}
