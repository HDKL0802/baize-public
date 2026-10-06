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
	TTSModel   string  `json:"ttsModel"`  // 合成（说话）用
	STTModel   string  `json:"sttModel"`  // 听写（听话）用
	Voice      string  `json:"voice"`     // 默认音色 id（吉祥物音色）
	AutoSpeak  bool    `json:"autoSpeak"` // 智能体回复是否自动朗读
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
	Enabled    bool   `json:"enabled"`
	Every      string `json:"every"`            // 间隔：@every 30m 或 cron 表达式
	Target     string `json:"target,omitempty"` // 分发目标：main（仅运行）、last（上次聊天渠道）、inbox（收件箱）
	TimeoutSec int    `json:"timeoutSec,omitempty"`
	// ActiveHours 不能加 omitempty：零值 ""（全天）与默认值 "08:00-22:00" 语义不同，
	// 一旦省略键，Load 时会被 Default() 重新填回默认值，用户永远清不掉。
	ActiveHours string `json:"activeHours"` // 活跃时间段，如 "08:00-22:00"，空=全天
	LastRunAt   int64  `json:"lastRunAt,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	Runs        int    `json:"runs,omitempty"`
}

// Browser 浏览器工具（QwenPaw browser 工具的 Go 版）：让 Agent 能"打开网页、跑 JS、截图"。
//
// 两种接法任选其一：
//   - cdpUrl：指向一个已在跑的 Chrome/Edge 调试端口（如 http://192.168.1.10:9222）——零依赖，
//     浏览器可以跑在 NAS 之外的机器上（"电脑是我的手脚"）；
//   - chromePath：由白泽自己起一个（留空则在 PATH 里自动找 chromium / google-chrome / chrome）。
//
// 两者都没有时，browser 工具会明确报"没有可用浏览器"，绝不静默失败。
type Browser struct {
	Enabled    bool   `json:"enabled"`
	Headless   bool   `json:"headless"`             // 自己拉起浏览器时是否无头（默认 true）
	ChromePath string `json:"chromePath,omitempty"` // 浏览器可执行文件；留空 = 自动在 PATH 里找
	CDPURL     string `json:"cdpUrl,omitempty"`     // 外部 CDP 地址，如 http://192.168.1.10:9222
	TimeoutSec int    `json:"timeoutSec"`           // 单次操作超时秒（默认 30）
	MaxBytes   int    `json:"maxBytes"`             // 返回文本上限（默认 262144）
}

// ChannelConfig 一个频道（QwenPaw 频道机制的 Go 版）。支持的 kind：
//   - webhook：出站把回复 POST 到 outboundUrl（format 决定请求体形状，可直接对接飞书/钉钉/Slack 群机器人），
//     入站 POST /api/channels/{id}/inbound（用 token 校验）
//   - onebot：QQ OneBot V11 反向 WebSocket，实现端连到 /api/channels/{id}/ws（用 token 校验）
//   - feishu：飞书事件回调模式，平台 POST 到 /api/channels/{id}/event（token = 验证令牌），出站走飞书 OpenAPI
type ChannelConfig struct {
	ID          string   `json:"id"`   // 频道 id（唯一，出现在 /api/channels/{id}/... 路径里）
	Kind        string   `json:"kind"` // 频道类型：webhook / onebot / feishu
	Enabled     bool     `json:"enabled"`
	Token       string   `json:"token,omitempty"`       // webhook / onebot 的入站令牌；feishu 用作事件回调的「验证令牌」
	OutboundURL string   `json:"outboundUrl,omitempty"` // webhook：出站地址
	Format      string   `json:"format,omitempty"`      // webhook：出站体形状（generic/feishu/dingtalk/slack）
	AppID       string   `json:"appId,omitempty"`       // feishu / dingtalk / qq / xiaoyi：应用 App ID（xiaoyi 即 AK）
	AppSecret   string   `json:"appSecret,omitempty"`   // feishu / dingtalk / qq / xiaoyi：应用 App Secret（xiaoyi 即 SK）
	AgentID     string   `json:"agentId,omitempty"`     // xiaoyi：小艺开放平台的 Agent ID
	EncryptKey  string   `json:"encryptKey,omitempty"`  // feishu：可选，事件加密时用来解密
	Domain      string   `json:"domain,omitempty"`      // feishu：可选，默认 https://open.feishu.cn（自建/测试可改）
	ChatIDs     []string `json:"chatIds,omitempty"`     // feishu：轮询监听的会话（chat_id）；配了就启用「轮询入站」（免公网）
	PollSec     int      `json:"pollSec,omitempty"`     // feishu：轮询间隔秒（默认 5，最小 2）
	BotPrefix   string   `json:"botPrefix,omitempty"`   // 回复前缀
}

// Search 联网搜索通道（web_search 工具用）。
//
// 刻意不设"默认可用"：白泽是私有部署，跑在用户自己的机器上，没有一个通用且合法的
// 免密钥搜索接口能被无条件依赖。所以这里只认显式配置：
//
//	searxng    —— 自建/公共 SearXNG 实例的 JSON 接口（推荐，不依赖第三方账号）
//	duckduckgo —— 抓 DuckDuckGo 的 HTML 结果页（无需密钥，但可能被限流/被墙）
//
// provider 留空 = 没配，web_search 会明确告诉用户去哪儿配，而不是返回空结果或伪造结果。
type Search struct {
	Provider   string `json:"provider,omitempty"` // searxng | duckduckgo；空 = 没配
	BaseURL    string `json:"baseUrl,omitempty"`  // searxng 实例地址，如 http://192.168.1.10:8080
	APIKey     string `json:"apiKey,omitempty"`   // searxng 可选（实例开了鉴权时用）
	TimeoutSec int    `json:"timeoutSec,omitempty"`
	MaxResults int    `json:"maxResults,omitempty"`
}

// ExternalAgent 一个「外部 Agent」：白泽可以把一段独立的活委托给它执行，只把结论拿回来。
//
// 类型（Type）：
//
//	http  —— 任意 HTTP 端点：POST 一段 JSON（{"goal":"..."}），把回复当结论
//	         （OpenAI 兼容的 agent、或别的编排服务都能这么接）。
//	baize —— 另一个白泽实例：喂它的 POST /api/agent/run + 它的配对令牌，复用同一套协议。
//
// 安全口径（关键）：外部 Agent 一律当**不可信执行体**——
//   - 只传任务文本与最小上下文，**不外泄**本机文件 / 记忆 / 密钥；
//   - 回来的内容按**外部输入**处理（防提示注入），不当作可信指令；
//   - 地址不是本机时沿用 AllowRemote 开关（与模型通道同一条"本地优先"规矩）。
type ExternalAgent struct {
	ID         string `json:"id"`              // 唯一 id（出现在 agent_call 的 target 参数里）
	Name       string `json:"name"`            // 显示名
	Type       string `json:"type"`            // http | baize
	URL        string `json:"url"`             // 端点地址
	Token      string `json:"token,omitempty"` // 鉴权令牌（http 走 Authorization: Bearer；baize 走 X-Baize-Token）
	Note       string `json:"note,omitempty"`  // 说明（进系统提示，帮模型决定该派给谁）
	Enabled    bool   `json:"enabled"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// PluginSource 一个插件源（插件市场从它的 index.json 拉货架）。
// URL 可以是 http(s) 地址（静态托管即可，比如 GitHub raw / Pages），也可以是本机路径
// （离线安装 / 自建源 / 开发插件时用）。插件包本身也是静态文件，所以整套东西**零成本**。
type PluginSource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// Config 后端 Agent 配置
type Config struct {
	AllowRemote bool       `json:"allowRemote"` // 是否允许非本机模型地址 / 远端 MCP 服务
	Providers   []Provider `json:"providers"`
	Workdir     string     `json:"workdir"` // 空 = <data>/workspace
	SkillsDir   string     `json:"skillsDir"`
	AllowShell  bool       `json:"allowShell"`
	// ShellAllowCmds shell_run 的命令白名单（只比对"命令名"）。
	// 空 = 不限制（沿用旧行为，任何命令都能跑，仅靠人工审批兜底）；
	// 非空 = 只允许这些命令，且**拒绝管道/重定向/串联**——否则 `ls; rm -rf /` 这类
	// 写法能轻松绕过白名单，那种白名单还不如没有。
	ShellAllowCmds []string `json:"shellAllowCmds,omitempty"`
	// ApprovalAllow 永久放行的工具名（人工审批时选「永久」才会写进来）。
	// 只影响**危险工具的审批闸门**；「严」档（每次都批）不会被它绕过。
	ApprovalAllow      []string  `json:"approvalAllow,omitempty"`
	MaxSteps           int       `json:"maxSteps"`
	MaxRetries         int       `json:"maxRetries"`
	TokenBudget        int       `json:"tokenBudget"`
	ApprovalTimeoutSec int       `json:"approvalTimeoutSec"` // 危险操作等人工审批的超时（超时按拒绝处理）
	Cron               []CronJob `json:"cron"`
	// DeviceRemarks 设备备注（key = 设备 id）。这是用户的标注而不是设备上报的状态，
	// 所以放在配置里：设备重装、换网络都不会把备注弄丢。
	DeviceRemarks  map[string]string  `json:"deviceRemarks,omitempty"`
	AutoRunOnStart bool               `json:"autoRunOnStart"`          // 启动时是否立刻跑一次定时任务检查
	MCPServers     []mcp.ServerConfig `json:"mcpServers"`              // MCP 外部服务（热插拔）
	BackupKeep     int                `json:"backupKeep"`              // 自动备份保留份数，默认 10
	Embedding      Embedding          `json:"embedding"`               // 向量化通道（记忆语义检索）
	Memory         Memory             `json:"memory"`                  // 记忆术设置
	Voice          Voice              `json:"voice"`                   // 语音通道（说话 / 听写）
	Persona        Persona            `json:"persona"`                 // 人设文件（Markdown 进系统提示）
	Heartbeat      HeartbeatConfig    `json:"heartbeat"`               // 心跳任务（定期运行 agent）
	Channels       []ChannelConfig    `json:"channels"`                // 频道（IM / webhook 接入）
	Browser        Browser            `json:"browser"`                 // 浏览器工具（打开网页 / 跑 JS / 截图）
	Search         Search             `json:"search"`                  // 联网搜索通道（web_search 工具）
	PluginSources  []PluginSource     `json:"pluginSources,omitempty"` // 插件源（插件市场）
	// ExternalAgents 外部 Agent（委托执行）：白泽可以把一段独立的活派给它，只把结论拿回来。
	// 只做"清理/去重"，**不自动新建**——没配就是没配（agent_call 会明确报"还没有外部 Agent"）。
	ExternalAgents []ExternalAgent `json:"externalAgents,omitempty"`
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
		Channels:           []ChannelConfig{},
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
		Browser: Browser{
			Enabled:    true,
			Headless:   true,
			TimeoutSec: 30,
			MaxBytes:   256 * 1024,
		},
		Search: Search{TimeoutSec: 20, MaxResults: 8},
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
	// 浏览器工具：只做清理与兜底，不在这里强开（开关看 Enabled）
	c.Browser.ChromePath = strings.TrimSpace(c.Browser.ChromePath)
	c.Browser.CDPURL = strings.TrimRight(strings.TrimSpace(c.Browser.CDPURL), "/")
	if c.Browser.TimeoutSec <= 0 {
		c.Browser.TimeoutSec = 30
	}
	if c.Browser.MaxBytes <= 0 {
		c.Browser.MaxBytes = 256 * 1024
	}
	// 搜索通道：只做清理与兜底。provider 非法值**不静默改成别的**——
	// 静默改会让人以为配好了；留给 web_search 在调用时明确报"不支持的通道"。
	c.Search.Provider = strings.ToLower(strings.TrimSpace(c.Search.Provider))
	c.Search.BaseURL = strings.TrimRight(strings.TrimSpace(c.Search.BaseURL), "/")
	if c.Search.TimeoutSec <= 0 {
		c.Search.TimeoutSec = 20
	}
	if c.Search.MaxResults <= 0 {
		c.Search.MaxResults = 8
	}
	// shell 命令白名单与永久放行清单：去空、去重、小写归一。**不在这里补默认值**——
	// 空就是"不限制/没有"，语义清楚（补一个默认白名单会悄悄改变用户的行为）。
	c.ShellAllowCmds = normalizeNameList(c.ShellAllowCmds)
	c.ApprovalAllow = normalizeNameList(c.ApprovalAllow)
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
	// 频道：只做"清理"（丢非法/重复 id、收敛 kind/format），不自动新建，也不因为字段缺失就补一个。
	// 缺省时 Load 已给了空切片兜底。
	if c.Channels == nil {
		c.Channels = []ChannelConfig{}
	}
	cleanedCh := make([]ChannelConfig, 0, len(c.Channels))
	seenCh := map[string]bool{}
	for _, ch := range c.Channels {
		ch.ID = strings.TrimSpace(ch.ID)
		if ch.ID == "" || seenCh[ch.ID] {
			continue // 没 id / 重复 id 的直接丢，别让它们躺在配置里装样子
		}
		seenCh[ch.ID] = true
		ch.Kind = normalizeChannelKind(ch.Kind)
		ch.Format = normalizeChannelFormat(ch.Format)
		ch.OutboundURL = strings.TrimSpace(ch.OutboundURL)
		ch.Token = strings.TrimSpace(ch.Token)
		ch.Domain = strings.TrimSpace(ch.Domain)
		// 轮询会话：去空、去重
		ids := make([]string, 0, len(ch.ChatIDs))
		seenID := map[string]bool{}
		for _, id := range ch.ChatIDs {
			id = strings.TrimSpace(id)
			if id == "" || seenID[id] {
				continue
			}
			seenID[id] = true
			ids = append(ids, id)
		}
		ch.ChatIDs = ids
		if len(ids) > 0 && ch.PollSec < 2 {
			ch.PollSec = 5 // 有轮询会话就给个默认间隔（最小 2 秒，别把 API 打爆）
		}
		cleanedCh = append(cleanedCh, ch)
	}
	c.Channels = cleanedCh
	// 插件源：去空白、丢空 url、按 url 去重；名字留空就拿 url 顶上（市场里总得有个显示名）。
	// 与频道一样，**不自动新建**——没配就是没配，市场会明确说"还没有可用的插件源"。
	cleanedSrc := make([]PluginSource, 0, len(c.PluginSources))
	seenSrc := map[string]bool{}
	for _, s := range c.PluginSources {
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		if s.URL == "" || seenSrc[s.URL] {
			continue
		}
		seenSrc[s.URL] = true
		if s.Name == "" {
			s.Name = s.URL
		}
		cleanedSrc = append(cleanedSrc, s)
	}
	c.PluginSources = cleanedSrc
	// 外部 Agent：去空白、按 id 去重、类型/超时兜底；**不自动新建**。
	// 没配就是没配（agent_call 会明确报"还没有外部 Agent"，而不是静默失败）。
	cleanedEA := make([]ExternalAgent, 0, len(c.ExternalAgents))
	seenEA := map[string]bool{}
	for _, ea := range c.ExternalAgents {
		ea.ID = strings.TrimSpace(ea.ID)
		ea.Name = strings.TrimSpace(ea.Name)
		ea.Type = normalizeExternalAgentType(ea.Type)
		ea.URL = strings.TrimRight(strings.TrimSpace(ea.URL), "/")
		ea.Token = strings.TrimSpace(ea.Token)
		ea.Note = strings.TrimSpace(ea.Note)
		if ea.ID == "" || seenEA[ea.ID] {
			continue // 没 id / 重复 id 的直接丢，别让它们躺在配置里装样子
		}
		seenEA[ea.ID] = true
		if ea.Name == "" {
			ea.Name = ea.ID
		}
		if ea.TimeoutSec <= 0 {
			ea.TimeoutSec = 120
		}
		cleanedEA = append(cleanedEA, ea)
	}
	c.ExternalAgents = cleanedEA
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

// normalizeNameList 名称清单归一：去首尾空白、压小写、去重、丢空项（保持出现顺序）
func normalizeNameList(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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
// main = 只留在运行记录里；last = 最近收到过消息的频道；inbox = id 为 inbox 的频道。
// last / inbox 在频道没配好时只会记一条日志，绝不假装已经发出去。
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

var channelKinds = []string{"webhook", "onebot", "feishu", "dingtalk", "qq", "xiaoyi", "yuanbao", "wechat"}
var channelFormats = []string{"generic", "feishu", "dingtalk", "slack"}

// ChannelKinds 支持的频道类型（界面下拉用）：
//   - webhook：出站 POST 到一个 URL（飞书/钉钉/Slack 群机器人都只要一个 URL），入站走 /api/channels/{id}/inbound
//   - onebot：QQ OneBot V11 反向 WebSocket（实现端连进来）
//   - feishu：飞书事件回调 / 轮询入站，出站走 OpenAPI
//   - dingtalk：钉钉 Stream 长连接（白泽主动连出去，免公网），回复走消息里的 sessionWebhook
//   - qq：QQ 官方机器人（AccessToken + WebSocket 网关收事件 + OpenAPI 发消息）
//   - xiaoyi：华为小艺（A2A over WebSocket，AK/SK 签名 + Agent ID）
//   - yuanbao：腾讯元宝（protobuf over WebSocket + sign-token）
//   - wechat：个人微信（官方 iLink Bot HTTP：二维码登录 + 长轮询收信 + HTTP 发信）
func ChannelKinds() []string { return append([]string{}, channelKinds...) }

// ChannelFormats webhook 出站请求体的形状（对接不同 IM 群机器人用）
func ChannelFormats() []string { return append([]string{}, channelFormats...) }

// ValidChannelKind 类型是否合法（接口层挡笔误，别让 normalize 悄悄改成 webhook）
func ValidChannelKind(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range channelKinds {
		if v == k {
			return true
		}
	}
	return false
}

// ValidChannelFormat 出站格式是否合法
func ValidChannelFormat(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range channelFormats {
		if v == k {
			return true
		}
	}
	return false
}

func normalizeChannelKind(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range channelKinds {
		if v == k {
			return v
		}
	}
	return "webhook"
}

func normalizeChannelFormat(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, k := range channelFormats {
		if v == k {
			return v
		}
	}
	return "generic"
}

// 外部 Agent 支持的类型：http（任意 HTTP 端点）/ baize（另一个白泽实例）。
// 「本机 CLI agent」（claude / codex 之类）**暂不做**——白泽后端只跑在 NAS 上，
// 容器里没有这些 CLI；真要做得走桌面端 device_run 那条路，等前两种跑通再说。
var externalAgentTypes = []string{"http", "baize"}

// ExternalAgentTypes 支持的外部 Agent 类型（界面下拉用）
func ExternalAgentTypes() []string { return append([]string{}, externalAgentTypes...) }

// ValidExternalAgentType 类型是否合法（接口层挡笔误，别让 normalize 悄悄改成 http）
func ValidExternalAgentType(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, t := range externalAgentTypes {
		if v == t {
			return true
		}
	}
	return false
}

func normalizeExternalAgentType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, t := range externalAgentTypes {
		if v == t {
			return v
		}
	}
	return "http"
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
