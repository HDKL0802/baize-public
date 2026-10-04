// Package store 负责后端 Agent 的持久化（SQLite，纯 Go 驱动 modernc.org/sqlite）。
//
// 四张表：
//
//	devices  —— 设备注册表
//	tasks    —— 统一任务库（三态流转与审批都在这里留痕）
//	records  —— 三态记录流水：事件 event / 指令 command / 回执 receipt / 审批 approval
//	memories —— 记忆落盘（任务结果、设备上下线等沉淀成的记忆条目）
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Device 设备
type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Hostname   string   `json:"hostname"`
	User       string   `json:"user"`
	Version    string   `json:"version"`
	Caps       []string `json:"caps"`
	Online     bool     `json:"online"`
	RemoteAddr string   `json:"remoteAddr"`
	FirstSeen  int64    `json:"firstSeen"`
	LastSeen   int64    `json:"lastSeen"`
}

// Task 统一任务
type Task struct {
	ID           string         `json:"id"`
	DeviceID     string         `json:"deviceId"`
	Action       string         `json:"action"`
	Args         map[string]any `json:"args,omitempty"`
	Status       string         `json:"status"`
	NeedApproval bool           `json:"needApproval"`
	ApprovedBy   string         `json:"approvedBy,omitempty"`
	Origin       string         `json:"origin"`            // user | agent
	IdemKey      string         `json:"idemKey,omitempty"` // 幂等键：同设备同键的在途任务会被复用
	CreatedAt    int64          `json:"createdAt"`
	DispatchedAt int64          `json:"dispatchedAt,omitempty"`
	FinishedAt   int64          `json:"finishedAt,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// Record 三态/审批记录
type Record struct {
	ID       int64          `json:"id"`
	Kind     string         `json:"kind"`
	DeviceID string         `json:"deviceId"`
	TaskID   string         `json:"taskId,omitempty"`
	At       int64          `json:"at"`
	Payload  map[string]any `json:"payload,omitempty"`
}

// Memory 记忆条目
type Memory struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	DeviceID   string `json:"deviceId,omitempty"`
	TaskID     string `json:"taskId,omitempty"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Importance int    `json:"importance"` // 0-100
	At         int64  `json:"at"`
}

// Stats 控制台概览数字
type Stats struct {
	Devices       int `json:"devices"`
	DevicesOnline int `json:"devicesOnline"`
	Tasks         int `json:"tasks"`
	TasksPending  int `json:"tasksPending"`
	TasksActive   int `json:"tasksActive"`
	TasksDone     int `json:"tasksDone"`
	TasksFailed   int `json:"tasksFailed"`
	Records       int `json:"records"`
	Memories      int `json:"memories"`
}

// Store 存储句柄
type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS devices(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  os TEXT NOT NULL DEFAULT '',
  arch TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  user TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  caps TEXT NOT NULL DEFAULT '[]',
  online INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  remote_addr TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tasks(
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT '',
  args TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT '',
  need_approval INTEGER NOT NULL DEFAULT 0,
  approved_by TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT 'user',
  idem_key TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  dispatched_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  result TEXT NOT NULL DEFAULT '{}',
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_device ON tasks(device_id);
CREATE INDEX IF NOT EXISTS idx_tasks_idem ON tasks(device_id, idem_key, status);
CREATE TABLE IF NOT EXISTS records(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL DEFAULT '',
  device_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0,
  payload TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_records_kind ON records(kind, id);
CREATE TABLE IF NOT EXISTS memories(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL DEFAULT '',
  device_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  importance INTEGER NOT NULL DEFAULT 50,
  at INTEGER NOT NULL DEFAULT 0
);
`

// Open 打开（不存在则创建）数据库
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败：%w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败：%w", err)
	}
	// modernc 驱动为纯 Go 实现，限制单连接避免并发写冲突
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化表结构失败：%w", err)
	}
	s := &Store{db: db, path: path}
	// 老库升级：补上后来加的列（CREATE TABLE IF NOT EXISTS 不会改已存在的表）
	if err := s.ensureColumn("tasks", "idem_key", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// ensureColumn 表里没有这一列就加上（幂等）
func (s *Store) ensureColumn(table, column, decl string) error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("读取表结构失败（%s）：%w", table, err)
	}
	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if strings.EqualFold(name, column) {
			found = true
		}
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("升级表结构失败（%s.%s）：%w", table, column, err)
	}
	return nil
}

// Path 数据库文件路径
func (s *Store) Path() string { return s.path }

// Close 关闭
func (s *Store) Close() error { return s.db.Close() }

/* ---------- devices ---------- */

// UpsertDevice 写入/更新设备（注册时调用）
func (s *Store) UpsertDevice(d Device) error {
	caps, err := json.Marshal(d.Caps)
	if err != nil {
		caps = []byte("[]")
	}
	_, err = s.db.Exec(`
INSERT INTO devices(id,name,os,arch,hostname,user,version,caps,online,first_seen,last_seen,remote_addr)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, os=excluded.os, arch=excluded.arch, hostname=excluded.hostname,
  user=excluded.user, version=excluded.version, caps=excluded.caps,
  online=excluded.online, last_seen=excluded.last_seen, remote_addr=excluded.remote_addr`,
		d.ID, d.Name, d.OS, d.Arch, d.Hostname, d.User, d.Version, string(caps),
		boolInt(d.Online), d.FirstSeen, d.LastSeen, d.RemoteAddr)
	return err
}

// SetDeviceOnline 更新在线状态
func (s *Store) SetDeviceOnline(id string, online bool, at int64) error {
	_, err := s.db.Exec(`UPDATE devices SET online=?, last_seen=? WHERE id=?`, boolInt(online), at, id)
	return err
}

// TouchDevice 刷新最后活跃时间
func (s *Store) TouchDevice(id string, at int64) error {
	_, err := s.db.Exec(`UPDATE devices SET last_seen=? WHERE id=?`, at, id)
	return err
}

// Devices 列出全部设备（在线优先、最近活跃优先）
func (s *Store) Devices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT id,name,os,arch,hostname,user,version,caps,online,first_seen,last_seen,remote_addr
		FROM devices ORDER BY online DESC, last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var caps string
		var online int
		if err := rows.Scan(&d.ID, &d.Name, &d.OS, &d.Arch, &d.Hostname, &d.User, &d.Version,
			&caps, &online, &d.FirstSeen, &d.LastSeen, &d.RemoteAddr); err != nil {
			return nil, err
		}
		d.Online = online != 0
		d.Caps = decodeStrings(caps)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device 取单个设备
func (s *Store) Device(id string) (Device, bool, error) {
	var d Device
	var caps string
	var online int
	err := s.db.QueryRow(`SELECT id,name,os,arch,hostname,user,version,caps,online,first_seen,last_seen,remote_addr
		FROM devices WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.OS, &d.Arch, &d.Hostname, &d.User, &d.Version, &caps, &online,
			&d.FirstSeen, &d.LastSeen, &d.RemoteAddr)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	d.Online = online != 0
	d.Caps = decodeStrings(caps)
	return d, true, nil
}

/* ---------- tasks ---------- */

// CreateTask 新建任务
func (s *Store) CreateTask(t Task) error {
	args, err := json.Marshal(nonNilMap(t.Args))
	if err != nil {
		return err
	}
	result, err := json.Marshal(nonNilMap(t.Result))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO tasks(id,device_id,action,args,status,need_approval,approved_by,origin,
		idem_key,created_at,dispatched_at,finished_at,result,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.DeviceID, t.Action, string(args), t.Status, boolInt(t.NeedApproval), t.ApprovedBy, t.Origin,
		t.IdemKey, t.CreatedAt, t.DispatchedAt, t.FinishedAt, string(result), t.Error)
	return err
}

// FindInFlightByIdem 找同设备同幂等键、还在途（排队/已下发/执行中）的任务
func (s *Store) FindInFlightByIdem(deviceID, key string) (Task, bool, error) {
	if strings.TrimSpace(key) == "" {
		return Task{}, false, nil
	}
	row := s.db.QueryRow(`SELECT id,device_id,action,args,status,need_approval,approved_by,origin,idem_key,
		created_at,dispatched_at,finished_at,result,error FROM tasks
		WHERE device_id=? AND idem_key=? AND status IN ('queued','dispatched','running')
		ORDER BY created_at DESC LIMIT 1`, deviceID, key)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return t, true, nil
}

// Task 取单个任务
func (s *Store) Task(id string) (Task, bool, error) {
	row := s.db.QueryRow(`SELECT id,device_id,action,args,status,need_approval,approved_by,origin,idem_key,
		created_at,dispatched_at,finished_at,result,error FROM tasks WHERE id=?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return t, true, nil
}

// Tasks 最近任务（按创建时间倒序）
func (s *Store) Tasks(limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,device_id,action,args,status,need_approval,approved_by,origin,idem_key,
		created_at,dispatched_at,finished_at,result,error FROM tasks ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TasksByStatus 按状态取任务（可按设备过滤，deviceID 为空表示不限）
func (s *Store) TasksByStatus(status, deviceID string) ([]Task, error) {
	q := `SELECT id,device_id,action,args,status,need_approval,approved_by,origin,idem_key,
		created_at,dispatched_at,finished_at,result,error FROM tasks WHERE status=?`
	args := []any{status}
	if deviceID != "" {
		q += ` AND device_id=?`
		args = append(args, deviceID)
	}
	q += ` ORDER BY created_at ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// StaleTasks 取已下发但回执超时的任务（dispatched_at 早于 before）
func (s *Store) StaleTasks(before int64) ([]Task, error) {
	rows, err := s.db.Query(`SELECT id,device_id,action,args,status,need_approval,approved_by,origin,idem_key,
		created_at,dispatched_at,finished_at,result,error FROM tasks
		WHERE status IN ('dispatched','running') AND dispatched_at>0 AND dispatched_at<?`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkTaskDispatched 记录指令下发时间并置为 dispatched
func (s *Store) MarkTaskDispatched(id string, at int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status=?, dispatched_at=? WHERE id=?`, "dispatched", at, id)
	return err
}

// MarkTaskApproved 审批通过：记录审批人，状态转为 queued
func (s *Store) MarkTaskApproved(id, by string, at int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='queued', approved_by=? WHERE id=?`, by, id)
	return err
}

// MarkTaskRejected 审批驳回
func (s *Store) MarkTaskRejected(id, by, reason string, at int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='rejected', approved_by=?, finished_at=?, error=? WHERE id=?`,
		by, at, reason, id)
	return err
}

// MarkTaskResult 写入回执结果
func (s *Store) MarkTaskResult(id, status string, result map[string]any, errMsg string, at int64) error {
	var finished int64
	if status == "done" || status == "failed" || status == "rejected" {
		finished = at
	}
	b, err := json.Marshal(nonNilMap(result))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE tasks SET status=?, result=?, error=?,
		finished_at=CASE WHEN ?>0 THEN ? ELSE finished_at END WHERE id=?`,
		status, string(b), errMsg, finished, finished, id)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(sc scanner) (Task, error) {
	var t Task
	var args, result string
	var needApproval int
	if err := sc.Scan(&t.ID, &t.DeviceID, &t.Action, &args, &t.Status, &needApproval, &t.ApprovedBy, &t.Origin,
		&t.IdemKey, &t.CreatedAt, &t.DispatchedAt, &t.FinishedAt, &result, &t.Error); err != nil {
		return Task{}, err
	}
	t.NeedApproval = needApproval != 0
	t.Args = decodeMap(args)
	t.Result = decodeMap(result)
	return t, nil
}

/* ---------- records ---------- */

// AddRecord 追加一条三态/审批记录
func (s *Store) AddRecord(r Record) error {
	if r.At == 0 {
		r.At = time.Now().UnixMilli()
	}
	b, err := json.Marshal(nonNilMap(r.Payload))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO records(kind,device_id,task_id,at,payload) VALUES(?,?,?,?,?)`,
		r.Kind, r.DeviceID, r.TaskID, r.At, string(b))
	return err
}

// Records 最近记录（kind 为空表示全部）
func (s *Store) Records(limit int, kind string) ([]Record, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id,kind,device_id,task_id,at,payload FROM records`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		var payload string
		if err := rows.Scan(&r.ID, &r.Kind, &r.DeviceID, &r.TaskID, &r.At, &payload); err != nil {
			return nil, err
		}
		r.Payload = decodeMap(payload)
		out = append(out, r)
	}
	return out, rows.Err()
}

/* ---------- memories ---------- */

// AddMemory 记忆落盘
func (s *Store) AddMemory(m Memory) error {
	if m.At == 0 {
		m.At = time.Now().UnixMilli()
	}
	if m.Importance == 0 {
		m.Importance = 50
	}
	_, err := s.db.Exec(`INSERT INTO memories(kind,device_id,task_id,title,content,importance,at)
		VALUES(?,?,?,?,?,?,?)`, m.Kind, m.DeviceID, m.TaskID, m.Title, m.Content, m.Importance, m.At)
	return err
}

// Memories 最近记忆
func (s *Store) Memories(limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT id,kind,device_id,task_id,title,content,importance,at
		FROM memories ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Kind, &m.DeviceID, &m.TaskID, &m.Title, &m.Content, &m.Importance, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

/* ---------- stats ---------- */

// Stats 概览
func (s *Store) Stats() (Stats, error) {
	var st Stats
	counts := []struct {
		q    string
		dest *int
	}{
		{`SELECT COUNT(*) FROM devices`, &st.Devices},
		{`SELECT COUNT(*) FROM devices WHERE online=1`, &st.DevicesOnline},
		{`SELECT COUNT(*) FROM tasks`, &st.Tasks},
		{`SELECT COUNT(*) FROM tasks WHERE status='pending_approval'`, &st.TasksPending},
		{`SELECT COUNT(*) FROM tasks WHERE status IN ('queued','dispatched','running')`, &st.TasksActive},
		{`SELECT COUNT(*) FROM tasks WHERE status='done'`, &st.TasksDone},
		{`SELECT COUNT(*) FROM tasks WHERE status='failed'`, &st.TasksFailed},
		{`SELECT COUNT(*) FROM records`, &st.Records},
		{`SELECT COUNT(*) FROM memories`, &st.Memories},
	}
	for _, c := range counts {
		if err := s.db.QueryRow(c.q).Scan(c.dest); err != nil {
			return st, err
		}
	}
	return st, nil
}

/* ---------- 小工具 ---------- */

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func decodeMap(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func decodeStrings(s string) []string {
	out := []string{}
	if s == "" {
		return out
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}
