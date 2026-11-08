package integration

// 诊断：用 Raw 编码请求递增尺寸的区域，找出中继吞掉更新的尺寸阈值。
// 运行：INSTABOX_API_KEY=... go test ./internal/integration/ -run TestUpdateSizeThreshold -v -timeout 180s

import (
	"context"
	"image"
	"os"
	"testing"
	"time"

	"github.com/cypggs/instabox-mac/internal/instabox"
	"github.com/cypggs/instabox-mac/internal/rfb"
)

func TestUpdateSizeThreshold(t *testing.T) {
	if !*live {
		t.Skip("未加 -integration：真实实例测试标记为未验证（不伪造结果）")
	}
	key := os.Getenv("INSTABOX_API_KEY")
	if key == "" {
		t.Skip("no key")
	}
	id := os.Getenv("HOLD_MACHINE")
	if id == "" {
		id = "m_agq3t4dbryqbxnxtnkkkonc2cm"
	}
	c := instabox.New("https://api.instabox.sh", key, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	trans, err := rfb.DialWebSocket(ctx, c.VNCWebSocketURL(id), map[string]string{"X-API-Key": c.APIKey()})
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	defer trans.Close(1000, "done")

	upd := make(chan int, 32)
	conn, err := rfb.Dial(ctx, trans, rfb.ClientConfig{
		Encodings: []int32{0}, // Raw only：payload = w*h*4 可预测
		Handlers: rfb.EventHandlers{
			FramebufferUpdate: func(rects []image.Rectangle) { upd <- len(rects) },
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	w, h := conn.FramebufferSize()
	t.Logf("connected %dx%d, requesting increasing Raw rects", w, h)

	for _, size := range []int{32, 64, 80, 96, 112, 128, 256, 512, 1024} {
		if size > w || size > h {
			break
		}
		if err := conn.RequestUpdate(false, image.Rect(0, 0, size, size)); err != nil {
			t.Fatalf("request: %v", err)
		}
		deadline := time.After(10 * time.Second)
		got := 0
		for got == 0 {
			select {
			case n := <-upd:
				got += n
			case <-deadline:
				t.Logf("size %4d (payload %9d): TIMEOUT", size, size*size*4)
				got = -1
			}
		}
		if got > 0 {
			t.Logf("size %4d (payload %9d): OK (%d rects)", size, size*size*4, got)
		}
	}
	t.Logf("done, conn err=%v", conn.Err())
}
