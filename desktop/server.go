package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed ui
var uiFS embed.FS

// 平台钩子：由 device_windows.go 的 init() 覆盖成真实实现；非 Windows 就是空实现。
var (
	baseCtx        context.Context = context.Background()
	deviceRestart                  = func(ctx context.Context, trigger string) {}
	deviceSnapshot                 = func() map[string]any { return map[string]any{"enabled": false, "supported": false} }
)

// 代理到 NAS 后端的 HTTP 客户端：Agent 派活/附件可能跑一会儿，给足超时。
var beClient = &http.Client{Timeout: 180 * time.Second}

func buildHandler() http.Handler {
	mux := http.NewServeMux()

	// 内嵌界面
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// 本机配置：只回"有没有令牌"，绝不回显令牌原文
	mux.HandleFunc("/api/local/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s, t := getConfig()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": s, "tokenSet": t != ""})
		case http.MethodPost:
			var req struct {
				Server string `json:"server"`
				Token  string `json:"token"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
			setConfig(req.Server, req.Token)
			deviceRestart(baseCtx, "设置页改了连接") // 地址/令牌变了就重连
			s, t := getConfig()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": s, "tokenSet": t != ""})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 GET/POST"})
		}
	})

	// 本机设备连接状态（桌面端把自己注册成设备，能不能被派活看这里）
	mux.HandleFunc("/api/local/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": deviceSnapshot()})
	})

	// 透传后端：/api/be/api/state → {server}/api/state
	// 与手机内核的 /api/be 前缀同一口径，配对令牌只在这一层加。
	mux.Handle("/api/be/", http.StripPrefix("/api/be", http.HandlerFunc(proxyBackend)))

	return mux
}

func proxyBackend(w http.ResponseWriter, r *http.Request) {
	server, token := getConfig()
	if strings.TrimSpace(server) == "" {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "还没配置后端地址（去「设置」里填）"})
		return
	}
	url := strings.TrimRight(server, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))

	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "构造请求失败：" + err.Error()})
		return
	}
	if token != "" {
		req.Header.Set("X-Baize-Token", token)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := beClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "连不上后端：" + err.Error()})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
