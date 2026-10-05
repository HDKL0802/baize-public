package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"AGENTS.md":        true,
		"my-notes.md":      true,
		"子文件夹.md":         true, // 中文文件名可以，只要不带路径分隔符
		"a.txt":            false,
		"../evil.md":       false,
		"sub/dir.md":       false,
		`sub\dir.md`:       false,
		".hidden.md":       false,
		"":                 false,
		"noext":            false,
		strings.Repeat("a", 80) + ".md": false,
	}
	for name, want := range cases {
		if got := Valid(name); got != want {
			t.Errorf("Valid(%q) = %v，期望 %v", name, got, want)
		}
	}
}

func TestWriteReadListBuild(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)
	if err := lib.Write("SOUL.md", "灵魂内容"); err != nil {
		t.Fatal(err)
	}
	if err := lib.Write("AGENTS.md", "工作方式"); err != nil {
		t.Fatal(err)
	}
	if err := lib.Write("EXTRA.md", "额外的"); err != nil {
		t.Fatal(err)
	}

	// Read 原文
	if got, err := lib.Read("SOUL.md"); err != nil || got != "灵魂内容" {
		t.Fatalf("Read 不对：%q %v", got, err)
	}
	// Read 不存在的文件必须明确报错（不能回空串）
	if _, err := lib.Read("NOPE.md"); err == nil {
		t.Fatal("读不存在的文件竟然没报错")
	}
	// 非法文件名必须拒绝
	if _, err := lib.Read("../etc/passwd"); err == nil {
		t.Fatal("路径穿越竟然被接受了")
	}

	// List：启用顺序在前（含不存在的），其余在目录里但未启用的排在后面
	files := lib.List([]string{"AGENTS.md", "PROFILE.md"})
	if len(files) != 4 {
		t.Fatalf("列了 %d 个，期望 4：%+v", len(files), files)
	}
	if files[0].Name != "AGENTS.md" || !files[0].Enabled || !files[0].Exists {
		t.Fatalf("第一个应是启用的 AGENTS.md：%+v", files[0])
	}
	if files[1].Name != "PROFILE.md" || !files[1].Enabled || files[1].Exists {
		t.Fatalf("第二个应是启用但缺失的 PROFILE.md：%+v", files[1])
	}
	if files[2].Name != "EXTRA.md" || files[2].Enabled || !files[2].Exists {
		t.Fatalf("第三个应是未启用的 EXTRA.md：%+v", files[2])
	}
	if files[3].Name != "SOUL.md" || files[3].Enabled {
		t.Fatalf("第四个应是未启用的 SOUL.md：%+v", files[3])
	}

	// Build 按启用顺序拼，带 # 文件名 头
	prompt := lib.Build([]string{"AGENTS.md", "SOUL.md"}, false)
	if !strings.Contains(prompt, "# AGENTS.md") || !strings.Contains(prompt, "# SOUL.md") {
		t.Fatalf("拼出来的提示缺文件名头：\n%s", prompt)
	}
	if strings.Index(prompt, "AGENTS.md") > strings.Index(prompt, "SOUL.md") {
		t.Fatal("顺序不对：AGENTS.md 应排在 SOUL.md 前面")
	}
	if strings.Contains(prompt, "额外的") {
		t.Fatal("未启用的文件不该进提示")
	}
	// 缺失文件只是跳过，不能报错也不能占位
	if got := lib.Build([]string{"PROFILE.md", "NOPE.md"}, false); got != "" {
		t.Fatalf("缺失文件应被跳过（期望空串）：%q", got)
	}
}

func TestFrontmatterAndHeartbeat(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)
	content := "---\ntags: [x]\n---\n\n开头正文\n\n<!-- heartbeat:start -->\n心跳规则\n<!-- heartbeat:end -->\n\n结尾正文\n"
	if err := lib.Write("AGENTS.md", content); err != nil {
		t.Fatal(err)
	}

	// front-matter 不进提示
	if got := lib.Build([]string{"AGENTS.md"}, false); strings.Contains(got, "tags:") {
		t.Fatalf("front-matter 没被去掉：\n%s", got)
	}
	// 心跳关闭：整段消失、标记也不留
	off := lib.Build([]string{"AGENTS.md"}, false)
	if strings.Contains(off, "心跳规则") || strings.Contains(off, "heartbeat:") {
		t.Fatalf("心跳关闭时该段应整体删掉：\n%s", off)
	}
	if !strings.Contains(off, "开头正文") || !strings.Contains(off, "结尾正文") {
		t.Fatalf("段外正文不能被误伤：\n%s", off)
	}
	// 心跳开启：内容留下、标记去掉
	on := lib.Build([]string{"AGENTS.md"}, true)
	if !strings.Contains(on, "心跳规则") || strings.Contains(on, "heartbeat:start") {
		t.Fatalf("心跳开启时应保留内容并去掉标记：\n%s", on)
	}
}

func TestArchiveAndReset(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)
	if err := lib.Write("EXTRA.md", "要归档的"); err != nil {
		t.Fatal(err)
	}
	if err := lib.Archive("EXTRA.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "EXTRA.md")); !os.IsNotExist(err) {
		t.Fatal("归档后原文件还在")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".archive", "EXTRA-*.md"))
	if len(matches) != 1 {
		t.Fatalf("归档目录里应有一份：%v", matches)
	}
	// 归档不存在的文件要报错
	if err := lib.Archive("EXTRA.md"); err == nil {
		t.Fatal("归档不存在的文件竟然成功了")
	}
	// 有模板的能恢复默认，没模板的明确报错
	if err := lib.Reset("SOUL.md"); err != nil {
		t.Fatal(err)
	}
	if got, _ := lib.Read("SOUL.md"); !strings.Contains(got, "灵魂") {
		t.Fatalf("恢复默认后内容不对：%q", got)
	}
	if err := lib.Reset("EXTRA.md"); err == nil {
		t.Fatal("没有模板的文件竟然能恢复默认")
	}
}

func TestEnsureTemplatesOnce(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)
	created, err := lib.EnsureTemplates()
	if err != nil || !created {
		t.Fatalf("首次应创建模板：created=%v err=%v", created, err)
	}
	for _, name := range DefaultFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("模板 %s 没落盘：%v", name, err)
		}
	}
	// 用户把人设全删了 → 重启不能又把模板塞回来
	for _, name := range DefaultFiles {
		if err := lib.Archive(name); err != nil {
			t.Fatal(err)
		}
	}
	created, err = lib.EnsureTemplates()
	if err != nil || created {
		t.Fatalf("第二次不该重建：created=%v err=%v", created, err)
	}
	if files := lib.List(nil); len(files) != 0 {
		t.Fatalf("归档后目录里不该再看到人设文件：%+v", files)
	}
}

func TestBuildOrderKeepsEnabledOrder(t *testing.T) {
	dir := t.TempDir()
	lib := New(dir)
	_ = lib.Write("A.md", "AAA")
	_ = lib.Write("B.md", "BBB")
	prompt := lib.Build([]string{"B.md", "A.md"}, false)
	if strings.Index(prompt, "BBB") > strings.Index(prompt, "AAA") {
		t.Fatalf("拼装顺序应听启用清单的：\n%s", prompt)
	}
}