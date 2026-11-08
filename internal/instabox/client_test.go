package instabox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	c := New("", "ibk_test", nil)
	if c.baseURL != defaultAPIBase {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultAPIBase)
	}
	if c.hc == nil || c.hc.Timeout != 30*time.Second {
		t.Errorf("默认 http.Client 超时应为 30s，得到 %+v", c.hc)
	}
	if c.apiKey != "ibk_test" {
		t.Errorf("apiKey = %q", c.apiKey)
	}

	custom := &http.Client{Timeout: time.Second}
	c2 := New("https://example.com/", "k", custom)
	if c2.baseURL != "https://example.com" {
		t.Errorf("baseURL 应去掉结尾斜杠，得到 %q", c2.baseURL)
	}
	if c2.hc != custom {
		t.Error("应使用传入的 http.Client")
	}
}

func TestAPIErrorErrorString(t *testing.T) {
	e := &APIError{Status: 404, Code: "not_found", Message: "machine not found"}
	if !strings.Contains(e.Error(), "not_found") || !strings.Contains(e.Error(), "404") {
		t.Errorf("Error() = %q", e.Error())
	}
	plain := &APIError{Status: 502}
	if !strings.Contains(plain.Error(), "502") {
		t.Errorf("Error() = %q", plain.Error())
	}
}

func TestIsAuthError(t *testing.T) {
	for _, code := range []string{"missing_key", "malformed_key", "invalid_key", "expired_key", "revoked_key"} {
		if !IsAuthError(&APIError{Status: 401, Code: code}) {
			t.Errorf("Code %q 应判定为认证错误", code)
		}
	}
	if IsAuthError(&APIError{Status: 403, Code: "missing_scope"}) {
		t.Error("missing_scope 不是认证错误")
	}
	if IsAuthError(&APIError{Status: 404, Code: "not_found"}) {
		t.Error("not_found 不是认证错误")
	}
	if IsAuthError(fmt.Errorf("普通错误")) {
		t.Error("非 APIError 不应判定为认证错误")
	}
	if !IsAuthError(fmt.Errorf("包装: %w", &APIError{Status: 401, Code: "invalid_key"})) {
		t.Error("包装的 APIError 应能被识别")
	}
}

// TestErrorBodyEdgeCases 覆盖：错误 JSON 缺字段 / 非 JSON body / 5xx 无 body，均不 panic。
func TestErrorBodyEdgeCases(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		contentTyp string
		wantCode   string
	}{
		{"错误 JSON 只有 code", 400, `{"error":{"code":"invalid_key"}}`, "application/json", "invalid_key"},
		{"错误 JSON 为空对象", 400, `{"error":{}}`, "application/json", ""},
		{"错误 JSON 缺 error 字段", 500, `{"foo":1}`, "application/json", ""},
		{"非 JSON body", 400, `<html>bad request</html>`, "text/html", ""},
		{"5xx 无 body", 503, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentTyp)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			c := New(srv.URL, "ibk_test", srv.Client())
			_, err := c.GetMachine(context.Background(), "m1")
			if err == nil {
				t.Fatal("应返回错误")
			}
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("错误类型应为 *APIError，得到 %T: %v", err, err)
			}
			if ae.Status != tc.status {
				t.Errorf("Status = %d, want %d", ae.Status, tc.status)
			}
			if ae.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", ae.Code, tc.wantCode)
			}
		})
	}
}

// TestCtxCancelDuringBackoff 验证重试退避等待期间 ctx 取消会立即返回。
func TestCtxCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"too_many_requests","message":"slow down","retryable":true}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.StartMachine(ctx, "m1")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("应返回 context.DeadlineExceeded，得到 %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("ctx 取消后应快速返回，耗时 %v", elapsed)
	}
}

// TestCtxAlreadyCanceled 验证已取消的 ctx 直接报错。
func TestCtxAlreadyCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "ibk_test", srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := c.ListMachines(ctx)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("应返回 context.Canceled，得到 %v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("应立即返回")
	}
}

func TestVNCWebSocketURL(t *testing.T) {
	c := New("https://api.instabox.sh", "k", nil)
	got := c.VNCWebSocketURL("machine-42")
	want := "wss://api.instabox.sh/v1/machines/machine-42/vnc"
	if got != want {
		t.Errorf("https 基地址应得到 wss，got %q want %q", got, want)
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer httpSrv.Close()
	c2 := New(httpSrv.URL, "k", nil)
	got = c2.VNCWebSocketURL("m/x")
	if !strings.HasPrefix(got, "ws://") {
		t.Errorf("http 基地址应得到 ws，got %q", got)
	}
	if !strings.Contains(got, "/v1/machines/m%2Fx/vnc") {
		t.Errorf("id 应被 PathEscape，got %q", got)
	}
}

func TestNewIdempotencyKey(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		k := newIdempotencyKey()
		if seen[k] {
			t.Fatalf("幂等键重复: %q", k)
		}
		seen[k] = true
		if len(k) != 36 || k[8] != '-' || k[13] != '-' || k[18] != '-' || k[23] != '-' {
			t.Fatalf("幂等键格式不是 uuid: %q", k)
		}
		if k[14] != '4' {
			t.Fatalf("version 应为 4: %q", k)
		}
	}
}
