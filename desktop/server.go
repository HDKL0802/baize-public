package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 平台钩子：由 device_windows.go 的 init() 覆盖成真实实现；非 Windows 就是空实现。
var (
	baseCtx        context.Context = context.Background()
	deviceRestart                  = func(ctx context.Context, trigger string) {}
	deviceSnapshot                 = func() map[string]any { return map[string]any{"enabled": false, "supported": false} }
	// 自启与「真正退出」也只有 Windows 桌面端有：关窗只是隐藏到后台，退出得显式来
	autostartSet = func(bool) error { return errors.New("只有 Windows 桌面端支持开机自启") }
	autostartGet = func() bool { return false }
	appQuit      = func() {}
	// 平台专属路由（Windows 的自动更新接口就挂在这儿）
	registerPlatformRoutes = func(mux *http.ServeMux) {}
)

// 代理到 NAS 后端的 HTTP 客户端：Agent 派活/附件可能跑一会儿，给足超时。
var beClient = &http.Client{Timeout: 180 * time.Second}

func buildHandler() http.Handler {
	mux := http.NewServeMux()

	// 界面：从磁盘上的 ui/ 目录读（多文件安装形态：exe + ui/ 目录）
	root, err := findUIDir()
	if err != nil {
		log.Fatalf("找不到界面文件：%v", err)
	}
	log.Printf("界面目录：%s", root)
	mux.Handle("/", http.FileServer(http.Dir(root)))

	// 本机配置：只回"有没有令牌"，绝不回显令牌原文
	mux.HandleFunc("/api/local/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": localConfigSnapshot()})
		case http.MethodPost:
			var req struct {
				Server string `json:"server"`
				Token  string `json:"token"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
			setConfig(req.Server, req.Token)
			deviceRestart(baseCtx, "设置页改了连接") // 地址/令牌变了就重连
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": localConfigSnapshot()})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 GET/POST"})
		}
	})

	// 桌面控制（权限分级）：这一层是**设备侧真正的闸门**，弹窗只是提醒
	mux.HandleFunc("/api/local/perm", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "perm": permNow(), "scopes": scopesNow(),
				"disclaimerAck": disclaimerAcked(),
				"levels":        permLevelViews(),
				"volumes":       listVolumes(),
			})
		case http.MethodPost:
			var req struct {
				Perm   *int     `json:"perm"`
				Scopes []string `json:"scopes"`
				// AckDisclaimer 用户是否已勾选"我已了解风险"。
				// 高权限档（指定盘 / 完全访问）**必须**为 true，否则拒绝保存。
				AckDisclaimer bool `json:"ackDisclaimer"`
			}
			if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "请求体解析失败：" + err.Error()})
				return
			}
			if req.Perm == nil {
				// 只确认免责声明（首次运行引导）
				ackDisclaimer()
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": localConfigSnapshot()})
				return
			}
			if err := setGuiPerm(*req.Perm, req.Scopes, req.AckDisclaimer); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			log.Printf("[桌面控制] 权限已改为 %s（范围 %v）", PermTitle(*req.Perm), cleanScopes(req.Scopes))
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": localConfigSnapshot()})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 GET/POST"})
		}
	})

	// 数据目录：桌面端的配置与日志放哪儿（默认 %APPDATA%\白泽，可改到 D 盘等）
	mux.HandleFunc("/api/local/datadir", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "dataDir": dataDir(), "default": defaultDataDir(), "configPath": cfgPath,
			})
		case http.MethodPost:
			var req struct {
				Dir string `json:"dir"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
			if strings.TrimSpace(req.Dir) == "" {
				// 传空 = 恢复默认
				cfgMu.Lock()
				cfg.DataDir = ""
				saveConfigLocked()
				cfgMu.Unlock()
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dataDir": dataDir(), "default": defaultDataDir()})
				return
			}
			if err := setDataDir(req.Dir); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dataDir": dataDir(), "configPath": cfgPath})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 GET/POST"})
		}
	})

	// 本机设备连接状态（桌面端把自己注册成设备，能不能被派活看这里）
	mux.HandleFunc("/api/local/device", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": deviceSnapshot()})
	})

	// 界面主题：亮 / 暗 / 跟随系统（存档在本地配置，浮窗与手机端同源）
	mux.HandleFunc("/api/local/theme", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Theme string `json:"theme"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
			setTheme(req.Theme)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "theme": themeNow()})
	})

	// 开机自启（写 HKCU 的 Run 键；不需要管理员）
	mux.HandleFunc("/api/local/autostart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
			}
			if err := autostartSet(req.Enabled); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": autostartGet()})
	})

	// 真正退出。关窗只是隐藏到后台（保住设备连接），所以要退出得显式调这个。
	mux.HandleFunc("/api/local/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		appQuit()
	})

	// 从本机文件夹导入 Markdown 笔记（Obsidian 库）
	mux.HandleFunc("/api/local/notes/import", handleLocalNotesImport)

	// 平台专属路由（Windows：自动更新）
	registerPlatformRoutes(mux)

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
	// 登录会话（多用户）：界面把会话令牌放在这个头上，由本层透传给后端。
	// 后端按它决定"以谁的身份访问数据"（记忆库/知识库按用户分区）。
	// ⚠️ 配对令牌始终由本层自己加（页面接触不到）；会话令牌相反，是页面给的、我们不持存。
	if sess := strings.TrimSpace(r.Header.Get("X-Baize-Session")); sess != "" {
		req.Header.Set("X-Baize-Session", sess)
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

/* ---------- 从本机文件夹导入笔记（Obsidian 库） ---------- */

// 导入上限：挡住"手滑选了整个 C 盘"这类误操作，超了如实回报，不静默截断。
const (
	notesImportMaxFiles = 2000
	notesImportMaxFile  = 2 << 20  // 单个 .md 最多 2MB
	notesImportMaxTotal = 32 << 20 // 一次导入总量上限
)

// 扫目录时要跳过的目录名（Obsidian 自身的配置、版本库、依赖等，都不是笔记正文）
var notesSkipDirs = map[string]bool{
	".obsidian": true, ".git": true, ".trash": true, "node_modules": true,
	".idea": true, ".vscode": true, "__pycache__": true,
}

type localNoteFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// handleLocalNotesImport 扫描本机文件夹里的 .md，转发给后端导入。
//
// 为什么必须在桌面端做这一步：后端跑在 NAS 上，读不到用户 PC 的磁盘；
// 桌面端在用户机器上，才扫得到 Obsidian 库。扫完把正文一次性转给后端，
// 令牌只在这一层加，网页不接触。
func handleLocalNotesImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 POST"})
		return
	}
	var req struct {
		Dir       string `json:"dir"`
		Namespace string `json:"namespace"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "请填写要导入的文件夹路径"})
		return
	}
	fi, err := os.Stat(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "文件夹不存在或读不到：" + err.Error()})
		return
	}
	if !fi.IsDir() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "给的不是文件夹：" + dir})
		return
	}
	files, skipped, total, err := scanMarkdown(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "扫描失败：" + err.Error()})
		return
	}
	if len(files) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "这个文件夹里没有找到 .md 笔记"})
		return
	}

	server, token := getConfig()
	if strings.TrimSpace(server) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "还没配置后端地址（去「设置」里填）"})
		return
	}
	payload, _ := json.Marshal(map[string]any{"namespace": req.Namespace, "files": files})
	url := strings.TrimRight(server, "/") + "/api/agent/notes/import"
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "构造请求失败：" + err.Error()})
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	if token != "" {
		hreq.Header.Set("X-Baize-Token", token)
	}
	resp, err := beClient.Do(hreq)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "连不上后端：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"error": "后端导入失败（HTTP " + strconv.Itoa(resp.StatusCode) + "）：" + string(body)})
		return
	}
	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		res = map[string]any{}
	}
	res["ok"] = true
	res["scanned"] = len(files)
	res["skippedNames"] = skipped
	res["bytes"] = total
	writeJSON(w, http.StatusOK, res)
}

// scanMarkdown 递归扫 .md，返回（文件列表, 跳过的文件数, 总字节, 错误）。
// 顺序稳定（按路径排序），同名不同目录靠相对路径区分。
func scanMarkdown(root string) ([]localNoteFile, int, int64, error) {
	var out []localNoteFile
	skipped := 0
	var total int64
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, 0, err
	}
	err = filepath.WalkDir(rootAbs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			skipped++
			return nil // 单个条目读不了就跳过，不中断整趟导入
		}
		if d.IsDir() {
			name := d.Name()
			if p != rootAbs && (notesSkipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		if len(out) >= notesImportMaxFiles {
			skipped++
			return nil
		}
		rel, err := filepath.Rel(rootAbs, p)
		if err != nil {
			rel = d.Name()
		}
		rel = filepath.ToSlash(rel)
		fi, err := d.Info()
		if err != nil || fi.Size() > notesImportMaxFile {
			skipped++
			return nil
		}
		if total+fi.Size() > notesImportMaxTotal {
			skipped++
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			skipped++
			return nil
		}
		total += int64(len(b))
		out = append(out, localNoteFile{Path: rel, Content: string(b)})
		return nil
	})
	if err != nil {
		return nil, skipped, total, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, skipped, total, nil
}

// findUIDir 定位界面目录。安装后是 exe 同级的 ui/；开发时 exe 在 desktop/bin/、
// 界面在 desktop/ui/，所以也看上一级的 ui/，最后回退到当前工作目录。
func findUIDir() (string, error) {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(dir, "ui"),
			filepath.Join(dir, "..", "ui"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, filepath.Join(wd, "ui"))
	}
	for _, c := range cands {
		if fi, err := os.Stat(filepath.Join(c, "index.html")); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs, nil
		}
	}
	return "", errors.New("没找到 ui/index.html（安装后 exe 同级应有 ui 目录；开发时应在 desktop/ui）")
}
