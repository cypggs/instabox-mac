// Instabox 原生 macOS 客户端。
//
// 主入口：装配 MyGo 窗口、机器列表 ↔ 桌面视图导航、菜单、
// 键盘捕获（CGEventTap）与按键释放兜底。
package main

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/cypggs/instabox-mac/internal/appui"
	"github.com/cypggs/instabox-mac/internal/grab"
	"github.com/cypggs/instabox-mac/internal/instabox"
	"github.com/cypggs/instabox-mac/internal/keys"
)

const apiBase = "https://api.instabox.sh"

// 捕获模式退出组合键：Ctrl+Option+G（物理键，与映射配置无关）。
const (
	escapeHID        = 0x0A // G
	escapeNeedMods   = 2    // 需要按住的修饰键数量（Ctrl + Option）
	keyHIDLeftCtrl   = 0xE0
	keyHIDLeftShift  = 0xE1
	keyHIDLeftAlt    = 0xE2
	keyHIDLeftSuper  = 0xE3
	keyHIDRightCtrl  = 0xE4
	keyHIDRightShift = 0xE5
	keyHIDRightAlt   = 0xE6
	keyHIDRightSuper = 0xE7
)

type app struct {
	win    *mygo.Window
	client *instabox.Client
	prefs  *appui.Preferences

	mu         sync.Mutex
	list       *appui.MachineList
	session    *appui.Session
	desktop    *appui.DesktopView
	translator *keys.Translator

	grabMgr       *grab.Manager
	grabOn        bool
	wasFullScreen bool
}

func main() {
	log.SetPrefix("[instabox] ")
	prefs := appui.LoadPreferences()
	key, fromEnv := appui.LoadAPIKey()
	client := appui.NewClient(apiBase, key)

	a := &app{client: client, prefs: prefs}

	mygo.App.WhenReady(func() {
		a.buildMenu()
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Instabox",
			Width:     960,
			Height:    680,
			MinWidth:  640,
			MinHeight: 480,
			Content:   ui.View(a.view),
		})

		a.list = appui.NewMachineList(client, prefs, key, fromEnv)
		a.list.OnConnect = a.connect
		a.list.SetOnChanged(func() { a.win.Invalidate() })
		a.list.Refresh(context.Background())

		// 调试：INSTABOX_AUTOCONNECT=1 时自动连接第一台 running 机器。
		if os.Getenv("INSTABOX_AUTOCONNECT") == "1" {
			go func() {
				time.Sleep(2 * time.Second)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				ms, err := client.ListMachines(ctx)
				if err != nil {
					return
				}
				for _, m := range ms {
					if m.State == "running" || m.State == "suspended" {
						a.connect(m)
						return
					}
				}
			}()
		}

		a.win.OnBlur(a.onBlur)
		a.win.OnResize(a.onResize)
		a.win.OnClose(func(e *mygo.CloseEvent) {
			a.setGrab(false)
			if s := a.currentSession(); s != nil {
				s.Close()
			}
		})
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// view 是窗口根视图：已连接会话显示远程桌面，否则显示机器列表。
func (a *app) view(c *ui.Context) {
	a.mu.Lock()
	s, d, l := a.session, a.desktop, a.list
	a.mu.Unlock()
	if s != nil && d != nil && s.State() != appui.StateDisconnected {
		d.View(c)
		return
	}
	if l != nil {
		l.View(c)
	}
}

// connect 发起一次桌面连接（来自列表的"连接"按钮）。
func (a *app) connect(m instabox.Machine) {
	a.mu.Lock()
	if a.session != nil && a.session.State() != appui.StateDisconnected {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	tr, err := keys.NewTranslator(a.prefs.GetMapping())
	if err != nil {
		log.Printf("translator: %v", err)
		return
	}
	s := appui.NewSession(a.client)
	d := appui.NewDesktopView(a.win, s, tr)
	s.OnCursor = d.CursorChanged
	s.OnCutText = d.RemoteCutText
	d.OnGrabRequest = a.setGrab
	d.ReleaseAll = a.releaseKeys

	// 包装 OnState：断开时停剪贴板轮询并刷新导航。
	orig := s.OnState
	s.OnState = func(st appui.SessionState, err error) {
		if st == appui.StateDisconnected {
			d.Stop()
		}
		if orig != nil {
			orig(st, err)
		}
		a.win.Invalidate()
	}

	a.mu.Lock()
	a.session, a.desktop, a.translator = s, d, tr
	a.mu.Unlock()

	d.Start()
	go func() {
		if err := s.Connect(context.Background(), m.ID, m.State); err != nil {
			log.Printf("connect %s: %v", m.ID, err)
		}
	}()
	a.win.Invalidate()
}

func (a *app) currentSession() *appui.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

// backToList 断开当前会话并返回机器列表。
func (a *app) backToList() {
	a.setGrab(false)
	if s := a.currentSession(); s != nil {
		s.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// --- 键盘捕获 ---

// setGrab 开启/关闭键盘捕获模式。enable=true 时检查辅助功能权限，
// 未授权则请求授权并打开系统设置。
func (a *app) setGrab(enable bool) {
	a.mu.Lock()
	d := a.desktop
	a.mu.Unlock()

	if enable {
		if !grab.Available() {
			grab.RequestAccess() // 触发系统授权弹窗
			if !grab.Available() {
				if d != nil {
					d.GrabMessage = "输入捕获需要辅助功能权限：系统设置 → 隐私与安全性 → 辅助功能，勾选 Instabox 后重试。"
				}
				grab.OpenSystemPrefs()
				if a.win != nil {
					a.win.Invalidate()
				}
				return
			}
		}
		if a.grabMgr == nil {
			a.grabMgr = &grab.Manager{}
		}
		if err := a.grabMgr.Start(a.onGrabEvent); err != nil {
			if d != nil {
				d.GrabMessage = "输入捕获启动失败: " + err.Error()
			}
			if a.win != nil {
				a.win.Invalidate()
			}
			return
		}
		a.grabOn = true
		grab.HideCursor() // 捕获输入：隐藏本机光标，只显示远程光标
		if d != nil {
			d.GrabMessage = ""
		}
	} else {
		if a.grabMgr != nil {
			a.grabMgr.Stop()
		}
		a.grabOn = false
		grab.ShowCursorAll()
		a.releaseKeys()
	}

	if d != nil {
		d.GrabActive = a.grabOn
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// onGrabEvent 在 tap 的 runloop goroutine 中被调用，返回 true 表示吞掉事件。
func (a *app) onGrabEvent(ev grab.Event) bool {
	// 退出捕获：Ctrl+Option+G（物理键，映射无关）。
	if ev.Down && !ev.Repeat && ev.HID == escapeHID &&
		countBits(ev.Mods&(grab.FlagMaskControl|grab.FlagMaskAlternate)) == escapeNeedMods {
		a.win.Update(func() { a.setGrab(false) })
		return true
	}
	a.mu.Lock()
	tr, s := a.translator, a.session
	a.mu.Unlock()
	if tr == nil || s == nil {
		return false
	}
	conn := s.Conn()
	if conn == nil {
		return false
	}
	var km keys.Mods
	if ev.Mods&grab.FlagMaskShift != 0 {
		km |= keys.ModShift
	}
	if ev.Mods&grab.FlagMaskControl != 0 {
		km |= keys.ModCtrl
	}
	if ev.Mods&grab.FlagMaskAlternate != 0 {
		km |= keys.ModAlt
	}
	if ev.Mods&grab.FlagMaskCommand != 0 {
		km |= keys.ModSuper
	}
	for _, e := range tr.KeyChange(ev.HID, ev.Down, grabSideOf(ev.HID), km, ev.Repeat) {
		_ = conn.SendKey(e.Keysym, e.Down)
	}
	return true // 捕获模式下所有按键都发给远程
}

// releaseKeys 释放所有仍按下的远程键（防卡键）。
func (a *app) releaseKeys() {
	a.mu.Lock()
	tr, s := a.translator, a.session
	a.mu.Unlock()
	if tr == nil || s == nil {
		return
	}
	conn := s.Conn()
	if conn == nil {
		return
	}
	for _, e := range tr.ReleaseAll() {
		_ = conn.SendKey(e.Keysym, false)
	}
}

// onBlur 失焦时释放全部按键并退出捕获（用户已切到别的应用，
// 继续吞键会让别的应用打不了字；光标也要立刻恢复可见）。
func (a *app) onBlur() {
	a.releaseKeys()
	grab.ShowCursorAll()
	if a.grabOn {
		a.win.Update(func() { a.setGrab(false) })
	}
}

// onResize 跟踪全屏切换，退出全屏时释放按键。
func (a *app) onResize() {
	if a.win == nil {
		return
	}
	fs := a.win.IsFullScreen()
	if a.wasFullScreen && !fs {
		a.releaseKeys()
	}
	a.wasFullScreen = fs
}

// grabSideOf 修饰键 HID 的左右判断（与 appui 同名逻辑保持一致）。
func grabSideOf(hid uint32) keys.ModSide {
	switch hid {
	case keyHIDLeftShift:
		return keys.SideShiftLeft
	case keyHIDRightShift:
		return keys.SideShiftRight
	case keyHIDLeftCtrl:
		return keys.SideCtrlLeft
	case keyHIDRightCtrl:
		return keys.SideCtrlRight
	case keyHIDLeftAlt:
		return keys.SideAltLeft
	case keyHIDRightAlt:
		return keys.SideAltRight
	case keyHIDLeftSuper:
		return keys.SideSuperLeft
	case keyHIDRightSuper:
		return keys.SideSuperRight
	}
	return keys.SideNone
}

func countBits(v uint64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

// --- 菜单 ---

// keyHIDW 是 W 键的 USB HID usage（Cmd+W 转发用）。
const keyHIDW = 0x1A

// onCloseWindow 处理"关闭窗口"（Cmd+W）：桌面会话进行中时把 Cmd+W
// 转发给远程（按用户修饰键映射，默认 Cmd→Super），否则正常关闭窗口。
// 这样 Hyprland/Omarchy 里 Super+W 关闭软件的操作不会被 macOS 抢去退出本应用。
func (a *app) onCloseWindow() {
	a.mu.Lock()
	s, tr := a.session, a.translator
	a.mu.Unlock()
	if s != nil && tr != nil && s.Conn() != nil &&
		(s.State() == appui.StateConnected || s.State() == appui.StateReconnecting) {
		var evs []keys.Event
		evs = append(evs, tr.KeyChange(keyHIDLeftSuper, true, keys.SideSuperLeft, 0, false)...)
		evs = append(evs, tr.KeyChange(keyHIDW, true, keys.SideNone, 0, false)...)
		evs = append(evs, tr.KeyChange(keyHIDW, false, keys.SideNone, 0, false)...)
		evs = append(evs, tr.KeyChange(keyHIDLeftSuper, false, keys.SideSuperLeft, 0, false)...)
		conn := s.Conn()
		for _, e := range evs {
			_ = conn.SendKey(e.Keysym, e.Down)
		}
		return
	}
	if a.win != nil {
		a.win.Close()
	}
}

func (a *app) buildMenu() {
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "会话", Submenu: []*mygo.MenuItem{
			{Label: "返回机器列表", Accelerator: "CmdOrCtrl+L", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.backToList()
			}},
			{Label: "切换全屏", Accelerator: "Ctrl+Cmd+F", Click: func(*mygo.MenuItem, *mygo.Window) {
				if a.win != nil {
					a.win.ToggleFullScreen()
				}
			}},
			mygo.Separator(),
			{Label: "关闭窗口", Accelerator: "CmdOrCtrl+W", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.onCloseWindow()
			}},
		}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
		{Role: mygo.RoleHelp},
	}))
}
