package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/* ---------- JSON 往返：字段一个都不能丢 ---------- */

func TestTaskJSONRoundTripKeepsEveryField(t *testing.T) {
	raw := []byte(`{
	  "id":"x1","title":"写方案","category":"创作","priority":"high","form":"schedule",
	  "due":"2026-09-26T14:00","weekly":true,"estimate":45,"deps":["a","b"],
	  "atts":[{"id":"a1","kind":"image","name":"p.jpg","mime":"image/jpeg","size":123,"path":"/x/p.jpg","thumb":"data:image/jpeg;base64,AAA"}],
	  "note":"备注","owner":"agent","status":"doing","remind":true,"auth":"pending",
	  "createdAt":1758800000000,"legacyField":{"x":1},"extraArr":[1,2]
	}`)
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if task.Owner != OwnerAgent || task.Auth != AuthPending || task.Estimate != 45 || !task.Weekly || !task.Remind {
		t.Fatalf("基础字段解析不对：%+v", task)
	}
	if len(task.Deps) != 2 || len(task.Atts) != 1 || task.Atts[0].Thumb == "" {
		t.Fatalf("依赖/附件解析不对：%+v", task)
	}

	out, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	for _, key := range []string{
		"id", "title", "category", "priority", "form", "due", "weekly", "estimate",
		"deps", "atts", "note", "owner", "status", "remind", "auth", "createdAt",
		"legacyField", "extraArr", // 后面两个是 Go 没建模的字段，必须原样保留
	} {
		if _, ok := back[key]; !ok {
			t.Fatalf("往返后丢了字段 %q：%s", key, string(out))
		}
	}
	if back["createdAt"].(float64) != 1758800000000 {
		t.Fatalf("createdAt 单位被改了（应保持毫秒）：%v", back["createdAt"])
	}

	// 再往返一次，保证稳定（不因为 Extra 反复变形）
	var again Task
	if err := json.Unmarshal(out, &again); err != nil {
		t.Fatalf("二次解析失败：%v", err)
	}
	out2, _ := json.Marshal(again)
	if string(out) != string(out2) {
		t.Fatalf("二次往返结果不稳定：\n%s\n%s", string(out), string(out2))
	}
}

func TestPassAndDocJSONRoundTrip(t *testing.T) {
	raw := []byte(`{
	  "todos":[], "vault":[{"id":"p1","title":"Trae","account":"a@b.com","group":"Trae",
	    "password":"now","history":[{"pwd":"old","at":1758800000000}],"url":"https://x","note":"n",
	    "source":"md","createdAt":1,"updatedAt":2,"vendorExt":"keep-me"}],
	  "schemaVersion":3
	}`)
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(doc.Vault) != 1 || doc.Vault[0].History[0].Pwd != "old" {
		t.Fatalf("密码条目解析不对：%+v", doc.Vault)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if _, ok := back["schemaVersion"]; !ok {
		t.Fatalf("文档级未知字段丢了：%s", string(out))
	}
	vault := back["vault"].([]any)[0].(map[string]any)
	if _, ok := vault["vendorExt"]; !ok {
		t.Fatalf("密码条目里的未知字段丢了：%s", string(out))
	}
}

func TestTaskNormalizeDefaults(t *testing.T) {
	var t2 Task
	if !t2.Normalize() {
		t.Fatal("空待办应被补齐默认值")
	}
	if t2.Category != "日常生活" || t2.Priority != PriorityMid || t2.Owner != OwnerUser || t2.Status != StatusTodo {
		t.Fatalf("默认值不对：%+v", t2)
	}
	if t2.Deps == nil || t2.Atts == nil {
		t.Fatal("deps/atts 应为空数组而不是 null（与 JS 一致）")
	}
	if t2.ID == "" {
		t.Fatal("应补出 id")
	}
}

/* ---------- 密码本合并规则（对照 JS mergePassword） ---------- */

func TestMergePassNewRecord(t *testing.T) {
	list := []Pass{}
	item := Pass{
		Title: "Trae 国际站", Account: "me@a.com", Password: "now", Group: "Trae",
		History: []PassHistory{{Pwd: "old", At: 1758800000000}, {Pwd: "now"}, {Pwd: ""}},
	}
	res, idx := MergePass(&list, item)
	if res != "new" || idx != 0 || len(list) != 1 {
		t.Fatalf("应新增一条：res=%s idx=%d len=%d", res, idx, len(list))
	}
	got := list[0]
	if got.Source != SourceManual || got.ID == "" || got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("新增记录缺少默认字段：%+v", got)
	}
	// 与当前密码重复的、空的历史都应被清掉
	if len(got.History) != 1 || got.History[0].Pwd != "old" {
		t.Fatalf("历史密码应只留 old：%+v", got.History)
	}
}

func TestMergePassUpdatesPasswordAndHistory(t *testing.T) {
	list := []Pass{{ID: "p1", Title: "GitHub", Account: "a@b.com", Password: "v1", CreatedAt: 1, UpdatedAt: 1}}

	res, idx := MergePass(&list, Pass{Title: "GitHub", Account: "a@b.com", Password: "v2", Group: "GH", URL: "https://github.com", Note: "备注"})
	if res != "updated" || idx != 0 {
		t.Fatalf("应判为改密码：res=%s idx=%d", res, idx)
	}
	got := list[0]
	if got.Password != "v2" || len(got.History) != 1 || got.History[0].Pwd != "v1" {
		t.Fatalf("旧密码应沉到历史：%+v", got)
	}
	if got.Group != "GH" || got.URL != "https://github.com" || got.Note != "备注" {
		t.Fatalf("归属/URL/备注应被补上：%+v", got)
	}
	if got.UpdatedAt <= 1 {
		t.Fatal("updatedAt 应刷新")
	}

	// 同一个密码再来一次 → same，历史不变
	res, _ = MergePass(&list, Pass{Title: "GitHub", Account: "a@b.com", Password: "v2"})
	if res != "same" || len(list[0].History) != 1 {
		t.Fatalf("密码相同应判 same 且不动历史：res=%s %+v", res, list[0])
	}

	// 归属只补不覆盖
	res, _ = MergePass(&list, Pass{Title: "GitHub", Account: "a@b.com", Group: "别的"})
	if res != "same" || list[0].Group != "GH" {
		t.Fatalf("归属不该被覆盖：%+v", list[0])
	}

	// 再来一次 v3，历史应有两条且按时间倒序
	res, _ = MergePass(&list, Pass{Title: "GitHub", Account: "a@b.com", Password: "v3"})
	if res != "updated" {
		t.Fatalf("应为 updated：%s", res)
	}
	h := list[0].History
	if len(h) != 2 || h[0].Pwd != "v2" || h[1].Pwd != "v1" {
		t.Fatalf("历史应为 [v2,v1] 倒序：%+v", h)
	}
	if h[0].At < h[1].At {
		t.Fatalf("历史应按时间倒序：%+v", h)
	}
}

func TestMergePassKeepsMultipleAccountsSameSite(t *testing.T) {
	list := []Pass{}
	MergePass(&list, Pass{Title: "Trae", Account: "a@x.com", Password: "p1", Group: "Trae"})
	MergePass(&list, Pass{Title: "Trae", Account: "b@x.com", Password: "p2", Group: "Trae"})
	if len(list) != 2 {
		t.Fatalf("同平台不同账号应是两条：%d", len(list))
	}
	// 同平台同账号才是同一条
	MergePass(&list, Pass{Title: "Trae", Account: "a@x.com", Password: "p9"})
	if len(list) != 2 || list[0].Password != "p9" {
		t.Fatalf("同平台同账号应合并进原来那条：%+v", list)
	}
}

func TestNormalizePassPromotesHistory(t *testing.T) {
	p := Pass{Title: "X", Account: "u", History: []PassHistory{{Pwd: "recent", At: 2}, {Pwd: "older", At: 1}}}
	if !normalizePass(&p) {
		t.Fatal("应发生改动（当前密码为空 → 提升历史）")
	}
	if p.Password != "recent" {
		t.Fatalf("应把最近的历史提上来当当前密码：%+v", p)
	}
	if len(p.History) != 1 || p.History[0].Pwd != "older" {
		t.Fatalf("剩余历史不该变：%+v", p.History)
	}
}

func TestUseHistorySwapsCurrent(t *testing.T) {
	doc := NewDoc()
	doc.Vault = []Pass{{ID: "p1", Title: "X", Account: "u", Password: "cur",
		History: []PassHistory{{Pwd: "h1", At: 100}, {Pwd: "h0", At: 50}}}}
	if err := doc.UseHistory("p1", 1); err != nil {
		t.Fatalf("换回历史失败：%v", err)
	}
	got := doc.Vault[0]
	if got.Password != "h0" {
		t.Fatalf("当前密码应为 h0：%+v", got)
	}
	if len(got.History) != 2 || got.History[0].Pwd != "cur" {
		t.Fatalf("原当前密码应沉到历史最前：%+v", got.History)
	}
	if err := doc.UseHistory("p1", 9); err == nil {
		t.Fatal("越界应报错")
	}
}

func TestFindPassByVariousRefs(t *testing.T) {
	doc := NewDoc()
	doc.Vault = []Pass{
		{ID: "p1", Title: "Trae", Account: "a@x.com", Password: "1"},
		{ID: "p2", Title: "Trae", Account: "b@x.com", Password: "2"},
		{ID: "p3", Title: "GitHub", Account: "c@x.com", Password: "3"},
	}
	if i, err := doc.FindPass("1"); err != nil || i != 0 {
		t.Fatalf("按序号定位失败：i=%d err=%v", i, err)
	}
	if i, err := doc.FindPass("Trae/b@x.com"); err != nil || i != 1 {
		t.Fatalf("按平台/账号定位失败：i=%d err=%v", i, err)
	}
	if _, err := doc.FindPass("Trae"); err == nil {
		t.Fatal("同名多账号应提示用「平台/账号」指定")
	}
	if i, err := doc.FindPass("GitHub"); err != nil || i != 2 {
		t.Fatalf("同名唯一时应能直接定位：i=%d err=%v", i, err)
	}
	if i, err := doc.FindPass("p3"); err != nil || i != 2 {
		t.Fatalf("按 id 定位失败：i=%d err=%v", i, err)
	}
}

func TestGenStrongPassword(t *testing.T) {
	pw, err := GenStrongPassword(16)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if len([]rune(pw)) != 16 {
		t.Fatalf("长度不对：%q", pw)
	}
	for _, set := range []string{pwUpper, pwLower, pwDigit, pwSym} {
		if !strings.ContainsAny(pw, set) {
			t.Fatalf("缺少字符集 %q：%q", set, pw)
		}
	}
	if strings.ContainsAny(pw, "0O1lI") {
		t.Fatalf("不该出现易混字符：%q", pw)
	}
	other, _ := GenStrongPassword(16)
	if pw == other {
		t.Fatal("两次生成不该相同")
	}
}

/* ---------- 待办操作 ---------- */

func TestAddTaskAndDefaults(t *testing.T) {
	doc := NewDoc()
	got := doc.AddTask(NewTask("写周报", FormSchedule, OwnerUser))
	if len(doc.Todos) != 1 || doc.Todos[0].ID != got.ID {
		t.Fatalf("应插到最前：%+v", doc.Todos)
	}
	if got.Category != "日常生活" || got.Priority != PriorityMid || got.Status != StatusTodo {
		t.Fatalf("默认值不对：%+v", got)
	}
	if got.Due == "" {
		t.Fatal("日程排期应带默认截止时间（一小时后）")
	}
	leisure := doc.AddTask(NewTask("看两章书", FormLeisure, OwnerAgent))
	if leisure.Due != "" || leisure.Form != FormLeisure || leisure.Owner != OwnerAgent {
		t.Fatalf("闲暇待办不该有截止时间：%+v", leisure)
	}
	if len(doc.Todos) != 2 || doc.Todos[0].ID != leisure.ID {
		t.Fatal("新任务应排在最前")
	}
}

func TestVisibleOrderAndSort(t *testing.T) {
	doc := NewDoc()
	doc.Todos = []Task{
		{ID: "c", Title: "C", Due: "2026-09-26T18:00", Status: StatusTodo, CreatedAt: 3},
		{ID: "a", Title: "A", Due: "2026-09-26T09:00", Status: StatusTodo, CreatedAt: 1},
		{ID: "d", Title: "D", Due: "", Status: StatusTodo, CreatedAt: 4},
		{ID: "b", Title: "B", Due: "2026-09-26T09:00", Status: StatusDone, CreatedAt: 2},
	}
	ord := VisibleOrder(doc.Todos, false)
	if len(ord) != 3 {
		t.Fatalf("未收尾项应为 3，实际 %d", len(ord))
	}
	if doc.Todos[ord[0]].ID != "a" || doc.Todos[ord[len(ord)-1]].ID != "d" {
		t.Fatalf("排序应为 a→c→d（无截止排最后）：%+v", ord)
	}

	priority := []Task{
		{ID: "1", Priority: PriorityLow, Due: "2026-01-01T00:00"},
		{ID: "2", Priority: PriorityHigh, Due: "2026-02-01T00:00"},
		{ID: "3", Priority: PriorityMid, Due: ""},
	}
	SortTasks(priority, SortByPriority)
	if priority[0].ID != "2" || priority[1].ID != "3" || priority[2].ID != "1" {
		t.Fatalf("优先级排序应为 high→mid→low：%+v", priority)
	}

	byTime := []Task{{ID: "x", Due: ""}, {ID: "y", Due: "2026-01-01T00:00"}}
	SortTasks(byTime, SortByTime)
	if byTime[0].ID != "y" || byTime[1].ID != "x" {
		t.Fatalf("时间排序应把无截止的排最后：%+v", byTime)
	}
}

func TestFindTaskAndStatusOps(t *testing.T) {
	doc := NewDoc()
	t1 := doc.AddTask(NewTask("甲", FormLeisure, OwnerUser))
	t2 := doc.AddTask(NewTask("乙", FormLeisure, OwnerUser))
	if i, err := doc.FindTask("1"); err != nil || doc.Todos[i].ID != t2.ID {
		t.Fatalf("序号 1 应为最后添加的那条：i=%d err=%v", i, err)
	}
	// 同一毫秒内生成的 id 前 6 位相同（与 JS 的 id 规则一致），
	// 所以前缀匹配只能保证「唯一时才命中」，有歧义必须明确报错
	sharedPrefix := commonPrefix(t1.ID, t2.ID)
	if len(sharedPrefix) >= 6 {
		if _, err := doc.FindTask(sharedPrefix); err == nil {
			t.Fatalf("前缀 %q 有歧义，应报错让人写全 id", sharedPrefix)
		}
	}
	if i, err := doc.FindTask(t1.ID); err != nil || doc.Todos[i].ID != t1.ID {
		t.Fatalf("完整 id 定位失败：i=%d err=%v", i, err)
	}
	if i, err := doc.FindTask(t1.ID[:12]); err != nil || doc.Todos[i].ID != t1.ID {
		t.Fatalf("带随机位的唯一前缀定位失败：i=%d err=%v", i, err)
	}
	if _, err := doc.FindTask("不存在"); err == nil {
		t.Fatal("找不到时应报错")
	}
	if st, err := doc.ToggleTaskDone(t1.ID); err != nil || st != StatusDone {
		t.Fatalf("标记完成失败：%s %v", st, err)
	}
	if st, _ := doc.ToggleTaskDone(t1.ID); st != StatusTodo {
		t.Fatalf("恢复待办失败：%s", st)
	}
	if err := doc.ConvertForm(t1.ID, FormSchedule); err != nil {
		t.Fatalf("转日程失败：%v", err)
	}
	if doc.Todos[doc.taskIndex(t1.ID)].Due == "" {
		t.Fatal("转日程应补默认截止时间")
	}
	if err := doc.ConvertForm(t1.ID, FormLeisure); err != nil {
		t.Fatalf("转闲暇失败：%v", err)
	}
	if doc.Todos[doc.taskIndex(t1.ID)].Due != "" {
		t.Fatal("转闲暇应清空截止时间")
	}
	if err := doc.RemoveTask(t1.ID); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if len(doc.Todos) != 1 {
		t.Fatalf("删除后应剩 1 条：%d", len(doc.Todos))
	}
	if err := doc.RemoveTask("ghost"); err == nil {
		t.Fatal("删除不存在的待办应报错")
	}
}

func TestTaskStatsAndGroupByDay(t *testing.T) {
	tasks := []Task{
		{ID: "1", Owner: OwnerUser, Form: FormSchedule, Due: "2026-09-26T09:00", Status: StatusTodo},
		{ID: "2", Owner: OwnerUser, Form: FormLeisure, Status: StatusTodo},
		{ID: "3", Owner: OwnerUser, Form: FormSchedule, Due: "2026-09-27T09:00", Status: StatusTodo},
		{ID: "4", Owner: OwnerUser, Status: StatusDone},
		{ID: "5", Owner: OwnerAgent, Status: StatusTodo},
	}
	sched, leisure := TaskStats(tasks, OwnerUser)
	if sched != 2 || leisure != 1 {
		t.Fatalf("统计不对：sched=%d leisure=%d", sched, leisure)
	}
	// 日程视图只展示未收尾的日程排期项
	open := []Task{}
	for _, item := range FilterTasks(tasks, OwnerUser, FormSchedule) {
		if !item.IsClosed() {
			open = append(open, item)
		}
	}
	groups := GroupByDay(open, SortByTime)
	if len(groups) != 2 || groups[0].Key != "2026-09-26" || groups[1].Key != "2026-09-27" {
		t.Fatalf("按天分组不对：%+v", groups)
	}
	withNone := GroupByDay([]Task{{ID: "x", Due: ""}, {ID: "y", Due: "2026-09-26T09:00"}}, SortByTime)
	if withNone[len(withNone)-1].Key != "" {
		t.Fatalf("未排期应排最后：%+v", withNone)
	}
	if len(FilterTasks(tasks, OwnerAgent, "")) != 1 {
		t.Fatal("按属主筛选失败（双源）")
	}
}

// commonPrefix 两个字符串的公共前缀
func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

/* ---------- Markdown 导入导出 ---------- */

func sampleDoc() *Doc {
	doc := NewDoc()
	doc.Todos = []Task{
		{ID: "t1", Title: "整理本周工作计划", Category: "工作开发", Priority: PriorityHigh,
			Form: FormSchedule, Due: "2026-09-26T14:00", Note: "示例", Owner: OwnerUser, Status: StatusTodo, CreatedAt: 1},
		{ID: "t2", Title: "AI 漫剧大纲", Category: "创作", Priority: PriorityMid,
			Form: FormLeisure, Owner: OwnerAgent, Status: StatusTodo, CreatedAt: 2, Deps: []string{"t1"}, Atts: []Attachment{}},
	}
	historyAt := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local).UnixMilli()
	doc.Vault = []Pass{
		{ID: "p1", Title: "GitHub", Account: "user@example.com", Group: "Trae", Password: "abc123",
			History: []PassHistory{{Pwd: "old123", At: historyAt}}, URL: "https://github.com",
			Note: "初始示例", CreatedAt: 1, UpdatedAt: 1},
		{ID: "p2", Title: "无历史", Account: "u2", Password: "p2", CreatedAt: 1, UpdatedAt: 1},
	}
	doc.Normalize()
	return doc
}

func TestExportMarkdownMatchesAppFormat(t *testing.T) {
	doc := sampleDoc()
	md := ExportMarkdown(doc, time.Date(2026, 9, 26, 10, 0, 0, 0, time.Local))
	want := []string{
		"# 待办中心导出（2026-09-26）",
		"## 待办",
		"- [ ] 整理本周工作计划 ｜ 类别: 工作开发 ｜ 优先级: 高优 ｜ 截止: 2026-09-26 14:00 ｜ 备注: 示例",
		"- [ ] AI 漫剧大纲（Agent） ｜ 类别: 创作 ｜ 优先级: 中 ｜ 依赖: 1项",
		"## 密码",
		"### GitHub",
		"- 账号: user@example.com",
		"- 归属: Trae",
		"- 密码: abc123",
		"- 历史密码: old123 ｜ 2026-09-20 10:00",
		"- URL: https://github.com",
		"- 备注: 初始示例",
		"### 无历史",
	}
	for _, line := range want {
		if !strings.Contains(md, line) {
			t.Fatalf("导出缺少这一行：%q\n实际：\n%s", line, md)
		}
	}
	if !strings.Contains(md, "\n\n## 密码\n") {
		t.Fatalf("待办段与密码段之间应有空行：\n%s", md)
	}
}

func TestMarkdownRoundTrip(t *testing.T) {
	doc := sampleDoc()
	md := ExportMarkdown(doc, time.Now())
	todos, passes := ParseMarkdown(md)
	if len(todos) != 2 || len(passes) != 2 {
		t.Fatalf("应解析出 2 待办 2 密码，实际 %d/%d\n%s", len(todos), len(passes), md)
	}
	var agent *Task
	for i := range todos {
		if todos[i].Owner == OwnerAgent {
			agent = &todos[i]
		}
	}
	if agent == nil {
		t.Fatalf("（Agent）标记应被识别为 Agent 待办：%+v", todos)
	}
	if agent.Title != "AI 漫剧大纲" || agent.Form != FormLeisure || agent.Category != "创作" {
		t.Fatalf("Agent 待办解析不对：%+v", agent)
	}
	t1 := todos[0]
	if t1.Title != "整理本周工作计划" || t1.Due != "2026-09-26T14:00" || t1.Priority != PriorityHigh || t1.Note != "示例" {
		t.Fatalf("待办字段解析不对：%+v", t1)
	}
	if passes[0].Title != "GitHub" || passes[0].Account != "user@example.com" || passes[0].Group != "Trae" ||
		passes[0].URL != "https://github.com" || passes[0].Note != "初始示例" {
		t.Fatalf("密码字段解析不对：%+v", passes[0])
	}
	if len(passes[0].History) != 1 || passes[0].History[0].Pwd != "old123" {
		t.Fatalf("历史密码解析不对：%+v", passes[0].History)
	}
}

func TestImportMarkdownDedupesAndMerges(t *testing.T) {
	doc := sampleDoc()
	md := ExportMarkdown(doc, time.Now())

	res := doc.ImportMarkdown(md)
	if res.TodoAdded != 0 || res.TodoSkipped != 2 {
		t.Fatalf("重复导入不该新增待办：%+v", res)
	}
	if res.PassAdded != 0 || res.PassMerged != 2 {
		t.Fatalf("重复导入应合并密码：%+v", res)
	}
	if len(doc.Vault) != 2 || len(doc.Todos) != 2 {
		t.Fatalf("重复导入后条数应不变：%d/%d", len(doc.Todos), len(doc.Vault))
	}
	if len(doc.Vault[0].History) != 1 || doc.Vault[0].History[0].Pwd != "old123" {
		t.Fatalf("重复导入不该把历史密码翻倍：%+v", doc.Vault[0].History)
	}

	// 新内容才新增
	fresh := "# 待办中心导出（2026-09-26）\n\n## 待办\n- [ ] 新任务 ｜ 类别: 创作 ｜ 优先级: 低\n\n## 密码\n### 新站\n- 账号: a\n- 密码: b\n"
	res = doc.ImportMarkdown(fresh)
	if res.TodoAdded != 1 || res.PassAdded != 1 {
		t.Fatalf("新内容应各新增 1 条：%+v", res)
	}
	if doc.Todos[0].Title != "新任务" {
		t.Fatalf("导入的待办应排在最前：%+v", doc.Todos[0])
	}
	if doc.Todos[0].Priority != PriorityLow || doc.Todos[0].Form != FormLeisure {
		t.Fatalf("待办字段不对：%+v", doc.Todos[0])
	}
}

func TestParseMarkdownAcceptsCLIStyleAndVaultOnly(t *testing.T) {
	// 密码本单独导出（App 的「导出密码本」）
	doc := sampleDoc()
	vaultMD := ExportVaultMarkdown(doc, time.Now())
	if !strings.HasPrefix(vaultMD, "# 密码本导出（") {
		t.Fatalf("密码本导出标题不对：%s", vaultMD)
	}
	todos, passes := ParseMarkdown(vaultMD)
	if len(todos) != 0 || len(passes) != 2 {
		t.Fatalf("密码本导出应只解析出密码：%d/%d", len(todos), len(passes))
	}

	// 电脑侧 CLI 的老格式（无（Agent）标记、无依赖字段、优先级 高优/中/低）
	cliMD := "# 待办中心导出（2026-09-25）\n\n## 待办\n- [x] 老格式待办 ｜ 类别: 工作开发 ｜ 优先级: 中 ｜ 截止: 2026-09-25 14:00\n\n## 密码\n### 老站\n- 账号: u\n- 密码: p\n- 历史密码: h ｜ 2026-09-20 10:00\n"
	todos, passes = ParseMarkdown(cliMD)
	if len(todos) != 1 || len(passes) != 1 {
		t.Fatalf("CLI 格式应能解析：%d/%d", len(todos), len(passes))
	}
	if todos[0].Status != StatusDone || todos[0].Due != "2026-09-25T14:00" || todos[0].Priority != PriorityMid {
		t.Fatalf("CLI 格式解析不对：%+v", todos[0])
	}
	if len(passes[0].History) != 1 || passes[0].History[0].At == 0 {
		t.Fatalf("历史密码时间应被解析：%+v", passes[0].History)
	}
}

/* ---------- 存储 ---------- */

func TestStoreRoundTripKeepsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	st := Open(dir)

	doc, err := st.Load()
	if err != nil {
		t.Fatalf("空目录应返回空文档：%v", err)
	}
	if len(doc.Todos) != 0 || len(doc.Vault) != 0 {
		t.Fatalf("空文档应为空：%+v", doc)
	}

	doc.Todos = append(doc.Todos, Task{ID: "t1", Title: "甲", Extra: map[string]json.RawMessage{"custom": json.RawMessage(`"keep"`)}})
	doc.Vault = append(doc.Vault, Pass{ID: "p1", Title: "站点", Account: "u", Password: "p"})
	if err := st.Save(doc); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	back, err := st.Load()
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(back.Todos) != 1 || back.Todos[0].Title != "甲" || len(back.Vault) != 1 {
		t.Fatalf("读回内容不对：%+v", back)
	}
	if string(back.Todos[0].Extra["custom"]) != `"keep"` {
		t.Fatalf("未知字段应保留：%+v", back.Todos[0].Extra)
	}
	// 规范化补齐的默认值也应落盘
	if back.Vault[0].Source != SourceManual || back.Vault[0].History == nil {
		t.Fatalf("保存时应补默认字段：%+v", back.Vault[0])
	}

	// 损坏的文件要明确报错
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil {
		t.Fatal("损坏的数据文件应报错，而不是静默当空数据")
	}
}

func TestStoreSettings(t *testing.T) {
	dir := t.TempDir()
	st := Open(dir)
	def, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("默认设置读取失败：%v", err)
	}
	if def.AI.Protocol != "openai" || def.AI.Model != "deepseek-chat" || !def.Remind {
		t.Fatalf("默认设置不对：%+v", def)
	}
	def.AI.APIKey = "sk-test"
	def.Notify = true
	def.Extra = map[string]json.RawMessage{"theme": json.RawMessage(`"dark"`)}
	if err := st.SaveSettings(def); err != nil {
		t.Fatalf("保存设置失败：%v", err)
	}
	back, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("读取设置失败：%v", err)
	}
	if back.AI.APIKey != "sk-test" || !back.Notify {
		t.Fatalf("设置内容不对：%+v", back)
	}
	if string(back.Extra["theme"]) != `"dark"` {
		t.Fatalf("未知设置字段应保留：%+v", back.Extra)
	}
}

func TestImportJSONShapes(t *testing.T) {
	full := []byte(`{"todos":[{"title":"甲"}],"vault":[{"title":"站","account":"u"}]}`)
	doc, err := ImportJSON(full)
	if err != nil || len(doc.Todos) != 1 || len(doc.Vault) != 1 {
		t.Fatalf("完整文档导入失败：%v %+v", err, doc)
	}
	todosOnly := []byte(`[{"title":"乙","due":"2026-09-26T09:00"}]`)
	doc, err = ImportJSON(todosOnly)
	if err != nil || len(doc.Todos) != 1 || doc.Todos[0].Title != "乙" {
		t.Fatalf("待办数组导入失败：%v %+v", err, doc)
	}
	vaultOnly := []byte(`[{"title":"站","account":"u","password":"p"}]`)
	doc, err = ImportJSON(vaultOnly)
	if err != nil || len(doc.Vault) != 1 {
		t.Fatalf("密码数组导入失败：%v %+v", err, doc)
	}
	if _, err := ImportJSON([]byte("   ")); err == nil {
		t.Fatal("空内容应报错")
	}
	if _, err := ImportJSON([]byte("不是 json")); err == nil {
		t.Fatal("非法内容应报错")
	}
}
