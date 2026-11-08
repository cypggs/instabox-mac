package instabox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIError 是带错误码的 API 错误。Code 为空表示响应不是 JSON 错误体。
type APIError struct {
	Status     int           // HTTP 状态码
	Code       string        // error.code，空表示非 JSON 错误体
	Message    string        // error.message
	Retryable  bool          // error.retryable
	RetryAfter time.Duration // error.retryAfterSeconds 换算结果
	RequestID  string        // error.requestId，缺省时回退到 X-Request-Id 头
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("instabox: %s (HTTP %d, code %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("instabox: HTTP %d", e.Status)
}

// IsAuthError 判断错误是否为 API key 认证类错误
// （missing_key / malformed_key / invalid_key / expired_key / revoked_key）。
func IsAuthError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case "missing_key", "malformed_key", "invalid_key", "expired_key", "revoked_key":
		return true
	}
	return false
}

// parseError 解析非 2xx 响应为 *APIError；2xx 返回 nil。
// 错误体缺字段、不是 JSON、或为空时都不 panic，尽量保留状态码与请求 ID。
func parseError(resp *http.Response) *APIError {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	e := &APIError{Status: resp.StatusCode}
	if reqID := resp.Header.Get("X-Request-Id"); reqID != "" {
		e.RequestID = reqID
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	if err != nil {
		return e
	}
	var env struct {
		Error *struct {
			Code              string `json:"code"`
			Message           string `json:"message"`
			Retryable         bool   `json:"retryable"`
			RetryAfterSeconds *int   `json:"retryAfterSeconds"`
			RequestID         string `json:"requestId"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) != nil || env.Error == nil {
		return e
	}
	e.Code = env.Error.Code
	e.Message = env.Error.Message
	e.Retryable = env.Error.Retryable
	if env.Error.RetryAfterSeconds != nil && *env.Error.RetryAfterSeconds > 0 {
		e.RetryAfter = time.Duration(*env.Error.RetryAfterSeconds) * time.Second
	}
	if env.Error.RequestID != "" {
		e.RequestID = env.Error.RequestID
	}
	return e
}
