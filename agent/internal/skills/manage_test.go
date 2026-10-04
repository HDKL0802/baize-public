package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baize/internal/tools"
)

func newMgr(t *testing.T) (*Manager, *Library, string) {
	t.Helper()
	dir := t.TempDir()
	lib, err := Load(dir)
	if err != nil {
		t.Fatalf("加载技能库失败：%v", err)
	}
	return NewManager(lib), lib, dir
}

const goodSkill = `---
name: 写周报
description: 把本周待办与记忆汇总成周报时用。
---

# 步骤
1. 用 memory_search 取本周记忆
2. 汇总成 Markdown
`

func TestCreateThenVisibleWithoutRestart(t *testing.T) {
	m, lib, dir := newMgr(t)
	if _, err := m.Create("weekly-report", "", goodSkill); err != nil {
		t.Fatalf("新建技能失败：%v", err)
	}
	// 关键：同一个 *Library 指针必须立刻能看到新技能（skill_load 持的就是它）
	if _, ok := lib.Get("weekly-report"); !ok {
		t.Fatalf("新建后技能库没刷新，清单：%v", lib.Names())
	}
	if !strings.Contains(lib.Index(), "写周报") {
		t.Fatalf("系统提示清单里应出现新技能：%s", lib.Index())
	}
	if _, err := os.Stat(filepath.Join(dir, "weekly-report", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 没落盘：%v", err)
	}
	// 重名要挡住，不能被静默覆盖
	if _, err := m.Create("weekly-report", "", goodSkill); err == nil {
		t.Fatal("重名新建应当报错")
	}
}

func TestCreateValidation(t *testing.T) {
	m, _, _ := newMgr(t)
	cases := []struct {
		name, skill, wantErr string
	}{
		{"BadName", goodSkill, "技能名"},
		{"no-front", "# 标题\n正文", "front-matter"},
		{"no-close", "---\nname: x\ndescription: y\n\n正文", "闭合"},
		{"no-name", "---\ndescription: y\n---\n正文", "name 字段"},
		{"no-desc", "---\nname: x\n---\n正文", "description 字段"},
		{"no-body", "---\nname: x\ndescription: y\n---\n", "正文"},
	}
	for _, c := range cases {
		if _, err := m.Create(c.name, "", c.skill); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s 应当报错（含 %q），实际：%v", c.name, c.wantErr, err)
		}
	}
	// 新建技能的 description 必须塞得进系统提示的一行预算
	long := strings.Repeat("很长", 40)
	if _, err := m.Create("too-long", "", "---\nname: x\ndescription: "+long+"\n---\n正文"); err == nil ||
		!strings.Contains(err.Error(), "description") {
		t.Fatalf("超长 description 应当被拒，实际：%v", err)
	}
	// 分类不能带斜杠
	if _, err := m.Create("ok-name", "a/b", goodSkill); err == nil {
		t.Fatal("分类带斜杠应当报错")
	}
}

func TestCategoryNestedAndEdit(t *testing.T) {
	m, lib, dir := newMgr(t)
	if _, err := m.Create("weekly-report", "ops", goodSkill); err != nil {
		t.Fatalf("带分类新建失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ops", "weekly-report", "SKILL.md")); err != nil {
		t.Fatalf("分类目录结构不对：%v", err)
	}
	// 分类相对路径也能定位
	if _, err := m.Edit("ops/weekly-report", strings.Replace(goodSkill, "写周报", "写月报", 1)); err != nil {
		t.Fatalf("按分类路径改技能失败：%v", err)
	}
	sk, ok := lib.Get("weekly-report")
	if !ok || sk.Name != "写月报" {
		t.Fatalf("整篇重写后技能库没更新：%+v", sk)
	}
	if _, err := m.Edit("ops/weekly-report", "没有 front-matter"); err == nil {
		t.Fatal("改成结构不合法的内容应当被拒")
	}
}

func TestPatchRules(t *testing.T) {
	m, _, _ := newMgr(t)
	if _, err := m.Create("weekly-report", "", goodSkill); err != nil {
		t.Fatal(err)
	}
	res, err := m.Patch("weekly-report", "汇总成 Markdown", "汇总成表格", "", false)
	if err != nil {
		t.Fatalf("patch 失败：%v", err)
	}
	if res["matchCount"].(int) != 1 {
		t.Fatalf("匹配数应为 1：%v", res["matchCount"])
	}
	if _, err := m.Patch("weekly-report", "不存在的原文", "x", "", false); err == nil {
		t.Fatal("找不到原文应当报错")
	}
	// 破坏 front-matter 的替换要被拦下
	if _, err := m.Patch("weekly-report", "name:", "", "", false); err == nil {
		t.Fatal("改坏 front-matter 应当被拒")
	}
}

func TestWriteAndRemoveSupportingFile(t *testing.T) {
	m, _, dir := newMgr(t)
	if _, err := m.Create("weekly-report", "", goodSkill); err != nil {
		t.Fatal(err)
	}
	if _, err := m.WriteFile("weekly-report", "references/checklist.md", "- [ ] 数据核对\n"); err != nil {
		t.Fatalf("写支持文件失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "weekly-report", "references", "checklist.md")); err != nil {
		t.Fatalf("支持文件没落盘：%v", err)
	}
	// 路径必须在允许的子目录内，且不许跳出技能目录
	for _, p := range []string{"evil.md", "../escape.md", "references/../../escape.md", "SKILL.md.bak/../../x"} {
		if _, err := m.WriteFile("weekly-report", p, "x"); err == nil {
			t.Fatalf("非法路径 %q 应当被拒", p)
		}
	}
	if _, err := m.RemoveFile("weekly-report", "references/checklist.md"); err != nil {
		t.Fatalf("删支持文件失败：%v", err)
	}
	if _, err := m.RemoveFile("weekly-report", "references/checklist.md"); err == nil {
		t.Fatal("删不存在的文件应当报错")
	}
	if _, err := m.RemoveFile("weekly-report", "SKILL.md"); err == nil {
		t.Fatal("不允许用 remove_file 删 SKILL.md")
	}
}

// 审批口径：沉淀技能免审批（但要打快照），删技能必须走人工审批
func TestRegisterToolsAndGates(t *testing.T) {
	m, _, _ := newMgr(t)
	reg := tools.NewRegistry()
	RegisterTools(reg, m)
	if _, ok := reg.Get("skill_manage"); !ok {
		t.Fatalf("应注册 skill_manage：%v", reg.Names())
	}
	if _, ok := reg.Get("skill_delete"); !ok {
		t.Fatalf("应注册 skill_delete：%v", reg.Names())
	}
	if reg.IsDangerous("skill_manage") {
		t.Fatal("新建/改写技能不该要人工审批")
	}
	if !reg.IsDangerous("skill_delete") {
		t.Fatal("删技能必须走人工审批")
	}
	if !reg.IsMutating("skill_manage") || !reg.IsMutating("skill_delete") {
		t.Fatal("动技能目录前必须打快照")
	}
	// 技能目录没配好时干脆不注册，免得模型调到一半才发现不能用
	empty := tools.NewRegistry()
	RegisterTools(empty, NewManager(Empty("")))
	if _, ok := empty.Get("skill_manage"); ok {
		t.Fatal("技能目录没配置时不该注册技能工具")
	}
}

// 工具入口的参数形状：缺 content/file_content/new_string、以及不支持的 action 都要明确报错
func TestManageToolRunShapes(t *testing.T) {
	m, _, _ := newMgr(t)
	tool := &ManageTool{M: m}
	ctx := context.Background()
	if _, err := tool.Run(ctx, map[string]any{"action": "create", "name": "weekly-report", "content": goodSkill}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "create", "name": "x"}); err == nil {
		t.Fatal("create 缺 content 应当报错")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "write_file", "name": "weekly-report", "file_path": "references/a.md"}); err == nil {
		t.Fatal("write_file 缺 file_content 应当报错")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "patch", "name": "weekly-report", "old_string": "步骤"}); err == nil {
		t.Fatal("patch 缺 new_string 应当报错（想删内容要显式传空串）")
	}
	if _, err := tool.Run(ctx, map[string]any{"action": "nope", "name": "weekly-report"}); err == nil {
		t.Fatal("不支持的 action 应当报错")
	}
}

func TestDeleteArchivesInsteadOfHardDelete(t *testing.T) {
	m, lib, dir := newMgr(t)
	if _, err := m.Create("weekly-report", "", goodSkill); err != nil {
		t.Fatal(err)
	}
	res, err := m.Delete("weekly-report")
	if err != nil {
		t.Fatalf("归档删除失败：%v", err)
	}
	if _, ok := lib.Get("weekly-report"); ok {
		t.Fatal("归档后不该还在技能清单里")
	}
	archived := res["archivedTo"].(string)
	if !strings.HasPrefix(archived, ".archive/") {
		t.Fatalf("应当归档到 .archive 下：%v", archived)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(archived), "SKILL.md")); err != nil {
		t.Fatalf("归档目录里应保留 SKILL.md：%v", err)
	}
	// 归档目录本身不能被当成技能重新加载
	lib2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lib2.All()) != 0 {
		t.Fatalf(".archive 不该被加载成技能：%v", lib2.Names())
	}
	if _, err := m.Delete("weekly-report"); err == nil {
		t.Fatal("删不存在的技能应当报错")
	}
}
