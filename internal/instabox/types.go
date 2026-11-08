package instabox

import "time"

// Machine 是一台云桌面机器。State / DesiredState 原样透传 API 值
// （running / suspended / stopped / archived 等）。
type Machine struct {
	ID              string
	Name            string
	State           string
	DesiredState    string
	TemplateID      string
	Ready           bool
	VCPUs           int
	MemMiB          int
	CooldownUntil   *time.Time // cooldown_until，可能为 null
	AutoSuspendedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// machineJSON 对应 API 的 snake_case 机器表示；嵌套对象只提取需要的字段。
type machineJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	State        string `json:"state"`
	DesiredState string `json:"desired_state"`
	Template     struct {
		ID string `json:"id"`
	} `json:"template"`
	Runtime struct {
		Ready bool `json:"ready"`
	} `json:"runtime"`
	Spec struct {
		VCPUs  int `json:"vcpus"`
		MemMiB int `json:"mem_mib"`
	} `json:"spec"`
	CooldownUntil   *time.Time `json:"cooldown_until"`
	AutoSuspendedAt *time.Time `json:"auto_suspended_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// toMachine 转成公开类型。
func (j *machineJSON) toMachine() Machine {
	return Machine{
		ID:              j.ID,
		Name:            j.Name,
		State:           j.State,
		DesiredState:    j.DesiredState,
		TemplateID:      j.Template.ID,
		Ready:           j.Runtime.Ready,
		VCPUs:           j.Spec.VCPUs,
		MemMiB:          j.Spec.MemMiB,
		CooldownUntil:   j.CooldownUntil,
		AutoSuspendedAt: j.AutoSuspendedAt,
		CreatedAt:       j.CreatedAt,
		UpdatedAt:       j.UpdatedAt,
	}
}
