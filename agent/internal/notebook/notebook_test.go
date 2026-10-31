package notebook

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMarkdown(t *testing.T) {
	content := "---\n" +
		"title: 我的笔记\n" +
		"aliases: [别名甲, \"别名乙\"]\n" +
		"tags: [front/tag, 另一个]\n" +
		"---\n\n" +
		"# 一级标题\n\n" +
		"正文含 [[另一篇笔记|显示名]] 与 ![[图片.png]]，还有 [[带锚点#小节]] 与 [[块引用#^abc]]。\n" +
		"行内代码 `#不是标签` 不算，`[[不是链接]]` 也不算。\n" +
		"#真实标签 和 #嵌套/标签 算。\n" +
		"[普通内链](子目录/文档.md) 算；[外链](https://example.com) 不算。\n\n" +
		"```go\n# 代码里的不算\n[[也不算]]\n```\n\n" +
		"## 二级标题\n"

	p := ParseMarkdown(content, "fallback")

	if p.Title != "我的笔记" {
		t.Fatalf("标题应取 frontmatter，得到 %q", p.Title)
	}
	if len(p.Aliases) != 2 || p.Aliases[0] != "别名甲" || p.Aliases[1] != "别名乙" {
		t.Fatalf("别名解析不对：%#v", p.Aliases)
	}
	tags := strings.Join(p.Tags, ",")
	for _, want := range []string{"front/tag", "另一个", "真实标签", "嵌套/标签"} {
		if !strings.Contains(tags, want) {
			t.Fatalf("标签里缺 %q：%v", want, p.Tags)
		}
	}
	if strings.Contains(tags, "不是标签") {
		t.Fatalf("行内代码里的井号被误判成标签：%v", p.Tags)
	}

	if len(p.Links) != 5 {
		t.Fatalf("应解析出 5 个内链，实际 %d：%#v", len(p.Links), p.Links)
	}
	byTarget := map[string]LinkRef{}
	for _, l := range p.Links {
		byTarget[l.Target] = l
	}
	if l, ok := byTarget["另一篇笔记"]; !ok || l.Alias != "显示名" {
		t.Fatalf("别名链接解析不对：%#v", l)
	}
	if l, ok := byTarget["图片.png"]; !ok || !l.Embed {
		t.Fatalf("嵌入链接解析不对：%#v", l)
	}
	if l, ok := byTarget["带锚点"]; !ok || l.Anchor != "小节" {
		t.Fatalf("锚点链接解析不对：%#v", l)
	}
	if l, ok := byTarget["块引用"]; !ok || l.Block != "abc" {
		t.Fatalf("块引用解析不对：%#v", l)
	}
	if _, ok := byTarget["子目录/文档.md"]; !ok {
		t.Fatalf("markdown 内链没解析出来：%#v", p.Links)
	}

	if len(p.Headings) != 2 || p.Headings[0].Level != 1 || p.Headings[1].Level != 2 {
		t.Fatalf("标题解析不对：%#v", p.Headings)
	}
	if p.BodyLine < 6 {
		t.Fatalf("正文起始行号不对：%d", p.BodyLine)
	}
}

func TestParseMarkdownNoFrontmatter(t *testing.T) {
	p := ParseMarkdown("# 只有标题\n\n正文", "文件名")
	if p.Title != "只有标题" {
		t.Fatalf("没有 frontmatter 时应取一级标题，得到 %q", p.Title)
	}
	if p.BodyLine != 1 {
		t.Fatalf("没有 frontmatter 时正文应从第 1 行开始，得到 %d", p.BodyLine)
	}
}

func TestVaultWriteReadLinksSearch(t *testing.T) {
	v := openTestVault(t)

	a, err := v.Write(WriteOptions{Path: "笔记A.md", Content: "# A\n\n链接到 [[笔记B]] 与 [[不存在的]]\n#标签甲"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Key != "笔记a.md" {
		t.Fatalf("key 应是小写路径，得到 %q", a.Key)
	}
	if _, err := v.Write(WriteOptions{Path: "笔记B.md", Content: "# B\n\n这是英文 hello world，中文写着双向链接。\n"}); err != nil {
		t.Fatal(err)
	}

	// 大小写不敏感
	n, ok, err := v.Read("笔记a.MD")
	if err != nil || !ok {
		t.Fatalf("读取失败：%v %v", ok, err)
	}
	if n.Title != "A" || n.Tokens <= 0 {
		t.Fatalf("笔记内容不对：%#v", n)
	}

	outs, err := v.Outlinks("笔记A.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 {
		t.Fatalf("出链应有 2 条，实际 %d", len(outs))
	}
	miss := 0
	for _, o := range outs {
		if o.Missing {
			miss++
		}
	}
	if miss != 1 {
		t.Fatalf("悬空链接应有 1 条，实际 %d", miss)
	}

	bl, err := v.Backlinks("笔记B.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 1 || bl[0].NoteKey != "笔记a.md" {
		t.Fatalf("反向链接不对：%#v", bl)
	}

	tags, err := v.Tags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Tag != "标签甲" {
		t.Fatalf("标签云不对：%#v", tags)
	}

	st, err := v.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Notes != 2 || st.Links != 1 || st.Missing != 1 || st.Orphans != 0 {
		t.Fatalf("概览不对：%#v", st)
	}

	hits, err := v.Search("双向链接", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != "笔记b.md" {
		t.Fatalf("中文检索不对：%#v", hits)
	}
	if hits, err = v.Search("hello", 10); err != nil || len(hits) != 1 {
		t.Fatalf("英文检索不对：%#v %v", hits, err)
	}

	list, total, err := v.List(ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("列表不对：total=%d len=%d", total, len(list))
	}

	if ok, err := v.Delete("笔记A.md"); err != nil || !ok {
		t.Fatalf("删除失败：%v %v", ok, err)
	}
	if st, err = v.Stats(); err != nil || st.Notes != 1 {
		t.Fatalf("删掉后应剩 1 篇：%#v %v", st, err)
	}

	if _, err := v.Rename("笔记B.md", "目录/笔记B改名.md"); err != nil {
		t.Fatal(err)
	}
	if n, ok, _ := v.Read("目录/笔记B改名.md"); !ok || n.Title != "B" {
		t.Fatalf("重命名后读不到：%#v", n)
	}
	if _, ok, _ := v.Read("笔记B.md"); ok {
		t.Fatal("重命名后旧路径还能读到")
	}
}

func TestVaultWriteMergesTagsIntoFrontmatter(t *testing.T) {
	v := openTestVault(t)
	content := "---\ntags: [已有]\nversion: 1\n---\n\n正文\n"
	n, err := v.Write(WriteOptions{Path: "标签.md", Content: content, Tags: []string{"#新增", "已有"}})
	if err != nil {
		t.Fatal(err)
	}
	tags := strings.Join(n.Tags, ",")
	if !strings.Contains(tags, "已有") || !strings.Contains(tags, "新增") {
		t.Fatalf("额外标签没合并进去：%#v\n内容：\n%s", n.Tags, n.Content)
	}
	if !strings.Contains(n.Content, "version: 1") {
		t.Fatalf("原有 frontmatter 被破坏：\n%s", n.Content)
	}
	if strings.Contains(n.Content, "[已有]") {
		t.Fatalf("内联 tags 应被改写成块级数组：\n%s", n.Content)
	}
}

func TestVaultScan(t *testing.T) {
	v := openTestVault(t)
	if _, err := v.Write(WriteOptions{Path: "B.md", Content: "# B\n\n看 [[新来的]]\n"}); err != nil {
		t.Fatal(err)
	}

	// 直接在磁盘上新建一篇（模拟 Obsidian 里另存文件）
	add := filepath.Join(v.Root(), "新来的.md")
	if err := os.WriteFile(add, []byte("# 新来的\n\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := v.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("应新增 1 篇：%#v", res)
	}
	if outs, _ := v.Outlinks("B.md"); len(outs) != 1 || outs[0].Missing {
		t.Fatalf("扫描后悬空链接应被解析：%#v", outs)
	}

	if err := os.WriteFile(add, []byte("# 新来的 v2\n\n改过了\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err = v.Scan(); err != nil || res.Updated != 1 {
		t.Fatalf("应更新 1 篇：%#v %v", res, err)
	}

	if err := os.Remove(add); err != nil {
		t.Fatal(err)
	}
	if res, err = v.Scan(); err != nil || res.Removed != 1 {
		t.Fatalf("应移除 1 篇：%#v %v", res, err)
	}
	if outs, _ := v.Outlinks("B.md"); len(outs) != 1 || !outs[0].Missing {
		t.Fatalf("文件没了，链接应退回悬空：%#v", outs)
	}
}

func TestGraphAndAutoRelated(t *testing.T) {
	v := openTestVault(t)
	writes := []WriteOptions{
		{Path: "A.md", Content: "# A\n\n#共同\n"},
		{Path: "B.md", Content: "# B\n\n指向 [[A]]\n#共同\n"},
		{Path: "D.md", Content: "# D\n\n也指向 [[A]] 和 [[没有这篇]]\n#共同\n"},
	}
	for _, w := range writes {
		if _, err := v.Write(w); err != nil {
			t.Fatal(err)
		}
	}

	g, err := v.Graph(GraphOptions{IncludeTags: true, IncludeUnresolved: true})
	if err != nil {
		t.Fatal(err)
	}
	if g.NoteCount != 3 || g.Shown != 5 {
		t.Fatalf("节点数不对：note=%d shown=%d nodes=%#v", g.NoteCount, g.Shown, g.Nodes)
	}
	if g.ExplicitEdges != 2 || g.MissingEdges != 1 || g.TagEdges != 3 {
		t.Fatalf("边统计不对：%#v", g)
	}

	// 以 A 为中心的 1 跳局部图：A + 指向 A 的两篇
	local, err := v.Graph(GraphOptions{Limit: 0, Hops: 1, Root: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if local.Shown != 3 {
		t.Fatalf("局部图应只有 3 个笔记点，实际 %d：%#v", local.Shown, local.Nodes)
	}

	res, err := v.AutoRelated(RelatedOptions{TopK: 2, MinSim: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pairs < 1 || res.Edges < 2 {
		t.Fatalf("应补出相关边：%#v", res)
	}

	withAuto, err := v.Graph(GraphOptions{IncludeRelated: true, MinWeight: res.MinSim})
	if err != nil {
		t.Fatal(err)
	}
	if withAuto.AutoEdges < 1 {
		t.Fatalf("带上相关边后应有 auto 边：%#v", withAuto)
	}
}

func openTestVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func TestImportLegacyNotes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE chunks(
		namespace TEXT NOT NULL DEFAULT 'default',
		source TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		source_ref TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chunks(namespace,source,kind,source_ref,title,content)
		VALUES('default','note','note','旧笔记.md','旧笔记','# 旧笔记\n\n指向 [[另一篇]]'),
		      ('work','note','note','周报.md','周报','# 周报\n\n本周进度')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	v, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	res, err := v.ImportLegacyNotes(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != 2 {
		t.Fatalf("应迁移 2 篇旧笔记，实际 %d：%#v", res.Notes, res)
	}
	if n, ok, err := v.Read("旧笔记.md"); err != nil || !ok || !strings.Contains(n.Content, "指向") {
		t.Fatalf("默认命名空间的旧笔记没还原：ok=%v err=%v", ok, err)
	}
	if _, ok, err := v.Read("legacy/work/周报.md"); err != nil || !ok {
		t.Fatalf("非默认命名空间的旧笔记没落到 legacy/work 下：ok=%v err=%v", ok, err)
	}

	// 幂等：再跑一次不应重复导入
	again, err := v.ImportLegacyNotes(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if again.Notes != 0 {
		t.Fatalf("迁移不是幂等的：又导入了 %d 篇", again.Notes)
	}
}
