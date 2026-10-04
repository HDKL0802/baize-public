package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServerState 单个 MCP 服务的运行状态（给控制台 / CLI 看）
type ServerState struct {
	Name          string       `json:"name"`
	Transport     string       `json:"transport"`
	Status        string       `json:"status"` // disabled | connecting | connected | error
	Error         string       `json:"error,omitempty"`
	Note          string       `json:"note,omitempty"`
	ToolCount     int          `json:"toolCount"`
	Tools         []string     `json:"tools,omitempty"`
	LatencyMs     int64        `json:"latencyMs,omitempty"`
	Protocol      string       `json:"protocol,omitempty"`
	ServerName    string       `json:"serverName,omitempty"`
	ServerVersion string       `json:"serverVersion,omitempty"`
	ConnectedAt   int64        `json:"connectedAt,omitempty"`
	Config        ServerConfig `json:"config"`
}

// ToolRef 一个可直接注册进本地工具中心的远端工具
type ToolRef struct {
	Server      string         `json:"server"`
	Remote      string         `json:"remote"`
	Name        string         `json:"name"` // 本地工具名（带前缀）
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema,omitempty"`
	Dangerous   bool           `json:"dangerous"`
}

// Manager MCP 服务管理器：负责连接、状态、以及热插拔（加/删/改/重载都不用重启后端）
type Manager struct {
	lg          *slog.Logger
	allowRemote bool

	mu      sync.RWMutex
	order   []string
	entries map[string]*entry
}

type entry struct {
	raw         ServerConfig
	cfg         ServerConfig
	normErr     error
	fingerprint string
	client      *Client
	state       ServerState
}

// NewManager 创建管理器
func NewManager(lg *slog.Logger, allowRemote bool) *Manager {
	if lg == nil {
		lg = slog.Default()
	}
	return &Manager{lg: lg, allowRemote: allowRemote, entries: map[string]*entry{}}
}

// SetAllowRemote 调整"是否允许远端 MCP 地址"（配置改了要跟着改，改完记得重新 Apply）
func (m *Manager) SetAllowRemote(v bool) {
	m.mu.Lock()
	m.allowRemote = v
	m.mu.Unlock()
}

// AllowRemote 当前是否放行远端地址
func (m *Manager) AllowRemote() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allowRemote
}

// Apply 按传入的配置列表做一次收敛：
// 新增的连上、改动的重连、删掉的断开、禁用的断开但保留配置。
// 坏配置不会静默丢掉，会以 error 状态挂在列表里。
func (m *Manager) Apply(cfgs []ServerConfig) []ServerState {
	m.mu.Lock()
	desired := map[string]ServerConfig{}
	order := make([]string, 0, len(cfgs))
	for i, c := range cfgs {
		raw := c.Clone()
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = fmt.Sprintf("unnamed-%d", i+1)
			raw.Name = name
		}
		if _, dup := desired[name]; dup {
			m.lg.Warn("MCP 服务重名，只保留最后一个", "name", name)
			continue
		}
		desired[name] = raw
		order = append(order, name)
	}

	// 1) 删掉不再需要的
	for name, e := range m.entries {
		if _, ok := desired[name]; !ok {
			m.closeClient(e)
			delete(m.entries, name)
			m.lg.Info("MCP 服务已下线", "name", name)
		}
	}

	// 2) 决定每个服务接下来怎么处理（关旧连接在本步完成）
	type task struct {
		name        string
		raw         ServerConfig
		cfg         ServerConfig
		normErr     error
		needConnect bool
	}
	tasks := make([]task, 0, len(order))
	for _, name := range order {
		raw := desired[name]
		cfg, nerr := raw.normalize(m.allowRemote)
		t := task{name: name, raw: raw, cfg: cfg, normErr: nerr}
		old := m.entries[name]
		fp := fingerprint(raw, nerr)
		switch {
		case nerr != nil:
			t.needConnect = false
		case !raw.Enabled:
			t.needConnect = false
		default:
			t.needConnect = old == nil || old.client == nil || old.fingerprint != fp
		}
		tasks = append(tasks, t)
		if old != nil && (t.needConnect || nerr != nil || !raw.Enabled) {
			m.closeClient(old)
		}
		if t.needConnect {
			m.entries[name] = &entry{
				raw: raw, cfg: cfg, normErr: nerr, fingerprint: fp,
				state: ServerState{Name: name, Transport: cfg.Transport, Status: "connecting", Config: raw},
			}
			continue
		}
		if old == nil {
			old = &entry{}
			m.entries[name] = old
		}
		old.raw, old.cfg, old.normErr, old.fingerprint = raw, cfg, nerr, fp
		switch {
		case nerr != nil:
			old.client = nil
			old.state = ServerState{Name: name, Transport: transportOf(raw), Status: "error", Error: nerr.Error(), Config: raw}
		case !raw.Enabled:
			old.client = nil
			old.state = ServerState{Name: name, Transport: cfg.Transport, Status: "disabled", Config: raw}
		default:
			old.state.Config = raw
		}
	}
	m.order = order
	m.mu.Unlock()

	// 3) 真正去连（不占锁，避免连接慢的时候把控制台查询卡住）
	for _, t := range tasks {
		if !t.needConnect {
			continue
		}
		state := m.connect(t.name, t.cfg)
		m.mu.Lock()
		if e, ok := m.entries[t.name]; ok {
			e.client = state.client
			e.state = state.state
		}
		m.mu.Unlock()
	}
	return m.Status()
}

// connectResult 一次连接的结果（失败也返回，状态即结果）
type connectResult struct {
	client *Client
	state  ServerState
}

// connect 连一个服务并拉取工具清单
func (m *Manager) connect(name string, cfg ServerConfig) connectResult {
	out := connectResult{state: ServerState{Name: name, Transport: cfg.Transport, Status: "error", Config: cfg}}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec+10)*time.Second)
	defer cancel()

	client, err := Connect(ctx, cfg, m.AllowRemote())
	if err != nil {
		out.state.Error = err.Error()
		m.lg.Warn("MCP 服务连接失败", "name", name, "transport", cfg.Transport, "err", err)
		return out
	}
	out.client = client
	out.state.LatencyMs = time.Since(start).Milliseconds()
	info := client.Info()
	out.state.Protocol = info.ProtocolVersion
	out.state.ServerName = info.ServerInfo.Name
	out.state.ServerVersion = info.ServerInfo.Version
	out.state.ConnectedAt = time.Now().UnixMilli()
	out.state.Status = "connected"

	tools, terr := client.RefreshTools(ctx)
	if terr != nil {
		if errors.Is(terr, ErrNoToolsCapability) {
			out.state.Note = "该服务没有暴露工具（连接正常）"
			m.lg.Info("MCP 服务已连接（无工具）", "name", name, "server", info.ServerInfo.Name)
			return out
		}
		out.state.Note = "已连接，但拉取工具清单失败：" + terr.Error()
		m.lg.Warn("MCP 工具清单拉取失败", "name", name, "err", terr)
		return out
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	out.state.ToolCount = len(tools)
	out.state.Tools = names
	m.lg.Info("MCP 服务已连接", "name", name, "server", info.ServerInfo.Name,
		"protocol", info.ProtocolVersion, "tools", len(tools), "cost", out.state.LatencyMs)
	return out
}

// Status 当前所有 MCP 服务的状态（按配置顺序）
func (m *Manager) Status() []ServerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerState, 0, len(m.order))
	for _, name := range m.order {
		e, ok := m.entries[name]
		if !ok {
			continue
		}
		st := e.state
		st.Config = e.raw
		out = append(out, st)
	}
	return out
}

// Configs 当前配置列表（原样，用于回写 config.json）
func (m *Manager) Configs() []ServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerConfig, 0, len(m.order))
	for _, name := range m.order {
		if e, ok := m.entries[name]; ok {
			out = append(out, e.raw.Clone())
		}
	}
	return out
}

// ServerConfig 取某个服务的配置
func (m *Manager) ServerConfig(name string) (ServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return ServerConfig{}, false
	}
	return e.raw.Clone(), true
}

// ToolRefs 汇总所有已连接服务的工具（本地工具名的前缀在配置里定）
func (m *Manager) ToolRefs() []ToolRef {
	m.mu.RLock()
	type pair struct {
		raw    ServerConfig // 原样配置（安全名单、信任开关看这个）
		norm   ServerConfig // 归一化后的配置（工具名前缀看这个）
		client *Client
	}
	pairs := make([]pair, 0, len(m.order))
	for _, name := range m.order {
		e, ok := m.entries[name]
		if !ok || e.client == nil || e.normErr != nil || !e.raw.Enabled {
			continue
		}
		pairs = append(pairs, pair{raw: e.raw, norm: e.cfg, client: e.client})
	}
	m.mu.RUnlock()

	var refs []ToolRef
	for _, p := range pairs {
		for _, t := range p.client.Tools() {
			refs = append(refs, ToolRef{
				Server:      p.raw.Name,
				Remote:      t.Name,
				Name:        p.norm.ToolName(t.Name),
				Description: describeRemote(p.raw, t),
				Schema:      t.InputSchema,
				Dangerous:   isDangerous(p.raw, t.Name),
			})
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs
}

// Tools 把远端工具包装成可直接注册的本地工具
func (m *Manager) Tools() []Tool {
	refs := m.ToolRefs()
	out := make([]Tool, 0, len(refs))
	for _, ref := range refs {
		out = append(out, NewServerTool(m, ref))
	}
	return out
}

// CallTool 直接调某个服务的某个远端工具（CLI / 控制台手动调用用）
func (m *Manager) CallTool(ctx context.Context, server, toolName string, args map[string]any) (CallResult, error) {
	m.mu.RLock()
	e, ok := m.entries[server]
	m.mu.RUnlock()
	if !ok {
		return CallResult{}, fmt.Errorf("没有这个 MCP 服务：%s", server)
	}
	if e.client == nil {
		if e.state.Error != "" {
			return CallResult{}, fmt.Errorf("MCP 服务 %s 当前不可用：%s", server, e.state.Error)
		}
		return CallResult{}, fmt.Errorf("MCP 服务 %s 当前未连接（状态：%s）", server, e.state.Status)
	}
	return e.client.CallTool(ctx, toolName, args)
}

// Reload 重连某个服务（配置没变也可以强制重连）
func (m *Manager) Reload(name string) []ServerState {
	cfgs := m.Configs()
	for i := range cfgs {
		if cfgs[i].Name != name {
			continue
		}
		m.mu.Lock()
		if e, ok := m.entries[name]; ok {
			m.closeClient(e)
			e.client = nil
			delete(m.entries, name) // 强制走"重连"分支
		}
		m.mu.Unlock()
	}
	return m.Apply(cfgs)
}

// Remove 删掉一个服务（配置一并移除）
func (m *Manager) Remove(name string) []ServerState {
	cfgs := m.Configs()
	kept := make([]ServerConfig, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Name != name {
			kept = append(kept, c)
		}
	}
	return m.Apply(kept)
}

// Upsert 新增或更新一个服务（按 name 匹配）
func (m *Manager) Upsert(cfg ServerConfig) ([]ServerState, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("MCP 服务缺少 name")
	}
	cfgs := m.Configs()
	found := false
	for i := range cfgs {
		if cfgs[i].Name == cfg.Name {
			cfgs[i] = cfg.Clone()
			found = true
			break
		}
	}
	if !found {
		cfgs = append(cfgs, cfg.Clone())
	}
	return m.Apply(cfgs), nil
}

// Close 断开所有服务
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, e := range m.entries {
		m.closeClient(e)
		delete(m.entries, name)
	}
	m.order = nil
}

/* ---------- 内部工具 ---------- */

func (m *Manager) closeClient(e *entry) {
	if e == nil || e.client == nil {
		return
	}
	_ = e.client.Close()
	e.client = nil
}

func fingerprint(raw ServerConfig, normErr error) string {
	if normErr != nil {
		return "invalid:" + normErr.Error()
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprintf("%v", raw)
	}
	if !raw.Enabled {
		return "disabled:" + string(b)
	}
	return string(b)
}

func transportOf(raw ServerConfig) string {
	t := strings.ToLower(strings.TrimSpace(raw.Transport))
	if t == "" {
		if strings.TrimSpace(raw.Command) != "" {
			return "stdio"
		}
		return "http"
	}
	if t == "sse" || t == "streamable-http" {
		return "http"
	}
	return t
}

func describeRemote(cfg ServerConfig, t ToolInfo) string {
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = "MCP 工具 " + t.Name + "（来自服务 " + cfg.Name + "）"
	}
	return "[MCP:" + cfg.Name + "] " + desc
}

// isDangerous 默认一律需要审批；只有显式信任（autoApprove）或列进 safeTools 才免批
func isDangerous(cfg ServerConfig, remote string) bool {
	if cfg.AutoApprove {
		return false
	}
	for _, s := range cfg.SafeTools {
		if strings.EqualFold(strings.TrimSpace(s), remote) {
			return false
		}
	}
	return true
}
