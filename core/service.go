package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version 核心版本（Go 全量重写版）
const Version = "0.10.0"

// Service 把核心能力以本地 HTTP 接口暴露给界面层（手机端 WebView / 桌面控制面板）。
//
// 文件布局（与手机 App 的 localStorage 槽位一一对应）：
//
//	data.json       {todos, vault}      ← 未设主密码时的明文库（与 CLI 同构）
//	settings.json   {ai, notify, ...}   ← bz_settings
//	vault.enc.json  {iv, data}          ← bz_vault_enc（设了主密码后的加密库）
//	vault.pwd.json  {salt, hash}        ← bz_vault_pwd
type Service struct {
	store *Store
	token string

	mu       sync.Mutex
	doc      *Doc
	settings Settings
	pwdRec   *PwdRecord
	key      []byte // 解锁后驻内存，锁定时为 nil（密码明文绝不落盘）

	// 跨端模式：挂上后端知识库后，正本在 NAS，本机只留一份待办缓存
	remote  *Remote
	cacheAt int64
}

// NewService 打开数据目录并载入全部状态
func NewService(dir, token string) (*Service, error) {
	s := &Service{store: Open(dir), token: token}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) reload() error {
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	st, err := s.store.LoadSettings()
	if err != nil {
		return err
	}
	rec, err := s.loadPwdRecord()
	if err != nil {
		return err
	}
	s.doc = doc
	s.settings = st
	s.pwdRec = rec
	return nil
}

func (s *Service) loadPwdRecord() (*PwdRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.store.dir, "vault.pwd.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取主密码记录失败：%w", err)
	}
	rec := &PwdRecord{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, rec); err != nil {
			return nil, fmt.Errorf("主密码记录解析失败：%w", err)
		}
	}
	return rec, nil
}

func (s *Service) encPath() string { return filepath.Join(s.store.dir, "vault.enc.json") }
func (s *Service) pwdPath() string { return filepath.Join(s.store.dir, "vault.pwd.json") }

// HasMasterPwd 是否设置了主密码
func (s *Service) HasMasterPwd() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pwdRec != nil && s.pwdRec.Salt != ""
}

// Locked 密码本是否处于锁定状态（设了主密码但没解锁）
func (s *Service) Locked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pwdRec != nil && s.pwdRec.Salt != "" && s.key == nil
}

// Vault 取密码本（锁定时返回空列表 + locked 标记）
func (s *Service) Vault() ([]Pass, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vaultLockedState()
}

// vaultLockedState 取密码本（调用方必须已持有 s.mu）
func (s *Service) vaultLockedState() ([]Pass, bool, error) {
	if s.key == nil {
		if s.pwdRec != nil && s.pwdRec.Salt != "" {
			return []Pass{}, true, nil
		}
		return append([]Pass{}, s.doc.Vault...), false, nil
	}
	list, err := s.readEncVaultLocked()
	if err != nil {
		return nil, false, err
	}
	return list, false, nil
}

func (s *Service) readEncVaultLocked() ([]Pass, error) {
	raw, err := os.ReadFile(s.encPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Pass{}, nil
		}
		return nil, fmt.Errorf("读取加密密码本失败：%w", err)
	}
	box := EncBox{}
	if err := json.Unmarshal(raw, &box); err != nil {
		return nil, fmt.Errorf("加密密码本解析失败：%w", err)
	}
	return DecryptVault(s.key, box)
}

func (s *Service) writeVaultLocked(list []Pass) error {
	if s.key == nil {
		if s.pwdRec != nil && s.pwdRec.Salt != "" {
			// 锁定时绝不能往明文槽写，否则会多出一份“看不见的密码本”
			return errors.New("密码本已锁定，先在密码本页解锁再写入")
		}
		s.doc.Vault = list
		return s.store.Save(s.doc)
	}
	box, err := EncryptVault(s.key, list)
	if err != nil {
		return err
	}
	if err := writeJSONFile(s.encPath(), box); err != nil {
		return err
	}
	// 加密库生效后清掉明文槽，避免留两份
	if len(s.doc.Vault) > 0 {
		s.doc.Vault = []Pass{}
		if err := s.store.Save(s.doc); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

/* ---------- 快照 ---------- */

// Snapshot 界面层需要的全部状态
type Snapshot struct {
	Version      string    `json:"version"`
	DataDir      string    `json:"dataDir"`
	Todos        []Task    `json:"todos"`
	Vault        []Pass    `json:"vault"`
	VaultLocked  bool      `json:"vaultLocked"`
	HasMasterPwd bool      `json:"hasMasterPwd"`
	Settings     Settings  `json:"settings"`
	Sched        int       `json:"schedCount"`
	Leisure      int       `json:"leisureCount"`
	Domains      []string  `json:"domains"`
	Estimates    []int     `json:"estimates"`
	AttMax       int       `json:"attachmentMax"`
	AgentStats   AgentStat `json:"agentStats"`

	// 跨端模式：正本在后端（NAS）时这几个字段才有意义
	Remote       bool   `json:"remote,omitempty"`
	RemoteServer string `json:"remoteServer,omitempty"`
	Stale        bool   `json:"stale,omitempty"`       // 后端连不上，这份是本机缓存（只读）
	StaleReason  string `json:"staleReason,omitempty"` // 连不上的原因，原样告诉界面，不美化
	CachedAt     int64  `json:"cachedAt,omitempty"`
}

// AgentStat Agent 待办概览（双源）
type AgentStat struct {
	Total    int `json:"total"`
	Pending  int `json:"pendingAuth"`
	Done     int `json:"done"`
	Executed int `json:"executed"`
}

// Snapshot 组装快照：挂了后端知识库就从后端取（正本在那边），否则读本机
func (s *Service) Snapshot() (Snapshot, error) {
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote != nil {
		return s.remoteSnapshot(remote)
	}
	return s.localSnapshot()
}

// SetRemote 挂上后端知识库（跨端模式）。传 nil 表示退回纯本机。
func (s *Service) SetRemote(r *Remote) {
	s.mu.Lock()
	s.remote = r
	s.mu.Unlock()
}

// Remote 当前挂的后端知识库（没挂则为 nil）
func (s *Service) Remote() *Remote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remote
}

// remoteSnapshot 从后端取快照；连不上就退回本机待办缓存，并如实标记 stale。
// 密码本不做本地缓存，所以离线时它一律呈现为「锁定」，界面会提示去联网。
func (s *Service) remoteSnapshot(r *Remote) (Snapshot, error) {
	snap, err := r.State()
	if err != nil {
		local, lerr := s.localSnapshot()
		if lerr != nil {
			return Snapshot{}, err
		}
		s.mu.Lock()
		cacheAt := s.cacheAt
		s.mu.Unlock()
		local.Vault = []Pass{}
		local.VaultLocked = true
		local.Remote = true
		local.RemoteServer = r.Server()
		local.Stale = true
		local.StaleReason = err.Error()
		local.CachedAt = cacheAt
		return local, nil
	}
	s.mu.Lock()
	s.cacheAt = time.Now().UnixMilli()
	cacheAt := s.cacheAt
	s.mu.Unlock()

	s.saveTodoCache(snap.Todos)
	snap.Version = Version
	snap.Remote = true
	snap.RemoteServer = r.Server()
	snap.CachedAt = cacheAt
	if snap.Todos == nil {
		snap.Todos = []Task{}
	}
	if snap.Vault == nil {
		snap.Vault = []Pass{}
	}
	return snap, nil
}

// saveTodoCache 把后端的待办在本机留一份只读副本（离线时界面还能看见列表）。
// 只缓存待办：密码本一律不出后端。
func (s *Service) saveTodoCache(todos []Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := append([]Task{}, todos...)
	if len(s.doc.Todos) == len(list) && len(list) == 0 {
		return
	}
	next := &Doc{Todos: list, Vault: []Pass{}}
	if err := s.store.Save(next); err != nil {
		// 缓存写不进去不该影响主流程（正本在后端，本机这份只是锦上添花）
		return
	}
	s.doc = next
}

// localSnapshot 读本机这一份（纯本地模式，或跨端模式下的缓存兜底）
func (s *Service) localSnapshot() (Snapshot, error) {
	return s.LocalSnapshot()
}

// LocalSnapshot 只读本机这一份数据，不看远端。
// 首次迁移要用它：那会儿正本还在本机，得先把本机的东西读出来。
func (s *Service) LocalSnapshot() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vault, locked, err := s.vaultLockedState()
	if err != nil {
		return Snapshot{}, err
	}
	sched, leisure := TaskStats(s.doc.Todos, OwnerUser)
	agentStat := AgentStat{}
	for _, t := range s.doc.Todos {
		if t.Owner != OwnerAgent {
			continue
		}
		agentStat.Total++
		switch {
		case t.Status == StatusDone:
			agentStat.Done++
		case t.Auth == AuthPending:
			agentStat.Pending++
		}
	}
	return Snapshot{
		Version:      Version,
		DataDir:      s.store.dir,
		Todos:        append([]Task{}, s.doc.Todos...),
		Vault:        vault,
		VaultLocked:  locked,
		HasMasterPwd: s.pwdRec != nil && s.pwdRec.Salt != "",
		Settings:     s.settings,
		Sched:        sched,
		Leisure:      leisure,
		Domains:      Domains,
		Estimates:    Estimates,
		AttMax:       MaxAttachments,
		AgentStats:   agentStat,
	}, nil
}

/* ---------- 操作分发 ---------- */

// OpRequest 统一操作请求：{"op":"todo.add","args":{...}}
type OpRequest struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

// OpResult 统一操作结果
type OpResult struct {
	OK   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
	Err  string `json:"error,omitempty"`
}

// isLocalOnlyOp 这些操作属于「这台设备」自己，不上后端：
// ping 是探活；settings.save 存的是通知开关/提醒策略/模型通道，每台设备各管各的。
func isLocalOnlyOp(op string) bool {
	switch op {
	case "ping", "settings.save":
		return true
	}
	return false
}

// blockedRemoteOp 跨端模式下必须拒绝的操作：正本已经在后端，
// 手机端再推整表会把后端数据冲掉（todo.replaceAll 只在"手机是正本"时才用）。
func blockedRemoteOp(op string) string {
	if op == "todo.replaceAll" {
		return "跨端模式下待办正本在后端知识库，不再接受手机端整表覆盖（todo.replaceAll）"
	}
	return ""
}

// mutatingOp 会改动正本的操作：这类操作成功后要刷新本机待办缓存
func mutatingOp(op string) bool {
	switch op {
	case "todo.list", "todo.stats", "vault.search", "md.export", "ping":
		return false
	}
	return strings.HasPrefix(op, "todo.") || strings.HasPrefix(op, "vault.") ||
		op == "md.import" || op == "kb.import"
}

// Do 执行一次操作。挂了后端知识库时，除少数本机专属操作外全部转给后端执行
func (s *Service) Do(req OpRequest) (any, error) {
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote != nil && !isLocalOnlyOp(req.Op) {
		if why := blockedRemoteOp(req.Op); why != "" {
			return nil, errors.New(why)
		}
		return s.doRemote(remote, req)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.Op {
	case "ping":
		return map[string]any{"pong": true, "version": Version}, nil

	/* ---- 待办 ---- */
	case "todo.add":
		var a struct {
			Title     string `json:"title"`
			Category  string `json:"category"`
			Priority  string `json:"priority"`
			Form      string `json:"form"`
			Due       string `json:"due"`
			Weekly    bool   `json:"weekly"`
			Estimate  int    `json:"estimate"`
			Note      string `json:"note"`
			Owner     string `json:"owner"`
			Remind    bool   `json:"remind"`
			Deps      []string `json:"deps"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Title) == "" {
			return nil, errors.New("任务标题不能为空")
		}
		t := NewTask(a.Title, a.Form, a.Owner)
		if a.Category != "" {
			t.Category = a.Category
		}
		if a.Priority != "" {
			p, err := NormalizePriority(a.Priority)
			if err != nil {
				return nil, err
			}
			t.Priority = p
		}
		if a.Form == FormLeisure {
			t.Due = ""
		} else if a.Due != "" {
			due, err := NormalizeDue(a.Due)
			if err != nil {
				return nil, err
			}
			t.Due = due
		}
		t.Weekly = a.Weekly
		t.Estimate = a.Estimate
		t.Note = a.Note
		t.Remind = a.Remind
		t.Deps = a.Deps
		added := s.doc.AddTask(t)
		return added, s.store.Save(s.doc)

	case "todo.update":
		var a struct {
			ID   string `json:"id"`
			Task Task   `json:"task"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if err := s.doc.UpdateTask(a.ID, a.Task); err != nil {
			return nil, err
		}
		return s.taskByID(a.ID), s.store.Save(s.doc)

	case "todo.upsert":
		// 客户端同步用：按 id 新建或整体替换一条待办。
		// 与 todo.add 的区别是「认 id」——客户端本地先生成了 id，后端得沿用同一个，
		// 否则每同步一次就会多出一条重复待办。
		var t Task
		if err := decodeArgs(req.Args, &t); err != nil {
			return nil, err
		}
		if strings.TrimSpace(t.Title) == "" {
			return nil, errors.New("任务标题不能为空")
		}
		if strings.TrimSpace(t.ID) == "" {
			t.ID = NewID()
		}
		if t.CreatedAt == 0 {
			t.CreatedAt = Now()
		}
		t.Normalize()
		if i := s.doc.taskIndex(t.ID); i >= 0 {
			s.doc.Todos[i] = t
		} else {
			s.doc.Todos = append([]Task{t}, s.doc.Todos...)
		}
		return t, s.store.Save(s.doc)

	case "todo.remove":
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if err := s.doc.RemoveTask(a.ID); err != nil {
			return nil, err
		}
		return map[string]any{"removed": a.ID}, s.store.Save(s.doc)

	case "todo.setStatus":
		var a struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if err := s.doc.SetTaskStatus(a.ID, a.Status); err != nil {
			return nil, err
		}
		return s.taskByID(a.ID), s.store.Save(s.doc)

	case "todo.toggleDone":
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		st, err := s.doc.ToggleTaskDone(a.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": a.ID, "status": st}, s.store.Save(s.doc)

	case "todo.convertForm":
		var a struct {
			ID   string `json:"id"`
			Form string `json:"form"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if err := s.doc.ConvertForm(a.ID, a.Form); err != nil {
			return nil, err
		}
		return s.taskByID(a.ID), s.store.Save(s.doc)

	case "todo.setDeps":
		var a struct {
			ID   string   `json:"id"`
			Deps []string `json:"deps"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		i := s.doc.taskIndex(a.ID)
		if i < 0 {
			return nil, fmt.Errorf("找不到待办：%s", a.ID)
		}
		s.doc.Todos[i].Deps = a.Deps
		return s.doc.Todos[i], s.store.Save(s.doc)

	case "todo.setAtts":
		var a struct {
			ID   string       `json:"id"`
			Atts []Attachment `json:"atts"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if len(a.Atts) > MaxAttachments {
			return nil, fmt.Errorf("一条待办最多 %d 个附件", MaxAttachments)
		}
		i := s.doc.taskIndex(a.ID)
		if i < 0 {
			return nil, fmt.Errorf("找不到待办：%s", a.ID)
		}
		s.doc.Todos[i].Atts = a.Atts
		return s.doc.Todos[i], s.store.Save(s.doc)

	case "todo.authorizeAgent":
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if err := s.doc.AuthorizeAgent(a.ID); err != nil {
			return nil, err
		}
		return s.taskByID(a.ID), s.store.Save(s.doc)

	case "todo.replaceAll":
		// 手机端才是待办数据的主人：App 每次改完待办就把整表推过来（壳代调用），
		// 这样跨端下发的 todo.list / todo.stats 读到的才是真数据。
		// 内核这边只负责被查，不参与编辑；replaceAll 是「整表替换」，
		// 删掉的待办也会跟着消失，不会留下幽灵条目。
		var a struct {
			Todos []Task `json:"todos"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		s.doc.Todos = append([]Task{}, a.Todos...)
		if err := s.store.Save(s.doc); err != nil {
			return nil, err
		}
		return map[string]any{"count": len(s.doc.Todos)}, nil

	/* ---- 密码本 ---- */
	case "vault.add", "vault.merge":
		var item Pass
		if err := decodeArgs(req.Args, &item); err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Account) == "" {
			return nil, errors.New("平台名称与账号为必填")
		}
		list, err := s.mutableVaultLocked()
		if err != nil {
			return nil, err
		}
		res, idx := MergePass(&list, item)
		if err := s.writeVaultLocked(list); err != nil {
			return nil, err
		}
		return map[string]any{"result": res, "record": list[idx]}, nil

	case "vault.upsert":
		// 客户端同步用：按 id 新建或整体替换一条密码记录。
		// 与 vault.merge 的分工：merge 走「平台+账号」的历史合并（给 Agent/导入用）；
		// upsert 认 id、完全按客户端给的记录落库（App 自己已经做过历史合并）。
		var p Pass
		if err := decodeArgs(req.Args, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Account) == "" {
			return nil, errors.New("平台名称与账号为必填")
		}
		list, err := s.mutableVaultLocked()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ID) == "" {
			p.ID = NewID()
		}
		if p.CreatedAt == 0 {
			p.CreatedAt = Now()
		}
		p.UpdatedAt = Now()
		idx := -1
		for i := range list {
			if list[i].ID == p.ID {
				idx = i
				break
			}
		}
		if idx >= 0 {
			list[idx] = p
		} else {
			list = append(list, p)
		}
		if err := s.writeVaultLocked(list); err != nil {
			return nil, err
		}
		return p, nil

	case "vault.update":
		var a struct {
			ID   string `json:"id"`
			Pass Pass   `json:"record"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		list, err := s.mutableVaultLocked()
		if err != nil {
			return nil, err
		}
		tmp := &Doc{Vault: list}
		if err := tmp.UpdatePass(a.ID, a.Pass); err != nil {
			return nil, err
		}
		if err := s.writeVaultLocked(tmp.Vault); err != nil {
			return nil, err
		}
		return s.passByID(a.ID), nil

	case "vault.remove":
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		list, err := s.mutableVaultLocked()
		if err != nil {
			return nil, err
		}
		out := make([]Pass, 0, len(list))
		found := false
		for _, v := range list {
			if v.ID == a.ID {
				found = true
				continue
			}
			out = append(out, v)
		}
		if !found {
			return nil, fmt.Errorf("找不到密码记录：%s", a.ID)
		}
		return map[string]any{"removed": a.ID}, s.writeVaultLocked(out)

	case "vault.useHistory":
		var a struct {
			ID    string `json:"id"`
			Index int    `json:"index"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		list, err := s.mutableVaultLocked()
		if err != nil {
			return nil, err
		}
		tmp := &Doc{Vault: list}
		if err := tmp.UseHistory(a.ID, a.Index); err != nil {
			return nil, err
		}
		if err := s.writeVaultLocked(tmp.Vault); err != nil {
			return nil, err
		}
		return s.passByID(a.ID), nil

	case "vault.generate":
		var a struct {
			Length int `json:"length"`
		}
		_ = decodeArgs(req.Args, &a)
		pw, err := GenStrongPassword(a.Length)
		if err != nil {
			return nil, err
		}
		return map[string]any{"password": pw}, nil

	case "vault.setMaster":
		var a struct {
			Password string `json:"password"`
			Confirm  string `json:"confirm"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if len([]rune(a.Password)) < 6 {
			return nil, errors.New("主密码至少 6 位")
		}
		if a.Confirm != "" && a.Confirm != a.Password {
			return nil, errors.New("两次输入不一致")
		}
		rec, key, err := NewPwdRecord(a.Password)
		if err != nil {
			return nil, err
		}
		plain := s.doc.Vault
		s.key = key
		if err := s.writeVaultLocked(plain); err != nil {
			s.key = nil
			return nil, err
		}
		s.pwdRec = &rec
		if err := writeJSONFile(s.pwdPath(), rec); err != nil {
			return nil, err
		}
		return map[string]any{"hasMasterPwd": true}, nil

	case "vault.unlock":
		var a struct {
			Password string `json:"password"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if s.pwdRec == nil || s.pwdRec.Salt == "" {
			return nil, errors.New("还没有设置主密码")
		}
		var box *EncBox
		if raw, err := os.ReadFile(s.encPath()); err == nil && len(raw) > 0 {
			b := EncBox{}
			if err := json.Unmarshal(raw, &b); err == nil {
				box = &b
			}
		}
		ok, err := VerifyPwd(a.Password, *s.pwdRec, box)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("密码错误")
		}
		key, err := KeyFor(a.Password, *s.pwdRec)
		if err != nil {
			return nil, err
		}
		s.key = key
		return map[string]any{"unlocked": true}, nil

	case "vault.lock":
		s.key = nil
		return map[string]any{"locked": true}, nil

	case "vault.clearMaster":
		// 丢弃密钥后旧密文库就打不开了，一并清掉，避免留下死数据
		s.key = nil
		s.pwdRec = nil
		_ = os.Remove(s.encPath())
		_ = os.Remove(s.pwdPath())
		return map[string]any{"hasMasterPwd": false}, nil

	case "vault.search":
		var a struct {
			Query string `json:"query"`
		}
		_ = decodeArgs(req.Args, &a)
		list, locked, err := s.vaultLockedState()
		if err != nil {
			return nil, err
		}
		if locked {
			return []Pass{}, nil
		}
		return SearchPass(list, a.Query), nil

	/* ---- 整库导入（手机端首次迁移到后端知识库用） ---- */
	case "kb.import":
		// 待办按「标题 + 截止」去重，密码按「平台 + 账号」并进历史，
		// 不会覆盖已有记录，重复调用是安全的。
		var a struct {
			Todos []Task `json:"todos"`
			Vault []Pass `json:"vault"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		res := s.doc.ImportDoc(&Doc{Todos: a.Todos})
		if err := s.store.Save(s.doc); err != nil {
			return nil, err
		}
		if len(a.Vault) > 0 {
			list, err := s.mutableVaultLocked()
			if err != nil {
				return nil, err
			}
			for _, p := range a.Vault {
				r, _ := MergePass(&list, p)
				if r == "new" {
					res.PassAdded++
				} else {
					res.PassMerged++
				}
			}
			if err := s.writeVaultLocked(list); err != nil {
				return nil, err
			}
		}
		return map[string]any{
			"todoAdded": res.TodoAdded, "todoSkipped": res.TodoSkipped,
			"passAdded": res.PassAdded, "passMerged": res.PassMerged,
		}, nil

	/* ---- 设置 ---- */
	case "settings.save":
		var st Settings
		if err := decodeArgs(req.Args, &st); err != nil {
			return nil, err
		}
		st.ApplyDefaults()
		s.settings = st
		return st, s.store.SaveSettings(st)

	/* ---- Markdown ---- */
	case "md.export":
		var a struct {
			VaultOnly bool `json:"vaultOnly"`
		}
		_ = decodeArgs(req.Args, &a)
		vault, locked, err := s.vaultLockedState()
		if err != nil {
			return nil, err
		}
		if a.VaultOnly {
			if locked {
				return nil, errors.New("密码本已锁定，先解锁再导出")
			}
			doc := &Doc{Todos: []Task{}, Vault: vault}
			return map[string]any{"markdown": ExportVaultMarkdown(doc, time.Now())}, nil
		}
		doc := &Doc{Todos: s.doc.Todos}
		if !locked {
			doc.Vault = vault
		}
		return map[string]any{"markdown": ExportMarkdown(doc, time.Now())}, nil

	case "md.import":
		var a struct {
			Text string `json:"text"`
		}
		if err := decodeArgs(req.Args, &a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Text) == "" {
			return nil, errors.New("导入内容为空")
		}
		todos, passes := ParseMarkdown(a.Text)
		if len(todos) == 0 && len(passes) == 0 {
			return nil, errors.New("未从内容中解析出待办或密码")
		}
		res := s.doc.ImportDoc(&Doc{Todos: todos})
		if err := s.store.Save(s.doc); err != nil {
			return nil, err
		}
		if len(passes) > 0 {
			list, err := s.mutableVaultLocked()
			if err != nil {
				return nil, err
			}
			for _, p := range passes {
				r, _ := MergePass(&list, p)
				if r == "new" {
					res.PassAdded++
				} else {
					res.PassMerged++
				}
			}
			if err := s.writeVaultLocked(list); err != nil {
				return nil, err
			}
		}
		return res, nil

	default:
		return nil, fmt.Errorf("未知操作：%s", req.Op)
	}
}

// doRemote 把操作转给后端知识库执行。失败原因原样抛出，绝不假装成功。
func (s *Service) doRemote(r *Remote, req OpRequest) (any, error) {
	res, err := r.Do(req.Op, req.Args)
	if err != nil {
		return nil, err
	}
	if mutatingOp(req.Op) {
		// 写成功之后顺手刷新本机待办缓存（后端是正本，这边只是离线可读的副本）
		if snap, serr := r.State(); serr == nil {
			s.saveTodoCache(snap.Todos)
		}
	}
	return res, nil
}

// Import 把一份整库（待办 + 密码本）并进后端知识库（首次迁移用）。
// 返回并入了多少条，便于如实报告。
func (s *Service) Import(r *Remote, todos []Task, vault []Pass) (map[string]any, error) {
	args, err := json.Marshal(map[string]any{"todos": todos, "vault": vault})
	if err != nil {
		return nil, err
	}
	res, err := r.Do("kb.import", args)
	if err != nil {
		return nil, err
	}
	out, _ := res.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if snap, serr := r.State(); serr == nil {
		s.saveTodoCache(snap.Todos)
	}
	return out, nil
}

// mutableVaultLocked 取一份可改的密码本副本（锁定时报错）
func (s *Service) mutableVaultLocked() ([]Pass, error) {
	if s.key == nil {
		if s.pwdRec != nil && s.pwdRec.Salt != "" {
			return nil, errors.New("密码本已锁定，先在密码本页解锁再写入")
		}
		return append([]Pass{}, s.doc.Vault...), nil
	}
	return s.readEncVaultLocked()
}

func (s *Service) taskByID(id string) Task {
	if i := s.doc.taskIndex(id); i >= 0 {
		return s.doc.Todos[i]
	}
	return Task{}
}

// passByID 调用方必须已持有 s.mu
func (s *Service) passByID(id string) Pass {
	list, _, err := s.vaultLockedState()
	if err != nil {
		return Pass{}
	}
	for _, v := range list {
		if v.ID == id {
			return v
		}
	}
	return Pass{}
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("参数解析失败：%w", err)
	}
	return nil
}

/* ---------- HTTP 层 ---------- */

// Handler 返回本地接口路由。所有接口都要带配对令牌（header X-Baize-Token 或 ?token=）。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeOp(w, OpResult{OK: true, Data: map[string]any{
			"version":      Version,
			"locked":       s.Locked(),
			"hasMasterPwd": s.HasMasterPwd(),
			"dataDir":      s.store.dir,
		}})
	}))
	mux.HandleFunc("GET /api/state", s.auth(func(w http.ResponseWriter, r *http.Request) {
		snap, err := s.Snapshot()
		if err != nil {
			writeOp(w, OpResult{Err: err.Error()})
			return
		}
		writeOp(w, OpResult{OK: true, Data: snap})
	}))
	mux.HandleFunc("POST /api/op", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var req OpRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
			writeOp(w, OpResult{Err: "请求体解析失败：" + err.Error()})
			return
		}
		data, err := s.Do(req)
		if err != nil {
			writeOp(w, OpResult{Err: err.Error()})
			return
		}
		writeOp(w, OpResult{OK: true, Data: data})
	}))
	return mux
}

func (s *Service) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			tk := r.Header.Get("X-Baize-Token")
			if tk == "" {
				tk = r.URL.Query().Get("token")
			}
			if tk != s.token {
				w.WriteHeader(http.StatusUnauthorized)
				writeOp(w, OpResult{Err: "配对令牌不正确"})
				return
			}
		}
		next(w, r)
	}
}

func writeOp(w http.ResponseWriter, res OpResult) {
	if res.Err != "" && !res.OK {
		w.WriteHeader(http.StatusBadRequest)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(res)
}
