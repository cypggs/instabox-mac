// Package keys 把物理键码（USB HID usage）+ 修饰键状态翻译成 X11/RFB keysym 事件流。
// 核心原则：永远基于物理键码映射，不看字符（Option 键/输入法会改变字符，但物理键位不变）。
package keys

// 常用 X11 keysym 常量，供其他包引用。
const (
	KeysymBackSpace = 0xFF08
	KeysymTab       = 0xFF09
	KeysymReturn    = 0xFF0D
	KeysymEscape    = 0xFF1B
	KeysymDelete    = 0xFFFF
	KeysymShiftL    = 0xFFE1
	KeysymShiftR    = 0xFFE2
	KeysymControlL  = 0xFFE3
	KeysymControlR  = 0xFFE4
	KeysymCapsLock  = 0xFFE5
	KeysymAltL      = 0xFFE9
	KeysymAltR      = 0xFFEA
	KeysymSuperL    = 0xFFEB
	KeysymSuperR    = 0xFFEC
)

// keymapEntry 描述一个 HID usage 的两档 keysym（base 档与 shifted 档）。
// shifted 为 0 表示该键不受 Shift 影响（功能键、小键盘等）。
type keymapEntry struct {
	base    uint32
	shifted uint32
}

// keymap 主键盘区映射表（USB HID usage 0x04-0x68）。
var keymap = map[uint32]keymapEntry{
	0x04: {'a', 'A'},
	0x05: {'b', 'B'},
	0x06: {'c', 'C'},
	0x07: {'d', 'D'},
	0x08: {'e', 'E'},
	0x09: {'f', 'F'},
	0x0A: {'g', 'G'},
	0x0B: {'h', 'H'},
	0x0C: {'i', 'I'},
	0x0D: {'j', 'J'},
	0x0E: {'k', 'K'},
	0x0F: {'l', 'L'},
	0x10: {'m', 'M'},
	0x11: {'n', 'N'},
	0x12: {'o', 'O'},
	0x13: {'p', 'P'},
	0x14: {'q', 'Q'},
	0x15: {'r', 'R'},
	0x16: {'s', 'S'},
	0x17: {'t', 'T'},
	0x18: {'u', 'U'},
	0x19: {'v', 'V'},
	0x1A: {'w', 'W'},
	0x1B: {'x', 'X'},
	0x1C: {'y', 'Y'},
	0x1D: {'z', 'Z'},
	0x1E: {'1', '!'},
	0x1F: {'2', '@'},
	0x20: {'3', '#'},
	0x21: {'4', '$'},
	0x22: {'5', '%'},
	0x23: {'6', '^'},
	0x24: {'7', '&'},
	0x25: {'8', '*'},
	0x26: {'9', '('},
	0x27: {'0', ')'},
	0x28: {KeysymReturn, 0},
	0x29: {KeysymEscape, 0},
	0x2A: {KeysymBackSpace, 0}, // Mac Delete 键（向后删除）
	0x2B: {KeysymTab, 0},
	0x2C: {' ', 0},
	0x2D: {'-', '_'},
	0x2E: {'=', '+'},
	0x2F: {'[', '{'},
	0x30: {']', '}'},
	0x31: {'\\', '|'},
	0x33: {';', ':'},
	0x34: {'\'', '"'},
	0x35: {'`', '~'},
	0x36: {',', '<'},
	0x37: {'.', '>'},
	0x38: {'/', '?'},
	0x39: {KeysymCapsLock, 0},
	0x3A: {0xFFBE, 0}, // F1
	0x3B: {0xFFBF, 0}, // F2
	0x3C: {0xFFC0, 0}, // F3
	0x3D: {0xFFC1, 0}, // F4
	0x3E: {0xFFC2, 0}, // F5
	0x3F: {0xFFC3, 0}, // F6
	0x40: {0xFFC4, 0}, // F7
	0x41: {0xFFC5, 0}, // F8
	0x42: {0xFFC6, 0}, // F9
	0x43: {0xFFC7, 0}, // F10
	0x44: {0xFFC8, 0}, // F11
	0x45: {0xFFC9, 0}, // F12
	0x46: {0xFF61, 0}, // PrintScreen (Sys_Req)
	0x47: {0xFF14, 0}, // ScrollLock
	0x48: {0xFF13, 0}, // Pause
	0x49: {0xFF63, 0}, // Insert
	0x4A: {0xFF50, 0}, // Home
	0x4B: {0xFF55, 0}, // PageUp
	0x4C: {KeysymDelete, 0}, // Delete(Forward)
	0x4D: {0xFF57, 0},       // End
	0x4E: {0xFF56, 0},       // PageDown
	0x4F: {0xFF53, 0},       // Right
	0x50: {0xFF51, 0},       // Left
	0x51: {0xFF54, 0},       // Down
	0x52: {0xFF52, 0},       // Up
	0x53: {0xFF7F, 0},       // NumLock
	0x54: {0xFFAF, 0},       // KP_Divide
	0x55: {0xFFAA, 0},       // KP_Multiply
	0x56: {0xFFAD, 0},       // KP_Subtract
	0x57: {0xFFAB, 0},       // KP_Add
	0x58: {0xFF8D, 0},       // KP_Enter
	0x59: {0xFFB1, 0},       // KP_1
	0x5A: {0xFFB2, 0},       // KP_2
	0x5B: {0xFFB3, 0},       // KP_3
	0x5C: {0xFFB4, 0},       // KP_4
	0x5D: {0xFFB5, 0},       // KP_5
	0x5E: {0xFFB6, 0},       // KP_6
	0x5F: {0xFFB7, 0},       // KP_7
	0x60: {0xFFB8, 0},       // KP_8
	0x61: {0xFFB9, 0},       // KP_9
	0x62: {0xFFB0, 0},       // KP_0
	0x63: {0xFFAE, 0},       // KP_Decimal
	0x64: {'\\', 0},         // 非 US \ 和 # (ISO)，同 0x31
	0x65: {0xFF7D, 0},       // Application
	0x68: {'<', 0},          // 非 US 反斜杠，JIS 布局占位
}

// KeysymForHID 返回 HID usage 对应的 X11 keysym（base 档）。
// 8 个修饰键（0xE0-0xE7）及未知 usage 返回 ok=false。
func KeysymForHID(hid uint32) (keysym uint32, ok bool) {
	e, ok := keymap[hid]
	if !ok {
		return 0, false
	}
	return e.base, true
}

// KeysymForHIDWithShift 返回 HID usage 对应的 keysym；shifted 为 true 时取上档
// （字母变大写，数字/标点变第二档；小键盘、F 键、方向等功能键忽略 shift）。
func KeysymForHIDWithShift(hid uint32, shifted bool) (keysym uint32, ok bool) {
	e, ok := keymap[hid]
	if !ok {
		return 0, false
	}
	if shifted && e.shifted != 0 {
		return e.shifted, true
	}
	return e.base, true
}
