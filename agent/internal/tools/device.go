package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DeviceBrief 设备摘要（跨端调度用）
type DeviceBrief struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	User   string   `json:"user"`
	Caps   []string `json:"caps"`
	Online bool     `json:"online"`
}

// DeviceResult 跨端任务的回执
type DeviceResult struct {
	TaskID     string         `json:"taskId"`
	DeviceID   string         `json:"deviceId"`
	Action     string         `json:"action"`
	Status     string         `json:"status"` // done | failed | rejected | timeout
	Output     map[string]any `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  int64          `json:"startedAt,omitempty"`
	FinishedAt int64          `json:"finishedAt,omitempty"`
}

// DeviceExecutor 跨端调度层（由后端的设备中枢实现；工具层只认这个接口，不反向依赖 hub）
type DeviceExecutor interface {
	ListDevices() ([]DeviceBrief, error)
	// Dispatch 把任务派给某台设备并等回执；timeout<=0 用默认值
	Dispatch(ctx context.Context, deviceID, action string, args map[string]any, timeout time.Duration) (DeviceResult, error)
}

// DeviceRecorder 记录跨端任务结果（由服务层用来把结果落进长期记忆）
type DeviceRecorder func(res DeviceResult)

/* ---------- device_list ---------- */

// DeviceList 列出已注册设备与能力（调度决策依据）
type DeviceList struct{ exec DeviceExecutor }

// NewDeviceList 创建 device_list
func NewDeviceList(exec DeviceExecutor) *DeviceList { return &DeviceList{exec: exec} }

// Name 工具名
func (t *DeviceList) Name() string { return "device_list" }

// Description 说明
func (t *DeviceList) Description() string {
	return "列出已注册的设备（桌面端/手机端）及其在线状态与能力，用来决定这个任务该派给谁"
}

// Schema 参数说明
func (t *DeviceList) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// Run 执行
func (t *DeviceList) Run(_ context.Context, _ map[string]any) (any, error) {
	if t.exec == nil {
		return nil, errors.New("当前后端没有接入设备中枢，无法跨端调度")
	}
	list, err := t.exec.ListDevices()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Online != list[j].Online {
			return list[i].Online
		}
		return list[i].ID < list[j].ID
	})
	return map[string]any{
		"count":   len(list),
		"devices": list,
		"hint":    "用 device_run 把任务派给 deviceId；不在线的设备任务会排队等它上线",
	}, nil
}

/* ---------- device_run ---------- */

// DeviceRun 把一条指令派给某台设备执行并等回执
type DeviceRun struct {
	exec     DeviceExecutor
	recorder DeviceRecorder
	timeout  time.Duration
}

// NewDeviceRun 创建 device_run
func NewDeviceRun(exec DeviceExecutor, rec DeviceRecorder) *DeviceRun {
	return &DeviceRun{exec: exec, recorder: rec, timeout: 120 * time.Second}
}

// Name 工具名
func (t *DeviceRun) Name() string { return "device_run" }

// Description 说明
func (t *DeviceRun) Description() string {
	return "在指定设备上执行一条指令并等待回执（例如 fs.delete/fs.stat/sys.info/window.now/ping）；" +
		"删除类动作是危险操作，会先走审批闸门"
}

// Schema 参数说明
func (t *DeviceRun) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"deviceId":   map[string]any{"type": "string", "description": "目标设备 id（先用 device_list 查）"},
			"action":     map[string]any{"type": "string", "description": "动作名，如 fs.delete / fs.stat / sys.info / window.now / ping"},
			"args":       map[string]any{"type": "object", "description": "动作参数，例如 {\"paths\":[\"D:\\\\tmp\\\\a.txt\"]}"},
			"timeoutSec": map[string]any{"type": "integer", "description": "等待回执的秒数，默认 120"},
		},
		"required": []string{"deviceId", "action"},
	}
}

// Dangerous 设备上的删除类动作需要人工审批
func (t *DeviceRun) Dangerous() bool { return true }

// Mutating 动作发生在别的设备上，本端工作目录快照没有意义
func (t *DeviceRun) Mutating() bool { return false }

// Run 执行
func (t *DeviceRun) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.exec == nil {
		return nil, errors.New("当前后端没有接入设备中枢，无法跨端调度")
	}
	deviceID := ArgString(args, "deviceId")
	action := ArgString(args, "action")
	if deviceID == "" || action == "" {
		return nil, errors.New("deviceId 与 action 都不能为空")
	}
	timeout := time.Duration(ArgInt(args, "timeoutSec", int(t.timeout/time.Second))) * time.Second
	if timeout <= 0 {
		timeout = t.timeout
	}
	payload := map[string]any{}
	if raw, ok := args["args"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("args 必须是对象，收到 %T", raw)
		}
		payload = m
	}
	res, err := t.exec.Dispatch(ctx, deviceID, action, payload, timeout)
	if t.recorder != nil {
		t.recorder(res)
	}
	if err != nil {
		return map[string]any{
			"deviceId": deviceID, "action": action, "taskId": res.TaskID,
			"status": res.Status, "error": err.Error(),
		}, err
	}
	return map[string]any{
		"deviceId": deviceID, "action": action, "taskId": res.TaskID,
		"status": res.Status, "output": res.Output,
	}, nil
}

/* ---------- 注册 ---------- */

// RegisterDevices 注册跨端调度工具
func RegisterDevices(r *Registry, exec DeviceExecutor, rec DeviceRecorder) {
	if exec == nil {
		return
	}
	r.Register(NewDeviceList(exec))
	r.Register(NewDeviceRun(exec, rec))
}

// DeviceCapabilitiesHint 给系统提示用的一句话（有哪些端可用）
func DeviceCapabilitiesHint(list []DeviceBrief) string {
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	online := 0
	for _, d := range list {
		state := "离线"
		if d.Online {
			state = "在线"
			online++
		}
		parts = append(parts, fmt.Sprintf("%s（%s/%s，%s，能力：%s）",
			d.Name, d.OS, d.Arch, state, strings.Join(d.Caps, ",")))
	}
	return fmt.Sprintf("已注册设备 %d 台（在线 %d）：%s", len(list), online, strings.Join(parts, "；"))
}
