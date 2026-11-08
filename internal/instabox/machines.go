package instabox

import (
	"context"
	"net/http"
	"net/url"
)

// ListMachines 列出全部机器。
func (c *Client) ListMachines(ctx context.Context) ([]Machine, error) {
	var out struct {
		Machines []machineJSON `json:"machines"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/machines", nil, &out, ""); err != nil {
		return nil, err
	}
	machines := make([]Machine, 0, len(out.Machines))
	for i := range out.Machines {
		machines = append(machines, out.Machines[i].toMachine())
	}
	return machines, nil
}

// GetMachine 按 ID 获取单台机器；404 时返回 Code 为 "not_found" 的 *APIError。
func (c *Client) GetMachine(ctx context.Context, id string) (*Machine, error) {
	var mj machineJSON
	if err := c.do(ctx, http.MethodGet, "/v1/machines/"+url.PathEscape(id), nil, &mj, ""); err != nil {
		return nil, err
	}
	m := mj.toMachine()
	return &m, nil
}

// StartMachine 启动机器。
func (c *Client) StartMachine(ctx context.Context, id string) error {
	return c.lifecycle(ctx, id, "start")
}

// StopMachine 停止机器。
func (c *Client) StopMachine(ctx context.Context, id string) error {
	return c.lifecycle(ctx, id, "stop")
}

// SuspendMachine 暂停机器。
func (c *Client) SuspendMachine(ctx context.Context, id string) error {
	return c.lifecycle(ctx, id, "suspend")
}

// ResumeMachine 恢复已暂停的机器。
func (c *Client) ResumeMachine(ctx context.Context, id string) error {
	return c.lifecycle(ctx, id, "resume")
}

// lifecycle 执行生命周期操作：自动生成幂等键，重试时复用同一个键。
func (c *Client) lifecycle(ctx context.Context, id, action string) error {
	key := newIdempotencyKey()
	path := "/v1/machines/" + url.PathEscape(id) + "/" + action
	return c.do(ctx, http.MethodPost, path, nil, nil, key)
}

// WakeMachine 唤醒闲置暂停的机器（POST /v1/machines/{id}/wake）。
// 不带 Idempotency-Key。API 语义是"等待机器恢复"，服务端最多等待约 45s，
// 因此调用方应给 ctx 留够时间（建议 60s），客户端 http.Client 的超时
// 也不应短于这个值。
func (c *Client) WakeMachine(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/machines/"+url.PathEscape(id)+"/wake", nil, nil, "")
}
