// Package conflicts 是「同一份文档被两个人各改了一版」的协商记录。
//
// 场景（用户原话）：A 和 B 同时改了同一份文档 → 文档分成两版（A 的一版、B 的一版），
// 系统向两人发出请求，让他们在同一个界面里左右对照，各自只能**看**对方那版（不能改），
// 通过聊天把话说清，直到两版合成一致、或某一方放弃自己那版。
//
// 本包只负责"把这件事记下来、讲清楚"：
//   - 会话（Session）：一次协商，属于某个用户组 + 某个文档名；
//   - 版本（Version）：这次协商涉及哪几份文档（谁是作者）；
//   - 聊天（Message）：协商过程中的文字往来（免公网的"聊天链接"就是会话 id）；
//   - 信令（Signal）：音视频用（WebRTC 的 SDP/ICE 中转）。
//
// **不碰媒体本身**：设备有没有麦克风/摄像头是客户端的事，后端只当信令中转站；
// 没有摄像头就退回文字，不需要后端做任何特殊处理。
package conflicts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Version 模块版本
const Version = "0.1.0"

// 会话状态
const (
	StatusOpen      = "open"      // 还在协商
	StatusResolved  = "resolved"  // 已定稿（保留某一版）
	StatusAbandoned = "abandoned" // 双方都不管了（不代表已定稿）
)

// Session 一次协商会话
type Session struct {
	ID          string `json:"id"`
	GroupID     string `json:"groupId"`
	Name        string `json:"name"`   // 文档名（同名才算冲突）
	Status      string `json:"status"` // open | resolved | abandoned
	OpenedBy    string `json:"openedBy,omitempty"`
	ResolvedDoc string `json:"resolvedDoc,omitempty"` // 定稿时保留的那一版
	ResolvedBy  string `json:"resolvedBy,omitempty"`
	VersionN    int    `json:"versionCount"`
	MessageN    int    `json:"messageCount"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	// 「回复AI」闸门：各方都点了确认之后，后端才让模型通读上下文去判定/合并
	AIStatus string `json:"aiStatus,omitempty"` // '' | running | done | failed
	AINote   string `json:"aiNote,omitempty"`
}

// Version 参与协商的一版文档
type DocVersion struct {
	DocID     string `json:"docId"`
	OwnerID   string `json:"ownerId,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Size      int64  `json:"size"`
	At        int64  `json:"at"`
}

// Message 协商里的一条聊天
type Message struct {
	ID       int64  `json:"id"`
	FromID   string `json:"fromId,omitempty"`
	FromName string `json:"fromName,omitempty"`
	Text     string `json:"text"`
	At       int64  `json:"at"`
}

// Signal 音视频信令（SDP/ICE 之类，后端只中转）
type Signal struct {
	ID      int64  `json:"id"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"` // 空 = 发给会话里的所有人
	Kind    string `json:"kind"`         // offer | answer | ice | bye
	Payload string `json:"payload,omitempty"`
	At      int64  `json:"at"`
}

// Store 协商记录（每个用户组一份 SQLite）
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions(
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  opened_by TEXT NOT NULL DEFAULT '',
  resolved_doc TEXT NOT NULL DEFAULT '',
  resolved_by TEXT NOT NULL DEFAULT '',
  ai_status TEXT NOT NULL DEFAULT '',
  ai_note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_group ON sessions(group_id, status, name);
CREATE TABLE IF NOT EXISTS versions(
  session_id TEXT NOT NULL,
  doc_id TEXT NOT NULL,
  owner_id TEXT NOT NULL DEFAULT '',
  owner_name TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(session_id, doc_id)
);
CREATE TABLE IF NOT EXISTS messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  from_id TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);
CREATE TABLE IF NOT EXISTS signals(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  from_id TEXT NOT NULL DEFAULT '',
  to_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_signals_session ON signals(session_id, id);
CREATE TABLE IF NOT EXISTS ai_confirms(
  session_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(session_id, user_id)
);
`

// migrateOld 给 0.9.32 之前建的老库补「回复AI」的两个列（CREATE IF NOT EXISTS
// 不会改已存在的表；重复加列报 duplicate column，忽略即可）
func migrateOld(db *sql.DB) {
	for _, stmt := range []string{
		`ALTER TABLE sessions ADD COLUMN ai_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN ai_note TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			// 其它错误不该静默吞，但也不至于打不开库（列缺了读写会报错，到时再说）
			_ = err
		}
	}
}

// Open 打开（或创建）某个数据目录下的协商记录库：<dir>/conflicts.db
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建协商记录目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "conflicts.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开协商记录库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化协商表结构失败：%w", err)
	}
	migrateOld(db)
	return &Store{db: db}, nil
}

// Close 关闭
func (s *Store) Close() error { return s.db.Close() }

// OpenSession 开一次协商。
//
// 幂等：同一个（用户组，文档名）已经有 open 的会话，就直接复用它并把新版本补进去——
// 两个人在同一份文档上来回改，不该开出七八个会话。
func (s *Store) OpenSession(groupID, name, byID string, vers []DocVersion) (Session, error) {
	groupID = strings.TrimSpace(groupID)
	name = strings.TrimSpace(name)
	if groupID == "" || name == "" {
		return Session{}, errors.New("用户组与文档名都不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, err := s.openSessionLocked(groupID, name); err == nil {
		if len(vers) > 0 {
			if err := s.upsertVersionsLocked(existing.ID, vers); err != nil {
				return Session{}, err
			}
		}
		_ = s.touchLocked(existing.ID)
		return s.sessionLocked(existing.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}

	id, err := randID()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO sessions(id,group_id,name,status,opened_by,created_at,updated_at)
		VALUES(?,?,?,'open',?,?,?)`, id, groupID, name, byID, now, now); err != nil {
		return Session{}, err
	}
	if err := s.upsertVersionsLocked(id, vers); err != nil {
		return Session{}, err
	}
	return s.sessionLocked(id)
}

func (s *Store) openSessionLocked(groupID, name string) (Session, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM sessions WHERE group_id=? AND name=? AND status='open'
		ORDER BY created_at DESC LIMIT 1`, groupID, name).Scan(&id)
	if err != nil {
		return Session{}, err
	}
	return s.sessionLocked(id)
}

func (s *Store) upsertVersionsLocked(sessionID string, vers []DocVersion) error {
	for _, v := range vers {
		if strings.TrimSpace(v.DocID) == "" {
			continue
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO versions(session_id,doc_id,owner_id,owner_name,size,at)
			VALUES(?,?,?,?,?,?)`, sessionID, v.DocID, v.OwnerID, v.OwnerName, v.Size, v.At); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) touchLocked(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, time.Now().UnixMilli(), sessionID)
	return err
}

// Sessions 列出某个用户组的协商会话（onlyOpen=true 只列还在协商的）
func (s *Store) Sessions(groupID string, onlyOpen bool) ([]Session, error) {
	where := `group_id=?`
	if onlyOpen {
		where += ` AND status='open'`
	}
	rows, err := s.db.Query(`SELECT id,group_id,name,status,opened_by,resolved_doc,resolved_by,ai_status,ai_note,created_at,updated_at,
		(SELECT COUNT(*) FROM versions v WHERE v.session_id=sessions.id),
		(SELECT COUNT(*) FROM messages m WHERE m.session_id=sessions.id)
		FROM sessions WHERE `+where+` ORDER BY updated_at DESC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.GroupID, &x.Name, &x.Status, &x.OpenedBy,
			&x.ResolvedDoc, &x.ResolvedBy, &x.AIStatus, &x.AINote,
			&x.CreatedAt, &x.UpdatedAt, &x.VersionN, &x.MessageN); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Session 取一个会话
func (s *Store) Session(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionLocked(id)
}

func (s *Store) sessionLocked(id string) (Session, error) {
	var x Session
	err := s.db.QueryRow(`SELECT id,group_id,name,status,opened_by,resolved_doc,resolved_by,ai_status,ai_note,created_at,updated_at,
		(SELECT COUNT(*) FROM versions v WHERE v.session_id=sessions.id),
		(SELECT COUNT(*) FROM messages m WHERE m.session_id=sessions.id)
		FROM sessions WHERE id=?`, id).
		Scan(&x.ID, &x.GroupID, &x.Name, &x.Status, &x.OpenedBy,
			&x.ResolvedDoc, &x.ResolvedBy, &x.AIStatus, &x.AINote,
			&x.CreatedAt, &x.UpdatedAt, &x.VersionN, &x.MessageN)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, errors.New("没有这个协商会话")
	}
	return x, err
}

// Versions 会话涉及的几版文档
func (s *Store) Versions(sessionID string) ([]DocVersion, error) {
	rows, err := s.db.Query(`SELECT doc_id,owner_id,owner_name,size,at FROM versions
		WHERE session_id=? ORDER BY at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DocVersion{}
	for rows.Next() {
		var v DocVersion
		if err := rows.Scan(&v.DocID, &v.OwnerID, &v.OwnerName, &v.Size, &v.At); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Resolve 定稿：保留 docID 那一版（其余版本建议由调用方清理）
func (s *Store) Resolve(sessionID, docID, byID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(docID) == "" {
		return Session{}, errors.New("要保留哪一版（docId）不能为空")
	}
	if _, err := s.sessionLocked(sessionID); err != nil {
		return Session{}, err
	}
	if _, err := s.db.Exec(`UPDATE sessions SET status='resolved',resolved_doc=?,resolved_by=?,updated_at=?
		WHERE id=?`, docID, byID, time.Now().UnixMilli(), sessionID); err != nil {
		return Session{}, err
	}
	return s.sessionLocked(sessionID)
}

// GiveUp 某一方放弃自己那版：对方的版本自动成为定稿。
// 正好两版才判定得出来"对方是哪一版"；超过两版就只标记为"已放弃"，由人再定。
func (s *Store) GiveUp(sessionID, byID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sessionLocked(sessionID); err != nil {
		return Session{}, err
	}
	rows, err := s.db.Query(`SELECT doc_id,owner_id FROM versions WHERE session_id=?`, sessionID)
	if err != nil {
		return Session{}, err
	}
	var keep string
	type pair struct{ doc, owner string }
	all := []pair{}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.doc, &p.owner); err != nil {
			rows.Close()
			return Session{}, err
		}
		all = append(all, p)
	}
	rows.Close()
	if len(all) == 2 {
		for _, p := range all {
			if p.owner != byID {
				keep = p.doc
			}
		}
	}
	if keep == "" {
		// 判不出对方那版（>2 版 / 作者缺失）：只标"已放弃"，不定稿
		if _, err := s.db.Exec(`UPDATE sessions SET status='abandoned',resolved_by=?,updated_at=? WHERE id=?`,
			byID, time.Now().UnixMilli(), sessionID); err != nil {
			return Session{}, err
		}
		return s.sessionLocked(sessionID)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET status='resolved',resolved_doc=?,resolved_by=?,updated_at=?
		WHERE id=?`, keep, byID, time.Now().UnixMilli(), sessionID); err != nil {
		return Session{}, err
	}
	return s.sessionLocked(sessionID)
}

/* ---------- 聊天 ---------- */

// AddMessage 追加一条聊天
func (s *Store) AddMessage(sessionID, fromID, fromName, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, errors.New("消息不能为空")
	}
	if len([]rune(text)) > 4000 {
		return Message{}, errors.New("消息太长（最多 4000 字）")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sessionLocked(sessionID); err != nil {
		return Message{}, err
	}
	now := time.Now().UnixMilli()
	r, err := s.db.Exec(`INSERT INTO messages(session_id,from_id,from_name,text,at) VALUES(?,?,?,?,?)`,
		sessionID, fromID, fromName, text, now)
	if err != nil {
		return Message{}, err
	}
	_ = s.touchLocked(sessionID)
	id, _ := r.LastInsertId()
	return Message{ID: id, FromID: fromID, FromName: fromName, Text: text, At: now}, nil
}

// Messages 取会话里的聊天（afterID>0 表示只取这之后新增的，轮询用）
func (s *Store) Messages(sessionID string, afterID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id,from_id,from_name,text,at FROM messages
		WHERE session_id=? AND id>? ORDER BY id LIMIT ?`, sessionID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.FromID, &m.FromName, &m.Text, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

/* ---------- 音视频信令中转 ---------- */

// PostSignal 记一条信令（offer/answer/ice/bye）。后端不解析 payload，只负责中转。
func (s *Store) PostSignal(sessionID, from, to, kind, payload string) error {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return errors.New("信令 kind 不能为空")
	}
	if len(payload) > 256*1024 {
		return errors.New("信令内容过大")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sessionLocked(sessionID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO signals(session_id,from_id,to_id,kind,payload,at) VALUES(?,?,?,?,?,?)`,
		sessionID, from, to, kind, payload, time.Now().UnixMilli())
	return err
}

// Signals 拉取信令：to 为空 = 拉全部（含广播）；否则只拉发给自己的或广播的。
// afterID 支持增量轮询；拉到的信令会被调用方用完即弃（这里不做删除，靠会话生命周期清理）。
func (s *Store) Signals(sessionID, to string, afterID int64, limit int) ([]Signal, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id,from_id,to_id,kind,payload,at FROM signals
		WHERE session_id=? AND id>? AND (to_id='' OR to_id=? OR ?='')
		ORDER BY id LIMIT ?`, sessionID, afterID, to, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signal{}
	for rows.Next() {
		var x Signal
		if err := rows.Scan(&x.ID, &x.From, &x.To, &x.Kind, &x.Payload, &x.At); err != nil {
			return nil, err
		}
		// 不回显自己发的（否则客户端会自己跟自己协商）
		if x.From == to && to != "" {
			continue
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

/* ---------- 「回复AI」确认闸 ---------- */

// ConfirmAI 某一方点了「回复AI」。
//
// 只有**每一版的作者**都确认过，才返回 allConfirmed=true（调用方据此触发
// AI 通读判定）。作者缺失（versions 里没记 owner，早期数据）时按"已确认"
// 处理——不能让人卡在一个补不回来的确认上。
// 幂等：同一人点多次只留第一次。
func (s *Store) ConfirmAI(sessionID, byID string) (bool, []string, error) {
	byID = strings.TrimSpace(byID)
	if byID == "" {
		return false, nil, errors.New("请先登录再确认")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sessionLocked(sessionID); err != nil {
		return false, nil, err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO ai_confirms(session_id,user_id,at) VALUES(?,?,?)`,
		sessionID, byID, time.Now().UnixMilli()); err != nil {
		return false, nil, err
	}
	// 每一版的作者（去重、去空）都要确认
	rows, err := s.db.Query(`SELECT DISTINCT owner_id FROM versions WHERE session_id=? AND owner_id<>''`, sessionID)
	if err != nil {
		return false, nil, err
	}
	owners := []string{}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return false, nil, err
		}
		owners = append(owners, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, nil, err
	}
	if len(owners) == 0 { // 早期数据没记作者：一个人确认就放行
		return true, nil, nil
	}
	confirmed, err := s.confirmedOwnersLocked(sessionID)
	if err != nil {
		return false, nil, err
	}
	missing := []string{}
	for _, o := range owners {
		if !confirmed[o] {
			missing = append(missing, o)
		}
	}
	return len(missing) == 0, missing, nil
}

func (s *Store) confirmedOwnersLocked(sessionID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT user_id FROM ai_confirms WHERE session_id=?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// SetAIStatus 记 AI 判定/合并的进度（” = 没跑；running/done/failed）
func (s *Store) SetAIStatus(sessionID, status, note string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.sessionLocked(sessionID); err != nil {
		return Session{}, err
	}
	if _, err := s.db.Exec(`UPDATE sessions SET ai_status=?,ai_note=?,updated_at=? WHERE id=?`,
		status, note, time.Now().UnixMilli(), sessionID); err != nil {
		return Session{}, err
	}
	return s.sessionLocked(sessionID)
}

// AIConfirmers 这个会话里已经点过「回复AI」的 userId 列表
func (s *Store) AIConfirmers(sessionID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT user_id FROM ai_confirms WHERE session_id=? ORDER BY at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func randID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话 id 失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}
