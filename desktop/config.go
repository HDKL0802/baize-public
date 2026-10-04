package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 桌面端本地配置：后端地址 + 配对令牌，落在 %APPDATA%\白泽\desktop.json。
// 令牌只在原生侧使用（代理时加请求头），绝不下发给网页。
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

var (
	cfgMu   sync.RWMutex
	cfg     Config
	cfgPath string
)

func loadConfig() {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	dir = filepath.Join(dir, "白泽")
	_ = os.MkdirAll(dir, 0o755)
	cfgPath = filepath.Join(dir, "desktop.json")
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
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
