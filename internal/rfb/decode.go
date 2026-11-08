package rfb

import (
	"encoding/binary"
	"fmt"
	"image"
)

// rectPayloadLen 计算矩形载荷的字节数，返回 ok=false 表示现有 data 不足以
// 确定长度（上层应等待更多数据）。data 是当前已缓冲的载荷前缀（可能为空）。
func rectPayloadLen(enc int32, w, h int, data []byte) (need int, ok bool, err error) {
	switch enc {
	case encRaw:
		return w * h * bytesPerPixel, true, nil
	case encCopyRect:
		return 4, true, nil
	case encRRE:
		if len(data) < 4 {
			return 0, false, nil
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		// u32 nsubrects + 背景像素(4) + n × (像素4 + x2 + y2 + w2 + h2)
		return 8 + n*12, true, nil
	case encHextile:
		// Hextile 载荷长度只能边解析边确定。
		n, ok := hextileLen(w, h, data)
		if !ok {
			return 0, false, nil
		}
		return n, true, nil
	case encZRLE:
		if len(data) < 4 {
			return 0, false, nil
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		return 4 + n, true, nil
	case encDesktopSize:
		return 0, true, nil
	case encCursor:
		return w*h*bytesPerPixel + ((w+7)/8)*h, true, nil
	default:
		return 0, false, fmt.Errorf("rfb: unsupported encoding %d", enc)
	}
}

// decodeRect 按编码把矩形数据写入帧缓冲。
func (c *Conn) decodeRect(enc int32, rect image.Rectangle, data []byte) error {
	switch enc {
	case encRaw:
		return decodeRaw(c.fb, rect, data)
	case encCopyRect:
		return decodeCopyRect(c.fb, rect, data)
	case encRRE:
		return decodeRRE(c.fb, rect, data)
	case encHextile:
		return decodeHextile(c.fb, rect, data)
	case encZRLE:
		return decodeZRLE(&c.zrle, c.fb, rect, data)
	}
	return fmt.Errorf("rfb: unsupported encoding %d", enc)
}

// decodeRaw：w*h*4 字节 [R,G,B,X] 逐行写入。
func decodeRaw(fb *framebuffer, rect image.Rectangle, data []byte) error {
	w, h := rect.Dx(), rect.Dy()
	if len(data) < w*h*bytesPerPixel {
		return fmt.Errorf("rfb: raw rect too short: %d < %d", len(data), w*h*bytesPerPixel)
	}
	for y := 0; y < h; y++ {
		row := data[y*w*bytesPerPixel : (y+1)*w*bytesPerPixel]
		fb.blitRow(rect.Min.X, rect.Min.Y+y, w, row)
	}
	return nil
}

// decodeCopyRect：4 字节源坐标，整体拷贝。
func decodeCopyRect(fb *framebuffer, rect image.Rectangle, data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("rfb: copyrect too short")
	}
	srcX := int(binary.BigEndian.Uint16(data[0:2]))
	srcY := int(binary.BigEndian.Uint16(data[2:4]))
	fb.mu.Lock()
	defer fb.mu.Unlock()
	b := fb.img.Bounds()
	dst := rect.Intersect(b)
	if dst.Empty() {
		return nil
	}
	for y := 0; y < dst.Dy(); y++ {
		si := fb.img.PixOffset(srcX+dst.Min.X-rect.Min.X, srcY+dst.Min.Y-rect.Min.Y+y)
		di := fb.img.PixOffset(dst.Min.X, dst.Min.Y+y)
		copy(fb.img.Pix[di:di+dst.Dx()*4], fb.img.Pix[si:si+dst.Dx()*4])
	}
	return nil
}

// decodeRRE：u32 nsubrects + bg 色 + n 个 (像素,xywh) 子矩形。
func decodeRRE(fb *framebuffer, rect image.Rectangle, data []byte) error {
	if len(data) < 8 {
		return fmt.Errorf("rfb: rre too short")
	}
	n := int(binary.BigEndian.Uint32(data[0:4]))
	bg := data[4:8]
	need := 8 + n*12
	if len(data) < need {
		return fmt.Errorf("rfb: rre rect %d too short: %d < %d", n, len(data), need)
	}
	fb.fillRect(rect, bg[0], bg[1], bg[2])
	w, h := rect.Dx(), rect.Dy()
	for i := 0; i < n; i++ {
		off := 8 + i*12
		px := data[off : off+4]
		x := int(binary.BigEndian.Uint16(data[off+4 : off+6]))
		y := int(binary.BigEndian.Uint16(data[off+6 : off+8]))
		rw := int(binary.BigEndian.Uint16(data[off+8 : off+10]))
		rh := int(binary.BigEndian.Uint16(data[off+10 : off+12]))
		if x+rw > w || y+rh > h {
			return fmt.Errorf("rfb: rre subrect out of bounds")
		}
		fb.fillRect(image.Rect(rect.Min.X+x, rect.Min.Y+y, rect.Min.X+x+rw, rect.Min.Y+y+rh), px[0], px[1], px[2])
	}
	return nil
}

// Hextile 子矩形掩码位。
const (
	hexRaw        = 1
	hexBackground = 2
	hexForeground = 4
	hexSubrects   = 8
	hexColored    = 16
)

// hextileLen 预解析 Hextile 载荷总长度；数据不足返回 ok=false。
func hextileLen(w, h int, data []byte) (int, bool) {
	pos := 0
	u8 := func() (uint8, bool) {
		if pos+1 > len(data) {
			return 0, false
		}
		v := data[pos]
		pos++
		return v, true
	}
	for ty := 0; ty < h; ty += 16 {
		for tx := 0; tx < w; tx += 16 {
			tw, th := 16, 16
			if w-tx < 16 {
				tw = w - tx
			}
			if h-ty < 16 {
				th = h - ty
			}
			sub, ok := u8()
			if !ok {
				return 0, false
			}
			need := func(n int) bool {
				if pos+n > len(data) {
					return false
				}
				pos += n
				return true
			}
			if sub&hexBackground != 0 && !need(4) {
				return 0, false
			}
			if sub&hexForeground != 0 && !need(4) {
				return 0, false
			}
			if sub&hexRaw != 0 {
				if !need(tw * th * bytesPerPixel) {
					return 0, false
				}
				continue
			}
			if sub&hexSubrects == 0 {
				continue
			}
			nc, ok := u8()
			if !ok {
				return 0, false
			}
			n := int(nc)
			if n == 255 {
				if pos+2 > len(data) {
					return 0, false
				}
				n = int(binary.BigEndian.Uint16(data[pos:]))
				pos += 2
			}
			per := 2
			if sub&hexColored != 0 {
				per = 6
			}
			if !need(n * per) {
				return 0, false
			}
		}
	}
	return pos, true
}

// decodeHextile：16x16 瓦片流（Hextile 瓦片固定 16x16）。像素为 4 字节 [R,G,B,X]。
func decodeHextile(fb *framebuffer, rect image.Rectangle, data []byte) error {
	r := newByteReader(data)
	bw, bh := rect.Dx(), rect.Dy()
	var bg, fg [4]uint8
	for ty := 0; ty < bh; ty += 16 {
		for tx := 0; tx < bw; tx += 16 {
			tw, th := 16, 16
			if bw-tx < 16 {
				tw = bw - tx
			}
			if bh-ty < 16 {
				th = bh - ty
			}
			sub, err := r.u8()
			if err != nil {
				return fmt.Errorf("rfb: hextile truncated at tile %d,%d", tx, ty)
			}
			if sub&hexBackground != 0 {
				if bg, err = r.pixel(); err != nil {
					return fmt.Errorf("rfb: hextile bg truncated")
				}
			}
			if sub&hexForeground != 0 {
				if fg, err = r.pixel(); err != nil {
					return fmt.Errorf("rfb: hextile fg truncated")
				}
			}
			if sub&hexRaw != 0 {
				for y := 0; y < th; y++ {
					row, err := r.bytes(tw * bytesPerPixel)
					if err != nil {
						return fmt.Errorf("rfb: hextile raw row truncated")
					}
					fb.blitRow(rect.Min.X+tx, rect.Min.Y+ty+y, tw, row)
				}
				continue
			}
			// 背景填充整个瓦片
			fb.fillRect(image.Rect(rect.Min.X+tx, rect.Min.Y+ty, rect.Min.X+tx+tw, rect.Min.Y+ty+th), bg[0], bg[1], bg[2])
			if sub&hexSubrects == 0 {
				continue
			}
			nb, err := r.u8()
			if err != nil {
				return fmt.Errorf("rfb: hextile nsubrects truncated")
			}
			n := int(nb)
			if n == 255 {
				hi, err := r.u16()
				if err != nil {
					return fmt.Errorf("rfb: hextile nsubrects16 truncated")
				}
				n = int(hi)
			}
			for i := 0; i < n; i++ {
				if sub&hexColored != 0 {
					if fg, err = r.pixel(); err != nil {
						return fmt.Errorf("rfb: hextile subrect pixel truncated")
					}
				}
				xy, err := r.u8()
				if err != nil {
					return fmt.Errorf("rfb: hextile subrect pos truncated")
				}
				wh, err := r.u8()
				if err != nil {
					return fmt.Errorf("rfb: hextile subrect size truncated")
				}
				sx, sy := int(xy>>4), int(xy&0xF)
				sw, sh := int(wh>>4)+1, int(wh&0xF)+1
				if sx+sw > tw || sy+sh > th {
					return fmt.Errorf("rfb: hextile subrect out of tile bounds")
				}
				fb.fillRect(image.Rect(rect.Min.X+tx+sx, rect.Min.Y+ty+sy, rect.Min.X+tx+sx+sw, rect.Min.Y+ty+sy+sh), fg[0], fg[1], fg[2])
			}
		}
	}
	return nil
}

// decodeCursor 解析 Cursor 伪编码矩形：w*h 个 [R,G,B,X] 像素 + 位掩码（MSB 先行，
// 1=有效像素）。返回 RGBA（无效像素 Alpha=0）；w 或 h 为 0 时返回 nil（系统光标）。
func decodeCursor(w, h int, data []byte) *image.RGBA {
	if w == 0 || h == 0 {
		return nil
	}
	maskLen := ((w + 7) / 8) * h
	need := w*h*bytesPerPixel + maskLen
	if len(data) < need {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	mask := data[w*h*bytesPerPixel:]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			p := data[i*4:]
			valid := mask[y*((w+7)/8)+x/8]&(0x80>>uint(x%8)) != 0
			a := uint8(0)
			if valid {
				a = 0xFF
			}
			img.Pix[i*4] = p[0]
			img.Pix[i*4+1] = p[1]
			img.Pix[i*4+2] = p[2]
			img.Pix[i*4+3] = a
		}
	}
	return img
}

// byteReader 是解码用的字节游标。
type byteReader struct {
	data []byte
	pos  int
}

func newByteReader(data []byte) *byteReader { return &byteReader{data: data} }

func (r *byteReader) u8() (uint8, error) {
	if r.pos+1 > len(r.data) {
		return 0, fmt.Errorf("eof")
	}
	v := r.data[r.pos]
	r.pos++
	return v, nil
}

func (r *byteReader) u16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, fmt.Errorf("eof")
	}
	v := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *byteReader) u32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, fmt.Errorf("eof")
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *byteReader) bytes(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, fmt.Errorf("eof")
	}
	v := r.data[r.pos : r.pos+n]
	r.pos += n
	return v, nil
}

// pixel 读一个 4 字节像素，返回 [R,G,B,X]。
func (r *byteReader) pixel() ([4]uint8, error) {
	var p [4]uint8
	b, err := r.bytes(bytesPerPixel)
	if err != nil {
		return p, err
	}
	copy(p[:], b)
	return p, nil
}
