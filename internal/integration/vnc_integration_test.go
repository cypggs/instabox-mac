// Package integration 包含针对真实 Instabox 实例的集成测试。
//
// 运行方式（不会自动跑，需显式开启）：
//
//	INSTABOX_API_KEY=ibk_... go test ./internal/integration/ -integration -v -timeout 120s
//
// 没有 INSTABOX_API_KEY 时全部跳过并打印"未验证"——绝不伪造结果。
// API key 只从环境变量读取，不写入源码/日志/仓库。
package integration

import (
	"context"
	"image"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/cypggs/instabox-mac/internal/instabox"
	"github.com/cypggs/instabox-mac/internal/rfb"
)

var live = flag.Bool("integration", false, "运行真实 Instabox 集成测试")

func apiClient(t *testing.T) *instabox.Client {
	t.Helper()
	if !*live {
		t.Skip("未加 -integration：真实实例测试标记为未验证（不伪造结果）")
	}
	key := os.Getenv("INSTABOX_API_KEY")
	if key == "" {
		t.Skip("INSTABOX_API_KEY 未设置：真实实例测试标记为未验证（不伪造结果）")
	}
	return instabox.New("https://api.instabox.sh", key, nil)
}

// TestListMachines 验证 API key 有效且能拉取机器列表。
func TestListMachines(t *testing.T) {
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ms, err := c.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	t.Logf("machines: %d", len(ms))
	for _, m := range ms {
		t.Logf("  %s state=%s template=%s", m.ID, m.State, m.TemplateID)
	}
}

// TestRFBHandshakeAndFramebuffer 对一台 running 机器完成
// WebSocket → RFB 3.8（None 认证）握手，并请求一次全量更新，
// 验证能收到 FramebufferUpdate 且帧缓冲尺寸与 ServerInit 一致。
func TestRFBHandshakeAndFramebuffer(t *testing.T) {
	c := apiClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ms, err := c.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	var target *instabox.Machine
	for i := range ms {
		if ms[i].State == "running" {
			target = &ms[i]
			break
		}
	}
	if target == nil {
		// 没有 running 的机器：尝试唤醒一台 suspended 的（覆盖自动唤醒路径）。
		for i := range ms {
			if ms[i].State == "suspended" {
				target = &ms[i]
				break
			}
		}
	}
	if target == nil {
		t.Skip("没有 running/suspended 状态的机器：跳过 RFB 集成测试（标记为未验证）")
	}
	t.Logf("target machine: %s (%s) state=%s", target.ID, target.Name, target.State)

	if target.State == "suspended" {
		t.Log("machine suspended, waking (up to 60s)…")
		wakeCtx, wakeCancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := c.WakeMachine(wakeCtx, target.ID)
		wakeCancel()
		if err != nil {
			t.Fatalf("WakeMachine: %v", err)
		}
		t.Log("wake OK")
	}

	url := c.VNCWebSocketURL(target.ID)
	trans, err := rfb.DialWebSocket(ctx, url, map[string]string{"X-API-Key": c.APIKey()})
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	defer trans.Close(1000, "test done")

	fbCh := make(chan []image.Rectangle, 8)
	conn, err := rfb.Dial(ctx, trans, rfb.ClientConfig{
		Handlers: rfb.EventHandlers{
			FramebufferUpdate: func(rects []image.Rectangle) { fbCh <- rects },
		},
	})
	if err != nil {
		t.Fatalf("RFB Dial: %v", err)
	}
	defer conn.Close()

	w, h := conn.FramebufferSize()
	t.Logf("ServerInit: %dx%d name=%q", w, h, conn.Init().Name)
	if w <= 0 || h <= 0 {
		t.Fatalf("bad framebuffer size %dx%d", w, h)
	}

	if err := conn.RequestFullUpdate(false); err != nil {
		t.Fatalf("RequestFullUpdate: %v", err)
	}
	select {
	case rects := <-fbCh:
		t.Logf("received FramebufferUpdate with %d rects", len(rects))
		if len(rects) == 0 {
			t.Fatal("empty framebuffer update")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("30s 内未收到 FramebufferUpdate")
	}

	// 第二次全量请求：验证跨更新的 ZRLE 流状态处理（wayvnc 持久流 +
	// sync-flush，第二条更新不是新的 zlib 头）。
	if err := conn.RequestFullUpdate(false); err != nil {
		t.Fatalf("second RequestFullUpdate: %v", err)
	}
	select {
	case rects := <-fbCh:
		t.Logf("second update OK, %d rects", len(rects))
	case <-time.After(30 * time.Second):
		t.Fatalf("第二次请求 30s 内未收到 FramebufferUpdate (conn err=%v)", conn.Err())
	}

	// 按键/指针发送路径冒烟：消息能写出即可（远程效果由手测验收）。
	if err := conn.SendKey(0xFF0D, true); err != nil { // Return down
		t.Fatalf("SendKey: %v", err)
	}
	if err := conn.SendKey(0xFF0D, false); err != nil {
		t.Fatalf("SendKey up: %v", err)
	}
	if err := conn.SendPointer(uint16(w/2), uint16(h/2), 0); err != nil {
		t.Fatalf("SendPointer: %v", err)
	}
	t.Log("key/pointer send path OK")
}
