package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithFrontMatterAndPlain(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "weekly-report", "SKILL.md"), `---
name: 周报生成
description: 把这一周的待办与记忆汇总成周报
triggers: [周报, weekly]
---

# 步骤
1. 调用 memory_search 取本周记忆
2. 汇总成 Markdown
`)
	write(t, filepath.Join(dir, "quick-note.md"), `# 随手记

把一句话记进记忆库，标注 kind=note。
`)

	lib, err := Load(dir)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	names := lib.Names()
	if len(names) != 2 {
		t.Fatalf("应加载 2 个技能，实际 %v", names)
	}
	sk, ok := lib.Get("周报生成")
	if !ok {
		t.Fatalf("应能按名字取到技能：%v", names)
	}
	if sk.Description == "" || !strings.Contains(sk.Body, "memory_search") {
		t.Fatalf("技能正文/说明不对：%+v", sk)
	}
	if len(sk.Triggers) != 2 || sk.Triggers[0] != "周报" {
		t.Fatalf("触发词解析不对：%+v", sk.Triggers)
	}
	if strings.Contains(sk.Body, "name: 周报生成") {
		t.Fatal("正文里不应再包含 front-matter")
	}

	plain, ok := lib.Get("quick-note")
	if !ok || plain.Name != "随手记" {
		t.Fatalf("没有 front-matter 时应取一级标题当名字：%+v", plain)
	}
	if _, ok := lib.Get("不存在"); ok {
		t.Fatal("不存在的技能不该命中")
	}

	index := lib.Index()
	if !strings.Contains(index, "周报生成") || strings.Contains(index, "memory_search") {
		t.Fatalf("清单只应包含名字与说明（渐进式披露）：\n%s", index)
	}
}

func TestLoadMissingDirIsFine(t *testing.T) {
	lib, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("技能目录不存在不该报错：%v", err)
	}
	if len(lib.All()) != 0 || lib.Index() != "" {
		t.Fatal("空技能库应返回空内容")
	}
}
