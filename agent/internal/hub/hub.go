// Package hub 是后端 Agent 的中枢：设备注册表 + 任务库 + 审批闸门 + 指令下发 + 回执处理。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"baize/shared/proto"
	"baize/internal/store"
)

// Conn 一条设备长连接（gorilla 的写操作不可并发，这里用锁串行化）
type Conn struct {
	ws         *websocket.Conn
	id         string
	remoteAddr string
	mu         sync.Mutex
}

// Send 发送一条消息给该设备
func (c *Conn) Send(env proto.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("消息编码失败：%w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// DeviceID 设备 id
func (c *Conn) DeviceID() string { return c.id }

// Hub 中枢
type Hub struct {
	st      *store.Store
	lg      *slog.Logger
	token   string
	timeout time.Duration
	started time.Time

	mu    sync.RWMutex
	conns map[string]*Conn
}

// New 创建 Hub。token 非空时，设备注册必须携带同样的令牌；timeout 是回执超时。
func New(st *store.Store, lg *slog.Logger, token string, timeout time.Duration) *Hub {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Hub{
		st:      st,
		lg:      lg,
		token:   token,
		timeout: timeout,
		started: time.Now(),
		conns:   map[string]*Conn{},
	}
}

// Uptime 运行时长
func (h *Hub) Uptime() time.Duration { return time.Since(h.started) }

// Store 暴露存储（仅供控制台读取）
func (h *Hub) Store() *store.Store { return h.st }

// Token 配对令牌
func (h *Hub) Token() string { return h.token }

// ReceiptTimeout 回执超时
func (h *Hub) ReceiptTimeout() time.Duration { return h.timeout }

// Online 设备是否在线
func (h *Hub) Online(deviceID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[deviceID]
	return ok
}

// OnlineCount 在线设备数
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

/* ---------- 注册 / 断开 ---------- */

// Register 处理 hello：校验令牌、登记设备、补发排队任务
func (h *Hub) Register(ws *websocket.Conn, hello proto.Hello, remoteAddr string) (*Conn, proto.HelloAck, error) {
	if h.token != "" && hello.Token != h.token {
		return nil, proto.HelloAck{}, errors.New("配对令牌不正确")
	}
	if hello.DeviceID == "" {
		return nil, proto.HelloAck{}, errors.New("缺少设备 id")
	}
	now := time.Now().UnixMilli()

	// 同一设备重复连接：踢掉旧连接
	c := &Conn{ws: ws, id: hello.DeviceID, remoteAddr: remoteAddr}
	h.mu.Lock()
	if old, ok := h.conns[c.id]; ok && old != nil {
		old.mu.Lock()
		_ = old.ws.Close()
		old.mu.Unlock()
		h.lg.Warn("设备重复连接，已关闭旧连接", "device", c.id)
	}
	h.conns[c.id] = c
	h.mu.Unlock()

	dev, exists, err := h.st.Device(c.id)
	if err != nil {
		return nil, proto.HelloAck{}, err
	}
	if !exists {
		dev.FirstSeen = now
	}
	dev.ID = c.id
	dev.Name = firstNonEmpty(hello.Name, hello.Hostname, c.id)
	dev.OS = hello.OS
	dev.Arch = hello.Arch
	dev.Hostname = hello.Hostname
	dev.User = hello.User
	dev.Version = hello.Version
	dev.Caps = hello.Caps
	dev.Online = true
	dev.LastSeen = now
	dev.RemoteAddr = remoteAddr
	if err := h.st.UpsertDevice(dev); err != nil {
		return nil, proto.HelloAck{}, err
	}

	queued := 0
	if tasks, err := h.st.TasksByStatus(proto.TaskQueued, c.id); err != nil {
		h.lg.Warn("统计排队任务失败", "device", c.id, "err", err)
	} else {
		queued = len(tasks)
	}

	ack := proto.HelloAck{
		DeviceID:    c.id,
		Server:      "baize-backend " + proto.Version,
		Protocol:    proto.Version,
		Time:        now,
		QueuedTasks: queued,
	}
	h.lg.Info("设备上线", "device", c.id, "name", dev.Name, "os", dev.OS+"/"+dev.Arch,
		"addr", remoteAddr, "queued", queued)
	if err := h.st.AddMemory(store.Memory{
		Kind: "device", DeviceID: c.id, Title: "设备上线：" + dev.Name,
		Content:    fmt.Sprintf("%s（%s/%s）已连接，能力：%s", dev.Name, dev.OS, dev.Arch, strings.Join(dev.Caps, ",")),
		Importance: 30,
	}); err != nil {
		h.lg.Warn("写记忆失败", "err", err)
	}
	return c, ack, nil
}

// StartDispatch 在 hello_ack 发出之后调用：把排队任务补发给刚上线的设备。
// 注意顺序：必须先让设备收到 hello_ack，再下发积压指令，否则端侧会在注册完成前收到指令。
func (h *Hub) StartDispatch(c *Conn) {
	if c == nil {
		return
	}
	n, err := h.DispatchQueued(c.id)
	if err != nil {
		h.lg.Warn("补发排队任务失败", "device", c.id, "err", err)
		return
	}
	if n > 0 {
		h.lg.Info("已向上线设备补发排队任务", "device", c.id, "count", n)
	}
}

// Unregister 处理断开：仅在当前连接仍是这条时摘除
func (h *Hub) Unregister(c *Conn) {
	if c == nil {
		return
	}
	h.mu.Lock()
	cur, ok := h.conns[c.id]
	if ok && cur == c {
		delete(h.conns, c.id)
	}
	h.mu.Unlock()
	if !ok || cur != c {
		return
	}
	now := time.Now().UnixMilli()
	if err := h.st.SetDeviceOnline(c.id, false, now); err != nil {
		h.lg.Warn("更新设备离线状态失败", "device", c.id, "err", err)
	}
	h.lg.Warn("设备离线", "device", c.id, "addr", c.remoteAddr)
	_ = h.st.AddMemory(store.Memory{
		Kind: "device", DeviceID: c.id, Title: "设备离线：" + c.id,
		Content: "连接已断开，未回执的任务会继续排队等待补发。", Importance: 25,
	})
}

/* ---------- 事件 / 心跳 ---------- */

// OnEvent 事件上报：落记录 + 刷新活跃时间
func (h *Hub) OnEvent(c *Conn, ev proto.Event) {
	at := ev.At
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	payload := map[string]any{"kind": ev.Kind, "title": ev.Title, "data": ev.Data}
	if err := h.st.AddRecord(store.Record{
		Kind: proto.RecordEvent, DeviceID: c.id, At: at, Payload: payload,
	}); err != nil {
		h.lg.Warn("写事件记录失败", "err", err)
	}
	if err := h.st.TouchDevice(c.id, at); err != nil {
		h.lg.Warn("刷新设备活跃时间失败", "err", err)
	}
	switch ev.Kind {
	case proto.EventWindow:
		h.lg.Info("事件·窗口", "device", c.id, "title", ev.Title,
			"process", str(ev.Data, "process"), "pid", str(ev.Data, "pid"))
		h.lg.Debug("窗口事件详情", "device", c.id, "data", jsonBrief(ev.Data))
	default:
		h.lg.Info("事件", "device", c.id, "kind", ev.Kind, "title", ev.Title)
	}
}

// OnHeartbeat 心跳：只刷新活跃时间（日志落到 debug，避免刷屏）
func (h *Hub) OnHeartbeat(c *Conn, hb proto.Heartbeat) {
	at := hb.At
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	if err := h.st.TouchDevice(c.id, at); err != nil {
		h.lg.Warn("刷新设备活跃时间失败", "err", err)
	}
	h.lg.Debug("心跳", "device", c.id, "uptimeSec", hb.UptimeSec, "busy", hb.Busy)
}

/* ---------- 任务 ---------- */

// CreateTask 新建任务（用户侧入口）。危险动作强制进入待审批，不接受调用方"免审批"的声明。
// Agent 跨端派活请用 CreateTaskEx（可带审批结果与幂等键）。
func (h *Hub) CreateTask(deviceID, action string, args map[string]any, origin string) (store.Task, error) {
	t, _, err := h.CreateTaskEx(CreateOptions{DeviceID: deviceID, Action: action, Args: args, Origin: origin})
	return t, err
}

// Approve 人工审批通过 → 立即下发
func (h *Hub) Approve(taskID, by string) (store.Task, error) {
	t, ok, err := h.st.Task(taskID)
	if err != nil {
		return store.Task{}, err
	}
	if !ok {
		return store.Task{}, fmt.Errorf("任务不存在：%s", taskID)
	}
	if t.Status != proto.TaskPendingApproval {
		return store.Task{}, fmt.Errorf("任务当前状态是 %s，不能审批（只有 pending_approval 可审批）", t.Status)
	}
	if strings.TrimSpace(by) == "" {
		by = "operator"
	}
	now := time.Now().UnixMilli()
	if err := h.st.MarkTaskApproved(t.ID, by, now); err != nil {
		return store.Task{}, err
	}
	_ = h.st.AddRecord(store.Record{
		Kind: proto.RecordApproval, DeviceID: t.DeviceID, TaskID: t.ID, At: now,
		Payload: map[string]any{"decision": "approved", "by": by, "action": t.Action},
	})
	h.lg.Info("审批通过", "task", t.ID, "device", t.DeviceID, "action", t.Action, "by", by)
	t.Status = proto.TaskQueued
	t.ApprovedBy = by
	if err := h.dispatchOne(t); err != nil {
		h.lg.Warn("审批后下发失败，任务排队等待设备上线", "task", t.ID, "device", t.DeviceID, "err", err)
	}
	out, _, _ := h.st.Task(t.ID)
	return out, nil
}

// Reject 人工审批驳回
func (h *Hub) Reject(taskID, by, reason string) (store.Task, error) {
	t, ok, err := h.st.Task(taskID)
	if err != nil {
		return store.Task{}, err
	}
	if !ok {
		return store.Task{}, fmt.Errorf("任务不存在：%s", taskID)
	}
	if t.Status != proto.TaskPendingApproval {
		return store.Task{}, fmt.Errorf("任务当前状态是 %s，不能驳回（只有 pending_approval 可驳回）", t.Status)
	}
	if strings.TrimSpace(by) == "" {
		by = "operator"
	}
	if strings.TrimSpace(reason) == "" {
		reason = "人工驳回"
	}
	now := time.Now().UnixMilli()
	if err := h.st.MarkTaskRejected(t.ID, by, reason, now); err != nil {
		return store.Task{}, err
	}
	_ = h.st.AddRecord(store.Record{
		Kind: proto.RecordApproval, DeviceID: t.DeviceID, TaskID: t.ID, At: now,
		Payload: map[string]any{"decision": "rejected", "by": by, "reason": reason, "action": t.Action},
	})
	h.lg.Warn("审批驳回", "task", t.ID, "device", t.DeviceID, "action", t.Action, "by", by, "reason", reason)
	h.saveTaskMemory(t, proto.TaskRejected, nil, reason, now)
	out, _, _ := h.st.Task(t.ID)
	return out, nil
}

// DispatchQueued 把该设备所有排队任务下发出去，返回成功下发条数
func (h *Hub) DispatchQueued(deviceID string) (int, error) {
	tasks, err := h.st.TasksByStatus(proto.TaskQueued, deviceID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tasks {
		if err := h.dispatchOne(t); err != nil {
			h.lg.Warn("下发失败", "task", t.ID, "device", t.DeviceID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// dispatchOne 下发单条任务并留“指令”记录
func (h *Hub) dispatchOne(t store.Task) error {
	env, err := proto.New(proto.TypeCommand, t.DeviceID, t.ID, proto.Command{
		TaskID:       t.ID,
		Action:       t.Action,
		Args:         t.Args,
		NeedApproval: t.NeedApproval,
		ApprovedBy:   t.ApprovedBy,
		IssuedAt:     time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	c := h.conn(t.DeviceID)
	if c == nil {
		return fmt.Errorf("设备不在线：%s", t.DeviceID)
	}
	if err := c.Send(env); err != nil {
		return fmt.Errorf("发送失败：%w", err)
	}
	now := time.Now().UnixMilli()
	if err := h.st.MarkTaskDispatched(t.ID, now); err != nil {
		return err
	}
	_ = h.st.AddRecord(store.Record{
		Kind: proto.RecordCommand, DeviceID: t.DeviceID, TaskID: t.ID, At: now,
		Payload: map[string]any{
			"action": t.Action, "args": t.Args, "needApproval": t.NeedApproval,
			"approvedBy": t.ApprovedBy, "origin": t.Origin,
		},
	})
	h.lg.Info("指令已下发", "task", t.ID, "device", t.DeviceID, "action", t.Action, "args", jsonBrief(t.Args))
	return nil
}

// OnReceipt 处理执行回执：更新任务状态、留“回执”记录、任务收尾时写记忆
func (h *Hub) OnReceipt(c *Conn, rc proto.Receipt) {
	t, ok, err := h.st.Task(rc.TaskID)
	if err != nil {
		h.lg.Error("读取任务失败", "task", rc.TaskID, "err", err)
		return
	}
	if !ok {
		h.lg.Warn("收到未知任务的回执，已忽略", "task", rc.TaskID, "device", c.id)
		return
	}
	if t.DeviceID != c.id {
		h.lg.Warn("回执设备与任务目标不一致，已忽略", "task", rc.TaskID, "taskDevice", t.DeviceID, "from", c.id)
		return
	}
	if t.Status == proto.TaskDone || t.Status == proto.TaskFailed || t.Status == proto.TaskRejected {
		h.lg.Debug("任务已收尾，忽略重复回执", "task", rc.TaskID, "status", t.Status, "again", rc.Status)
		return
	}

	at := rc.FinishedAt
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	status := rc.Status
	switch status {
	case proto.ReceiptRunning:
		status = proto.TaskRunning
	case proto.ReceiptDone:
		status = proto.TaskDone
	case proto.ReceiptFailed:
		status = proto.TaskFailed
	case proto.ReceiptRejected:
		status = proto.TaskRejected
	default:
		h.lg.Warn("回执状态未知，按失败处理", "task", rc.TaskID, "status", rc.Status)
		status = proto.TaskFailed
	}
	if err := h.st.MarkTaskResult(t.ID, status, rc.Result, rc.Error, at); err != nil {
		h.lg.Error("写入回执结果失败", "task", t.ID, "err", err)
		return
	}
	_ = h.st.AddRecord(store.Record{
		Kind: proto.RecordReceipt, DeviceID: c.id, TaskID: t.ID, At: at,
		Payload: map[string]any{"status": rc.Status, "result": rc.Result, "error": rc.Error},
	})

	switch status {
	case proto.TaskRunning:
		h.lg.Info("任务执行中", "task", t.ID, "device", c.id, "action", t.Action)
	case proto.TaskDone:
		h.lg.Info("任务完成", "task", t.ID, "device", c.id, "action", t.Action, "result", jsonBrief(rc.Result))
		h.saveTaskMemory(t, status, rc.Result, "", at)
	case proto.TaskFailed:
		h.lg.Error("任务失败", "task", t.ID, "device", c.id, "action", t.Action, "err", rc.Error)
		h.saveTaskMemory(t, status, rc.Result, rc.Error, at)
	case proto.TaskRejected:
		h.lg.Warn("任务被端侧拒绝", "task", t.ID, "device", c.id, "action", t.Action, "reason", rc.Error)
		h.saveTaskMemory(t, status, rc.Result, rc.Error, at)
	}
}

// saveTaskMemory 任务收尾 → 记忆落盘
func (h *Hub) saveTaskMemory(t store.Task, status string, result map[string]any, errMsg string, at int64) {
	title := map[string]string{
		proto.TaskDone:     "任务完成：" + t.Action,
		proto.TaskFailed:   "任务失败：" + t.Action,
		proto.TaskRejected: "任务被驳回：" + t.Action,
	}[status]
	if title == "" {
		title = "任务结束：" + t.Action
	}
	importance := 60
	if status == proto.TaskFailed {
		importance = 75
	}
	content := fmt.Sprintf("设备 %s；动作 %s；参数 %s；状态 %s",
		t.DeviceID, t.Action, jsonBrief(t.Args), status)
	if len(result) > 0 {
		content += "；结果 " + jsonBrief(result)
	}
	if errMsg != "" {
		content += "；说明 " + errMsg
	}
	if err := h.st.AddMemory(store.Memory{
		Kind: "task", DeviceID: t.DeviceID, TaskID: t.ID,
		Title: title, Content: content, Importance: importance, At: at,
	}); err != nil {
		h.lg.Warn("写记忆失败", "task", t.ID, "err", err)
	}
}

// SweepOnce 扫一次超时任务（已下发但长时间无回执）
func (h *Hub) SweepOnce() (int, error) {
	before := time.Now().Add(-h.timeout).UnixMilli()
	tasks, err := h.st.StaleTasks(before)
	if err != nil {
		return 0, err
	}
	for _, t := range tasks {
		now := time.Now().UnixMilli()
		msg := fmt.Sprintf("回执超时（超过 %s 无响应）", h.timeout)
		if err := h.st.MarkTaskResult(t.ID, proto.TaskFailed, nil, msg, now); err != nil {
			return 0, err
		}
		_ = h.st.AddRecord(store.Record{
			Kind: proto.RecordReceipt, DeviceID: t.DeviceID, TaskID: t.ID, At: now,
			Payload: map[string]any{"status": proto.ReceiptFailed, "error": msg, "timeout": true},
		})
		h.lg.Error("任务回执超时，标记失败", "task", t.ID, "device", t.DeviceID, "action", t.Action)
		h.saveTaskMemory(t, proto.TaskFailed, nil, msg, now)
	}
	return len(tasks), nil
}

// StartSweeper 启动超时扫描（每 10s 一次）
func (h *Hub) StartSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := h.SweepOnce(); err != nil {
					h.lg.Warn("超时扫描失败", "err", err)
				} else if n > 0 {
					h.lg.Warn("本轮超时任务数", "count", n)
				}
			}
		}
	}()
}

/* ---------- 内部工具 ---------- */

func (h *Hub) conn(deviceID string) *Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[deviceID]
}

func newTaskID() string {
	const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 4)
	for i := range b {
		b[i] = base36[rand.Intn(len(base36))]
	}
	return "t" + strconv.FormatInt(time.Now().UnixMilli(), 36) + string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func jsonBrief(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}
