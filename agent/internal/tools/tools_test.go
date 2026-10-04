package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baize/internal/memory"
)

func newWS(t *testing.T, allow bool) *Workspace {
	t.Helper()
	ws, err := NewWorkspace(t.TempDir(), allow)
	if err != nil {
		t.Fatalf("创建工作目录失败：%v", err)
	}
	return ws
}

func TestWorkspaceSandbox(t *testing.T) {
	ws := newWS(t, false)
	if _, err := ws.Resolve("a/b.txt"); err != nil {
		t.Fatalf("工作目录内的路径应放行：%v", err)
	}
	if _, err := ws.Resolve(filepath.Join("..", "escape.txt")); err == nil {
		t.Fatal("越界路径必须被拒绝")
	}
	if _, err := ws.Resolve(string(filepath.Separator) + "etc"); err == nil && os.PathSeparator != '\\' {
		t.Fatal("绝对路径越界必须被拒绝")
	}
	if _, err := ws.Resolve(""); err == nil {
		t.Fatal("空路径必须报错")
	}
	loose := newWS(t, true)
	if _, err := loose.Resolve(filepath.Join("..", "x.txt")); err != nil {
		t.Fatalf("allowOutside=true 时应放行：%v", err)
	}
}

// 审批口径（文档 §6.1）：删除 / 执行命令 / 跨端下发才要人工审批；
// 沙箱内写文件不要审批，但属于"会改现场"，执行前必须打快照。
func TestApprovalScopeVersusMutating(t *testing.T) {
	ws := newWS(t, false)
	mem, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	t.Cleanup(func() { mem.Close() })

	reg := NewRegistry()
	RegisterFS(reg, ws)
	reg.Register(NewShellRun(ws, true))
	RegisterMemory(reg, mem, "")

	cases := []struct {
		tool                string
		dangerous, mutating bool
	}{
		{"fs_write", false, true},
		{"fs_delete", true, true},
		{"shell_run", true, true},
		{"fs_read", false, false},
		{"fs_list", false, false},
		{"memory", false, true},       // 记忆读写免审批，但要打快照
		{"memory_forget", true, true}, // 忘掉记忆 = 删除，必须审批
	}
	for _, c := range cases {
		if got := reg.IsDangerous(c.tool); got != c.dangerous {
			t.Fatalf("%s 需不需要审批不对：期望 %v，实际 %v", c.tool, c.dangerous, got)
		}
		if got := reg.IsMutating(c.tool); got != c.mutating {
			t.Fatalf("%s 要不要打快照不对：期望 %v，实际 %v", c.tool, c.mutating, got)
		}
	}
}

func TestFSWriteReadListDelete(t *testing.T) {
	ws := newWS(t, false)
	ctx := context.Background()
	reg := NewRegistry()
	RegisterFS(reg, ws)

	if _, err := reg.Call(ctx, "fs_write", map[string]any{"path": "docs/a.md", "content": "第一行\n第二行"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := reg.Call(ctx, "fs_read", map[string]any{"path": "docs/a.md"})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	content := got.(map[string]any)["content"].(string)
	if !strings.Contains(content, "第二行") {
		t.Fatalf("读取内容不对：%q", content)
	}

	list, err := reg.Call(ctx, "fs_list", map[string]any{"path": "docs"})
	if err != nil {
		t.Fatalf("列目录失败：%v", err)
	}
	if list.(map[string]any)["count"].(int) != 1 {
		t.Fatalf("目录里应有 1 个文件：%+v", list)
	}

	if _, err := reg.Call(ctx, "fs_write", map[string]any{"path": "docs/a.md", "content": "追加", "append": true}); err != nil {
		t.Fatalf("追加失败：%v", err)
	}
	got, _ = reg.Call(ctx, "fs_read", map[string]any{"path": "docs/a.md"})
	if !strings.Contains(got.(map[string]any)["content"].(string), "追加") {
		t.Fatal("追加模式应保留原内容")
	}

	if _, err := reg.Call(ctx, "fs_delete", map[string]any{"path": "docs/a.md"}); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "docs", "a.md")); !os.IsNotExist(err) {
		t.Fatal("文件应已删除")
	}
}

func TestFSDeleteRefusesWorkspaceRoot(t *testing.T) {
	ws := newWS(t, false)
	reg := NewRegistry()
	RegisterFS(reg, ws)
	if _, err := reg.Call(context.Background(), "fs_delete", map[string]any{"path": "."}); err == nil {
		t.Fatal("不许删除工作目录本身")
	}
}

func TestFSReadMissingFileIsClearError(t *testing.T) {
	ws := newWS(t, false)
	reg := NewRegistry()
	RegisterFS(reg, ws)
	_, err := reg.Call(context.Background(), "fs_read", map[string]any{"path": "nope.txt"})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("应明确报「文件不存在」，实际：%v", err)
	}
}

func TestShellRunDisabledIsExplicit(t *testing.T) {
	ws := newWS(t, false)
	reg := NewRegistry()
	reg.Register(NewShellRun(ws, false))
	_, err := reg.Call(context.Background(), "shell_run", map[string]any{"cmd": "echo hi"})
	if err == nil || !strings.Contains(err.Error(), "未开启命令执行") {
		t.Fatalf("未开启时必须明确报错，实际：%v", err)
	}
	if !reg.IsDangerous("shell_run") {
		t.Fatal("shell_run 应标记为危险工具")
	}
}

func TestShellRunEnabled(t *testing.T) {
	ws := newWS(t, false)
	reg := NewRegistry()
	reg.Register(NewShellRun(ws, true))
	out, err := reg.Call(context.Background(), "shell_run", map[string]any{"cmd": "echo baize-ok"})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	m := out.(map[string]any)
	if m["exitCode"].(int) != 0 || !strings.Contains(m["output"].(string), "baize-ok") {
		t.Fatalf("输出不对：%+v", m)
	}
}

func TestRegistryUnknownToolAndSpecs(t *testing.T) {
	ws := newWS(t, false)
	reg := NewRegistry()
	RegisterFS(reg, ws)
	if _, err := reg.Call(context.Background(), "no.such.tool", nil); err == nil {
		t.Fatal("未知工具必须报错")
	}
	specs := reg.Specs()
	if len(specs) != 4 {
		t.Fatalf("应注册 4 个文件工具，实际 %d", len(specs))
	}
	for _, s := range specs {
		if s.Name == "" || s.Description == "" || s.Schema == nil {
			t.Fatalf("工具说明不完整：%+v", s)
		}
	}
}

func TestMemoryTools(t *testing.T) {
	mem, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	defer mem.Close()
	reg := NewRegistry()
	RegisterMemory(reg, mem, "")
	ctx := context.Background()

	if _, err := reg.Call(ctx, "memory", map[string]any{
		"action":  "store",
		"content": "项目代号白泽，密码本用 PBKDF2 150000 次迭代加 AES-GCM 加密。",
		"kind":    "decision", "title": "加密方案",
	}); err != nil {
		t.Fatalf("写记忆失败：%v", err)
	}
	out, err := reg.Call(ctx, "memory", map[string]any{"action": "hybrid_search", "query": "加密方案"})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if out.(map[string]any)["count"].(int) == 0 {
		t.Fatalf("应检索到刚写入的记忆：%+v", out)
	}
	// recall 也要能想回来（没配向量通道时应明确说明只用了关键词）
	rec, err := reg.Call(ctx, "memory", map[string]any{"action": "recall", "query": "密码本怎么加密的"})
	if err != nil {
		t.Fatalf("召回失败：%v", err)
	}
	if rec.(map[string]any)["found"].(int) == 0 {
		t.Fatalf("recall 应能想起这条记忆：%+v", rec)
	}
	if _, err := reg.Call(ctx, "memory", map[string]any{"action": "store"}); err == nil {
		t.Fatal("store 缺 content 必须报错")
	}
	if _, err := reg.Call(ctx, "memory", map[string]any{"action": "不存在的动作"}); err == nil {
		t.Fatal("未知 action 必须报错（不能静默成功）")
	}
	// 遗忘必须能删掉，且删不到时明确报错
	if _, err := reg.Call(ctx, "memory_forget", map[string]any{"docKey": "根本没有这把键"}); err == nil {
		t.Fatal("删不到东西必须报错")
	}
}

func TestWebFetchRejectsBadScheme(t *testing.T) {
	f := NewWebFetch()
	if _, err := f.Run(context.Background(), map[string]any{"url": "file:///etc/passwd"}); err == nil {
		t.Fatal("非 http/https 必须拒绝")
	}
	if _, err := f.Run(context.Background(), map[string]any{"url": ""}); err == nil {
		t.Fatal("空地址必须报错")
	}
	if got := htmlToText("<html><head><style>x{}</style></head><body><p>你好 <b>世界</b></p><script>bad()</script></body></html>"); strings.Contains(got, "bad()") || !strings.Contains(got, "你好") {
		t.Fatalf("HTML 转文本不对：%q", got)
	}
}
