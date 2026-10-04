// Package core 是白泽待办中心的领域核心（Go 全量重写版）。
//
// 它承载与手机 App（原 HTML/JS 版）完全一致的数据模型与规则：
// 待办（含双源属主 user/agent、依赖前置、附件引用）、密码本（同一平台多账号、
// 同账号密码历史、归属分组、AES-GCM 加密）、Markdown 导入导出、设置。
//
// 三端共用：CLI（电脑侧 bz）、手机端内核（bzcore）、后端 Agent。
// 兼容性铁律：JSON 字段名、单位、默认值必须与 JS 版一致；
// 解析到不认识的字段要原样保留（用 Extra 兜底），绝不能在往返中丢字段。
package core

import (
	"encoding/json"
	"math/rand"
	"strconv"
	"time"
)

/* ---------- 取值域（与 JS 版一致） ---------- */

// 优先级
const (
	PriorityHigh = "high"
	PriorityMid  = "mid"
	PriorityLow  = "low"
)

// 任务形式
const (
	FormSchedule = "schedule" // 日程排期
	FormLeisure  = "leisure"  // 闲暇待办
)

// 状态
const (
	StatusTodo      = "todo"
	StatusDoing     = "doing"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
)

// 属主（双源待办：用户待办 / Agent 待办）
const (
	OwnerUser  = "user"
	OwnerAgent = "agent"
)

// 密码来源
const (
	SourceManual = "manual"
	SourceMD     = "md"
	SourceAgent  = "agent"
)

// AuthPending Agent 待办的“已申请授权执行”标记
const AuthPending = "pending"

// Domains 领域取值（UI 快捷选项）
var Domains = []string{"工作开发", "创作", "日常生活"}

// Estimates 预计耗时快捷选项（分钟）
var Estimates = []int{15, 30, 45, 60, 90, 120}

// MaxAttachments 单条待办的附件上限（与 App 一致）
const MaxAttachments = 9

/* ---------- 附件 ---------- */

// Attachment 附件引用：原文件由端侧存到私有目录，这里只存引用与图片缩略图
type Attachment struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // image | file
	Name  string `json:"name"`
	Mime  string `json:"mime,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Path  string `json:"path,omitempty"`
	Thumb string `json:"thumb,omitempty"`
}

/* ---------- 待办 ---------- */

// Task 待办任务（字段名与 JS 版逐一对齐）
type Task struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Category  string       `json:"category"` // 所属领域
	Priority  string       `json:"priority"` // high | mid | low
	Form      string       `json:"form"`     // schedule | leisure
	Due       string       `json:"due"`      // "YYYY-MM-DDTHH:mm"（本地时间），空表示未排期
	Weekly    bool         `json:"weekly"`   // 每周提醒
	Estimate  int          `json:"estimate"` // 预计耗时（分钟）
	Deps      []string     `json:"deps"`     // 前置依赖（其它待办 id）
	Atts      []Attachment `json:"atts"`     // 附件引用
	Note      string       `json:"note"`
	Owner     string       `json:"owner"`  // user | agent（双源）
	Status    string       `json:"status"` // todo | doing | done | cancelled
	Remind    bool         `json:"remind"`
	Auth      string       `json:"auth,omitempty"` // Agent 待办：pending 表示已申请执行
	CreatedAt int64        `json:"createdAt"`      // 毫秒时间戳

	// Extra 保存 JS 侧有、Go 侧尚未建模的字段，保证往返不丢数据
	Extra map[string]json.RawMessage `json:"-"`
}

var knownTaskKeys = map[string]bool{
	"id": true, "title": true, "category": true, "priority": true, "form": true,
	"due": true, "weekly": true, "estimate": true, "deps": true, "atts": true,
	"note": true, "owner": true, "status": true, "remind": true, "auth": true,
	"createdAt": true,
}

func (t Task) MarshalJSON() ([]byte, error) {
	type alias Task
	base, err := json.Marshal(alias(t))
	if err != nil {
		return nil, err
	}
	return mergeExtra(base, t.Extra)
}

func (t *Task) UnmarshalJSON(b []byte) error {
	type alias Task
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*t = Task(a)
	extra, err := rawExcept(b, knownTaskKeys)
	if err != nil {
		return err
	}
	t.Extra = extra
	return nil
}

// Normalize 补齐默认值（与 JS quickAdd / saveForm 的默认一致），返回是否改动过
func (t *Task) Normalize() bool {
	changed := false
	if t.ID == "" {
		t.ID = NewID()
		changed = true
	}
	if t.Category == "" {
		t.Category = "日常生活"
		changed = true
	}
	if t.Priority == "" {
		t.Priority = PriorityMid
		changed = true
	}
	if t.Owner == "" {
		t.Owner = OwnerUser
		changed = true
	}
	if t.Status == "" {
		t.Status = StatusTodo
		changed = true
	}
	if t.Deps == nil {
		t.Deps = []string{}
		changed = true
	}
	if t.Atts == nil {
		t.Atts = []Attachment{}
		changed = true
	}
	return changed
}

// IsClosed 是否已收尾（完成/取消）
func (t Task) IsClosed() bool {
	return t.Status == StatusDone || t.Status == StatusCancelled
}

/* ---------- 密码本 ---------- */

// PassHistory 该账号用过的旧密码（按时间倒序，不含当前密码）
type PassHistory struct {
	Pwd string `json:"pwd"`
	At  int64  `json:"at"` // 毫秒时间戳，0 表示时间未知
}

// Pass 密码本条目（字段名与 JS 版逐一对齐）
type Pass struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`   // 平台
	Account   string        `json:"account"` // 账号
	Group     string        `json:"group"`   // 归属：同一服务的多个站点填同一个
	Password  string        `json:"password"`
	History   []PassHistory `json:"history"`
	URL       string        `json:"url"`
	Note      string        `json:"note"`
	Source    string        `json:"source"` // manual | md | agent
	CreatedAt int64         `json:"createdAt"`
	UpdatedAt int64         `json:"updatedAt"`

	Extra map[string]json.RawMessage `json:"-"`
}

var knownPassKeys = map[string]bool{
	"id": true, "title": true, "account": true, "group": true, "password": true,
	"history": true, "url": true, "note": true, "source": true,
	"createdAt": true, "updatedAt": true,
}

func (p Pass) MarshalJSON() ([]byte, error) {
	type alias Pass
	base, err := json.Marshal(alias(p))
	if err != nil {
		return nil, err
	}
	return mergeExtra(base, p.Extra)
}

func (p *Pass) UnmarshalJSON(b []byte) error {
	type alias Pass
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = Pass(a)
	extra, err := rawExcept(b, knownPassKeys)
	if err != nil {
		return err
	}
	p.Extra = extra
	return nil
}

/* ---------- 数据文档 ---------- */

// Doc 一份完整数据：待办 + 密码本。
// 与 CLI 的 ~/.baize-todo/data.json、App 的导出 JSON 同构。
type Doc struct {
	Todos []Task `json:"todos"`
	Vault []Pass `json:"vault"`

	Extra map[string]json.RawMessage `json:"-"`
}

var knownDocKeys = map[string]bool{"todos": true, "vault": true}

func (d Doc) MarshalJSON() ([]byte, error) {
	type alias Doc
	base, err := json.Marshal(alias(d))
	if err != nil {
		return nil, err
	}
	return mergeExtra(base, d.Extra)
}

func (d *Doc) UnmarshalJSON(b []byte) error {
	type alias Doc
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*d = Doc(a)
	extra, err := rawExcept(b, knownDocKeys)
	if err != nil {
		return err
	}
	d.Extra = extra
	return nil
}

// NewDoc 空文档
func NewDoc() *Doc {
	return &Doc{Todos: []Task{}, Vault: []Pass{}}
}

// Normalize 全文规范化：补默认字段、收敛历史领域名、整理密码历史，返回是否改动
func (d *Doc) Normalize() bool {
	changed := false
	for i := range d.Todos {
		if d.Todos[i].Normalize() {
			changed = true
		}
		if migrateCategory(&d.Todos[i]) {
			changed = true
		}
	}
	for i := range d.Vault {
		if d.Vault[i].Normalize() {
			changed = true
		}
	}
	return changed
}

/* ---------- 设置 ---------- */

// AIConfig 模型接入（OpenAI / Anthropic 两种协议，其余用户自填）
type AIConfig struct {
	Protocol string `json:"protocol"` // openai | anthropic
	BaseURL  string `json:"baseUrl"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
}

// Settings 设置页数据（bz_settings）
type Settings struct {
	AI         AIConfig `json:"ai"`
	ChatAgents []string `json:"chatAgents"` // AI 助手页选中的智能体（多个=群发）
	Notify     bool     `json:"notify"`
	Remind     bool     `json:"remind"`
	PlanPolicy string   `json:"planPolicy"` // 排期策略

	Extra map[string]json.RawMessage `json:"-"`
}

var knownSettingsKeys = map[string]bool{
	"ai": true, "chatAgents": true, "notify": true, "remind": true, "planPolicy": true,
}

func (s Settings) MarshalJSON() ([]byte, error) {
	type alias Settings
	base, err := json.Marshal(alias(s))
	if err != nil {
		return nil, err
	}
	return mergeExtra(base, s.Extra)
}

func (s *Settings) UnmarshalJSON(b []byte) error {
	type alias Settings
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*s = Settings(a)
	extra, err := rawExcept(b, knownSettingsKeys)
	if err != nil {
		return err
	}
	s.Extra = extra
	return nil
}

// DefaultSettings 与 JS 版 defaultSettings 一致
func DefaultSettings() Settings {
	return Settings{
		AI: AIConfig{
			Protocol: "openai",
			BaseURL:  "https://api.deepseek.com/v1",
			APIKey:   "",
			Model:    "deepseek-chat",
		},
		ChatAgents: []string{},
		Notify:     false,
		Remind:     true,
		PlanPolicy: "balance",
	}
}

// ApplyDefaults 用默认值补空字段（不覆盖已有值），返回是否改动
func (s *Settings) ApplyDefaults() bool {
	d := DefaultSettings()
	changed := false
	if s.AI.Protocol == "" {
		s.AI.Protocol = d.AI.Protocol
		changed = true
	}
	if s.AI.BaseURL == "" {
		s.AI.BaseURL = d.AI.BaseURL
		changed = true
	}
	if s.AI.Model == "" {
		s.AI.Model = d.AI.Model
		changed = true
	}
	if s.ChatAgents == nil {
		s.ChatAgents = []string{}
		changed = true
	}
	if s.PlanPolicy == "" {
		s.PlanPolicy = d.PlanPolicy
		changed = true
	}
	return changed
}

/* ---------- id 与字段兼容工具 ---------- */

const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"

// NewID 生成与 JS 版同形态的 id：毫秒时间戳的 36 进制 + 6 位随机后缀
func NewID() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = base36[rand.Intn(len(base36))]
	}
	return strconv.FormatInt(time.Now().UnixMilli(), 36) + string(b)
}

// NewAttachmentID 附件 id：'a' + 36 进制时间戳 + 3 位随机
func NewAttachmentID() string {
	b := make([]byte, 3)
	for i := range b {
		b[i] = base36[rand.Intn(len(base36))]
	}
	return "a" + strconv.FormatInt(time.Now().UnixMilli(), 36) + string(b)
}

// Now 当前毫秒时间戳（统一出口，便于测试注入）
var Now = func() int64 { return time.Now().UnixMilli() }

func mergeExtra(base []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return base, nil
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, exists := m[k]; !exists {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

func rawExcept(b []byte, known map[string]bool) (map[string]json.RawMessage, error) {
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for k, v := range all {
		if !known[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// migrateCategory 收敛历史数据里的旧领域名（与 JS migrate 一致）
func migrateCategory(t *Task) bool {
	if t.Category == "创作（AI漫剧等）" {
		t.Category = "创作"
		return true
	}
	return false
}
