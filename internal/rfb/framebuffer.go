package rfb

import (
	"image"
	"sync"
)

// framebuffer 是服务端的帧缓冲，RGBA 格式（Alpha 固定 255），
// 与请求的像素格式对应：wire 上每像素 4 字节 [R,G,B,X]，
// 解码时只把 X 置为 0xFF，无需字节交换。
type framebuffer struct {
	mu     sync.RWMutex
	img    *image.RGBA
	stride int
}

func newFramebuffer(w, h int) *framebuffer {
	return &framebuffer{
		img:    image.NewRGBA(image.Rect(0, 0, w, h)),
		stride: w * 4,
	}
}

// bounds 返回当前尺寸（线程安全）。
func (f *framebuffer) bounds() image.Rectangle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.img.Bounds()
}

// resize 重建指定尺寸的帧缓冲（内容丢弃）。
func (f *framebuffer) resize(w, h int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.img = image.NewRGBA(image.Rect(0, 0, w, h))
	f.stride = w * 4
}

// setRGBA 写入一个像素。
func (f *framebuffer) setRGBA(x, y int, r, g, b uint8) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.img.PixOffset(x, y)
	f.img.Pix[i] = r
	f.img.Pix[i+1] = g
	f.img.Pix[i+2] = b
	f.img.Pix[i+3] = 0xFF
}

// fillRect 用单一颜色填充矩形。
func (f *framebuffer) fillRect(rect image.Rectangle, r, g, b uint8) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fillRectLocked(rect, r, g, b)
}

func (f *framebuffer) fillRectLocked(rect image.Rectangle, r, g, b uint8) {
	rect = rect.Intersect(f.img.Bounds())
	if rect.Empty() {
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		row := f.img.Pix[f.img.PixOffset(rect.Min.X, y):]
		for x := 0; x < rect.Dx()*4; x += 4 {
			row[x] = r
			row[x+1] = g
			row[x+2] = b
			row[x+3] = 0xFF
		}
	}
}

// blitRaw 把 wire 格式 [R,G,B,X]*n 像素写入 (x,y) 起始的一行区域。
// data 长度必须 == w*4。X 字节被忽略。
func (f *framebuffer) blitRow(x, y, w int, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if y < f.img.Bounds().Min.Y || y >= f.img.Bounds().Max.Y || x < 0 {
		return
	}
	if x+w > f.img.Bounds().Max.X {
		w = f.img.Bounds().Max.X - x
	}
	dst := f.img.Pix[f.img.PixOffset(x, y):]
	for i := 0; i < w*4; i += 4 {
		dst[i] = data[i]
		dst[i+1] = data[i+1]
		dst[i+2] = data[i+2]
		dst[i+3] = 0xFF
	}
}

// snapshot 返回当前整帧的 RGBA 拷贝（UI 层用它更新 bitmap）。
func (f *framebuffer) snapshot() *image.RGBA {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cp := image.NewRGBA(f.img.Bounds())
	copy(cp.Pix, f.img.Pix)
	return cp
}

// copyInto 把当前整帧拷入 dst（尺寸必须一致，避免每帧分配）。
func (f *framebuffer) copyInto(dst *image.RGBA) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if dst.Bounds() != f.img.Bounds() {
		return
	}
	copy(dst.Pix, f.img.Pix)
}
