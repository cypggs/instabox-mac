package rfb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"sync"
)

// 编码号（rfbproto 注册表子集）。
const (
	encCopyRect    int32 = 1
	encRRE         int32 = 2
	encHextile     int32 = 5
	encZRLE        int32 = 16
	encRaw         int32 = 0
	encDesktopSize int32 = -223
	encCursor      int32 = -239
)

// bytesPerPixel 是我们请求像素格式（bpp32/depth24，R/G/B 各占一字节）的像素字节数。
const bytesPerPixel = 4

// ErrClosed 连接已关闭。
var ErrClosed = errors.New("rfb: connection closed")

// HandshakeError 握手阶段失败。
type HandshakeError struct {
	Stage  string
	Detail string
}

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("rfb: handshake %s: %s", e.Stage, e.Detail)
}

// ServerInit 是 RFB ServerInit 消息的内容。
type ServerInit struct {
	Width  uint16
	Height uint16
	Name   string
}

// EventHandlers 是服务端事件的回调集合，全部在 Conn 的读 goroutine 里调用。
// 回调必须快速返回；重活请丢给别的 goroutine。
// FramebufferUpdate 在所有矩形解码完成后回调一次，rects 为脏区域。
// Cursor 回调 cursor 形状变化：img 为 nil 表示恢复系统光标；否则 img 是指定 hot 点的 RGBA。
// Error 在读循环遇到致命错误时回调一次（随后连接关闭）。
type EventHandlers struct {
	FramebufferUpdate func(rects []image.Rectangle)
	DesktopSize       func(width, height uint16)
	Cursor            func(hotX, hotY uint16, img *image.RGBA)
	CutText           func(text string)
	Bell              func()
	Error             func(err error)
}

// Conn 是一条已握手的 RFB 3.8 连接。
//
// 线程模型：内部一个读 goroutine 驱动 EventHandlers；发送方法
// （SendKey/SendPointer/RequestUpdate/SendCutText）可从任意 goroutine 调用，
// 内部串行化。连接断开后所有发送返回 ErrClosed。
type Conn struct {
	t    Transport
	fb   *framebuffer
	init ServerInit

	h    EventHandlers
	once sync.Once
	done chan struct{}
	wmu  sync.Mutex // 发送串行化
	err  error

	// handBuf 是握手阶段读超出的粘滞字节（一条消息长于本次读取需求时
	// 的剩余部分），读完交给 readLoop 继续解析。
	handBuf []byte

	// zrle 是 ZRLE 流状态（wayvnc 用持久流 + sync-flush，见 zrle.go）。
	zrle zrleState
}

// ClientConfig 配置 Dial。
type ClientConfig struct {
	// Handlers 事件回调（可部分为 nil）。
	Handlers EventHandlers
	// Encodings 覆盖默认编码协商顺序（高级用法，一般留空）。
	Encodings []int32
}

// Dial 在 transport 上完成 RFB 3.8 握手（None 认证）并返回就绪连接。
// transport 必须已完成 WebSocket 等底层升级。
//
// 握手流程（客户端先说版本）：
//
//	客户端: "RFB 003.008\n"
//	服务端: "RFB 003.008\n" + 安全类型列表
//	客户端: 选择 None(1)
//	服务端: SecurityResult
//	客户端: ClientInit(shared=1) → 服务端: ServerInit
//	客户端: SetPixelFormat + SetEncodings
func Dial(ctx context.Context, t Transport, cfg ClientConfig) (*Conn, error) {
	c := &Conn{
		t:    t,
		h:    cfg.Handlers,
		done: make(chan struct{}),
	}

	// --- 协议版本 ---
	if err := c.writeRaw([]byte("RFB 003.008\n")); err != nil {
		return nil, &HandshakeError{Stage: "version-send", Detail: err.Error()}
	}
	ver := make([]byte, 12)
	if _, err := c.readFull(ctx, ver); err != nil {
		return nil, &HandshakeError{Stage: "version-read", Detail: err.Error()}
	}
	if string(ver[:3]) != "RFB" {
		return nil, &HandshakeError{Stage: "version-read", Detail: fmt.Sprintf("bad version %q", ver)}
	}

	// --- 安全类型（RFB 3.7+：服务端列表，客户端选择） ---
	numBuf := make([]byte, 1)
	if _, err := c.readFull(ctx, numBuf); err != nil {
		return nil, &HandshakeError{Stage: "security-types", Detail: err.Error()}
	}
	if numBuf[0] == 0 {
		reason := c.readReasonString(ctx)
		return nil, &HandshakeError{Stage: "security-types", Detail: "server offered no types: " + reason}
	}
	types := make([]byte, numBuf[0])
	if _, err := c.readFull(ctx, types); err != nil {
		return nil, &HandshakeError{Stage: "security-types", Detail: err.Error()}
	}
	offeredNone := false
	for _, ty := range types {
		if ty == 1 {
			offeredNone = true
			break
		}
	}
	if !offeredNone {
		return nil, &HandshakeError{Stage: "security-types", Detail: fmt.Sprintf("None(1) not offered, server types: %v", types)}
	}
	if err := c.writeRaw([]byte{1}); err != nil {
		return nil, &HandshakeError{Stage: "security-select", Detail: err.Error()}
	}

	// --- SecurityResult ---
	res := make([]byte, 4)
	if _, err := c.readFull(ctx, res); err != nil {
		return nil, &HandshakeError{Stage: "security-result", Detail: err.Error()}
	}
	if v := binary.BigEndian.Uint32(res); v != 0 {
		reason := c.readReasonString(ctx)
		return nil, &HandshakeError{Stage: "security-result", Detail: fmt.Sprintf("status %d: %s", v, reason)}
	}

	// --- ClientInit ---
	if err := c.writeRaw([]byte{1}); err != nil {
		return nil, &HandshakeError{Stage: "client-init", Detail: err.Error()}
	}

	// --- ServerInit ---
	head := make([]byte, 24)
	if _, err := c.readFull(ctx, head); err != nil {
		return nil, &HandshakeError{Stage: "server-init", Detail: err.Error()}
	}
	c.init.Width = binary.BigEndian.Uint16(head[0:2])
	c.init.Height = binary.BigEndian.Uint16(head[2:4])
	nameLen := binary.BigEndian.Uint32(head[20:24])
	if nameLen > 1<<20 {
		return nil, &HandshakeError{Stage: "server-init", Detail: fmt.Sprintf("name too long: %d", nameLen)}
	}
	name := make([]byte, nameLen)
	if _, err := c.readFull(ctx, name); err != nil {
		return nil, &HandshakeError{Stage: "server-init", Detail: err.Error()}
	}
	c.init.Name = string(name)

	c.fb = newFramebuffer(int(c.init.Width), int(c.init.Height))

	// --- SetPixelFormat：bpp32/depth24/LE，R:G:B = 字节 0:1:2 ---
	// 该格式下 wire 像素 [R,G,B,X]，解码免交换；ZRLE 的 CPIXEL 为其低 3 字节 [R,G,B]。
	// PIXEL_FORMAT 布局：bpp/depth/endian/truecolour 各 1 字节，rgb-max 各 2 字节，shift 各 1 字节。
	spf := make([]byte, 20)
	spf[0] = 0 // message-type
	// spf[1..3] padding
	spf[4] = 32 // bits-per-pixel
	spf[5] = 24 // depth
	spf[6] = 0  // big-endian-flag
	spf[7] = 1  // true-colour-flag
	binary.BigEndian.PutUint16(spf[8:10], 255)  // red-max
	binary.BigEndian.PutUint16(spf[10:12], 255) // green-max
	binary.BigEndian.PutUint16(spf[12:14], 255) // blue-max
	spf[14] = 0                                 // red-shift
	spf[15] = 8                                 // green-shift
	spf[16] = 16                                // blue-shift
	// spf[17..19] padding
	if err := c.writeRaw(spf); err != nil {
		return nil, &HandshakeError{Stage: "set-pixelformat", Detail: err.Error()}
	}

	// --- SetEncodings ---
	encs := cfg.Encodings
	if encs == nil {
		encs = []int32{encZRLE, encHextile, encRRE, encCopyRect, encRaw, encCursor, encDesktopSize}
	}
	if err := c.sendSetEncodings(encs); err != nil {
		return nil, &HandshakeError{Stage: "set-encodings", Detail: err.Error()}
	}

	go c.readLoop()
	return c, nil
}

// readFull 从 transport 按字节流语义读满 buf：先吃粘滞缓冲，再逐条消息拼接。
// 多出的字节留在 c.handBuf 供下次读取（或交给 readLoop）。
func (c *Conn) readFull(ctx context.Context, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		if len(c.handBuf) > 0 {
			m := copy(buf[n:], c.handBuf)
			n += m
			c.handBuf = c.handBuf[m:]
			continue
		}
		_, data, err := c.t.ReadMessage(ctx)
		if err != nil {
			return n, err
		}
		c.handBuf = data
	}
	return n, nil
}

// readReasonString 读 SecurityResult/失败原因字符串（u32 长度 + UTF-8）。
func (c *Conn) readReasonString(ctx context.Context) string {
	lb := make([]byte, 4)
	if _, err := c.readFull(ctx, lb); err != nil {
		return ""
	}
	n := binary.BigEndian.Uint32(lb)
	if n == 0 || n > 4096 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := c.readFull(ctx, buf); err != nil {
		return ""
	}
	return string(buf)
}

// writeRaw 发送一条消息（写锁）。
func (c *Conn) writeRaw(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	return c.t.WriteMessage(context.Background(), b)
}

// sendSetEncodings 发送 SetEncodings 消息。
func (c *Conn) sendSetEncodings(encs []int32) error {
	msg := make([]byte, 4+4*len(encs))
	msg[0] = 2 // message-type
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(encs)))
	for i, e := range encs {
		binary.BigEndian.PutUint32(msg[4+i*4:], uint32(e))
	}
	return c.writeRaw(msg)
}

// Init 返回 ServerInit 信息。
func (c *Conn) Init() ServerInit { return c.init }

// FramebufferSize 返回当前帧缓冲尺寸（DesktopSize 伪编码会改变它）。
func (c *Conn) FramebufferSize() (int, int) {
	b := c.fb.bounds()
	return b.Dx(), b.Dy()
}

// Snapshot 返回整帧 RGBA 拷贝。
func (c *Conn) Snapshot() *image.RGBA { return c.fb.snapshot() }

// CopyFramebuffer 把当前整帧拷入 dst（Bounds 必须一致；UI 层复用缓冲避免每帧分配）。
func (c *Conn) CopyFramebuffer(dst *image.RGBA) { c.fb.copyInto(dst) }

// --- 客户端消息发送 ---

// SendKey 发送 KeyEvent。
func (c *Conn) SendKey(keysym uint32, down bool) error {
	msg := make([]byte, 8)
	msg[0] = 4
	if down {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[4:], keysym)
	return c.writeRaw(msg)
}

// SendPointer 发送 PointerEvent：mask bit0 左键、bit1 右键、bit2 中键；
// bit3=向上滚一格，bit4=向下，bit5=向左，bit6=向右（瞬时位，下一次 PointerEvent 应清除）。
func (c *Conn) SendPointer(x, y uint16, mask uint8) error {
	msg := make([]byte, 6)
	msg[0] = 5
	msg[1] = mask
	binary.BigEndian.PutUint16(msg[2:4], x)
	binary.BigEndian.PutUint16(msg[4:6], y)
	return c.writeRaw(msg)
}

// RequestUpdate 请求 framebuffer 更新。inc=false 为全量请求（连接后首次），
// 之后用 inc=true 的增量请求：收到 FramebufferUpdate 后再发一次增量全屏请求，
// 即经典的请求-响应轮询模型。
func (c *Conn) RequestUpdate(inc bool, rect image.Rectangle) error {
	msg := make([]byte, 10)
	msg[0] = 3
	if inc {
		msg[1] = 1
	}
	binary.BigEndian.PutUint16(msg[2:4], uint16(rect.Min.X))
	binary.BigEndian.PutUint16(msg[4:6], uint16(rect.Min.Y))
	binary.BigEndian.PutUint16(msg[6:8], uint16(rect.Dx()))
	binary.BigEndian.PutUint16(msg[8:10], uint16(rect.Dy()))
	return c.writeRaw(msg)
}

// RequestFullUpdate 请求整个帧缓冲的更新。
func (c *Conn) RequestFullUpdate(inc bool) error {
	return c.RequestUpdate(inc, c.fb.bounds())
}

// SendCutText 发送 ClientCutText（Latin-1 可表示的文本；无法表示的字符被替换为 '?'）。
// 若服务端协商了 ExtendedClipboard，后续版本可升级；MVP 用基础消息。
func (c *Conn) SendCutText(text string) error {
	msg := make([]byte, 8)
	msg[0] = 6
	b := latin1Bytes(text)
	binary.BigEndian.PutUint32(msg[4:], uint32(len(b)))
	return c.writeRaw(append(msg, b...))
}

func latin1Bytes(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xFF {
			out = append(out, byte(r))
		} else {
			out = append(out, '?')
		}
	}
	return out
}

// latin1String 解码 Latin-1 字节串。
func latin1String(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

// Close 关闭连接并停止读循环。
func (c *Conn) Close() {
	c.closeWithError(ErrClosed)
	_ = c.t.Close(websocketStatusNormal, "client close")
}

func (c *Conn) closeWithError(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
	})
}

// Err 返回导致连接终止的错误（nil 表示正常）。
func (c *Conn) Err() error { return c.err }

// websocketStatusNormal 是正常关闭码（避免直接依赖 websocket 包到处写）。
const websocketStatusNormal = 1000

// --- 读循环与服务端消息解析 ---

// readLoop 读消息并增量解析。缓冲按字节流拼接；解析器在消息不完整时
// 保留剩余缓冲等待下一条消息（消息可能分片到达，也可能多条合并）。
func (c *Conn) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			c.closeWithError(fmt.Errorf("rfb: panic in read loop: %v", r))
		}
		if c.h.Error != nil && c.err != nil && !errors.Is(c.err, ErrClosed) {
			c.h.Error(c.err)
		}
	}()

	var buf []byte
	// 握手阶段可能已读入部分服务端消息（粘滞缓冲），先交给解析循环。
	if len(c.handBuf) > 0 {
		buf = append(buf, c.handBuf...)
		c.handBuf = nil
	}
	ctx := context.Background()
	for {
		select {
		case <-c.done:
			return
		default:
		}
		_, data, err := c.t.ReadMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			c.closeWithError(fmt.Errorf("rfb: read: %w", err))
			return
		}
		buf = append(buf, data...)
		for len(buf) > 0 {
			consumed, perr := c.parseMessage(buf)
			if perr != nil {
				c.closeWithError(perr)
				return
			}
			if consumed == 0 {
				break // 不完整，等下一条消息
			}
			buf = buf[consumed:]
		}
		// 防无界增长：buf 总是消耗到 0 或保留不完整的尾部。
	}
}

// parseMessage 尝试从 buf 头部解析一条服务端消息。
// 返回 (consumed, nil)：成功；返回 (0, nil)：数据不足；
// 返回 (0, err)：协议错误。
func (c *Conn) parseMessage(buf []byte) (int, error) {
	if len(buf) < 1 {
		return 0, nil
	}
	switch buf[0] {
	case 0: // FramebufferUpdate
		return c.parseFramebufferUpdate(buf)
	case 1: // SetColorMapEntries
		if len(buf) < 6 {
			return 0, nil
		}
		n := int(binary.BigEndian.Uint16(buf[4:6]))
		total := 6 + n*6
		if len(buf) < total {
			return 0, nil
		}
		// 真彩模式下不会发生；忽略。
		return total, nil
	case 2: // Bell
		if c.h.Bell != nil {
			c.h.Bell()
		}
		return 1, nil
	case 3: // ServerCutText
		if len(buf) < 8 {
			return 0, nil
		}
		n := int(int32(binary.BigEndian.Uint32(buf[4:8])))
		if n < 0 {
			return 0, fmt.Errorf("rfb: extended ServerCutText not implemented (len %d)", n)
		}
		total := 8 + n
		if len(buf) < total {
			return 0, nil
		}
		if c.h.CutText != nil {
			c.h.CutText(latin1String(buf[8:total]))
		}
		return total, nil
	default:
		return 0, fmt.Errorf("rfb: unknown server message type %d", buf[0])
	}
}

// parseFramebufferUpdate 解析 FramebufferUpdate（type 0 + pad + nrects + rects）。
func (c *Conn) parseFramebufferUpdate(buf []byte) (int, error) {
	if len(buf) < 4 {
		return 0, nil
	}
	nrects := int(binary.BigEndian.Uint16(buf[2:4]))
	pos := 4
	var dirty []image.Rectangle
	for i := 0; i < nrects; i++ {
		if len(buf) < pos+12 {
			return 0, nil
		}
		x := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
		y := int(binary.BigEndian.Uint16(buf[pos+2 : pos+4]))
		w := int(binary.BigEndian.Uint16(buf[pos+4 : pos+6]))
		h := int(binary.BigEndian.Uint16(buf[pos+6 : pos+8]))
		enc := int32(binary.BigEndian.Uint32(buf[pos+8 : pos+12]))
		pos += 12

		need, ok, err := rectPayloadLen(enc, w, h, buf[pos:])
		if err != nil {
			return 0, err
		}
		if !ok || len(buf) < pos+need {
			return 0, nil // 等待更多数据
		}
		data := buf[pos : pos+need]
		pos += need

		switch enc {
		case encDesktopSize:
			c.fb.resize(w, h)
			if c.h.DesktopSize != nil {
				c.h.DesktopSize(uint16(w), uint16(h))
			}
		case encCursor:
			img := decodeCursor(w, h, data)
			if c.h.Cursor != nil {
				c.h.Cursor(uint16(x), uint16(y), img)
			}
		default:
			if err := c.decodeRect(enc, image.Rect(x, y, x+w, y+h), data); err != nil {
				return 0, err
			}
			dirty = append(dirty, image.Rect(x, y, x+w, y+h))
		}
	}
	if c.h.FramebufferUpdate != nil && len(dirty) > 0 {
		c.h.FramebufferUpdate(dirty)
	}
	return pos, nil
}
