// Package config 是后端 Agent 的运行配置（data/config.json）。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"baize/internal/mcp"
	"baize/internal/persona"
)

// Provider 一个模型通道
type Provider struct {
	Name       string   `json:"name"`
	Protocol   string   `json:"protocol"` // openai | anthropic
	BaseURL    string   `json:"baseUrl"`
	APIKey     string   `json:"apiKey,omitempty"`
	Model      string   `json:"model"`
	Kinds      []string `json:"kinds,omitempty"`    // 该通道负责的任务类型；空=通吃
	Fallback   bool     `json:"fallback,omitempty"` // 是否作为回退通道
	TimeoutSec int      `json:"timeoutSec,omitempty"`
}

// Embedding 向量化通道（记忆的语义检索用）。
// 刻意与对话通道分开：向量模型和对话模型常常不是同一家（DeepSeek 就没有 embedding 接口）。
// 不配这条通道时记忆退回关键词检索，界面会明说"语义检索未启用"，不假装有向量。
type Embedding struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"` // 目前只支持 openai 兼容的 /embeddings
	BaseURL    string `json:"baseUrl"`
	APIKey     string `json:"apiKey,omitempty"`
	Model      string `json:"model"`
	Dim        int    `json:"dim,omitempty"` // 0 = 首次调用后自动记录
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// Voice 语音通道（说得出话 + 听得懂话）。
//
// 刻意与对话通道分开：语音模型常常和对话模型不是一家（DeepSeek 没有语音接口，
// 而阿里百炼有 qwen-tts / paraformer）。Protocol 决定"怎么调"：
//
//	openai    —— 标准 OpenAI 音频协议：/audio/speech + /audio/transcriptions
//	dashscope —— 阿里云百炼原生：qwen-tts 合成（返回音频地址，后端取回来）
//	             + paraformer-realtime-v2 实时听写（后端走 WebSocket 转写）
//
// 不配这条通道时，语音功能一律明确报"没配语音通道"，不静默降级成没声音。
type Voice struct {
	Enabled    bool    `json:"enabled"`
	Protocol   string  `json:"protocol"` // openai | dashscope
	BaseURL    string  `json:"baseUrl"`  // openai 协议用；dashscope 留空即官方地址
	APIKey     string  `json:"apiKey,omitempty"`
	TTSModel   string  `json:"ttsModel"`   // 合成（说话）用
	STTModel   string  `json:"sttModel"`   // 听写（听话）用
	Voice      string  `json:"voice"`      // 默认音色 id（吉祥物音色）
	AutoSpeak  bool    `json:"autoSpeak"`  // 智能体回复是否自动朗读
	Speed      float64 `json:"speed,omitempty"`
	TimeoutSec int     `json:"timeoutSec,omitempty"`
}

// Memory 记忆（记忆术）设置
type Memory struct {
	AutoRecall     bool    `json:"autoRecall"`     // 每次派活前自动召回相关记忆注入上下文
	RecallProfile  string  `json:"recallProfile"`  // balanced | semantic | lexical | graph_first
	RecallBudget   int     `json:"recallBudget"`   // 自动召回注入的 token 预算
	RecallMinScore float64 `json:"recallMinScore"` // 自动召回的最低向量相似度（没配 embedding 时该项不生效）
	Namespace      string  `json:"namespace"`      // 本机默认记忆分区（多用户/多智能体隔离用）
}

// Persona 人设（一组 Markdown 文件，按顺序拼进系统提示；改完立即生效）。
// Files 就是「加载清单」：顺序 = 拼进提示的顺序，不在清单里的文件不生效。
// 空数组 = 一个都不加载；字段缺失（null）时回落成默认三件套。
type Persona struct {
	Enabled bool     `json:"enabled"`
	Files   []string `json:"files"`
}

// CronJob 定时任务（昆帕定时任务机制的 Go 版）
type CronJob struct {
	ID          string `json:"id"`
	Expr        string `json:"expr"` // 五段 cron 或 @every 30m / @daily
	Goal        string `json:"goal"`
	Recipe      string `json:"recipe,omitempty"`
	Enabled     bool   `json:"enabled"`
	AutoApprove bool   `json:"autoApprove,omitempty"` // 危险操作是否免审批（默认 false，走审批队列）
	LastRunAt   int64  `json:"lastRunAt,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	Runs        int    `json:"runs,omitempty"`
}

// HeartbeatConfig 心跳任务配置（定期运行 agent 并可选择分发结果）
type HeartbeatConfig struct {
	Enabled        bool   `json:"enabled"`
	Every          string `json:"every"`           // 间隔：@every 30m 或 cron 表达式
	Target         string `json:"target,omitempty"` // 分发目标：main（仅运行）、last（上次聊天渠道）、inbox（收件箱）
	TimeoutSec     int    `json:"timeoutSec,omitempty"`
	// ActiveHours 不能加 omitempty：零值 ""（全天）与默认值 "08:00-22:00" 语义不同，
	// 一旦省略键，Load 时会被 Default() 重新填回默认值，用户永远清不掉。
	ActiveHours    string `json:"activeHours"`          // 活跃时间段，如 "08:00-22:00"，空=全天
	LastRunAt      int64  `json:"lastRunAt,omitempty"`
	LastStatus     string `json:"lastStatus,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	Runs           int    `json:"runs,omitempty"`
}

// Config 后端 Agent 配置
type Config struct {
	AllowRemote        bool       `json:"allowRemote"` // 是否允许非本机模型地址 / 远端 MCP 服务
	Providers          []Provider `json:"providers"`
	Workdir            string     `json:"workdir"` // 空 = <data>/workspace
	SkillsDir          string     `json:"skillsDir"`
	AllowShell         bool       `json:"allowShell"`
	MaxSteps           int        `json:"maxSteps"`
	MaxRetries         int        `json:"maxRetries"`
	TokenBudget        int        `json:"tokenBudget"`
	ApprovalTimeoutSec int        `json:"approvalTimeoutSec"` // 危险操作等人工审批的超时（超时按拒绝处理）
	Cron               []CronJob  `json:"cron"`
	// DeviceRemarks 设备备注（key = 设备 id）。这是用户的标注而不是设备上报的状态，
	// 所以放在配置里：设备重装、换网络都不会把备注弄丢。
	DeviceRemarks  map[string]string  `json:"deviceRemarks,omitempty"`
	AutoRunOnStart bool               `json:"autoRunOnStart"` // 启动时是否立刻跑一次定时任务检查
	MCPServers     []mcp.ServerConfig `json:"mcpServers"`     // MCP 外部服务（热插拔）
	BackupKeep     int                `json:"backupKeep"`     // 自动备份保留份数，默认 10
	Embedding      Embedding          `json:"embedding"`      // 向量化通道（记忆语义检索）
	Memory         Memory             `json:"memory"`         // 记忆术设置
	Voice          Voice              `json:"voice"`          // 语音通道（说话 / 听写）
	Persona        Persona            `json:"persona"`        // 人设文件（Markdown 进系统提示）
	Heartbeat      HeartbeatConfig    `json:"heartbeat"`      // 心跳任务（定期运行 agent）
}

// 记忆检索的权重档位（非法值一律回落到 balanced）
var recallProfiles = []string{"balanced", "semantic", "lexical", "graph_first"}

// Default 默认配置（默认没有任何模型通道：宁可明确报"没模型"，也不偷偷上云）
func Default() Config {
	return Config{
		AllowRemote:        false,
		Providers:          []Provider{},
		MaxSteps:           12,
		MaxRetries:         2,
		TokenBudget:        8000,
		ApprovalTimeoutSec: 300,
		Cron:               []CronJob{},
		MCPServers:         []mcp.ServerConfig{},
		BackupKeep:         10,
		Memory: Memory{
			AutoRecall:     true,
			RecallProfile:  "balanced",
			RecallBudget:   600,
			RecallMinScore: 0.35,
			Namespace:      "default",
		},
		Persona: Persona{Enabled: true, Files: append([]string{}, persona.DefaultFiles...)},
		Heartbeat: HeartbeatConfig{
			Enabled:     false,
			Every:       "@every 30m",
			Target:      "main",
			TimeoutSec:  600,
			ActiveHours: "08:00-22:00",
		},
	}
}

// Load 读取配置（不存在时返回默认值并落盘一份，方便用户照着改）
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := Default()
			if err := Save(dir, cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
		return Default(), fmt.Errorf("读取配置失败（%s）：%w", path, err)
	}
	cfg := Default()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("配置解析失败（%s）：%w", path, err)
		}
	}
	cfg.normalize()
	return cfg, nil
}

// Save 写回配置
func Save(dir string, cfg Config) error {
	cfg.normalize()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) normalize() {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 12
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.TokenBudget <= 0 {
		c.TokenBudget = 8000
	}
	if c.ApprovalTimeoutSec <= 0 {
		c.ApprovalTimeoutSec = 300
	}
	if c.Providers == nil {
		c.Providers = []Provider{}
	}
	if c.Cron == nil {
		c.Cron = []CronJob{}
	}
	if c.MCPServers == nil {
		c.MCPServers = []mcp.ServerConfig{}
	}
	if c.BackupKeep <= 0 {
		c.BackupKeep = 10
	}
	for i := range c.Providers {
		if c.Providers[i].TimeoutSec <= 0 {
			c.Providers[i].TimeoutSec = 120
		}
		if c.Providers[i].Protocol == "" {
			c.Providers[i].Protocol = "openai"
		}
	}
	// 记忆术设置：缺项补齐，档位写错就直接回落，别让手改 JSON 的人踩空
	if strings.TrimSpace(c.Memory.Namespace) == "" {
		c.Memory.Namespace = "default"
	}
	c.Memory.RecallProfile = normalizeRecallProfile(c.Memory.RecallProfile)
	if c.Memory.RecallBudget <= 0 {
		c.Memory.RecallBudget = 600
	}
	if c.Memory.RecallMinScore <= 0 || c.Memory.RecallMinScore > 1 {
		c.Memory.RecallMinScore = 0.35
	}
	// 人设：顺序与启停都在 Files 里（顺序 = 拼进系统提示的顺序）。
	// 只做"清理"不做"补默认"：字段缺失时 Load 已用 Default 兜底，这里再补会把
	// 用户刻意留空的清单又填回去。
	if c.Persona.Files == nil {
		c.Persona.Files = append([]string{}, persona.DefaultFiles...)
	}
	cleaned := make([]string, 0, len(c.Persona.Files))
	seen := map[string]bool{}
	for _, name := range c.Persona.Files {
		name = strings.TrimSpace(name)
		if !persona.Valid(name) || seen[name] {
			continue // 非法文件名 / 重复项直接丢掉，别让它们躺在配置里装样子
		}
		seen[name] = true
		cleaned = append(cleaned, name)
	}
	c.Persona.Files = cleaned
	if c.Embedding.Protocol == "" {
		c.Embedding.Protocol = "openai"
	}
	if c.Embedding.TimeoutSec <= 0 {
		c.Embedding.TimeoutSec = 60
	}
	// 语音通道：协议/模型/音色缺项按协议补默认值，非法协议回落 openai。
	// 这一步只是"给出可用的起手值"，不会把关闭的通道打开（Enabled 不动）。
	c.Voice.Protocol = NormalizeVoiceProtocol(c.Voice.Protocol)
	if c.Voice.Protocol == VoiceProtocolDashScope && strings.TrimSpace(c.Voice.BaseURL) == "" {
		c.Voice.BaseURL = DashScopeBaseURL
	}
	if c.Voice.TTSModel == "" {
		c.Voice.TTSModel = defaultTTSModel(c.Voice.Protocol)
	}
	if c.Voice.STTModel == "" {
		c.Voice.STTModel = defaultSTTModel(c.Voice.Protocol)
	}
	if c.Voice.Voice == "" {
		c.Voice.Voice = defaultVoice(c.Voice.Protocol)
	}
	if c.Voice.TimeoutSec <= 0 {
		c.Voice.TimeoutSec = 120
	}
	if c.Voice.Speed <= 0 || c.Voice.Speed > 4 {
		c.Voice.Speed = 1
	}
	// 心跳任务：every 缺省给默认值，target 收敛到合法值，超时给默认值
	if c.Heartbeat.Every == "" {
		c.Heartbeat.Every = "@every 30m"
	}
	if c.Heartbeat.TimeoutSec <= 0 {
		c.Heartbeat.TimeoutSec = 600
	}
	c.Heartbeat.Target = normalizeHeartbeatTarget(c.Heartbeat.Target)
	if c.Heartbeat.ActiveHours != "" {
		c.Heartbeat.ActiveHours = strings.TrimSpace(c.Heartbeat.ActiveHours)
	}
}

// 语音通道支持的协议
const (
	VoiceProtocolOpenAI    = "openai"
	VoiceProtocolDashScope = "dashscope"
)

// DashScopeBaseURL 阿里云百炼官方地址（dashscope 协议不填地址时用它）
const DashScopeBaseURL = "https://dashscope.aliyuncs.com"

// NormalizeVoiceProtocol 收敛协议名（大小写/空格容忍，非法回落 openai）
func NormalizeVoiceProtocol(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case VoiceProtocolDashScope, "bailian", "aliyun", "百炼":
		return VoiceProtocolDashScope
	default:
		return VoiceProtocolOpenAI
	}
}

func defaultTTSModel(protocol string) string {
	if protocol == VoiceProtocolDashScope {
		return "qwen-tts"
	}
	return "tts-1"
}

func defaultSTTModel(protocol string) string {
	if protocol == VoiceProtocolDashScope {
		return "paraformer-realtime-v2"
	}
	return "whisper-1"
}

func defaultVoice(protocol string) string {
	if protocol == VoiceProtocolDashScope {
		return "Cherry"
	}
	return "nova"
}

// normalizeRecallProfile 把档位名收敛到合法值（大小写/空格容忍，非法回落 balanced）
func normalizeRecallProfile(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range recallProfiles {
		if v == p {
			return p
		}
	}
	return "balanced"
}

var heartbeatTargets = []string{"main", "last", "inbox"}

// HeartbeatTargets 心跳结果的分发目标清单（界面下拉用）：
// main = 只留在运行记录里；last / inbox 要等渠道能力落地才真正发得出去。
func HeartbeatTargets() []string { return append([]string{}, heartbeatTargets...) }

// ValidHeartbeatTarget 目标是否合法。接口层先用它挡住笔误，
// 免得写错的目标被 normalize 悄悄改成 main（用户以为设成了 last，其实没有）。
func ValidHeartbeatTarget(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, t := range heartbeatTargets {
		if v == t {
			return true
		}
	}
	return false
}

func normalizeHeartbeatTarget(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, t := range heartbeatTargets {
		if v == t {
			return v
		}
	}
	return "main"
}

// MainProviders 主通道（非回退）
func (c Config) MainProviders() []Provider {
	out := []Provider{}
	for _, p := range c.Providers {
		if !p.Fallback {
			out = append(out, p)
		}
	}
	return out
}

// FallbackProviders 回退通道
func (c Config) FallbackProviders() []Provider {
	out := []Provider{}
	for _, p := range c.Providers {
		if p.Fallback {
			out = append(out, p)
		}
	}
	return out
}
