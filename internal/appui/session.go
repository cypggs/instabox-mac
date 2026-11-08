// Package appui 是应用的 MyGo 原生 UI：机器列表、桌面视图、偏好设置。
package appui

import (
	"context"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/cypggs/instabox-mac/internal/instabox"
	"github.com/cypggs/instabox-mac/internal/rfb"
)

// SessionState 是会话连接状态。
type SessionState int

const (
	StateDisconnected SessionState = iota
	StateConnecting
	StateConnected
	StateReconnecting
	StateFailed // 重试耗尽或明确失败，等待用户操作
)

func (s SessionState) String() string {
	switch s {
	case StateConnecting:
		return "连接中"
	case StateConnected:
		return "已连接"
	case StateReconnecting:
		return "重连中"
	case StateFailed:
		return "连接失败"
	}
	return "未连接"
}

// Session 管理一条到某台机器桌面的 VNC 连接生命周期。
//
// 事件回调（OnState/OnFrame/OnSize/OnError）在任意 goroutine 被调用，
// UI 层用 mygo.Window.Update 切回主线程。
//
// 更新请求模型：Instabox 中继会丢弃过大的服务器→客户端 WS 消息
//（实测 payload 16KB 通过、64KB 被吞且连接随后被断），因此不能请求全屏
// 更新（首帧全屏 ZRLE 可达数百 KB）。改为 64x64 小块网格：
// 连接后把所有块非增量请求一遍（首帧），之后由扫描 ticker 轮流请求
// 增量块，保证任何单条更新消息 ≤16KB。
type Session struct {
	// OnState 状态变化时回调。
	OnState func(s SessionState, err error)
	// OnFrame 帧缓冲有更新（解码后），UI 应重绘。
	OnFrame func()
	// OnSize 远端桌面尺寸变化（DesktopSize），UI 应重建 bitmap。
	OnSize func(w, h int)
	// OnCursor 光标伪编码（img=nil 恢复系统光标）。
	OnCursor func(hotX, hotY uint16, img *image.RGBA)
	// OnCutText 远端剪贴板文本。
	OnCutText func(text string)

	client *instabox.Client
	conn   *rfb.Conn

	mu           sync.Mutex
	state        SessionState
	closed       bool
	lastErr      error
	reconnecting bool
	gotFrame     bool
	watchdog     *time.Timer

	machineID string
	reconnect int
}

// 平铺参数（实测推导，见文件头注释）。
const (
	tileEdge          = 64                // 64x64 Raw = 16KB，实测可通过中继
	sweepBatch        = 24                // 每轮增量扫描发出的请求数
	sweepTick         = 60 * time.Millisecond
	firstFrameTimeout = 12 * time.Second  // 首帧看门狗：超时按中继故障重连
)

// NewSession 创建会话（未连接）。
func NewSession(client *instabox.Client) *Session {
	return &Session{client: client, state: StateDisconnected}
}

// Err 返回导致当前非 Connected 状态的最后一次错误。
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// State 返回当前状态。
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// setState 更新状态并回调。
func (s *Session) setState(st SessionState, err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.state = st
	s.lastErr = err
	s.mu.Unlock()
	if s.OnState != nil {
		s.OnState(st, err)
	}
}

// Connect 连接到指定机器的桌面。machineState 是当前机器状态
//（"suspended" 时先走 wake 唤醒闲置暂停）。
func (s *Session) Connect(ctx context.Context, machineID, machineState string) error {
	s.mu.Lock()
	if s.state == StateConnecting || s.state == StateConnected || s.state == StateReconnecting {
		s.mu.Unlock()
		return fmt.Errorf("会话已在连接中")
	}
	s.machineID = machineID
	s.closed = false
	s.mu.Unlock()

	if machineState == "suspended" {
		s.setState(StateConnecting, nil)
		wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := s.client.WakeMachine(wctx, machineID)
		cancel()
		if err != nil {
			s.setState(StateFailed, fmt.Errorf("唤醒机器失败: %w", err))
			return err
		}
	}

	return s.dial(ctx)
}

func (s *Session) dial(ctx context.Context) error {
	s.setState(StateConnecting, nil)

	url := s.client.VNCWebSocketURL(s.machineID)
	apiKey := s.client.APIKey()
	trans, err := rfb.DialWebSocket(ctx, url, map[string]string{"X-API-Key": apiKey})
	if err != nil {
		s.setState(StateFailed, err)
		return err
	}

	var conn *rfb.Conn
	conn, err = rfb.Dial(ctx, trans, rfb.ClientConfig{
		Handlers: rfb.EventHandlers{
			FramebufferUpdate: func(rects []image.Rectangle) {
				s.mu.Lock()
				s.gotFrame = true
				wd := s.watchdog
				s.watchdog = nil
				s.mu.Unlock()
				if wd != nil {
					wd.Stop()
				}
				if s.OnFrame != nil {
					s.OnFrame()
				}
			},
			DesktopSize: func(w, h uint16) {
				if s.OnSize != nil {
					s.OnSize(int(w), int(h))
				}
				// 分辨率变化后按新网格补一轮非增量请求。
				if c := s.Conn(); c != nil {
					_ = requestAllTiles(c)
				}
			},
			Error: func(err error) {
				s.mu.Lock()
				closed := s.closed
				already := s.state == StateReconnecting && s.lastErr != nil
				s.mu.Unlock()
				if closed {
					return
				}
				if !already {
					s.setState(StateReconnecting, err)
				}
				go s.reconnectLoop()
			},
			Cursor: func(hx, hy uint16, img *image.RGBA) {
				if s.OnCursor != nil {
					s.OnCursor(hx, hy, img)
				}
			},
			CutText: func(text string) {
				if s.OnCutText != nil {
					s.OnCutText(text)
				}
			},
		},
	})
	if err != nil {
		s.setState(StateFailed, err)
		return err
	}

	s.mu.Lock()
	s.conn = conn
	s.gotFrame = false
	if s.watchdog != nil {
		s.watchdog.Stop()
	}
	s.watchdog = time.AfterFunc(firstFrameTimeout, func() { s.onFirstFrameTimeout(conn) })
	s.mu.Unlock()

	s.setState(StateConnected, nil)
	// 通知初始尺寸（ServerInit）；wayvnc 只在尺寸变化时发 DesktopSize 伪编码，
	// 不能指望它提供首帧尺寸。
	if s.OnSize != nil {
		s.OnSize(int(conn.Init().Width), int(conn.Init().Height))
	}
	// 首帧：全部小块非增量请求（单条响应 ≤16KB，避开中继丢大消息的故障）。
	if err := requestAllTiles(conn); err != nil {
		return err
	}
	// 增量扫描：轮流请求小块，驱动持续更新。
	go s.sweepLoop(conn)
	return nil
}

// onFirstFrameTimeout 首帧看门狗：握手成功但超时未收到任何画面，
// 判定为中继吞数据（已知故障模式），报错并走重连。
func (s *Session) onFirstFrameTimeout(conn *rfb.Conn) {
	s.mu.Lock()
	if s.closed || s.gotFrame || s.conn != conn {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.setState(StateReconnecting, fmt.Errorf("已连接但未收到画面数据（中继响应超时）"))
	conn.Close()
	go s.reconnectLoop()
}

// tiles 把帧缓冲切成 tileEdge 小块网格。
func tiles(w, h int) []image.Rectangle {
	var ts []image.Rectangle
	for y := 0; y < h; y += tileEdge {
		for x := 0; x < w; x += tileEdge {
			ts = append(ts, image.Rect(x, y, min(x+tileEdge, w), min(y+tileEdge, h)))
		}
	}
	return ts
}

// requestAllTiles 非增量请求全部小块（首帧全量）。
func requestAllTiles(conn *rfb.Conn) error {
	w, h := conn.FramebufferSize()
	for _, t := range tiles(w, h) {
		if err := conn.RequestUpdate(false, t); err != nil {
			return err
		}
	}
	return nil
}

// sweepLoop 增量扫描循环：每 tick 轮流请求一批小块的增量更新，
// 直到连接更换或会话关闭。wayvnc 只为有变化的块回数据，单条消息 ≤16KB。
func (s *Session) sweepLoop(conn *rfb.Conn) {
	tick := time.NewTicker(sweepTick)
	defer tick.Stop()
	pos := 0
	for range tick.C {
		s.mu.Lock()
		if s.closed || s.conn != conn {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		w, h := conn.FramebufferSize()
		ts := tiles(w, h)
		if len(ts) == 0 {
			continue
		}
		for i := 0; i < sweepBatch && i < len(ts); i++ {
			if err := conn.RequestUpdate(true, ts[pos]); err != nil {
				return
			}
			pos = (pos + 1) % len(ts)
		}
	}
}

// reconnectLoop 指数退避重连，直到成功或 Close。可并发调用——
// 只有第一个调用者进入循环（首帧看门狗与 Error 回调可能同时触发）。
func (s *Session) reconnectLoop() {
	s.mu.Lock()
	if s.reconnecting {
		s.mu.Unlock()
		return
	}
	s.reconnecting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.reconnecting = false
		s.mu.Unlock()
	}()

	backoff := time.Second
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		time.Sleep(backoff)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		if backoff < 15*time.Second {
			backoff *= 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.dial(ctx)
		cancel()
		if err == nil {
			return
		}
	}
}

// Reconnect 立即重试（失败状态/用户点重连按钮时）。
func (s *Session) Reconnect(ctx context.Context) error {
	s.mu.Lock()
	s.closed = false
	id := s.machineID
	s.mu.Unlock()
	if id == "" {
		return fmt.Errorf("尚未连接过")
	}
	return s.dial(ctx)
}

// Conn 返回当前 RFB 连接（未连接时 nil）。
func (s *Session) Conn() *rfb.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

// Close 断开并停止重连。
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conn := s.conn
	s.conn = nil
	if s.watchdog != nil {
		s.watchdog.Stop()
		s.watchdog = nil
	}
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	s.setState(StateDisconnected, nil)
}
