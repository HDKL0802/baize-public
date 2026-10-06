// Package proto 定义后端与各端（桌面端 / 手机端）之间的内部消息契约。
//
// 这是**共享契约**：后端（agent）、桌面端（agent/cmd/desktop）、手机内核（core/cmd/bzcore）
// 都引用这一份，避免各端各写一套导致字段悄悄漂移。
//
// 传输：HTTP + WebSocket 双通道；消息统一用 Envelope 包裹。
// 三态：事件(event，端→后端) / 指令(command，后端→端) / 回执(receipt，端→后端)。
package proto

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Version 内部协议版本，端与后端不一致时后端会记警告。
const Version = "0.1.0"

// 消息类型
const (
	TypeHello     = "hello"     // 端 → 后端：注册（携带设备信息与能力）
	TypeHelloAck  = "hello_ack" // 后端 → 端：注册结果
	TypeHeartbeat = "heartbeat" // 端 → 后端：心跳
	TypeEvent     = "event"     // 端 → 后端：事件上报
	TypeCommand   = "command"   // 后端 → 端：指令下发
	TypeReceipt   = "receipt"   // 端 → 后端：执行回执
	TypeError     = "error"     // 后端 → 端：协议级错误
)

// 回执状态（端 → 后端）
const (
	ReceiptRunning  = "running"  // 已开始执行
	ReceiptDone     = "done"     // 执行成功
	ReceiptFailed   = "failed"   // 执行失败
	ReceiptRejected = "rejected" // 端侧拒绝执行
)

// 任务状态（后端视角，落库）
const (
	TaskPendingApproval = "pending_approval" // 危险动作，等人工审批
	TaskQueued          = "queued"           // 已批准/无需审批，等设备在线或待下发
	TaskDispatched      = "dispatched"       // 指令已发出，等回执
	TaskRunning         = "running"          // 设备回执"执行中"
	TaskDone            = "done"
	TaskFailed          = "failed"
	TaskRejected        = "rejected"
)

// 事件类型
const (
	EventWindow  = "window"  // 活动窗口/前台进程（桌面端）
	EventProcess = "process" // 进程概况（桌面端）
	EventNote    = "note"    // 端侧自由文本事件
)

// 动作（后端下发、端侧执行）
const (
	ActionPing      = "ping"       // 探活
	ActionSysInfo   = "sys.info"   // 取本机信息
	ActionWindowNow = "window.now" // 取当前活动窗口（桌面端）
	ActionFsStat    = "fs.stat"    // 查看文件/目录信息（桌面端）
	ActionFsDelete  = "fs.delete"  // 删除文件/目录（危险，强制审批；桌面端）

	// 手机端（待办内核）可执行的动作：均为只读，不改数据
	ActionTodoList  = "todo.list"  // 列出待办（可按状态/关键词过滤）
	ActionTodoStats = "todo.stats" // 待办统计（总数/完成/逾期等）
)

// 能力标记（hello.caps）
const (
	CapWindow   = "window.report"
	CapFs       = "fs"
	CapFsDelete = "fs.delete"
	CapTodo     = "todo" // 待办内核（手机端）
)

// 记录（三态 + 审批）落库用的种类
const (
	RecordEvent    = "event"
	RecordCommand  = "command"
	RecordReceipt  = "receipt"
	RecordApproval = "approval"
)

// Envelope 统一消息信封
type Envelope struct {
	Type     string          `json:"type"`
	DeviceID string          `json:"deviceId,omitempty"`
	TaskID   string          `json:"taskId,omitempty"`
	SentAt   int64           `json:"sentAt,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// New 构造一条消息（payload 为 nil 时留空）
func New(typ, deviceID, taskID string, payload any) (Envelope, error) {
	env := Envelope{Type: typ, DeviceID: deviceID, TaskID: taskID, SentAt: time.Now().UnixMilli()}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return env, fmt.Errorf("消息体编码失败：%w", err)
		}
		env.Payload = b
	}
	return env, nil
}

// MustNew 仅用于不可能失败的场景（测试/常量消息）
func MustNew(typ, deviceID, taskID string, payload any) Envelope {
	env, err := New(typ, deviceID, taskID, payload)
	if err != nil {
		panic(err)
	}
	return env
}

// Decode 解析消息体
func (e Envelope) Decode(v any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("消息体为空（type=%s）", e.Type)
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("消息体解析失败（type=%s）：%w", e.Type, err)
	}
	return nil
}

// Hello 端 → 后端 注册
type Hello struct {
	DeviceID  string   `json:"deviceId"`
	Name      string   `json:"name"`
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	Hostname  string   `json:"hostname"`
	User      string   `json:"user"`
	Version   string   `json:"version"`
	Protocol  string   `json:"protocol"`
	Caps      []string `json:"caps"`
	StartedAt int64    `json:"startedAt"`
	Token     string   `json:"token,omitempty"` // 配对令牌

	// GuiPerm 桌面端的「桌面控制」权限等级（0=关闭 1=只读 2=只读指定目录 3=只读指定盘 4=完全访问）。
	// 0 同时也是"不是桌面端/没上报"的默认值；手机端等其它端不上报这个字段。
	// 它在设备侧已经决定了 caps（权限不够的能力根本不上报），这里再报一次是为了
	// 让后端/控制台能直接把"这台机器放开了多少"显示给人看。
	GuiPerm   int      `json:"guiPerm,omitempty"`
	GuiScopes []string `json:"guiScopes,omitempty"`
}

// HelloAck 后端 → 端 注册结果
type HelloAck struct {
	DeviceID    string `json:"deviceId"`
	Server      string `json:"server"`
	Protocol    string `json:"protocol"`
	Time        int64  `json:"time"`
	QueuedTasks int    `json:"queuedTasks"` // 本次上线后端补发的排队任务数
}

// ErrorMsg 协议级错误
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Heartbeat 端 → 后端 心跳
type Heartbeat struct {
	At        int64 `json:"at"`
	UptimeSec int64 `json:"uptimeSec"`
	Busy      bool  `json:"busy"`
}

// Event 端 → 后端 事件
type Event struct {
	Kind  string         `json:"kind"`
	At    int64          `json:"at"`
	Title string         `json:"title,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

// Command 后端 → 端 指令
type Command struct {
	TaskID       string         `json:"taskId"`
	Action       string         `json:"action"`
	Args         map[string]any `json:"args,omitempty"`
	NeedApproval bool           `json:"needApproval"`
	ApprovedBy   string         `json:"approvedBy,omitempty"`
	IssuedAt     int64          `json:"issuedAt"`
}

// Receipt 端 → 后端 回执
type Receipt struct {
	TaskID     string         `json:"taskId"`
	Status     string         `json:"status"`
	Result     map[string]any `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  int64          `json:"startedAt,omitempty"`
	FinishedAt int64          `json:"finishedAt,omitempty"`
}

// knownActions 后端允许创建的动作白名单
var knownActions = []string{
	ActionPing, ActionSysInfo, ActionWindowNow, ActionFsStat, ActionFsDelete,
	ActionTodoList, ActionTodoStats,
}

// KnownAction 判断动作是否在白名单内
func KnownAction(action string) bool {
	for _, a := range knownActions {
		if a == action {
			return true
		}
	}
	return false
}

// KnownActions 返回动作白名单副本（供控制台渲染下拉框）
func KnownActions() []string {
	out := make([]string, len(knownActions))
	copy(out, knownActions)
	return out
}

// dangerousPrefixes 需要人工审批的动作（删除/提权/外发/改权限类）
var dangerousPrefixes = []string{
	"fs.delete", "proc.kill", "sys.exec", "net.upload", "perm.change", "msg.send",
}

// IsDangerous 判断动作是否必须人工审批。危险动作不接受调用方"免审批"的声明。
func IsDangerous(action string) bool {
	for _, p := range dangerousPrefixes {
		if action == p || strings.HasPrefix(action, p+".") {
			return true
		}
	}
	return false
}

// ActionNeedsPaths 判断动作是否需要 paths 参数（控制台据此显示输入框）
func ActionNeedsPaths(action string) bool {
	return action == ActionFsStat || action == ActionFsDelete
}
