package appui

import "testing"

// TestCloseSetsDisconnected 回归测试：Close 后状态必须变为 StateDisconnected。
// 若 Close 走 setState，其对 closed 的守卫会丢弃状态翻转，导致视图永远
// 停在桌面（"返回机器列表"点了没反应）。
func TestCloseSetsDisconnected(t *testing.T) {
	s := NewSession(nil)
	// 模拟已连接（不经网络，直接置内部状态）。
	s.mu.Lock()
	s.state = StateConnected
	s.mu.Unlock()

	called := false
	s.OnState = func(st SessionState, err error) {
		called = true
		if st != StateDisconnected {
			t.Errorf("OnState got %v, want StateDisconnected", st)
		}
	}
	s.Close()

	if got := s.State(); got != StateDisconnected {
		t.Fatalf("State() = %v, want StateDisconnected", got)
	}
	if !called {
		t.Fatal("OnState not called")
	}

	// 重复 Close 也应保持 StateDisconnected 且安全返回。
	s.Close()
	if got := s.State(); got != StateDisconnected {
		t.Fatalf("after double Close: State() = %v", got)
	}
}
