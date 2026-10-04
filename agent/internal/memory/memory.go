// Package memory 是长期记忆服务（OpenHuman Memory Tree 的 Go 重写，MIT 独立实现）。
//
// 记忆管线（与开发文档 §5.1 一致）：
//
//	数据源 → 规范化 Markdown → 分块(≤3k token) → 多维评分(重要度/新鲜度/信息量/使用频次)
//	      → 层级摘要树(按天 L1、按周 L2) → 落 SQLite + 导出 Obsidian 兼容 .md
//
// 检索走关键词打分 + 记忆自身评分：既看"匹配得好不好"，也看"这条记忆值不值得信"。
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"baize/internal/llm"

	_ "modernc.org/sqlite"
)

// DefaultMaxTokens 单个分块的 token 上限（文档要求 ≤3k）
const DefaultMaxTokens = 3000

// Chunk 记忆分块（记忆树的最小叶子）
type Chunk struct {
	ID         int64          `json:"id"`
	Namespace  string         `json:"namespace"` // 记忆分区（多用户/多智能体隔离）
	Category   string         `json:"category"`  // core / daily / conversation / custom
	DocKey     string         `json:"docKey"`    // 文档级键：同键再写是「覆盖」，不是越堆越多
	Source     string         `json:"source"`    // 数据源：agent / desktop / mobile / file / note …
	SourceRef  string         `json:"sourceRef"` // 来源引用（文件路径 / 任务 id / 会话 id）
	Kind       string         `json:"kind"`      // note / event / task / decision / error / doc
	Title      string         `json:"title"`
	Content    string         `json:"content"`
	Tokens     int            `json:"tokens"`
	Importance float64        `json:"importance"`
	Recency    float64        `json:"recency"`
	Richness   float64        `json:"richness"`
	Score      float64        `json:"score"`
	Hits       int            `json:"hits"`
	NodeID     int64          `json:"nodeId"`
	CreatedAt  int64          `json:"createdAt"`
	UpdatedAt  int64          `json:"updatedAt"`
	HasVector  bool           `json:"hasVector"`            // 是否已落向量（没配 embedding 通道时恒为 false）
	EmbedModel string         `json:"embedModel,omitempty"` // 向量是哪个模型算的（换模型时据此重算）
	Meta       map[string]any `json:"meta,omitempty"`

	vec []float32 // 向量本体，只在检索内部用，不出 JSON
}

// Node 记忆树节点（层级摘要）
type Node struct {
	ID        int64  `json:"id"`
	Level     int    `json:"level"` // 1=天 2=周
	ParentID  int64  `json:"parentId"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Tokens    int    `json:"tokens"`
	Day       string `json:"day"` // L1 用："2006-01-02"
	Week      string `json:"week"`
	Chunks    int    `json:"chunkCount"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Stats 记忆库概览
type Stats struct {
	Chunks   int   `json:"chunks"`
	Nodes    int   `json:"nodes"`
	Tokens   int64 `json:"tokens"`
	Days     int   `json:"days"`
	LastSeen int64 `json:"lastSeen"`
	Embedded int   `json:"embedded"`         // 已有向量的条数
	Missing  int   `json:"missingEmbedding"` // 还没有向量的条数（>0 时语义检索搜不到它们）
}

// Summarizer 把若干条文本压成一段摘要（由模型路由提供；没有模型时用抽取式兜底）
type Summarizer interface {
	Summarize(ctx context.Context, title string, texts []string) (string, error)
}

// Store 记忆库
type Store struct {
	db        *sql.DB
	dir       string
	maxTokens int
	mu        sync.Mutex
	embedder  llm.Embedder // 可选：没配 embedding 通道时为 nil，检索退化为关键词
}

// SetEmbedder 接入向量化通道（可随时换成 nil 关掉语义检索）
func (s *Store) SetEmbedder(e llm.Embedder) {
	s.mu.Lock()
	s.embedder = e
	s.mu.Unlock()
}

func (s *Store) embedderRef() llm.Embedder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.embedder
}

const schema = `
CREATE TABLE IF NOT EXISTS chunks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL DEFAULT '',
  source_ref TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'note',
  title TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  importance REAL NOT NULL DEFAULT 0,
  recency REAL NOT NULL DEFAULT 0,
  richness REAL NOT NULL DEFAULT 0,
  score REAL NOT NULL DEFAULT 0,
  hits INTEGER NOT NULL DEFAULT 0,
  node_id INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  meta TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source, source_ref);
CREATE INDEX IF NOT EXISTS idx_chunks_node ON chunks(node_id);
CREATE INDEX IF NOT EXISTS idx_chunks_created ON chunks(created_at);
CREATE TABLE IF NOT EXISTS nodes(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  level INTEGER NOT NULL DEFAULT 1,
  parent_id INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  day TEXT NOT NULL DEFAULT '',
  week TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_nodes_day ON nodes(level, day);
`

// chunkCols chunks 表的列清单（所有查询共用一份，避免加列时各处漏改）
const chunkCols = `id,namespace,category,doc_key,source,source_ref,kind,title,content,tokens,
	importance,recency,richness,score,hits,node_id,created_at,updated_at,
	embedding IS NOT NULL AND length(embedding)>0 AS has_vec,embedding_model,meta`

// chunkColumnMigrations 结构演进：老库（0.5.x 之前）没有分区/键/向量这几列，
// 这里按需补列，幂等，可反复跑。
var chunkColumnMigrations = []struct{ name, ddl string }{
	{"namespace", `ALTER TABLE chunks ADD COLUMN namespace TEXT NOT NULL DEFAULT 'default'`},
	{"category", `ALTER TABLE chunks ADD COLUMN category TEXT NOT NULL DEFAULT 'core'`},
	{"doc_key", `ALTER TABLE chunks ADD COLUMN doc_key TEXT NOT NULL DEFAULT ''`},
	{"embedding", `ALTER TABLE chunks ADD COLUMN embedding BLOB`},
	{"embedding_model", `ALTER TABLE chunks ADD COLUMN embedding_model TEXT NOT NULL DEFAULT ''`},
}

const schemaIndexes = `
CREATE INDEX IF NOT EXISTS idx_chunks_ns_key ON chunks(namespace, doc_key);
CREATE INDEX IF NOT EXISTS idx_chunks_ns_cat ON chunks(namespace, category);
`

// migrate 补齐新列并建索引（补列按需要做，建索引总是跑）
func migrate(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(chunks)`)
	if err != nil {
		return fmt.Errorf("读取记忆表结构失败：%w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for _, m := range chunkColumnMigrations {
		if have[m.name] {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("记忆库补列 %s 失败：%w", m.name, err)
		}
	}
	if _, err := db.Exec(schemaIndexes); err != nil {
		return fmt.Errorf("记忆库建索引失败：%w", err)
	}
	return backfillDocKeys(db)
}

// backfillDocKeys 老数据没有文档键：按内容补一把，这样"再记一遍同一件事"
// 走的是覆盖更新，而不是又堆一条近似重复的记忆。
func backfillDocKeys(db *sql.DB) error {
	// 注意：连接池只有 1 条连接，必须先把结果读完再写，否则会自己把自己锁住
	type row struct {
		id      int64
		content string
	}
	var todo []row
	rows, err := db.Query(`SELECT id,content FROM chunks WHERE doc_key IS NULL OR doc_key=''`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	for _, r := range todo {
		if _, err := db.Exec(`UPDATE chunks SET doc_key=? WHERE id=?`, MakeKey(r.content), r.id); err != nil {
			return fmt.Errorf("补文档键失败（id=%d）：%w", r.id, err)
		}
	}
	return nil
}

// Open 打开（或创建）记忆库，dir/memory.db
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建记忆目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "memory.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开记忆库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化记忆表结构失败：%w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dir: dir, maxTokens: DefaultMaxTokens}, nil
}

// Close 关闭
func (s *Store) Close() error { return s.db.Close() }

// Path 数据文件路径
func (s *Store) Path() string { return filepath.Join(s.dir, "memory.db") }

// IngestOptions 收数参数
type IngestOptions struct {
	Namespace  string // 记忆分区；空 = default
	Category   string // core / daily / conversation / custom；空 = core
	DocKey     string // 文档级键；空 = 按内容自动生成（一样的内容 → 一样的键）
	Source     string
	SourceRef  string
	Kind       string
	Title      string
	Importance float64 // 0 表示按 kind 推断
	Meta       map[string]any
}

// IngestResult 收数结果
type IngestResult struct {
	Chunks   int    `json:"chunks"`
	Tokens   int    `json:"tokens"`
	NodeID   int64  `json:"nodeId"`
	Skipped  int    `json:"skipped"`
	Replaced int    `json:"replaced"` // 同键覆盖掉的旧条数
	DocKey   string `json:"docKey"`
	Embedded int    `json:"embedded"` // 成功落向量的条数
}

// Ingest 把一段原始内容收进记忆管线（无 ctx 版本，给不方便传 ctx 的调用方用）
func (s *Store) Ingest(text string, opt IngestOptions) (IngestResult, error) {
	return s.IngestContext(context.Background(), text, opt)
}

// IngestContext 收数（带 ctx：向量化会走网络，需要能取消/超时）
//
// 写入语义（对齐 OpenHuman）：
//   - 同一个 (分区, 文档键) 再写是**覆盖**：先删掉该键的旧分块再写新的，不会越堆越多；
//   - 同一个 (来源, 来源引用, 内容) 完全重复则直接跳过（幂等）。
func (s *Store) IngestContext(ctx context.Context, text string, opt IngestOptions) (IngestResult, error) {
	clean := NormalizeText(text)
	if strings.TrimSpace(clean) == "" {
		return IngestResult{}, errors.New("内容为空，没有可入库的记忆")
	}
	opt.Namespace = NormalizeNamespace(opt.Namespace)
	opt.Category = NormalizeCategory(opt.Category)
	if strings.TrimSpace(opt.DocKey) == "" {
		opt.DocKey = MakeKey(clean)
	}
	if opt.Kind == "" {
		opt.Kind = "note"
	}
	if opt.Source == "" {
		opt.Source = "agent"
	}
	if opt.Title == "" {
		opt.Title = firstLine(clean)
	}
	if opt.Importance <= 0 {
		opt.Importance = defaultImportance(opt.Kind)
	}

	parts := ChunkText(clean, s.maxTokens)

	// 向量在锁外算：一次向量化是网络调用，绝不能占着库锁
	vecs, embedModel := s.embedTexts(ctx, parts)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 幂等：同一个来源引用 + 同一份内容不重复入库
	var existing int
	if opt.SourceRef != "" {
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM chunks WHERE source=? AND source_ref=? AND content=?`,
			opt.Source, opt.SourceRef, clean).Scan(&existing); err != nil {
			return IngestResult{}, err
		}
		if existing > 0 {
			return IngestResult{Skipped: 1, DocKey: opt.DocKey}, nil
		}
	}

	// 同键覆盖：把这把键下的旧分块清掉（连带清掉空掉的当天节点归属）
	res := IngestResult{DocKey: opt.DocKey}
	var oldIDs []int64
	rows, err := s.db.Query(`SELECT id FROM chunks WHERE namespace=? AND doc_key=?`, opt.Namespace, opt.DocKey)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		oldIDs = append(oldIDs, id)
	}
	rows.Close()
	if len(oldIDs) > 0 {
		if _, err := s.db.Exec(`DELETE FROM chunks WHERE namespace=? AND doc_key=?`, opt.Namespace, opt.DocKey); err != nil {
			return res, err
		}
		res.Replaced = len(oldIDs)
	}

	now := time.Now().UnixMilli()
	// updated_at 用"严格递增"的戳，见 nextStamp 的说明；created_at 照旧用当前时间，
	// 免得跨零点时把一条记忆挤到隔壁那天去。
	stamp := s.nextStamp(now)
	for i, part := range parts {
		title := opt.Title
		if len(parts) > 1 {
			title = fmt.Sprintf("%s（%d/%d）", opt.Title, i+1, len(parts))
		}
		richness := Richness(part)
		importance := clamp01(opt.Importance / 100.0)
		recency := RecencyFactor(now, now)
		score := Score(importance, recency, richness, 0)
		meta, _ := json.Marshal(opt.Meta)
		var blob any
		model := ""
		if i < len(vecs) && len(vecs[i]) > 0 {
			blob = llm.EncodeVec(vecs[i])
			model = embedModel
			res.Embedded++
		}
		r, err := s.db.Exec(`INSERT INTO chunks(namespace,category,doc_key,source,source_ref,kind,title,content,tokens,
			importance,recency,richness,score,hits,node_id,created_at,updated_at,meta,embedding,embedding_model)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,?,?)`,
			opt.Namespace, opt.Category, opt.DocKey, opt.Source, opt.SourceRef, opt.Kind, title, part, EstimateTokens(part),
			importance, recency, richness, score, now, stamp, string(meta), blob, model)
		if err != nil {
			return res, err
		}
		id, _ := r.LastInsertId()
		if err := s.attachToDayNode(id, now, 0); err != nil {
			return res, err
		}
		res.Chunks++
		res.Tokens += EstimateTokens(part)
	}
	return res, nil
}

// embedTexts 给分块算向量；没配通道就返回 nil（调用方据此知道"这次没落向量"）
func (s *Store) embedTexts(ctx context.Context, texts []string) ([][]float32, string) {
	e := s.embedderRef()
	if e == nil || len(texts) == 0 {
		return nil, ""
	}
	if ctx == nil {
		ctx = context.Background()
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		// 向量化失败不能让记忆写不进去：内容照存，只是这条没向量
		return nil, ""
	}
	return vecs, e.Name()
}

// attachToDayNode 归属到"这一天"的 L1 节点（不存在就建一个占位节点，摘要稍后由 RebuildTree 补）
func (s *Store) attachToDayNode(chunkID, at, _ int64) error {
	day := time.UnixMilli(at).Format("2006-01-02")
	nodeID, err := s.ensureDayNode(day)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE chunks SET node_id=? WHERE id=?`, nodeID, chunkID)
	return err
}

// nextStamp 给新写入的分块一个**严格递增**的 updated_at。
//
// 为什么要多这一步：记忆树判断"当天摘要过期没"靠的是「分块比节点新」（严格大于，
// 见 StaleDays）。毫秒精度下，同一毫秒里连着写两条，时间戳会相等 —— 相等就会被
// 判成"没过期"，新写进去的内容永远摘不进摘要，而且后台每小时都觉得没事可做。
// 所以这里保证新分块的时间戳一定大于库里已有的最大时间戳。
// 只动 updated_at、不动 created_at：created_at 还担着"这条属于哪一天"的职责。
func (s *Store) nextStamp(now int64) int64 {
	var maxAt int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(updated_at),0) FROM chunks`).Scan(&maxAt); err != nil {
		return now
	}
	if now <= maxAt {
		return maxAt + 1
	}
	return now
}

func (s *Store) ensureDayNode(day string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM nodes WHERE level=1 AND day=?`, day).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := time.Now().UnixMilli()
	r, err := s.db.Exec(`INSERT INTO nodes(level,parent_id,title,summary,tokens,day,week,created_at,updated_at)
		VALUES(1,0,?,'',0,?,'',?,?)`, day+" 记忆", day, now, now)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

/* ---------- 层级摘要树 ---------- */

// RebuildTree 重建摘要树：L1 按天、L2 按周。days 为空表示只重算最近 7 天。
func (s *Store) RebuildTree(ctx context.Context, sum Summarizer, days ...string) (int, error) {
	s.mu.Lock()
	target := days
	if len(target) == 0 {
		rows, err := s.db.Query(`SELECT DISTINCT day FROM nodes WHERE level=1 ORDER BY day DESC LIMIT 7`)
		if err != nil {
			s.mu.Unlock()
			return 0, err
		}
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				rows.Close()
				s.mu.Unlock()
				return 0, err
			}
			target = append(target, d)
		}
		rows.Close()
	}
	s.mu.Unlock()

	updated := 0
	for _, day := range target {
		if strings.TrimSpace(day) == "" {
			continue
		}
		n, err := s.rebuildDay(ctx, sum, day)
		if err != nil {
			return updated, err
		}
		updated += n
	}
	if err := s.rebuildWeeks(ctx, sum); err != nil {
		return updated, err
	}
	return updated, nil
}

// StaleDays 最近 maxDays 天里"摘要还没跟上"的天。
//
// 两种算没跟上：① 摘要还是空的（刚开库、或前几天从没重建过）；
// ② 当天又写进了新记忆（分块的updated_at比节点新）。
// 后台定时重建靠它决定要不要花 LLM 调用——没过期就一次都不调。
func (s *Store) StaleDays(maxDays int) ([]string, error) {
	if maxDays <= 0 {
		maxDays = 7
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT n.day, n.updated_at, n.summary, COALESCE(MAX(c.updated_at),0)
		FROM nodes n LEFT JOIN chunks c ON c.node_id=n.id
		WHERE n.level=1 AND n.day<>''
		GROUP BY n.id ORDER BY n.day DESC LIMIT ?`, maxDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stale := []string{}
	for rows.Next() {
		var day, summary string
		var nodeAt, chunkAt int64
		if err := rows.Scan(&day, &nodeAt, &summary, &chunkAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(summary) == "" || chunkAt > nodeAt {
			stale = append(stale, day)
		}
	}
	return stale, rows.Err()
}

// RebuildTreeIfStale 只重建摘要过期的天（没有过期的一个 LLM 调用都不发），
// 返回真正重建的天数。重建完节点时间会新过当天的分块，所以下一轮它就不再过期，
// 不会出现"每小时把同一份内容摘要一遍"的空转。
func (s *Store) RebuildTreeIfStale(ctx context.Context, sum Summarizer, maxDays int) (int, error) {
	days, err := s.StaleDays(maxDays)
	if err != nil {
		return 0, err
	}
	if len(days) == 0 {
		return 0, nil
	}
	return s.RebuildTree(ctx, sum, days...)
}

func (s *Store) rebuildDay(ctx context.Context, sum Summarizer, day string) (int, error) {
	s.mu.Lock()
	id, err := s.ensureDayNode(day)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	start, end := dayRange(day)
	chunks, err := s.chunksBetween(start, end)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	if len(chunks) == 0 {
		s.mu.Unlock()
		return 0, nil
	}
	texts := make([]string, 0, len(chunks))
	titles := make([]string, 0, len(chunks))
	for _, c := range chunks {
		texts = append(texts, c.Title+"："+c.Content)
		titles = append(titles, c.Title)
	}
	summary := extractive(strings.Join(texts, "\n"), 600)
	if sum != nil {
		if got, err := sum.Summarize(ctx, day+" 的记忆", texts); err == nil && strings.TrimSpace(got) != "" {
			summary = got
		}
	}
	// 摘要时间对齐到"这一天的分块里最新的那条"（而不是墙上时钟的 now）。
	// 这样"摘要是否过期"就是纯粹的"有没有比它更新的分块"，跟两次操作是不是
	// 落在同一毫秒无关；配上 IngestContext 里严格递增的戳，判断变得确定。
	stamp := time.Now().UnixMilli()
	var maxAt int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(updated_at),0) FROM chunks WHERE node_id=?`, id).Scan(&maxAt); err == nil && maxAt > 0 {
		stamp = maxAt
	}
	_, err = s.db.Exec(`UPDATE nodes SET summary=?, tokens=?, updated_at=? WHERE id=?`,
		summary, EstimateTokens(summary), stamp, id)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Store) rebuildWeeks(ctx context.Context, sum Summarizer) error {
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id,day,summary FROM nodes WHERE level=1 ORDER BY day`)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	type dayNode struct {
		id, week, summary string
	}
	byWeek := map[string][]dayNode{}
	for rows.Next() {
		var id int64
		var day, summary string
		if err := rows.Scan(&id, &day, &summary); err != nil {
			rows.Close()
			s.mu.Unlock()
			return err
		}
		d, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			continue
		}
		y, w := d.ISOWeek()
		week := fmt.Sprintf("%d-W%02d", y, w)
		byWeek[week] = append(byWeek[week], dayNode{id: fmt.Sprint(id), week: week, summary: summary})
	}
	rows.Close()
	s.mu.Unlock()

	now := time.Now().UnixMilli()
	for week, list := range byWeek {
		texts := make([]string, 0, len(list))
		for _, n := range list {
			if strings.TrimSpace(n.summary) != "" {
				texts = append(texts, n.summary)
			}
		}
		if len(texts) == 0 {
			continue
		}
		summary := extractive(strings.Join(texts, "\n"), 800)
		if sum != nil {
			if got, err := sum.Summarize(ctx, week+" 的记忆", texts); err == nil && strings.TrimSpace(got) != "" {
				summary = got
			}
		}
		s.mu.Lock()
		var nodeID int64
		err := s.db.QueryRow(`SELECT id FROM nodes WHERE level=2 AND week=?`, week).Scan(&nodeID)
		switch {
		case err == nil:
			_, err = s.db.Exec(`UPDATE nodes SET summary=?, tokens=?, updated_at=? WHERE id=?`,
				summary, EstimateTokens(summary), now, nodeID)
		case errors.Is(err, sql.ErrNoRows):
			var r sql.Result
			r, err = s.db.Exec(`INSERT INTO nodes(level,parent_id,title,summary,tokens,day,week,created_at,updated_at)
				VALUES(2,0,?,'',0,'',?,?,?)`, week+" 周记忆", week, now, now)
			if err == nil {
				nodeID, _ = r.LastInsertId()
				_, err = s.db.Exec(`UPDATE nodes SET summary=?, tokens=? WHERE id=?`, summary, EstimateTokens(summary), nodeID)
			}
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// Tree 取摘要树（L2 在前，其下挂 L1）
func (s *Store) Tree(limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 60
	}
	rows, err := s.db.Query(`SELECT id,level,parent_id,title,summary,tokens,day,week,
		(SELECT COUNT(*) FROM chunks c WHERE c.node_id=nodes.id) AS chunk_count,
		created_at,updated_at FROM nodes ORDER BY week DESC, day DESC, level DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Level, &n.ParentID, &n.Title, &n.Summary, &n.Tokens,
			&n.Day, &n.Week, &n.Chunks, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

/* ---------- 检索 ---------- */

// Parts 一条命中的分项得分（让模型/人看得出"为什么排前面"）
type Parts struct {
	Graph     float64 `json:"graph"`     // 联想：与其它命中同源/同天节点的程度
	Vector    float64 `json:"vector"`    // 语义相似度（没配 embedding 通道时恒为 0）
	Keyword   float64 `json:"keyword"`   // 关键词命中
	Freshness float64 `json:"freshness"` // 新鲜度
}

// Hit 一条检索结果
type Hit struct {
	Chunk Chunk   `json:"chunk"`
	Score float64 `json:"score"`
	Why   string  `json:"why"`
	Parts Parts   `json:"parts,omitempty"`
}

// Search 混合检索（关键词 + 语义 + 联想 + 新鲜度）的便捷入口。
// 具体算法见 hybrid.go；命中会累加使用频次（越常用的记忆权重越高）。
func (s *Store) Search(query string, limit int) ([]Hit, error) {
	res, err := s.HybridSearch(context.Background(), HybridQuery{Text: query, Limit: limit})
	if err != nil {
		return nil, err
	}
	return res.Hits, nil
}

// allChunks 取出（按条件过滤后的）全部记忆，供混合检索在内存里打分。
// 个人记忆库量级不大（几千条以内），一次捞齐再做向量/联想打分比在 SQL 里拼简单得多。
func (s *Store) allChunks(q HybridQuery) ([]Chunk, error) {
	where, args := q.whereSQL()
	sqlText := `SELECT ` + chunkCols + ` FROM chunks WHERE ` + where +
		` ORDER BY created_at DESC LIMIT 5000`
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// vectorsFor 一次把（同样过滤条件下的）向量全捞出来，避免逐条查（BLOB 单独查，不进列表查询）
func (s *Store) vectorsFor(q HybridQuery) (map[int64][]float32, error) {
	where, args := q.whereSQL()
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,embedding FROM chunks WHERE `+where+
		` AND embedding IS NOT NULL AND length(embedding)>0 LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]float32{}
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		if v := llm.DecodeVec(blob); len(v) > 0 {
			out[id] = v
		}
	}
	return out, rows.Err()
}

// bumpHits 命中计数 +1（越常用的记忆越容易再被想起）
func (s *Store) bumpHits(ids []int64) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		_, _ = s.db.Exec(`UPDATE chunks SET hits=hits+1 WHERE id=?`, id)
	}
}

// Recent 最近入库的记忆
func (s *Store) Recent(limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.Query(`SELECT `+chunkCols+` FROM chunks
		ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, Hit{Chunk: c, Score: c.Score, Why: "最近入库"})
	}
	return out, rows.Err()
}

// Stats 概览
func (s *Store) Stats() (Stats, error) {
	// "缺向量"按当前向量模型算：换过模型之后旧向量维度对不上、余弦恒为 0，
	// 也必须算成"缺"，否则界面会显示一切正常、检索却什么都搜不到。
	clause, args := vectorStateClause(s.embedModelName())
	var st Stats
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens),0), COALESCE(MAX(created_at),0),
		COALESCE(SUM(CASE WHEN `+clause+` THEN 1 ELSE 0 END),0) FROM chunks`, args...).
		Scan(&st.Chunks, &st.Tokens, &st.LastSeen, &st.Missing); err != nil {
		return st, err
	}
	st.Embedded = st.Chunks - st.Missing
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&st.Nodes); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT day) FROM nodes WHERE level=1 AND day<>''`).Scan(&st.Days); err != nil {
		return st, err
	}
	return st, nil
}

/* ---------- Obsidian 落地 ---------- */

// WriteObsidian 把记忆树导成 Obsidian 兼容的 Markdown（每天一个文件 + 一个索引）
func (s *Store) WriteObsidian(dir string) (int, error) {
	target := filepath.Join(dir, "memory")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, err
	}
	nodes, err := s.Tree(500)
	if err != nil {
		return 0, err
	}
	written := 0
	var index strings.Builder
	index.WriteString("# 记忆树索引\n\n> 由白泽 Agent 自动生成，勿手工改动结构\n\n")

	for _, n := range nodes {
		if n.Level != 1 || n.Day == "" {
			continue
		}
		chunks, err := s.chunksOfNode(n.ID)
		if err != nil {
			return written, err
		}
		var b strings.Builder
		b.WriteString("---\n")
		b.WriteString("date: " + n.Day + "\n")
		b.WriteString("tags: [baize/memory]\n")
		b.WriteString(fmt.Sprintf("chunks: %d\n", len(chunks)))
		b.WriteString("---\n\n")
		b.WriteString("# " + n.Day + " 记忆\n\n")
		if strings.TrimSpace(n.Summary) != "" {
			b.WriteString("## 摘要\n\n" + strings.TrimSpace(n.Summary) + "\n\n")
		}
		b.WriteString("## 明细\n\n")
		for _, c := range chunks {
			b.WriteString(fmt.Sprintf("- **%s**（%s，重要度 %.0f）\n", c.Title, c.Kind, c.Importance*100))
			for _, line := range strings.Split(strings.TrimSpace(c.Content), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				b.WriteString("  " + line + "\n")
			}
		}
		file := filepath.Join(target, n.Day+".md")
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			return written, err
		}
		written++
		index.WriteString(fmt.Sprintf("- [[%s]] ｜ %d 条\n", n.Day, len(chunks)))
	}
	if err := os.WriteFile(filepath.Join(target, "index.md"), []byte(index.String()), 0o644); err != nil {
		return written, err
	}
	return written, nil
}

func (s *Store) chunksOfNode(nodeID int64) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT `+chunkCols+` FROM chunks WHERE node_id=? ORDER BY created_at`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

/* ---------- 评分与分块（纯函数，便于单测） ---------- */

// NormalizeText 规范化：统一换行、去行尾空白、压缩多余空行
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// EstimateTokens 粗估 token 数：CJK 按 1 字 1 token，其余按 4 字符 1 token
func EstimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + other/4 + 1
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// ChunkText 分块：优先按空行段落，其次按句子，最后硬切；保证每块 ≤ maxTokens
func ChunkText(text string, maxTokens int) []string {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	if EstimateTokens(text) <= maxTokens {
		return []string{strings.TrimSpace(text)}
	}
	paras := strings.Split(text, "\n\n")
	out := []string{}
	cur := ""
	flush := func() {
		if strings.TrimSpace(cur) != "" {
			out = append(out, strings.TrimSpace(cur))
		}
		cur = ""
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if EstimateTokens(p) > maxTokens {
			for _, s := range splitSentences(p) {
				if EstimateTokens(cur+"\n"+s) > maxTokens {
					flush()
				}
				if EstimateTokens(s) > maxTokens {
					for _, piece := range hardSplit(s, maxTokens) {
						out = append(out, piece)
					}
					continue
				}
				cur = joinNonEmpty(cur, s, "\n")
			}
			continue
		}
		if EstimateTokens(cur+"\n\n"+p) > maxTokens {
			flush()
		}
		cur = joinNonEmpty(cur, p, "\n\n")
	}
	flush()
	if len(out) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return out
}

var sentenceEnd = []rune{'。', '！', '？', '；', '\n', '.', '!', '?', ';'}

func splitSentences(p string) []string {
	out := []string{}
	start := 0
	for i, r := range p {
		for _, e := range sentenceEnd {
			if r == e {
				seg := strings.TrimSpace(p[start : i+len(string(r))])
				if seg != "" {
					out = append(out, seg)
				}
				start = i + len(string(r))
				break
			}
		}
	}
	if rest := strings.TrimSpace(p[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

func hardSplit(s string, maxTokens int) []string {
	runes := []rune(s)
	out := []string{}
	perChunk := maxTokens * 3 // 粗估：1 token ≈ 3 个非 CJK 字符
	for start := 0; start < len(runes); start += perChunk {
		end := start + perChunk
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, strings.TrimSpace(string(runes[start:end])))
	}
	return out
}

func joinNonEmpty(a, b, sep string) string {
	if strings.TrimSpace(a) == "" {
		return b
	}
	return a + sep + b
}

// Richness 信息量打分：数字、专有名词、标点密度等启发式，归一到 0-1
func Richness(s string) float64 {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	runes := []rune(s)
	digits, upper, quotes := 0, 0, 0
	for _, r := range runes {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsUpper(r):
			upper++
		case r == '"' || r == '“' || r == '「' || r == '`':
			quotes++
		}
	}
	length := float64(len(runes))
	score := (float64(digits)/length)*1.2 + (float64(upper)/length)*0.8 + (float64(quotes)/length)*1.5
	// 太短的信息量低
	if len(runes) < 20 {
		score *= 0.7
	}
	return clamp01(score)
}

// RecencyFactor 新鲜度：半衰期 7 天
func RecencyFactor(now, at int64) float64 {
	if at <= 0 {
		return 0.5
	}
	days := float64(now-at) / float64(24*time.Hour/time.Millisecond)
	if days < 0 {
		days = 0
	}
	return clamp01(1.0 / (1.0 + days/7.0))
}

// Score 多维评分：重要度 40% + 新鲜度 30% + 信息量 20% + 使用频次 10%
func Score(importance, recency, richness float64, hits int) float64 {
	freq := mathMin(1.0, float64(hits)/10.0)
	return clamp01(0.4*importance + 0.3*recency + 0.2*richness + 0.1*freq)
}

func defaultImportance(kind string) float64 {
	switch strings.ToLower(kind) {
	case "decision":
		return 80
	case "error":
		return 75
	case "task":
		return 65
	case "doc":
		return 55
	case "event":
		return 50
	case "note":
		return 45
	}
	return 50
}

// Tokenize 检索用的切词：CJK 双字 + 拉丁词
func Tokenize(s string) []string {
	out := []string{}
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if w := strings.ToLower(latin.String()); len(w) >= 2 {
			out = append(out, w)
		}
		latin.Reset()
	}
	flushCJK := func() {
		for i := 0; i < len(cjk); i++ {
			if i+1 < len(cjk) {
				out = append(out, string(cjk[i:i+2]))
			}
			if len(cjk) == 1 {
				out = append(out, string(cjk[i]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(unicode.ToLower(r))
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	// 去重
	seen := map[string]bool{}
	uniq := out[:0]
	for _, t := range out {
		if seen[t] {
			continue
		}
		seen[t] = true
		uniq = append(uniq, t)
	}
	return uniq
}

func matchScore(c Chunk, terms []string, idf map[string]float64) (float64, string) {
	title := strings.ToLower(c.Title)
	content := strings.ToLower(c.Content)
	hitTerms := []string{}
	score, total := 0.0, 0.0
	considered := 0
	for _, t := range terms {
		// idf 里没有 = 这个词在库里压根没出现过：不计入分母（免得把整批结果一起拉低）
		w, ok := idf[t]
		if !ok || w <= 0 {
			continue
		}
		total += w
		considered++
		switch {
		case strings.Contains(title, t):
			score += w // 标题命中算满
			hitTerms = append(hitTerms, t)
		case strings.Contains(content, t):
			score += 0.5 * w
			hitTerms = append(hitTerms, t)
		}
	}
	if score == 0 || total <= 0 {
		return 0, ""
	}
	// 命中比例越低越要压一压，避免长文本靠"碰词"冒头。
	// 分母用 considered（库里真有的词）而不是 len(terms)：查询里带了库里不存在的词时，
	// 命中"所有能命中的词"就是满分，不该因为那些词连库都没有而被扣分。
	ratio := float64(len(hitTerms)) / float64(considered)
	final := clamp01(score/total) * (0.6 + 0.4*ratio)
	return final, "命中：" + strings.Join(hitTerms, "、")
}

func scanChunk(sc interface{ Scan(...any) error }) (Chunk, error) {
	var c Chunk
	var meta string
	var hasVec int
	var embedModel sql.NullString
	if err := sc.Scan(&c.ID, &c.Namespace, &c.Category, &c.DocKey, &c.Source, &c.SourceRef,
		&c.Kind, &c.Title, &c.Content, &c.Tokens,
		&c.Importance, &c.Recency, &c.Richness, &c.Score, &c.Hits, &c.NodeID,
		&c.CreatedAt, &c.UpdatedAt, &hasVec, &embedModel, &meta); err != nil {
		return c, err
	}
	c.HasVector = hasVec > 0
	c.EmbedModel = embedModel.String
	if strings.TrimSpace(meta) != "" {
		_ = json.Unmarshal([]byte(meta), &c.Meta)
	}
	return c, nil
}

func (s *Store) chunksBetween(start, end int64) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT `+chunkCols+` FROM chunks
		WHERE created_at>=? AND created_at<? ORDER BY created_at`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chunk{}
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func dayRange(day string) (int64, int64) {
	d, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		now := time.Now()
		d = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	return d.UnixMilli(), d.Add(24 * time.Hour).UnixMilli()
}

// extractive 没有模型时的抽取式摘要：取开头若干 token，并标注总量
func extractive(text string, tokens int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if EstimateTokens(text) <= tokens {
		return text
	}
	runes := []rune(text)
	keep := tokens * 2
	if keep > len(runes) {
		keep = len(runes)
	}
	return strings.TrimSpace(string(runes[:keep])) + fmt.Sprintf("…（共约 %d token）", EstimateTokens(text))
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#>-* "))
		if line != "" {
			if len([]rune(line)) > 40 {
				return string([]rune(line)[:40])
			}
			return line
		}
	}
	return "记忆"
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
