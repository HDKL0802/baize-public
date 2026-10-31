package notebook

// vault 层：笔记的正本就是磁盘上的 .md 文件（Obsidian 兼容的目录结构），
// SQLite（notes.db）只是「索引 + 缓存」，删了可以随时重建。
//
// 这是与旧实现最根本的差别：旧实现把笔记当记忆库的 chunks，没有文件层，
// 用户看不到、也拿不走自己的笔记；这里反过来——文件是正本，索引是派生。
//
// 增量重建靠 (mtime, size) 先比对、命中再比 content hash，只有真变了才重解析，
// 所以扫描上万个 .md 也不会每次都做全量解析。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 的 SQLite 驱动（注册 "sqlite" 方言）
)

// 常量
const (
	IndexFileName = "notes.db" // 索引文件名
	VaultDirName  = "vault"    // 笔记根目录名（相对数据目录）
	MaxNoteBytes  = 8 << 20    // 单篇笔记上限 8MB（防止误把大文件当笔记）
)

// 链接类型
const (
	LinkKindLink  = "link"  // [[目标]] 或 [文字](笔记.md)
	LinkKindEmbed = "embed" // ![[目标]]
)

var ignoreDirs = map[string]bool{
	".obsidian": true, ".trash": true, ".git": true,
	"node_modules": true, ".svn": true,
}

// Note 一篇笔记（含正文）
type Note struct {
	Key         string         `json:"key"`  // 稳定身份 = 小写相对路径
	Path        string         `json:"path"` // 相对路径（保留大小写）
	Title       string         `json:"title"`
	Aliases     []string       `json:"aliases"`
	Tags        []string       `json:"tags"`
	Content     string         `json:"content"`
	Frontmatter map[string]any `json:"frontmatter"`
	Size        int64          `json:"size"`
	MTime       int64          `json:"mtime"`
	Tokens      int            `json:"tokens"`
	Links       []LinkRef      `json:"links"`
	CreatedAt   int64          `json:"createdAt"`
	UpdatedAt   int64          `json:"updatedAt"`
	HasVector   bool           `json:"hasVector"` // 笔记模块不产生向量，恒为 false（保留字段给旧前端）
}

// NoteSummary 列表用的轻量笔记（不带正文）
type NoteSummary struct {
	Key       string   `json:"key"`
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Aliases   []string `json:"aliases"`
	Tags      []string `json:"tags"`
	Excerpt   string   `json:"excerpt"`
	Size      int64    `json:"size"`
	Tokens    int      `json:"tokens"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

// TagCount 标签及热度
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Backlink 反向链接
type Backlink struct {
	NoteKey string   `json:"noteKey"`
	Title   string   `json:"title"`
	Path    string   `json:"path"`
	Alias   string   `json:"alias,omitempty"`
	Anchor  string   `json:"anchor,omitempty"`
	Embed   bool     `json:"embed"`
	Tags    []string `json:"tags"`
}

// Outlink 出链（含悬空）
type Outlink struct {
	Target  string `json:"target"`
	NoteKey string `json:"noteKey"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Anchor  string `json:"anchor,omitempty"`
	Embed   bool   `json:"embed"`
	Missing bool   `json:"missing"` // 目标还不存在（悬空链接）
}

// Stats 笔记层概览
type Stats struct {
	Notes        int   `json:"notes"`
	Links        int   `json:"links"`   // 已解析的内部链接
	Missing      int   `json:"missing"` // 悬空链接
	Tags         int   `json:"tags"`    // 去重后的标签数
	Orphans      int   `json:"orphans"` // 没有任何链接的笔记
	Tokens       int64 `json:"tokens"`
	LastScanAt   int64 `json:"lastScanAt"`
	IndexedTerms int   `json:"indexedTerms"`
}

// Vault 笔记库
type Vault struct {
	root string // 笔记根目录（.md 存放处）
	db   *sql.DB
	mu   sync.Mutex
}

// Open 打开「数据目录」下的笔记库：<dir>/vault + <dir>/notes.db
func Open(dir string) (*Vault, error) {
	return OpenAt(dir, filepath.Join(dir, VaultDirName))
}

// OpenAt 指定笔记根目录（可以把已有的 Obsidian 库直接挂进来）
func OpenAt(dir, root string) (*Vault, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建笔记目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, IndexFileName)) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开笔记索引失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化笔记索引失败：%w", err)
	}
	return &Vault{root: root, db: db}, nil
}

// Close 关闭
func (v *Vault) Close() error {
	if v == nil || v.db == nil {
		return nil
	}
	return v.db.Close()
}

// Root 笔记根目录
func (v *Vault) Root() string { return v.root }

// IndexPath 索引文件路径
func (v *Vault) IndexPath() string {
	return filepath.Join(filepath.Dir(v.root), IndexFileName)
}

const schema = `
CREATE TABLE IF NOT EXISTS notes(
  key          TEXT PRIMARY KEY,
  path         TEXT NOT NULL,
  title        TEXT NOT NULL,
  aliases      TEXT NOT NULL DEFAULT '[]',
  tags         TEXT NOT NULL DEFAULT '[]',
  frontmatter  TEXT NOT NULL DEFAULT '{}',
  content      TEXT NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '',
  size         INTEGER NOT NULL DEFAULT 0,
  mtime        INTEGER NOT NULL DEFAULT 0,
  tokens       INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_notes_path ON notes(path);
CREATE TABLE IF NOT EXISTS note_links(
  from_key TEXT NOT NULL,
  target   TEXT NOT NULL,
  to_key   TEXT NOT NULL DEFAULT '',
  kind     TEXT NOT NULL DEFAULT 'link',
  alias    TEXT NOT NULL DEFAULT '',
  anchor   TEXT NOT NULL DEFAULT '',
  embed    INTEGER NOT NULL DEFAULT 0,
  line     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(from_key, target, kind, anchor, embed)
);
CREATE INDEX IF NOT EXISTS idx_note_links_to ON note_links(to_key);
CREATE TABLE IF NOT EXISTS note_tags(
  note_key TEXT NOT NULL,
  tag      TEXT NOT NULL,
  PRIMARY KEY(note_key, tag)
);
CREATE INDEX IF NOT EXISTS idx_note_tags_tag ON note_tags(tag);
CREATE TABLE IF NOT EXISTS note_terms(
  note_key TEXT NOT NULL,
  term     TEXT NOT NULL,
  tf       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(note_key, term)
);
CREATE INDEX IF NOT EXISTS idx_note_terms_term ON note_terms(term);
CREATE TABLE IF NOT EXISTS note_related(
  a          TEXT NOT NULL,
  b          TEXT NOT NULL,
  weight     REAL NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(a, b)
);
CREATE INDEX IF NOT EXISTS idx_note_related_a ON note_related(a);
CREATE TABLE IF NOT EXISTS note_meta(
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL DEFAULT ''
);
`

// ---------- 路径 ----------

// cleanPath 规范化成 vault 内的相对路径（统一 /、补 .md、禁止穿越）
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", errors.New("路径不能为空")
	}
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		p += ".md"
	}
	cleaned := path.Clean(p)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("非法路径：%s", p)
	}
	return cleaned, nil
}

func keyOf(p string) string { return strings.ToLower(p) }

// linkTargetKey 链接目标的匹配键：去 .md、去 ./ ../、小写、/ 分隔
func linkTargetKey(t string) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "\\", "/"))
	t = strings.TrimPrefix(t, "./")
	t = strings.TrimPrefix(t, "../")
	t = strings.ToLower(t)
	t = strings.TrimSuffix(t, ".md")
	return t
}

func baseStem(p string) string { return strings.TrimSuffix(path.Base(p), ".md") }

// ---------- 读 ----------

// Read 按 key 取一篇笔记；key 允许传路径（大小写不敏感）
func (v *Vault) Read(key string) (Note, bool, error) {
	k, err := cleanPath(key)
	if err != nil {
		return Note{}, false, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.readNoLock(keyOf(k))
}

// readNoLock 调用方需已持有 v.mu
func (v *Vault) readNoLock(k string) (Note, bool, error) {
	var n Note
	var aliasesJSON, tagsJSON, fmJSON string
	err := v.db.QueryRow(`SELECT key,path,title,aliases,tags,frontmatter,content,size,mtime,tokens,created_at,updated_at FROM notes WHERE key=?`,
		k).Scan(&n.Key, &n.Path, &n.Title, &aliasesJSON, &tagsJSON, &fmJSON, &n.Content,
		&n.Size, &n.MTime, &n.Tokens, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, false, nil
	}
	if err != nil {
		return Note{}, false, err
	}
	_ = json.Unmarshal([]byte(aliasesJSON), &n.Aliases)
	_ = json.Unmarshal([]byte(tagsJSON), &n.Tags)
	_ = json.Unmarshal([]byte(fmJSON), &n.Frontmatter)
	if n.Aliases == nil {
		n.Aliases = []string{}
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	if n.Frontmatter == nil {
		n.Frontmatter = map[string]any{}
	}
	links, err := v.linksOf(n.Key)
	if err != nil {
		return Note{}, false, err
	}
	n.Links = links
	return n, true, nil
}

func (v *Vault) linksOf(key string) ([]LinkRef, error) {
	rows, err := v.db.Query(`SELECT target,alias,anchor,embed,kind,line FROM note_links WHERE from_key=? ORDER BY line,target`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LinkRef{}
	for rows.Next() {
		var l LinkRef
		var embed int
		var kind string
		if err := rows.Scan(&l.Target, &l.Alias, &l.Anchor, &embed, &kind, &l.Line); err != nil {
			return nil, err
		}
		l.Embed = embed > 0 || kind == LinkKindEmbed
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------- 写 ----------

// WriteOptions 写笔记参数
type WriteOptions struct {
	Path    string // 相对路径（空则用标题）
	Title   string // 空则取正文一级标题 / 首行
	Content string
	Tags    []string // 额外标签（正文 #tag 也会被提取并合并）
}

// Write 写入（同路径覆盖）一篇笔记，落盘 + 更新索引 + 重解析链接
func (v *Vault) Write(opt WriteOptions) (Note, error) {
	content := strings.TrimSpace(opt.Content)
	if content == "" {
		return Note{}, errors.New("笔记内容为空")
	}
	p := strings.TrimSpace(opt.Path)
	if p == "" {
		title := strings.TrimSpace(opt.Title)
		if title == "" {
			title = firstLine(content)
		}
		if title == "" {
			title = "未命名"
		}
		p = sanitizeFileName(title) + ".md"
	}
	rel, err := cleanPath(p)
	if err != nil {
		return Note{}, err
	}
	// 额外标签并进 frontmatter（保留用户原文）
	if len(opt.Tags) > 0 {
		content = mergeTagsIntoContent(content, opt.Tags)
	}

	abs := filepath.Join(v.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Note{}, fmt.Errorf("创建目录失败：%w", err)
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return Note{}, fmt.Errorf("写笔记失败：%w", err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return Note{}, fmt.Errorf("落盘失败：%w", err)
	}

	now := time.Now().UnixMilli()
	info, err := os.Stat(abs)
	if err != nil {
		return Note{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.indexOneLocked(rel, content, info.Size(), info.ModTime().UnixMilli(), now); err != nil {
		return Note{}, err
	}
	if _, err := v.resolveLocked(); err != nil {
		return Note{}, err
	}
	return v.readLocked(rel)
}

// ImportFile 一条待导入的笔记
type ImportFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ImportResult 导入结果
type ImportResult struct {
	Notes   int      `json:"notes"`
	Skipped int      `json:"skipped"`
	Links   int      `json:"links"`
	Tags    int      `json:"tags"`
	Errors  []string `json:"errors,omitempty"`
}

// Import 批量导入（Obsidian 库就是一堆 .md）。一条失败不影响其余。
func (v *Vault) Import(files []ImportFile) (ImportResult, error) {
	out := ImportResult{}
	for _, f := range files {
		if strings.TrimSpace(f.Content) == "" {
			out.Skipped++
			continue
		}
		n, err := v.Write(WriteOptions{Path: f.Path, Content: f.Content})
		if err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, fmt.Sprintf("%s：%v", f.Path, err))
			continue
		}
		out.Notes++
		out.Links += len(n.Links)
		out.Tags += len(n.Tags)
	}
	if _, err := v.ResolveLinks(); err != nil {
		return out, err
	}
	return out, nil
}

// Delete 删除一篇笔记（文件 + 索引；指向它的链接会退回「悬空」而不是被删掉）
func (v *Vault) Delete(key string) (bool, error) {
	rel, err := cleanPath(key)
	if err != nil {
		return false, err
	}
	k := keyOf(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	var p string
	err = v.db.QueryRow(`SELECT path FROM notes WHERE key=?`, k).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	abs := filepath.Join(v.root, filepath.FromSlash(p))
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("删除文件失败：%w", err)
	}
	if _, err := v.db.Exec(`DELETE FROM notes WHERE key=?`, k); err != nil {
		return false, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_links WHERE from_key=?`, k); err != nil {
		return false, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_tags WHERE note_key=?`, k); err != nil {
		return false, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_terms WHERE note_key=?`, k); err != nil {
		return false, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_related WHERE a=? OR b=?`, k, k); err != nil {
		return false, err
	}
	if _, err := v.db.Exec(`UPDATE note_links SET to_key='' WHERE to_key=?`, k); err != nil {
		return false, err
	}
	return true, nil
}

// Rename 重命名/移动，返回新的 key
func (v *Vault) Rename(oldKey, newPath string) (Note, error) {
	n, ok, err := v.Read(oldKey)
	if err != nil {
		return Note{}, err
	}
	if !ok {
		return Note{}, fmt.Errorf("没有这篇笔记：%s", oldKey)
	}
	rel, err := cleanPath(newPath)
	if err != nil {
		return Note{}, err
	}
	oldAbs := filepath.Join(v.root, filepath.FromSlash(n.Path))
	newAbs := filepath.Join(v.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return Note{}, err
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return Note{}, fmt.Errorf("移动文件失败：%w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, err := v.db.Exec(`DELETE FROM notes WHERE key=?`, n.Key); err != nil {
		return Note{}, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_links WHERE from_key=?`, n.Key); err != nil {
		return Note{}, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_tags WHERE note_key=?`, n.Key); err != nil {
		return Note{}, err
	}
	if _, err := v.db.Exec(`DELETE FROM note_terms WHERE note_key=?`, n.Key); err != nil {
		return Note{}, err
	}
	if _, err := v.db.Exec(`UPDATE note_links SET to_key='' WHERE to_key=?`, n.Key); err != nil {
		return Note{}, err
	}
	info, err := os.Stat(newAbs)
	if err != nil {
		return Note{}, err
	}
	if err := v.indexOneLocked(rel, n.Content, info.Size(), info.ModTime().UnixMilli(), time.Now().UnixMilli()); err != nil {
		return Note{}, err
	}
	if _, err := v.resolveLocked(); err != nil {
		return Note{}, err
	}
	return v.readLocked(rel)
}

// ---------- 索引 ----------

// indexOneLocked 解析并写入一篇笔记的索引（调用方需持锁）
func (v *Vault) indexOneLocked(rel, content string, size, mtime, now int64) error {
	key := keyOf(rel)
	p := ParseMarkdown(content, baseStem(rel))
	if p.Title == "" {
		p.Title = baseStem(rel)
	}
	title := p.Title
	aliasesJSON, _ := json.Marshal(p.Aliases)
	tagsJSON, _ := json.Marshal(p.Tags)
	fmJSON, _ := json.Marshal(p.Frontmatter)

	var createdAt int64
	err := v.db.QueryRow(`SELECT created_at FROM notes WHERE key=?`, key).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) || createdAt <= 0 {
		createdAt = now
	}

	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR REPLACE INTO notes
		(key,path,title,aliases,tags,frontmatter,content,content_hash,size,mtime,tokens,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, key, rel, title, string(aliasesJSON), string(tagsJSON), string(fmJSON),
		content, hashOf(content), size, mtime, tokenEstimate(content), createdAt, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM note_links WHERE from_key=?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM note_tags WHERE note_key=?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM note_terms WHERE note_key=?`, key); err != nil {
		return err
	}

	for _, l := range p.Links {
		target := linkTargetKey(l.Target)
		if target == "" {
			continue
		}
		kind := LinkKindLink
		if l.Embed {
			kind = LinkKindEmbed
		}
		embed := 0
		if l.Embed {
			embed = 1
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO note_links
			(from_key,target,to_key,kind,alias,anchor,embed,line) VALUES(?,?, '',?,?,?,?,?)`,
			key, target, kind, l.Alias, l.Anchor, embed, l.Line); err != nil {
			return err
		}
	}
	for _, t := range p.Tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO note_tags(note_key,tag) VALUES(?,?)`, key, t); err != nil {
			return err
		}
	}
	for term, tf := range termFreq(title + "\n" + content + "\n" + strings.Join(p.Aliases, "\n")) {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO note_terms(note_key,term,tf) VALUES(?,?,?)`, key, term, tf); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = v.db.Exec(`INSERT OR REPLACE INTO note_meta(k,v) VALUES('last_scan_at',?)`, fmt.Sprint(now))
	return nil
}

func (v *Vault) readLocked(rel string) (Note, error) {
	n, ok, err := v.readNoLock(keyOf(rel))
	if err != nil {
		return Note{}, err
	}
	if !ok {
		return Note{}, fmt.Errorf("写入后读不到这篇笔记：%s", rel)
	}
	return n, nil
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func sanitizeFileName(s string) string {
	s = strings.TrimSpace(s)
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	s = repl.Replace(s)
	s = strings.Trim(s, " .-")
	if s == "" {
		return "未命名"
	}
	return utf8Truncate(s, 100)
}

// mergeTagsIntoContent 把显式传入的标签补进 frontmatter（没有就新建一段）。
//
// 统一写成块级数组：即使原来写的是内联 `tags: [a, b]`，也改写成
//
//	tags:
//	  - a
//	  - b
//
// 否则「内联数组 + 块级续行」在标准 YAML（Obsidian 用的那套）里是非法结构。
func mergeTagsIntoContent(content string, extra []string) string {
	var want []string
	for _, t := range extra {
		if t = normalizeTag(t); t != "" {
			want = append(want, t)
		}
	}
	if len(want) == 0 {
		return content
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	hasFM := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	end := -1
	if hasFM {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" || strings.TrimSpace(lines[i]) == "..." {
				end = i
				break
			}
		}
	}
	if end < 0 {
		// 没有 frontmatter：新建一段
		return "---\ntags:\n  - " + strings.Join(want, "\n  - ") + "\n---\n" + content
	}

	fm := lines[1:end]
	start, last := -1, -1
	var existing []string
	for i, raw := range fm {
		trim := strings.TrimSpace(raw)
		key, val, ok := splitYAMLKey(trim)
		if !ok || !strings.EqualFold(key, "tags") && !strings.EqualFold(key, "tag") {
			continue
		}
		start, last = i, i
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			existing = splitInlineList(val[1 : len(val)-1])
		}
		// 块级项紧跟其后；一并吃掉，统一重写成块级数组
		for j := i + 1; j < len(fm); j++ {
			t := strings.TrimSpace(fm[j])
			if !strings.HasPrefix(t, "- ") {
				break
			}
			existing = append(existing, unquote(strings.TrimSpace(t[2:])))
			last = j
		}
		break
	}
	merged := dedupeNormalizedTags(append(existing, want...))
	block := []string{"tags:"}
	for _, t := range merged {
		block = append(block, "  - "+t)
	}
	// 全部拼到新切片里（不要原地改 fm：它与 lines 共用底层数组，
	// 原地 append 会覆盖掉后面的正文）
	parts := []string{"---"}
	if start >= 0 {
		parts = append(parts, fm[:start]...)
		parts = append(parts, block...)
		parts = append(parts, fm[last+1:]...)
	} else {
		parts = append(parts, fm...)
		parts = append(parts, block...)
	}
	parts = append(parts, lines[end:]...) // 含收尾的 --- 及其后正文
	return strings.Join(parts, "\n")
}

// splitYAMLKey 拆 `key: value`，值可为空
func splitYAMLKey(trim string) (string, string, bool) {
	idx := strings.Index(trim, ":")
	if idx <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(trim[:idx]), strings.TrimSpace(trim[idx+1:]), true
}

// ---------- 链接解析 ----------

// resolveLocked 把「还没解析到」的链接按 路径 → 文件名 → 标题 → 别名 的顺序补上
func (v *Vault) resolveLocked() (int, error) {
	rows, err := v.db.Query(`SELECT key,path,title,aliases FROM notes`)
	if err != nil {
		return 0, err
	}
	byPath := map[string]string{}
	byBase := map[string]string{}
	byTitle := map[string]string{}
	byAlias := map[string]string{}
	type row struct{ key, path string }
	var all []row
	for rows.Next() {
		var key, p, title, aliasesJSON string
		if err := rows.Scan(&key, &p, &title, &aliasesJSON); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, row{key, p})
		if _, ok := byPath[keyOf(p)]; !ok {
			byPath[keyOf(p)] = key
		}
		stem := strings.ToLower(baseStem(p))
		if _, ok := byBase[stem]; !ok {
			byBase[stem] = key
		}
		if t := strings.ToLower(strings.TrimSpace(title)); t != "" {
			if _, ok := byTitle[t]; !ok {
				byTitle[t] = key
			}
		}
		var aliases []string
		_ = json.Unmarshal([]byte(aliasesJSON), &aliases)
		for _, a := range aliases {
			if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
				if _, ok := byAlias[a]; !ok {
					byAlias[a] = key
				}
			}
		}
	}
	rows.Close()
	// 同分优先级时按路径排序，保证结果稳定
	sort.Slice(all, func(i, j int) bool { return all[i].path < all[j].path })

	type pend struct {
		from, target string
		id           int64
	}
	var todo []pend
	prows, err := v.db.Query(`SELECT rowid,from_key,target FROM note_links WHERE to_key=''`)
	if err != nil {
		return 0, err
	}
	for prows.Next() {
		var p pend
		if err := prows.Scan(&p.id, &p.from, &p.target); err != nil {
			prows.Close()
			return 0, err
		}
		todo = append(todo, p)
	}
	prows.Close()

	n := 0
	for _, p := range todo {
		t := linkTargetKey(p.target)
		toKey := ""
		if k, ok := byPath[t]; ok {
			toKey = k
		} else if k, ok := byBase[t]; ok {
			toKey = k
		} else if k, ok := byTitle[t]; ok {
			toKey = k
		} else if k, ok := byAlias[t]; ok {
			toKey = k
		}
		if toKey == "" || toKey == p.from {
			continue
		}
		if _, err := v.db.Exec(`UPDATE note_links SET to_key=? WHERE rowid=?`, toKey, p.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ResolveLinks 对外暴露的「重新解析全部链接」
func (v *Vault) ResolveLinks() (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.resolveLocked()
}

// ---------- 扫描 ----------

// ScanResult 一次扫描的结果
type ScanResult struct {
	Scanned  int      `json:"scanned"`
	Added    int      `json:"added"`
	Updated  int      `json:"updated"`
	Removed  int      `json:"removed"`
	Resolved int      `json:"resolved"`
	Errors   []string `json:"errors,omitempty"`
}

// Scan 增量扫描笔记目录：新增/修改的入库，磁盘上没了的从索引里删掉。
func (v *Vault) Scan() (ScanResult, error) {
	out := ScanResult{}
	type diskFile struct {
		rel   string
		size  int64
		mtime int64
	}
	var files []diskFile
	err := filepath.WalkDir(v.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个文件读失败不影响整体
		}
		if d.IsDir() {
			if ignoreDirs[strings.ToLower(d.Name())] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(v.root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.Size() > MaxNoteBytes {
			out.Errors = append(out.Errors, rel+"：超过单篇大小上限，已跳过")
			return nil
		}
		out.Scanned++
		files = append(files, diskFile{rel: rel, size: info.Size(), mtime: info.ModTime().UnixMilli()})
		return nil
	})
	if err != nil {
		return out, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	known := map[string][2]int64{} // key → {size, mtime}
	rows, err := v.db.Query(`SELECT key,size,mtime FROM notes`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k string
		var size, mtime int64
		if err := rows.Scan(&k, &size, &mtime); err != nil {
			rows.Close()
			return out, err
		}
		known[k] = [2]int64{size, mtime}
	}
	rows.Close()

	now := time.Now().UnixMilli()
	seen := map[string]bool{}
	for _, f := range files {
		key := keyOf(f.rel)
		seen[key] = true
		if prev, ok := known[key]; ok && prev[0] == f.size && prev[1] == f.mtime {
			continue
		}
		abs := filepath.Join(v.root, filepath.FromSlash(f.rel))
		content, err := os.ReadFile(abs)
		if err != nil {
			out.Errors = append(out.Errors, f.rel+"：读取失败")
			continue
		}
		if _, ok := known[key]; ok {
			out.Updated++
		} else {
			out.Added++
		}
		if err := v.indexOneLocked(f.rel, string(content), f.size, f.mtime, now); err != nil {
			out.Errors = append(out.Errors, f.rel+"：索引失败 "+err.Error())
		}
	}

	for key, p := range known {
		if seen[key] {
			continue
		}
		_ = p
		if _, err := v.db.Exec(`DELETE FROM notes WHERE key=?`, key); err != nil {
			continue
		}
		_, _ = v.db.Exec(`DELETE FROM note_links WHERE from_key=?`, key)
		_, _ = v.db.Exec(`DELETE FROM note_tags WHERE note_key=?`, key)
		_, _ = v.db.Exec(`DELETE FROM note_terms WHERE note_key=?`, key)
		_, _ = v.db.Exec(`UPDATE note_links SET to_key='' WHERE to_key=?`, key)
		out.Removed++
	}

	resolved, err := v.resolveLocked()
	if err != nil {
		return out, err
	}
	out.Resolved = resolved
	return out, nil
}

// ---------- 列表 / 标签 / 反链 ----------

// ListOptions 列表过滤
type ListOptions struct {
	Tag     string
	Query   string
	Limit   int
	Offset  int
	SortAsc bool // 默认按更新时间倒序
}

// List 列出笔记
func (v *Vault) List(opt ListOptions) ([]NoteSummary, int, error) {
	if opt.Limit <= 0 {
		opt.Limit = 50
	}
	if opt.Offset < 0 {
		opt.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if t := normalizeTag(opt.Tag); t != "" {
		where = append(where, "EXISTS(SELECT 1 FROM note_tags t WHERE t.note_key=n.key AND lower(t.tag)=lower(?))")
		args = append(args, t)
	}
	if q := strings.TrimSpace(opt.Query); q != "" {
		where = append(where, "(n.title LIKE ? OR n.content LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	order := "n.updated_at DESC"
	if opt.SortAsc {
		order = "n.updated_at ASC"
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	var total int
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM notes n WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	qargs := append(args, opt.Limit, opt.Offset)
	rows, err := v.db.Query(`SELECT n.key,n.path,n.title,n.aliases,n.tags,n.content,n.size,n.tokens,n.created_at,n.updated_at
		FROM notes n WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ? OFFSET ?`, qargs...)
	if err != nil {
		return nil, 0, err
	}
	out := []NoteSummary{}
	for rows.Next() {
		var s NoteSummary
		var aliasesJSON, tagsJSON, content string
		if err := rows.Scan(&s.Key, &s.Path, &s.Title, &aliasesJSON, &tagsJSON, &content,
			&s.Size, &s.Tokens, &s.CreatedAt, &s.UpdatedAt); err != nil {
			rows.Close()
			return nil, 0, err
		}
		_ = json.Unmarshal([]byte(aliasesJSON), &s.Aliases)
		_ = json.Unmarshal([]byte(tagsJSON), &s.Tags)
		if s.Aliases == nil {
			s.Aliases = []string{}
		}
		if s.Tags == nil {
			s.Tags = []string{}
		}
		s.Excerpt = excerpt(content, 160)
		out = append(out, s)
	}
	rows.Close()
	return out, total, rows.Err()
}

// Tags 标签云
func (v *Vault) Tags() ([]TagCount, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	rows, err := v.db.Query(`SELECT tag,COUNT(*) FROM note_tags GROUP BY lower(tag) ORDER BY COUNT(*) DESC, tag`)
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

// Backlinks 谁链到我
func (v *Vault) Backlinks(key string) ([]Backlink, error) {
	rel, err := cleanPath(key)
	if err != nil {
		return nil, err
	}
	k := keyOf(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	rows, err := v.db.Query(`SELECT l.from_key,n.title,n.path,l.alias,l.anchor,l.embed
		FROM note_links l JOIN notes n ON n.key=l.from_key
		WHERE l.to_key=? AND l.from_key<>? ORDER BY n.title`, k, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backlink{}
	for rows.Next() {
		var b Backlink
		var embed int
		if err := rows.Scan(&b.NoteKey, &b.Title, &b.Path, &b.Alias, &b.Anchor, &embed); err != nil {
			rows.Close()
			return nil, err
		}
		b.Embed = embed > 0
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 连接池只有一条连接：必须先把上面的 rows 关掉，才能再查标签
	// （否则第二个查询会一直等这条连接，看起来就像死锁）。
	for i := range out {
		out[i].Tags = v.tagsOfLocked(out[i].NoteKey)
	}
	return out, nil
}

// Outlinks 我链到谁（含悬空）
func (v *Vault) Outlinks(key string) ([]Outlink, error) {
	rel, err := cleanPath(key)
	if err != nil {
		return nil, err
	}
	k := keyOf(rel)
	v.mu.Lock()
	defer v.mu.Unlock()
	rows, err := v.db.Query(`SELECT l.target,l.to_key,l.anchor,l.embed,
		COALESCE(n.title,''),COALESCE(n.path,'')
		FROM note_links l LEFT JOIN notes n ON n.key=l.to_key
		WHERE l.from_key=? ORDER BY l.line,l.target`, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Outlink{}
	for rows.Next() {
		var o Outlink
		var embed int
		if err := rows.Scan(&o.Target, &o.NoteKey, &o.Anchor, &embed, &o.Title, &o.Path); err != nil {
			return nil, err
		}
		o.Embed = embed > 0
		o.Missing = o.NoteKey == ""
		out = append(out, o)
	}
	return out, rows.Err()
}

func (v *Vault) tagsOfLocked(key string) []string {
	rows, err := v.db.Query(`SELECT tag FROM note_tags WHERE note_key=? ORDER BY tag`, key)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// Stats 概览
func (v *Vault) Stats() (Stats, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := Stats{}
	if err := v.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(tokens),0) FROM notes`).Scan(&out.Notes, &out.Tokens); err != nil {
		return out, err
	}
	if err := v.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN to_key<>'' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN to_key='' THEN 1 ELSE 0 END),0)
		FROM note_links`).Scan(&out.Links, &out.Missing); err != nil {
		return out, err
	}
	if err := v.db.QueryRow(`SELECT COUNT(DISTINCT lower(tag)) FROM note_tags`).Scan(&out.Tags); err != nil {
		return out, err
	}
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM notes n WHERE NOT EXISTS(
		SELECT 1 FROM note_links l WHERE l.from_key=n.key AND l.to_key<>''
		UNION ALL
		SELECT 1 FROM note_links l2 WHERE l2.to_key=n.key)`).Scan(&out.Orphans); err != nil {
		return out, err
	}
	if err := v.db.QueryRow(`SELECT COUNT(*) FROM note_terms`).Scan(&out.IndexedTerms); err != nil {
		return out, err
	}
	var last string
	_ = v.db.QueryRow(`SELECT v FROM note_meta WHERE k='last_scan_at'`).Scan(&last)
	if last != "" {
		var n int64
		fmt.Sscan(last, &n)
		out.LastScanAt = n
	}
	return out, nil
}

func excerpt(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	return utf8Truncate(s, maxRunes)
}
