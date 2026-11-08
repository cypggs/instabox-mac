//go:build !darwin

package grab

// 非 darwin 平台的占位实现：API 与 darwin 版一致，但全部报告"不支持"，
// 保证跨平台可编译。键盘捕获依赖 macOS 的 CGEventTap，无其他平台实现。

// Manager 在非 macOS 平台上不可用。
type Manager struct{}

// Start 始终返回 ErrUnsupported。
func (m *Manager) Start(cb func(Event) (swallow bool)) error {
	return ErrUnsupported
}

// Stop 是 no-op。
func (m *Manager) Stop() {}

// Running 恒为 false。
func (m *Manager) Running() bool { return false }

// Available 恒为 false。
func Available() bool { return false }

// RequestAccess 恒为 false，不弹任何对话框。
func RequestAccess() bool { return false }

// OpenSystemPrefs 是 no-op。
func OpenSystemPrefs() {}

// HideCursor/ShowCursor/ShowCursorAll 是 no-op。
func HideCursor()          {}
func ShowCursor()         {}
func ShowCursorAll()      {}
