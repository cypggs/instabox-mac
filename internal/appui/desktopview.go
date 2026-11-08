package appui

import (
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"github.com/cypggs/instabox-mac/internal/keys"
	"github.com/cypggs/instabox-mac/internal/rfb"
)

// ScaleMode 桌面缩放模式。
type ScaleMode int

const (
	ScaleFit ScaleMode = iota // 适应窗口（保持比例，letterbox）
	ScaleFill                 // 填满窗口（保持比例，可能裁切）
	Scale100                  // 原始大小
)

func (m ScaleMode) String() string {
	switch m {
	case ScaleFill:
		return "填满"
	case Scale100:
		return "原始"
	}
	return "适应"
}

// DesktopView 是会话窗口的桌面视图：渲染 framebuffer、转发键鼠、
// 同步剪贴板、显示连接状态与键盘捕获提示。
type DesktopView struct {
	Win     *mygo.Window
	Session *Session

	Translator *keys.Translator // 键映射状态机（构造时注入）

	// OnGrabRequest 点击"捕获键盘"时回调（由 main 处理 grab.Manager 与权限）。
	OnGrabRequest func(enable bool)
	// GrabActive 由 main 在捕获状态变化时调用。
	GrabActive bool
	// OnReleaseKeys 请求释放所有按下键（窗口失焦/退出捕获时 main 调用）。
	ReleaseAll func()
	// GrabMessage 捕获相关的提示/错误（如权限缺失），由 main 设置。
	GrabMessage string

	mu       sync.Mutex
	bm       *ui.Bitmap
	frameBuf *image.RGBA // 与 bm 同尺寸的持久缓冲（CopyFramebuffer 目标）
	fbW      int
	fbH      int
	needInit bool // 首次拿到尺寸后建 bitmap

	viewRect ui.Rect // Draw 回调记录的桌面显示区域（DIPs），用于坐标映射
	ptr      image.Point
	buttons  uint8
	scale    ScaleMode
	lastCut  uint64 // 本地剪贴板最近内容哈希（防回环）
	running  bool   // 剪贴板轮询 goroutine 开关

	// 远端光标
	cursor    *ui.Bitmap
	cursorImg *image.RGBA
	cursorHot image.Point
}

// NewDesktopView 创建视图；translator 为键盘映射器。
func NewDesktopView(win *mygo.Window, s *Session, tr *keys.Translator) *DesktopView {
	d := &DesktopView{
		Win:        win,
		Session:    s,
		Translator: tr,
		scale:      ScaleFit,
		needInit:   true,
	}
	s.OnFrame = d.onFrame
	s.OnSize = d.onRemoteSize
	s.OnState = d.onState
	return d
}

// Start 开始剪贴板同步轮询（连接后调用）。
func (d *DesktopView) Start() {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()
	go d.clipboardPoll()
}

// Stop 停止轮询（断开/关闭视图时调用）。
func (d *DesktopView) Stop() {
	d.mu.Lock()
	d.running = false
	d.mu.Unlock()
}

// --- 会话事件（读 goroutine）→ 主线程 UI 更新 ---

func (d *DesktopView) onFrame() {
	d.Win.Update(func() {
		conn := d.Session.Conn()
		if conn == nil {
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.frameBuf == nil {
			return
		}
		conn.CopyFramebuffer(d.frameBuf)
		if d.bm != nil {
			d.bm.Update(d.frameBuf)
		}
	})
}

func (d *DesktopView) onRemoteSize(w, h int) {
	d.Win.Update(func() {
		d.rebuildBitmap(w, h)
	})
}

func (d *DesktopView) onState(st SessionState, err error) {
	d.Win.Update(func() {
		d.Win.Invalidate()
	})
}

// rebuildBitmap 按远端尺寸重建 bitmap 与持久缓冲。
func (d *DesktopView) rebuildBitmap(w, h int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fbW, d.fbH = w, h
	d.frameBuf = image.NewRGBA(image.Rect(0, 0, w, h))
	d.bm = ui.NewBitmap(d.frameBuf)
	d.needInit = false
}

// --- 剪贴板 ---

func (d *DesktopView) clipboardPoll() {
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		d.mu.Lock()
		running := d.running
		d.mu.Unlock()
		if !running {
			return
		}
		if d.Session.State() != StateConnected {
			continue
		}
		data, err := mygo.Clipboard.Read(transfer.Text)
		if err != nil {
			continue
		}
		f, ok := data.Preferred(transfer.Text)
		if !ok {
			continue
		}
		b, err := data.Read(f)
		if err != nil || len(b) == 0 {
			continue
		}
		h := fnv.New64a()
		h.Write(b)
		sum := h.Sum64()
		d.mu.Lock()
		if sum == d.lastCut {
			d.mu.Unlock()
			continue
		}
		d.lastCut = sum
		d.mu.Unlock()
		if conn := d.Session.Conn(); conn != nil {
			_ = conn.SendCutText(string(b))
		}
	}
}

// RemoteCutText 是 rfb ServerCutText 的回调入口（main 装配）。
func (d *DesktopView) RemoteCutText(s string) {
	h := fnv.New64a()
	h.Write([]byte(s))
	sum := h.Sum64()
	d.mu.Lock()
	if sum == d.lastCut {
		d.mu.Unlock()
		return
	}
	d.lastCut = sum
	d.mu.Unlock()
	_ = mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, []byte(s)))))
}

// --- 视图 ---

// View 是 MyGo 视图函数。
func (d *DesktopView) View(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		d.toolbar(c, t)
		ui.Box(c).Fill().Focusable().Focus().Label("远程桌面").
			HandleInput(d.input).
			Draw(func(p *ui.Painter, r ui.Rect) {
				d.drawDesktop(p, r)
			})
		d.statusBar(c, t)
	})
}

func (d *DesktopView) toolbar(c *ui.Context, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(6).Justify(ui.Center).Children(func() {
		st := d.Session.State()
		dot := "●"
		color := t.Success
		switch st {
		case StateConnecting, StateReconnecting:
			color = t.Warning
		case StateFailed, StateDisconnected:
			color = t.Danger
		}
		ui.Text(c, dot).TextColor(color)
		ui.Text(c, "桌面 "+st.String()).FontSize(12)
		if ui.Button(c.Key("scale"), "缩放: "+d.scale.String()).Clicked() {
			d.scale = (d.scale + 1) % 3
		}
		if ui.Button(c.Key("grab"), d.grabLabel()).Clicked() {
			if d.OnGrabRequest != nil {
				d.OnGrabRequest(!d.GrabActive)
			}
		}
		if ui.Button(c.Key("fs"), "全屏").Clicked() {
			d.Win.ToggleFullScreen()
		}
	})
}

func (d *DesktopView) grabLabel() string {
	if d.GrabActive {
		return "⏏ 释放输入 (Ctrl+Opt+G)"
	}
	return "捕获输入"
}

func (d *DesktopView) statusBar(c *ui.Context, t *ui.Theme) {
	st := d.Session.State()
	if st == StateConnected && !d.GrabActive && d.GrabMessage == "" {
		return
	}
	ui.Box(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(10).Padding(6).Justify(ui.Center).Children(func() {
			if d.GrabMessage != "" {
				ui.Text(c, d.GrabMessage).FontSize(12).TextColor(t.Danger)
			}
			if st != StateConnected {
				msg := st.String()
				if st == StateFailed || st == StateReconnecting {
					if err := d.Session.Err(); err != nil {
						msg += ": " + err.Error()
					}
				}
				ui.Text(c, msg).FontSize(12).TextColor(t.Warning)
				if ui.Button(c.Key("reconnect"), "重连").Clicked() {
					go func() { _ = d.Session.Reconnect(context.Background()) }()
				}
				if ui.Button(c.Key("back"), "返回列表").Clicked() {
					d.Session.Close()
				}
			}
			if d.GrabActive {
				ui.Text(c, "⌨️ 输入已捕获 — 键盘鼠标都发给远程；按 Ctrl+Option+G 或点这里释放").FontSize(12).TextColor(t.Success)
				if ui.Button(c.Key("ungrab"), "释放").Clicked() {
					if d.OnGrabRequest != nil {
						d.OnGrabRequest(false)
					}
				}
			}
		})
	})
}

// drawDesktop 在 Box 的 Draw 回调里渲染帧缓冲与远端光标。
func (d *DesktopView) drawDesktop(p *ui.Painter, r ui.Rect) {
	d.mu.Lock()
	bm := d.bm
	w, h := d.fbW, d.fbH
	d.viewRect = r
	d.mu.Unlock()

	if bm == nil || w == 0 || h == 0 {
		return
	}
	dr := d.destRect(r)
	fit := ui.Contain
	if d.scale == ScaleFill {
		fit = ui.Cover
	}
	p.Image(bm, dr, fit)

	// 远端光标：用与点击完全一致的映射绘制（toScreenCoords）。
	// 捕获模式下若远端未给光标形状，画一个明显的准星标记（与 macOS 箭头
	// 明显不同，避免用户误以为本机光标没隐藏）。
	d.mu.Lock()
	cb, ch, ci := d.cursor, d.cursorHot, d.cursorImg
	ptr := d.ptr
	d.mu.Unlock()
	if cb != nil && ci != nil {
		sx, sy, s := d.toScreenCoords(float32(ptr.X), float32(ptr.Y))
		p.Image(cb, ui.Rect{X: sx - float32(ch.X)*s, Y: sy - float32(ch.Y)*s,
			W: float32(ci.Bounds().Dx()) * s, H: float32(ci.Bounds().Dy()) * s}, ui.FillBox)
	} else if d.GrabActive {
		sx, sy, _ := d.toScreenCoords(float32(ptr.X), float32(ptr.Y))
		p.Image(captureReticle(), ui.Rect{X: sx - 11, Y: sy - 11, W: 22, H: 22}, ui.FillBox)
	}
}

// destRect 计算帧缓冲在显示区域中的目标矩形。
// Scale100 时若远端大于可视区，左上角对齐（居中会让上下都跑到可视区外，
// 用户只能看到白边，像是"空白"）。
func (d *DesktopView) destRect(r ui.Rect) ui.Rect {
	d.mu.Lock()
	w, h := d.fbW, d.fbH
	mode := d.scale
	d.mu.Unlock()
	if w == 0 || h == 0 {
		return r
	}
	if mode == Scale100 {
		x := r.X + (r.W-float32(w))/2
		y := r.Y + (r.H-float32(h))/2
		if x < r.X {
			x = r.X
		}
		if y < r.Y {
			y = r.Y
		}
		return ui.Rect{X: x, Y: y, W: float32(w), H: float32(h)}
	}
	// Fit/Fill 由 Painter 的 Contain/Cover 处理，直接用整个区域
	return r
}

// toRemoteCoords 把元素内 DIP 坐标映射为帧缓冲像素坐标。
func (d *DesktopView) toRemoteCoords(x, y float32) (uint16, uint16, bool) {	d.mu.Lock()
	r := d.viewRect
	w, h := d.fbW, d.fbH
	mode := d.scale
	d.mu.Unlock()
	if w == 0 || h == 0 {
		return 0, 0, false
	}
	var fx, fy float32
	switch mode {
	case Scale100:
		dr := d.destRect(r)
		fx = x - dr.X
		fy = y - dr.Y
		if fx < 0 || fy < 0 || fx >= dr.W || fy >= dr.H {
			return 0, 0, false
		}
	case ScaleFill:
		// Cover：按比例缩放，可能裁切，居中
		s := max(r.W/float32(w), r.H/float32(h))
		drw, drh := float32(w)*s, float32(h)*s
		offX := (drw - r.W) / 2
		offY := (drh - r.H) / 2
		fx = (x - r.X + offX) / s
		fy = (y - r.Y + offY) / s
	default: // Fit / Contain
		s := min(r.W/float32(w), r.H/float32(h))
		drw, drh := float32(w)*s, float32(h)*s
		offX := (r.W - drw) / 2
		offY := (r.H - drh) / 2
		fx = (x - r.X - offX) / s
		fy = (y - r.Y - offY) / s
	}
	if fx < 0 || fy < 0 || fx >= float32(w) || fy >= float32(h) {
		return 0, 0, false
	}
	return uint16(fx), uint16(fy), true
}

// toScreenCoords 是 toRemoteCoords 的逆映射：帧缓冲像素 → 元素内 DIP。
// 画远程光标必须用与点击完全一致的映射，否则"光标看着在这、点击落在那"。
func (d *DesktopView) toScreenCoords(fx, fy float32) (float32, float32, float32) {
	d.mu.Lock()
	r := d.viewRect
	w, h := d.fbW, d.fbH
	mode := d.scale
	d.mu.Unlock()
	if w == 0 || h == 0 {
		return 0, 0, 1
	}
	var s float32
	switch mode {
	case Scale100:
		dr := d.destRect(r)
		return dr.X + fx, dr.Y + fy, 1
	case ScaleFill:
		s = max(r.W/float32(w), r.H/float32(h))
		drw, drh := float32(w)*s, float32(h)*s
		return r.X - (drw-r.W)/2 + fx*s, r.Y - (drh-r.H)/2 + fy*s, s
	default: // Fit / Contain
		s = min(r.W/float32(w), r.H/float32(h))
		drw, drh := float32(w)*s, float32(h)*s
		return r.X + (r.W-drw)/2 + fx*s, r.Y + (r.H-drh)/2 + fy*s, s
	}
}

// --- 输入 ---

var wheelAccX, wheelAccY float32

func (d *DesktopView) input(ev ui.InputEvent) bool {
	conn := d.Session.Conn()
	if conn == nil {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerMove:
		rx, ry, ok := d.toRemoteCoords(ev.X, ev.Y)
		if !ok {
			return true
		}
		d.mu.Lock()
		d.ptr = image.Pt(int(rx), int(ry))
		mask := d.buttons
		d.mu.Unlock()
		_ = conn.SendPointer(rx, ry, mask)
		return true
	case ui.InputPointerDown, ui.InputPointerUp:
		bit := uint8(1) << uint(ev.Button)
		if ev.Button > 2 {
			return false
		}
		down := ev.Kind == ui.InputPointerDown
		d.mu.Lock()
		if down {
			d.buttons |= bit
		} else {
			d.buttons &^= bit
		}
		mask := d.buttons
		d.mu.Unlock()
		rx, ry, ok := d.toRemoteCoords(ev.X, ev.Y)
		if !ok {
			return true
		}
		d.mu.Lock()
		d.ptr = image.Pt(int(rx), int(ry))
		d.mu.Unlock()
		_ = conn.SendPointer(rx, ry, mask)
		return true
	case ui.InputScroll:
		return d.handleScroll(conn, ev)
	case ui.InputKeyDown, ui.InputKeyUp:
		down := ev.Kind == ui.InputKeyDown
		return d.handleKey(conn, uint32(ev.Code), down, ev.Mods, ev.Repeat)
	}
	return false
}

func (d *DesktopView) handleScroll(conn *rfb.Conn, ev ui.InputEvent) bool {
	wheelAccX += ev.DX
	wheelAccY += ev.DY
	// 触控板精确滚动：累积约 1 格发一个滚轮事件；鼠标滚轮 DY 通常为 ±16 左右整格。
	const notch = 1.0
	d.mu.Lock()
	x, y := d.ptr.X, d.ptr.Y
	mask := d.buttons
	d.mu.Unlock()
	for wheelAccY >= notch {
		_ = conn.SendPointer(uint16(x), uint16(y), mask|8)
		_ = conn.SendPointer(uint16(x), uint16(y), mask)
		wheelAccY -= notch
	}
	for wheelAccY <= -notch {
		_ = conn.SendPointer(uint16(x), uint16(y), mask|16)
		_ = conn.SendPointer(uint16(x), uint16(y), mask)
		wheelAccY += notch
	}
	for wheelAccX <= -notch {
		_ = conn.SendPointer(uint16(x), uint16(y), mask|32)
		_ = conn.SendPointer(uint16(x), uint16(y), mask)
		wheelAccX += notch
	}
	for wheelAccX >= notch {
		_ = conn.SendPointer(uint16(x), uint16(y), mask|64)
		_ = conn.SendPointer(uint16(x), uint16(y), mask)
		wheelAccX -= notch
	}
	return true
}

// handleKey 把一个物理键事件经 Translator 映射后发送。
// 返回 true 表示事件被消费。
func (d *DesktopView) handleKey(conn *rfb.Conn, hid uint32, down bool, mods ui.Modifiers, repeat bool) bool {
	if hid == 0 {
		return false
	}
	km, side := convertMods(mods), sideOf(hid)
	for _, e := range d.Translator.KeyChange(hid, down, side, km, repeat) {
		_ = conn.SendKey(e.Keysym, e.Down)
	}
	return true
}

// convertMods ui.Modifiers → keys.Mods（映射前的物理状态）。
func convertMods(m ui.Modifiers) keys.Mods {
	var km keys.Mods
	if m&ui.Shift != 0 {
		km |= keys.ModShift
	}
	if m&ui.Ctrl != 0 {
		km |= keys.ModCtrl
	}
	if m&ui.Alt != 0 {
		km |= keys.ModAlt
	}
	if m&ui.Super != 0 {
		km |= keys.ModSuper
	}
	return km
}

// sideOf 由 HID usage 判断修饰键左右。
func sideOf(hid uint32) keys.ModSide {
	switch hid {
	case 0xE1:
		return keys.SideShiftLeft
	case 0xE5:
		return keys.SideShiftRight
	case 0xE0:
		return keys.SideCtrlLeft
	case 0xE4:
		return keys.SideCtrlRight
	case 0xE2:
		return keys.SideAltLeft
	case 0xE6:
		return keys.SideAltRight
	case 0xE3:
		return keys.SideSuperLeft
	case 0xE7:
		return keys.SideSuperRight
	}
	return keys.SideNone
}

// captureReticle 是捕获模式下远端未给光标形状时绘制的准星：
// 绿色圆环 + 中心点 + 十字线，刻意做得与 macOS 箭头完全不同，
// 让用户一眼能分辨"这是远程光标，本机光标已隐藏"。
var captureReticle = sync.OnceValue(func() *ui.Bitmap {
	const sz = 22
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))
	green := color.RGBA{80, 220, 120, 255}
	cx, cy := sz/2, sz/2
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			dx, dy := x-cx, y-cy
			r2 := dx*dx + dy*dy
			ring := r2 >= 7*7 && r2 <= 9*9 // 圆环 r=8
			arm := (abs(dx) <= 0 && abs(dy) >= 11) || (abs(dy) <= 0 && abs(dx) >= 11) ||
				(r2 <= 1) // 十字线 + 中心点
			if ring || arm {
				img.SetRGBA(x, y, green)
			}
		}
	}
	return ui.NewBitmap(img)
})

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// CursorChanged 是 rfb Cursor 伪编码回调入口（main 装配）。
func (d *DesktopView) CursorChanged(hotX, hotY uint16, img *image.RGBA) {
	d.Win.Update(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if img == nil {
			d.cursor = nil
			d.cursorImg = nil
			return
		}
		d.cursorImg = img
		d.cursorHot = image.Pt(int(hotX), int(hotY))
		d.cursor = ui.NewBitmap(img)
	})
}

// String 便于调试。
func (d *DesktopView) String() string {
	return fmt.Sprintf("DesktopView(%dx%d)", d.fbW, d.fbH)
}
