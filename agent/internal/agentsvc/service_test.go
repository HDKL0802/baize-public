package agentsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"baize/internal/config"
	"baize/internal/memory"
)

/* ---------- 一个 OpenAI 兼容协议的假模型服务 ---------- */

type step func(req map[string]any) map[string]any

type fakeLLM struct {
	t      *testing.T
	mu     sync.Mutex
	step   int
	steps  []step
	bodies []map[string]any
	srv    *httptest.Server
}

func newFakeLLM(t *testing.T, steps ...step) *fakeLLM {
	f := &fakeLLM{t: t, steps: steps}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		idx := f.step
		f.step++
		f.mu.Unlock()
		if len(f.steps) == 0 {
			writeChat(w, sayBody("（没有脚本了）"))
			return
		}
		if idx >= len(f.steps) {
			idx = len(f.steps) - 1
		}
		writeChat(w, f.steps[idx](body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeChat(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func sayBody(text string) map[string]any {
	return map[string]any{
		"model": "fake-model",
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": text},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	}
}

func toolBody(id, name string, args map[string]any) map[string]any {
	raw, _ := json.Marshal(args)
	return map[string]any{
		"model": "fake-model",
		"choices": []map[string]any{{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": string(raw)},
				}},
			},
		}},
		"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
	}
}

/* ---------- 服务级用例 ---------- */

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newService 建一个已配好假模型通道的服务
func newService(t *testing.T, f *fakeLLM, tweak func(*config.Config)) *Service {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	t.Cleanup(s.Close)

	cfg := s.Config()
	if f != nil {
		cfg.Providers = []config.Provider{{
			Name: "fake", Protocol: "openai", BaseURL: f.srv.URL + "/v1", Model: "fake-model", TimeoutSec: 30,
		}}
	}
	cfg.ApprovalTimeoutSec = 5
	if tweak != nil {
		tweak(&cfg)
	}
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	return s
}

// 打通「后端服务 → 真实 HTTP 模型通道 → 工具链 → 记忆落盘」
func TestServiceRunEndToEnd(t *testing.T) {
	f := newFakeLLM(t,
		func(map[string]any) map[string]any {
			return toolBody("c1", "fs_write", map[string]any{"path": "report.md", "content": "# 报告\n\n内容"})
		},
		func(map[string]any) map[string]any { return sayBody("已写好 report.md") },
	)
	s := newService(t, f, nil)

	res, err := s.Run(context.Background(), "写一份 report.md", "chat", "loose")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	if !strings.Contains(res.Text, "report.md") || res.ToolCalls != 1 {
		t.Fatalf("运行结果不对：%+v", res)
	}
	if _, err := os.Stat(filepath.Join(s.Workdir(), "report.md")); err != nil {
		t.Fatalf("工具应真的写出文件：%v", err)
	}
	if res.MemoryChunks == 0 {
		t.Fatal("应写入记忆")
	}

	// 状态接口要能看到运行记录、通道、记忆统计
	st := s.State()
	if len(st.Providers) != 1 || !st.Providers[0].Usable {
		t.Fatalf("通道状态不对：%+v", st.Providers)
	}
	if len(st.Runs) == 0 || st.Runs[0].RunID != res.RunID || st.Runs[0].Status != "done" {
		t.Fatalf("运行记录不对：%+v", st.Runs)
	}
	if st.Memory.Chunks == 0 || st.LastRun == nil {
		t.Fatalf("记忆/最近运行不对：%+v", st)
	}
	if st.Issue != "" {
		t.Fatalf("有可用通道时不该报配置问题：%s", st.Issue)
	}
	// 轨迹要落到运行记录里（控制台要看）
	detail, err := s.RunDetail(res.RunID)
	if err != nil {
		t.Fatalf("取运行详情失败：%v", err)
	}
	if len(detail.Messages) < 3 {
		t.Fatalf("对话消息应落库：%d", len(detail.Messages))
	}
	if raw, ok := detail.Run.Meta["trace"]; !ok || raw == nil {
		t.Fatalf("轨迹应写进运行记录：%+v", detail.Run.Meta)
	}

	// 假模型收到的请求里应该带上了工具说明与系统提示
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) < 1 {
		t.Fatal("假模型没收到请求")
	}
	msgs, _ := f.bodies[0]["messages"].([]any)
	joined, _ := json.Marshal(msgs)
	if !strings.Contains(string(joined), "白泽") || !strings.Contains(string(joined), "fs_write") {
		t.Fatalf("系统提示/工具说明没发过去：%s", string(joined))
	}
}

// 危险操作必须等人工审批；拒绝后不放行
func TestApprovalGateBlocksAndRejects(t *testing.T) {
	f := newFakeLLM(t,
		func(map[string]any) map[string]any {
			return toolBody("c1", "shell_run", map[string]any{"cmd": "echo 不该执行"})
		},
		func(map[string]any) map[string]any { return sayBody("命令被拒，改别的方案") },
	)
	s := newService(t, f, func(c *config.Config) { c.AllowShell = true; c.ApprovalTimeoutSec = 10 })

	type out struct {
		res interface{}
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := s.Run(context.Background(), "执行一条命令", "chat", "")
		done <- out{res: res, err: err}
	}()

	// 等到审批出现在队列里
	var ap Approval
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list := s.Approvals()
		if len(list) > 0 && list[0].Status == ApprovalPending {
			ap = list[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ap.ID == "" {
		t.Fatal("危险操作应进入审批队列")
	}
	if ap.Tool != "shell_run" {
		t.Fatalf("审批内容不对：%+v", ap)
	}
	if err := s.Reject(ap.ID, "tester", "不许执行"); err != nil {
		t.Fatalf("拒绝失败：%v", err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("被拒后运行应平稳结束：%v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("拒绝后运行没有及时结束")
	}

	list := s.Approvals()
	if len(list) == 0 || list[0].Status != ApprovalRejected || list[0].By != "tester" {
		t.Fatalf("审批状态不对：%+v", list)
	}
	// 命令绝不能真的执行过：工作目录里不应留下任何命令痕迹
	if _, err := os.Stat(filepath.Join(s.Workdir(), "不该执行")); err == nil {
		t.Fatal("被拒绝的命令不该产生副作用")
	}
	// 审批通过的情况
	f2 := newFakeLLM(t,
		func(map[string]any) map[string]any {
			return toolBody("c1", "fs_delete", map[string]any{"path": "x.txt"})
		},
		func(map[string]any) map[string]any { return sayBody("好") },
	)
	s2 := newService(t, f2, func(c *config.Config) { c.ApprovalTimeoutSec = 10 })
	go func() {
		_, _ = s2.Run(context.Background(), "删掉 x.txt", "chat", "")
	}()
	deadline = time.Now().Add(5 * time.Second)
	var ap2 Approval
	for time.Now().Before(deadline) {
		list := s2.Approvals()
		if len(list) > 0 && list[0].Status == ApprovalPending {
			ap2 = list[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ap2.ID == "" {
		t.Fatal("第二次审批没出现")
	}
	if err := s2.Approve(ap2.ID, "tester"); err != nil {
		t.Fatalf("批准失败：%v", err)
	}
	if err := s2.Approve(ap2.ID, "tester"); err == nil {
		t.Fatal("重复处理同一条审批应报错")
	}
}

// 超时未审批 → 按拒绝处理
func TestApprovalTimeoutRejects(t *testing.T) {
	f := newFakeLLM(t,
		func(map[string]any) map[string]any {
			return toolBody("c1", "fs_delete", map[string]any{"path": "y.txt"})
		},
		func(map[string]any) map[string]any { return sayBody("没批准，跳过") },
	)
	s := newService(t, f, func(c *config.Config) { c.ApprovalTimeoutSec = 1 })
	res, err := s.Run(context.Background(), "删掉 y.txt", "chat", "")
	if err != nil {
		t.Fatalf("超时被拒后运行应平稳结束：%v", err)
	}
	if len(res.Trace) == 0 || !strings.Contains(res.Trace[0].Error, "审批闸门") {
		t.Fatalf("轨迹应记录被闸门拦下：%+v", res.Trace)
	}
	list := s.Approvals()
	if len(list) == 0 || list[0].Status != ApprovalTimeout {
		t.Fatalf("审批应标记为超时：%+v", list)
	}
}

// 没有模型 / 远端地址被拦 / 配置问题都要明确报出来
func TestProviderConfigurationErrors(t *testing.T) {
	// 1) 完全没有通道
	s := newService(t, nil, nil)
	if _, err := s.Run(context.Background(), "随便", "chat", "loose"); err == nil ||
		!strings.Contains(err.Error(), "没有可用的模型通道") {
		t.Fatalf("没有通道时应明确报错，实际：%v", err)
	}
	if st := s.State(); st.Issue == "" || !strings.Contains(st.Issue, "还没有配置") {
		t.Fatalf("状态里应提示配置问题：%+v", st.Issue)
	}

	// 2) 远端地址（默认只接本地）
	s2 := newService(t, nil, func(c *config.Config) {
		c.Providers = []config.Provider{{Name: "cloud", Protocol: "openai", BaseURL: "https://api.example.com/v1", Model: "m"}}
	})
	st := s2.State()
	if len(st.Providers) != 1 || st.Providers[0].Usable {
		t.Fatalf("远端地址默认不该可用：%+v", st.Providers)
	}
	if !strings.Contains(st.Providers[0].Err, "本地") {
		t.Fatalf("应说明只接本地模型：%+v", st.Providers[0])
	}
	if _, err := s2.Run(context.Background(), "随便", "chat", "loose"); err == nil {
		t.Fatal("不可用通道时应报错")
	}

	// 3) 允许远端后就可用
	s3 := newService(t, nil, func(c *config.Config) {
		c.AllowRemote = true
		c.Providers = []config.Provider{{Name: "cloud", Protocol: "openai", BaseURL: "https://api.example.com/v1", Model: "m"}}
	})
	st3 := s3.State()
	if len(st3.Providers) != 1 || !st3.Providers[0].Usable {
		t.Fatalf("显式允许远端后应可用：%+v", st3.Providers)
	}
	if !st3.Providers[0].HasAPIKey == true { // 没配 key 也应可用（很多本地中转不需要）
		t.Log("无 apiKey")
	}
}

// 定时任务到点要真的触发一次运行，并把结果写回配置
func TestCronJobFires(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("定时任务完成") })
	s := newService(t, f, func(c *config.Config) {
		c.Cron = []config.CronJob{{
			ID: "job1", Expr: "@every 1m", Goal: "巡检一下", Recipe: "chat", Enabled: true,
		}}
	})

	// 起跑前状态里应能看到任务与下一次触发时间
	st := s.State()
	if len(st.Cron) != 1 || st.Cron[0].ID != "job1" {
		t.Fatalf("状态里应有定时任务：%+v", st.Cron)
	}

	// 把"下一次触发时间"拨到过去，手动 tick 一次
	s.sched.mu.Lock()
	s.sched.next["job1"] = time.Now().Add(-time.Second)
	s.sched.mu.Unlock()
	s.sched.tick()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cfg := s.Config()
		if len(cfg.Cron) == 1 && cfg.Cron[0].Runs > 0 {
			if cfg.Cron[0].LastStatus != "done" {
				t.Fatalf("任务状态应为 done：%+v", cfg.Cron[0])
			}
			if cfg.Cron[0].LastRunAt == 0 {
				t.Fatal("应记录最后运行时间")
			}
			// 结果也要留在运行记录里
			runs, _ := s.Runs(5)
			if len(runs) == 0 || runs[0].Goal != "巡检一下" {
				t.Fatalf("定时任务的运行记录不对：%+v", runs)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("定时任务没有触发")
}

// 技能清单要进系统提示，skill_load 能取全文
func TestSkillsInjectedIntoPrompt(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "triage")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: 分诊\ndescription: 把用户的话分派到合适的技能\n---\n\n正文内容-秘密细节\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, testLogger())
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()
	cfg := s.Config()
	cfg.Providers = []config.Provider{{Name: "fake", Protocol: "openai", BaseURL: f.srv.URL + "/v1", Model: "m"}}
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "随便", "chat", "loose"); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	joined, _ := json.Marshal(f.bodies[0]["messages"])
	if !strings.Contains(string(joined), "可用技能") || !strings.Contains(string(joined), "分诊") {
		t.Fatalf("系统提示里应带上技能清单：%s", string(joined))
	}
	if strings.Contains(string(joined), "秘密细节") {
		t.Fatal("渐进式披露：正文不该出现在系统提示里")
	}
	st := s.State()
	if len(st.Skills) != 1 || st.Skills[0].Name != "分诊" {
		t.Fatalf("状态里应有技能：%+v", st.Skills)
	}
	_ = dir
}

// Agent 自己把做法沉淀成技能（skill_manage 免审批）：同一轮里就要能在技能清单看到它，
// 并且能马上用 skill_load 取回全文（技能库是原地刷新的，不靠重启）
func TestSkillManageCreatesSkillForAgent(t *testing.T) {
	f := newFakeLLM(t,
		func(map[string]any) map[string]any {
			return toolBody("c1", "skill_manage", map[string]any{
				"action": "create", "name": "weekly-report",
				"content": "---\nname: 写周报\ndescription: 把本周待办汇总成周报时用。\n---\n\n1. 取待办\n2. 汇总成表格\n",
			})
		},
		func(map[string]any) map[string]any {
			return toolBody("c2", "skill_load", map[string]any{"name": "weekly-report"})
		},
		func(map[string]any) map[string]any { return sayBody("技能已沉淀好") },
	)
	s := newService(t, f, nil)
	res, err := s.Run(context.Background(), "把写周报的做法记成技能", "chat", "")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	st := s.State()
	found := false
	for _, sk := range st.Skills {
		if sk.Name == "写周报" {
			found = true
		}
	}
	if !found {
		t.Fatalf("新建的技能应立刻出现在技能清单里：%+v", st.Skills)
	}
	if _, err := os.Stat(filepath.Join(st.DataDir, "skills", "weekly-report", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 没落盘：%v", err)
	}
	// 第二次工具调用的结果里应当带回正文（证明 skill_load 立刻认得这个新技能）
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) < 3 {
		t.Fatalf("应有 3 次模型调用，实际 %d", len(f.bodies))
	}
	names := toolNamesOf(f.bodies[0])
	for _, want := range []string{"skill_manage", "skill_delete", "skill_load"} {
		if !containsStr(names, want) {
			t.Fatalf("模型工具表里应有 %s（实际：%s）", want, strings.Join(names, ","))
		}
	}
	joined, _ := json.Marshal(f.bodies[2]["messages"])
	if !strings.Contains(string(joined), "汇总成表格") {
		t.Fatalf("skill_load 应该能取回刚建好的技能正文：%s", string(joined))
	}
	if res.Text == "" {
		t.Fatal("应有最终回复")
	}
}

// 浏览器工具：默认启用时出现在工具表里；关掉就不在（免得模型白调一个必然失败的家伙）
func TestBrowserToolRegistered(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, nil)

	if _, err := s.Run(context.Background(), "随便说点什么", "chat", ""); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	if len(f.bodies) == 0 {
		f.mu.Unlock()
		t.Fatal("模型没被调用")
	}
	names := toolNamesOf(f.bodies[0])
	f.mu.Unlock()
	if !containsStr(names, "browser") {
		t.Fatalf("默认应注册 browser 工具（实际：%s）", strings.Join(names, ","))
	}

	// 关掉之后不该再注册
	cfg := s.Config()
	cfg.Browser.Enabled = false
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	if _, err := s.Run(context.Background(), "再随便说点什么", "chat", ""); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	last := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()
	if containsStr(toolNamesOf(last), "browser") {
		t.Fatal("关掉后不该再注册 browser 工具")
	}
}

// toolNamesOf 从请求体里抠出下发给模型的工具名
func toolNamesOf(body map[string]any) []string {
	raw, _ := body["tools"].([]any)
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := m["function"].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, fmt.Sprint(fn["name"]))
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// 主动记忆召回：派活前自动把相关记忆塞进系统提示；记忆工具（含删除）要在工具表里
func TestAutoRecallInjectsMemoryAndTools(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, nil)
	if _, err := s.mem.Ingest("白泽后端部署在 NAS 上，用 systemd 守护，端口 8787。",
		memory.IngestOptions{Title: "部署方式", Kind: "decision"}); err != nil {
		t.Fatalf("写记忆失败：%v", err)
	}
	if _, err := s.Run(context.Background(), "白泽后端部署在哪里", "chat", ""); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("模型没被调用")
	}
	names := toolNamesOf(f.bodies[0])
	for _, want := range []string{"memory", "memory_forget"} {
		if !containsStr(names, want) {
			t.Fatalf("模型工具表里应有 %s（实际：%s）", want, strings.Join(names, ","))
		}
	}
	joined, _ := json.Marshal(f.bodies[0]["messages"])
	if !strings.Contains(string(joined), "相关记忆") || !strings.Contains(string(joined), "systemd 守护") {
		t.Fatalf("系统提示里应带上自动召回的记忆：%s", string(joined))
	}
}

// 关掉自动召回就不该往提示词里塞记忆（省 token，也避免旧账干扰）
func TestAutoRecallCanBeTurnedOff(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, func(c *config.Config) { c.Memory.AutoRecall = false })
	if _, err := s.mem.Ingest("白泽后端部署在 NAS 上，用 systemd 守护。",
		memory.IngestOptions{Title: "部署方式"}); err != nil {
		t.Fatalf("写记忆失败：%v", err)
	}
	if _, err := s.Run(context.Background(), "白泽后端部署在哪里", "chat", ""); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	joined, _ := json.Marshal(f.bodies[0]["messages"])
	if strings.Contains(string(joined), "systemd 守护") {
		t.Fatalf("关掉自动召回后不该注入记忆：%s", string(joined))
	}
}

// 没配 embedding 通道时，状态里必须明说语义检索没生效（不能让人以为它在工作）
func TestEmbeddingStateIsHonest(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, nil)
	st := s.State()
	if st.Embedding.Usable {
		t.Fatal("没配通道却报可用")
	}
	if st.Embedding.Note == "" {
		t.Fatal("没配通道必须明确说明语义检索未启用")
	}
	if st.MemoryCfg.Namespace == "" || st.MemoryCfg.RecallProfile == "" {
		t.Fatalf("记忆配置应有默认值：%+v", st.MemoryCfg)
	}

	// 配了但地址不合法（非本机 + allowRemote=false）→ 要能说清原因，而不是静默失败
	if err := s.SaveConfig(func() config.Config {
		c := s.Config()
		c.Embedding = config.Embedding{BaseURL: "https://api.example.com/v1", Model: "emb", APIKey: "k"}
		return c
	}()); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}
	st2 := s.State()
	if st2.Embedding.Usable || st2.Embedding.Err == "" {
		t.Fatalf("非法地址应报出原因：%+v", st2.Embedding)
	}
}

/* ---------- 异步派发与运行跟踪 ---------- */

// Start 必须立刻给出运行 id，控制台靠它跟踪；顶层串行，同时只跑一个
func TestStartReturnsRunIDAndSerializes(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any {
		time.Sleep(400 * time.Millisecond)
		return sayBody("做完了")
	})
	s := newService(t, f, nil)

	id, err := s.Start("随便做点什么", "chat", "")
	if err != nil {
		t.Fatalf("派发失败：%v", err)
	}
	if id == "" {
		t.Fatal("派发应返回运行 id")
	}
	if got := s.CurrentRunID(); got != id {
		t.Fatalf("状态里应能看到当前运行 id（期望 %s，实际 %s）", id, got)
	}
	if _, err := s.Start("再来一个", "chat", ""); err == nil {
		t.Fatal("顶层串行：已有任务在跑时应明确拒绝，而不是排队干等")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !s.State().Running {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := s.State()
	if st.Running || st.CurrentRunID != "" {
		t.Fatalf("跑完应清空运行状态：running=%v id=%s", st.Running, st.CurrentRunID)
	}
	runs, err := s.Runs(1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("应有 1 条运行记录：%v %+v", err, runs)
	}
	if runs[0].RunID != id {
		t.Fatalf("运行记录应能按派发时给的 id 对上（期望 %s，实际 %s）", id, runs[0].RunID)
	}
	if runs[0].Status != "done" {
		t.Fatalf("运行状态不对：%+v", runs[0])
	}
}

/* ---------- 人设 ---------- */

// 人设文件要真的进系统提示（热重载：改完立即生效，不用重启）；
// 总开关关掉后同一份文件不能再生效；心跳段在心跳没启用时要从提示里消失。
func TestPersonaInjectedAndHotReload(t *testing.T) {
	f := newFakeLLM(t, func(map[string]any) map[string]any { return sayBody("好") })
	s := newService(t, f, nil)

	// 首次初始化应自动落模板
	lib := s.Persona()
	if lib == nil {
		t.Fatal("人设库没建起来")
	}
	if _, err := os.Stat(filepath.Join(lib.Dir(), "SOUL.md")); err != nil {
		t.Fatalf("首次应生成默认人设模板：%v", err)
	}

	// 改文件 → 立即生效（不重启）
	if err := lib.Write("SOUL.md", "灵魂：说话像老友，先给结论。\n"); err != nil {
		t.Fatal(err)
	}
	if err := lib.Write("AGENTS.md", "工作方式：先列清单。\n\n<!-- heartbeat:start -->\n心跳专属规则\n<!-- heartbeat:end -->\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "随便", "chat", "loose"); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	joined, _ := json.Marshal(f.bodies[len(f.bodies)-1]["messages"])
	f.mu.Unlock()
	text := string(joined)
	if !strings.Contains(text, "说话像老友，先给结论") || !strings.Contains(text, "先列清单") {
		t.Fatalf("系统提示里应带上人设文件内容：%s", text)
	}
	// 心跳没启用 → 心跳段整体不进提示
	if strings.Contains(text, "心跳专属规则") {
		t.Fatalf("心跳未启用时不该注入心跳段：%s", text)
	}

	// 总开关关掉 → 人设不再注入（身份行还在）
	cfg := s.Config()
	cfg.Persona.Enabled = false
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "随便", "chat", "loose"); err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	f.mu.Lock()
	joined2, _ := json.Marshal(f.bodies[len(f.bodies)-1]["messages"])
	f.mu.Unlock()
	if strings.Contains(string(joined2), "说话像老友，先给结论") {
		t.Fatalf("总开关关掉后不该注入人设：%s", string(joined2))
	}
	if !strings.Contains(string(joined2), "你是白泽") {
		t.Fatal("身份行不该被人设开关影响")
	}
}
