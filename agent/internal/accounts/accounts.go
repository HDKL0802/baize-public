// Package accounts 是白泽的多用户底座：本地账号、用户组与登录会话。
//
// 设计要点（对应用户诉求「每个用户单独一个数据库 + 用户组再加一个共享库」）：
//
//   - **账号是本地账号**，不接第三方实名（用户明确要求绝不实名）。用户名 + 口令（PBKDF2 哈希）。
//   - **数据分区靠目录**：每个用户一份 `<数据目录>/users/<用户id>/`，每个用户组一份
//     `<数据目录>/groups/<组id>/`；记忆库（memory.db）与知识库（kb/）都在里面，天然隔离。
//   - **向后兼容**：根数据目录仍是「默认主体」的数据（老的单人用法一点没变）。
//     没登录时一切照旧走默认主体；登录成某个用户后，记忆/知识库接口按他的分区走。
//
// 不做的事：不提供公网注册、不做权限角色矩阵（只有 admin 一个布尔位）——个人 NAS 自用，
// 过度设计反而没人维护得动。
package accounts

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Version 账号模块版本
const Version = "0.1.0"

// SessionTTL 登录会话有效期（默认 30 天）
const SessionTTL = 30 * 24 * time.Hour

// User 一个本地账号
type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Admin     bool   `json:"admin"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Group 一个用户组
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OwnerID     string `json:"ownerId,omitempty"`
	MemberCount int    `json:"memberCount"`
	CreatedAt   int64  `json:"createdAt"`
}

// Member 组内成员（带用户名，方便界面直接显示）
type Member struct {
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	Role     string `json:"role"` // owner | member
	JoinedAt int64  `json:"joinedAt"`
}

// Registry 账号注册表
type Registry struct {
	dir string
	db  *sql.DB
	lg  *slog.Logger
	mu  sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS users(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  name_lower TEXT NOT NULL UNIQUE,
  pass_hash TEXT NOT NULL,
  admin INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS groups(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  name_lower TEXT NOT NULL UNIQUE,
  owner_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS members(
  group_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'member',
  joined_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_members_user ON members(user_id);
CREATE TABLE IF NOT EXISTS sessions(
  token TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  created_at INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
`

// Open 打开（或首次创建）账号库：<数据目录>/accounts.db。
// 首次打开且一个用户都没有时会建一个 admin 账号，随机口令写到
// `<数据目录>/admin-password.txt`（0600），并在日志里点明位置——总得有个入口。
func Open(dataDir string, lg *slog.Logger) (*Registry, error) {
	if lg == nil {
		lg = slog.Default()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dataDir, "accounts.db")) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开账号库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化账号表结构失败：%w", err)
	}
	r := &Registry{dir: dataDir, db: db, lg: lg}
	if err := r.bootstrap(lg); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

// Close 关闭账号库
func (r *Registry) Close() error { return r.db.Close() }

// Dir 数据目录
func (r *Registry) Dir() string { return r.dir }

// bootstrap 首启建管理员账号（已存在任何用户就什么都不做）
func (r *Registry) bootstrap(lg *slog.Logger) error {
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	// 随机口令，写文件让人自己去看（不能预设成 "admin" 那种）
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("生成初始口令失败：%w", err)
	}
	pwd := hex.EncodeToString(buf)
	u, err := r.CreateUser("admin", pwd, true)
	if err != nil {
		return err
	}
	path := filepath.Join(r.dir, "admin-password.txt")
	if err := os.WriteFile(path, []byte(
		"白泽初始管理员账号\n用户名：admin\n口令："+pwd+
			"\n\n（登录后请尽快在设置里改掉，并删掉本文件）\n"), 0o600); err != nil {
		lg.Warn("写入初始口令文件失败", "err", err, "password", pwd)
		return nil
	}
	lg.Info("已创建初始管理员账号", "user", u.Name, "passwordFile", path)
	return nil
}

/* ---------- 用户 ---------- */

// Users 列出全部用户（按创建时间）
func (r *Registry) Users() ([]User, error) {
	rows, err := r.db.Query(`SELECT id,name,admin,created_at,updated_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var admin int
		if err := rows.Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Admin = admin != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// User 按 id 取用户
func (r *Registry) User(id string) (User, error) {
	return r.scanUser(`SELECT id,name,admin,created_at,updated_at FROM users WHERE id=?`, id)
}

// UserByName 按用户名取用户（大小写不敏感）
func (r *Registry) UserByName(name string) (User, error) {
	return r.scanUser(`SELECT id,name,admin,created_at,updated_at FROM users WHERE name_lower=?`,
		strings.ToLower(strings.TrimSpace(name)))
}

func (r *Registry) scanUser(query string, args ...any) (User, error) {
	var u User
	var admin int
	err := r.db.QueryRow(query, args...).Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("用户不存在")
	}
	if err != nil {
		return User{}, err
	}
	u.Admin = admin != 0
	return u, nil
}

// CreateUser 新建用户并准备他的数据分区
func (r *Registry) CreateUser(name, password string, admin bool) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("用户名不能为空")
	}
	if len([]rune(name)) > 32 {
		return User{}, errors.New("用户名太长（最多 32 个字）")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := r.newID("u")
	if err != nil {
		return User{}, err
	}
	now := time.Now().UnixMilli()
	_, err = r.db.Exec(`INSERT INTO users(id,name,name_lower,pass_hash,admin,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)`, id, name, strings.ToLower(name), hash, boolToInt(admin), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, fmt.Errorf("用户名「%s」已存在", name)
		}
		return User{}, err
	}
	if err := r.ensureUserDirs(id); err != nil {
		return User{}, err
	}
	return User{ID: id, Name: name, Admin: admin, CreatedAt: now, UpdatedAt: now}, nil
}

// SetPassword 改口令
func (r *Registry) SetPassword(id, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := r.db.Exec(`UPDATE users SET pass_hash=?, updated_at=? WHERE id=?`,
		hash, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("用户不存在")
	}
	// 改口令后把该用户的会话全部作废（口令变了，旧会话不该继续有效）
	_, _ = r.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	return nil
}

// DeleteUser 删除用户（连同他的会话；组内关系一并清掉）。
// **不删数据目录**——删数据是不可逆的，交给用户在文件层面自己决定。
func (r *Registry) DeleteUser(id string) error {
	res, err := r.db.Exec(`DELETE FROM users WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("用户不存在")
	}
	_, _ = r.db.Exec(`DELETE FROM sessions WHERE user_id=?`, id)
	_, _ = r.db.Exec(`DELETE FROM members WHERE user_id=?`, id)
	return nil
}

// Authenticate 校验用户名口令
func (r *Registry) Authenticate(name, password string) (User, error) {
	u, err := r.UserByName(name)
	if err != nil {
		// 用户不存在也照跑一次哈希，避免用响应时间判断用户名是否存在
		_ = verifyPassword("pbkdf2_sha256$1$AAAA$AAAA", password)
		return User{}, ErrBadPassword
	}
	var hash string
	if err := r.db.QueryRow(`SELECT pass_hash FROM users WHERE id=?`, u.ID).Scan(&hash); err != nil {
		return User{}, err
	}
	if !verifyPassword(hash, password) {
		return User{}, ErrBadPassword
	}
	return u, nil
}

/* ---------- 用户组 ---------- */

// Groups 列出全部用户组
func (r *Registry) Groups() ([]Group, error) {
	rows, err := r.db.Query(`SELECT g.id,g.name,g.owner_id,g.created_at,
		(SELECT COUNT(*) FROM members m WHERE m.group_id=g.id) FROM groups g ORDER BY g.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.OwnerID, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Group 按 id 取用户组
func (r *Registry) Group(id string) (Group, error) {
	var g Group
	err := r.db.QueryRow(`SELECT g.id,g.name,g.owner_id,g.created_at,
		(SELECT COUNT(*) FROM members m WHERE m.group_id=g.id) FROM groups g WHERE g.id=?`, id).
		Scan(&g.ID, &g.Name, &g.OwnerID, &g.CreatedAt, &g.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, errors.New("用户组不存在")
	}
	return g, err
}

// CreateGroup 新建用户组并准备共享分区；ownerID 非空时自动成为 owner 成员
func (r *Registry) CreateGroup(name, ownerID string) (Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Group{}, errors.New("用户组名不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := r.newID("g")
	if err != nil {
		return Group{}, err
	}
	now := time.Now().UnixMilli()
	if _, err := r.db.Exec(`INSERT INTO groups(id,name,name_lower,owner_id,created_at) VALUES(?,?,?,?,?)`,
		id, name, strings.ToLower(name), ownerID, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Group{}, fmt.Errorf("用户组「%s」已存在", name)
		}
		return Group{}, err
	}
	if err := r.ensureGroupDirs(id); err != nil {
		return Group{}, err
	}
	g := Group{ID: id, Name: name, OwnerID: ownerID, CreatedAt: now}
	if strings.TrimSpace(ownerID) != "" {
		if _, err := r.db.Exec(`INSERT OR REPLACE INTO members(group_id,user_id,role,joined_at)
			VALUES(?,?,'owner',?)`, id, ownerID, now); err != nil {
			return Group{}, err
		}
		g.MemberCount = 1
	}
	return g, nil
}

// DeleteGroup 删除用户组（连同成员关系；共享数据目录保留，理由同 DeleteUser）
func (r *Registry) DeleteGroup(id string) error {
	res, err := r.db.Exec(`DELETE FROM groups WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("用户组不存在")
	}
	_, _ = r.db.Exec(`DELETE FROM members WHERE group_id=?`, id)
	return nil
}

// AddMember 把用户加进组（已在组里就当成成功，幂等）
func (r *Registry) AddMember(groupID, userID string) error {
	if _, err := r.Group(groupID); err != nil {
		return err
	}
	if _, err := r.User(userID); err != nil {
		return errors.New("要加入的用户不存在")
	}
	role := "member"
	var owner string
	_ = r.db.QueryRow(`SELECT owner_id FROM groups WHERE id=?`, groupID).Scan(&owner)
	if owner == userID {
		role = "owner"
	}
	_, err := r.db.Exec(`INSERT OR REPLACE INTO members(group_id,user_id,role,joined_at)
		VALUES(?,?,?,?)`, groupID, userID, role, time.Now().UnixMilli())
	return err
}

// RemoveMember 把用户移出组
func (r *Registry) RemoveMember(groupID, userID string) error {
	res, err := r.db.Exec(`DELETE FROM members WHERE group_id=? AND user_id=?`, groupID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("该用户不在这个组里")
	}
	return nil
}

// Members 列出组成员
func (r *Registry) Members(groupID string) ([]Member, error) {
	rows, err := r.db.Query(`SELECT m.user_id,COALESCE(u.name,''),m.role,m.joined_at
		FROM members m LEFT JOIN users u ON u.id=m.user_id
		WHERE m.group_id=? ORDER BY m.joined_at`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.UserName, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GroupsOf 用户所在的全部用户组
func (r *Registry) GroupsOf(userID string) ([]Group, error) {
	rows, err := r.db.Query(`SELECT g.id,g.name,g.owner_id,g.created_at,
		(SELECT COUNT(*) FROM members m2 WHERE m2.group_id=g.id)
		FROM groups g JOIN members m ON m.group_id=g.id
		WHERE m.user_id=? ORDER BY g.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.OwnerID, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// IsMember 某人是否在组里
func (r *Registry) IsMember(groupID, userID string) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM members WHERE group_id=? AND user_id=?`,
		groupID, userID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

/* ---------- 会话 ---------- */

// Login 登录：口令对了就发一个会话令牌
func (r *Registry) Login(name, password string) (User, string, error) {
	u, err := r.Authenticate(name, password)
	if err != nil {
		return User{}, "", err
	}
	tok, err := r.newToken()
	if err != nil {
		return User{}, "", err
	}
	now := time.Now()
	if _, err := r.db.Exec(`INSERT INTO sessions(token,user_id,created_at,expires_at) VALUES(?,?,?,?)`,
		tok, u.ID, now.UnixMilli(), now.Add(SessionTTL).UnixMilli()); err != nil {
		return User{}, "", err
	}
	return u, tok, nil
}

// Logout 注销会话
func (r *Registry) Logout(token string) error {
	_, err := r.db.Exec(`DELETE FROM sessions WHERE token=?`, strings.TrimSpace(token))
	return err
}

// Session 用令牌换用户（过期视为无效）
func (r *Registry) Session(token string) (User, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, false
	}
	var uid string
	var exp int64
	err := r.db.QueryRow(`SELECT user_id,expires_at FROM sessions WHERE token=?`, token).Scan(&uid, &exp)
	if err != nil {
		return User{}, false
	}
	if exp > 0 && time.Now().UnixMilli() > exp {
		_, _ = r.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
		return User{}, false
	}
	u, err := r.User(uid)
	if err != nil {
		return User{}, false
	}
	return u, true
}

// CleanupSessions 清掉过期会话（启动时跑一次即可）
func (r *Registry) CleanupSessions() int64 {
	res, err := r.db.Exec(`DELETE FROM sessions WHERE expires_at>0 AND expires_at<?`, time.Now().UnixMilli())
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

/* ---------- 内部工具 ---------- */

// newID 生成 `前缀_随机` 形式的 id（目录名要能直接当文件夹名，所以只用小写十六进制）
func (r *Registry) newID(prefix string) (string, error) {
	for i := 0; i < 8; i++ {
		buf := make([]byte, 6)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成 id 失败：%w", err)
		}
		id := prefix + "_" + hex.EncodeToString(buf)
		var exists int
		var err error
		switch prefix {
		case "u":
			err = r.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, id).Scan(&exists)
		case "g":
			err = r.db.QueryRow(`SELECT COUNT(*) FROM groups WHERE id=?`, id).Scan(&exists)
		}
		if err != nil {
			return "", err
		}
		if exists == 0 {
			return id, nil
		}
	}
	return "", errors.New("生成唯一 id 失败（重试多次仍冲突）")
}

func (r *Registry) newToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话令牌失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
