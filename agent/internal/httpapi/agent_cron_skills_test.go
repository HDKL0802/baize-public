package httpapi_test

import (
	"strings"
	"testing"
)

/* 定时任务 / 技能的增删改接口测试。
   这两块原先只能读（cron 只能新增、技能只能列），桌面端因此只能做成「只增 / 只读」。 */

type cronResp struct {
	Jobs []struct {
		ID       string `json:"id"`
		Expr     string `json:"expr"`
		Goal     string `json:"goal"`
		Recipe   string `json:"recipe"`
		Enabled  bool   `json:"enabled"`
		ExprText string `json:"exprText"`
		Running  bool   `json:"running"`
	} `json:"jobs"`
	Added   string `json:"added"`
	Started bool   `json:"started"`
	RunID   string `json:"runId"`
}

// skillView 技能清单里的一条（与接口返回同形）
type skillView struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type skillsResp struct {
	Skills []skillView `json:"skills"`
}

// findSkill 按 slug 找一条技能（返回的是切片元素副本；找不到返回 nil）
//
// 为什么需要它：环境里现在带着随二进制分发的**内置技能**（读文件/文档/造技能/定时任务/笔记），
// 所以"清单里有几条"不再是一个固定数字，测试要按 slug 定位自己那条。
func findSkill(list []skillView, slug string) *skillView {
	for _, s := range list {
		if s.Slug == slug {
			cp := s
			return &cp
		}
	}
	return nil
}

// 一份合法的 SKILL.md。
// 注意两个名字是分开的：目录名（slug）必须 ASCII 且以字母/数字开头，
// front-matter 里的 name 才是展示名（可以中文）。
const goodSkill = "---\nname: 测试技能\ndescription: 用来验证技能接口\n---\n\n正文：先做 A 再做 B\n"

func TestCronCRUD(t *testing.T) {
	e, _ := newAgentEnv(t)

	var base cronResp
	if code := e.do("GET", "/api/agent/cron", nil, true, &base); code != 200 {
		t.Fatalf("GET /api/agent/cron 期望 200，实际 %d", code)
	}
	before := len(base.Jobs)

	// 坏表达式必须被挡住：否则它会以 parseError 长期躺在列表里，看着像"加了但没跑"
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "add", "expr": "不是 cron", "goal": "x"}, true, &cronResp{}); code != 400 {
		t.Fatalf("非法 cron 表达式期望 400，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "add", "expr": "0 8 * * *"}, true, &cronResp{}); code != 400 {
		t.Fatalf("缺 goal 期望 400，实际 %d", code)
	}

	// 新增
	var added cronResp
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "add", "expr": "0 8 * * *", "goal": "总结昨天的记录"}, true, &added); code != 200 {
		t.Fatalf("新增定时任务期望 200，实际 %d", code)
	}
	if len(added.Jobs) != before+1 || added.Added == "" {
		t.Fatalf("新增后应有 %d 条且返回 added id：%+v", before+1, added)
	}
	id := added.Added

	// 表达式要有人话描述：光看 "0 8 * * *" 用户没法确认自己填对了
	if got := added.Jobs[0].ExprText; got != "每天 08:00" {
		t.Fatalf("exprText 期望「每天 08:00」，实际 %q", got)
	}

	// 停用
	var off cronResp
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "toggle", "id": id, "enabled": false}, true, &off); code != 200 {
		t.Fatalf("停用期望 200，实际 %d", code)
	}
	if off.Jobs[0].Enabled {
		t.Fatalf("停用后 enabled 应为 false：%+v", off.Jobs[0])
	}
	// toggle 少给 enabled 要报错，不能默默当成开
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "toggle", "id": id}, true, &cronResp{}); code != 400 {
		t.Fatalf("toggle 缺 enabled 期望 400，实际 %d", code)
	}

	// 改（表达式 + 目标）
	var upd cronResp
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "update", "id": id, "expr": "30 7 * * 1-5", "goal": "工作日早上总结"}, true, &upd); code != 200 {
		t.Fatalf("更新期望 200，实际 %d", code)
	}
	if upd.Jobs[0].Expr != "30 7 * * 1-5" || upd.Jobs[0].Goal != "工作日早上总结" {
		t.Fatalf("更新没生效：%+v", upd.Jobs[0])
	}
	// 改成坏表达式同样要挡
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "update", "id": id, "expr": "99 99 * * *"}, true, &cronResp{}); code != 400 {
		t.Fatalf("更新为非法表达式期望 400，实际 %d", code)
	}

	// 删
	var del cronResp
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "remove", "id": id}, true, &del); code != 200 {
		t.Fatalf("删除期望 200，实际 %d", code)
	}
	if len(del.Jobs) != before {
		t.Fatalf("删除后应回到 %d 条：%+v", before, del.Jobs)
	}

	// 不存在的 id / 不认识的 action
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "remove", "id": "job-nope"}, true, &cronResp{}); code != 404 {
		t.Fatalf("不存在的 id 期望 404，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "wat"}, true, &cronResp{}); code != 400 {
		t.Fatalf("未知 action 期望 400，实际 %d", code)
	}
}

// 立即执行：不存在的 id 要 404、没有目标要 409、真跑起来要 202 且带 runId。
//
// 为什么单列一条：这条路径会真的派活（占顶层串行锁、调模型）。测试环境里没有可用
// 模型通道，所以"跑起来"那一步只会立刻失败 —— 但接口契约（202 + runId）与两种
// 拒绝的语义必须钉死，否则界面上「立即执行」按钮会以各种奇怪的方式静默失败。
func TestCronRunNow(t *testing.T) {
	e, _ := newAgentEnv(t)

	if code := e.do("POST", "/api/agent/cron/run", map[string]any{"id": "job-nope"}, true, &cronResp{}); code != 404 {
		t.Fatalf("不存在的 id 期望 404，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/cron/run", map[string]any{}, true, &cronResp{}); code != 404 {
		t.Fatalf("空 id 期望 404，实际 %d", code)
	}

	var added cronResp
	if code := e.do("POST", "/api/agent/cron",
		map[string]any{"action": "add", "expr": "0 8 * * *", "goal": "立即执行自检"}, true, &added); code != 200 {
		t.Fatalf("新增定时任务期望 200，实际 %d", code)
	}
	var started cronResp
	if code := e.do("POST", "/api/agent/cron/run", map[string]any{"id": added.Added}, true, &started); code != 202 {
		t.Fatalf("立即执行期望 202，实际 %d", code)
	}
	if !started.Started || started.RunID == "" {
		t.Fatalf("立即执行应回 started=true 且带 runId：%+v", started)
	}
}

func TestSkillsCRUD(t *testing.T) {
	e, _ := newAgentEnv(t)

	var base skillsResp
	if code := e.do("GET", "/api/agent/skills", nil, true, &base); code != 200 {
		t.Fatalf("GET /api/agent/skills 期望 200，实际 %d", code)
	}
	// 环境里会有随二进制分发的内置技能；这里要确认的是"还没有用户自己建的技能"
	if findSkill(base.Skills, "triage") != nil {
		t.Fatalf("新建的环境里不该有 triage：%+v", base.Skills)
	}
	baseCount := len(base.Skills)

	// 缺 front-matter 要明确报错，不能悄悄落一个坏技能
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "create", "name": "bad", "content": "没有 front-matter"}, true, &skillsResp{}); code != 400 {
		t.Fatalf("缺 front-matter 期望 400，实际 %d", code)
	}
	// 目录名（slug）必须 ASCII：中文名要挡住，否则会落出一个怪目录
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "create", "name": "中文目录", "content": goodSkill}, true, &skillsResp{}); code != 400 {
		t.Fatalf("非法目录名期望 400，实际 %d", code)
	}

	// save：不存在 → 新建
	var made skillsResp
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "save", "name": "triage", "content": goodSkill}, true, &made); code != 200 {
		t.Fatalf("新建技能期望 200，实际 %d", code)
	}
	if len(made.Skills) != baseCount+1 {
		t.Fatalf("新建后清单应多 1 条：%+v", made.Skills)
	}
	tri := findSkill(made.Skills, "triage")
	if tri == nil || tri.Name != "测试技能" {
		t.Fatalf("slug 应是 triage、展示名应是 测试技能：%+v", made.Skills)
	}
	if tri.Description != "用来验证技能接口" {
		t.Fatalf("description 应来自 front-matter：%+v", tri)
	}

	// save：已存在 → 改写（不能报"已存在"，界面上只有一个「保存」按钮）
	// 这里刻意用【展示名】而不是 slug，验证名字归一
	edited := "---\nname: 测试技能\ndescription: 改过的描述\n---\n\n正文：只做 A\n"
	var again skillsResp
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "save", "name": "测试技能", "content": edited}, true, &again); code != 200 {
		t.Fatalf("按展示名改写期望 200，实际 %d", code)
	}
	if tri := findSkill(again.Skills, "triage"); tri == nil || tri.Description != "改过的描述" {
		t.Fatalf("按展示名改写没生效（名字归一有问题）：%+v", again.Skills)
	}

	// 局部替换（patch）
	var pat skillsResp
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "patch", "name": "triage", "oldString": "只做 A", "newString": "只做 B"}, true, &pat); code != 200 {
		t.Fatalf("patch 期望 200，实际 %d", code)
	}
	if tri := findSkill(pat.Skills, "triage"); tri == nil || !strings.Contains(tri.Body, "只做 B") {
		t.Fatalf("patch 没生效：%+v", pat.Skills)
	}

	// 未知 action
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "wat", "name": "triage"}, true, &skillsResp{}); code != 400 {
		t.Fatalf("未知 action 期望 400，实际 %d", code)
	}

	// 删除（归档到 .archive，加载器跳过点号目录 → 清单里就该看不见了）
	var gone skillsResp
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "delete", "name": "triage"}, true, &gone); code != 200 {
		t.Fatalf("删除技能期望 200，实际 %d", code)
	}
	if findSkill(gone.Skills, "triage") != nil {
		t.Fatalf("归档后不该还能看到 triage：%+v", gone.Skills)
	}
	if len(gone.Skills) != baseCount {
		t.Fatalf("归档后清单应回到 %d 条：%+v", baseCount, gone.Skills)
	}
}

// import：粘贴一份带分类的 SKILL.md 建技能 → 清单里该项 category 正确。
// 同时覆盖两条护栏：缺 SKILL.md 要报错、assets/ 下的二进制这轮不支持。
func TestSkillImportWithCategory(t *testing.T) {
	e, _ := newAgentEnv(t)

	var made skillsResp
	if code := e.do("POST", "/api/agent/skills", map[string]any{
		"action": "import", "name": "imported", "category": "ops",
		"files": []map[string]any{
			{"path": "SKILL.md", "content": goodSkill},
			{"path": "references/checklist.md", "content": "步骤清单\n"},
		},
	}, true, &made); code != 200 {
		t.Fatalf("import 期望 200，实际 %d", code)
	}

	var got skillsResp
	if code := e.do("GET", "/api/agent/skills", nil, true, &got); code != 200 {
		t.Fatalf("GET /api/agent/skills 期望 200，实际 %d", code)
	}
	s := findSkill(got.Skills, "imported")
	if s == nil {
		t.Fatalf("导入后应能按 slug 找到：%+v", got.Skills)
	}
	if s.Category != "ops" {
		t.Fatalf("category 应为 ops，实际 %q：%+v", s.Category, s)
	}

	// 没有 SKILL.md 一律报错，别悄悄落一个坏技能
	if code := e.do("POST", "/api/agent/skills", map[string]any{
		"action": "import", "name": "nores",
		"files": []map[string]any{{"path": "references/a.md", "content": "x"}},
	}, true, &skillsResp{}); code != 400 {
		t.Fatalf("缺 SKILL.md 期望 400，实际 %d", code)
	}
	// assets/ 下的二进制这轮不支持
	if code := e.do("POST", "/api/agent/skills", map[string]any{
		"action": "import", "name": "withasset",
		"files": []map[string]any{
			{"path": "SKILL.md", "content": goodSkill},
			{"path": "assets/logo.png", "content": "\x89PNG"},
		},
	}, true, &skillsResp{}); code != 400 {
		t.Fatalf("assets 非文本期望 400，实际 %d", code)
	}
}

// 没挂 Agent 服务时这几个接口不该注册（控制台/桌面端据此判断功能有没有）
func TestCronSkillsAbsentWithoutAgent(t *testing.T) {
	e := newEnv(t, 5e9)
	if code := e.do("GET", "/api/agent/cron", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/cron 不该可用，实际 %d", code)
	}
	if code := e.do("GET", "/api/agent/skills", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/skills 不该可用，实际 %d", code)
	}
}

/* 技能原文读取接口：验证 front-matter 里的自定义字段能被原样读出（编辑不再丢字段）。 */
type skillRawResp struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
}

func TestSkillRawRead(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 新建一份带额外 front-matter 字段（triggers / version）的 SKILL.md，
	// 这些字段经 GET /api/agent/skills 解析后不会出现在列表里，正是原文接口要救回来的。
	const slug = "raw-read-probe"
	const md = "---\nname: 原文探针\ndescription: 验证原文读取\ntriggers: [探针, 原文]\nversion: 9\n---\n\n正文：保留全部 front-matter。\n"
	if code := e.do("POST", "/api/agent/skills",
		map[string]any{"action": "create", "name": slug, "content": md}, true, &skillsResp{}); code != 200 {
		t.Fatalf("新建技能期望 200，实际 %d", code)
	}
	t.Cleanup(func() {
		e.do("POST", "/api/agent/skills", map[string]any{"action": "delete", "name": slug}, true, &skillsResp{})
	})

	// 缺 name 必须 400，不静默返回空内容。
	if code := e.do("GET", "/api/agent/skills/raw", nil, true, &skillRawResp{}); code != 400 {
		t.Fatalf("缺 name 期望 400，实际 %d", code)
	}
	// 不存在的技能必须 404。
	if code := e.do("GET", "/api/agent/skills/raw?name=not-exist-xyz", nil, true, &skillRawResp{}); code != 404 {
		t.Fatalf("不存在技能期望 404，实际 %d", code)
	}

	var raw skillRawResp
	if code := e.do("GET", "/api/agent/skills/raw?name="+slug, nil, true, &raw); code != 200 {
		t.Fatalf("读原文期望 200，实际 %d", code)
	}
	if raw.Slug != slug {
		t.Fatalf("slug 期望 %q，实际 %q", slug, raw.Slug)
	}
	if raw.Bytes != len(md) {
		t.Fatalf("bytes 期望 %d，实际 %d", len(md), raw.Bytes)
	}
	for _, want := range []string{"triggers: [探针, 原文]", "version: 9", "正文：保留全部 front-matter。"} {
		if !strings.Contains(raw.Content, want) {
			t.Errorf("原文接口丢了内容 %q；实际返回：%q", want, raw.Content)
		}
	}
}
