package hub

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"baize/shared/proto"
	"baize/internal/store"
	"baize/internal/tools"
)

// CreateOptions 创建任务的完整选项（Agent 跨端派活用）
type CreateOptions struct {
	DeviceID   string
	Action     string
	Args       map[string]any
	Origin     string // user | agent
	ApprovedBy string // 非空表示审批已通过，不再进 pending_approval
	IdemKey    string // 幂等键：同设备同键的"在途"任务会被复用
}

// CreateTaskEx 按选项创建任务（幂等）；返回任务与是否复用了已有在途任务
func (h *Hub) CreateTaskEx(opt CreateOptions) (store.Task, bool, error) {
	deviceID := strings.TrimSpace(opt.DeviceID)
	action := strings.TrimSpace(opt.Action)
	if deviceID == "" {
		return store.Task{}, false, errors.New("缺少 deviceId")
	}
	if action == "" {
		return store.Task{}, false, errors.New("缺少 action")
	}
	if !proto.KnownAction(action) {
		return store.Task{}, false, fmt.Errorf("不支持的动作：%s（可选：%s）", action, strings.Join(proto.KnownActions(), ", "))
	}
	if _, ok, err := h.st.Device(deviceID); err != nil {
		return store.Task{}, false, err
	} else if !ok {
		return store.Task{}, false, fmt.Errorf("设备未注册：%s", deviceID)
	}

	// 幂等：同设备同键还在途 → 直接复用，避免重复下发同一条指令
	if opt.IdemKey != "" {
		if existing, ok, err := h.st.FindInFlightByIdem(deviceID, opt.IdemKey); err != nil {
			return store.Task{}, false, err
		} else if ok {
			h.lg.Info("跨端任务幂等命中，复用已有任务", "task", existing.ID, "device", deviceID,
				"action", action, "status", existing.Status)
			return existing, true, nil
		}
	}

	need := proto.IsDangerous(action) && strings.TrimSpace(opt.ApprovedBy) == ""
	status := proto.TaskQueued
	if need {
		status = proto.TaskPendingApproval
	}
	origin := opt.Origin
	if origin == "" {
		origin = "user"
	}
	t := store.Task{
		ID:           newTaskID(),
		DeviceID:     deviceID,
		Action:       action,
		Args:         opt.Args,
		Status:       status,
		NeedApproval: need,
		ApprovedBy:   strings.TrimSpace(opt.ApprovedBy),
		Origin:       origin,
		IdemKey:      opt.IdemKey,
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := h.st.CreateTask(t); err != nil {
		return store.Task{}, false, err
	}
	if need {
		h.lg.Warn("任务待人工审批", "task", t.ID, "device", deviceID, "action", action,
			"args", jsonBrief(opt.Args), "origin", origin, "reason", "危险操作闸门")
		return t, false, nil
	}
	h.lg.Info("任务已创建", "task", t.ID, "device", deviceID, "action", action,
		"args", jsonBrief(opt.Args), "origin", origin)
	if err := h.dispatchOne(t); err != nil {
		h.lg.Warn("任务下发失败，继续排队", "task", t.ID, "device", deviceID, "err", err)
	}
	out, _, _ := h.st.Task(t.ID)
	return out, false, nil
}

// WaitTask 等一个任务的回执（供 Agent 同步等待跨端结果）
func (h *Hub) WaitTask(ctx context.Context, id string, timeout time.Duration) (store.Task, error) {
	if timeout <= 0 {
		timeout = h.timeout
	}
	deadline := time.Now().Add(timeout)
	for {
		t, ok, err := h.st.Task(id)
		if err != nil {
			return store.Task{}, err
		}
		if !ok {
			return store.Task{}, fmt.Errorf("任务不存在：%s", id)
		}
		switch t.Status {
		case proto.TaskDone, proto.TaskFailed, proto.TaskRejected:
			return t, nil
		case proto.TaskPendingApproval:
			return t, fmt.Errorf("任务 %s 还在等人工审批，未执行", id)
		}
		if time.Now().After(deadline) {
			return t, fmt.Errorf("等待回执超时（%s），任务 %s 当前状态：%s", timeout, id, t.Status)
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

/* ---------- 实现 tools.DeviceExecutor：让 Agent 能跨端派活 ---------- */

// ListDevices 设备清单（Agent 决策依据）
func (h *Hub) ListDevices() ([]tools.DeviceBrief, error) {
	devices, err := h.st.Devices()
	if err != nil {
		return nil, err
	}
	out := make([]tools.DeviceBrief, 0, len(devices))
	for _, d := range devices {
		out = append(out, tools.DeviceBrief{
			ID: d.ID, Name: d.Name, OS: d.OS, Arch: d.Arch, User: d.User,
			Caps: d.Caps, Online: d.Online,
		})
	}
	return out, nil
}

// Dispatch 把任务派给设备并等回执（Agent 调用；审批已在上层闸门完成）
func (h *Hub) Dispatch(ctx context.Context, deviceID, action string, args map[string]any, timeout time.Duration) (tools.DeviceResult, error) {
	res := tools.DeviceResult{DeviceID: deviceID, Action: action}
	t, reused, err := h.CreateTaskEx(CreateOptions{
		DeviceID: deviceID, Action: action, Args: args, Origin: "agent",
		ApprovedBy: "agent-approval-gate", IdemKey: idemKey(deviceID, action, args),
	})
	res.TaskID = t.ID
	res.StartedAt = t.CreatedAt
	if err != nil {
		res.Status = proto.TaskFailed
		res.Error = err.Error()
		return res, err
	}
	if reused {
		h.lg.Info("复用在途跨端任务，直接等回执", "task", t.ID, "device", deviceID)
	}

	done, err := h.WaitTask(ctx, t.ID, timeout)
	res.Status = done.Status
	res.Output = done.Result
	res.Error = done.Error
	res.FinishedAt = done.FinishedAt
	switch done.Status {
	case proto.TaskDone:
		return res, nil
	case proto.TaskFailed:
		if res.Error == "" {
			res.Error = "设备执行失败"
		}
		return res, fmt.Errorf("设备 %s 执行 %s 失败：%s", deviceID, action, res.Error)
	case proto.TaskRejected:
		if res.Error == "" {
			res.Error = "设备侧拒绝执行"
		}
		return res, fmt.Errorf("设备 %s 拒绝执行 %s：%s", deviceID, action, res.Error)
	}
	if err != nil {
		return res, err
	}
	return res, fmt.Errorf("任务 %s 未完成（状态 %s）", t.ID, done.Status)
}

// idemKey 幂等键：同设备 + 同动作 + 同参数 → 同键
func idemKey(deviceID, action string, args map[string]any) string {
	raw := jsonBrief(args)
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceID + "|" + action + "|" + raw))
	return "ag" + strconv.FormatUint(uint64(h.Sum32()), 36)
}
