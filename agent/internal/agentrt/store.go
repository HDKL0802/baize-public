package agentrt

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"baize/internal/llm"
)

// RunRecord 一次运行的落库记录
type RunRecord struct {
	RunID        string         `json:"runId"`
	SessionID    string         `json:"sessionId"`
	Recipe       string         `json:"recipe"`
	Goal         string         `json:"goal"`
	Status       string         `json:"status"` // done | failed
	Text         string         `json:"text"`
	Steps        int            `json:"steps"`
	ToolCalls    int            `json:"toolCalls"`
	Retries      int            `json:"retries"`
	PromptTokens int            `json:"promptTokens"`
	OutTokens    int            `json:"outTokens"`
	StartedAt    int64          `json:"startedAt"`
	FinishedAt   int64          `json:"finishedAt"`
	Err          string         `json:"error,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

// Store 运行记录与对话消息的持久化（<dataDir>/runs.db）
type Store struct {
	db *sql.DB
}

const runSchema = `
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL DEFAULT '',
  recipe TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  steps INTEGER NOT NULL DEFAULT 0,
  tool_calls INTEGER NOT NULL DEFAULT 0,
  retries INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  out_tokens INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  meta TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL DEFAULT '',
  idx INTEGER NOT NULL DEFAULT 0,
  role TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  tool_calls TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_run ON messages(run_id, idx);
CREATE TABLE IF NOT EXISTS scroll_chunks(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL DEFAULT '',
  start_idx INTEGER NOT NULL DEFAULT 0,
  end_idx INTEGER NOT NULL DEFAULT 0,
  summary TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_scroll_run ON scroll_chunks(run_id, start_idx);
`

// OpenStore 打开运行记录库
func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "runs.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开运行记录库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(runSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化运行记录表失败：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

// SaveRun 写入/更新一次运行
func (s *Store) SaveRun(r RunRecord) error {
	if s == nil {
		return nil
	}
	meta, err := json.Marshal(nonNilMap(r.Meta))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO runs(id,session_id,recipe,goal,status,text,steps,tool_calls,retries,
		prompt_tokens,out_tokens,started_at,finished_at,error,meta)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, text=excluded.text, steps=excluded.steps,
		tool_calls=excluded.tool_calls, retries=excluded.retries, prompt_tokens=excluded.prompt_tokens,
		out_tokens=excluded.out_tokens, finished_at=excluded.finished_at, error=excluded.error, meta=excluded.meta`,
		r.RunID, r.SessionID, r.Recipe, r.Goal, r.Status, r.Text, r.Steps, r.ToolCalls, r.Retries,
		r.PromptTokens, r.OutTokens, r.StartedAt, r.FinishedAt, r.Err, string(meta))
	return err
}

// AppendMessage 追加一条对话消息
func (s *Store) AppendMessage(runID string, idx int, m llm.Message) error {
	if s == nil {
		return nil
	}
	tc, err := json.Marshal(m.ToolCalls)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO messages(run_id,idx,role,content,tool_calls,tool_call_id,name,at)
		VALUES(?,?,?,?,?,?,?,?)`,
		runID, idx, m.Role, m.Content, string(tc), m.ToolCallID, m.Name, time.Now().UnixMilli())
	return err
}

// Runs 最近运行
func (s *Store) Runs(limit int) ([]RunRecord, error) {
	if s == nil {
		return []RunRecord{}, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id,session_id,recipe,goal,status,text,steps,tool_calls,retries,
		prompt_tokens,out_tokens,started_at,finished_at,error,meta FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		var r RunRecord
		var meta string
		if err := rows.Scan(&r.RunID, &r.SessionID, &r.Recipe, &r.Goal, &r.Status, &r.Text, &r.Steps,
			&r.ToolCalls, &r.Retries, &r.PromptTokens, &r.OutTokens, &r.StartedAt, &r.FinishedAt,
			&r.Err, &meta); err != nil {
			return nil, err
		}
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &r.Meta)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Run 取单次运行
func (s *Store) Run(id string) (RunRecord, bool, error) {
	if s == nil {
		return RunRecord{}, false, nil
	}
	var r RunRecord
	var meta string
	err := s.db.QueryRow(`SELECT id,session_id,recipe,goal,status,text,steps,tool_calls,retries,
		prompt_tokens,out_tokens,started_at,finished_at,error,meta FROM runs WHERE id=?`, id).
		Scan(&r.RunID, &r.SessionID, &r.Recipe, &r.Goal, &r.Status, &r.Text, &r.Steps, &r.ToolCalls,
			&r.Retries, &r.PromptTokens, &r.OutTokens, &r.StartedAt, &r.FinishedAt, &r.Err, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, err
	}
	if meta != "" {
		_ = json.Unmarshal([]byte(meta), &r.Meta)
	}
	return r, true, nil
}

// Messages 某次运行的全部消息
func (s *Store) Messages(runID string) ([]llm.Message, error) {
	if s == nil {
		return []llm.Message{}, nil
	}
	rows, err := s.db.Query(`SELECT role,content,tool_calls,tool_call_id,name FROM messages
		WHERE run_id=? ORDER BY idx`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []llm.Message{}
	for rows.Next() {
		var m llm.Message
		var tc string
		if err := rows.Scan(&m.Role, &m.Content, &tc, &m.ToolCallID, &m.Name); err != nil {
			return nil, err
		}
		if tc != "" && tc != "null" {
			_ = json.Unmarshal([]byte(tc), &m.ToolCalls)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveScrollChunk 记录一批"被滚出上下文"的原文区间（原文本身还在 messages 表里）
func (s *Store) SaveScrollChunk(c ScrollChunk) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO scroll_chunks(run_id,start_idx,end_idx,summary,tokens,at)
		VALUES(?,?,?,?,?,?)`,
		c.RunID, c.StartIdx, c.EndIdx, c.Summary, c.Tokens, time.Now().UnixMilli())
	return err
}

// ScrollChunks 某次运行的滚出记录（按区间从前到后）
func (s *Store) ScrollChunks(runID string) ([]ScrollChunk, error) {
	if s == nil {
		return []ScrollChunk{}, nil
	}
	rows, err := s.db.Query(`SELECT run_id,start_idx,end_idx,summary,tokens,at FROM scroll_chunks
		WHERE run_id=? ORDER BY start_idx`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScrollChunk{}
	for rows.Next() {
		var c ScrollChunk
		if err := rows.Scan(&c.RunID, &c.StartIdx, &c.EndIdx, &c.Summary, &c.Tokens, &c.At); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MessagesRange 按原始下标区间取回对话消息（Scroll 回放用）。
// from/to 是 messages 表里的 idx；to < 0 表示到末尾；limit <= 0 用默认值。
func (s *Store) MessagesRange(runID string, from, to, limit int) ([]llm.Message, error) {
	if s == nil {
		return []llm.Message{}, nil
	}
	if limit <= 0 {
		limit = 40
	}
	if from < 0 {
		from = 0
	}
	q := `SELECT idx,role,content,tool_calls,tool_call_id,name FROM messages WHERE run_id=? AND idx>=?`
	args := []any{runID, from}
	if to >= 0 {
		q += ` AND idx<?`
		args = append(args, to)
	}
	q += ` ORDER BY idx LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []llm.Message{}
	for rows.Next() {
		var m llm.Message
		var idx int
		var tc string
		if err := rows.Scan(&idx, &m.Role, &m.Content, &tc, &m.ToolCallID, &m.Name); err != nil {
			return nil, err
		}
		if tc != "" && tc != "null" {
			_ = json.Unmarshal([]byte(tc), &m.ToolCalls)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
