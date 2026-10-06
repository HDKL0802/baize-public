package memory

import (
	"context"
	"testing"
)

func TestParseWikiLinks(t *testing.T) {
	content := `# 标题
这是 [[加密方案]] 和 [[密码本|密码]]，还有带小标题的 [[部署#服务器]]。
重复一次 [[加密方案]]。嵌入 ![[图床截图]]。没闭合的 [[忽略我
`
	got := ParseWikiLinks(content)
	want := []string{"加密方案", "密码本", "部署", "图床截图"}
	if len(got) != len(want) {
		t.Fatalf("解析出 %d 个链接，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个链接 %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestParseTags(t *testing.T) {
	content := `# 这是一个标题，不是标签
正文里有 #工作 和 #项目/白泽 两个标签。
井号后面直接跟数字不算 #123，颜色 #ff0000 也不算。
再来一个中文标签 #读书笔记。
`
	got := ParseTags(content)
	has := func(tag string) bool {
		for _, g := range got {
			if g == tag {
				return true
			}
		}
		return false
	}
	if !has("工作") {
		t.Fatalf("应识别 #工作：%v", got)
	}
	if !has("项目/白泽") {
		t.Fatalf("应识别 #项目/白泽：%v", got)
	}
	if !has("读书笔记") {
		t.Fatalf("应识别 #读书笔记：%v", got)
	}
	// 标题的 # 后面是空格，不该算
	for _, g := range got {
		if g == "这是一个标题，不是标签" {
			t.Fatalf("Markdown 标题被误判成标签：%v", got)
		}
	}
}

func TestWriteNoteAndLinks(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("打开记忆库失败：%v", err)
	}
	defer st.Close()

	ctx := context.Background()
	// 先写被引用的目标
	if _, err := st.WriteNote(ctx, WriteNoteOptions{
		Path: "加密方案.md", Content: "# 加密方案\n用 PBKDF2 + AES-GCM。\n#安全",
	}); err != nil {
		t.Fatalf("写目标笔记失败：%v", err)
	}
	// 再写引用它的笔记（写的时候目标已存在 → 应立刻解析到）
	n, err := st.WriteNote(ctx, WriteNoteOptions{
		Path: "密码本.md", Content: "# 密码本\n实现细节见 [[加密方案]]，另有 #安全。",
	})
	if err != nil {
		t.Fatalf("写来源笔记失败：%v", err)
	}
	if len(n.Links) != 1 || n.Links[0] != "加密方案" {
		t.Fatalf("出链解析不对：%v", n.Links)
	}

	// 反向链接：加密方案 应该看到 密码本 链过来
	bl, err := st.Backlinks(n.Namespace, NoteKey(n.Namespace+"|加密方案.md"))
	if err != nil {
		t.Fatalf("取反向链接失败：%v", err)
	}
	if len(bl) != 1 || bl[0].Path != "密码本.md" {
		t.Fatalf("反向链接不对：%+v", bl)
	}

	// 标签
	tags, err := st.Tags(n.Namespace)
	if err != nil {
		t.Fatalf("取标签失败：%v", err)
	}
	if len(tags) != 1 || tags[0].Tag != "安全" || tags[0].Count != 2 {
		t.Fatalf("标签统计不对：%+v", tags)
	}

	// 图：2 点 1 显式边
	g, err := st.Graph(GraphOptions{Namespace: n.Namespace})
	if err != nil {
		t.Fatalf("取图失败：%v", err)
	}
	if len(g.Nodes) != 2 || g.ExplicitEdges != 1 {
		t.Fatalf("图不对：nodes=%d explicit=%d", len(g.Nodes), g.ExplicitEdges)
	}
}

func TestNoteKeyStableAcrossWrites(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	ctx := context.Background()

	a, err := st.WriteNote(ctx, WriteNoteOptions{Path: "同一篇.md", Content: "第一版内容"})
	if err != nil {
		t.Fatalf("写第一版失败：%v", err)
	}
	b, err := st.WriteNote(ctx, WriteNoteOptions{Path: "同一篇.md", Content: "第二版内容，改过了"})
	if err != nil {
		t.Fatalf("写第二版失败：%v", err)
	}
	if a.DocKey != b.DocKey {
		t.Fatalf("同一路径应得到同一把键：%s vs %s", a.DocKey, b.DocKey)
	}
	list, err := st.ListNotes("default", "", "", 50, 0)
	if err != nil {
		t.Fatalf("列笔记失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("同键覆盖后应只剩 1 条，实际 %d", len(list))
	}
	if list[0].Content != "第二版内容，改过了" {
		t.Fatalf("内容应被覆盖：%q", list[0].Content)
	}
}

func TestResolveLinksAfterImport(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	ctx := context.Background()
	ns := "vault"

	// 先写「引用方」，此时目标还不存在 → 悬空
	if _, err := st.WriteNote(ctx, WriteNoteOptions{
		Namespace: ns, Path: "来源.md", Content: "看 [[后来的目标]]。",
	}); err != nil {
		t.Fatalf("写来源失败：%v", err)
	}
	out, err := st.Outlinks(ns, NoteKey(ns+"|来源.md"))
	if err != nil {
		t.Fatalf("取出链失败：%v", err)
	}
	if len(out) != 1 || !out[0].Missing {
		t.Fatalf("链接应暂时悬空：%+v", out)
	}
	// 目标笔记以「不触发解析」的方式出现（模拟外部直接灌库），此时链接仍悬空
	insertRawNote(t, st, ns, "后来的目标.md", "后来的目标", "我就是目标。")
	out2, err := st.Outlinks(ns, NoteKey(ns+"|来源.md"))
	if err != nil {
		t.Fatalf("取出链失败：%v", err)
	}
	if len(out2) != 1 || !out2[0].Missing {
		t.Fatalf("这次仍应是悬空的：%+v", out2)
	}
	// ResolveLinks 应把它接上
	n, err := st.ResolveLinks(ns)
	if err != nil {
		t.Fatalf("解析链接失败：%v", err)
	}
	if n < 1 {
		t.Fatalf("应至少解析到 1 条链接，实际 %d", n)
	}
	// 正常 WriteNote 路径则应当「写完即解析」（这是导入 Obsidian 库时的常态）
	if _, err := st.WriteNote(ctx, WriteNoteOptions{
		Namespace: ns, Path: "第三篇.md", Content: "我也链 [[后来的目标]]。",
	}); err != nil {
		t.Fatalf("写第三篇失败：%v", err)
	}
	bl, err := st.Backlinks(ns, NoteKey(ns+"|后来的目标.md"))
	if err != nil {
		t.Fatalf("取反向链接失败：%v", err)
	}
	paths := map[string]bool{}
	for _, b := range bl {
		paths[b.Path] = true
	}
	if len(bl) != 2 || !paths["来源.md"] || !paths["第三篇.md"] {
		t.Fatalf("解析后反向链接不对：%+v", bl)
	}
}

// insertRawNote 直接往库里塞一条笔记，绕过 WriteNote 的链接解析，
// 用来构造「悬空链接」这类只靠正常写入路径不容易造出来的状态。
func insertRawNote(t *testing.T, st *Store, ns, relPath, title, content string) {
	t.Helper()
	key := NoteKey(ns + "|" + relPath)
	now := int64(1)
	if _, err := st.db.Exec(`INSERT INTO chunks(namespace,category,doc_key,source,source_ref,kind,title,content,tokens,
		importance,recency,richness,score,hits,node_id,created_at,updated_at,meta,embedding,embedding_model)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,?,?)`,
		ns, CategoryCore, key, SourceNote, relPath, KindNote, title, content,
		EstimateTokens(content), 0.5, 0.5, 0.5, 0.5, now, now, "{}", nil, ""); err != nil {
		t.Fatalf("插入原始笔记失败：%v", err)
	}
}

func TestAutoLink(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	ctx := context.Background()
	ns := "auto"
	st.SetEmbedder(&fakeEmbedder{dim: 128})

	notes := []NoteFile{
		{Path: "1.md", Content: "Go 语言 并发 goroutine channel 调度器 内存模型"},
		{Path: "2.md", Content: "Go 语言 并发 goroutine channel 调度 与运行时"},
		{Path: "3.md", Content: "烘焙 面包 酵母 发酵 烤箱 配方"},
	}
	if _, err := st.ImportNotes(ctx, ns, notes); err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	res, err := st.AutoLink(ctx, AutoLinkOptions{Namespace: ns, TopK: 2, MinSim: 0.3})
	if err != nil {
		t.Fatalf("自动连边失败：%v", err)
	}
	if !res.VectorUsed {
		t.Fatalf("应使用向量：%+v", res)
	}
	if res.Pairs < 1 {
		t.Fatalf("两篇 Go 笔记应被连起来：%+v", res)
	}
	// 图里一对笔记只画一条线（无向去重），所以是「边数 ≥1」
	g, err := st.Graph(GraphOptions{Namespace: ns, IncludeAuto: true})
	if err != nil {
		t.Fatalf("取图失败：%v", err)
	}
	if g.AutoEdges < 1 {
		t.Fatalf("应至少有一条自动边，实际 %d", g.AutoEdges)
	}
	// 但「双向」要成立：两篇笔记各自的反向链接里都能看到对方
	k1 := NoteKey(ns + "|1.md")
	k2 := NoteKey(ns + "|2.md")
	bl1, err := st.Backlinks(ns, k1)
	if err != nil {
		t.Fatalf("取反向链接失败：%v", err)
	}
	if len(bl1) == 0 || bl1[0].Kind != LinkAuto {
		t.Fatalf("1.md 的反向链接里应有自动边指向 2.md：%+v", bl1)
	}
	bl2, err := st.Backlinks(ns, k2)
	if err != nil {
		t.Fatalf("取反向链接失败：%v", err)
	}
	if len(bl2) == 0 {
		t.Fatal("自动边应当是双向的：2.md 也应看到 1.md")
	}
}

func TestAutoLinkWithoutVectorChannel(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	ctx := context.Background()
	if _, err := st.WriteNote(ctx, WriteNoteOptions{Namespace: "n", Path: "a.md", Content: "随便一段内容"}); err != nil {
		t.Fatalf("写笔记失败：%v", err)
	}
	res, err := st.AutoLink(ctx, AutoLinkOptions{Namespace: "n"})
	if err != nil {
		t.Fatalf("自动连边不该报错：%v", err)
	}
	if res.VectorUsed {
		t.Fatal("没配向量通道时不该声称用了向量")
	}
	if res.Note == "" {
		t.Fatal("没配向量通道时应给出明确说明，而不是静默")
	}
}

func TestNoteStatsAndForget(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	defer st.Close()
	ctx := context.Background()
	ns := "s"
	if _, err := st.WriteNote(ctx, WriteNoteOptions{Namespace: ns, Path: "a.md", Content: "A 链到 [[B]]"}); err != nil {
		t.Fatalf("写 A 失败：%v", err)
	}
	if _, err := st.WriteNote(ctx, WriteNoteOptions{Namespace: ns, Path: "b.md", Content: "B 独立"}); err != nil {
		t.Fatalf("写 B 失败：%v", err)
	}
	stt, err := st.NoteStats(ns)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if stt.Notes != 2 || stt.ExplicitEdges != 1 {
		t.Fatalf("统计不对：%+v", stt)
	}
	// 删掉 B：A 的出链应变成悬空，且不留残边
	if _, err := st.ForgetNote(ns, NoteKey(ns+"|b.md")); err != nil {
		t.Fatalf("删笔记失败：%v", err)
	}
	stt2, err := st.NoteStats(ns)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if stt2.Notes != 1 {
		t.Fatalf("删后应剩 1 条笔记：%+v", stt2)
	}
	if stt2.ExplicitEdges != 0 {
		t.Fatalf("删掉目标后不该残留指向它的边：%+v", stt2)
	}
}
