// Package instabox 是 Instabox 云桌面服务（https://instabox.sh）的 Go API 客户端。
package instabox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultAPIBase 是官方 API 地址。
const defaultAPIBase = "https://api.instabox.sh"

// maxAttempts 是单次操作的总尝试次数（首次请求 + 最多 3 次重试）。
const maxAttempts = 4

// maxErrorBodySize 限制错误响应体的读取大小，防止异常大的 body 撑爆内存。
const maxErrorBodySize = 1 << 20

// Client 是 Instabox API 客户端，并发安全。
type Client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

// New 创建客户端。apiBase 为空时使用官方地址；hc 为 nil 时使用默认的
// http.Client（超时 30s）。
func New(apiBase, apiKey string, hc *http.Client) *Client {
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	apiBase = strings.TrimRight(apiBase, "/")
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: apiBase, apiKey: apiKey, hc: hc}
}

// do 是统一的请求方法：发送 JSON 请求、解析 JSON 错误、按 Retry-After /
// retryAfterSeconds / 指数退避重试，全部集中在此处。
// body 非 nil 时作为 JSON 请求体；out 非 nil 时把 2xx 响应体解析进去。
// idemKey 非空时作为 Idempotency-Key 头发送，并在所有重试间复用同一个值。
func (c *Client) do(ctx context.Context, method, path string, body, out any, idemKey string) error {
	var delay time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// 退避等待；上下文取消时立即退出
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}

		req, err := c.newRequest(ctx, method, path, body, idemKey)
		if err != nil {
			return err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return err
		}

		apiErr := parseError(resp)
		if apiErr == nil {
			// 2xx：解析响应体（允许空 body）
			if out != nil {
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					return err
				}
				if len(bytes.TrimSpace(data)) > 0 {
					if err := json.Unmarshal(data, out); err != nil {
						return fmt.Errorf("instabox: 解析响应失败: %w", err)
					}
				}
			} else {
				resp.Body.Close()
			}
			return nil
		}
		resp.Body.Close()

		if !shouldRetry(apiErr) || attempt == maxAttempts-1 {
			return apiErr
		}
		delay = retryDelay(resp.Header, apiErr, attempt)
	}
	// 不可达
	return nil
}

// newRequest 构造单次请求；body 每次重新序列化以支持重试。
func (c *Client) newRequest(ctx context.Context, method, path string, body any, idemKey string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("instabox: 序列化请求体失败: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	return req, nil
}

// shouldRetry 判断该错误是否值得重试：429 或 retryable=true。
func shouldRetry(e *APIError) bool {
	return e.Status == http.StatusTooManyRequests || e.Retryable
}

// retryDelay 计算第 attempt 次失败后的等待时间：优先 Retry-After 头，
// 其次错误体里的 retryAfterSeconds，否则 1s * 2^attempt。
func retryDelay(h http.Header, e *APIError, attempt int) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	return time.Duration(1<<uint(attempt)) * time.Second
}

// newIdempotencyKey 生成 uuid 风格的随机幂等键。
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 几乎不会失败；退化到纳秒时间戳
		return fmt.Sprintf("idem-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// VNCWebSocketURL 返回该机器 VNC 的 WebSocket URL（供 UI 层 dial）。
// https 基地址对应 wss，http 对应 ws。
func (c *Client) VNCWebSocketURL(id string) string {
	scheme := "wss"
	rest := c.baseURL
	if strings.HasPrefix(rest, "https://") {
		rest = strings.TrimPrefix(rest, "https://")
	} else if strings.HasPrefix(rest, "http://") {
		scheme = "ws"
		rest = strings.TrimPrefix(rest, "http://")
	}
	return scheme + "://" + rest + "/v1/machines/" + url.PathEscape(id) + "/vnc"
}

// APIKey 返回客户端的 API key（用于 VNC WebSocket 握手头等场景）。
func (c *Client) APIKey() string { return c.apiKey }
