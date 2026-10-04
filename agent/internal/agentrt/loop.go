package agentrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"baize/internal/hooks"
	"baize/internal/llm"
	"baize/internal/memory"
	"baize/internal/tools"
)

// Config 运行时配置
type Config struct {
	Provider    llm.Provider   // 主通道（模型路由给出）
	Fallbacks   []llm.Provider // 回退通道
	Tools       *tools.Registry
	Memory      *memory.Store
	Hooks       *hooks.Bus
	Store       *Store
	Workspace   *tools.Workspace
	Checkpoints *CheckpointManager
	Summarizer  memory.Summarizer
	Logger      *slog.Logger

	Recipe      string // 任务类型（chat / plan / code …），用于模型路由
	SessionID   string
	RunID       string // 可选的运行 id（调用方想提前跟踪就自己给一个；空则自动生成）
	MaxSteps    int    // 默认 12
	MaxRetries  int    // 单次模型调用重试次数，默认 2
	TokenBudget int    // 上下文预算，默认 8000
	SystemExtra string
	// Approve 危险操作的审批回调：返回 true 才执行；nil 表示没有审批通道（一律拒绝）
	Approve func(tool string, args map[string]any) bool
	// ApproveAllTools 审批松紧度「严」：不只危险操作，**每一次**工具调用都要人批。
	// 默认 false = 「中」口径，只有 Dangerous() 的工具才过闸门。
	ApproveAllTools bool
	// CheckpointBeforeWrite 危险工具执行前是否自动打快照
	CheckpointBeforeWrite bool
}

// Runner 运行时
type Runner struct {
	cfg        Config
	compressor *Compressor
}

// New 创建运行时
func New(cfg Config) *Runner {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 12
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 2
	}
	if cfg.TokenBudget <= 0 {
		cfg.TokenBudget = 8000
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{
		cfg:        cfg,
		compressor: NewCompressor(cfg.TokenBudget, cfg.Summarizer, cfg.Logger),
	}
}

// RunResult 一次运行的结果
type RunResult struct {
	RunID        string       `json:"runId"`
	SessionID    string       `json:"sessionId"`
	Text         string       `json:"text"`
	Steps        int          `json:"steps"`
	ToolCalls    int          `json:"toolCalls"`
	Retries      int          `json:"retries"`
	Usage        llm.Usage    `json:"usage"`
	Checkpoints  []string     `json:"checkpoints,omitempty"`
	MemoryChunks int          `json:"memoryChunks"`
	StartedAt    int64        `json:"startedAt"`
	FinishedAt   int64        `json:"finishedAt"`
	Errors       []string     `json:"errors,omitempty"`
	Trace        []TraceEntry `json:"trace,omitempty"`
}

// TraceEntry 执行轨迹（干了什么、结果如何）
type TraceEntry struct {
	Step   int    `json:"step"`
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
}

// Run 跑一次任务：目标 → 工具链 → 记忆落盘
func (r *Runner) Run(ctx context.Context, goal string) (RunResult, error) {
	goal = strings.TrimSpace(goal)
	res := RunResult{RunID: r.runID(), SessionID: r.cfg.SessionID, StartedAt: time.Now().UnixMilli()}
	if goal == "" {
		return res, errors.New("任务目标不能为空")
	}
	if r.cfg.Provider == nil {
		return res, llm.ErrNoProvider
	}
	if r.cfg.Hooks != nil {
		r.cfg.Hooks.Emit(ctx, hooks.EventRunStart, hooks.Payload{"runId": res.RunID, "goal": goal})
	}

	system := r.buildSystemPrompt(ctx, goal)
	messages := []llm.Message{{Role: llm.RoleUser, Content: goal}}
	msgIdx := 0
	persist := func(m llm.Message) {
		if err := r.cfg.Store.AppendMessage(res.RunID, msgIdx, m); err != nil {
			r.cfg.Logger.Warn("对话消息落库失败", "err", err)
		}
		msgIdx++
	}
	persist(messages[0])

	finish := func(status string, runErr error) (RunResult, error) {
		res.FinishedAt = time.Now().UnixMilli()
		// 记忆落盘：这次的结论要留下来
		res.MemoryChunks = r.writeMemory(ctx, goal, res)
		if err := r.cfg.Store.SaveRun(RunRecord{
			RunID: res.RunID, SessionID: res.SessionID, Recipe: r.cfg.Recipe, Goal: goal,
			Status: status, Text: res.Text, Steps: res.Steps, ToolCalls: res.ToolCalls, Retries: res.Retries,
			PromptTokens: res.Usage.PromptTokens, OutTokens: res.Usage.CompletionTokens,
			StartedAt: res.StartedAt, FinishedAt: res.FinishedAt, Err: errString(runErr),
			Meta: map[string]any{
				"checkpoints": res.Checkpoints, "memoryChunks": res.MemoryChunks,
				"trace": res.Trace, "errors": res.Errors,
			},
		}); err != nil {
			r.cfg.Logger.Warn("运行记录落库失败", "err", err)
		}
		if r.cfg.Hooks != nil {
			r.cfg.Hooks.Emit(ctx, hooks.EventRunFinish, hooks.Payload{
				"runId": res.RunID, "status": status, "steps": res.Steps,
				"toolCalls": res.ToolCalls, "text": res.Text, "err": errString(runErr),
			})
		}
		return res, runErr
	}

	for step := 1; step <= r.cfg.MaxSteps; step++ {
		res.Steps = step

		// 1) 上下文压缩
		compressed, changed, err := r.compressor.Compress(ctx, messages, system)
		if err != nil {
			r.cfg.Logger.Warn("上下文压缩失败，继续用原文", "err", err)
		}
		if changed {
			messages = compressed
			if r.cfg.Hooks != nil {
				r.cfg.Hooks.Emit(ctx, hooks.EventCompress, hooks.Payload{
					"runId": res.RunID, "step": step, "messages": len(messages),
				})
			}
		}

		// 2) 问模型（带重试与回退）
		resp, retries, err := r.chat(ctx, system, messages)
		res.Retries += retries
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			return finish("failed", fmt.Errorf("模型调用失败：%w", err))
		}
		res.Usage.PromptTokens += resp.Usage.PromptTokens
		res.Usage.CompletionTokens += resp.Usage.CompletionTokens
		res.Usage.TotalTokens += resp.Usage.TotalTokens

		assistant := llm.Message{Role: llm.RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls}
		messages = append(messages, assistant)
		persist(assistant)

		// 3) 没有工具调用 → 收工
		if len(resp.ToolCalls) == 0 {
			res.Text = strings.TrimSpace(resp.Text)
			if res.Text == "" {
				res.Text = "（模型没有给出结论）"
			}
			return finish("done", nil)
		}

		// 4) 依次执行工具
		for _, call := range resp.ToolCalls {
			res.ToolCalls++
			entry := TraceEntry{Step: step, Tool: call.Name, Args: brief(argsJSON(call.Args))}
			start := time.Now()

			if r.cfg.Hooks != nil {
				r.cfg.Hooks.Emit(ctx, hooks.EventToolBefore, hooks.Payload{
					"runId": res.RunID, "tool": call.Name, "args": call.Args,
				})
			}

			out, callErr := r.executeTool(ctx, call, &res)
			entry.Ms = time.Since(start).Milliseconds()
			if callErr != nil {
				entry.Error = callErr.Error()
				res.Errors = append(res.Errors, call.Name+": "+callErr.Error())
				if r.cfg.Hooks != nil {
					r.cfg.Hooks.Emit(ctx, hooks.EventToolError, hooks.Payload{
						"runId": res.RunID, "tool": call.Name, "err": callErr.Error(),
					})
				}
			} else {
				entry.Result = brief(out)
				if r.cfg.Hooks != nil {
					r.cfg.Hooks.Emit(ctx, hooks.EventToolAfter, hooks.Payload{
						"runId": res.RunID, "tool": call.Name, "result": brief(out),
					})
				}
			}
			res.Trace = append(res.Trace, entry)

			// 喂给模型的是完整版：轨迹里那份 entry.Result 是 brief 过的短版，只给人看
			content := toolOutput(out)
			if callErr != nil {
				content = "工具执行失败：" + callErr.Error() + "\n（请换个方式或换条路，不要重复同样的调用）"
			}
			toolMsg := llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: content}
			messages = append(messages, toolMsg)
			persist(toolMsg)
		}
	}

	err := fmt.Errorf("超过最大步数（%d 步）仍未完成，已停下避免无限循环", r.cfg.MaxSteps)
	res.Errors = append(res.Errors, err.Error())
	return finish("failed", err)
}

// executeTool 执行单个工具调用（走审批闸门 + 变更前快照）
func (r *Runner) executeTool(ctx context.Context, call llm.ToolCall, res *RunResult) (any, error) {
	if r.cfg.Tools == nil {
		return nil, errors.New("运行时没有注册任何工具")
	}
	if _, ok := r.cfg.Tools.Get(call.Name); !ok {
		return nil, fmt.Errorf("%w：%s（可用：%s）", tools.ErrUnknownTool, call.Name,
			strings.Join(r.cfg.Tools.SortedNames(), ", "))
	}

	// 审批闸门：危险工具（或「严」档下的所有工具）必须过闸。
	// 没有审批通道（Approve==nil）时一律拒绝——宁可不动手，也不能悄悄执行危险操作。
	if r.cfg.ApproveAllTools || r.cfg.Tools.IsDangerous(call.Name) {
		if r.cfg.Approve == nil || !r.cfg.Approve(call.Name, call.Args) {
			if r.cfg.Hooks != nil {
				r.cfg.Hooks.Emit(ctx, hooks.EventToolBlocked, hooks.Payload{
					"runId": res.RunID, "tool": call.Name, "args": call.Args,
				})
			}
			return nil, fmt.Errorf("被审批闸门拒绝：%s 未获人工批准", call.Name)
		}
	}
	// 会改工作目录的工具：动手之前先打快照（写文件不需要审批，但必须留退路）
	if r.cfg.CheckpointBeforeWrite && r.cfg.Checkpoints != nil && r.cfg.Tools.IsMutating(call.Name) {
		info, err := r.cfg.Checkpoints.Create(call.Name)
		if err != nil {
			return nil, fmt.Errorf("变更前快照失败，已中止该工具调用：%w", err)
		}
		res.Checkpoints = append(res.Checkpoints, info.ID)
		if r.cfg.Hooks != nil {
			r.cfg.Hooks.Emit(ctx, hooks.EventCheckpoint, hooks.Payload{
				"runId": res.RunID, "id": info.ID, "tool": call.Name, "files": info.Files,
			})
		}
	}
	return r.cfg.Tools.Call(ctx, call.Name, call.Args)
}

// chat 调模型：主通道失败重试，再逐个回退通道
func (r *Runner) chat(ctx context.Context, system string, messages []llm.Message) (llm.Response, int, error) {
	req := llm.Request{
		System:      system,
		Messages:    messages,
		Temperature: 0.2,
	}
	if r.cfg.Tools != nil {
		req.Tools = r.cfg.Tools.Specs()
	}
	if r.cfg.Hooks != nil {
		r.cfg.Hooks.Emit(ctx, hooks.EventLLMRequest, hooks.Payload{
			"runId": "", "provider": r.cfg.Provider.Name(), "messages": len(messages), "tools": len(req.Tools),
		})
	}

	chain := append([]llm.Provider{r.cfg.Provider}, r.cfg.Fallbacks...)
	retries := 0
	var lastErr error
	for i, p := range chain {
		attempts := r.cfg.MaxRetries + 1
		if i > 0 {
			attempts = 1 // 回退通道只试一次，避免叠加等待
		}
		for a := 0; a < attempts; a++ {
			resp, err := p.Chat(ctx, req)
			if err == nil {
				if r.cfg.Hooks != nil {
					r.cfg.Hooks.Emit(ctx, hooks.EventLLMResponse, hooks.Payload{
						"provider": p.Name(), "text": brief(resp.Text), "toolCalls": len(resp.ToolCalls),
						"tokens": resp.Usage.TotalTokens,
					})
				}
				return resp, retries, nil
			}
			lastErr = err
			if a < attempts-1 {
				retries++
				wait := time.Duration(300*(a+1)) * time.Millisecond
				r.cfg.Logger.Warn("模型调用失败，重试中", "provider", p.Name(), "attempt", a+1, "err", err)
				if r.cfg.Hooks != nil {
					r.cfg.Hooks.Emit(ctx, hooks.EventLLMRetry, hooks.Payload{
						"provider": p.Name(), "attempt": a + 1, "err": err.Error(),
					})
				}
				select {
				case <-ctx.Done():
					return llm.Response{}, retries, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			if i+1 < len(chain) {
				r.cfg.Logger.Warn("主通道不可用，改用回退通道", "from", p.Name(), "err", err)
			}
		}
	}
	if lastErr == nil {
		lastErr = llm.ErrNoProvider
	}
	return llm.Response{}, retries, lastErr
}

// buildSystemPrompt 组装系统提示：角色 + 工作目录 + 工具 + 策略 + 相关记忆
func (r *Runner) buildSystemPrompt(ctx context.Context, goal string) string {
	var b strings.Builder
	b.WriteString("你是白泽，一个跑在本机的私人智能体。目标：用尽量少的步骤把事情真正做完。\n")
	if r.cfg.Workspace != nil {
		b.WriteString("工作目录：" + r.cfg.Workspace.Root() + "\n")
	}
	if r.cfg.Tools != nil {
		b.WriteString("可用工具：" + strings.Join(r.cfg.Tools.SortedNames(), "、") + "\n")
	}
	b.WriteString(`规则：
1. 需要事实、文件内容、文件列表时必须调用工具，不要凭空猜。
2. 危险操作（删除、执行命令）会先经过人工审批；被拒绝时换方案并说明，不要反复重试同一个调用。
3. 文件操作一律使用工作目录内的路径。
4. 结束时只汇报结论、产物路径和未完成事项，不要复述过程。
`)
	if len(r.cfg.Fallbacks) > 0 {
		b.WriteString("（模型通道带有回退，主通道异常时会自动切换，无需你在回答里提及。）\n")
	}
	if r.cfg.SystemExtra != "" {
		b.WriteString("\n" + strings.TrimSpace(r.cfg.SystemExtra) + "\n")
	}
	// 注意：记忆召回不在这里做。运行时的记忆注入由上层服务（agentsvc）按配置的
	// 主动召回闸门（相似度下限 / 相对分数下限 / token 预算）统一决定，并通过
	// SystemExtra 传进来；这里再召回一次会导致「关掉自动召回也不生效」。
	return b.String()
}

// writeMemory 运行收尾时把结论写进记忆（记忆落盘）
func (r *Runner) writeMemory(ctx context.Context, goal string, res RunResult) int {
	if r.cfg.Memory == nil {
		return 0
	}
	var b strings.Builder
	b.WriteString("任务：" + goal + "\n")
	if len(res.Trace) > 0 {
		b.WriteString("过程：\n")
		for _, t := range res.Trace {
			line := fmt.Sprintf("- %s(%s)", t.Tool, t.Args)
			if t.Error != "" {
				line += " → 失败：" + t.Error
			} else {
				line += " → " + t.Result
			}
			b.WriteString(brief(line) + "\n")
		}
	}
	if strings.TrimSpace(res.Text) != "" {
		b.WriteString("结论：" + res.Text + "\n")
	}
	out, err := r.cfg.Memory.Ingest(b.String(), memory.IngestOptions{
		Source: "agent", SourceRef: "run:" + res.RunID, Kind: "task",
		Title: "任务：" + brief(goal),
		// 同一个目标在同一天重复跑只留最新一次的结论（后写覆盖先写）。
		// 不这么做的话，反复问同一件事会在记忆里堆出一串近似重复的条目，
		// 把语义检索的结果稀释掉；逐次的过程历史「运行记录」里一直都有。
		DocKey: memory.RunGoalKey(goal, res.StartedAt),
	})
	if err != nil {
		r.cfg.Logger.Warn("记忆落盘失败", "err", err)
		return 0
	}
	if r.cfg.Hooks != nil {
		r.cfg.Hooks.Emit(ctx, hooks.EventMemoryWrite, hooks.Payload{
			"runId": res.RunID, "chunks": out.Chunks, "tokens": out.Tokens,
		})
	}
	return out.Chunks
}

// runID 本次运行的 id：调用方指定就用它，否则现生成
func (r *Runner) runID() string {
	if strings.TrimSpace(r.cfg.RunID) != "" {
		return r.cfg.RunID
	}
	return NewRunID()
}

/* ---------- 小工具 ---------- */

// NewRunID 生成一个运行 id（调用方可以在开跑之前先拿到它，便于跟踪这次运行）
func NewRunID() string {
	const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 4)
	for i := range b {
		b[i] = base36[rand.Intn(len(base36))]
	}
	return "r" + strconv.FormatInt(time.Now().UnixMilli(), 36) + string(b)
}

func argsJSON(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	return string(raw)
}

// stringify 把任意值序列化成文本（不改换行；工具结果与参数都走这里）
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

// brief 给日志 / 轨迹 / 记忆用的短版：单行、300 字以内
func brief(v any) string {
	s := strings.ReplaceAll(stringify(v), "\n", " ")
	if len([]rune(s)) > 300 {
		return string([]rune(s)[:300]) + "…"
	}
	return s
}

// maxToolOutput 喂给模型的单个工具结果上限（字符数）。
//
// 这里**绝不能**用 brief 的 300 字当上限：工具结果常常是 JSON，拦腰砍断会让模型看不到
// 关键字段，于是反复重试同一个调用——真机联调时手机 todo.list 的回执正好被砍在 title
// 之前，模型连试三次都拿不到标题，最后只能报「任务未完成」。
// 截断时也必须说清「被截断了 + 完整多长 + 怎么缩小范围」，否则模型分不清是"没有数据"
// 还是"没显示出来"。
const maxToolOutput = 6000

// toolOutput 工具结果 -> 喂给模型的那段文本
func toolOutput(v any) string {
	s := stringify(v)
	r := []rune(s)
	if len(r) <= maxToolOutput {
		return s
	}
	return string(r[:maxToolOutput]) + fmt.Sprintf(
		"\n…（结果太长已截断：完整约 %d 字符，这里只给了前 %d 个。请缩小范围重试——加 limit/keyword 过滤或分页取，不要原样重复调用）",
		len(r), maxToolOutput)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
