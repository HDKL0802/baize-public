package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatalf("创建工作目录失败：%v", err)
	}
	return ws
}

func writeTestFile(t *testing.T, ws *Workspace, rel, content string) {
	t.Helper()
	p := filepath.Join(ws.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}

func TestFileSearchByNameAndContent(t *testing.T) {
	ws := newTestWorkspace(t)
	writeTestFile(t, ws, "notes/白泽.md", "# 白泽\n这是一条笔记，提到 双向链接 这个词。\n第二行。")
	writeTestFile(t, ws, "notes/其他.md", "# 其他\n无关内容。")
	writeTestFile(t, ws, "code/main.go", "package main\n// 双向链接 只是注释\n")
	reg := NewRegistry()
	RegisterFileSearch(reg, ws)
	ctx := context.Background()

	// 只按内容搜
	res, err := reg.Call(ctx, "file_search", map[string]any{"query": "双向链接"})
	if err != nil {
		t.Fatalf("按内容搜索失败：%v", err)
	}
	m := res.(map[string]any)
	if m["count"].(int) != 2 {
		t.Fatalf("应命中 2 个文件，实际 %v", m["count"])
	}

	// 内容 + 文件名过滤（只看 md）
	res, err = reg.Call(ctx, "file_search", map[string]any{"query": "双向链接", "name": "*.md"})
	if err != nil {
		t.Fatalf("搜失败：%v", err)
	}
	m = res.(map[string]any)
	if m["count"].(int) != 1 {
		t.Fatalf("加 *.md 过滤后应只剩 1 个，实际 %v", m["count"])
	}

	// 只按文件名搜
	res, err = reg.Call(ctx, "file_search", map[string]any{"name": "main"})
	if err != nil {
		t.Fatalf("按文件名搜索失败：%v", err)
	}
	m = res.(map[string]any)
	if m["count"].(int) != 1 {
		t.Fatalf("按文件名应命中 1 个，实际 %v", m["count"])
	}

	// 两个参数都不给 → 明确报错
	if _, err := reg.Call(ctx, "file_search", map[string]any{}); err == nil {
		t.Fatal("既没 query 也没 name 时应报错")
	}
}

func TestFileSearchSkipsBinaryAndTooBig(t *testing.T) {
	ws := newTestWorkspace(t)
	writeTestFile(t, ws, "ok.txt", "hello 关键字 world")
	// 二进制（含 NUL）
	writeTestFile(t, ws, "bin.dat", "关键字\x00\x00更多内容")
	// 超大（超过 maxFileBytes）
	writeTestFile(t, ws, "big.txt", strings.Repeat("关键字", 2000))

	reg := NewRegistry()
	RegisterFileSearch(reg, ws)
	res, err := reg.Call(context.Background(), "file_search", map[string]any{
		"query": "关键字", "maxFileBytes": 512,
	})
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	m := res.(map[string]any)
	if m["count"].(int) != 1 {
		t.Fatalf("只应命中 ok.txt，实际 %v（%v）", m["count"], m["hits"])
	}
	if m["skippedBinary"].(int) < 1 {
		t.Fatalf("应如实报告跳过了二进制文件：%v", m)
	}
	if m["skippedTooBig"].(int) < 1 {
		t.Fatalf("应如实报告跳过了超大文件：%v", m)
	}
}

func TestFileSearchBinaryOnlyFileNotReadable(t *testing.T) {
	ws := newTestWorkspace(t)
	// 全是二进制内容：只按文件名能命中，按内容不会被当成文本匹配
	writeTestFile(t, ws, "data.bin", "\x00\x01\x02binary")
	reg := NewRegistry()
	RegisterFileSearch(reg, ws)
	res, err := reg.Call(context.Background(), "file_search", map[string]any{"name": "data.bin"})
	if err != nil {
		t.Fatalf("按文件名搜索失败：%v", err)
	}
	if res.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("二进制文件也应能按文件名命中：%v", res)
	}
}

/* ---------- run_tool_batch 的安全边界 ---------- */

func TestRunToolBatchRunsReadOnlyTools(t *testing.T) {
	ws := newTestWorkspace(t)
	writeTestFile(t, ws, "a.txt", "hi")
	reg := NewRegistry()
	RegisterFS(reg, ws)
	RegisterRunToolBatch(reg)

	res, err := reg.Call(context.Background(), "run_tool_batch", map[string]any{
		"calls": []any{
			map[string]any{"tool": "fs_list", "args": map[string]any{"path": "."}},
			map[string]any{"tool": "fs_read", "args": map[string]any{"path": "a.txt"}},
		},
	})
	if err != nil {
		t.Fatalf("批量调用失败：%v", err)
	}
	m := res.(map[string]any)
	if m["okCount"].(int) != 2 {
		t.Fatalf("两个只读调用都该成功：%v", m)
	}
}

func TestRunToolBatchBlocksDangerousAndMutating(t *testing.T) {
	ws := newTestWorkspace(t)
	writeTestFile(t, ws, "a.txt", "hi")
	reg := NewRegistry()
	RegisterFS(reg, ws) // fs_delete 是危险工具，fs_write 会改现场
	RegisterRunToolBatch(reg)

	res, err := reg.Call(context.Background(), "run_tool_batch", map[string]any{
		"calls": []any{
			map[string]any{"tool": "fs_delete", "args": map[string]any{"path": "a.txt"}},
			map[string]any{"tool": "fs_write", "args": map[string]any{"path": "b.txt", "content": "x"}},
			map[string]any{"tool": "run_tool_batch", "args": map[string]any{"calls": []any{}}},
		},
	})
	if err != nil {
		t.Fatalf("批量调用本身不该失败（应逐条回报被挡）：%v", err)
	}
	m := res.(map[string]any)
	if m["okCount"].(int) != 0 {
		t.Fatalf("危险/会改现场的都不该被执行：%v", m)
	}
	if m["blockedCount"].(int) != 3 {
		t.Fatalf("三条都该被安全策略挡下，实际 %v：%v", m["blockedCount"], m["results"])
	}
	// 文件必须还在（fs_delete 没被执行）
	if _, err := os.Stat(filepath.Join(ws.Root(), "a.txt")); err != nil {
		t.Fatalf("危险工具被挡下后文件不该被删：%v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "b.txt")); err == nil {
		t.Fatal("会改现场的工具被挡下后不该写出文件")
	}
}

func TestRunToolBatchRejectsEmptyCalls(t *testing.T) {
	reg := NewRegistry()
	RegisterRunToolBatch(reg)
	if _, err := reg.Call(context.Background(), "run_tool_batch", map[string]any{}); err == nil {
		t.Fatal("calls 为空时应报错")
	}
}

/* ---------- web_search：没配通道必须明确报错 ---------- */

func TestWebSearchUnconfigured(t *testing.T) {
	reg := NewRegistry()
	RegisterWebSearch(reg, WebSearchConfig{}) // 不配 provider
	_, err := reg.Call(context.Background(), "web_search", map[string]any{"query": "白泽"})
	if err == nil {
		t.Fatal("没配搜索通道时应报错，而不是返回空结果")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Fatalf("错误信息应指向配置项 search：%v", err)
	}
}

func TestWebSearchUnknownProvider(t *testing.T) {
	reg := NewRegistry()
	RegisterWebSearch(reg, WebSearchConfig{Provider: "baidu"})
	_, err := reg.Call(context.Background(), "web_search", map[string]any{"query": "白泽"})
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("未知通道应明确报不支持：%v", err)
	}
}

/* ---------- cron 工具 ---------- */

func TestCronToolLifecycle(t *testing.T) {
	// 用一个内存版 hooks 验证工具层的 action 分发与校验
	type job struct {
		id, expr, goal string
		enabled        bool
	}
	var jobs []job
	nextID := 0
	hooks := CronHooks{
		List: func() []CronJobInfo {
			out := []CronJobInfo{}
			for _, j := range jobs {
				out = append(out, CronJobInfo{ID: j.id, Expr: j.expr, Goal: j.goal, Enabled: j.enabled})
			}
			return out
		},
		Add: func(expr, goal, recipe string, autoApprove bool) (CronJobInfo, error) {
			if strings.TrimSpace(expr) == "" || strings.TrimSpace(goal) == "" {
				return CronJobInfo{}, errTest("需要 expr 与 goal")
			}
			nextID++
			j := job{id: "j" + string(rune('0'+nextID)), expr: expr, goal: goal, enabled: true}
			jobs = append(jobs, j)
			return CronJobInfo{ID: j.id, Expr: j.expr, Goal: j.goal, Enabled: true}, nil
		},
		Update: func(id, expr, goal, recipe string, enabled *bool) (CronJobInfo, error) {
			for i := range jobs {
				if jobs[i].id != id {
					continue
				}
				if expr != "" {
					jobs[i].expr = expr
				}
				if goal != "" {
					jobs[i].goal = goal
				}
				if enabled != nil {
					jobs[i].enabled = *enabled
				}
				return CronJobInfo{ID: jobs[i].id, Expr: jobs[i].expr, Goal: jobs[i].goal, Enabled: jobs[i].enabled}, nil
			}
			return CronJobInfo{}, errTest("没有这个定时任务：" + id)
		},
		Remove: func(id string) error {
			for i := range jobs {
				if jobs[i].id == id {
					jobs = append(jobs[:i], jobs[i+1:]...)
					return nil
				}
			}
			return errTest("没有这个定时任务：" + id)
		},
	}
	reg := NewRegistry()
	RegisterCron(reg, hooks)
	ctx := context.Background()

	// 空列表
	res, err := reg.Call(ctx, "cron", map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list 失败：%v", err)
	}
	if res.(map[string]any)["count"].(int) != 0 {
		t.Fatalf("初始应为空：%v", res)
	}

	// 新增
	res, err = reg.Call(ctx, "cron", map[string]any{"action": "add", "expr": "0 8 * * *", "goal": "生成昨日小结"})
	if err != nil {
		t.Fatalf("add 失败：%v", err)
	}
	id := res.(map[string]any)["job"].(CronJobInfo).ID

	// 停用
	res, err = reg.Call(ctx, "cron", map[string]any{"action": "toggle", "id": id, "enabled": false})
	if err != nil {
		t.Fatalf("toggle 失败：%v", err)
	}
	if res.(map[string]any)["job"].(CronJobInfo).Enabled {
		t.Fatal("toggle false 后应为停用")
	}

	// 改目标
	res, err = reg.Call(ctx, "cron", map[string]any{"action": "update", "id": id, "goal": "改成新的目标"})
	if err != nil {
		t.Fatalf("update 失败：%v", err)
	}
	if res.(map[string]any)["job"].(CronJobInfo).Goal != "改成新的目标" {
		t.Fatalf("goal 应被更新：%v", res)
	}

	// 缺少参数要明确报错
	if _, err := reg.Call(ctx, "cron", map[string]any{"action": "add", "expr": "0 8 * * *"}); err == nil {
		t.Fatal("add 缺 goal 时应报错")
	}
	if _, err := reg.Call(ctx, "cron", map[string]any{"action": "toggle", "id": id}); err == nil {
		t.Fatal("toggle 缺 enabled 时应报错")
	}

	// 删除是独立工具（走审批）
	if _, err := reg.Call(ctx, "cron_remove", map[string]any{"id": id}); err != nil {
		t.Fatalf("cron_remove 失败：%v", err)
	}
	res, _ = reg.Call(ctx, "cron", map[string]any{"action": "list"})
	if res.(map[string]any)["count"].(int) != 0 {
		t.Fatalf("删除后应为空：%v", res)
	}
}

func TestCronRemoveNeedsApproval(t *testing.T) {
	reg := NewRegistry()
	RegisterCron(reg, CronHooks{List: func() []CronJobInfo { return nil }})
	if !reg.IsDangerous("cron_remove") {
		t.Fatal("cron_remove 必须需要人工审批")
	}
	if reg.IsDangerous("cron") {
		t.Fatal("cron（看/加/改/启停）不该需要审批")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
