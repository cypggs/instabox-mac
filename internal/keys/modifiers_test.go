package keys

import (
	"reflect"
	"testing"
)

func newTranslator(t *testing.T, m Mapping) *Translator {
	t.Helper()
	tr, err := NewTranslator(m)
	if err != nil {
		t.Fatalf("NewTranslator(%+v) 失败: %v", m, err)
	}
	return tr
}

func assertEvents(t *testing.T, got []Event, want []Event) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("事件序列不符:\n got=%v\nwant=%v", got, want)
	}
}

// 修饰键不影响普通键 keysym（除 Shift 外）。
func TestOptionDoesNotChangeKeysym(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	got := tr.KeyChange(0x06 /*c*/, true, SideNone, ModAlt, false)
	assertEvents(t, got, []Event{{Keysym: 'c', Down: true}})
	tr.KeyChange(0x06, false, SideNone, ModAlt, false)

	// Ctrl、Super、组合也不影响
	got = tr.KeyChange(0x06, true, SideNone, ModCtrl|ModSuper|ModAlt, false)
	assertEvents(t, got, []Event{{Keysym: 'c', Down: true}})
}

func TestShiftChangesKeysym(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	// Shift+c → 'C'
	got := tr.KeyChange(0x06 /*c*/, true, SideNone, ModShift, false)
	assertEvents(t, got, []Event{{Keysym: 'C', Down: true}})

	// Shift+1 → '!'
	got = tr.KeyChange(0x1E /*1*/, true, SideNone, ModShift, false)
	assertEvents(t, got, []Event{{Keysym: '!', Down: true}})

	// 无 Shift → 小写/数字
	got = tr.KeyChange(0x06, true, SideNone, 0, false)
	assertEvents(t, got, []Event{{Keysym: 'c', Down: true}})
	got = tr.KeyChange(0x1E, true, SideNone, 0, false)
	assertEvents(t, got, []Event{{Keysym: '1', Down: true}})
}

// CapsLock 作为普通锁定键直接发 KeysymCapsLock down/up，不做 shift 变换。
func TestCapsLockIsNormalKey(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	got := tr.KeyChange(0x39, true, SideNone, ModShift, false)
	assertEvents(t, got, []Event{{Keysym: KeysymCapsLock, Down: true}})
	got = tr.KeyChange(0x39, false, SideNone, 0, false)
	assertEvents(t, got, []Event{{Keysym: KeysymCapsLock, Down: false}})
	if n := tr.PressedCount(); n != 0 {
		t.Errorf("PressedCount() = %d; 想要 0", n)
	}
}

func TestModifierLeftCommand(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	got := tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false)
	assertEvents(t, got, []Event{{Keysym: KeysymSuperL, Down: true}})

	got = tr.KeyChange(0xE3, false, SideSuperLeft, 0, false)
	assertEvents(t, got, []Event{{Keysym: KeysymSuperL, Down: false}})
}

// 左右 Cmd 都映射 Super：先按的生效，不重复发 down；全部松开才发 up。
func TestModifierBothCommandKeys(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	assertEvents(t, tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false),
		[]Event{{Keysym: KeysymSuperL, Down: true}})
	assertEvents(t, tr.KeyChange(0xE7, true, SideSuperRight, ModSuper, false), nil)

	// 松开一个：不发 up
	assertEvents(t, tr.KeyChange(0xE3, false, SideSuperLeft, ModSuper, false), nil)
	// 两个都松：发 up
	assertEvents(t, tr.KeyChange(0xE7, false, SideSuperRight, 0, false),
		[]Event{{Keysym: KeysymSuperR, Down: false}})
}

// 左右 Cmd 按下顺序反过来，引用计数语义不变。
func TestModifierBothCommandKeysReverse(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	assertEvents(t, tr.KeyChange(0xE7, true, SideSuperRight, ModSuper, false),
		[]Event{{Keysym: KeysymSuperR, Down: true}})
	assertEvents(t, tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false), nil)
	assertEvents(t, tr.KeyChange(0xE7, false, SideSuperRight, ModSuper, false), nil)
	assertEvents(t, tr.KeyChange(0xE3, false, SideSuperLeft, 0, false),
		[]Event{{Keysym: KeysymSuperL, Down: false}})
}

// 映射为 none 的修饰键：全程无事件，且不影响 PressedCount。
func TestModifierMappedToNone(t *testing.T) {
	m := Mapping{Command: "none", Option: "alt", Control: "ctrl"}
	tr := newTranslator(t, m)

	assertEvents(t, tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false), nil)
	assertEvents(t, tr.KeyChange(0xE3, false, SideSuperLeft, 0, false), nil)
	assertEvents(t, tr.KeyChange(0xE7, true, SideSuperRight, ModSuper, false), nil)
	if n := tr.PressedCount(); n != 0 {
		t.Errorf("PressedCount() = %d; 想要 0", n)
	}
}

// 普通键 repeat=true 再发一次 down；修饰键 repeat 忽略。
func TestRepeat(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	assertEvents(t, tr.KeyChange(0x04 /*a*/, true, SideNone, 0, false),
		[]Event{{Keysym: 'a', Down: true}})
	assertEvents(t, tr.KeyChange(0x04, true, SideNone, 0, true),
		[]Event{{Keysym: 'a', Down: true}})

	// 修饰键 repeat 忽略
	assertEvents(t, tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false),
		[]Event{{Keysym: KeysymSuperL, Down: true}})
	assertEvents(t, tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, true), nil)
}

// 同一 keysym 已按下又收到 down（非 repeat）：先补 up 再发 down。
func TestDuplicateDownDefensive(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	assertEvents(t, tr.KeyChange(0x04, true, SideNone, 0, false),
		[]Event{{Keysym: 'a', Down: true}})
	assertEvents(t, tr.KeyChange(0x04, true, SideNone, 0, false),
		[]Event{{Keysym: 'a', Down: false}, {Keysym: 'a', Down: true}})
}

// ReleaseAll：按 Cmd+Shift+c 后 ReleaseAll → up c、Shift、Super，之后计数归零。
func TestReleaseAll(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())

	tr.KeyChange(0xE3, true, SideSuperLeft, ModSuper, false)
	tr.KeyChange(0xE1, true, SideShiftLeft, ModShift|ModSuper, false)
	tr.KeyChange(0x06 /*c*/, true, SideNone, ModShift|ModSuper, false)

	if n := tr.PressedCount(); n != 3 {
		t.Fatalf("PressedCount() = %d; 想要 3", n)
	}

	got := tr.ReleaseAll()
	// 普通键（按下的 'C'）必须先于修饰键释放；修饰键之间顺序不限
	if len(got) != 3 {
		t.Fatalf("ReleaseAll() = %v; 想要 3 个事件", got)
	}
	if got[0] != (Event{Keysym: 'C', Down: false}) {
		t.Errorf("ReleaseAll()[0] = %v; 想要 c up（按下时是 Shift+c，keysym 为 'C'）", got[0])
	}
	rest := map[Event]bool{got[1]: true, got[2]: true}
	if !rest[Event{Keysym: KeysymShiftL, Down: false}] || !rest[Event{Keysym: KeysymSuperL, Down: false}] {
		t.Errorf("ReleaseAll() 修饰键部分 = %v; 想要 ShiftL up + SuperL up", got[1:])
	}
	if n := tr.PressedCount(); n != 0 {
		t.Errorf("ReleaseAll 后 PressedCount() = %d; 想要 0", n)
	}
	// 再次 ReleaseAll 应为空
	assertEvents(t, tr.ReleaseAll(), nil)
}

// 未跟踪的键松开：不发事件。
func TestKeyUpWithoutDown(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	assertEvents(t, tr.KeyChange(0x04, false, SideNone, 0, false), nil)
	assertEvents(t, tr.KeyChange(0xE3, false, SideSuperLeft, 0, false), nil)
}

// 未知 HID：无事件。
func TestUnknownHIDNoEvents(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	assertEvents(t, tr.KeyChange(0x00, true, SideNone, 0, false), nil)
	assertEvents(t, tr.KeyChange(0x99, true, SideNone, 0, false), nil)
}

// 修饰键重映射：Option→super 时，左 Option 发 Super_L。
func TestModifierRemapOptionToSuper(t *testing.T) {
	m := Mapping{Command: "ctrl", Option: "super", Control: "alt"}
	tr := newTranslator(t, m)

	assertEvents(t, tr.KeyChange(0xE2, true, SideAltLeft, ModAlt, false),
		[]Event{{Keysym: KeysymSuperL, Down: true}})
	assertEvents(t, tr.KeyChange(0xE2, false, SideAltLeft, 0, false),
		[]Event{{Keysym: KeysymSuperL, Down: false}})

	// Control→alt
	assertEvents(t, tr.KeyChange(0xE0, true, SideCtrlLeft, ModCtrl, false),
		[]Event{{Keysym: KeysymAltL, Down: true}})
	assertEvents(t, tr.KeyChange(0xE0, false, SideCtrlLeft, 0, false),
		[]Event{{Keysym: KeysymAltL, Down: false}})
}

// Validate：非法值与冲突映射报错。
func TestValidate(t *testing.T) {
	if err := DefaultMapping().Validate(); err != nil {
		t.Errorf("DefaultMapping().Validate() = %v; 想要 nil", err)
	}
	if err := (Mapping{Command: "none", Option: "none", Control: "none"}).Validate(); err != nil {
		t.Errorf("全 none 应合法: %v", err)
	}
	cases := []Mapping{
		{Command: "super", Option: "super", Control: "ctrl"}, // 冲突
		{Command: "alt", Option: "alt", Control: "ctrl"},     // 冲突
		{Command: "super", Option: "alt", Control: "super"},  // 冲突
		{Command: "windows", Option: "alt", Control: "ctrl"}, // 非法值
		{Command: "", Option: "alt", Control: "ctrl"},        // 非法值
	}
	for _, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("Validate(%+v) 应返回 error", m)
		}
	}
}

// NewTranslator 拒绝非法映射。
func TestNewTranslatorRejectsInvalidMapping(t *testing.T) {
	if _, err := NewTranslator(Mapping{Command: "super", Option: "super", Control: "ctrl"}); err == nil {
		t.Error("NewTranslator 应拒绝冲突映射")
	}
}

// Shift 映射后状态：当前 Mapping 不含 Shift 重映射，Mac Shift 始终生效。
// 这里验证通过 mods 传入的 ModShift 生效、ModAlt/ModCtrl 不生效。
func TestShiftComesOnlyFromShiftMod(t *testing.T) {
	tr := newTranslator(t, DefaultMapping())
	// ModCtrl 不算 shift
	assertEvents(t, tr.KeyChange(0x1E, true, SideNone, ModCtrl, false),
		[]Event{{Keysym: '1', Down: true}})
	tr.KeyChange(0x1E, false, SideNone, ModCtrl, false)
	// ModSuper 不算 shift
	assertEvents(t, tr.KeyChange(0x1E, true, SideNone, ModSuper, false),
		[]Event{{Keysym: '1', Down: true}})
}
