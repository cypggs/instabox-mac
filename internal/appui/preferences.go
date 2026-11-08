package appui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo"

	"github.com/cypggs/instabox-mac/internal/instabox"
	"github.com/cypggs/instabox-mac/internal/keys"
)

const (
	secretService = "instabox-mac"
	secretAPIKey  = "api-key"
)

// Preferences 应用偏好：API key（Keychain）+ 修饰键映射（JSON 文件）。
type Preferences struct {
	mu      sync.Mutex
	Mapping keys.Mapping
	path    string
}

// LoadPreferences 读取偏好；mapping 文件损坏时用默认值。
func LoadPreferences() *Preferences {
	p := &Preferences{Mapping: keys.DefaultMapping()}
	dir, err := os.UserConfigDir()
	if err == nil {
		p.path = filepath.Join(dir, "instabox-mac", "preferences.json")
		if b, err := os.ReadFile(p.path); err == nil {
			var m struct {
				Command string `json:"command"`
				Option  string `json:"option"`
				Control string `json:"control"`
			}
			if json.Unmarshal(b, &m) == nil {
				cand := keys.Mapping{Command: m.Command, Option: m.Option, Control: m.Control}
				if cand.Validate() == nil {
					p.Mapping = cand
				}
			}
		}
	}
	return p
}

// Save 保存修饰键映射到 JSON 文件。
func (p *Preferences) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p.Mapping, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, b, 0o600)
}

// Mapping 返回当前修饰键映射。
func (p *Preferences) GetMapping() keys.Mapping {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Mapping
}

// SetMapping 更新并保存映射。
func (p *Preferences) SetMapping(m keys.Mapping) error {
	if err := m.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	p.Mapping = m
	p.mu.Unlock()
	return p.Save()
}

// LoadAPIKey 读 API key：INSTABOX_API_KEY 环境变量优先（覆盖钥匙串，
// 便于调试与免弹窗启动），否则读 Keychain。
// 返回值同时标记是否来自环境变量（UI 提示用）。
func LoadAPIKey() (key string, fromEnv bool) {
	if v := os.Getenv("INSTABOX_API_KEY"); v != "" {
		return v, true
	}
	if b, err := mygo.Secrets.Get(secretAPIKey); err == nil && len(b) > 0 {
		return string(b), false
	}
	return "", false
}

// SaveAPIKey 把 API key 写入 Keychain（内存中不落盘其他位置）。
func SaveAPIKey(key string) error {
	if key == "" {
		return mygo.Secrets.Delete(secretAPIKey)
	}
	return mygo.Secrets.Set(secretAPIKey, []byte(key))
}

// NewClient 用当前凭证构造 API 客户端。
func NewClient(apiBase, key string) *instabox.Client {
	return instabox.New(apiBase, key, nil)
}

// ListMachines 拉取机器列表（给 UI 的便捷封装）。
func ListMachines(ctx context.Context, c *instabox.Client) ([]instabox.Machine, error) {
	return c.ListMachines(ctx)
}
