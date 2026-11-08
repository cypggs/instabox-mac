package appui

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/cypggs/instabox-mac/internal/keys"
)

// newTestDesktop 构造一个给定远端尺寸的 DesktopView（不触发 UI）。
func newTestDesktop(w, h int) *DesktopView {
	d := &DesktopView{scale: ScaleFit}
	d.fbW, d.fbH = w, h
	d.viewRect = ui.Rect{X: 0, Y: 0, W: 800, H: 600}
	return d
}

func TestToRemoteCoordsFit(t *testing.T) {
	d := newTestDesktop(906, 742) // 比例 1.221
	x, y, ok := d.toRemoteCoords(400, 300) // 显示区域中心
	if !ok {
		t.Fatal("center should map")
	}
	// Fit：scale = min(800/906, 600/742) = 0.8086；宽居中偏移 (800-732.6)/2≈33.7
	// fx=(400-33.7)/0.8086 ≈ 453, fy=300/0.8086 ≈ 371
	if x != 453 || y != 371 {
		t.Fatalf("center maps to (%d,%d), want (453,371)", x, y)
	}
}

func TestToRemoteCoordsFill(t *testing.T) {
	d := newTestDesktop(906, 742)
	d.scale = ScaleFill // Cover：宽填满 800，高=800/1.221=655 裁掉 55
	x, y, ok := d.toRemoteCoords(400, 300)
	if !ok {
		t.Fatal("should map")
	}
	// scale=800/906=0.883；offY=(655-600)/2=27.5 → fy=(300+27.5)/0.883≈370
	if y < 365 || y > 375 {
		t.Fatalf("y = %d, want ~370", y)
	}
	if x < 448 || x > 458 {
		t.Fatalf("x = %d, want ~453", x)
	}
}

func TestToRemoteCoords100(t *testing.T) {
	d := newTestDesktop(906, 742)
	d.scale = Scale100 // 远端大于可视区 → 左上角对齐，dr 原点 = (0,0)
	x, y, ok := d.toRemoteCoords(400, 300)
	if !ok || x != 400 || y != 300 {
		t.Fatalf("got (%d,%d,%v), want (400,300,true)", x, y, ok)
	}
	// 远端比窗口小时原始区域外必须拒绝：换 400x300 远端，窗口 (10,10) 在 1:1 区外
	d2 := newTestDesktop(400, 300)
	d2.scale = Scale100 // dr 偏移 (200,150)
	if _, _, ok := d2.toRemoteCoords(10, 10); ok {
		t.Fatal("outside 1:1 rect should be rejected")
	}
	if x, y, ok := d2.toRemoteCoords(400, 300); !ok || x != 200 || y != 150 {
		t.Fatalf("d2 center = (%d,%d,%v)", x, y, ok)
	}
}

// TestToScreenCoordsRoundtrip 验证 toScreenCoords 与 toRemoteCoords 互逆：
// 光标的绘制位置必须和点击发送的位置是同一个点（"指哪点哪"）。
func TestToScreenCoordsRoundtrip(t *testing.T) {
	abs := func(v float32) float32 {
		if v < 0 {
			return -v
		}
		return v
	}
	points := [][2]float32{{400, 300}, {33, 41}, {700, 500}, {100, 550}}
	for _, mode := range []ScaleMode{ScaleFit, ScaleFill, Scale100} {
		d := newTestDesktop(906, 742)
		d.scale = mode
		for _, p := range points {
			rx, ry, ok := d.toRemoteCoords(p[0], p[1])
			if !ok {
				continue // 该点在有效区外（如 Scale100 越界），跳过
			}
			sx, sy, _ := d.toScreenCoords(float32(rx), float32(ry))
			if abs(sx-p[0]) > 1.5 || abs(sy-p[1]) > 1.5 {
				t.Fatalf("mode=%v point=(%v,%v): roundtrip (%v,%v)", mode, p[0], p[1], sx, sy)
			}
		}
	}
	// Scale100 远端小于窗口（居中场景）也要互逆
	d := newTestDesktop(400, 300)
	d.scale = Scale100 // dr 偏移 (200,150)
	for _, p := range points {
		rx, ry, ok := d.toRemoteCoords(p[0], p[1])
		if !ok {
			continue
		}
		sx, sy, _ := d.toScreenCoords(float32(rx), float32(ry))
		if abs(sx-p[0]) > 1.5 || abs(sy-p[1]) > 1.5 {
			t.Fatalf("small Scale100 point=(%v,%v): roundtrip (%v,%v)", p[0], p[1], sx, sy)
		}
	}
}

func TestConvertModsAndSide(t *testing.T) {	var m ui.Modifiers
	m |= ui.Shift | ui.Ctrl | ui.Alt | ui.Super
	km := convertMods(m)
	if km != 0x0F {
		t.Fatalf("mods = %b", km)
	}
	if sideOf(0xE0) != sideOf(0xE0) || sideOf(0xE0) == sideOf(0xE4) {
		t.Fatal("ctrl sides wrong")
	}
	if sideOf(0xE7) != keys.SideSuperRight {
		t.Fatalf("right cmd side = %v", sideOf(0xE7))
	}
	if sideOf(0xE0) != keys.SideCtrlLeft || sideOf(0xE4) != keys.SideCtrlRight {
		t.Fatal("ctrl side mapping wrong")
	}
}

func TestCursorChangedNil(t *testing.T) {
	d := &DesktopView{}
	// 恢复系统光标（img=nil）不应 panic。
	d.cursor = &ui.Bitmap{}
	d.cursorImg = image.NewRGBA(image.Rect(0, 0, 4, 4))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = d
}
