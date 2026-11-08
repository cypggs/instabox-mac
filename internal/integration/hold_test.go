package integration

// 诊断/回归：用真实 Session（平铺请求路径）连接 omarchy 机器，
// 验证首帧与持续增量更新都能到达——即用户报告的"已连接但空白"场景。
// 运行：INSTABOX_API_KEY=... go test ./internal/integration/ -run TestSessionFrames -v -timeout 120s

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cypggs/instabox-mac/internal/appui"
	"github.com/cypggs/instabox-mac/internal/instabox"
)

func TestSessionFrames(t *testing.T) {
	if !*live {
		t.Skip("未加 -integration：真实实例测试标记为未验证（不伪造结果）")
	}
	key := os.Getenv("INSTABOX_API_KEY")
	if key == "" {
		t.Skip("no key")
	}
	id := os.Getenv("HOLD_MACHINE")
	if id == "" {
		id = "m_agq3t4dbryqbxnxtnkkkonc2cm" // omarchy
	}
	c := instabox.New("https://api.instabox.sh", key, nil)

	s := appui.NewSession(c)
	var frames atomic.Int64
	s.OnFrame = func() { frames.Add(1) }
	s.OnSize = func(w, h int) { t.Logf("size %dx%d", w, h) }
	s.OnState = func(st appui.SessionState, err error) {
		t.Logf("state=%v err=%v", st, err)
		if st == appui.StateFailed {
			t.Errorf("session failed: %v", err)
		}
	}

	if err := s.Connect(context.Background(), id, "running"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	// 等首帧（看门狗 12s 内必然有结果）
	deadline := time.After(25 * time.Second)
	for frames.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("no frame in 25s; state=%v err=%v", s.State(), s.Err())
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Logf("first frame OK; total=%d; waiting 5s for steady-state frames", frames.Load())
	time.Sleep(5 * time.Second)
	n := frames.Load()
	t.Logf("frames after 5s more: %d", n)

	// 活动性：移动远程指针应触发画面更新（远程渲染光标 = damage）。
	if c := s.Conn(); c != nil {
		before := frames.Load()
		for i := 0; i < 10; i++ {
			_ = c.SendPointer(uint16(100+i*40), uint16(100+i*30), 0)
			time.Sleep(150 * time.Millisecond)
		}
		deadline := time.Now().Add(5 * time.Second)
		for frames.Load() == before && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		got := frames.Load() - before
		t.Logf("pointer-move frames: %d", got)
		if got == 0 {
			t.Error("no frames after remote pointer movement (liveness broken)")
		}
	}
}
