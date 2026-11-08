package appui

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/cypggs/instabox-mac/internal/instabox"
)

// MachineList 是机器列表视图：展示、刷新、启停/暂停/恢复、连接。
type MachineList struct {
	Client *instabox.Client

	// OnConnect 用户请求连接某台机器时回调。
	OnConnect func(m instabox.Machine)
	// OnOpenPrefs 打开设置面板。
	OnOpenPrefs func()

	mu         sync.Mutex
	machines   []instabox.Machine
	err        error
	loading    bool
	showPrefs  bool
	apiKey     string
	apiKeyEnv  bool
	keyEdited  string
	mappingMsg string
	lastAction map[string]string // machineID → 最近操作提示
	prefs      *Preferences
	onChanged  func()           // 数据变化回调（main 设为切主线程 Invalidate）
}

// NewMachineList 创建列表视图。apiKey/fromEnv 用于设置面板显示。
func NewMachineList(client *instabox.Client, prefs *Preferences, apiKey string, fromEnv bool) *MachineList {
	return &MachineList{
		Client:     client,
		prefs:      prefs,
		apiKey:     apiKey,
		apiKeyEnv:  fromEnv,
		lastAction: map[string]string{},
	}
}

// Refresh 异步拉取机器列表；结果经回调反映在 UI（调用方负责 Invalidate）。
func (l *MachineList) Refresh(ctx context.Context) {
	l.mu.Lock()
	l.loading = true
	l.mu.Unlock()
	go func() {
		ms, err := l.Client.ListMachines(ctx)
		l.mu.Lock()
		l.loading = false
		if err != nil {
			l.err = err
		} else {
			l.err = nil
			sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
			l.machines = ms
		}
		l.mu.Unlock()
		if l.onChanged != nil {
			l.onChanged()
		}
	}()
}

// SetOnChanged 注册数据变化回调（切主线程刷新）。
func (l *MachineList) SetOnChanged(f func()) { l.onChanged = f }

// snapshot 返回当前数据副本。
func (l *MachineList) snapshot() ([]instabox.Machine, error, bool, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.machines, l.err, l.loading, l.showPrefs
}

// lifecycle 操作（异步执行，完成后刷新）。
func (l *MachineList) do(ctx context.Context, id, action string, fn func(context.Context, string) error) {
	l.mu.Lock()
	l.lastAction[id] = action + "…"
	l.mu.Unlock()
	if l.onChanged != nil {
		l.onChanged()
	}
	go func() {
		err := fn(ctx, id)
		l.mu.Lock()
		if err != nil {
			l.lastAction[id] = action + "失败: " + err.Error()
		} else {
			l.lastAction[id] = action + "已发送"
		}
		l.mu.Unlock()
		time.Sleep(500 * time.Millisecond) // 等服务端状态流转
		l.Refresh(ctx)
	}()
}

// View 是 MyGo 视图。
func (l *MachineList) View(c *ui.Context) {
	t := c.Theme()
	ms, err, loading, showPrefs := l.snapshot()
	ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Children(func() {
			ui.Text(c, "Instabox 机器").FontSize(20).Bold()
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("refresh"), "刷新").Disabled(loading).Clicked() {
					l.Refresh(context.Background())
				}
				if ui.Button(c.Key("prefs"), "设置").Clicked() {
					l.mu.Lock()
					l.showPrefs = !l.showPrefs
					l.keyEdited = l.apiKey
					l.mu.Unlock()
				}
			})
		})
		if showPrefs {
			l.prefsView(c, t)
		}
		if err != nil {
			ui.Text(c, "加载失败: "+err.Error()).TextColor(t.Danger)
			if instabox.IsAuthError(err) {
				ui.Text(c, "API key 无效或已过期，请在 设置 中更新（ibk_ 开头）。").FontSize(12).TextColor(t.Warning)
			}
		}
		if loading && len(ms) == 0 {
			ui.Text(c, "加载中…").TextColor(t.TextMuted)
		}
		for _, m := range ms {
			l.machineRow(c, t, m)
		}
		if len(ms) == 0 && !loading && err == nil {
			ui.Text(c, "没有机器。在 dashboard.instabox.sh 创建一台（模板选 omarchy 可获得 Hyprland 桌面）。").TextColor(t.TextMuted)
		}
	})
}

func (l *MachineList) machineRow(c *ui.Context, t *ui.Theme, m instabox.Machine) {
	l.mu.Lock()
	note := l.lastAction[m.ID]
	l.mu.Unlock()
	ui.Box(c.Key(m.ID)).FillWidth().Padding(10).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, m.Name).Bold()
					stateColor := t.Success
					switch m.State {
					case "suspended":
						stateColor = t.Warning
					case "stopped":
						stateColor = t.TextMuted
					}
					ui.Text(c, m.State).FontSize(12).TextColor(stateColor)
					ui.Text(c, m.TemplateID).FontSize(12).TextColor(t.TextMuted)
					if m.CooldownUntil != nil && time.Now().Before(*m.CooldownUntil) {
						ui.Text(c, "冷却中").FontSize(12).TextColor(t.Warning)
					}
					if note != "" {
						ui.Text(c, note).FontSize(12).TextColor(t.TextMuted)
					}
				})
				mem := ""
				if m.MemMiB > 0 {
					mem = fmt.Sprintf(" · %d GiB", m.MemMiB/1024)
				}
				ui.Text(c, fmt.Sprintf("%d vCPU%s · %s", m.VCPUs, mem, m.ID)).FontSize(11).TextColor(t.TextMuted)
			})
			ui.Row(c).Gap(6).Children(func() {
				switch m.State {
				case "running":
					if ui.Button(c.Key("conn"+m.ID), "连接").Clicked() {
						if l.OnConnect != nil {
							l.OnConnect(m)
						}
					}
					if ui.Button(c.Key("sus"+m.ID), "暂停").Clicked() {
						l.do(context.Background(), m.ID, "暂停", l.Client.SuspendMachine)
					}
					if ui.Button(c.Key("stop"+m.ID), "停止").Clicked() {
						l.do(context.Background(), m.ID, "停止", l.Client.StopMachine)
					}
				case "suspended":
					if ui.Button(c.Key("conn"+m.ID), "连接（唤醒）").Clicked() {
						if l.OnConnect != nil {
							l.OnConnect(m)
						}
					}
					if ui.Button(c.Key("res"+m.ID), "恢复").Clicked() {
						l.do(context.Background(), m.ID, "恢复", l.Client.ResumeMachine)
					}
				case "stopped":
					if ui.Button(c.Key("start"+m.ID), "启动").Clicked() {
						l.do(context.Background(), m.ID, "启动", l.Client.StartMachine)
					}
				}
			})
		})
	})
}

func (l *MachineList) prefsView(c *ui.Context, t *ui.Theme) {
	l.mu.Lock()
	key := l.keyEdited
	env := l.apiKeyEnv
	l.mu.Unlock()
	mapping := l.prefs.GetMapping()
	ui.Box(c.Key("prefs-panel")).FillWidth().Padding(12).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "设置").Bold()
			ui.Text(c, "API Key（存于 macOS 钥匙串；也可通过 INSTABOX_API_KEY 环境变量提供）").FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c.Key("apikey"), &key).Password().FillWidth().Placeholder("ibk_…")
				if ui.Button(c.Key("savekey"), "保存").Clicked() {
					if err := SaveAPIKey(key); err != nil {
						l.mappingMsg = "保存失败: " + err.Error()
					} else {
						l.mappingMsg = "已保存到钥匙串，重启应用生效"
						l.apiKey = key
					}
				}
			})
			if env {
				ui.Text(c, "当前使用环境变量 INSTABOX_API_KEY").FontSize(11).TextColor(t.Warning)
			}
			ui.Text(c, "修饰键映射（Mac 键 → 远程键）").FontSize(12).TextColor(t.TextMuted)
			l.mappingRow(c, "Command (⌘)", &mapping.Command)
			l.mappingRow(c, "Option (⌥)", &mapping.Option)
			l.mappingRow(c, "Control (⌃)", &mapping.Control)
			if l.mappingMsg != "" {
				ui.Text(c, l.mappingMsg).FontSize(11).TextColor(t.Warning)
			}
		})
	})
}

func (l *MachineList) mappingRow(c *ui.Context, label string, val *string) {
	// Select 的值带展示说明，保存时归一化为纯角色名。
	current := *val + selSuffix(*val)
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, label).Width(120)
		if ui.Select(c.Key(label), &current,
			[]string{"super (Hyprland 主键)", "alt", "ctrl", "none (不发送)"}).
			Changed() {
			*val = selNormalize(current)
			if err := l.prefs.SetMapping(l.prefs.GetMapping()); err != nil {
				l.mappingMsg = err.Error()
			} else {
				l.mappingMsg = ""
			}
		}
	})
}

func selSuffix(role string) string {
	switch role {
	case "super":
		return " (Hyprland 主键)"
	case "none":
		return " (不发送)"
	}
	return ""
}

func selNormalize(v string) string {
	for _, role := range []string{"super", "alt", "ctrl", "none"} {
		if v == role || len(v) > len(role) && v[:len(role)] == role && v[len(role)] == ' ' {
			return role
		}
	}
	return v
}

