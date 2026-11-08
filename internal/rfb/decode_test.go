package rfb

import (
	"bytes"
	"encoding/binary"
	"image"
	"testing"
)

// newTestFB 建一个 w*h 帧缓冲，并用 setTestPixel 便于断言。
func newTestFB(w, h int) *framebuffer { return newFramebuffer(w, h) }

func px(fb *framebuffer, x, y int) [4]uint8 {
	c := fb.snapshot().RGBAAt(x, y)
	return [4]uint8{c.R, c.G, c.B, c.A}
}

func TestDecodeCopyRect(t *testing.T) {
	fb := newTestFB(8, 8)
	fb.fillRect(image.Rect(0, 0, 2, 2), 1, 2, 3)
	// 把 (0,0) 2x2 拷贝到 (4,4)
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data[0:2], 0)
	binary.BigEndian.PutUint16(data[2:4], 0)
	if err := decodeCopyRect(fb, image.Rect(4, 4, 6, 6), data); err != nil {
		t.Fatal(err)
	}
	if got := px(fb, 5, 5); got != [4]uint8{1, 2, 3, 255} {
		t.Fatalf("pixel = %v", got)
	}
}

func TestDecodeRRE(t *testing.T) {
	fb := newTestFB(10, 10)
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(1)) // nsubrects
	b.Write([]byte{9, 9, 9, 0})                   // bg
	b.Write([]byte{200, 100, 50, 0})              // subrect pixel
	binary.Write(&b, binary.BigEndian, uint16(2))
	binary.Write(&b, binary.BigEndian, uint16(3))
	binary.Write(&b, binary.BigEndian, uint16(4))
	binary.Write(&b, binary.BigEndian, uint16(2))
	if err := decodeRRE(fb, image.Rect(0, 0, 10, 10), b.Bytes()); err != nil {
		t.Fatal(err)
	}
	if got := px(fb, 0, 0); got != [4]uint8{9, 9, 9, 255} {
		t.Fatalf("bg = %v", got)
	}
	if got := px(fb, 3, 3); got != [4]uint8{200, 100, 50, 255} {
		t.Fatalf("subrect = %v", got)
	}
	if got := px(fb, 5, 4); got != [4]uint8{200, 100, 50, 255} {
		t.Fatalf("subrect edge = %v", got)
	}
	if got := px(fb, 6, 3); got != [4]uint8{9, 9, 9, 255} {
		t.Fatalf("outside subrect = %v", got)
	}
}

func TestDecodeHextileRawAndSubrects(t *testing.T) {
	fb := newTestFB(16, 16)
	var b bytes.Buffer
	// 瓦片 (0,0)：raw，16x16 全绿
	b.WriteByte(hexRaw)
	for i := 0; i < 16*16; i++ {
		b.Write([]byte{0, 255, 0, 0})
	}
	if err := decodeHextile(fb, image.Rect(0, 0, 16, 16), b.Bytes()); err != nil {
		t.Fatal(err)
	}
	if got := px(fb, 7, 7); got != [4]uint8{0, 255, 0, 255} {
		t.Fatalf("raw tile = %v", got)
	}

	// 瓦片：bg 红 + 两个彩色子矩形
	fb2 := newTestFB(16, 16)
	b.Reset()
	b.WriteByte(hexBackground | hexForeground | hexSubrects | hexColored)
	b.Write([]byte{255, 0, 0, 0}) // bg 红
	b.Write([]byte{0, 0, 255, 0}) // fg 蓝（colored 模式每个子矩形自带像素，这个被忽略但占位）
	b.WriteByte(2)                // 2 子矩形
	b.Write([]byte{0, 0, 255, 0}) // 子矩形1 像素 蓝
	b.WriteByte(0x01)             // x=0 y=1
	b.WriteByte(0x11)             // w=2 h=2
	b.Write([]byte{0, 255, 255, 0}) // 子矩形2 像素 青
	b.WriteByte(0x22)             // x=2 y=2
	b.WriteByte(0x11)             // w=2 h=2
	if err := decodeHextile(fb2, image.Rect(0, 0, 16, 16), b.Bytes()); err != nil {
		t.Fatal(err)
	}
	if got := px(fb2, 0, 0); got != [4]uint8{255, 0, 0, 255} {
		t.Fatalf("bg = %v", got)
	}
	if got := px(fb2, 1, 2); got != [4]uint8{0, 0, 255, 255} {
		t.Fatalf("sub1 = %v", got)
	}
	if got := px(fb2, 3, 3); got != [4]uint8{0, 255, 255, 255} {
		t.Fatalf("sub2 = %v", got)
	}
	// hextileLen 与解码器一致
	if n, ok := hextileLen(16, 16, b.Bytes()); !ok || n != b.Len() {
		t.Fatalf("hextileLen = %d,%v want %d", n, ok, b.Len())
	}
}

func TestDecodeMalformed(t *testing.T) {
	fb := newTestFB(8, 8)
	cases := []struct {
		name string
		enc  int32
		rect image.Rectangle
		data []byte
	}{
		{"raw-short", encRaw, image.Rect(0, 0, 4, 4), make([]byte, 10)},
		{"copyrect-short", encCopyRect, image.Rect(0, 0, 2, 2), make([]byte, 2)},
		{"rre-short", encRRE, image.Rect(0, 0, 4, 4), make([]byte, 4)},
		{"hextile-truncated", encHextile, image.Rect(0, 0, 16, 16), []byte{hexRaw, 1, 2, 3}},
		{"zrle-truncated", encZRLE, image.Rect(0, 0, 8, 8), []byte{0, 0, 0, 100, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := decodeRectForTest(fb, tc.enc, tc.rect, tc.data); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func decodeRectForTest(fb *framebuffer, enc int32, rect image.Rectangle, data []byte) error {
	c := &Conn{fb: fb}
	return c.decodeRect(enc, rect, data)
}

func TestRectPayloadLen(t *testing.T) {
	if n, ok, err := rectPayloadLen(encRaw, 10, 10, nil); !ok || err != nil || n != 400 {
		t.Fatalf("raw: %d %v %v", n, ok, err)
	}
	if _, ok, err := rectPayloadLen(encZRLE, 10, 10, []byte{0, 0}); ok || err != nil {
		t.Fatalf("zrle incomplete should be !ok: %v", ok)
	}
	if _, _, err := rectPayloadLen(999, 10, 10, nil); err == nil {
		t.Fatal("unknown encoding should error")
	}
}
