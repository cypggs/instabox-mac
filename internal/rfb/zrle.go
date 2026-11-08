package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"io"
)

// ZRLE 解码（rfbproto.rst "ZRLE Encoding"）：
// 矩形载荷 = u32 zlibData 长度 + zlib 流。解压后是 64x64 瓦片序列，
// 每瓦片 1 字节 subencoding：高位=是否 RLE，低 7 位=调色板大小。
// CPIXEL = 3 字节 [R,G,B]（truecolor bpp32 depth24 时取低 3 字节）。
//
// 流模式的兼容处理（实测 wayvnc/neatvnc）：
// neatvnc 为整个连接维护一条 zlib 流，每个矩形写完后 Z_SYNC_FLUSH 而
// 不做 Z_FINISH，因此单个矩形的流未收尾（io.ErrUnexpectedEOF），且不能
// 用"每矩形新建 reader"的方式解码第二条及以后的矩形。TigerVNC/RealVNC
// 则每矩形一条完整流。zrleState 先按持久流累积解码，一旦持久解码失败
// 就回退到每矩形独立流模式。

const zrleTileSize = 64

// zrleHistoryCap 持久流累积的压缩字节上限。wayvnc/neatvnc 的 ZRLE 流
// 跨更新持续（只在连接建立时有一个 zlib 头），无法安全重同步；超过上限
// 返回错误让上层断开重连（Session 的指数退避自动处理）， bounded memory。
const zrleHistoryCap = 8 << 20

// zrleState 一条连接上的 ZRLE 流状态。
type zrleState struct {
	comp    []byte // 持久流模式：累积的压缩字节
	decomp  []byte // 持久流模式：累积的解压结果（前缀稳定，增量即当前矩形）
	perRect bool   // 已确认服务端用每矩形独立流
}

// inflate 解压一个矩形的 zlen+chunk，返回该矩形的解压字节。
func (z *zrleState) inflate(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("rfb: zrle too short")
	}
	zlen := int(binary.BigEndian.Uint32(data[:4]))
	if len(data) < 4+zlen {
		return nil, fmt.Errorf("rfb: zrle data truncated: %d < %d", len(data), 4+zlen)
	}
	chunk := data[4 : 4+zlen]

	if z.perRect {
		return inflateChunk(chunk)
	}
	if len(z.comp) > zrleHistoryCap {
		return nil, fmt.Errorf("rfb: zrle stream history exceeds %d bytes, reconnecting", zrleHistoryCap)
	}

	// 持久流：累积压缩字节后从头重建 reader（解压前缀是稳定的）。
	prev := len(z.decomp)
	z.comp = append(z.comp, chunk...)
	r, err := zlib.NewReader(bytes.NewReader(z.comp))
	if err != nil {
		// 服务端可能是每矩形独立流：回退重试。
		if b, ferr := inflateChunk(chunk); ferr == nil {
			z.perRect = true
			z.comp = nil
			z.decomp = nil
			return b, nil
		}
		return nil, fmt.Errorf("rfb: zrle zlib: %w", err)
	}
	raw, rerr := io.ReadAll(r)
	_ = r.Close()
	if rerr != nil && rerr != io.ErrUnexpectedEOF {
		// 流损坏：服务端很可能是每矩形独立流，回退。
		if b, ferr := inflateChunk(chunk); ferr == nil {
			z.perRect = true
			z.comp = nil
			z.decomp = nil
			return b, nil
		}
		return nil, fmt.Errorf("rfb: zrle inflate: %w", rerr)
	}
	if len(raw) < prev {
		return nil, fmt.Errorf("rfb: zrle decompressed shrank: %d < %d", len(raw), prev)
	}
	z.decomp = raw
	return raw[prev:], nil
}

// inflateChunk 解压一条独立完整的 zlib 流（容忍未 Z_FINISH 的 sync-flush 流）。
func inflateChunk(chunk []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(chunk))
	if err != nil {
		return nil, fmt.Errorf("rfb: zrle zlib: %w", err)
	}
	raw, rerr := io.ReadAll(r)
	_ = r.Close()
	if rerr != nil && rerr != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("rfb: zrle inflate: %w", rerr)
	}
	return raw, nil
}

func decodeZRLE(z *zrleState, fb *framebuffer, rect image.Rectangle, data []byte) error {
	raw, err := z.inflate(data)
	if err != nil {
		return err
	}

	r := newByteReader(raw)
	bw, bh := rect.Dx(), rect.Dy()
	for ty := 0; ty < bh; ty += zrleTileSize {
		for tx := 0; tx < bw; tx += zrleTileSize {
			tw, th := zrleTileSize, zrleTileSize
			if bw-tx < zrleTileSize {
				tw = bw - tx
			}
			if bh-ty < zrleTileSize {
				th = bh - ty
			}
			if err := decodeZTile(fb, r, rect.Min.X+tx, rect.Min.Y+ty, tw, th); err != nil {
				return fmt.Errorf("rfb: zrle tile %d,%d: %w", tx, ty, err)
			}
		}
	}
	return nil
}

// cpixel 读一个 3 字节 CPIXEL [R,G,B]，补 Alpha=0xFF。
func (r *byteReader) cpixel() ([4]uint8, error) {
	var p [4]uint8
	b, err := r.bytes(3)
	if err != nil {
		return p, err
	}
	p[0], p[1], p[2] = b[0], b[1], b[2]
	p[3] = 0xFF
	return p, nil
}

// zrun 读 ZRLE 变长 run 长度：1 + Σ（连续 255 继续）。
func (r *byteReader) zrun() (int, error) {
	sum := 0
	for {
		b, err := r.u8()
		if err != nil {
			return 0, err
		}
		sum += int(b)
		if b != 255 {
			return sum + 1, nil
		}
	}
}

func decodeZTile(fb *framebuffer, r *byteReader, dx, dy, tw, th int) error {
	sub, err := r.u8()
	if err != nil {
		return err
	}
	area := tw * th
	switch {
	case sub == 0:
		// Raw：tw*th 个 CPIXEL，逐行
		for y := 0; y < th; y++ {
			for x := 0; x < tw; x++ {
				p, err := r.cpixel()
				if err != nil {
					return err
				}
				fb.setRGBA(dx+x, dy+y, p[0], p[1], p[2])
			}
		}
	case sub == 1:
		// Solid
		p, err := r.cpixel()
		if err != nil {
			return err
		}
		fb.fillRect(image.Rect(dx, dy, dx+tw, dy+th), p[0], p[1], p[2])
	case sub >= 2 && sub <= 16:
		// Packed palette：调色板 sub 色，然后位打包索引（MSB 先行，行按字节对齐）
		palette := make([][4]uint8, sub)
		for i := range palette {
			if palette[i], err = r.cpixel(); err != nil {
				return err
			}
		}
		var bpp int // 每像素位数
		switch {
		case sub == 2:
			bpp = 1
		case sub <= 4:
			bpp = 2
		default:
			bpp = 4
		}
		rowBytes := (tw*bpp + 7) / 8
		for y := 0; y < th; y++ {
			row, err := r.bytes(rowBytes)
			if err != nil {
				return err
			}
			bit := 0
			for x := 0; x < tw; x++ {
				idx := 0
				for j := 0; j < bpp; j++ {
					idx = idx<<1 | int((row[bit/8]>>uint(7-bit%8))&1)
					bit++
				}
				if idx >= len(palette) {
					return fmt.Errorf("palette index %d >= %d", idx, len(palette))
				}
				p := palette[idx]
				fb.setRGBA(dx+x, dy+y, p[0], p[1], p[2])
			}
		}
	case sub == 128:
		// Plain RLE：(像素, run) 序列直到填满
		filled := 0
		for filled < area {
			p, err := r.cpixel()
			if err != nil {
				return err
			}
			run, err := r.zrun()
			if err != nil {
				return err
			}
			if filled+run > area {
				return fmt.Errorf("plain rle overflow: %d+%d > %d", filled, run, area)
			}
			for i := 0; i < run; i++ {
				x := filled % tw
				y := filled / tw
				fb.setRGBA(dx+x, dy+y, p[0], p[1], p[2])
				filled++
			}
		}
	case sub >= 130:
		// Palette RLE：调色板 sub-128 色；run 编码（index<128 单像素，>=128 run）
		n := sub - 128
		palette := make([][4]uint8, n)
		for i := range palette {
			if palette[i], err = r.cpixel(); err != nil {
				return err
			}
		}
		filled := 0
		for filled < area {
			idx, err := r.u8()
			if err != nil {
				return err
			}
			run := 1
			if idx&0x80 != 0 {
				run, err = r.zrun()
				if err != nil {
					return err
				}
			}
			if int(idx&0x7F) >= len(palette) {
				return fmt.Errorf("palette index %d >= %d", idx&0x7F, len(palette))
			}
			if filled+run > area {
				return fmt.Errorf("palette rle overflow: %d+%d > %d", filled, run, area)
			}
			p := palette[idx&0x7F]
			for i := 0; i < run; i++ {
				x := filled % tw
				y := filled / tw
				fb.setRGBA(dx+x, dy+y, p[0], p[1], p[2])
				filled++
			}
		}
	default:
		return fmt.Errorf("unsupported zrle subencoding %d", sub)
	}
	return nil
}
