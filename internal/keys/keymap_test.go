package keys

import "testing"

func TestKeysymForHIDTable(t *testing.T) {
	cases := []struct {
		hid    uint32
		keysym uint32
	}{
		{0x04, 'a'},
		{0x1D, 'z'},
		{0x1E, '1'},
		{0x26, '9'},
		{0x27, '0'},
		{0x28, KeysymReturn},
		{0x29, KeysymEscape},
		{0x2A, KeysymBackSpace},
		{0x2B, KeysymTab},
		{0x2C, ' '},
		{0x39, KeysymCapsLock},
		{0x3A, 0xFFBE}, // F1
		{0x45, 0xFFC9}, // F12
		{0x46, 0xFF61}, // PrintScreen
		{0x47, 0xFF14}, // ScrollLock
		{0x48, 0xFF13}, // Pause
		{0x49, 0xFF63}, // Insert
		{0x4A, 0xFF50}, // Home
		{0x4B, 0xFF55}, // PageUp
		{0x4C, KeysymDelete},
		{0x4D, 0xFF57}, // End
		{0x4E, 0xFF56}, // PageDown
		{0x4F, 0xFF53}, // Right
		{0x50, 0xFF51}, // Left
		{0x51, 0xFF54}, // Down
		{0x52, 0xFF52}, // Up
		{0x53, 0xFF7F}, // NumLock
		{0x54, 0xFFAF}, // KP_Divide
		{0x55, 0xFFAA}, // KP_Multiply
		{0x56, 0xFFAD}, // KP_Subtract
		{0x57, 0xFFAB}, // KP_Add
		{0x58, 0xFF8D}, // KP_Enter
		{0x59, 0xFFB1}, // KP_1
		{0x61, 0xFFB9}, // KP_9
		{0x62, 0xFFB0}, // KP_0
		{0x63, 0xFFAE}, // KP_Decimal
		{0x64, '\\'},
		{0x65, 0xFF7D}, // Application
		{0x68, '<'},
	}
	for _, c := range cases {
		ks, ok := KeysymForHID(c.hid)
		if !ok || ks != c.keysym {
			t.Errorf("KeysymForHID(0x%02X) = 0x%X, %v; 想要 0x%X, true", c.hid, ks, ok, c.keysym)
		}
	}
}

func TestKeysymForHIDUnknownAndModifiers(t *testing.T) {
	// 8 个修饰键 0xE0-0xE7 由 modifiers 处理，这里必须返回 ok=false
	for hid := uint32(0xE0); hid <= 0xE7; hid++ {
		if _, ok := KeysymForHID(hid); ok {
			t.Errorf("KeysymForHID(0x%02X) 修饰键应返回 ok=false", hid)
		}
	}
	// 未知/未映射值
	for _, hid := range []uint32{0x00, 0x03, 0x32, 0x67, 0x69, 0x9F, 0x100, 0xFFFFFFFF} {
		if _, ok := KeysymForHID(hid); ok {
			t.Errorf("KeysymForHID(0x%X) 应返回 ok=false", hid)
		}
	}
}

func TestKeysymForHIDWithShiftLetters(t *testing.T) {
	// 字母：shifted → 大写
	for hid := uint32(0x04); hid <= 0x1D; hid++ {
		base, _ := KeysymForHID(hid)
		shifted, ok := KeysymForHIDWithShift(hid, true)
		if !ok || shifted != base-('a'-'A') {
			t.Errorf("KeysymForHIDWithShift(0x%02X, true) = 0x%X; 想要大写 0x%X", hid, shifted, base-32)
		}
		// 两档调用 base 必须一致
		again, _ := KeysymForHIDWithShift(hid, false)
		if again != base {
			t.Errorf("KeysymForHIDWithShift(0x%02X, false) = 0x%X; 想要 0x%X", hid, again, base)
		}
	}
}

func TestKeysymForHIDWithShiftPunctuation(t *testing.T) {
	cases := []struct {
		hid        uint32
		base, up   uint32
	}{
		{0x2D, '-', '_'},
		{0x2E, '=', '+'},
		{0x2F, '[', '{'},
		{0x30, ']', '}'},
		{0x31, '\\', '|'},
		{0x33, ';', ':'},
		{0x34, '\'', '"'},
		{0x35, '`', '~'},
		{0x36, ',', '<'},
		{0x37, '.', '>'},
		{0x38, '/', '?'},
	}
	for _, c := range cases {
		b, ok1 := KeysymForHIDWithShift(c.hid, false)
		u, ok2 := KeysymForHIDWithShift(c.hid, true)
		if !ok1 || !ok2 || b != c.base || u != c.up {
			t.Errorf("标点 0x%02X: got base=0x%X up=0x%X; 想要 base=0x%X up=0x%X", c.hid, b, u, c.base, c.up)
		}
	}
	// 数字上档
	digits := map[uint32]uint32{
		0x1E: '!', 0x1F: '@', 0x20: '#', 0x21: '$', 0x22: '%',
		0x23: '^', 0x24: '&', 0x25: '*', 0x26: '(', 0x27: ')',
	}
	for hid, up := range digits {
		got, _ := KeysymForHIDWithShift(hid, true)
		if got != up {
			t.Errorf("数字 0x%02X shifted = 0x%X; 想要 0x%X", hid, got, up)
		}
	}
}

func TestKeysymForHIDWithShiftIgnoresShift(t *testing.T) {
	// 功能键、小键盘、方向键等：两档相同
	for _, hid := range []uint32{0x28, 0x39, 0x3A, 0x45, 0x52, 0x58, 0x59, 0x62, 0x63, 0x4C} {
		b, _ := KeysymForHIDWithShift(hid, false)
		u, ok := KeysymForHIDWithShift(hid, true)
		if !ok || u != b {
			t.Errorf("0x%02X: shifted 档应为 0x%X，得到 0x%X", hid, b, u)
		}
	}
}
