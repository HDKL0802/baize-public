// Package agentsvc 把 Agent 运行时（agentrt）包装成一个常驻服务：
// 配置、模型通道、工具、记忆、审批队列、定时任务、技能库都归它管，
// 后端（cmd/backend）与控制台只跟它打交道。
package agentsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/internal/activity"
	"baize/internal/agentrt"
	"baize/internal/backup"
	"baize/internal/channels"
	"baize/internal/config"
	"baize/internal/cron"
	"baize/internal/hooks"
	"baize/internal/kb"
	"baize/internal/llm"
	"baize/internal/mcp"
	"baize/internal/memory"
	"baize/internal/persona"
	"baize/internal/skills"
	"baize/internal/tools"
)

// Version 服务版本。默认值只在没被覆盖时用（单测等）；
// 后端启动时会调 SetVersion 把它对齐到 cmd/backend 的版本号，避免两处漂移
// （备份 manifest 写的就是这个值）。
var Version = "0.8.0"

// SetVersion 由 cmd/backend 在启动时调用，让「服务版本」与「后端版本」只有一个来源。
func SetVersion(v string) {
	if strings.TrimSpace(v) != "" {
		Version = v
	}
}

// ProviderInfo 通道状态（给控制台看；apiKey 只显示是否配置，不回显）
type ProviderInfo struct {
	Name      string   `json:"name"`
	Protocol  string   `json:"protocol"`
	BaseURL   string   `json:"baseUrl"`
	Model     string   `json:"model"`
	Kinds     []string `json:"kinds,omitempty"`
	Fallback  bool     `json:"fallback"`
	HasAPIKey bool     `json:"hasApiKey"`
	Usable    bool     `json:"usable"`
	Err       string   `json:"error,omitempty"`
	Local     bool     `json:"local"`
}

// EmbeddingInfo 向量化通道状态（给控制台看；同理不回显 key）
type EmbeddingInfo struct {
	Name      string `json:"name,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	Model     string `json:"model,omitempty"`
	Dim       int    `json:"dim,omitempty"`
	HasAPIKey bool   `json:"hasApiKey"`
	Usable    bool   `json:"usable"`
	Err       string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
}

// JobState 定时任务状态
type JobState struct {
	config.CronJob
	NextAt   int64  `json:"nextAt,omitempty"`
	ParseErr string `json:"parseError,omitempty"`
}

// HeartbeatState 心跳任务状态（配置 + 排期 + HEARTBEAT.md 的落地情况）
type HeartbeatState struct {
	config.HeartbeatConfig
	NextAt   int64  `json:"nextAt,omitempty"`
	ParseErr string `json:"parseError,omitempty"`
	Path     string `json:"path"`
	HasFile  bool   `json:"hasFile"`
	Size     int64  `json:"size,omitempty"`
	ModTime  int64  `json:"modTime,omitempty"`
}

// State 控制台需要的全部状态
type State struct {
	Version      string              `json:"version"`
	DataDir      string              `json:"dataDir"`
	Workdir      string              `json:"workdir"`
	SkillsDir    string              `json:"skillsDir"`
	BackupDir    string              `json:"backupDir"`
	AllowShell   bool                `json:"allowShell"`
	AllowRemote  bool                `json:"allowRemote"`
	Running      bool                `json:"running"`
	CurrentRunID string              `json:"currentRunId,omitempty"` // 正在跑的那次运行 id
	Providers    []ProviderInfo      `json:"providers"`
	Skills       []skills.Skill      `json:"skills"`
	Runs         []agentrt.RunRecord `json:"runs"`
	Approvals    []Approval          `json:"approvals"`
	Cron         []JobState          `json:"cron"`
	Heartbeat    HeartbeatState      `json:"heartbeat"`
	Memory       memory.Stats        `json:"memory"`
	Embedding    EmbeddingInfo       `json:"embedding"`
	MemoryCfg    config.Memory       `json:"memoryCfg"`
	MCP          []mcp.ServerState   `json:"mcp"`
	Backups      []backup.Archive    `json:"backups"`
	LastRun      *agentrt.RunResult  `json:"lastRun,omitempty"`
	Activity     activity.Snapshot   `json:"activity"`
	Issue        string              `json:"issue,omitempty"` // 配置层面的问题（例如没有任何可用模型）
}

// Detail 单次运行的详情
type Detail struct {
	Run      agentrt.RunRecord `json:"run"`
	Messages []llm.Message     `json:"messages"`
}

// Service Agent 服务
type Service struct {
	dataDir string
	lg      *slog.Logger

	mu        sync.RWMutex
	cfg       config.Config
	providers []ProviderInfo
	router    *llm.Router
	skills    *skills.Library
	skillMgr  *skills.Manager

	mem         *memory.Store
	embedder    llm.Embedder // 可选：记忆的向量化通道
	embedErr    string       // 配了但用不了时的原因（要显示出来，不能静默失效）
	runs        *agentrt.Store
	ck          *agentrt.CheckpointManager
	ws          *tools.Workspace
	mcp         *mcp.Manager
	kbs         *kb.Service
	persona     *persona.Library
	chans       *channels.Manager
	chansCancel context.CancelFunc    // 轮询型频道的取消器（重建/关闭时停）
	browser     *tools.BrowserSession // 浏览器会话（browser 工具；懒连接、跨运行复用）

	hooks     *hooks.Bus
	approvals *ApprovalQueue
	// approvedTools 会话级"记住放行"（进程内，重启即忘）。永久放行的清单在
	// config.ApprovalAllow 里，两者由 toolRemembered 合并判断。
	approvedTools map[string]bool
	devices       tools.DeviceExecutor // 可选：跨端调度层
	activity      *activity.Tracker    // 智能体活动追踪（始终开启，AlwaysOn）

	runMu     sync.Mutex // 顶层运行串行，避免同一工作目录并发写
	running   bool
	lastRun   *agentrt.RunResult
	curRunID  string
	schedStop chan struct{}
	sched     *scheduler
}

// Option 服务可选配置
type Option func(*Service)

// WithDevices 接入跨端调度层（设备中枢），有了它 Agent 才能把活派到桌面/手机端
func WithDevices(exec tools.DeviceExecutor) Option {
	return func(s *Service) { s.devices = exec }
}

// New 打开数据目录并启动服务（含定时任务调度）
func New(dataDir string, lg *slog.Logger, opts ...Option) (*Service, error) {
	if lg == nil {
		lg = slog.Default()
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, err
	}
	mem, err := memory.Open(dataDir)
	if err != nil {
		return nil, err
	}
	runs, err := agentrt.OpenStore(dataDir)
	if err != nil {
		mem.Close()
		return nil, err
	}
	workdir := cfg.Workdir
	if strings.TrimSpace(workdir) == "" {
		workdir = filepath.Join(dataDir, "workspace")
	}
	ws, err := tools.NewWorkspace(workdir, false)
	if err != nil {
		mem.Close()
		runs.Close()
		return nil, err
	}
	ck, err := agentrt.NewCheckpointManager(dataDir, ws.Root(), 30)
	if err != nil {
		mem.Close()
		runs.Close()
		return nil, err
	}
	skillsDir := cfg.SkillsDir
	if strings.TrimSpace(skillsDir) == "" {
		skillsDir = filepath.Join(dataDir, "skills")
	}
	// 内置技能（读文件/文档/造技能/定时任务/笔记）：只在"该技能还不存在"时落盘，
	// 绝不覆盖用户改过的版本（详见 skills.SeedBuiltin）。
	if n, err := skills.SeedBuiltin(skillsDir); err != nil {
		lg.Warn("内置技能落盘失败（不影响运行）", "err", err)
	} else if n > 0 {
		lg.Info("已写入内置技能", "count", n, "dir", skillsDir)
	}
	lib, err := skills.Load(skillsDir)
	if err != nil {
		// 兜底成"指向该目录的空库"：加载失败不影响运行，技能管理也还能用
		lg.Warn("技能库加载失败（先用空库兜底）", "err", err)
		lib = skills.Empty(skillsDir)
	}
	// 知识库（待办 + 密码本的正本）：手机端把正本放在后端，Agent 也就近直接读写
	kbs, err := kb.Open(dataDir, lg)
	if err != nil {
		mem.Close()
		runs.Close()
		return nil, err
	}
	// 人设文件：首次初始化落一份默认模板（之后用户改/删都不再自动重建）
	personaLib := persona.New(filepath.Join(dataDir, "persona"))
	if created, err := personaLib.EnsureTemplates(); err != nil {
		lg.Warn("人设模板初始化失败（不影响运行）", "err", err)
	} else if created {
		lg.Info("已创建默认人设文件", "dir", personaLib.Dir())
	}

	s := &Service{
		dataDir: dataDir, lg: lg, cfg: cfg,
		mem: mem, runs: runs, ck: ck, ws: ws, skills: lib, skillMgr: skills.NewManager(lib), kbs: kbs,
		persona: personaLib,
		mcp:     mcp.NewManager(lg, cfg.AllowRemote),
		hooks:   hooks.NewBus(), approvals: NewApprovalQueue(200),
		approvedTools: map[string]bool{},
		activity:      activity.New(50),
		schedStop:     make(chan struct{}),
	}
	// 活动追踪始终开启：挂到同一根事件总线上，运行时的每一步都会实时反映出来
	s.activity.Attach(s.hooks)
	for _, opt := range opts {
		opt(s)
	}
	s.rebuildProviders()
	s.rebuildEmbedder()
	s.rebuildChannels()
	s.rebuildBrowser()
	s.mcp.Apply(cfg.MCPServers) // 启动即按配置连 MCP 服务（连不上会明确报状态，不静默）
	s.logIssues()
	s.sched = newScheduler(s)
	go s.sched.run(s.schedStop)
	// 记忆树摘要的后台维护：只补"摘要没跟上"的天，没新东西一个模型调用都不发
	go s.treeLoop(s.schedStop)
	return s, nil
}

// Close 停止后台任务并关闭存储
func (s *Service) Close() {
	close(s.schedStop)
	s.mu.Lock()
	if s.chansCancel != nil {
		s.chansCancel()
		s.chansCancel = nil
	}
	br := s.browser
	s.browser = nil
	s.mu.Unlock()
	if br != nil {
		br.Close() // 自己拉起的浏览器会在这里被收掉
	}
	s.mcp.Close()
	s.mem.Close()
	s.runs.Close()
}

/* ---------- 配置与通道 ---------- */

// Config 当前配置副本
func (s *Service) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SaveConfig 保存配置并重建通道
func (s *Service) SaveConfig(cfg config.Config) error {
	if err := config.Save(s.dataDir, cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return s.Reload()
}

// Skills 当前技能清单（控制台 / 桌面端列技能用；与白泽自己看到的是同一份）
func (s *Service) Skills() []skills.Skill {
	s.mu.RLock()
	lib := s.skills
	s.mu.RUnlock()
	if lib == nil {
		return nil
	}
	return lib.All()
}

// SkillManager 技能管理（新建 / 改写 / 打补丁 / 写支持文件 / 归档删除）。
// 刻意复用与 Agent 的 skill_manage 同一个 Manager：这样「界面上建技能」和
// 「白泽自己建技能」不会走出两套不一样的校验与落盘写法。
func (s *Service) SkillManager() *skills.Manager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.skillMgr
}

// Jobs 定时任务状态（含下次触发时间与表达式解析错误），供 /api/agent/cron 用
func (s *Service) Jobs() []JobState { return s.jobStates() }

/* ---------- 定时任务管理（控制台接口与 Agent 的 cron 工具共用同一套语义） ---------- */

// CronAdd 新增一个定时任务。表达式先用 cron.Parse 校验：
// 坏表达式当场报错，而不是留一条 parseError 在列表里假装加上了。
func (s *Service) CronAdd(expr, goal, recipe string, autoApprove bool) (tools.CronJobInfo, error) {
	expr = strings.TrimSpace(expr)
	goal = strings.TrimSpace(goal)
	if expr == "" || goal == "" {
		return tools.CronJobInfo{}, errors.New("需要 expr（cron 表达式）与 goal（到点要做的事）")
	}
	if _, err := cron.Parse(expr); err != nil {
		return tools.CronJobInfo{}, errors.New("cron 表达式不合法：" + err.Error())
	}
	cfg := s.Config()
	rc := strings.TrimSpace(recipe)
	if rc == "" {
		rc = "chat" // 与 /api/agent/cron 的默认口径一致
	}
	job := config.CronJob{
		ID: "job" + strconv.FormatInt(time.Now().UnixMilli(), 36),
		// 默认启用、默认不免审批（危险操作照旧走人工审批队列）
		Expr: expr, Goal: goal, Recipe: rc,
		Enabled: true, AutoApprove: autoApprove,
	}
	cfg.Cron = append(cfg.Cron, job)
	if err := s.SaveConfig(cfg); err != nil {
		return tools.CronJobInfo{}, err
	}
	return s.cronJobInfoByID(job.ID), nil
}

// CronUpdate 改一条定时任务；传空字符串的字段表示"不改"。enabled 为 nil 表示不改启停。
func (s *Service) CronUpdate(id, expr, goal, recipe string, enabled *bool) (tools.CronJobInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return tools.CronJobInfo{}, errors.New("需要 id")
	}
	cfg := s.Config()
	idx := -1
	for i := range cfg.Cron {
		if cfg.Cron[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return tools.CronJobInfo{}, errors.New("没有这个定时任务：" + id)
	}
	if e := strings.TrimSpace(expr); e != "" {
		if _, err := cron.Parse(e); err != nil {
			return tools.CronJobInfo{}, errors.New("cron 表达式不合法：" + err.Error())
		}
		cfg.Cron[idx].Expr = e
	}
	if g := strings.TrimSpace(goal); g != "" {
		cfg.Cron[idx].Goal = g
	}
	if rc := strings.TrimSpace(recipe); rc != "" {
		cfg.Cron[idx].Recipe = rc
	}
	if enabled != nil {
		cfg.Cron[idx].Enabled = *enabled
	}
	if err := s.SaveConfig(cfg); err != nil {
		return tools.CronJobInfo{}, err
	}
	return s.cronJobInfoByID(id), nil
}

// CronRemove 删一条定时任务
func (s *Service) CronRemove(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("需要 id")
	}
	cfg := s.Config()
	idx := -1
	for i := range cfg.Cron {
		if cfg.Cron[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("没有这个定时任务：" + id)
	}
	cfg.Cron = append(cfg.Cron[:idx], cfg.Cron[idx+1:]...)
	return s.SaveConfig(cfg)
}

// cronJobInfoByID 取某个任务的最新状态（含下次触发时间）；找不到返回零值（不报错，
// 因为"刚加完立刻读回来"的竞态下宁可少一个字段，也不要让调用方以为加失败了）。
func (s *Service) cronJobInfoByID(id string) tools.CronJobInfo {
	for _, js := range s.Jobs() {
		if js.ID == id {
			return tools.CronJobInfo{
				ID: js.ID, Expr: js.Expr, Goal: js.Goal, Recipe: js.Recipe,
				Enabled: js.Enabled, AutoApprove: js.AutoApprove, NextAt: js.NextAt,
				ParseErr: js.ParseErr, LastRunAt: js.LastRunAt, LastStatus: js.LastStatus,
				LastError: js.LastError, Runs: js.Runs,
			}
		}
	}
	return tools.CronJobInfo{ID: id}
}

// cronHooks 把定时任务能力交给 Agent 的 cron / cron_remove 工具
func (s *Service) cronHooks() tools.CronHooks {
	return tools.CronHooks{
		List: func() []tools.CronJobInfo {
			jobs := s.Jobs()
			out := make([]tools.CronJobInfo, 0, len(jobs))
			for _, js := range jobs {
				out = append(out, tools.CronJobInfo{
					ID: js.ID, Expr: js.Expr, Goal: js.Goal, Recipe: js.Recipe,
					Enabled: js.Enabled, AutoApprove: js.AutoApprove, NextAt: js.NextAt,
					ParseErr: js.ParseErr, LastRunAt: js.LastRunAt, LastStatus: js.LastStatus,
					LastError: js.LastError, Runs: js.Runs,
				})
			}
			return out
		},
		Add:    s.CronAdd,
		Update: s.CronUpdate,
		Remove: s.CronRemove,
	}
}

// Persona 人设库（控制台/桌面端管理人设文件用）
func (s *Service) Persona() *persona.Library {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.persona
}

// Reload 重新读配置、重建通道与技能库
func (s *Service) Reload() error {
	cfg, err := config.Load(s.dataDir)
	if err != nil {
		return err
	}
	skillsDir := cfg.SkillsDir
	if strings.TrimSpace(skillsDir) == "" {
		skillsDir = filepath.Join(s.dataDir, "skills")
	}
	// 原地刷新技能库（不换指针）：skill_manage / skill_load 都持着同一个 *Library，
	// 换成新对象的话它们会一直看着旧的清单。
	s.mu.RLock()
	lib := s.skills
	s.mu.RUnlock()
	if lib != nil {
		lib.SetDir(skillsDir)
		if err := lib.Reload(); err != nil {
			s.lg.Warn("技能库加载失败（沿用上一份）", "err", err)
		}
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.rebuildProviders()
	s.rebuildEmbedder()
	s.rebuildChannels()
	s.rebuildBrowser()
	s.mcp.SetAllowRemote(cfg.AllowRemote)
	s.mcp.Apply(cfg.MCPServers) // MCP 服务热插拔：配置一变就重连/下线，不用重启后端
	s.logIssues()
	return nil
}

// rebuildChannels 按配置重建频道管理器（启动与 Reload 都走这条）。
// Runner 直接复用 Service.Run：频道消息就是"又来了一条 goal"，跑完把结论发回去。
// 重建时换一个新的 Manager（旧对象在后台跑完自己那次就自然回收）。
func (s *Service) rebuildChannels() {
	cfg := s.Config()
	mgr := channels.NewManager(func(ctx context.Context, session, text string) (string, error) {
		res, err := s.Run(ctx, text, "chat", "")
		if err != nil {
			return "", err
		}
		return res.Text, nil
	}, s.lg)
	// 先停掉上一轮的轮询（改配置会整体重建）
	s.mu.Lock()
	if s.chansCancel != nil {
		s.chansCancel()
		s.chansCancel = nil
	}
	s.mu.Unlock()

	for _, ch := range cfg.Channels {
		switch ch.Kind {
		case "webhook", "":
			mgr.Register(ch, channels.NewWebhook(ch))
		case "onebot":
			mgr.Register(ch, channels.NewOneBot(ch))
		case "feishu":
			mgr.Register(ch, channels.NewFeishu(ch, s.lg))
		default:
			s.lg.Warn("未知频道类型，已跳过", "id", ch.ID, "kind", ch.Kind)
		}
	}
	s.mu.RLock()
	old := s.chans
	s.mu.RUnlock()
	if old != nil {
		// 尽量把"最近有消息的频道"带过去，免得改一次配置就把心跳 target=last 弄丢
		if last := old.LastChannel(); last != "" && mgr.HasChannel(last) {
			mgr.NoteLast(last)
		}
	}
	s.mu.Lock()
	s.chans = mgr
	s.mu.Unlock()

	// 轮询型频道（如飞书免公网模式）：起后台轮询，拉到消息就投递给 Agent
	polCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.chansCancel = cancel
	s.mu.Unlock()
	for _, info := range mgr.List() {
		if !info.Enabled {
			continue
		}
		ch, ok := mgr.Get(info.ID)
		if !ok {
			continue
		}
		if pc, ok := ch.(channels.PollChannel); ok {
			go pc.Poll(polCtx, func(m channels.Message) {
				if err := mgr.Deliver(m); err != nil {
					s.lg.Warn("频道轮询消息被拒", "channel", m.Channel, "err", err)
				}
			})
		}
	}
	if n := mgr.Count(); n > 0 {
		s.lg.Info("已装配频道", "count", n)
	}
}

// rebuildBrowser 按配置调整浏览器会话：没变就复用（不打断已连的浏览器），变了才重连
func (s *Service) rebuildBrowser() {
	opt := s.Config().Browser
	opts := tools.BrowserOptions{
		Enabled:    opt.Enabled,
		Headless:   opt.Headless,
		ChromePath: opt.ChromePath,
		CDPURL:     opt.CDPURL,
		TimeoutSec: opt.TimeoutSec,
		MaxBytes:   opt.MaxBytes,
	}
	s.mu.Lock()
	if s.browser == nil {
		s.browser = tools.NewBrowserSession(opts, s.lg)
	} else {
		s.browser.SetConfig(opts)
	}
	s.mu.Unlock()
}

// Channels 频道管理器（控制台 / 频道回调用）
func (s *Service) Channels() *channels.Manager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.chans
}

// rebuildProviders 按配置建通道；有问题的通道记录下来，绝不静默丢弃
func (s *Service) rebuildProviders() {
	s.mu.Lock()
	defer s.mu.Unlock()
	router := llm.NewRouter(s.cfg.AllowRemote)
	infos := make([]ProviderInfo, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		info := ProviderInfo{
			Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL, Model: p.Model,
			Kinds: p.Kinds, Fallback: p.Fallback, HasAPIKey: p.APIKey != "",
		}
		if info.Name == "" {
			info.Name = p.Protocol + ":" + p.Model
		}
		cfg := llm.Config{
			Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL,
			APIKey: p.APIKey, Model: p.Model, TimeoutSec: p.TimeoutSec,
		}
		info.Local = isLocalBase(cfg.BaseURL)
		switch {
		case strings.TrimSpace(cfg.BaseURL) == "":
			info.Err = "缺少 baseUrl"
		case strings.TrimSpace(cfg.Model) == "":
			info.Err = "缺少 model"
		default:
			if err := router.CheckEndpoint(cfg); err != nil {
				info.Err = err.Error()
				break
			}
			prov, err := llm.New(cfg)
			if err != nil {
				info.Err = err.Error()
				break
			}
			info.Usable = true
			if p.Fallback {
				router.AddFallback(prov)
			} else {
				router.Add(p.Kinds, prov)
			}
		}
		infos = append(infos, info)
	}
	s.router = router
	s.providers = infos
}

// rebuildEmbedder 按配置建（或拆掉）向量化通道，并把它接到记忆库上。
// 没配就是没有——记忆退回关键词检索，界面会明说，绝不假装有语义检索。
func (s *Service) rebuildEmbedder() {
	s.mu.Lock()
	emb := s.cfg.Embedding
	allowRemote := s.cfg.AllowRemote
	s.mu.Unlock()

	e, err := buildEmbedder(emb, allowRemote)
	s.mu.Lock()
	s.embedder, s.embedErr = e, ""
	if err != nil {
		s.embedErr = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		s.lg.Warn("embedding 通道配置有问题，记忆将退回关键词检索", "err", err)
	}
	if s.mem != nil {
		s.mem.SetEmbedder(e)
	}
}

// embeddingInfo 向量通道状态（没配就明说"未启用"，别让界面以为语义检索在工作）
func (s *Service) embeddingInfo() EmbeddingInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	emb := s.cfg.Embedding
	info := EmbeddingInfo{
		Name: emb.Name, Protocol: emb.Protocol, BaseURL: emb.BaseURL, Model: emb.Model,
		HasAPIKey: emb.APIKey != "", Err: s.embedErr,
	}
	if s.embedder != nil {
		info.Usable = true
		info.Dim = s.embedder.Dim()
		if info.Name == "" {
			info.Name = s.embedder.Name()
		}
		return info
	}
	if emb.BaseURL == "" && emb.Model == "" {
		info.Note = "未配置向量化通道：记忆的语义检索与「最低相似度」闸门都不生效，检索按关键词 + 新鲜度打分"
	}
	return info
}

// EmbeddingPing 对向量化通道做一次真实调用（探活），成功后把探测到的维度记回配置，
// 控制台就能显示出向量维度，而不是只有"配没配"。
func (s *Service) EmbeddingPing(ctx context.Context) (int, error) {
	s.mu.RLock()
	e := s.embedder
	embedErr := s.embedErr
	s.mu.RUnlock()
	if e == nil {
		if embedErr != "" {
			return 0, errors.New(embedErr)
		}
		return 0, errors.New("还没配置向量化通道（需要 baseUrl 与 model）")
	}
	oe, ok := e.(*llm.OpenAIEmbedder)
	if !ok {
		return 0, errors.New("当前向量化通道不支持探活")
	}
	dim, err := oe.EmbedPing(ctx)
	if err != nil {
		return 0, err
	}
	cfg := s.Config()
	if cfg.Embedding.Dim != dim {
		cfg.Embedding.Dim = dim
		if err := s.SaveConfig(cfg); err != nil {
			s.lg.Warn("记录向量维度失败（不影响探活结果）", "err", err)
		}
	}
	return dim, nil
}

// ReindexEmbeddings 给还没有向量的存量记忆补向量（刚配好向量通道时用一次）
func (s *Service) ReindexEmbeddings(ctx context.Context) (memory.ReindexResult, error) {
	if s.mem == nil {
		return memory.ReindexResult{}, errors.New("记忆库未启用")
	}
	return s.mem.ReindexEmbeddings(ctx, 0)
}

// recallContext 派活前自动想起相关记忆，拼成一段可注入系统提示的文本。
// 召回失败/没配向量都不影响任务运行，只记一条日志。
func (s *Service) recallContext(ctx context.Context, goal string, cfg config.Config) (string, int) {
	if s.mem == nil || !cfg.Memory.AutoRecall || strings.TrimSpace(goal) == "" {
		return "", 0
	}
	res, err := s.mem.AutoRecall(ctx, memory.RecallOptions{
		Text:         goal,
		Namespace:    cfg.Memory.Namespace,
		Profile:      cfg.Memory.RecallProfile,
		BudgetTokens: cfg.Memory.RecallBudget,
		MinVectorSim: cfg.Memory.RecallMinScore,
	})
	if err != nil {
		s.lg.Warn("自动召回失败（不影响本次运行）", "err", err)
		return "", 0
	}
	if res.Context == "" {
		return "", 0
	}
	head := "【相关记忆】（系统自动召回，供参考；与当前任务无关就忽略）\n"
	if res.Note != "" {
		head += "（" + res.Note + "）\n"
	}
	return head + res.Context, res.Tokens
}

// buildEmbedder 组装向量化通道；没配置时返回 (nil, nil)
func buildEmbedder(emb config.Embedding, allowRemote bool) (llm.Embedder, error) {
	if strings.TrimSpace(emb.BaseURL) == "" && strings.TrimSpace(emb.Model) == "" {
		return nil, nil
	}
	lc := llm.Config{
		Name: emb.Name, Protocol: emb.Protocol, BaseURL: emb.BaseURL,
		APIKey: emb.APIKey, Model: emb.Model, Dim: emb.Dim, TimeoutSec: emb.TimeoutSec,
	}
	if err := llm.NewRouter(allowRemote).CheckEndpointEmbedding(lc); err != nil {
		return nil, err
	}
	return llm.NewOpenAIEmbedder(lc)
}

func (s *Service) logIssues() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.providers) == 0 {
		s.lg.Warn("还没有配置任何模型通道，Agent 任务无法运行",
			"config", filepath.Join(s.dataDir, "config.json"))
		return
	}
	usable := 0
	for _, p := range s.providers {
		if p.Usable {
			usable++
			continue
		}
		s.lg.Warn("模型通道配置有问题", "name", p.Name, "err", p.Err)
	}
	if usable == 0 {
		s.lg.Warn("没有任何可用模型通道，Agent 任务无法运行")
	}
}

func (s *Service) providerIssues() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.providers) == 0 {
		return "还没有配置模型通道"
	}
	parts := make([]string, 0, len(s.providers))
	for _, p := range s.providers {
		if p.Usable {
			continue
		}
		parts = append(parts, p.Name+"："+p.Err)
	}
	if len(parts) == 0 {
		return "模型通道不可用"
	}
	return strings.Join(parts, "；")
}

/* ---------- 运行 ---------- */

// 审批松紧度（一次派活一个档，不落到全局配置里）：
//
//	"loose"   松 —— 本次运行的危险操作全部自动放行（相当于旧的 autoApprove）
//	"" /"mid" 中 —— 默认口径：危险工具挂审批柜等人批，安全工具直接跑
//	"strict"  严 —— 每一次工具调用（含 fs_list 这类只读）都要人批
//
// 松档等价于「人先放行这次运行、之后不再逐个问」，所以它也是人工授权的一种，
// 只是授权发生在派活那一刻，而不是工具弹出的那一刻。
func normalizeStrictness(mode string) (autoApprove bool, allTools bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "loose", "松", "免审批", "auto":
		return true, false
	case "strict", "严", "全部审批":
		return false, true
	default: // "" / mid / 中
		return false, false
	}
}

// Run 跑一次任务（同步，顶层串行）。strictness 为空 = 中档（默认口径）
func (s *Service) Run(ctx context.Context, goal, recipe string, strictness string) (agentrt.RunResult, error) {
	auto, all := normalizeStrictness(strictness)
	return s.runInner(ctx, agentrt.NewRunID(), goal, recipe, auto, all, 0)
}

// Start 异步跑一次任务：立刻返回运行 id，随后用 State().Running / CurrentRunID 跟踪，
// 结果落在运行记录里（跑完才会出现，运行中的任务靠 running 标记看）。
func (s *Service) Start(goal, recipe, strictness string) (string, error) {
	if strings.TrimSpace(goal) == "" {
		return "", errors.New("任务目标不能为空")
	}
	s.mu.RLock()
	busy := s.running
	s.mu.RUnlock()
	if busy {
		return "", errors.New("已有任务在运行（顶层串行，等它跑完再派）")
	}
	id := agentrt.NewRunID()
	auto, all := normalizeStrictness(strictness)
	// 先把状态挂上，客户端一拿到 id 就能在控制台看到"正在跑"
	s.mu.Lock()
	s.running = true
	s.curRunID = id
	s.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := s.runInner(ctx, id, goal, recipe, auto, all, 0); err != nil {
			s.lg.Warn("Agent 运行结束（有错误）", "runId", id, "err", err, "goal", goal)
		}
	}()
	return id, nil
}

// CurrentRunID 正在跑的那次运行 id（没有则为空）
func (s *Service) CurrentRunID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.curRunID
}

func (s *Service) runInner(ctx context.Context, runID, goal, recipe string, autoApprove, allTools bool, depth int) (agentrt.RunResult, error) {
	if depth == 0 {
		s.runMu.Lock()
		defer s.runMu.Unlock()
		s.mu.Lock()
		s.running = true
		s.curRunID = runID
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.running = false
			s.curRunID = ""
			s.mu.Unlock()
		}()
	}
	if strings.TrimSpace(goal) == "" {
		return agentrt.RunResult{}, errors.New("任务目标不能为空")
	}
	if strings.TrimSpace(recipe) == "" {
		recipe = "chat"
	}

	s.mu.RLock()
	router := s.router
	cfg := s.cfg
	lib := s.skills
	skillMgr := s.skillMgr
	personaLib := s.persona
	browser := s.browser
	s.mu.RUnlock()

	provider, err := router.Pick(recipe)
	if err != nil {
		return agentrt.RunResult{}, fmt.Errorf("没有可用的模型通道（%s）。请编辑 %s 配置一个本地模型（Ollama / vLLM / llama.cpp 均走 OpenAI 兼容协议）",
			s.providerIssues(), filepath.Join(s.dataDir, "config.json"))
	}

	// 每次运行都用一个独立的工具注册表：派发子 Agent 时要带上正确的深度
	reg := tools.NewRegistry()
	tools.RegisterFS(reg, s.ws)
	// 在工作目录内按文件名/内容搜索（纯 Go，不依赖平台有没有 grep/find）
	tools.RegisterFileSearch(reg, s.ws)
	reg.Register(tools.NewShellRun(s.ws, cfg.AllowShell).WithAllowCmds(cfg.ShellAllowCmds))
	reg.Register(tools.NewWebFetch())
	// 联网搜索：只认显式配置的通道（searxng / duckduckgo）；没配就明确报错，不伪造结果
	tools.RegisterWebSearch(reg, tools.WebSearchConfig{
		Provider: cfg.Search.Provider, BaseURL: cfg.Search.BaseURL, APIKey: cfg.Search.APIKey,
		TimeoutSec: cfg.Search.TimeoutSec, MaxResults: cfg.Search.MaxResults,
	})
	// 浏览器工具：打开动态页面 / 跑 JS / 截图。没配浏览器时工具仍注册，调用会明确报"没有可用浏览器"，不静默
	if cfg.Browser.Enabled && browser != nil {
		reg.Register(&tools.BrowserTool{Sess: browser, WS: s.ws})
	}
	tools.RegisterMemory(reg, s.mem, cfg.Memory.Namespace)
	// 笔记（记忆星图）：成篇的 Markdown 笔记 + [[双向链接]] 关系网
	tools.RegisterNotes(reg, s.mem, cfg.Memory.Namespace)
	// 知识库（待办 + 密码本的正本在后端）：增/查随便用，删与看密码明文走审批
	kb.RegisterTools(reg, s.kbs)
	// 跨端调度：把 device_list / device_run 交给设备中枢执行，结果落记忆
	tools.RegisterDevices(reg, s.devices, s.recordDeviceResult)
	// 技能管理：Agent 自己把做法沉淀成技能（新建/改写免审批但要快照，删除走审批）
	skills.RegisterTools(reg, skillMgr)
	// 定时任务：Agent 自己安排按点自动跑的活（删除单独成 cron_remove，走审批）
	tools.RegisterCron(reg, s.cronHooks())
	// MCP 外部工具：每次运行前重新注册，配置一改立刻生效（热插拔）
	if n := s.mcp.RegisterInto(reg); n > 0 {
		s.lg.Debug("已注册 MCP 外部工具", "count", n)
	}
	agentrt.RegisterRuntimeTools(reg, &agentrt.DelegateTool{
		Depth: depth, MaxDepth: 2, MaxSubGoal: 600,
		RunSub: func(subCtx context.Context, subGoal, subRecipe string) (string, error) {
			res, err := s.runInner(subCtx, agentrt.NewRunID(), subGoal, subRecipe, autoApprove, allTools, depth+1)
			if err != nil {
				return "", err
			}
			return res.Text, nil
		},
	}, lib)

	// 上下文回放（Scroll 的"取回原文"那一半）：较早的对话被滚出上下文窗口后，
	// 原文仍在 runs.db 的 messages 表里，模型可以用 context_recall 按区间取回来。
	if s.runs != nil {
		reg.Register(agentrt.NewContextRecall(s.runs))
	}

	// 批量调用：**最后注册**，这样它能看见上面所有工具；
	// 它内部会拒绝危险/会改现场的工具（避免绕过审批与快照）。
	tools.RegisterRunToolBatch(reg)

	approver := func(tool string, args map[string]any) bool {
		if autoApprove {
			s.lg.Warn("审批闸门：按请求自动放行危险操作", "tool", tool)
			return true
		}
		// 「记住放行」只在「中」档生效：用户选了「严」档就是要每次都过目，
		// 不能被之前记住的规则绕过去（否则"严"形同虚设）。
		if !allTools && s.toolRemembered(tool) {
			s.lg.Info("审批闸门：该工具已被记住放行，本次直接执行", "tool", tool)
			return true
		}
		timeout := time.Duration(cfg.ApprovalTimeoutSec) * time.Second
		ok, item := s.approvals.Request(ctx, s.currentRun(), tool, args, timeout)
		if !ok {
			s.lg.Warn("审批闸门：危险操作未获批准", "tool", tool, "status", item.Status)
		}
		return ok
	}

	// 主动记忆召回：不是等模型来查，而是派活前就把相关记忆塞进上下文
	sysExtra := s.systemExtra()
	if recalled, tokens := s.recallContext(ctx, goal, cfg); recalled != "" {
		sysExtra += "\n" + recalled
		s.lg.Info("已注入自动召回的记忆", "tokens", tokens)
	}

	// 人设：现读文件拼好（热重载），紧跟身份行进系统提示
	personaText := ""
	if cfg.Persona.Enabled && personaLib != nil {
		personaText = personaLib.Build(cfg.Persona.Files, cfg.Heartbeat.Enabled)
	}

	runner := agentrt.New(agentrt.Config{
		Provider: provider, Fallbacks: router.Fallbacks(),
		Tools: reg, Memory: s.mem, Hooks: s.hooks, Store: s.runs,
		Workspace: s.ws, Checkpoints: s.ck, Summarizer: memory.NewLLMSummarizer(provider),
		Logger: s.lg, Recipe: recipe, SessionID: "backend", RunID: runID,
		MaxSteps: cfg.MaxSteps, MaxRetries: cfg.MaxRetries, TokenBudget: cfg.TokenBudget,
		SystemExtra: sysExtra, Persona: personaText,
		Approve: approver, ApproveAllTools: allTools, CheckpointBeforeWrite: true,
	})

	res, runErr := runner.Run(ctx, goal)
	if depth == 0 {
		s.mu.Lock()
		s.lastRun = &res
		s.curRunID = ""
		s.mu.Unlock()
	}
	return res, runErr
}

// systemExtra 把技能清单（渐进式披露）与可调度设备塞进系统提示
func (s *Service) systemExtra() string {
	s.mu.RLock()
	lib := s.skills
	devices := s.devices
	s.mu.RUnlock()
	var b strings.Builder
	if devices != nil {
		if list, err := devices.ListDevices(); err == nil {
			if hint := tools.DeviceCapabilitiesHint(list); hint != "" {
				b.WriteString("【可调度设备】\n" + hint + "\n" +
					"需要在本机之外（另一台电脑/手机）执行时，用 device_run 把任务派过去并等回执。\n")
			}
		}
	}
	if lib != nil && lib.Index() != "" {
		b.WriteString("\n【可用技能】（需要照做时用 skill_load 取全文）\n" + lib.Index())
	}
	if s.skillMgr != nil && strings.TrimSpace(s.skillMgr.Dir()) != "" {
		b.WriteString("\n【技能管理】把反复用到的做法沉淀成技能：新建/改写用 skill_manage（免审批），" +
			"删技能用 skill_delete（要人工审批，且只归档不硬删）。技能名用小写字母/数字/连字符，description 一句话说清触发场景。\n")
	}
	b.WriteString("\n【长期记忆】用 memory 工具：store 记住值得长期留的事（**同一把 docKey 再写是覆盖更新**，" +
		"用来记「最新情况」而不是堆旧账）；recall 想相关的；hybrid_search 混合检索；kinds 看有哪些分区。" +
		"忘掉某条用 memory_forget（要人工审批）。\n")
	if s.kbs != nil {
		b.WriteString("\n【知识库】待办与密码本的正本都在这里（手机 App 与本机共用同一份）。\n" +
			"· 加待办用 kb_add_todo，查待办用 kb_list_todos，加密码用 kb_add_password，找密码用 kb_search_passwords —— 这些不需要审批，直接用。\n" +
			"· 删待办 / 删密码 / 看密码明文（kb_delete_todo、kb_delete_password、kb_reveal_password）需要人工审批，只在用户明确要求时调用。\n")
	}
	if s.mcp != nil {
		if hint := s.mcp.Hint(); hint != "" {
			b.WriteString("\n" + hint)
		}
	}
	return b.String()
}

// recordDeviceResult 跨端任务结果统一落进长期记忆（文档 §10：结果统一落记忆并通知）
func (s *Service) recordDeviceResult(res tools.DeviceResult) {
	if s.mem == nil {
		return
	}
	title := "跨端任务：" + res.Action + " @" + res.DeviceID
	content := fmt.Sprintf("设备 %s；动作 %s；任务 %s；状态 %s", res.DeviceID, res.Action, res.TaskID, res.Status)
	if len(res.Output) > 0 {
		raw, err := json.Marshal(res.Output)
		if err == nil {
			content += "；结果 " + string(raw)
		}
	}
	if res.Error != "" {
		content += "；说明 " + res.Error
	}
	kind := "task"
	if res.Status != "done" {
		kind = "error"
	}
	if _, err := s.mem.Ingest(content, memory.IngestOptions{
		Source: "device", SourceRef: "task:" + res.TaskID, Kind: kind, Title: title,
		// 机械回执归到「日常流水」，核心记忆留给真正的结论
		Category: memory.CategoryDaily,
		// 同一台设备的同一个动作只留最新一次回执（详见 memory.DeviceTaskKey）
		DocKey: memory.DeviceTaskKey(res.DeviceID, res.Action),
	}); err != nil {
		s.lg.Warn("跨端任务结果写记忆失败", "task", res.TaskID, "err", err)
	}
}

/* ---------- 记忆树的后台维护 ---------- */

// RebuildMemoryTree 立刻把摘要过期的天重建一遍（返回重建的天数）。
// 摘要要调模型，所以只在"真有新记忆写进来"的天才动手；没有过期的一个请求都不发。
func (s *Service) RebuildMemoryTree(ctx context.Context) (int, error) {
	if s.mem == nil {
		return 0, errors.New("记忆库未启用")
	}
	s.mu.RLock()
	router := s.router
	s.mu.RUnlock()
	if router == nil {
		return 0, errors.New("模型通道还没建起来")
	}
	p, err := router.Pick("chat")
	if err != nil {
		return 0, errors.New("没有可用的模型通道，生成摘要要先配一条：" + err.Error())
	}
	return s.mem.RebuildTreeIfStale(ctx, memory.NewLLMSummarizer(p), 8)
}

// treeLoop 每小时看一眼记忆树的摘要跟没跟上，没跟上就补一次。
// 放在后台而不是"每次写入都重建"：摘要要调模型，写一条就摘要一次太贵也没有意义。
func (s *Service) treeLoop(stop <-chan struct{}) {
	// 启动后先等一会儿再跑第一轮：别和刚起来的自检、迁移抢模型额度
	first := time.NewTimer(45 * time.Second)
	defer first.Stop()
	select {
	case <-stop:
		return
	case <-first.C:
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		s.rebuildTreeOnce()
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

func (s *Service) rebuildTreeOnce() {
	if s.mem == nil {
		return
	}
	days, err := s.mem.StaleDays(8)
	if err != nil {
		s.lg.Warn("查记忆树摘要状态失败", "err", err)
		return
	}
	if len(days) == 0 {
		return
	}
	n, err := s.RebuildMemoryTree(context.Background())
	if err != nil {
		// 没配模型通道时这里会一直失败：只记一条 debug，不刷屏
		s.lg.Debug("记忆树摘要没重建", "days", len(days), "err", err)
		return
	}
	if n > 0 {
		s.lg.Info("已重建记忆树摘要", "days", n)
	}
}

func (s *Service) currentRun() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.curRunID
}

/* ---------- 查询接口 ---------- */

// Runs 最近运行
func (s *Service) Runs(limit int) ([]agentrt.RunRecord, error) { return s.runs.Runs(limit) }

// RunDetail 单次运行 + 对话消息
func (s *Service) RunDetail(id string) (Detail, error) {
	rec, ok, err := s.runs.Run(id)
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, fmt.Errorf("没有这次运行：%s", id)
	}
	msgs, err := s.runs.Messages(id)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Run: rec, Messages: msgs}, nil
}

// Memory 记忆库（供控制台检索/看树）
func (s *Service) Memory() *memory.Store { return s.mem }

// KB 知识库（待办 + 密码本的正本）
func (s *Service) KB() *kb.Service { return s.kbs }

// Activity 当前活动快照（始终开启的活动追踪；控制台/手机端用它显示「现在在干什么」）
func (s *Service) Activity() activity.Snapshot {
	if s.activity == nil {
		return activity.Snapshot{Level: activity.Level, Stage: activity.StageIdle, Steps: []activity.Step{}}
	}
	return s.activity.Snapshot()
}

// Workdir 工作目录
func (s *Service) Workdir() string { return s.ws.Root() }

// DataDir 数据目录
func (s *Service) DataDir() string { return s.dataDir }

// Checkpoints 快照列表
func (s *Service) Checkpoints() ([]agentrt.CheckpointInfo, error) { return s.ck.List() }

// Rollback 回滚到某个快照
func (s *Service) Rollback(id string) error { return s.ck.Rollback(id) }

// Approvals 审批列表
func (s *Service) Approvals() []Approval { return s.approvals.List() }

// Approve 批准
func (s *Service) Approve(id, by string) error { return s.approvals.Approve(id, by) }

// 审批「记住放行」的口径
const (
	RememberNone    = ""        // 只批这一次
	RememberSession = "session" // 本次进程内不再问（重启即忘）
	RememberAlways  = "always"  // 永久放行（写进配置，重启仍在）
)

// ApproveWith 批准一条审批，并可选地"记住这个工具下次别再问"。
//
// 为什么单独加一个方法而不是改 Approve 的签名：Approve 已被接口层与测试用着，
// 改签名会把无关的地方一起牵动；这里只加能力，不动旧调用。
func (s *Service) ApproveWith(id, by, remember string) error {
	tool := ""
	for _, a := range s.approvals.List() {
		if a.ID == id {
			tool = a.Tool
			break
		}
	}
	if err := s.approvals.Approve(id, by); err != nil {
		return err
	}
	remember = strings.ToLower(strings.TrimSpace(remember))
	if tool == "" || remember == RememberNone {
		return nil
	}
	switch remember {
	case RememberSession:
		s.mu.Lock()
		s.approvedTools[tool] = true
		s.mu.Unlock()
		s.lg.Warn("审批：已记住本次放行（仅本进程有效）", "tool", tool, "by", by)
	case RememberAlways:
		// 只写配置，**不**再往会话集合里塞：两个 scope 各自独立，
		// 否则撤销 always 之后会话里的那条还在，"撤销"就撤不干净（踩过）。
		if err := s.persistApprovalAllow(tool); err != nil {
			return err
		}
		s.lg.Warn("审批：已永久放行该工具（写进配置）", "tool", tool, "by", by)
	default:
		return fmt.Errorf("不认识的 remember 取值：%q（可用 %s / %s）", remember, RememberSession, RememberAlways)
	}
	return nil
}

// toolRemembered 该工具是否已被"记住放行"（会话级或永久）
func (s *Service) toolRemembered(tool string) bool {
	s.mu.RLock()
	ok := s.approvedTools[tool]
	s.mu.RUnlock()
	if ok {
		return true
	}
	for _, t := range s.Config().ApprovalAllow {
		if t == tool {
			return true
		}
	}
	return false
}

func (s *Service) persistApprovalAllow(tool string) error {
	cfg := s.Config()
	for _, t := range cfg.ApprovalAllow {
		if t == tool {
			return nil
		}
	}
	cfg.ApprovalAllow = append(cfg.ApprovalAllow, tool)
	if err := s.SaveConfig(cfg); err != nil {
		return fmt.Errorf("写入永久放行清单失败：%w", err)
	}
	return nil
}

// ApprovalAllow 当前放行清单：always（永久，配置里）+ session（本进程内记住的）
func (s *Service) ApprovalAllow() map[string]any {
	s.mu.RLock()
	session := make([]string, 0, len(s.approvedTools))
	for t := range s.approvedTools {
		session = append(session, t)
	}
	s.mu.RUnlock()
	sort.Strings(session)
	always := s.Config().ApprovalAllow
	if always == nil {
		always = []string{}
	}
	return map[string]any{"always": always, "session": session}
}

// ClearApprovalAllow 撤销放行。scope：always / session / all（空 = all）。
// tool 为空表示清空该 scope 下全部，否则只撤这一个工具。
//
// 必须能撤：只给"永久放行"不给撤销的口子，等于让一次误点永久生效。
func (s *Service) ClearApprovalAllow(scope, tool string) error {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		scope = "all"
	}
	tool = strings.TrimSpace(tool)
	if scope == "session" || scope == "all" {
		s.mu.Lock()
		if tool == "" {
			s.approvedTools = map[string]bool{}
		} else {
			delete(s.approvedTools, tool)
		}
		s.mu.Unlock()
	}
	if scope == "always" || scope == "all" {
		cfg := s.Config()
		kept := make([]string, 0, len(cfg.ApprovalAllow))
		for _, t := range cfg.ApprovalAllow {
			if tool == "" || t != tool {
				kept = append(kept, t)
			}
		}
		cfg.ApprovalAllow = kept
		if err := s.SaveConfig(cfg); err != nil {
			return err
		}
	}
	return nil
}

// Reject 拒绝
func (s *Service) Reject(id, by, reason string) error { return s.approvals.Reject(id, by, reason) }

// State 控制台状态
func (s *Service) State() State {
	s.mu.RLock()
	cfg := s.cfg
	providers := append([]ProviderInfo{}, s.providers...)
	lib := s.skills
	running := s.running
	curRun := s.curRunID
	last := s.lastRun
	s.mu.RUnlock()

	runs, err := s.runs.Runs(10)
	if err != nil {
		s.lg.Warn("读运行记录失败", "err", err)
	}
	stats, err := s.mem.Stats()
	if err != nil {
		s.lg.Warn("读记忆统计失败", "err", err)
	}
	st := State{
		Version: Version, DataDir: s.dataDir, Workdir: s.ws.Root(),
		AllowShell: cfg.AllowShell, AllowRemote: cfg.AllowRemote, Running: running,
		CurrentRunID: curRun,
		Providers:    providers, Runs: runs, Approvals: s.approvals.List(),
		Cron: s.jobStates(), Memory: stats, LastRun: last,
		Heartbeat: s.heartbeatState(),
		BackupDir: backup.Dir(s.dataDir),
		MemoryCfg: cfg.Memory, Embedding: s.embeddingInfo(),
		Activity: s.Activity(),
	}
	if s.mcp != nil {
		st.MCP = s.mcp.Status()
	}
	if list, err := backup.List(s.dataDir); err == nil {
		st.Backups = list
	} else {
		s.lg.Warn("读备份列表失败", "err", err)
	}
	st.SkillsDir = cfg.SkillsDir
	if strings.TrimSpace(st.SkillsDir) == "" {
		st.SkillsDir = filepath.Join(s.dataDir, "skills")
	}
	if lib != nil {
		st.Skills = lib.All()
	}
	if len(providers) == 0 {
		st.Issue = "还没有配置模型通道：在 " + filepath.Join(s.dataDir, "config.json") + " 里加一个本地模型即可"
	} else {
		usable := 0
		for _, p := range providers {
			if p.Usable {
				usable++
			}
		}
		if usable == 0 {
			st.Issue = "没有可用模型通道：" + s.providerIssues()
		}
	}
	return st
}

func isLocalBase(u string) bool {
	s := strings.ToLower(strings.TrimSpace(u))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	host := strings.Split(s, "/")[0]
	host = strings.Split(host, ":")[0]
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.")
}
