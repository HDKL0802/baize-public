package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 桌面端本地配置：后端地址 + 配对令牌 + 桌面控制权限，
// 落在 <数据目录>\desktop.json。令牌只在原生侧使用（代理时加请求头），绝不下发给网页。
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`

	// GuiPerm 桌面控制（被派活）的权限等级，见 perm.go：
	// 0=关闭（连读都不行）1=只读 2=只读指定目录 3=只读指定盘 4=完全访问
	// 默认 1（只读基础信息）：安全优先，想要更多得用户显式放开。
	GuiPerm int `json:"guiPerm"`
	// GuiScopes 权限范围：level 2 填目录（D:\文档），level 3 填盘（D:\）；level 0/1/4 忽略
	GuiScopes []string `json:"guiScopes,omitempty"`
	// DisclaimerAck 是否已确认风险免责声明（level>=3 以及首次运行都要求先确认）
	DisclaimerAck bool `json:"disclaimerAck"`
	// DataDir 数据目录（桌面端的配置与日志；留空 = 默认 %APPDATA%\白泽）
	DataDir string `json:"dataDir,omitempty"`

	// AutoUpdate 是否自动更新（后台定时检查，有新版就自动下载并重启）；默认关
	AutoUpdate bool `json:"autoUpdate,omitempty"`
	// AutoUpdateMin 自动更新的检查间隔（分钟）；<=0 时按默认 360
	AutoUpdateMin int `json:"autoUpdateMin,omitempty"`

	// BallHidden 是否隐藏悬浮球（默认 false = 显示）。关掉后能随时在设置里再打开。
	BallHidden bool `json:"ballHidden,omitempty"`

	// Theme 界面主题：light / dark / system（默认 system = 跟随系统）
	Theme string `json:"theme,omitempty"`

	// SubtitleLang 实时字幕的**译文**目标语言：zh / en（默认 zh）
	SubtitleLang string `json:"subtitleLang,omitempty"`
	// SubtitleHideSource 实时字幕是否**不显示**原文（默认 false = 原文+译文都显示）
	SubtitleHideSource bool `json:"subtitleHideSource,omitempty"`
	// SubtitleFont 实时字幕字号：s / m / l（默认 m）
	SubtitleFont string `json:"subtitleFont,omitempty"`
}

var (
	cfgMu   sync.RWMutex
	cfg     Config
	cfgPath string
)

// defaultDataDir 默认数据目录（%APPDATA%\白泽）
func defaultDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "白泽")
}

// dataDir 当前生效的数据目录（配置里改过就用改过的）
func dataDir() string {
	cfgMu.RLock()
	d := strings.TrimSpace(cfg.DataDir)
	cfgMu.RUnlock()
	if d == "" {
		return defaultDataDir()
	}
	return d
}

func loadConfig() {
	dir := defaultDataDir()
	_ = os.MkdirAll(dir, 0o755)
	cfgPath = filepath.Join(dir, "desktop.json")
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	// 首次运行（配置文件都没有）时权限停在"只读"，并要求先看免责声明
	if cfg.GuiPerm == 0 {
		cfg.GuiPerm = PermReadOnly
	}
	if cfg.AutoUpdateMin <= 0 {
		cfg.AutoUpdateMin = 360 // 默认 6 小时扫一次
	}
}

func saveConfigLocked() {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0o600)
}

func getConfig() (server, token string) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.Server, cfg.Token
}

// setConfig：server 允许清空（传空即清）；token 传空 = 保留原来那串（与其它端一个口径）。
func setConfig(server, token string) {
	cfgMu.Lock()
	cfg.Server = strings.TrimSpace(server)
	if t := strings.TrimSpace(token); t != "" {
		cfg.Token = t
	}
	saveConfigLocked()
	cfgMu.Unlock()
}

// localConfigSnapshot 给界面看的本机配置快照（**不含令牌原文**，只回"有没有"）
func localConfigSnapshot() map[string]any {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return map[string]any{
		"server": cfg.Server, "tokenSet": cfg.Token != "",
		"guiPerm": cfg.GuiPerm, "guiScopes": append([]string{}, cfg.GuiScopes...),
		"disclaimerAck": cfg.DisclaimerAck,
		"dataDir":       dataDir(), "dataDirDefault": defaultDataDir(),
		"configPath": cfgPath,
		"autoUpdate": cfg.AutoUpdate, "autoUpdateMin": autoUpdateMinLocked(),
		"ballVisible": !cfg.BallHidden,
		"theme":       themeNow(),
	}
}

// themeNow 当前主题偏好："light" / "dark" / "system"
func themeNow() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	switch cfg.Theme {
	case "light", "dark":
		return cfg.Theme
	default:
		return "system"
	}
}

// setTheme 改主题偏好（非法值一律退回 system）；返回落定后的值
func setTheme(t string) string {
	if t != "light" && t != "dark" && t != "system" {
		t = "system"
	}
	cfgMu.Lock()
	cfg.Theme = t
	saveConfigLocked()
	cfgMu.Unlock()
	return t
}

// subtitleNow 实时字幕的当前设置（缺省/非法值一律归一到默认：中文字幕、显示原文、中号字）
func subtitleNow() map[string]any {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	to := cfg.SubtitleLang
	if to != "en" {
		to = "zh"
	}
	font := cfg.SubtitleFont
	if font != "s" && font != "l" {
		font = "m"
	}
	return map[string]any{"to": to, "showSource": !cfg.SubtitleHideSource, "font": font}
}

// setSubtitle 改实时字幕设置；nil/空 = 该项不改。返回落定后的完整设置（界面直接拿去重画）。
func setSubtitle(to string, showSource *bool, font string) map[string]any {
	cfgMu.Lock()
	if to == "zh" || to == "en" {
		cfg.SubtitleLang = to
	}
	if showSource != nil {
		cfg.SubtitleHideSource = !*showSource
	}
	if font == "s" || font == "m" || font == "l" {
		cfg.SubtitleFont = font
	}
	saveConfigLocked()
	cfgMu.Unlock()
	return subtitleNow()
}

// ballVisible 悬浮球是否显示（默认显示）
func ballVisible() bool {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return !cfg.BallHidden
}

// setBallVisible 改悬浮球显示开关
func setBallVisible(v bool) {
	cfgMu.Lock()
	cfg.BallHidden = !v
	saveConfigLocked()
	cfgMu.Unlock()
}

// autoUpdateMinLocked 取检查间隔（调用方须已持锁）；<=0 按默认 360
func autoUpdateMinLocked() int {
	if cfg.AutoUpdateMin <= 0 {
		return 360
	}
	return cfg.AutoUpdateMin
}

// autoUpdateState 返回（是否自动更新, 间隔分钟）
func autoUpdateState() (bool, int) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.AutoUpdate, autoUpdateMinLocked()
}

// setAutoUpdate 改自动更新设置（指针为 nil 表示该项不改）
func setAutoUpdate(enabled *bool, minutes *int) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if enabled != nil {
		cfg.AutoUpdate = *enabled
	}
	if minutes != nil && *minutes > 0 {
		cfg.AutoUpdateMin = *minutes
	}
	saveConfigLocked()
}

// setGuiPerm 改桌面控制权限。返回错误时什么都没改。
//
// 免责闸门在**这一层**校验：允许选高权限（指定盘 / 完全访问）与"首次运行"都必须
// 先显式确认过风险，不能只靠前端弹窗——前端能绕过，原生侧不能。
func setGuiPerm(perm int, scopes []string, ack bool) error {
	if !validPerm(perm) {
		return errBadPerm(perm)
	}
	if needsDisclaimer(perm) && !ack {
		return errNeedDisclaimer
	}
	cleaned := cleanScopes(scopes)
	cfgMu.Lock()
	cfg.GuiPerm = perm
	cfg.GuiScopes = cleaned
	if ack {
		cfg.DisclaimerAck = true
	}
	saveConfigLocked()
	cfgMu.Unlock()
	// 权限变了要重连：caps（能力标记）与 hello 里的权限信息得重新上报给后端，
	// 否则后端还按旧能力派活，会出现"派了干不了"或"能干的没派"。
	deviceRestart(baseCtx, "桌面控制权限变更")
	return nil
}

// ackDisclaimer 只确认免责声明（首次运行引导用），不改权限
func ackDisclaimer() {
	cfgMu.Lock()
	cfg.DisclaimerAck = true
	saveConfigLocked()
	cfgMu.Unlock()
}

// setDataDir 改数据目录：把现有 desktop.json 搬到新目录，之后配置与日志都在那儿。
//
// 为什么要搬而不是"下次生效"：用户改完位置却发现配置还留在旧地方，
// 会让"我改了怎么没用"变成悬案。这里直接搬过去，语义清楚。
func setDataDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errBadDataDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return errBadDataDir
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return errBadDataDir
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	old := cfgPath
	cfg.DataDir = abs
	newPath := filepath.Join(abs, "desktop.json")
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(newPath, b, 0o600); err != nil {
		return errBadDataDir
	}
	if old != "" && old != newPath {
		_ = os.Remove(old) // 旧配置搬走了就删掉，免得两边不一致
	}
	cfgPath = newPath
	return nil
}
