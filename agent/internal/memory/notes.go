package memory

// 本文件是「记忆星图」的笔记层：把 Markdown 笔记当正本存进记忆库，
// 自动解析 `[[双向链接]]` 与 `#标签`，并维护「谁链到谁」这张关系网。
//
// 与普通笔记软件（Obsidian 等）的关键区别在 **自动**：
//  - 显式双链 `[[目标]]` 会被解析成有向边（笔记 → 目标）；
//  - 没写链接的笔记之间，由向量语义相似度**自动**补边（见 graph.go 的 AutoLink）。
//    用户不用手动去连，白泽替他把「其实相关的笔记」连起来。
//
// 存储复用记忆库（memory.db）：笔记就是 source='note' 的分块，因此天然的
// 语义检索、向量、分区隔离全都沿用；笔记之间的关系另存 note_links / note_tags。
//
// 为什么关系表按 doc_key（字符串）而不是分块 id 记：笔记重写是「同键覆盖」，
// 分块 id 会变，而 doc_key 不变——用 id 记边，改一次笔记边就全断了。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"baize/internal/llm"
)

// SourceNote 笔记的数据来源标记（写进 chunks.source）
const SourceNote = "note"

// KindNote 笔记的类型标记
const KindNote = "note"

// 链接类型
const (
	LinkExplicit = "explicit" // 显式 [[双链]]
	LinkAuto     = "auto"     // 自动（语义相似度）
)

const notesSchema = `
CREATE TABLE IF NOT EXISTS note_links(
  namespace  TEXT NOT NULL DEFAULT 'default',
  from_key   TEXT NOT NULL,
  target     TEXT NOT NULL,              -- 归一化后的链接目标（标题）
  to_key     TEXT NOT NULL DEFAULT '',   -- 解析到的目标笔记键（'' = 还没解析到）
  kind       TEXT NOT NULL DEFAULT 'explicit',
  weight     REAL NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(namespace, from_key, target, kind)
);
CREATE INDEX IF NOT EXISTS idx_note_links_from ON note_links(namespace, from_key);
CREATE INDEX IF NOT EXISTS idx_note_links_to ON note_links(namespace, to_key);
CREATE TABLE IF NOT EXISTS note_tags(
  namespace TEXT NOT NULL DEFAULT 'default',
  note_key  TEXT NOT NULL,
  tag       TEXT NOT NULL,
  PRIMARY KEY(namespace, note_key, tag)
);
CREATE INDEX IF NOT EXISTS idx_note_tags_tag ON note_tags(namespace, tag);
`

// ensureNotesSchema 建笔记关系表（幂等，可反复跑）
func ensureNotesSchema(db *sql.DB) error {
	_, err := db.Exec(notesSchema)
	return err
}

// Note 一条笔记（Markdown 正本）
type Note struct {
	ID        int64    `json:"id"`
	Namespace string   `json:"namespace"`
	Path      string   `json:"path"` // 相对路径（Obsidian 里就是 目录/文件.md）
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	DocKey    string   `json:"docKey"` // 稳定身份：同一路径 = 同一把键
	Tokens    int      `json:"tokens"`
	Links     []string `json:"links"` // 出链目标（原始标题，未解析）
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	HasVector bool     `json:"hasVector"`
}

// NoteKey 笔记的文档键：可读词干 + 路径哈希（小写归一，同一路径同一把键）。
// 同一路径得到同一把键 → 重新导入/保存是覆盖更新，不会越堆越多。
func NoteKey(nsPath string) string {
	norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(nsPath), "\\", "/"))
	base := strings.TrimSuffix(path.Base(norm), ".md")
	return "note-" + keySlug(base) + "-" + hashShort(norm)
}

// hashShort sha256 前 12 位十六进制（稳定、短、够区分）
func hashShort(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// ParseWikiLinks 解析正文里的 [[双向链接]]。
// 支持 `[[目标]]`、`[[目标|显示别名]]`、`[[目标#小标题]]`、`[[目标#小标题|别名]]`，
// 也认 `![[目标]]`（嵌入式引用）。返回去重后的目标标题列表（保持出现顺序）。
func ParseWikiLinks(content string) []string {
	out := []string{}
	seen := map[string]bool{}
	i := 0
	for i < len(content) {
		// 找下一个 "[["
		rel := strings.Index(content[i:], "[[")
		if rel < 0 {
			break
		}
		start := i + rel + 2
		endRel := strings.Index(content[start:], "]]")
		if endRel < 0 {
			break // 没闭合，剩下的不算链接
		}
		raw := content[start : start+endRel]
		i = start + endRel + 2
		// 去掉别名（| 之后）与小标题（# 之后）
		if p := strings.Index(raw, "|"); p >= 0 {
			raw = raw[:p]
		}
		if p := strings.Index(raw, "#"); p >= 0 {
			raw = raw[:p]
		}
		t := strings.TrimSpace(raw)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ParseTags 解析正文里的 #标签。
// 只在「# 位于行首或空白之后、且紧跟着一个字母（含中文）」时才算数——
// 免得把 Markdown 标题（`# 标题`，井号后有空格）和文中的 `#` 误判成标签。
func ParseTags(content string) []string {
	out := []string{}
	seen := map[string]bool{}
	runes := []rune(content)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '#' {
			continue
		}
		// 井号前必须是行首或空白
		if i > 0 && !unicode.IsSpace(runes[i-1]) {
			continue
		}
		// 井号后必须紧跟字母（中文按字母算）
		j := i + 1
		if j >= len(runes) || !unicode.IsLetter(runes[j]) {
			continue
		}
		k := j
		for k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsDigit(runes[k]) ||
			runes[k] == '-' || runes[k] == '_' || runes[k] == '/') {
			k++
		}
		tag := string(runes[j:k])
		tag = strings.Trim(tag, "-_/")
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
		i = k - 1
	}
	return out
}

// normalizeLinkTarget 把链接目标收敛成用于匹配的键：小写、去扩展名、去首尾空白
func normalizeLinkTarget(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".md")
	s = strings.ReplaceAll(s, "\\", "/")
	return strings.ToLower(s)
}

// titleFromContent 没有显式标题时：取正文第一个一级标题，再不行取首行
func titleFromContent(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "# ") {
			if h := strings.TrimSpace(t[2:]); h != "" {
				return h
			}
		}
	}
	if t := strings.TrimSpace(firstLine(content)); t != "" {
		return t
	}
	return fallback
}

// WriteNoteOptions 写笔记参数
type WriteNoteOptions struct {
	Namespace  string
	Category   string
	Path       string // 相对路径；空则用 标题.md
	Title      string // 空则取正文一级标题 / 首行
	Content    string
	Tags       []string // 额外标签（正文 #tag 也会被提取并合并）
	Source     string   // 默认 note
	Importance float64
}

// WriteNote 写入（或按路径覆盖）一条笔记，并解析出链与标签、尽量就地解析链接。
func (s *Store) WriteNote(ctx context.Context, opt WriteNoteOptions) (Note, error) {
	content := strings.TrimSpace(opt.Content)
	if content == "" {
		return Note{}, errors.New("笔记内容为空")
	}
	ns := NormalizeNamespace(opt.Namespace)
	cat := NormalizeCategory(opt.Category)
	if cat == "" {
		cat = CategoryCore
	}
	title := strings.TrimSpace(opt.Title)
	relPath := strings.TrimSpace(opt.Path)
	if relPath == "" {
		base := title
		if base == "" {
			base = "未命名"
		}
		relPath = base + ".md"
	}
	if !strings.HasSuffix(strings.ToLower(relPath), ".md") {
		relPath += ".md"
	}
	if title == "" {
		title = titleFromContent(content, strings.TrimSuffix(path.Base(relPath), ".md"))
	}
	key := NoteKey(ns + "|" + relPath)

	// 标签 = 正文里解析出的 + 显式传入的，去重
	tags := ParseTags(content)
	for _, t := range opt.Tags {
		t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		if t != "" {
			tags = append(tags, t)
		}
	}
	tags = dedupeStrings(tags)

	// 向量在锁外算（网络调用，别占着库锁）
	vecs, model := s.embedTexts(ctx, []string{embedInput(content)})
	var blob any
	if len(vecs) == 1 && len(vecs[0]) > 0 {
		blob = llm.EncodeVec(vecs[0])
	} else {
		model = ""
	}

	imp := opt.Importance
	if imp <= 0 {
		imp = defaultImportance(KindNote)
	}
	src := strings.TrimSpace(opt.Source)
	if src == "" {
		src = SourceNote
	}

	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	stamp := s.nextStamp(now)
	// 同键覆盖：这条笔记的旧记录连同它的出链/标签一起清掉（入链由对方那侧维护，不动）
	if _, err := s.db.Exec(`DELETE FROM chunks WHERE namespace=? AND doc_key=?`, ns, key); err != nil {
		return Note{}, err
	}
	if _, err := s.db.Exec(`DELETE FROM note_links WHERE namespace=? AND from_key=?`, ns, key); err != nil {
		return Note{}, err
	}
	if _, err := s.db.Exec(`DELETE FROM note_tags WHERE namespace=? AND note_key=?`, ns, key); err != nil {
		return Note{}, err
	}

	importance := clamp01(imp / 100.0)
	richness := Richness(content)
	res, err := s.db.Exec(`INSERT INTO chunks(namespace,category,doc_key,source,source_ref,kind,title,content,tokens,
		importance,recency,richness,score,hits,node_id,created_at,updated_at,meta,embedding,embedding_model)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,?,?)`,
		ns, cat, key, src, relPath, KindNote, title, content, EstimateTokens(content),
		importance, RecencyFactor(now, now), richness, Score(importance, RecencyFactor(now, now), richness, 0),
		now, stamp, "{}", blob, model)
	if err != nil {
		return Note{}, err
	}
	id, _ := res.LastInsertId()
	if err := s.attachToDayNode(id, now, 0); err != nil {
		return Note{}, err
	}

	for _, tg := range tags {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO note_tags(namespace,note_key,tag) VALUES(?,?,?)`,
			ns, key, tg); err != nil {
			return Note{}, err
		}
	}
	links := ParseWikiLinks(content)
	for _, target := range links {
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO note_links(namespace,from_key,target,to_key,kind,weight,created_at)
			VALUES(?,?,?,'',?,1,?)`, ns, key, normalizeLinkTarget(target), LinkExplicit, now); err != nil {
			return Note{}, err
		}
	}
	// 就地解析一次：把这条笔记的出链、以及别人指向它的入链都接上
	if _, err := s.resolveLinksLocked(ns); err != nil {
		return Note{}, err
	}

	return Note{
		ID: id, Namespace: ns, Path: relPath, Title: title, Content: content, Tags: tags,
		DocKey: key, Tokens: EstimateTokens(content), Links: links, CreatedAt: now, UpdatedAt: stamp,
		HasVector: blob != nil,
	}, nil
}

// NoteFile 导入时的一条笔记
type NoteFile struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// ImportResult 导入结果
type ImportResult struct {
	Notes     int      `json:"notes"`
	Skipped   int      `json:"skipped"`
	Tags      int      `json:"tags"`
	Links     int      `json:"links"`
	Namespace string   `json:"namespace"`
	Errors    []string `json:"errors,omitempty"`
}

// ImportNotes 批量导入笔记（Obsidian 库就是一堆 .md）。一条失败不影响其余。
func (s *Store) ImportNotes(ctx context.Context, ns string, files []NoteFile) (ImportResult, error) {
	ns = NormalizeNamespace(ns)
	out := ImportResult{Namespace: ns}
	for _, f := range files {
		if strings.TrimSpace(f.Content) == "" {
			out.Skipped++
			continue
		}
		n, err := s.WriteNote(ctx, WriteNoteOptions{
			Namespace: ns, Path: f.Path, Title: f.Title, Content: f.Content,
		})
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("%s：%v", f.Path, err))
			out.Skipped++
			continue
		}
		out.Notes++
		out.Tags += len(n.Tags)
		out.Links += len(n.Links)
	}
	return out, nil
}

// ListNotes 列出笔记（可按标签 / 关键词过滤，按更新时间倒序）
func (s *Store) ListNotes(ns, tag, query string, limit, offset int) ([]Note, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"c.source=?", "c.kind=?"}
	args := []any{SourceNote, KindNote}
	if ns = strings.TrimSpace(ns); ns != "" {
		where = append(where, "c.namespace=?")
		args = append(args, NormalizeNamespace(ns))
	}
	if tag = strings.TrimSpace(strings.TrimPrefix(tag, "#")); tag != "" {
		where = append(where, "EXISTS(SELECT 1 FROM note_tags t WHERE t.namespace=c.namespace AND t.note_key=c.doc_key AND t.tag=?)")
		args = append(args, tag)
	}
	if q := strings.TrimSpace(query); q != "" {
		where = append(where, "(c.title LIKE ? OR c.content LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	sqlText := `SELECT c.id,c.namespace,c.source_ref,c.doc_key,c.title,c.content,c.tokens,
		c.created_at,c.updated_at,
		(c.embedding IS NOT NULL AND length(c.embedding)>0) AS has_vec
		FROM chunks c WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY c.updated_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	s.mu.Lock()
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	notes := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			rows.Close()
			s.mu.Unlock()
			return nil, err
		}
		notes = append(notes, n)
	}
	rows.Close()
	s.mu.Unlock()
	if err := s.fillNoteDecor(notes); err != nil {
		return nil, err
	}
	return notes, nil
}

// GetNote 按文档键取一条笔记（含出链、入链、标签）
func (s *Store) GetNote(ns, key string) (Note, bool, error) {
	ns = NormalizeNamespace(ns)
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT c.id,c.namespace,c.source_ref,c.doc_key,c.title,c.content,c.tokens,
		c.created_at,c.updated_at,
		(c.embedding IS NOT NULL AND length(c.embedding)>0) AS has_vec
		FROM chunks c WHERE c.namespace=? AND c.doc_key=? LIMIT 1`, ns, key)
	if err != nil {
		s.mu.Unlock()
		return Note{}, false, err
	}
	var out Note
	found := false
	if rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			rows.Close()
			s.mu.Unlock()
			return Note{}, false, err
		}
		out, found = n, true
	}
	rows.Close()
	s.mu.Unlock()
	if !found {
		return Note{}, false, nil
	}
	list := []Note{out}
	if err := s.fillNoteDecor(list); err != nil {
		return Note{}, false, err
	}
	return list[0], true, nil
}

// fillNoteDecor 给一批笔记补上标签与出链（避免 N+1 次单查）
func (s *Store) fillNoteDecor(notes []Note) error {
	if len(notes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(notes))
	idx := map[string]int{}
	for i, n := range notes {
		keys = append(keys, n.DocKey)
		idx[n.DocKey] = i
	}
	ph := placeholders(len(keys))
	s.mu.Lock()
	defer s.mu.Unlock()
	tagRows, err := s.db.Query(`SELECT note_key,tag FROM note_tags WHERE note_key IN (`+ph+`) ORDER BY tag`, toAny(keys)...)
	if err != nil {
		return err
	}
	for tagRows.Next() {
		var k, t string
		if err := tagRows.Scan(&k, &t); err != nil {
			tagRows.Close()
			return err
		}
		if i, ok := idx[k]; ok {
			notes[i].Tags = append(notes[i].Tags, t)
		}
	}
	tagRows.Close()
	linkRows, err := s.db.Query(`SELECT from_key,target FROM note_links
		WHERE kind=? AND from_key IN (`+ph+`) ORDER BY target`, append([]any{LinkExplicit}, toAny(keys)...)...)
	if err != nil {
		return err
	}
	for linkRows.Next() {
		var k, t string
		if err := linkRows.Scan(&k, &t); err != nil {
			linkRows.Close()
			return err
		}
		if i, ok := idx[k]; ok {
			notes[i].Links = append(notes[i].Links, t)
		}
	}
	linkRows.Close()
	return nil
}

// Backlink 一条反向链接（谁链到我）
type Backlink struct {
	NoteKey string  `json:"noteKey"`
	NoteID  int64   `json:"noteId"`
	Title   string  `json:"title"`
	Path    string  `json:"path"`
	Kind    string  `json:"kind"`   // explicit / auto
	Weight  float64 `json:"weight"` // auto 边是相似度；explicit 恒为 1
}

// Backlinks 取一条笔记的反向链接：显式的（谁写了 [[我]]）+ 自动的（谁跟我语义最像）。
func (s *Store) Backlinks(ns, key string) ([]Backlink, error) {
	ns = NormalizeNamespace(ns)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT l.from_key, l.kind, l.weight, c.id, c.title, c.source_ref
		FROM note_links l JOIN chunks c ON c.namespace=l.namespace AND c.doc_key=l.from_key
		WHERE l.namespace=? AND l.to_key=? AND l.from_key<>?
		ORDER BY (l.kind='explicit') DESC, l.weight DESC`, ns, key, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backlink{}
	for rows.Next() {
		var b Backlink
		if err := rows.Scan(&b.NoteKey, &b.Kind, &b.Weight, &b.NoteID, &b.Title, &b.Path); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Outlinks 一条笔记的出链（指向谁），把还没解析到的目标也列出来
type Outlink struct {
	Target  string  `json:"target"`
	NoteKey string  `json:"noteKey"`
	NoteID  int64   `json:"noteId"`
	Title   string  `json:"title"`
	Kind    string  `json:"kind"`
	Weight  float64 `json:"weight"`
	Missing bool    `json:"missing"` // 目标笔记还不存在（悬空链接）
}

// Outlinks 取一条笔记的出链
func (s *Store) Outlinks(ns, key string) ([]Outlink, error) {
	ns = NormalizeNamespace(ns)
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT l.target, l.to_key, l.kind, l.weight,
		COALESCE(c.id,0), COALESCE(c.title,'')
		FROM note_links l LEFT JOIN chunks c ON c.namespace=l.namespace AND c.doc_key=l.to_key
		WHERE l.namespace=? AND l.from_key=? AND l.kind=?
		ORDER BY l.target`, ns, key, LinkExplicit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Outlink{}
	for rows.Next() {
		var o Outlink
		if err := rows.Scan(&o.Target, &o.NoteKey, &o.Kind, &o.Weight, &o.NoteID, &o.Title); err != nil {
			return nil, err
		}
		o.Missing = o.NoteKey == ""
		out = append(out, o)
	}
	return out, rows.Err()
}

// ResolveLinks 把整个分区里「还没解析到」的显式链接，按标题/路径匹配补上目标键。
// 导入整个 Obsidian 库之后调用一次，双向链接就全接上了。
func (s *Store) ResolveLinks(ns string) (int, error) {
	ns = NormalizeNamespace(ns)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveLinksLocked(ns)
}

// resolveLinksLocked 调用方需已持有 s.mu
func (s *Store) resolveLinksLocked(ns string) (int, error) {
	// 建索引：标题/路径 → 笔记键
	rows, err := s.db.Query(`SELECT doc_key,title,source_ref FROM chunks
		WHERE namespace=? AND source=? AND kind=?`, ns, SourceNote, KindNote)
	if err != nil {
		return 0, err
	}
	byTitle := map[string]string{}
	byPath := map[string]string{}
	for rows.Next() {
		var key, title, p string
		if err := rows.Scan(&key, &title, &p); err != nil {
			rows.Close()
			return 0, err
		}
		if t := normalizeLinkTarget(title); t != "" {
			if _, ok := byTitle[t]; !ok {
				byTitle[t] = key
			}
		}
		if p != "" {
			byPath[normalizeLinkTarget(p)] = key
			byPath[normalizeLinkTarget(strings.TrimSuffix(p, ".md"))] = key
			byPath[normalizeLinkTarget(path.Base(p))] = key
			byPath[normalizeLinkTarget(strings.TrimSuffix(path.Base(p), ".md"))] = key
		}
	}
	rows.Close()

	// 拉出未解析的链接
	type pend struct {
		from, target string
	}
	var todo []pend
	prows, err := s.db.Query(`SELECT from_key,target FROM note_links
		WHERE namespace=? AND kind=? AND (to_key IS NULL OR to_key='')`, ns, LinkExplicit)
	if err != nil {
		return 0, err
	}
	for prows.Next() {
		var p pend
		if err := prows.Scan(&p.from, &p.target); err != nil {
			prows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	prows.Close()

	n := 0
	for _, p := range todo {
		key := byPath[p.target]
		if key == "" {
			key = byTitle[p.target]
		}
		if key == "" {
			continue
		}
		if _, err := s.db.Exec(`UPDATE note_links SET to_key=? WHERE namespace=? AND from_key=? AND target=? AND kind=?`,
			key, ns, p.from, p.target, LinkExplicit); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ForgetNote 删除一条笔记（连同它的出链与标签；指向它的入链一并清掉，避免留下悬空边）
func (s *Store) ForgetNote(ns, key string) (int, error) {
	ns = NormalizeNamespace(ns)
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.Exec(`DELETE FROM chunks WHERE namespace=? AND doc_key=? AND source=?`, ns, key, SourceNote)
	if err != nil {
		return 0, err
	}
	n, _ := r.RowsAffected()
	if _, err := s.db.Exec(`DELETE FROM note_links WHERE namespace=? AND (from_key=? OR to_key=?)`, ns, key, key); err != nil {
		return int(n), err
	}
	if _, err := s.db.Exec(`DELETE FROM note_tags WHERE namespace=? AND note_key=?`, ns, key); err != nil {
		return int(n), err
	}
	return int(n), nil
}

// AllTags 分区内所有标签及计数
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Tags 列出分区里的标签云（按热度倒序）
func (s *Store) Tags(ns string) ([]TagCount, error) {
	where := ""
	args := []any{}
	if ns = strings.TrimSpace(ns); ns != "" {
		where = " WHERE namespace=?"
		args = append(args, NormalizeNamespace(ns))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT tag,COUNT(*) FROM note_tags`+where+` GROUP BY tag ORDER BY COUNT(*) DESC, tag`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

/* ---------- 小工具 ---------- */

// scanNote 从一行查询结果读出一条笔记
func scanNote(sc interface{ Scan(...any) error }) (Note, error) {
	var n Note
	var hasVec int
	if err := sc.Scan(&n.ID, &n.Namespace, &n.Path, &n.DocKey, &n.Title, &n.Content, &n.Tokens,
		&n.CreatedAt, &n.UpdatedAt, &hasVec); err != nil {
		return n, err
	}
	n.HasVector = hasVec > 0
	n.Tags = []string{}
	n.Links = []string{}
	return n, nil
}

// embedInput 向量化用的文本：太长的笔记只取头部（embedding 接口普遍有输入上限），
// 头部（标题 + 开头）已经足够代表这篇笔记在讲什么。
func embedInput(content string) string {
	r := []rune(content)
	const maxRunes = 4000
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return content
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
