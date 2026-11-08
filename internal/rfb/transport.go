// Package rfb 实现 RFB 3.8（VNC）协议客户端的一个子集：
// None 认证、Raw/CopyRect/RRE/Hextile/ZRLE 编码、Cursor/DesktopSize 伪编码、
// 剪贴板（ClientCutText/ServerCutText）。设计为跑在任意 message-oriented
// transport 上（Instabox 的 VNC over WebSocket 中继）。
package rfb

import (
	"context"
	"fmt"
	"io"

	"github.com/coder/websocket"
)

// Transport 是消息导向的传输层：一条 WriteMessage 对应一个 binary 消息
// （noVNC 的讲法），ReadMessage 读到对端发来的完整消息。
// 实现必须满足：并发读、写安全由 Transport 自己保证。
type Transport interface {
	// ReadMessage 读入一条消息。返回的消息类型对 RFB 而言总是 binary。
	ReadMessage(ctx context.Context) (MessageType, []byte, error)
	// WriteMessage 写出一条完整的 RFB 消息。
	WriteMessage(ctx context.Context, data []byte) error
	// Close 关闭底层连接。
	Close(code StatusCode, reason string) error
}

// MessageType 是 WebSocket 消息类型。
type MessageType = websocket.MessageType

const (
	MessageBinary = websocket.MessageBinary
	MessageText   = websocket.MessageText
)

// StatusCode 是 WebSocket 关闭码。
type StatusCode = websocket.StatusCode

// WSTransport 把 coder/websocket 的连接适配成 Transport。
type WSTransport struct {
	conn *websocket.Conn
	// 读循环用 Background；连接关闭后所有阻塞读返回错误。
}

// NewWSTransport 包装一个已建立的 WebSocket 连接。
func NewWSTransport(conn *websocket.Conn) *WSTransport {
	return &WSTransport{conn: conn}
}

// DialWebSocket 建立到给定 URL 的 WebSocket 连接（binary 子协议语义，不协商子协议）。
func DialWebSocket(ctx context.Context, url string, header map[string]string) (Transport, error) {
	opts := &websocket.DialOptions{}
	if len(header) > 0 {
		h := make(map[string][]string, len(header))
		for k, v := range header {
			h[k] = []string{v}
		}
		opts.HTTPHeader = h
	}
	conn, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("rfb: websocket dial %s: HTTP %d: %w", url, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("rfb: websocket dial %s: %w", url, err)
	}
	return NewWSTransport(conn), nil
}

func (t *WSTransport) ReadMessage(ctx context.Context) (MessageType, []byte, error) {
	mt, data, err := t.conn.Read(ctx)
	return mt, data, err
}

func (t *WSTransport) WriteMessage(ctx context.Context, data []byte) error {
	return t.conn.Write(ctx, websocket.MessageBinary, data)
}

func (t *WSTransport) Close(code StatusCode, reason string) error {
	return t.conn.Close(code, reason)
}

// pipeTransport 是测试用的内存 transport（消息语义，逐条传递）。
type pipeTransport struct {
	in     <-chan []byte
	out    chan<- []byte
	closed chan struct{}
}

func newPipeTransport(in <-chan []byte, out chan<- []byte) *pipeTransport {
	return &pipeTransport{in: in, out: out, closed: make(chan struct{})}
}

func (p *pipeTransport) ReadMessage(ctx context.Context) (MessageType, []byte, error) {
	select {
	case <-p.closed:
		return 0, nil, io.ErrClosedPipe
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case msg, ok := <-p.in:
		if !ok {
			return 0, nil, io.EOF
		}
		return MessageBinary, msg, nil
	}
}

func (p *pipeTransport) WriteMessage(_ context.Context, data []byte) error {
	select {
	case <-p.closed:
		return io.ErrClosedPipe
	default:
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return io.ErrClosedPipe
	}
}

func (p *pipeTransport) Close(StatusCode, string) error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}
