package conflicts

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenSessionIsIdempotent(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s1, err := st.OpenSession("groupA", "doc2.txt", "userA", []DocVersion{
		{DocID: "d1", OwnerID: "userA", OwnerName: "userA", Size: 10},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if s1.ID == "" || s1.Status != StatusOpen || s1.VersionN != 1 {
		t.Fatalf("会话字段不对：%+v", s1)
	}
	// 同一份文档再开一次：复用会话并把新版本补进去
	s2, err := st.OpenSession("groupA", "doc2.txt", "userB", []DocVersion{
		{DocID: "d2", OwnerID: "userB", OwnerName: "userB", Size: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID != s1.ID {
		t.Fatalf("同名文档应复用会话，实得 %s vs %s", s2.ID, s1.ID)
	}
	if s2.VersionN != 2 {
		t.Fatalf("应补进第 2 版，实得 %d", s2.VersionN)
	}
	// 另一个文档名 = 另一个会话
	s3, _ := st.OpenSession("groupA", "doc3.txt", "userA", nil)
	if s3.ID == s1.ID {
		t.Fatal("不同文档名不该共用会话")
	}
	// 别的组互不干扰
	s4, _ := st.OpenSession("groupB", "doc2.txt", "userC", nil)
	if s4.ID == s1.ID {
		t.Fatal("不同用户组不该共用会话")
	}
}

func TestGiveUpKeepsOtherSide(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	s, _ := st.OpenSession("g", "报告.docx", "A", []DocVersion{
		{DocID: "docA", OwnerID: "A"}, {DocID: "docB", OwnerID: "B"},
	})
	got, err := st.GiveUp(s.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusResolved || got.ResolvedDoc != "docB" {
		t.Fatalf("A 放弃后应保留 B 那版：%+v", got)
	}
	// 超过两版判不出对方是哪版 → 只标已放弃，不定稿
	s2, _ := st.OpenSession("g", "三版.txt", "A", []DocVersion{
		{DocID: "x", OwnerID: "A"}, {DocID: "y", OwnerID: "B"}, {DocID: "z", OwnerID: "C"},
	})
	got2, _ := st.GiveUp(s2.ID, "A")
	if got2.Status != StatusAbandoned || got2.ResolvedDoc != "" {
		t.Fatalf("三版应只标已放弃：%+v", got2)
	}
}

func TestResolveAndChatAndSignals(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	s, _ := st.OpenSession("g", "方案.md", "A", []DocVersion{
		{DocID: "docA", OwnerID: "A"}, {DocID: "docB", OwnerID: "B"},
	})

	if _, err := st.AddMessage(s.ID, "A", "A", "我改了预算那一段"); err != nil {
		t.Fatal(err)
	}
	m2, err := st.AddMessage(s.ID, "B", "B", "好，我看看")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMessage(s.ID, "A", "A", "   "); err == nil {
		t.Fatal("空消息应当拒绝")
	}
	all, err := st.Messages(s.ID, 0, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("应有 2 条消息，实得 %d / %v", len(all), err)
	}
	// 增量拉取
	inc, _ := st.Messages(s.ID, m2.ID, 0)
	if len(inc) != 0 {
		t.Fatalf("afterID=最后一条，不该再有增量：%+v", inc)
	}

	// 信令：A 发广播，B 发定向给 A
	if err := st.PostSignal(s.ID, "A", "", "offer", `{"sdp":"x"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.PostSignal(s.ID, "B", "A", "ice", `{"c":1}`); err != nil {
		t.Fatal(err)
	}
	if err := st.PostSignal(s.ID, "A", "", "", "x"); err == nil {
		t.Fatal("空 kind 应当拒绝")
	}
	got, err := st.Signals(s.ID, "A", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A 应当收到 B 定向发来的 ice（广播那条是 A 自己发的，要过滤掉）
	if len(got) != 1 || got[0].Kind != "ice" || got[0].From != "B" {
		t.Fatalf("A 应只收到 B 的 ice：%+v", got)
	}
	// B 应当收到 A 的广播 offer
	gotB, _ := st.Signals(s.ID, "B", 0, 0)
	if len(gotB) != 1 || gotB[0].Kind != "offer" {
		t.Fatalf("B 应收到 A 的 offer：%+v", gotB)
	}

	// 定稿
	rs, err := st.Resolve(s.ID, "docB", "A")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Status != StatusResolved || rs.ResolvedDoc != "docB" || rs.ResolvedBy != "A" {
		t.Fatalf("定稿结果不对：%+v", rs)
	}
	if _, err := st.Resolve(s.ID, "", "A"); err == nil {
		t.Fatal("空 docId 应当拒绝")
	}
	// 定稿之后不再出现在"仅看 open"的列表里
	open, _ := st.Sessions("g", true)
	if len(open) != 0 {
		t.Fatalf("已定稿不该还在 open 列表：%+v", open)
	}
	allsess, _ := st.Sessions("g", false)
	if len(allsess) != 1 || allsess[0].MessageN != 2 {
		t.Fatalf("全量列表应有 1 个会话且带消息数：%+v", allsess)
	}
}

func TestMissingSessionErrors(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	if _, err := st.Session("nope"); err == nil {
		t.Fatal("不存在的会话应当报错")
	}
	if _, err := st.AddMessage("nope", "A", "A", "hi"); err == nil {
		t.Fatal("往不存在的会话发消息应当报错")
	}
	if _, err := st.OpenSession("", "x", "A", nil); err == nil {
		t.Fatal("空 groupID 应当报错")
	}
}

// 「回复AI」确认闸：每一版的作者都确认了才放行；单人确认如实回"还差谁"。
func TestConfirmAI(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()

	s, _ := st.OpenSession("g", "方案.md", "A", []DocVersion{
		{DocID: "docA", OwnerID: "A"}, {DocID: "docB", OwnerID: "B"},
	})
	all, missing, err := st.ConfirmAI(s.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if all || len(missing) != 1 || missing[0] != "B" {
		t.Fatalf("A 确认后还差 B：all=%v missing=%v", all, missing)
	}
	all, missing, err = st.ConfirmAI(s.ID, "B")
	if err != nil {
		t.Fatal(err)
	}
	if !all || len(missing) != 0 {
		t.Fatalf("两人都确认了应当放行：all=%v missing=%v", all, missing)
	}
	// 幂等：同一人再点一次不变
	confirmers, err := st.AIConfirmers(s.ID)
	if err != nil || len(confirmers) != 2 {
		t.Fatalf("应记 2 个确认人，实得 %v / %v", confirmers, err)
	}
	if _, _, err = st.ConfirmAI(s.ID, "B"); err != nil {
		t.Fatal(err)
	}
	if confirmers, _ = st.AIConfirmers(s.ID); len(confirmers) != 2 {
		t.Fatalf("重复确认不该多记：%v", confirmers)
	}
}

// 作者缺失的老数据：任何人确认一次就放行（不能卡在补不回来的确认上）
func TestConfirmAINoOwners(t *testing.T) {
	st, _ := Open(t.TempDir())
	defer st.Close()
	s, _ := st.OpenSession("g", "老文档.txt", "A", []DocVersion{
		{DocID: "docA"}, {DocID: "docB"},
	})
	all, missing, err := st.ConfirmAI(s.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	if !all || len(missing) != 0 {
		t.Fatalf("没记作者的会话一人确认即放行：all=%v missing=%v", all, missing)
	}
}

// 状态读写 + 老库迁移：手工造一份没有 ai_status 列的老 schema，Open 后照常能用
func TestAIStatusAndOldSchemaMigration(t *testing.T) {
	dir := t.TempDir()
	// 老库：只建早期那几张表（sessions 里没有 ai_status / ai_note）
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "conflicts.db")))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY, group_id TEXT NOT NULL,
		name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'open',
		opened_by TEXT NOT NULL DEFAULT '', resolved_doc TEXT NOT NULL DEFAULT '',
		resolved_by TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`)
	_ = db.Close()

	st, err := Open(dir)
	if err != nil {
		t.Fatalf("老库应当能打开（迁移补列）：%v", err)
	}
	defer st.Close()
	s, err := st.OpenSession("g", "迁移.md", "A", []DocVersion{{DocID: "d1", OwnerID: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.AIStatus != "" || s.AINote != "" {
		t.Fatalf("新列默认值应为空：%+v", s)
	}
	got, err := st.SetAIStatus(s.ID, "running", "")
	if err != nil || got.AIStatus != "running" {
		t.Fatalf("写状态失败：%+v / %v", got, err)
	}
	got, err = st.SetAIStatus(s.ID, "done", "AI 判定：保留 B 版")
	if err != nil || got.AIStatus != "done" || got.AINote == "" {
		t.Fatalf("完成态不对：%+v / %v", got, err)
	}
}
