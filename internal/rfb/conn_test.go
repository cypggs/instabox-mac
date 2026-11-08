package rfb

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeServer 在内存 pipe 上说 RFB 3.8（None 认证），供测试驱动客户端。
type fakeServer struct {
	t          *testing.T
	toClient   chan []byte // 服务端 → 客户端
	fromClient chan []byte // 客户端 → 服务端
	trans      Transport
	closed     chan struct{}
	closeOnce  sync.Once
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	toC := make(chan []byte, 64)
	fromC := make(chan []byte, 64)
	s := &fakeServer{
		t:          t,
		toClient:   toC,
		fromClient: fromC,
		closed:     make(chan struct{}),
	}
	s.trans = &pipeServerTransport{in: fromC, out: toC, closed: s.closed}
	return s
}

type pipeServerTransport struct {
	in     <-chan []byte
	out    chan<- []byte
	closed chan struct{}
}

func (p *pipeServerTransport) ReadMessage(ctx context.Context) (MessageType, []byte, error) {
	select {
	case <-p.closed:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case msg, ok := <-p.in:
		if !ok {
			return 0, nil, io.EOF
		}
		return MessageBinary, msg, nil
	}
}

func (p *pipeServerTransport) WriteMessage(_ context.Context, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return io.ErrClosedPipe
	}
}

func (p *pipeServerTransport) Close(StatusCode, string) error { return nil }

// serveHandshake 执行服务端侧握手（None 认证 + ServerInit），
// 返回握手后客户端发来的消息流读取函数。
func (s *fakeServer) serveHandshake(t *testing.T, w, h uint16, name string) {
	t.Helper()
	ctx := context.Background()
	read := func(n int) []byte {
		t.Helper()
		buf := make([]byte, n)
		got := 0
		for got < n {
			_, data, err := s.trans.ReadMessage(ctx)
			if err != nil {
				t.Fatalf("server read: %v", err)
			}
			got += copy(buf[got:], data)
		}
		return buf
	}
	if v := read(12); string(v) != "RFB 003.008\n" {
		t.Fatalf("client version = %q", v)
	}
	s.trans.WriteMessage(ctx, []byte("RFB 003.008\n"))
	s.trans.WriteMessage(ctx, []byte{1, 1}) // 1 个类型：None
	if sel := read(1); sel[0] != 1 {
		t.Fatalf("client selected security %d", sel[0])
	}
	s.trans.WriteMessage(ctx, []byte{0, 0, 0, 0}) // SecurityResult OK
	if ci := read(1); ci[0] != 1 {
		t.Fatalf("client init shared = %d", ci[0])
	}
	var si bytes.Buffer
	binary.Write(&si, binary.BigEndian, w)
	binary.Write(&si, binary.BigEndian, h)
	pf := make([]byte, 16)
	pf[0], pf[1], pf[2], pf[3] = 32, 24, 0, 1
	binary.BigEndian.PutUint16(pf[4:6], 255)
	binary.BigEndian.PutUint16(pf[6:8], 255)
	binary.BigEndian.PutUint16(pf[8:10], 255)
	pf[10], pf[11], pf[12] = 16, 8, 0 // 服务端自报的格式，客户端不要求匹配
	si.Write(pf)
	nb := make([]byte, 4)
	binary.BigEndian.PutUint32(nb, uint32(len(name)))
	si.Write(nb)
	si.WriteString(name)
	s.trans.WriteMessage(ctx, si.Bytes())
}

// expectSetPixelFormatAndEncodings 读取客户端的 SetPixelFormat 与 SetEncodings。
func (s *fakeServer) expectSetPixelFormatAndEncodings(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	buf := make([]byte, 0, 64)
	read := func(n int) {
		for len(buf) < n {
			_, data, err := s.trans.ReadMessage(ctx)
			if err != nil {
				t.Fatalf("server read: %v", err)
			}
			buf = append(buf, data...)
		}
	}
	read(20)
	if buf[0] != 0 {
		t.Fatalf("expected SetPixelFormat, got type %d", buf[0])
	}
	// 我们请求的像素格式：bpp32 depth24，R shift 0
	if buf[4] != 32 || buf[5] != 24 || buf[6] != 0 || buf[7] != 1 ||
		buf[14] != 0 || buf[15] != 8 || buf[16] != 16 {
		t.Fatalf("unexpected pixel format bytes %v", buf[4:20])
	}
	buf = buf[20:]
	read(4)
	n := int(binary.BigEndian.Uint16(buf[2:4]))
	if buf[0] != 2 {
		t.Fatalf("expected SetEncodings, got type %d", buf[0])
	}
	buf = buf[4:]
	read(4 * n)
	buf = buf[4*n:]
	if len(buf) != 0 {
		t.Fatalf("unexpected %d extra bytes after encodings", len(buf))
	}
}

// sendFramebufferUpdate 发送一条 FramebufferUpdate 消息（载荷自定义）。
func (s *fakeServer) sendMessage(t *testing.T, msg []byte) {
	t.Helper()
	if err := s.trans.WriteMessage(context.Background(), msg); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

// fbUpdateMsg 构造 nrects 个矩形的 FramebufferUpdate 消息。
func fbUpdateMsg(rects ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteByte(0)
	b.WriteByte(0)
	binary.Write(&b, binary.BigEndian, uint16(len(rects)))
	for _, r := range rects {
		b.Write(r)
	}
	return b.Bytes()
}

// rawRect 构造 Raw 编码矩形（w*h 个 [R,G,B,X] 像素，颜色渐变）。
func rawRect(x, y, w, h int, r, g, bl uint8) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(x))
	binary.Write(&b, binary.BigEndian, uint16(y))
	binary.Write(&b, binary.BigEndian, uint16(w))
	binary.Write(&b, binary.BigEndian, uint16(h))
	binary.Write(&b, binary.BigEndian, int32(encRaw))
	for i := 0; i < w*h; i++ {
		b.Write([]byte{r, g, bl, 0})
	}
	return b.Bytes()
}

// zrleRect 构造 ZRLE 编码矩形：把 tiles 函数生成的解压后数据压进 zlib。
// 单瓦片辅助：solidTile 生成纯色瓦片。
func zrleRect(x, y, w, h int, raw []byte) []byte {
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(raw)
	zw.Close()
	zdata := zb.Bytes()
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(x))
	binary.Write(&b, binary.BigEndian, uint16(y))
	binary.Write(&b, binary.BigEndian, uint16(w))
	binary.Write(&b, binary.BigEndian, uint16(h))
	binary.Write(&b, binary.BigEndian, int32(encZRLE))
	binary.Write(&b, binary.BigEndian, uint32(len(zdata)))
	b.Write(zdata)
	return b.Bytes()
}

// zrleRectChunk 构造 ZRLE 矩形，但用调用方给的已压缩块（用于持久流测试）。
func zrleRectChunk(x, y, w, h int, zdata []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(x))
	binary.Write(&b, binary.BigEndian, uint16(y))
	binary.Write(&b, binary.BigEndian, uint16(w))
	binary.Write(&b, binary.BigEndian, uint16(h))
	binary.Write(&b, binary.BigEndian, int32(encZRLE))
	binary.Write(&b, binary.BigEndian, uint32(len(zdata)))
	b.Write(zdata)
	return b.Bytes()
}

// TestZRLEPersistentStream 模拟 wayvnc/neatvnc 的行为：整个更新共用一条
// zlib 流，每个矩形写完后 Z_SYNC_FLUSH（不 Z_FINISH），切成独立的
// zlen 块发送。两条矩形必须在同一条 FramebufferUpdate 里才能解码。
func TestZRLEPersistentStream(t *testing.T) {
	// 压缩端：一条流，两个 solid 瓦片，中间 Flush。
	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write([]byte{1, 255, 0, 0}) // 瓦片 (0,0): solid 红
	zw.Flush()
	chunk1 := append([]byte(nil), zb.Bytes()...)
	zw.Write([]byte{1, 0, 255, 0}) // 瓦片 (64,0): solid 绿
	zw.Flush()
	chunk2 := append([]byte(nil), zb.Bytes()[len(chunk1):]...)
	zw.Close()

	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 128, 64, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		s.sendMessage(t, fbUpdateMsg(
			zrleRectChunk(0, 0, 64, 64, chunk1),
			zrleRectChunk(64, 0, 64, 64, chunk2),
		))
	}()
	gotUpdate := make(chan struct{}, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{FramebufferUpdate: func(rects []image.Rectangle) { gotUpdate <- struct{}{} }},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-gotUpdate:
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
	if c := conn.rgbaAt(10, 10); c != [4]uint8{255, 0, 0, 255} {
		t.Fatalf("rect1 pixel = %+v", c)
	}
	if c := conn.rgbaAt(74, 10); c != [4]uint8{0, 255, 0, 255} {
		t.Fatalf("rect2 pixel = %+v", c)
	}
}

// rreRect 构造 RRE 矩形（真彩 4 字节像素），subs 每项 = [R,G, x,y,w,h]。
func rreRect(x, y, w, h int, bg [3]uint8, subs ...[6]uint16) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(x))
	binary.Write(&b, binary.BigEndian, uint16(y))
	binary.Write(&b, binary.BigEndian, uint16(w))
	binary.Write(&b, binary.BigEndian, uint16(h))
	binary.Write(&b, binary.BigEndian, int32(encRRE))
	binary.Write(&b, binary.BigEndian, uint32(len(subs)))
	b.Write([]byte{bg[0], bg[1], bg[2], 0})
	for _, s := range subs {
		b.Write([]byte{byte(s[0]), byte(s[1]), 0, 0}) // 像素 [R,G,0,0]
		binary.Write(&b, binary.BigEndian, s[2])      // x
		binary.Write(&b, binary.BigEndian, s[3])      // y
		binary.Write(&b, binary.BigEndian, s[4])      // w
		binary.Write(&b, binary.BigEndian, s[5])      // h
	}
	return b.Bytes()
}

// TestRREZeroSubrectsFollowedByRect 覆盖实测踩到的坑：nsubrects=0 的 RRE
// 矩形（纯色填充，8 字节载荷）后面紧跟其他矩形时，rectPayloadLen 少算
// 4 字节会导致整体错位。
func TestRREZeroSubrectsFollowedByRect(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 16, 8, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		s.sendMessage(t, fbUpdateMsg(
			rreRect(0, 0, 8, 8, [3]uint8{10, 20, 30}),
			rawRect(8, 0, 8, 8, 99, 98, 97),
		))
	}()
	gotUpdate := make(chan struct{}, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{FramebufferUpdate: func(rects []image.Rectangle) { gotUpdate <- struct{}{} }},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-gotUpdate:
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
	if c := conn.rgbaAt(4, 4); c != [4]uint8{10, 20, 30, 255} {
		t.Fatalf("rre bg pixel = %+v", c)
	}
	// 紧随其后的 Raw 矩形必须解析到正确位置。
	if c := conn.rgbaAt(12, 4); c != [4]uint8{99, 98, 97, 255} {
		t.Fatalf("raw pixel after rre = %+v", c)
	}
}

func TestDialHandshakeAndRawUpdate(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 100, 80, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		s.sendMessage(t, fbUpdateMsg(rawRect(2, 3, 4, 2, 10, 20, 30)))
	}()

	var mu sync.Mutex
	var dirty []image.Rectangle
	gotUpdate := make(chan struct{}, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{
			FramebufferUpdate: func(rects []image.Rectangle) {
				mu.Lock()
				dirty = append(dirty, rects...)
				mu.Unlock()
				select {
				case gotUpdate <- struct{}{}:
				default:
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if conn.Init().Width != 100 || conn.Init().Height != 80 || conn.Init().Name != "WayVNC" {
		t.Fatalf("init = %+v", conn.Init())
	}

	select {
	case <-gotUpdate:
	case <-time.After(2 * time.Second):
		t.Fatal("no framebuffer update")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dirty) != 1 || dirty[0] != image.Rect(2, 3, 6, 5) {
		t.Fatalf("dirty = %v", dirty)
	}
	img := conn.Snapshot()
	if got := img.RGBAAt(2, 3); got.R != 10 || got.G != 20 || got.B != 30 || got.A != 255 {
		t.Fatalf("pixel = %v", got)
	}
	if got := img.RGBAAt(5, 4); got.R != 10 || got.G != 20 || got.B != 30 || got.A != 255 {
		t.Fatalf("pixel = %v", got)
	}
}

// rgbaAt 抓取指定像素的 RGBA 四元组（测试辅助）。
func (c *Conn) rgbaAt(x, y int) [4]uint8 {
	c8 := c.fb.snapshot().RGBAAt(x, y)
	return [4]uint8{c8.R, c8.G, c8.B, c8.A}
}

// clientSide 把客户端接到 fake server 的 channel 上。
type clientSide struct {
	t *testing.T
	s *fakeServer
}

func (c *clientSide) ReadMessage(ctx context.Context) (MessageType, []byte, error) {
	select {
	case <-c.s.closed:
		return 0, nil, io.EOF
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case msg, ok := <-c.s.toClient:
		if !ok {
			return 0, nil, io.EOF
		}
		return MessageBinary, msg, nil
	}
}

func (c *clientSide) WriteMessage(_ context.Context, data []byte) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	select {
	case c.s.fromClient <- cp:
		return nil
	case <-c.s.closed:
		return io.ErrClosedPipe
	}
}

func (c *clientSide) Close(StatusCode, string) error {
	c.s.closeOnce.Do(func() { close(c.s.closed) })
	return nil
}

func TestDialRejectsMissingNone(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		ctx := context.Background()
		buf := make([]byte, 12)
		s.trans.ReadMessage(ctx)
		copy(buf, "RFB 003.008\n")
		s.trans.WriteMessage(ctx, []byte("RFB 003.008\n"))
		s.trans.WriteMessage(ctx, []byte{1, 2}) // 只提供 VNC Auth
		s.trans.ReadMessage(ctx)
	}()

	_, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{})
	var hs *HandshakeError
	if !errors.As(err, &hs) {
		t.Fatalf("err = %v, want HandshakeError", err)
	}
	if hs.Stage != "security-types" {
		t.Fatalf("stage = %q", hs.Stage)
	}
}

func TestDialSecurityFailureReason(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		ctx := context.Background()
		s.trans.ReadMessage(ctx)
		s.trans.WriteMessage(ctx, []byte("RFB 003.008\n"))
		s.trans.WriteMessage(ctx, []byte{1, 1})
		s.trans.ReadMessage(ctx)
		reason := "too many viewers"
		msg := []byte{0, 0, 0, 1}
		rb := make([]byte, 4)
		binary.BigEndian.PutUint32(rb, uint32(len(reason)))
		msg = append(msg, rb...)
		msg = append(msg, reason...)
		s.trans.WriteMessage(ctx, msg)
	}()

	_, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{})
	var hs *HandshakeError
	if !errors.As(err, &hs) || hs.Stage != "security-result" {
		t.Fatalf("err = %v", err)
	}
	if !contains(hs.Detail, "too many viewers") {
		t.Fatalf("detail = %q", hs.Detail)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func TestIncrementalMessagesAndTruncation(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 32, 32, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		full := fbUpdateMsg(rawRect(0, 0, 8, 8, 1, 2, 3))
		// 分 3 片发送，模拟消息分片
		s.sendMessage(t, full[:5])
		s.sendMessage(t, full[5:40])
		s.sendMessage(t, full[40:])
		// 一条 ServerCutText
		ct := []byte{3, 0, 0, 0, 0, 0, 0, 5}
		ct = append(ct, []byte("hello")...)
		s.sendMessage(t, ct)
		// Bell
		s.sendMessage(t, []byte{2})
		// DesktopSize 伪编码（32x32 → 64x48）
		var ds bytes.Buffer
		binary.Write(&ds, binary.BigEndian, uint16(0))
		binary.Write(&ds, binary.BigEndian, uint16(0))
		binary.Write(&ds, binary.BigEndian, uint16(64))
		binary.Write(&ds, binary.BigEndian, uint16(48))
		binary.Write(&ds, binary.BigEndian, int32(encDesktopSize))
		s.sendMessage(t, fbUpdateMsg(ds.Bytes()))
	}()

	var texts []string
	bells := 0
	sizes := make(chan image.Rectangle, 4)
	errCh := make(chan error, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{
			CutText: func(s string) { texts = append(texts, s) },
			Bell:    func() { bells++ },
			DesktopSize: func(w, h uint16) {
				sizes <- image.Rectangle{Max: image.Pt(int(w), int(h))}
			},
			Error: func(err error) { errCh <- err },
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	select {
	case sz := <-sizes:
		if sz.Max != image.Pt(64, 48) {
			t.Fatalf("size = %v", sz)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for events")
	}
	if len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("texts = %v", texts)
	}
	if bells != 1 {
		t.Fatalf("bells = %d", bells)
	}
	w, h := conn.FramebufferSize()
	if w != 64 || h != 48 {
		t.Fatalf("fb size = %dx%d", w, h)
	}
	// 像素仍来自第一个更新（Raw rect 在 resize 前写入；resize 丢弃内容——
	// 但这里 rect 先于 resize 到达，内容被丢弃是预期行为；只验证不 panic）。
	_ = conn.Snapshot()
}

func TestZRSolidTile(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 64, 64, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		// 单瓦片 64x64，solid 红色
		s.sendMessage(t, fbUpdateMsg(zrleRect(0, 0, 64, 64, []byte{1, 255, 0, 0})))
	}()
	gotUpdate := make(chan struct{}, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{
			FramebufferUpdate: func(rects []image.Rectangle) {
				gotUpdate <- struct{}{}
			},
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-gotUpdate:
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
	c := conn.rgbaAt(10, 10)
	if c != [4]uint8{255, 0, 0, 255} {
		t.Fatalf("pixel = %+v", c)
	}
}

func TestZRLEPackedPaletteAndRLE(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 64, 64, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		var raw bytes.Buffer
		// 瓦片1 (0,0): packed palette 2 色（黑/白棋盘 8 像素宽 4 行）
		raw.WriteByte(2) // subencoding=2: 2 色调色板
		raw.Write([]byte{0, 0, 0})       // 调色板0 黑
		raw.Write([]byte{255, 255, 255}) // 调色板1 白
		// 瓦片 64x64，1bpp 行对齐 8 字节/行，行图案 10101010 = 0xAA
		for y := 0; y < 64; y++ {
			for x := 0; x < 8; x++ {
				raw.WriteByte(0xAA)
			}
		}
		s.sendMessage(t, fbUpdateMsg(zrleRect(0, 0, 64, 64, raw.Bytes())))
	}()
	gotUpdate := make(chan struct{}, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{FramebufferUpdate: func(rects []image.Rectangle) { gotUpdate <- struct{}{} }},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case <-gotUpdate:
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
	// 行 y=0: x 偶=白(1) x 奇=黑(0)，按 0xAA = 10101010 MSB 先行 → x0=1 白
	if c := conn.rgbaAt(0, 0); c != [4]uint8{255, 255, 255, 255} {
		t.Fatalf("(0,0) = %+v", c)
	}
	if c := conn.rgbaAt(1, 0); c != [4]uint8{0, 0, 0, 255} {
		t.Fatalf("(1,0) = %+v", c)
	}
	if c := conn.rgbaAt(2, 3); c != [4]uint8{255, 255, 255, 255} {
		t.Fatalf("(2,3) = %+v", c)
	}
}

func TestCursorPseudoEncoding(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 32, 32, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		// 2x1 光标，hot(0,0)，像素红、绿，掩码 0b11000000
		var r bytes.Buffer
		binary.Write(&r, binary.BigEndian, uint16(0))
		binary.Write(&r, binary.BigEndian, uint16(0))
		binary.Write(&r, binary.BigEndian, uint16(2))
		binary.Write(&r, binary.BigEndian, uint16(1))
		binary.Write(&r, binary.BigEndian, int32(encCursor))
		r.Write([]byte{255, 0, 0, 0, 0, 255, 0, 0})
		r.WriteByte(0xC0)
		s.sendMessage(t, fbUpdateMsg(r.Bytes()))
		// 空光标（系统光标）
		var r2 bytes.Buffer
		binary.Write(&r2, binary.BigEndian, uint16(0))
		binary.Write(&r2, binary.BigEndian, uint16(0))
		binary.Write(&r2, binary.BigEndian, uint16(0))
		binary.Write(&r2, binary.BigEndian, uint16(0))
		binary.Write(&r2, binary.BigEndian, int32(encCursor))
		s.sendMessage(t, fbUpdateMsg(r2.Bytes()))
	}()
	cursors := make(chan *image.RGBA, 2)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{Cursor: func(hx, hy uint16, img *image.RGBA) { cursors <- img }},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case img := <-cursors:
		if img == nil || img.Bounds() != image.Rect(0, 0, 2, 1) {
			t.Fatalf("cursor = %v", img)
		}
		if img.RGBAAt(0, 0).R != 255 || img.RGBAAt(1, 0).G != 255 {
			t.Fatalf("cursor pixels = %v %v", img.RGBAAt(0, 0), img.RGBAAt(1, 0))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cursor")
	}
	select {
	case img := <-cursors:
		if img != nil {
			t.Fatalf("expected nil cursor, got %v", img.Bounds())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no nil cursor")
	}
}

func TestServerCutTextFragmented(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 16, 16, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		ct := []byte{3, 0, 0, 0, 0, 0, 0, 11}
		ct = append(ct, []byte("hello world")...)
		half := len(ct) / 2
		s.sendMessage(t, ct[:half])
		s.sendMessage(t, ct[half:])
	}()
	texts := make(chan string, 1)
	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{
		Handlers: EventHandlers{CutText: func(s string) { texts <- s }},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	select {
	case s := <-texts:
		if s != "hello world" {
			t.Fatalf("text = %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cut text")
	}
}

func TestSendMessages(t *testing.T) {
	s := newFakeServer(t)
	go func() {
		s.serveHandshake(t, 16, 16, "WayVNC")
		s.expectSetPixelFormatAndEncodings(t)
		// 读 KeyEvent / PointerEvent / CutText / FBUpdateRequest
		ctx := context.Background()
		_, key, _ := s.trans.ReadMessage(ctx)
		if key[0] != 4 || key[1] != 1 || binary.BigEndian.Uint32(key[4:]) != 0xFF0D {
			t.Errorf("keyevent = %v", key)
		}
		_, ptr, _ := s.trans.ReadMessage(ctx)
		if ptr[0] != 5 || ptr[1] != 0x3 || binary.BigEndian.Uint16(ptr[2:]) != 7 || binary.BigEndian.Uint16(ptr[4:]) != 9 {
			t.Errorf("pointer = %v", ptr)
		}
		_, ct, _ := s.trans.ReadMessage(ctx)
		if ct[0] != 6 || string(ct[8:]) != "h\x69" {
			t.Errorf("cuttext = %v", ct)
		}
		_, ur, _ := s.trans.ReadMessage(ctx)
		if ur[0] != 3 || ur[1] != 1 {
			t.Errorf("updatereq = %v", ur)
		}
	}()

	conn, err := Dial(context.Background(), &clientSide{t: t, s: s}, ClientConfig{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SendKey(0xFF0D, true); err != nil {
		t.Fatal(err)
	}
	if err := conn.SendPointer(7, 9, 0x3); err != nil {
		t.Fatal(err)
	}
	if err := conn.SendCutText("hi"); err != nil {
		t.Fatal(err)
	}
	if err := conn.RequestUpdate(true, image.Rect(0, 0, 16, 16)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
}
