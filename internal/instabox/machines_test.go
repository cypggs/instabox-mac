package instabox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const machineListJSON = `{
  "machines": [
    {
      "id": "abc-123",
      "name": "dev-box",
      "state": "running",
      "desired_state": "running",
      "template": {"id": "omarchy", "version": "2026.10"},
      "runtime": {"ready": true, "ip": "10.0.0.1"},
      "spec": {"vcpus": 4, "mem_mib": 8192, "disk_gib": 80},
      "cooldown_until": "2026-11-08T09:53:15.017471Z",
      "auto_suspended_at": null,
      "created_at": "2026-10-01T12:00:00.123456Z",
      "updated_at": "2026-11-08T08:00:00.654321Z",
      "unrelated_field": {"foo": 1}
    },
    {
      "id": "def-456",
      "name": "idle-box",
      "state": "suspended",
      "desired_state": "suspended",
      "template": {"id": "ubuntu"},
      "runtime": {"ready": false},
      "spec": {"vcpus": 2, "mem_mib": 4096},
      "cooldown_until": null,
      "auto_suspended_at": "2026-11-07T23:00:00.000000Z",
      "created_at": "2026-10-05T09:30:00.000000Z",
      "updated_at": "2026-11-07T23:00:00.000000Z"
    }
  ]
}`

func TestListMachines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/machines" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "ibk_test" {
			t.Errorf("X-API-Key = %q", r.Header.Get("X-API-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, machineListJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	machines, err := c.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("机器数 = %d, want 2", len(machines))
	}

	m0 := machines[0]
	if m0.ID != "abc-123" || m0.Name != "dev-box" || m0.State != "running" ||
		m0.DesiredState != "running" || m0.TemplateID != "omarchy" {
		t.Errorf("m0 基本字段解析错误: %+v", m0)
	}
	if !m0.Ready || m0.VCPUs != 4 || m0.MemMiB != 8192 {
		t.Errorf("m0 runtime/spec 解析错误: %+v", m0)
	}
	if m0.CooldownUntil == nil {
		t.Error("m0.CooldownUntil 不应为 nil")
	} else if m0.CooldownUntil.UTC().Format(time.RFC3339) != "2026-11-08T09:53:15Z" {
		t.Errorf("m0.CooldownUntil = %v", m0.CooldownUntil)
	}
	if m0.AutoSuspendedAt != nil {
		t.Errorf("m0.AutoSuspendedAt 应为 nil，得到 %v", m0.AutoSuspendedAt)
	}
	if m0.CreatedAt.UTC().Format(time.RFC3339) != "2026-10-01T12:00:00Z" {
		t.Errorf("m0.CreatedAt = %v", m0.CreatedAt)
	}

	m1 := machines[1]
	if m1.Ready || m1.VCPUs != 2 || m1.MemMiB != 4096 {
		t.Errorf("m1 runtime/spec 解析错误: %+v", m1)
	}
	if m1.CooldownUntil != nil {
		t.Errorf("m1.CooldownUntil 应为 nil，得到 %v", m1.CooldownUntil)
	}
	if m1.AutoSuspendedAt == nil {
		t.Error("m1.AutoSuspendedAt 不应为 nil")
	}
}

func TestListMachinesEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"machines": []}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	machines, err := c.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	if machines == nil || len(machines) != 0 {
		t.Errorf("应返回空非 nil 切片，得到 %#v", machines)
	}
}

func TestGetMachineNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/machines/ghost" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"machine not found","retryable":false,"requestId":"req-abc"}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	_, err := c.GetMachine(context.Background(), "ghost")
	if err == nil {
		t.Fatal("应返回错误")
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("错误类型应为 *APIError，得到 %T", err)
	}
	if ae.Code != "not_found" || ae.Status != http.StatusNotFound {
		t.Errorf("Code/Status = %q/%d", ae.Code, ae.Status)
	}
	if ae.Message != "machine not found" || ae.RequestID != "req-abc" {
		t.Errorf("Message/RequestID = %q/%q", ae.Message, ae.RequestID)
	}
	if ae.Retryable {
		t.Error("Retryable 应透传为 false")
	}
	if IsAuthError(err) {
		t.Error("not_found 不是认证错误")
	}
}

func TestGetMachineOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
		  "id": "solo", "name": "solo", "state": "stopped", "desired_state": "running",
		  "template": {"id": "omarchy"}, "runtime": {"ready": false},
		  "spec": {"vcpus": 8, "mem_mib": 16384},
		  "cooldown_until": null, "auto_suspended_at": null,
		  "created_at": "2026-10-01T12:00:00.000000Z", "updated_at": "2026-10-01T12:00:00.000000Z"
		}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	m, err := c.GetMachine(context.Background(), "solo")
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if m.ID != "solo" || m.State != "stopped" || m.DesiredState != "running" ||
		m.TemplateID != "omarchy" || m.VCPUs != 8 || m.MemMiB != 16384 {
		t.Errorf("字段解析错误: %+v", m)
	}
}

// TestLifecycleIdempotencyKey 验证：四个生命周期调用都带 Idempotency-Key 且各不相同；
// 同一次调用重试时复用同一个 key。
func TestLifecycleIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		// 第 1 次请求（Stop 的首次尝试）返回 429，迫使客户端重试
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"too_many_requests","message":"busy","retryable":true}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	ctx := context.Background()

	if err := c.StartMachine(ctx, "m1"); err != nil {
		t.Fatalf("StartMachine: %v", err)
	}
	if err := c.StopMachine(ctx, "m1"); err != nil {
		t.Fatalf("StopMachine: %v", err)
	}
	if err := c.SuspendMachine(ctx, "m1"); err != nil {
		t.Fatalf("SuspendMachine: %v", err)
	}
	if err := c.ResumeMachine(ctx, "m1"); err != nil {
		t.Fatalf("ResumeMachine: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 5 { // Stop 重试一次，共 5 次请求
		t.Fatalf("请求数 = %d, want 5", len(keys))
	}
	for i, k := range keys {
		if k == "" {
			t.Fatalf("第 %d 次请求缺少 Idempotency-Key", i+1)
		}
	}
	// 请求 1=Start(429)，2=Start 重试，3=Stop，4=Suspend，5=Resume
	if keys[0] != keys[1] {
		t.Errorf("Start 重试应复用同一幂等键: %q vs %q", keys[0], keys[1])
	}
	for i := 1; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] == keys[j] {
				t.Errorf("第 %d 与第 %d 次调用的幂等键不应相同: %q", i+1, j+1, keys[i])
			}
		}
	}
}

// TestRetryAfterHeader 验证 429 + Retry-After 头 → 重试后成功。
func TestRetryAfterHeader(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"too_many_requests","message":"slow down","retryable":false}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	if err := c.ResumeMachine(context.Background(), "m1"); err != nil {
		t.Fatalf("ResumeMachine: %v", err)
	}
	if calls != 2 {
		t.Errorf("请求数 = %d, want 2", calls)
	}
}

// TestRetryOnRetryableFlag 验证 retryable=true（无 Retry-After 头）→ 按退避重试后成功。
// retryAfterSeconds 应被换算进 APIError.RetryAfter。
func TestRetryOnRetryableFlag(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"operation_conflict","message":"conflict","retryable":true,"retryAfterSeconds":0}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	start := time.Now()
	if err := c.StartMachine(context.Background(), "m1"); err != nil {
		t.Fatalf("StartMachine: %v", err)
	}
	if calls != 3 {
		t.Errorf("请求数 = %d, want 3", calls)
	}
	// 无 Retry-After 且 retryAfterSeconds=0 时按默认指数退避 1s+2s 等待
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("重试耗时异常: %v", elapsed)
	}
}

// TestRetryExhausted 验证重试次数用尽后返回最后一次的 APIError。
func TestRetryExhausted(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Retry-After", "0")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"too_many_requests","message":"still busy","retryable":true}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	err := c.StartMachine(context.Background(), "m1")
	if err == nil {
		t.Fatal("应返回错误")
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "too_many_requests" {
		t.Fatalf("应返回最后一次的 APIError，得到 %v", err)
	}
	if calls != maxAttempts {
		t.Errorf("请求数 = %d, want %d（首次 + 3 次重试）", calls, maxAttempts)
	}
}

// TestNoRetryOnNonRetryable4xx 验证非 retryable 的 4xx 只请求一次。
func TestNoRetryOnNonRetryable4xx(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"invalid_key","message":"bad key","retryable":false}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_bad", srv.Client())
	err := c.StartMachine(context.Background(), "m1")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !IsAuthError(err) {
		t.Errorf("invalid_key 应判定为认证错误: %v", err)
	}
	if calls != 1 {
		t.Errorf("请求数 = %d, want 1（不应重试）", calls)
	}
}

// TestWakeMachine 验证 wake 路径正确且不发送 Idempotency-Key。
func TestWakeMachine(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotIdem = r.Header.Get("Idempotency-Key")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	if err := c.WakeMachine(context.Background(), "m1"); err != nil {
		t.Fatalf("WakeMachine: %v", err)
	}
	if gotPath != "/v1/machines/m1/wake" {
		t.Errorf("path = %q", gotPath)
	}
	if gotIdem != "" {
		t.Errorf("Wake 不应带 Idempotency-Key，得到 %q", gotIdem)
	}
}
