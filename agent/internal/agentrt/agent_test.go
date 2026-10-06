package agentrt

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/internal/hooks"
	"baize/internal/llm"
	"baize/internal/memory"
	"baize/internal/tools"
)

type harness struct {
	t         *testing.T
	dir       string
	dataDir   string
	ws        *tools.Workspace
	reg       *tools.Registry
	mem       *memory.Store
	store     *Store
	bus       *hooks.Bus
	ck        *CheckpointManager
	fired     map[hooks.Event]int
	runnerFor func(p llm.Provider, cfg func(*Config)) *Runner
}

func newHarness(t *testing.T, withCheckpoints bool) *harness {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	ws, err := tools.NewWorkspace(filepath.Join(dir, "ws"), false)
	if err != nil {
		t.Fatalf("工作目录失败：%v", err)
	}
	mem, err := memory.Open(dataDir)
	if err != nil {
		t.Fatalf("记忆库失败：%v", err)
	}
	t.Cleanup(func() { mem.Close() })
	st, err := OpenStore(dataDir)
	if err != nil {
		t.Fatalf("运行记录库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := tools.NewRegistry()
	tools.RegisterFS(reg, ws)
	tools.RegisterMemory(reg, mem, memory.DefaultNamespace)

	bus := hooks.NewBus()
	h := &harness{t: t, dir: dir, dataDir: dataDir, ws: ws, reg: reg, mem: mem, store: st, bus: bus,
		fired: map[hooks.Event]int{}}
	for _, ev := range []hooks.Event{
		hooks.EventRunStart, hooks.EventRunFinish, hooks.EventToolBefore, hooks.EventToolAfter,
		hooks.EventToolError, hooks.EventToolBlocked, hooks.EventMemoryWrite, hooks.EventCheckpoint,
		hooks.EventCompress, hooks.EventLLMRetry,
	} {
		e := ev
		bus.On(e, func(context.Context, hooks.Event, hooks.Payload) { h.fired[e]++ })
	}
	if withCheckpoints {
		ck, err := NewCheckpointManager(dataDir, ws.Root(), 5)
		if err != nil {
			t.Fatalf("快照管理器失败：%v", err)
		}
		h.ck = ck
	}
	return h
}

func (h *harness) run(p llm.Provider, tweak func(*Config), goal string) (RunResult, error) {
	h.t.Helper()
	cfg := Config{
		Provider: p, Tools: h.reg, Memory: h.mem, Hooks: h.bus, Store: h.store,
		Workspace: h.ws, Checkpoints: h.ck, Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Recipe: "chat", MaxSteps: 8, TokenBudget: 4000, SessionID: "test-session",
	}
	if tweak != nil {
		tweak(&cfg)
	}
	return New(cfg).Run(context.Background(), goal)
}

// 验收用例：一句话任务 → 工具链 → 记忆落盘
func TestRunToolChainAndMemoryPersist(t *testing.T) {
	h := newHarness(t, true)
	p := llm.NewFake("fake", llm.Script(
		llm.CallTool("c1", "fs_list", map[string]any{"path": "."}),
		llm.CallTool("c2", "fs_write", map[string]any{"path": "summary.md", "content": "# 小结\n\n已列出目录。"}),
		llm.Say("已完成：写好了 summary.md"),
	))

	res, err := h.run(p, func(c *Config) {
		c.Approve = func(string, map[string]any) bool { return true }
		c.CheckpointBeforeWrite = true
	}, "看看这个目录有什么，并写一份 summary.md")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	if res.Steps != 3 || res.ToolCalls != 2 {
		t.Fatalf("步数/工具调用数不对：%+v", res)
	}
	if !strings.Contains(res.Text, "summary.md") {
		t.Fatalf("结论不对：%q", res.Text)
	}
	if _, err := os.Stat(filepath.Join(h.ws.Root(), "summary.md")); err != nil {
		t.Fatalf("工具应真的写出了文件：%v", err)
	}
	if res.MemoryChunks == 0 {
		t.Fatal("运行结论应写进长期记忆")
	}
	hits, err := h.mem.Search("summary 目录", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("记忆里应能检索到这次任务：%v %+v", err, hits)
	}
	if h.fired[hooks.EventRunStart] != 1 || h.fired[hooks.EventRunFinish] != 1 {
		t.Fatalf("运行钩子没触发：%+v", h.fired)
	}
	if h.fired[hooks.EventToolAfter] != 2 || h.fired[hooks.EventMemoryWrite] != 1 {
		t.Fatalf("工具/记忆钩子没触发：%+v", h.fired)
	}
	if h.fired[hooks.EventCheckpoint] != 1 || len(res.Checkpoints) != 1 {
		t.Fatalf("写文件前应打快照：%+v", res.Checkpoints)
	}

	// 运行记录与对话消息都要落库
	rec, ok, err := h.store.Run(res.RunID)
	if err != nil || !ok {
		t.Fatalf("运行记录没落库：%v %v", ok, err)
	}
	if rec.Status != "done" || rec.ToolCalls != 2 || rec.SessionID != "test-session" {
		t.Fatalf("运行记录内容不对：%+v", rec)
	}
	msgs, err := h.store.Messages(res.RunID)
	if err != nil || len(msgs) < 5 {
		t.Fatalf("对话消息应落库：%d %v", len(msgs), err)
	}
	roles := map[string]int{}
	for _, m := range msgs {
		roles[m.Role]++
	}
	if roles[llm.RoleTool] != 2 || roles[llm.RoleAssistant] != 3 {
		t.Fatalf("对话角色不完整：%+v", roles)
	}
}

// 提示词里要带上工具清单，并把上层给的 SystemExtra（含记忆召回结果）原样透传
func TestSystemPromptContainsToolsAndExtra(t *testing.T) {
	h := newHarness(t, false)
	p := llm.NewFake("fake", llm.Script(llm.Say("好")))
	extra := "【相关记忆】（系统自动召回，供参考；与当前任务无关就忽略）\n- [2026-10-01｜decision｜0.72] 项目约定：所有文件只能写在工作目录内。"
	if _, err := h.run(p, func(c *Config) { c.SystemExtra = extra }, "文件应该写在哪里？"); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	calls := p.Calls()
	if len(calls) != 1 {
		t.Fatalf("应只调用一次模型：%d", len(calls))
	}
	sys := calls[0].System
	for _, want := range []string{"白泽", "工作目录", "fs_write", "相关记忆", "项目约定"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("系统提示缺少 %q：\n%s", want, sys)
		}
	}
	if len(calls[0].Tools) == 0 {
		t.Fatal("应把工具说明一起发给模型")
	}
}

// 重试：主通道连续失败后成功
func TestRetryThenSucceed(t *testing.T) {
	h := newHarness(t, false)
	attempts := 0
	p := llm.NewFake("flaky", func(context.Context, int, llm.Request) (llm.Response, error) {
		attempts++
		if attempts < 3 {
			return llm.Response{}, os.ErrDeadlineExceeded
		}
		return llm.Say("终于成功"), nil
	})
	res, err := h.run(p, func(c *Config) { c.MaxRetries = 3 }, "随便做点什么")
	if err != nil {
		t.Fatalf("应重试后成功：%v", err)
	}
	if res.Retries != 2 || !strings.Contains(res.Text, "成功") {
		t.Fatalf("重试次数不对：%+v", res)
	}
	if h.fired[hooks.EventLLMRetry] != 2 {
		t.Fatalf("重试钩子应触发 2 次：%+v", h.fired)
	}
}

// 回退：主通道彻底挂了也能靠回退通道跑完
func TestFallbackProvider(t *testing.T) {
	h := newHarness(t, false)
	broken := llm.NewFake("broken", func(context.Context, int, llm.Request) (llm.Response, error) {
		return llm.Response{}, os.ErrInvalid
	})
	backup := llm.NewFake("backup", llm.Script(llm.Say("回退通道顶上")))
	res, err := h.run(broken, func(c *Config) {
		c.Fallbacks = []llm.Provider{backup}
		c.MaxRetries = 0
	}, "干活")
	if err != nil {
		t.Fatalf("应靠回退通道完成：%v", err)
	}
	if !strings.Contains(res.Text, "回退通道") {
		t.Fatalf("结果应来自回退通道：%+v", res)
	}
}

// 工具报错要喂回模型，而不是把整次运行判死
func TestToolErrorFedBackToModel(t *testing.T) {
	h := newHarness(t, false)
	p := llm.NewFake("fake", llm.Script(
		llm.CallTool("c1", "fs_read", map[string]any{"path": "没有这个文件.txt"}),
		llm.Say("读不到，改看目录列表。"),
	))
	res, err := h.run(p, nil, "读一下那个文件")
	if err != nil {
		t.Fatalf("工具报错不应让整次运行失败：%v", err)
	}
	if len(res.Trace) != 1 || res.Trace[0].Error == "" {
		t.Fatalf("轨迹里应记录工具错误：%+v", res.Trace)
	}
	if len(res.Errors) == 0 {
		t.Fatal("错误应被收集起来")
	}
	// 第二次请求里必须能看到工具错误内容
	calls := p.Calls()
	if len(calls) != 2 {
		t.Fatalf("应调用模型两次：%d", len(calls))
	}
	last := calls[1].Messages[len(calls[1].Messages)-1]
	if last.Role != llm.RoleTool || !strings.Contains(last.Content, "工具执行失败") {
		t.Fatalf("工具错误应作为 tool 消息喂回：%+v", last)
	}
}

// 危险工具：没有审批通道 → 拒绝；审批返回 false → 拒绝；true → 执行
func TestDangerousToolApprovalGate(t *testing.T) {
	h := newHarness(t, false)
	h.reg.Register(tools.NewShellRun(h.ws, true))

	script := func() llm.Provider {
		return llm.NewFake("fake", llm.Script(
			llm.CallTool("c1", "shell_run", map[string]any{"cmd": "echo should-not-run"}),
			llm.Say("算了"),
		))
	}

	// 1) 没有审批通道
	res, err := h.run(script(), nil, "执行命令")
	if err != nil {
		t.Fatalf("运行不应整体失败：%v", err)
	}
	if res.Trace[0].Error == "" || !strings.Contains(res.Trace[0].Error, "未获人工批准") {
		t.Fatalf("应被审批闸门拦下：%+v", res.Trace[0])
	}

	// 2) 审批返回 false
	h.fired[hooks.EventToolBlocked] = 0 // case 1（无审批通道）也拦了一次，这里从零数
	res, err = h.run(script(), func(c *Config) {
		c.Approve = func(string, map[string]any) bool { return false }
	}, "执行命令")
	if err != nil {
		t.Fatalf("运行不应整体失败：%v", err)
	}
	if !strings.Contains(res.Trace[0].Error, "未获人工批准") {
		t.Fatalf("应被拒绝：%+v", res.Trace[0])
	}
	if h.fired[hooks.EventToolBlocked] != 1 {
		t.Fatalf("拦下事件应触发：%+v", h.fired)
	}

	// 3) 审批通过 → 真的执行
	res, err = h.run(script(), func(c *Config) {
		c.Approve = func(string, map[string]any) bool { return true }
	}, "执行命令")
	if err != nil {
		t.Fatalf("审批通过后应能执行：%v", err)
	}
	if res.Trace[0].Error != "" || !strings.Contains(res.Trace[0].Result, "should-not-run") {
		t.Fatalf("命令应真的执行：%+v", res.Trace[0])
	}
}

// 审批松紧度「严」：连 fs_list 这种安全工具也要人批；「中」（默认）则不过闸门
func TestApproveAllToolsStrictness(t *testing.T) {
	h := newHarness(t, false)
	seen := []string{}
	// 每次运行都要一份全新的剧本：假通道有状态，复用同一份会在第二次运行时就"没词"了
	script := func() llm.Provider {
		return llm.NewFake("fake", llm.Script(
			llm.CallTool("c1", "fs_list", map[string]any{"path": "."}),
			llm.Say("看完了"),
		))
	}

	// 严：安全工具也被闸门拦下
	res, err := h.run(script(), func(c *Config) {
		c.Approve = func(tool string, _ map[string]any) bool {
			seen = append(seen, tool)
			return false
		}
		c.ApproveAllTools = true
	}, "看看目录")
	if err != nil {
		t.Fatalf("运行不应整体失败：%v", err)
	}
	if len(seen) != 1 || seen[0] != "fs_list" {
		t.Fatalf("严档下 fs_list 也该过闸门，实际回调了 %v", seen)
	}
	if len(res.Trace) != 1 || !strings.Contains(res.Trace[0].Error, "未获人工批准") {
		t.Fatalf("严档应拦下安全工具：%+v", res.Trace)
	}

	// 中（默认）：同样的调用不进闸门，直接执行
	seen = seen[:0]
	res, err = h.run(script(), func(c *Config) {
		c.Approve = func(tool string, _ map[string]any) bool {
			seen = append(seen, tool)
			return true
		}
	}, "看看目录")
	if err != nil {
		t.Fatalf("运行不应整体失败：%v", err)
	}
	if len(seen) != 0 {
		t.Fatalf("中档下安全工具不该进闸门，实际回调了 %v", seen)
	}
	if len(res.Trace) != 1 || res.Trace[0].Error != "" {
		t.Fatalf("中档下 fs_list 应直接执行：%+v", res.Trace)
	}
}

// 变更前快照 + 回滚
func TestCheckpointAndRollback(t *testing.T) {
	h := newHarness(t, true)
	target := filepath.Join(h.ws.Root(), "important.txt")
	if err := os.WriteFile(target, []byte("原始内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := llm.NewFake("fake", llm.Script(
		llm.CallTool("c1", "fs_write", map[string]any{"path": "important.txt", "content": "改坏了"}),
		llm.Say("改完了"),
	))
	res, err := h.run(p, func(c *Config) {
		c.Approve = func(string, map[string]any) bool { return true }
		c.CheckpointBeforeWrite = true
	}, "改一下 important.txt")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "改坏了" {
		t.Fatalf("文件应已被覆盖：%q", string(raw))
	}
	if len(res.Checkpoints) != 1 {
		t.Fatalf("应有 1 个快照：%+v", res.Checkpoints)
	}
	if err := h.ck.Rollback(res.Checkpoints[0]); err != nil {
		t.Fatalf("回滚失败：%v", err)
	}
	raw, _ = os.ReadFile(target)
	if string(raw) != "原始内容" {
		t.Fatalf("回滚后应恢复原内容：%q", string(raw))
	}
	list, err := h.ck.List()
	if err != nil || len(list) != 1 || list[0].Files == 0 {
		t.Fatalf("快照列表不对：%+v %v", list, err)
	}
}

// 超过最大步数要停下并如实报错
func TestMaxStepsStopsRun(t *testing.T) {
	h := newHarness(t, false)
	looping := llm.NewFake("looping", func(context.Context, int, llm.Request) (llm.Response, error) {
		return llm.CallTool("c", "fs_list", map[string]any{"path": "."}), nil
	})
	res, err := h.run(looping, func(c *Config) { c.MaxSteps = 3 }, "无限循环")
	if err == nil {
		t.Fatal("超过最大步数应报错")
	}
	if !strings.Contains(err.Error(), "最大步数") {
		t.Fatalf("错误信息应说明原因：%v", err)
	}
	if res.Steps != 3 {
		t.Fatalf("应在第 3 步停下：%d", res.Steps)
	}
	rec, ok, _ := h.store.Run(res.RunID)
	if !ok || rec.Status != "failed" {
		t.Fatalf("运行记录应标为 failed：%+v", rec)
	}
	if rec.Err == "" {
		t.Fatal("失败原因应落库")
	}
}

// 上下文压缩：超预算时把较早的对话"滚出窗口"（原文不销毁，摘要单独交回）
func TestCompressorShrinksContext(t *testing.T) {
	c := NewCompressor(50, nil, nil)
	long := strings.Repeat("这是一段很长的历史消息。", 30)
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: long},
		{Role: llm.RoleAssistant, Content: long},
		{Role: llm.RoleUser, Content: long},
		{Role: llm.RoleAssistant, Content: long},
		{Role: llm.RoleUser, Content: "最近的问题"},
		{Role: llm.RoleAssistant, Content: "最近的回答"},
	}
	cr, err := c.Compress(context.Background(), msgs, "系统提示")
	if err != nil {
		t.Fatalf("压缩失败：%v", err)
	}
	if !cr.Changed {
		t.Fatal("超预算时应发生压缩")
	}
	if len(cr.Messages) >= len(msgs) {
		t.Fatalf("压缩后消息数应减少：%d → %d", len(msgs), len(cr.Messages))
	}
	if cr.Scrolled <= 0 || cr.Tokens <= 0 {
		t.Fatalf("应报出滚出的条数与 token：%+v", cr)
	}
	if strings.TrimSpace(cr.Summary) == "" {
		t.Fatal("应给出被滚出部分的摘要")
	}
	// 关键：摘要不再挤进消息列表（原文靠 messages 表 + context_recall 回放）
	// 切点两侧的消息数应当减少，且最近的消息必须原样保留
	if cr.Messages[len(cr.Messages)-1].Content != "最近的回答" {
		t.Fatal("最近的消息应原样保留")
	}

	// 预算充足时不动
	short := []llm.Message{{Role: llm.RoleUser, Content: "短"}}
	if cr2, _ := c.Compress(context.Background(), short, "系统"); cr2.Changed || len(cr2.Messages) != 1 {
		t.Fatal("预算充足时不该改上下文")
	}
}

func TestRunRejectsEmptyGoalAndMissingProvider(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.run(llm.NewFake("f", llm.Script(llm.Say("x"))), nil, "   "); err == nil {
		t.Fatal("空目标应报错")
	}
	r := New(Config{Tools: h.reg, Store: h.store, Logger: slog.Default()})
	if _, err := r.Run(context.Background(), "有目标但没模型"); err == nil {
		t.Fatal("没有模型通道应明确报错")
	}
}

// 运行记录与消息能按 JSON 序列化（供控制台接口用）
func TestRunRecordJSON(t *testing.T) {
	h := newHarness(t, false)
	p := llm.NewFake("fake", llm.Script(llm.Say("ok")))
	res, err := h.run(p, nil, "随便")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	rec, ok, err := h.store.Run(res.RunID)
	if err != nil || !ok {
		t.Fatalf("读运行记录失败：%v", err)
	}
	raw, err := json.Marshal(rec)
	if err != nil || !strings.Contains(string(raw), res.RunID) {
		t.Fatalf("运行记录 JSON 不对：%v %s", err, string(raw))
	}
	_ = time.Now
}

// 工具结果喂给模型时不能被 brief 的 300 字砍断：真机联调时手机 todo.list 的回执正好被
// 砍在 title 之前，模型连试三次都拿不到标题，最后只能报「任务未完成」。
func TestToolOutputKeepsKeyFieldsVisible(t *testing.T) {
	// 字段顺序刻意把 Title 放最后、前面塞长 note：用 300 字上限一定会砍掉标题
	type todo struct {
		Auth     string `json:"auth"`
		Category string `json:"category"`
		Due      string `json:"due"`
		ID       string `json:"id"`
		Owner    string `json:"owner"`
		Status   string `json:"status"`
		Note     string `json:"note"`
		Title    string `json:"title"`
	}
	payload := map[string]any{
		"count": 1, "matched": 6, "truncated": false,
		"todos": []todo{{
			Category: "日常生活", Due: "2026-09-25T14:00", ID: "mugn9d106g5ufq",
			Owner: "user", Status: "todo", Note: strings.Repeat("备", 400), Title: "下午2点交周报",
		}},
	}

	out := toolOutput(payload)
	if !strings.Contains(out, "下午2点交周报") {
		t.Fatalf("工具结果里的关键字段必须完整送到模型，却被截掉了：%s", out)
	}
	if strings.Contains(out, "已截断") {
		t.Fatalf("上限内的结果不该被截断：%s", out)
	}
	// brief 仍然保持「给人看的短版」语义，别顺手把它也放大
	if got := brief(payload); len([]rune(got)) > 301 {
		t.Fatalf("brief 应该还是 300 字短版，实际 %d 字", len([]rune(got)))
	}

	// 真超长时必须写明「截断了 + 怎么缩小范围」，否则模型只会一味重试同一个调用
	long := toolOutput(strings.Repeat("字", maxToolOutput*2))
	if !strings.Contains(long, "已截断") || !strings.Contains(long, "缩小范围") {
		t.Fatalf("超长结果必须带截断提示与缩小范围的建议：%s", long[len(long)-160:])
	}
}
