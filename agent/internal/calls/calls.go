// Package calls 是「组内两人之间的通话」记录：文字消息 + 音视频信令（WebRTC 中转）。
//
// 场景：用户组里的两个人想通话（像微信那样互发消息、开音视频），但后端只做
// **信令中转与留言记录**，不碰媒体本身（有没有麦克风/摄像头是客户端的事，
// 与 internal/conflicts 的口径一致）。
//
// 与 conflicts 的区别：conflicts 会话由「同名文档分叉」自动开出、围绕一份文档；
// calls 是**两人之间任意**发起的（可以不带冲突，纯聊天/通话），也可以挂在
// 某个协商会话下面（conflict_sid 非空）——协商时的通话记录会一并交给「回复AI」
// 通读（见 agentsvc.ConflictAIJudge）。
//
// 每个用户组一份 SQLite（<组分区>/calls.db），与 conflicts.db 同目录同生命周期。
package calls

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

// 通话状态
const (
	StatusLive  = "live"  // 进行中
	StatusEnded = "ended" // 已挂断（留言与信令仍留着可查）
)

// Call 两人之间的一次通话（a/b 无序：OpenCall 幂等按「无序对」复用）
type Call struct {
	ID          string `json:"id"`
	GroupID     string `json:"groupId"`
	AID         string `json:"aId"`
	BID         string `json:"bId"`
	AName       string `json:"aName,omitempty"`
	BName       string `json:"bName,omitempty"`
	ConflictSID string `json:"conflictSid,omitempty"` // 挂在哪次文档协商下（空 = 自由通话）
	Status      string `json:"status"`
	OpenedBy    string `json:"openedBy,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`

	// 视图扩展（只有列表/详情接口会填，落库没有这些列）：
	PeerID   string `json:"peerId,omitempty"` // 相对当前查看者，对方是谁
	PeerName string `json:"peerName,omitempty"`
	MsgN     int    `json:"messageCount"`
}

// Message 通话里的一条文字留言
type Message struct {
	ID       int64  `json:"id"`
	FromID   string `json:"fromId,omitempty"`
	FromName string `json:"fromName,omitempty"`
	Text     string `json:"text"`
	At       int64  `json:"at"`
}

// Signal 音视频信令（offer/answer/ice/bye，后端只中转、不解析 payload）
type Signal struct {
	ID      int64  `json:"id"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"` // 空 = 发给通话里的所有人
	Kind    string `json:"kind"`
	Payload string `json:"payload,omitempty"`
	At      int64  `json:"at"`
}

// Store 通话记录（每个用户组一份 SQLite）
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS calls(
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  a_id TEXT NOT NULL,
  b_id TEXT NOT NULL,
  a_name TEXT NOT NULL DEFAULT '',
  b_name TEXT NOT NULL DEFAULT '',
  conflict_sid TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'live',
  opened_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_calls_pair ON calls(group_id, a_id, b_id, status);
CREATE INDEX IF NOT EXISTS idx_calls_conflict ON calls(conflict_sid);
CREATE TABLE IF NOT EXISTS call_messages(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  call_id TEXT NOT NULL,
  from_id TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_call_messages ON call_messages(call_id, id);
CREATE TABLE IF NOT EXISTS call_signals(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  call_id TEXT NOT NULL,
  from_id TEXT NOT NULL DEFAULT '',
  to_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_call_signals ON call_signals(call_id, id);
`

// Open 打开（或创建）某个数据目录下的通话记录库：<dir>/calls.db
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建通话记录目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "calls.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开通话记录库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化通话表结构失败：%w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭
func (s *Store) Close() error { return s.db.Close() }

// OpenCall 开一次通话（幂等：同一用户组内同一对人已有一条 live 的就直接复用；
// 复用期间若带了新的 conflict_sid 就补挂上去——自由通话中途关联到某次协商）。
func (s *Store) OpenCall(groupID, aID, aName, bID, bName, conflictSID string) (Call, error) {
	groupID, aID, bID = strings.TrimSpace(groupID), strings.TrimSpace(aID), strings.TrimSpace(bID)
	if groupID == "" || aID == "" || bID == "" || aID == bID {
		return Call{}, errors.New("通话要组里两个不同的人")
	}
	// 无序对：同一对人不重复开（live 期间）
	rows, err := s.db.Query(`SELECT id FROM calls WHERE group_id=? AND status=? AND
		((a_id=? AND b_id=?) OR (a_id=? AND b_id=?))`,
		groupID, StatusLive, aID, bID, bID, aID)
	if err != nil {
		return Call{}, err
	}
	var existing string
	for rows.Next() {
		if err := rows.Scan(&existing); err != nil {
			rows.Close()
			return Call{}, err
		}
	}
	rows.Close()
	if existing != "" {
		if conflictSID != "" {
			if _, err := s.db.Exec(`UPDATE calls SET conflict_sid=?,updated_at=? WHERE id=? AND conflict_sid=''`,
				conflictSID, time.Now().UnixMilli(), existing); err != nil {
				return Call{}, err
			}
		}
		return s.Call(existing)
	}

	id, err := randID()
	if err != nil {
		return Call{}, err
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO calls(id,group_id,a_id,b_id,a_name,b_name,conflict_sid,status,opened_by,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,'live',?,?,?)`,
		id, groupID, aID, bID, strings.TrimSpace(aName), strings.TrimSpace(bName),
		strings.TrimSpace(conflictSID), aID, now, now); err != nil {
		return Call{}, err
	}
	return s.Call(id)
}

// Call 取一条通话（带未读计数不在此，只有消息总数）
func (s *Store) Call(id string) (Call, error) {
	var x Call
	err := s.db.QueryRow(`SELECT id,group_id,a_id,b_id,a_name,b_name,conflict_sid,status,opened_by,created_at,updated_at,
		(SELECT COUNT(*) FROM call_messages m WHERE m.call_id=calls.id)
		FROM calls WHERE id=?`, id).
		Scan(&x.ID, &x.GroupID, &x.AID, &x.BID, &x.AName, &x.BName,
			&x.ConflictSID, &x.Status, &x.OpenedBy, &x.CreatedAt, &x.UpdatedAt, &x.MsgN)
	if errors.Is(err, sql.ErrNoRows) {
		return Call{}, errors.New("没有这条通话")
	}
	return x, err
}

// CallInGroup 取通话并确认它属于这个用户组（防拿别组的 id 撞库）
func (s *Store) CallInGroup(groupID, id string) (Call, error) {
	x, err := s.Call(id)
	if err != nil {
		return Call{}, err
	}
	if x.GroupID != groupID {
		return Call{}, errors.New("这条通话不属于该用户组")
	}
	return x, nil
}

// Calls 列某人在某用户组里的通话（onlyLive=true 只看进行中的），按查看者视角填 Peer。
func (s *Store) Calls(groupID, userID string, onlyLive bool) ([]Call, error) {
	status := StatusLive
	if !onlyLive {
		status = ""
	}
	q := `SELECT c.id,c.group_id,c.a_id,c.b_id,c.a_name,c.b_name,c.conflict_sid,c.status,c.opened_by,c.created_at,c.updated_at,
		(SELECT COUNT(*) FROM call_messages m WHERE m.call_id=c.id)
		FROM calls c WHERE c.group_id=? AND ((c.a_id=? AND c.b_id<>?) OR (c.b_id=? AND c.a_id<>?))`
	args := []any{groupID, userID, userID, userID, userID}
	if status != "" {
		q += ` AND c.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY c.updated_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		var x Call
		if err := rows.Scan(&x.ID, &x.GroupID, &x.AID, &x.BID, &x.AName, &x.BName,
			&x.ConflictSID, &x.Status, &x.OpenedBy, &x.CreatedAt, &x.UpdatedAt, &x.MsgN); err != nil {
			return nil, err
		}
		// 查看者视角的"对方"
		if x.AID == userID {
			x.PeerID, x.PeerName = x.BID, x.BName
		} else {
			x.PeerID, x.PeerName = x.AID, x.AName
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ByConflict 某次文档协商关联的全部通话（含已挂断的，AI 通读上下文要用）
func (s *Store) ByConflict(groupID, conflictSID string) ([]Call, error) {
	rows, err := s.db.Query(`SELECT id,group_id,a_id,b_id,a_name,b_name,conflict_sid,status,opened_by,created_at,updated_at,
		(SELECT COUNT(*) FROM call_messages m WHERE m.call_id=calls.id)
		FROM calls WHERE group_id=? AND conflict_sid=? ORDER BY created_at`, groupID, conflictSID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		var x Call
		if err := rows.Scan(&x.ID, &x.GroupID, &x.AID, &x.BID, &x.AName, &x.BName,
			&x.ConflictSID, &x.Status, &x.OpenedBy, &x.CreatedAt, &x.UpdatedAt, &x.MsgN); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// End 挂断（状态翻成 ended；留言与信令留着，可回看）
func (s *Store) End(callID, byID string) (Call, error) {
	if _, err := s.Call(callID); err != nil {
		return Call{}, err
	}
	_ = byID
	if _, err := s.db.Exec(`UPDATE calls SET status=?,updated_at=? WHERE id=?`,
		StatusEnded, time.Now().UnixMilli(), callID); err != nil {
		return Call{}, err
	}
	return s.Call(callID)
}

/* ---------- 文字留言 ---------- */

// AddMessage 追加一条留言（fromID 传 "ai"、fromName 传 "白泽AI" 即可写系统留言）
func (s *Store) AddMessage(callID, fromID, fromName, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, errors.New("消息不能为空")
	}
	if len([]rune(text)) > 4000 {
		return Message{}, errors.New("消息太长（最多 4000 字）")
	}
	if _, err := s.Call(callID); err != nil {
		return Message{}, err
	}
	now := time.Now().UnixMilli()
	r, err := s.db.Exec(`INSERT INTO call_messages(call_id,from_id,from_name,text,at) VALUES(?,?,?,?,?)`,
		callID, fromID, fromName, text, now)
	if err != nil {
		return Message{}, err
	}
	if _, err := s.db.Exec(`UPDATE calls SET updated_at=? WHERE id=?`, now, callID); err != nil {
		return Message{}, err
	}
	id, _ := r.LastInsertId()
	return Message{ID: id, FromID: fromID, FromName: fromName, Text: text, At: now}, nil
}

// Messages 取留言（afterID>0 表示只取这之后新增的，轮询用）
func (s *Store) Messages(callID string, afterID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id,from_id,from_name,text,at FROM call_messages
		WHERE call_id=? AND id>? ORDER BY id LIMIT ?`, callID, afterID, limit)
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

/* ---------- 音视频信令中转（与 conflicts 同口径） ---------- */

// PostSignal 记一条信令（offer/answer/ice/bye）。后端不解析 payload，只负责转给对方。
func (s *Store) PostSignal(callID, from, to, kind, payload string) error {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return errors.New("信令 kind 不能为空")
	}
	if len(payload) > 256*1024 {
		return errors.New("信令内容过大")
	}
	if _, err := s.Call(callID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO call_signals(call_id,from_id,to_id,kind,payload,at) VALUES(?,?,?,?,?,?)`,
		callID, from, to, kind, payload, time.Now().UnixMilli())
	return err
}

// Signals 拉信令：to 为空 = 拉全部（含广播）；否则只拉发给自己的或广播的。
// 拉给指定 to 时会过滤掉他自己发的（否则客户端会自己跟自己协商）。
func (s *Store) Signals(callID, to string, afterID int64, limit int) ([]Signal, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id,from_id,to_id,kind,payload,at FROM call_signals
		WHERE call_id=? AND id>? AND (to_id='' OR to_id=? OR ?='')
		ORDER BY id LIMIT ?`, callID, afterID, to, to, limit)
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
		if x.From == to && to != "" {
			continue
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func randID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成通话 id 失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}
