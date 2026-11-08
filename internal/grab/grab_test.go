package grab

import "testing"

// TestHIDForVK 全表 spot check：覆盖字母、数字、符号、主控制键、修饰键
// （含左右）、F1-F20、编辑/导航键、小键盘、ISO/JIS 键，共 60+ 项。
func TestHIDForVK(t *testing.T) {
	cases := []struct {
		vk  uint32
		hid uint32
	}{
		// 字母（ANSI 键序）
		{0x00, 0x04}, // A
		{0x01, 0x16}, // S
		{0x02, 0x07}, // D
		{0x03, 0x09}, // F
		{0x04, 0x0B}, // H
		{0x05, 0x0A}, // G
		{0x06, 0x1D}, // Z
		{0x07, 0x1B}, // X
		{0x08, 0x06}, // C
		{0x09, 0x19}, // V
		{0x0B, 0x05}, // B
		{0x0C, 0x14}, // Q
		{0x0D, 0x1A}, // W
		{0x0E, 0x08}, // E
		{0x0F, 0x15}, // R
		{0x10, 0x1C}, // Y
		{0x11, 0x17}, // T
		{0x1F, 0x12}, // O
		{0x20, 0x18}, // U
		{0x22, 0x0C}, // I
		{0x23, 0x13}, // P
		{0x25, 0x0F}, // L
		{0x26, 0x0D}, // J
		{0x28, 0x0E}, // K
		{0x2D, 0x11}, // N
		{0x2E, 0x10}, // M

		// 数字行
		{0x12, 0x1E}, // 1
		{0x13, 0x1F}, // 2
		{0x14, 0x20}, // 3
		{0x15, 0x21}, // 4
		{0x17, 0x22}, // 5
		{0x16, 0x23}, // 6
		{0x1A, 0x24}, // 7
		{0x1C, 0x25}, // 8
		{0x19, 0x26}, // 9
		{0x1D, 0x27}, // 0

		// 符号
		{0x1B, 0x2D}, // -
		{0x18, 0x2E}, // =
		{0x21, 0x2F}, // [
		{0x1E, 0x30}, // ]
		{0x2A, 0x31}, // \ ANSI
		{0x29, 0x33}, // ;
		{0x27, 0x34}, // '
		{0x32, 0x35}, // `
		{0x2B, 0x36}, // ,
		{0x2F, 0x37}, // .
		{0x2C, 0x38}, // /

		// 主控制键
		{0x24, 0x28}, // Return
		{0x30, 0x2B}, // Tab
		{0x31, 0x2C}, // Space
		{0x33, 0x2A}, // Backspace
		{0x35, 0x29}, // Escape
		{0x39, 0x39}, // CapsLock

		// 修饰键（左右）
		{0x37, 0xE3}, // Cmd Left
		{0x36, 0xE7}, // Cmd Right
		{0x38, 0xE1}, // Shift Left
		{0x3C, 0xE5}, // Shift Right
		{0x3A, 0xE2}, // Option Left
		{0x3D, 0xE6}, // Option Right
		{0x3B, 0xE0}, // Control Left
		{0x3E, 0xE4}, // Control Right

		// F1-F12（macOS kVK 顺序乱）
		{0x7A, 0x3A}, // F1
		{0x78, 0x3B}, // F2
		{0x63, 0x3C}, // F3
		{0x76, 0x3D}, // F4
		{0x60, 0x3E}, // F5
		{0x61, 0x3F}, // F6
		{0x62, 0x40}, // F7
		{0x64, 0x41}, // F8
		{0x65, 0x42}, // F9
		{0x6D, 0x43}, // F10
		{0x67, 0x44}, // F11
		{0x6F, 0x45}, // F12

		// 编辑/导航
		{0x72, 0x49}, // Insert
		{0x73, 0x4A}, // Home
		{0x74, 0x4B}, // PageUp
		{0x75, 0x4C}, // Delete Forward
		{0x77, 0x4D}, // End
		{0x79, 0x4E}, // PageDown
		{0x7B, 0x50}, // Left
		{0x7C, 0x4F}, // Right
		{0x7D, 0x51}, // Down
		{0x7E, 0x52}, // Up

		// 小键盘
		{0x47, 0x53}, // Clear/NumLock
		{0x4B, 0x54}, // KP /
		{0x43, 0x55}, // KP *
		{0x4E, 0x56}, // KP -
		{0x45, 0x57}, // KP +
		{0x4C, 0x58}, // KP Enter
		{0x53, 0x59}, // KP 1
		{0x54, 0x5A}, // KP 2
		{0x55, 0x5B}, // KP 3
		{0x56, 0x5C}, // KP 4
		{0x57, 0x5D}, // KP 5
		{0x58, 0x5E}, // KP 6
		{0x59, 0x5F}, // KP 7
		{0x5B, 0x60}, // KP 8
		{0x5C, 0x61}, // KP 9
		{0x52, 0x62}, // KP 0
		{0x41, 0x63}, // KP .

		// ISO/JIS
		{0x0A, 0x64}, // ISO § (Non-US \)
		{0x68, 0x88}, // JIS Kana
	}
	for _, c := range cases {
		hid, ok := HIDForVK(c.vk)
		if !ok {
			t.Errorf("HIDForVK(0x%02X): ok=false, want 0x%02X", c.vk, c.hid)
			continue
		}
		if hid != c.hid {
			t.Errorf("HIDForVK(0x%02X) = 0x%02X, want 0x%02X", c.vk, hid, c.hid)
		}
	}
}

// TestHIDForVKUnknown 查不到的 keycode 必须返回 ok=false。
func TestHIDForVKUnknown(t *testing.T) {
	unknown := []uint32{
		0x3F,       // Fn（kVK_Function，无 HID usage）
		0x48,       // 音量上（consumer page，非 0x07）
		0x49,       // 音量下
		0x4A,       // 静音
		0x51,       // 小键盘 =（无可靠 HID usage）
		0x7F, 0x80, // 超出表范围
		0xFF,
	}
	for _, vk := range unknown {
		if hid, ok := HIDForVK(vk); ok {
			t.Errorf("HIDForVK(0x%02X) = 0x%02X, want ok=false", vk, hid)
		}
	}
}

// TestModifierDown 修饰键 flags 比较逻辑。
func TestModifierDown(t *testing.T) {
	const shift = FlagMaskShift
	cases := []struct {
		name        string
		prev, cur   uint64
		mask        uint64
		wantDown    bool
		wantChanged bool
	}{
		{"按下", 0, shift, shift, true, true},
		{"按住未变", shift, shift, shift, true, false},
		{"松开", shift, 0, shift, false, true},
		{"未变且未按", 0, 0, shift, false, false},
		{"他键变化不影响", FlagMaskControl, FlagMaskControl | shift, shift, true, true},
		{"他键变化掩码无关", FlagMaskControl, FlagMaskControl | shift, FlagMaskControl, true, false},
		{"Cmd 位", 0, FlagMaskCommand, FlagMaskCommand, true, true},
	}
	for _, c := range cases {
		down, changed := modifierDown(c.prev, c.cur, c.mask)
		if down != c.wantDown || changed != c.wantChanged {
			t.Errorf("%s: modifierDown(0x%X, 0x%X, 0x%X) = (%v, %v), want (%v, %v)",
				c.name, c.prev, c.cur, c.mask, down, changed, c.wantDown, c.wantChanged)
		}
	}
}

// TestTranslateKeyDownUp 普通键的 down/up 翻译。
func TestTranslateKeyDownUp(t *testing.T) {
	var st translateState

	ev, ok := translate(&st, evKeyDown, 0x00, FlagMaskShift, false) // A + Shift
	if !ok {
		t.Fatal("keydown A: ok=false")
	}
	if ev.HID != 0x04 || !ev.Down || ev.Repeat || ev.Mods != FlagMaskShift {
		t.Errorf("keydown A: got %+v", ev)
	}

	ev, ok = translate(&st, evKeyDown, 0x00, FlagMaskShift, true) // A 自动重复
	if !ok || !ev.Repeat || !ev.Down {
		t.Errorf("keydown A repeat: got %+v, ok=%v", ev, ok)
	}

	ev, ok = translate(&st, evKeyUp, 0x00, 0, false)
	if !ok || ev.Down || ev.HID != 0x04 {
		t.Errorf("keyup A: got %+v, ok=%v", ev, ok)
	}

	// 未知 keycode（Fn）：不拦截
	if _, ok = translate(&st, evKeyDown, 0x3F, 0, false); ok {
		t.Error("keydown Fn: ok=true, want false（放过）")
	}
}

// TestTranslateFlagsChanged 修饰键事件的按下/松开判定与左右区分。
// 语义：同一 keycode 的 flagsChanged 事件按"按一次、松一次"交替
// （因为同侧修饰键都按住时 flags 位不变化，只能靠逐 keycode 计数）。
func TestTranslateFlagsChanged(t *testing.T) {
	var st translateState

	// 左 Shift 按下（首次见到该键：以 flags 位为准）
	ev, ok := translate(&st, evFlagsChanged, 0x38, FlagMaskShift, false)
	if !ok || !ev.Down || ev.HID != 0xE1 || ev.Mods != FlagMaskShift {
		t.Errorf("shift down: got %+v, ok=%v", ev, ok)
	}

	// 左 Shift 松开
	ev, ok = translate(&st, evFlagsChanged, 0x38, 0, false)
	if !ok || ev.Down || ev.HID != 0xE1 {
		t.Errorf("shift up: got %+v, ok=%v", ev, ok)
	}

	// 左右 Shift 都按住的情形：flags 位在右 Shift 动作时不变，
	// 但 keycode 不同，各自交替。
	ev, ok = translate(&st, evFlagsChanged, 0x38, FlagMaskShift, false) // 左 Shift 按下
	if !ok || !ev.Down || ev.HID != 0xE1 {
		t.Errorf("left shift down: got %+v, ok=%v", ev, ok)
	}
	ev, ok = translate(&st, evFlagsChanged, 0x3C, FlagMaskShift, false) // 右 Shift 按下（flags 相同！）
	if !ok || !ev.Down || ev.HID != 0xE5 {
		t.Errorf("right shift down: got %+v, ok=%v", ev, ok)
	}
	ev, ok = translate(&st, evFlagsChanged, 0x38, FlagMaskShift, false) // 左 Shift 松开（flags 仍置位）
	if !ok || ev.Down || ev.HID != 0xE1 {
		t.Errorf("left shift up (right still held): got %+v, ok=%v", ev, ok)
	}
	ev, ok = translate(&st, evFlagsChanged, 0x3C, 0, false) // 右 Shift 松开
	if !ok || ev.Down || ev.HID != 0xE5 {
		t.Errorf("right shift up: got %+v, ok=%v", ev, ok)
	}

	// Cmd 按下（keycode 0x37 → 左 Cmd 0xE3）
	ev, ok = translate(&st, evFlagsChanged, 0x37, FlagMaskCommand, false)
	if !ok || !ev.Down || ev.HID != 0xE3 {
		t.Errorf("cmd down: got %+v, ok=%v", ev, ok)
	}
	ev, ok = translate(&st, evFlagsChanged, 0x37, 0, false)
	if !ok || ev.Down {
		t.Errorf("cmd up: got %+v, ok=%v", ev, ok)
	}

	// CapsLock
	ev, ok = translate(&st, evFlagsChanged, 0x39, FlagMaskAlphaShift, false)
	if !ok || !ev.Down || ev.HID != 0x39 {
		t.Errorf("caps down: got %+v, ok=%v", ev, ok)
	}
}

// TestTranslateFlagsChangedUnknown 非修饰键 keycode 的 flagsChanged 放过。
func TestTranslateFlagsChangedUnknown(t *testing.T) {
	var st translateState
	if _, ok := translate(&st, evFlagsChanged, 0x00, FlagMaskShift, false); ok {
		t.Error("flagsChanged for non-modifier keycode: ok=true")
	}
}

// TestHandleEvent 回调吞掉语义。
func TestHandleEvent(t *testing.T) {
	var got []Event
	m := &Manager{cb: func(ev Event) bool {
		got = append(got, ev)
		return ev.HID == 0x04 // 吞掉 A，其他放过
	}}

	// A down：翻译 + 吞掉
	if !m.handleEvent(evKeyDown, 0x00, 0, false) {
		t.Error("A down should be swallowed")
	}
	// B down：翻译 + 放过
	if m.handleEvent(evKeyDown, 0x0B, 0, false) {
		t.Error("B down should pass through")
	}
	// Fn down：未知 keycode，不调用回调，放过
	if m.handleEvent(evKeyDown, 0x3F, 0, false) {
		t.Error("Fn down should pass through")
	}
	// 无回调：全部放过
	nocb := &Manager{}
	if nocb.handleEvent(evKeyDown, 0x00, 0, false) {
		t.Error("nil callback should pass through")
	}

	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].HID != 0x04 || got[1].HID != 0x05 {
		t.Errorf("events: %+v", got)
	}
}

// TestEventMaskBit 对应 CGEventMaskBit 头文件宏。
func TestEventMaskBit(t *testing.T) {
	if eventMaskBit(10) != 1<<10 || eventMaskBit(12) != 1<<12 {
		t.Error("eventMaskBit wrong")
	}
}
