package appui

import (
	"image"
	"testing"
)

func imageRect(x0, y0, x1, y1 int) image.Rectangle {
	return image.Rect(x0, y0, x1, y1)
}

func TestTilesGrid(t *testing.T) {
	// 1024x768 → 16x12 = 192 块，全部 64x64
	ts := tiles(1024, 768)
	if len(ts) != 192 {
		t.Fatalf("tiles(1024,768) = %d, want 192", len(ts))
	}
	if ts[0] != imageRect(0, 0, 64, 64) {
		t.Fatalf("first tile = %v", ts[0])
	}
	last := ts[191]
	if last != imageRect(960, 704, 1024, 768) {
		t.Fatalf("last tile = %v", last)
	}
	// 非整除：100x50 → 2x1 网格，边缘块收窄
	ts = tiles(100, 50)
	if len(ts) != 2 {
		t.Fatalf("tiles(100,50) = %d, want 2", len(ts))
	}
	if ts[0] != imageRect(0, 0, 64, 50) || ts[1] != imageRect(64, 0, 100, 50) {
		t.Fatalf("edge tiles = %v", ts)
	}
	// 单块
	if ts := tiles(32, 32); len(ts) != 1 || ts[0] != imageRect(0, 0, 32, 32) {
		t.Fatalf("tiles(32,32) = %v", ts)
	}
}
