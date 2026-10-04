// Package activity 是白泽的「智能体活动追踪」（对标 OpenHuman 的活动追踪，档位恒为 AlwaysOn）。
//
// 用户已决定：始终开启，不需要手动选档。所以这里没有档位旋钮，运行时就一直看着。
//
// 与 OpenHuman 的对应关系：
//   - 实时状态沿用它的 LiveSync 三字段（stage / detail / updatedAtMs）；
//   - 静默过久又没收尾的活动按「陈旧」处理（读取时惰性清理），
//     避免界面被一个卡死的旧状态永远钉住——这正是 OpenHuman 用 30 分钟上限解决的坑。
//
// 数据来源是白泽自己的生命周期事件总线（hooks.Bus），不额外引入新的事件通道：
// 一次运行从 run.start → llm.* / tool.* → memory.write → run.finish，
// 追踪器把每一步翻成一个中文阶段，顺手记一小段轨迹供控制台/手机端展示。
//
// 状态只留在进程内存里（与 OpenHuman 一致，不落盘）：进程重启后从「空闲」重新开始，
// 而不是拿一份过期的旧活动冒充"正在干活"。
package activity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"baize/internal/hooks"
)

// Level 白泽的活动档位恒为 always_on（用户已决定始终开启，不做档位选择）。
const Level = "always_on"

// 活动阶段（stage）。非终态表示"还在跑"，终态表示这一轮已经收尾。
const (
	StageIdle       = "idle"        // 空闲：没有正在跑的活动
	StageRunning    = "running"     // 一次运行开始
	StageThinking   = "thinking"    // 正在问模型
	StageRetrying   = "retrying"    // 模型调用失败，重试中
	StageTool       = "tool"        // 正在调工具
	StageToolDone   = "tool_done"   // 工具返回
	StageToolError  = "tool_error"  // 工具报错
	StageBlocked    = "blocked"     // 被审批闸门拦下
	StageCompress   = "compressing" // 上下文压缩
	StageCheckpoint = "checkpoint"  // 变更前打快照
	StageMemory     = "memory"      // 记忆落盘
	StageDone       = "done"        // 一轮运行正常收尾（终态）
	StageError      = "error"       // 出错收尾（终态）
)

// staleAfter 静默超过这个时长、又没收尾的活动，按「陈旧」处理（不再算在跑）。
const staleAfter = 30 * time.Minute

// Live 当前活动的实时状态（对齐 OpenHuman 的 LiveSync 三字段）
type Live struct {
	Stage       string `json:"stage"`
	Detail      string `json:"detail,omitempty"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
	RunID       string `json:"runId,omitempty"`
}

// Step 一条活动轨迹（给界面看时间线，新的在前）
type Step struct {
	At     int64  `json:"at"`
	Stage  string `json:"stage"`
	Detail string `json:"detail,omitempty"`
	RunID  string `json:"runId,omitempty"`
}

// Stats 累计统计（进程内，不落盘）
type Stats struct {
	Runs      int   `json:"runs"`
	ToolCalls int   `json:"toolCalls"`
	Blocked   int   `json:"blocked"`
	Errors    int   `json:"errors"`
	StartedAt int64 `json:"startedAtMs,omitempty"`
	LastRunAt int64 `json:"lastRunAtMs,omitempty"`
}

// Snapshot 对外快照（HTTP /api/agent/activity 与控制台都用它）
type Snapshot struct {
	Level       string `json:"level"`
	Active      bool   `json:"active"`
	Stage       string `json:"stage"`
	Detail      string `json:"detail,omitempty"`
	UpdatedAtMs int64  `json:"updatedAtMs,omitempty"`
	SinceMs     int64  `json:"sinceMs,omitempty"`
	RunID       string `json:"runId,omitempty"`
	Note        string `json:"note,omitempty"`
	Steps       []Step `json:"steps"`
	Stats       Stats  `json:"stats"`
}

// Tracker 活动追踪器（始终开启）
type Tracker struct {
	mu       sync.RWMutex
	live     *Live
	runStart int64
	steps    []Step
	maxSteps int
	stats    Stats
	now      func() int64
}

// New 创建追踪器；maxSteps 是轨迹条数上限（<=0 用默认 50）
func New(maxSteps int) *Tracker {
	if maxSteps <= 0 {
		maxSteps = 50
	}
	return &Tracker{
		maxSteps: maxSteps,
		steps:    []Step{},
		now:      func() int64 { return time.Now().UnixMilli() },
	}
}

// Attach 订阅生命周期事件。Bus 为 nil（没装总线）时静默不订阅，不报错。
func (t *Tracker) Attach(bus *hooks.Bus) {
	if t == nil || bus == nil {
		return
	}
	bus.OnAny(func(_ context.Context, ev hooks.Event, p hooks.Payload) {
		t.note(ev, p)
	})
}

// Snapshot 当前活动快照
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{Level: Level, Stage: StageIdle, Steps: []Step{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.pruneLocked(now)

	snap := Snapshot{
		Level: Level,
		Steps: append([]Step{}, t.steps...),
		Stats: t.stats,
	}
	if t.live != nil {
		snap.Active = true
		snap.Stage = t.live.Stage
		snap.Detail = t.live.Detail
		snap.UpdatedAtMs = t.live.UpdatedAtMs
		snap.RunID = t.live.RunID
		snap.SinceMs = t.runStart
		return snap
	}
	snap.Stage = StageIdle
	if len(t.steps) > 0 {
		last := t.steps[0]
		snap.Detail = "上次：" + label(last.Stage) + "（" + last.Detail + "）"
	}
	snap.Note = "当前没有正在跑的活动"
	return snap
}

// note 处理一条生命周期事件
func (t *Tracker) note(ev hooks.Event, p hooks.Payload) {
	if t == nil {
		return
	}
	now := t.now()
	stage, detail := stageOf(ev, p)
	runID := str(p["runId"])

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stats.StartedAt == 0 {
		t.stats.StartedAt = now
	}
	switch ev {
	case hooks.EventRunStart:
		t.stats.Runs++
		t.runStart = now
	case hooks.EventToolBefore:
		t.stats.ToolCalls++
	case hooks.EventToolBlocked:
		t.stats.Blocked++
	case hooks.EventToolError, hooks.EventError:
		t.stats.Errors++
	case hooks.EventRunFinish:
		t.stats.LastRunAt = now
		if str(p["status"]) == "failed" {
			t.stats.Errors++
		}
	}

	t.pushLocked(Step{At: now, Stage: stage, Detail: detail, RunID: runID})

	if isTerminal(ev) {
		// 收尾：清掉"在跑"状态，界面上立刻回到空闲（但轨迹里留痕）
		t.live = nil
		t.runStart = 0
		return
	}
	t.live = &Live{Stage: stage, Detail: detail, UpdatedAtMs: now, RunID: runID}
}

// pushLocked 记一条轨迹（新的在前，超出上限截断）。调用方需持锁。
func (t *Tracker) pushLocked(s Step) {
	t.steps = append([]Step{s}, t.steps...)
	if len(t.steps) > t.maxSteps {
		t.steps = t.steps[:t.maxSteps]
	}
}

// pruneLocked 丢掉陈旧的在跑状态：静默太久又没收尾的，不再算在跑。
func (t *Tracker) pruneLocked(now int64) {
	if t.live != nil && now-t.live.UpdatedAtMs > staleAfter.Milliseconds() {
		t.live = nil
		t.runStart = 0
	}
}

// stageOf 把一个生命周期事件翻译成（阶段, 细节）
func stageOf(ev hooks.Event, p hooks.Payload) (string, string) {
	switch ev {
	case hooks.EventRunStart:
		return StageRunning, str(p["goal"])
	case hooks.EventLLMRequest:
		return StageThinking, "问模型（" + orDefault(str(p["provider"]), "未知通道") + "）"
	case hooks.EventLLMResponse:
		n := str(p["toolCalls"])
		if n != "" && n != "0" {
			return StageThinking, "模型决定调 " + n + " 个工具"
		}
		return StageThinking, "模型给出结论"
	case hooks.EventLLMRetry:
		return StageRetrying, "第 " + orDefault(str(p["attempt"]), "?") + " 次重试：" + str(p["err"])
	case hooks.EventToolBefore:
		return StageTool, str(p["tool"])
	case hooks.EventToolAfter:
		return StageToolDone, str(p["tool"])
	case hooks.EventToolError:
		return StageToolError, joinNonEmpty(str(p["tool"]), "：", str(p["err"]))
	case hooks.EventToolBlocked:
		return StageBlocked, str(p["tool"]) + "（未获人工批准）"
	case hooks.EventCompress:
		return StageCompress, "压缩到 " + str(p["messages"]) + " 条消息"
	case hooks.EventCheckpoint:
		return StageCheckpoint, "变更前快照：" + str(p["tool"])
	case hooks.EventMemoryWrite:
		return StageMemory, "落盘 " + str(p["chunks"]) + " 块记忆"
	case hooks.EventRunFinish:
		status := str(p["status"])
		detail := "状态 " + orDefault(status, "结束")
		if e := str(p["err"]); e != "" {
			detail += "：" + e
		}
		if status == "failed" {
			return StageError, detail
		}
		return StageDone, detail
	case hooks.EventError:
		return StageError, str(p["err"])
	}
	return string(ev), ""
}

// isTerminal 是否为终态事件（会结束"在跑"状态）
func isTerminal(ev hooks.Event) bool {
	return ev == hooks.EventRunFinish || ev == hooks.EventError
}

// label 阶段的中文短名（给"上次：……"那句话用）
func label(stage string) string {
	switch stage {
	case StageRunning:
		return "开始运行"
	case StageThinking:
		return "问模型"
	case StageRetrying:
		return "重试"
	case StageTool:
		return "调工具"
	case StageToolDone:
		return "工具返回"
	case StageToolError:
		return "工具报错"
	case StageBlocked:
		return "被审批拦下"
	case StageCompress:
		return "压缩上下文"
	case StageCheckpoint:
		return "打快照"
	case StageMemory:
		return "记忆落盘"
	case StageDone:
		return "完成"
	case StageError:
		return "失败"
	}
	return stage
}

/* ---------- 取值小工具 ---------- */

// str 把负载里的任意值转成字符串（取不到就空串）
func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func joinNonEmpty(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
	}
	return strings.TrimSpace(b.String())
}
