//go:build windows

// 桌面端自动更新：取清单 → 比版本 → 下载更新包（zip：exe + ui/ + float/）→ 校验 sha256
// → 换掉 exe 与界面 → 重启。多文件形态下，包内是 baize-desktop.exe、ui/ 与 float/。
//
// 清单来源：**GitHub Releases 优先**（公开镜像仓 baize-public，匿名可下，不需要令牌），
// 取不到再回退到自己的后端 /dl/latest.json。这样只要 GitHub 上发了新版，
// 客户端下一次检查就会自动跟上；而后端分发仍然保留（内网 / 没外网出口的机器可用）。
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type updateManifest struct {
	Version    string `json:"version"`
	URL        string `json:"url"` // 可以是相对路径（跟随后端地址），也可以是绝对 URL（GitHub Release 资产）
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	Notes      string `json:"notes"`
	ReleasedAt string `json:"releasedAt"`
}

// githubManifestURL 公开镜像仓的「最新 Release」清单地址。
// latest/download/ 是 GitHub 的固定跳转，永远指向最新一个 Release 的同名资产 ——
// 不需要 API、不需要令牌，客户端只管请求它。
const githubManifestURL = "https://github.com/HDKL0802/baize-public/releases/latest/download/latest.json"

// giteeLatestReleaseURL 国内镜像的「最新 Release」API（Gitee 允许匿名跨域读公开仓）。
const giteeLatestReleaseURL = "https://gitee.com/api/v5/repos/HDKL0037/baize-public/releases/latest"

// giteeRelease 只取清单需要的字段。
type giteeRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetchGiteeManifest 国内网络下 GitHub 常常连不上：从 Gitee 最新 Release 里
// 先取 latest.json（版本 / 说明 / sha256），再把下载地址换成 Gitee 自己托管的 zip。
func fetchGiteeManifest() (updateManifest, error) {
	var rel giteeRelease
	if err := getJSON(giteeLatestReleaseURL, false, 20*time.Second, &rel); err != nil {
		return updateManifest{}, err
	}
	version := strings.TrimPrefix(rel.TagName, "desktop-v")
	var m updateManifest
	var zipURL string
	for _, a := range rel.Assets {
		if a.Name == "latest.json" && a.URL != "" {
			var man updateManifest
			if err := getJSON(a.URL, false, 20*time.Second, &man); err == nil {
				m = man
			}
		}
		if version != "" && a.Name == "baize-desktop-"+version+".zip" {
			zipURL = a.URL
		}
	}
	if m.Version == "" {
		m.Version = version
	}
	if zipURL != "" {
		m.URL = zipURL
	}
	if m.Version == "" || m.URL == "" {
		return m, errors.New("Gitee 最新 Release 缺 latest.json / 更新包")
	}
	return m, nil
}

func init() {
	// 平台专属路由：自动更新 + 截图提问（截图那组见 shot_windows.go）+ 悬浮球迷你小窗 + 临时浮窗
	registerPlatformRoutes = func(mux *http.ServeMux) {
		registerUpdateRoutes(mux)
		registerShotRoutes(mux)
		registerBallRoutes(mux)
		registerFloatRoutes(mux)
		registerFileRoutes(mux) // 电脑控制第 1 档：本机文件浏览（受桌面控制权限闸门约束）
		// 全局快捷键清单（含"没抢到"的真实状态）：只有 Windows 有系统级快捷键，
		// 所以挂在平台专属这一组里，别让跨平台的 server.go 去引用 windows-only 的函数。
		mux.HandleFunc("/api/local/hotkeys", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hotkeys": ballHotkeyStatus()})
		})
	}
	// 标题栏跟主题（dwm_windows.go 的实现；改主题 / 定时轮询时各刷一次）
	titleBarRefresh = refreshTitleBar
}

func newBackendRequest(method, path string) (*http.Request, error) {
	server, token := getConfig()
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return nil, errors.New("还没配置后端地址（到「设置」里填）")
	}
	if !strings.HasPrefix(path, "http") {
		path = server + path
	}
	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Baize-Token", token)
	}
	return req, nil
}

// fetchManifest 取更新清单：GitHub → Gitee → 后端，依次回退。
func fetchManifest() (updateManifest, error) {
	var m updateManifest
	// GitHub 是公开仓：匿名请求，**不要**带本机令牌（令牌只应该发给自己的后端）。
	ghErr := getJSON(githubManifestURL, false, 20*time.Second, &m)
	if ghErr == nil && strings.TrimSpace(m.Version) != "" {
		return m, nil
	}
	if ghErr != nil {
		log.Printf("[更新] GitHub 清单取不到：%v（改用 Gitee）", ghErr)
	} else {
		log.Printf("[更新] GitHub 清单为空（改用 Gitee）")
	}

	var geErr error
	m, geErr = fetchGiteeManifest()
	if geErr == nil {
		return m, nil
	}
	log.Printf("[更新] Gitee 清单取不到：%v（改用后端）", geErr)

	m = updateManifest{}
	req, err := newBackendRequest(http.MethodGet, "/dl/latest.json")
	if err != nil {
		return m, fmt.Errorf("GitHub、Gitee 与后端都取不到更新清单（GitHub：%v；Gitee：%v）", ghErr, geErr)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return m, fmt.Errorf("取更新清单失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("取更新清单失败：HTTP %d（后端上还没发布过桌面端？）", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("更新清单解析失败：%w", err)
	}
	return m, nil
}

// getJSON 请求一个 URL 并解析 JSON。withToken=true 时走 newBackendRequest
// （补上本机配置的后端地址与令牌）；false 时按原样请求（用于 GitHub 这类公开地址）。
func getJSON(rawURL string, withToken bool, timeout time.Duration, out any) error {
	var req *http.Request
	var err error
	if withToken {
		req, err = newBackendRequest(http.MethodGet, rawURL)
	} else {
		req, err = http.NewRequest(http.MethodGet, rawURL, nil)
	}
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// CheckUpdate 问后端有没有新版本（不下载）
func CheckUpdate() (map[string]any, error) {
	m, err := fetchManifest()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"current": desktopAppVersion, "latest": m.Version,
		"hasUpdate": newerVersion(m.Version, desktopAppVersion),
		"notes":     m.Notes, "sizeBytes": m.SizeBytes, "releasedAt": m.ReleasedAt,
	}, nil
}

// ApplyUpdate 下载更新包（zip：含 baize-desktop.exe + ui/ + float/）→ 校验 → 换掉 exe 与界面 → 起新进程 → 自己退出。
//
// ★为什么能覆盖「正在运行的自己」：Windows 不允许覆盖正在运行的 exe，但**允许改名**。
// 顺序：先换 ui/（不是被占用的文件，可原地覆盖）→ 把自己改名为 xxx.old → 写新 exe → 起新进程 → 退出。
// 另外必须在起新进程前**松开单实例锁**，否则新进程会被自己判成"已有实例"而退出。
func ApplyUpdate() (map[string]any, error) {
	m, err := fetchManifest()
	if err != nil {
		return nil, err
	}
	if !newerVersion(m.Version, desktopAppVersion) {
		return map[string]any{"ok": false, "error": "已经是最新版本（" + desktopAppVersion + "）"}, nil
	}

	zipPath, err := downloadVerified(m)
	if err != nil {
		return nil, err
	}
	defer os.Remove(zipPath)

	// 解开到临时目录：应含 baize-desktop.exe 与 ui/
	pkgDir, err := extractZip(zipPath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(pkgDir)

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exeDir := filepath.Dir(self)

	// 1) 先换界面目录（多文件形态）
	if src := filepath.Join(pkgDir, "ui"); dirExists(src) {
		if err := replaceDir(src, filepath.Join(exeDir, "ui")); err != nil {
			return nil, fmt.Errorf("替换界面目录失败：%w", err)
		}
	}

	// 1b) Chrome 扩展目录（浏览器双方案）。有就覆盖（不先删目录：
	//     Chrome 可能正加载着它，删不掉就尽量逐文件覆盖；老包没有就跳过）。
	if src := filepath.Join(pkgDir, "browser-ext"); dirExists(src) {
		if err := copyTree(src, filepath.Join(exeDir, "browser-ext")); err != nil {
			return nil, fmt.Errorf("替换扩展目录失败：%w", err)
		}
	}

	// 1c) 浮窗界面（实时字幕小窗）。包内是 float/dist/，装到 exe 同级的 float/ 下。
	//     整目录替换：旧版的 hash 资源文件名会残留，不删会越滚越大。
	if src := filepath.Join(pkgDir, "float"); dirExists(src) {
		if err := replaceDir(src, filepath.Join(exeDir, "float")); err != nil {
			return nil, fmt.Errorf("替换浮窗界面失败：%w", err)
		}
	}

	// 2) 再换 exe
	newExe := filepath.Join(pkgDir, filepath.Base(self))
	if !fileExists(newExe) {
		newExe = filepath.Join(pkgDir, "baize-desktop.exe")
	}
	if !fileExists(newExe) {
		return nil, errors.New("更新包里没有可执行文件（baize-desktop.exe）")
	}
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		return nil, fmt.Errorf("挪开旧版本失败：%w", err)
	}
	if err := copyFile(newExe, self); err != nil {
		_ = os.Rename(old, self) // 换不成就回滚，别把自己搞没了
		return nil, fmt.Errorf("写入新版本失败（已回滚）：%w", err)
	}

	releaseSingleInstance() // 让即将起来的那个能拿到锁

	cmd := exec.Command(self)
	cmd.Dir = exeDir
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("已替换但重启失败：%w（手动双击一次 exe 即可）", err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond) // 留点时间把 HTTP 响应写完
		quitApp()
	}()
	return map[string]any{"ok": true, "restarted": true, "version": m.Version}, nil
}

// extractZip 把更新包解到一个新临时目录；只接受普通文件/目录，挡掉越界路径（zip slip）。
func extractZip(zipPath string) (string, error) {
	dst, err := os.MkdirTemp("", "baize-update-")
	if err != nil {
		return "", err
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		_ = os.RemoveAll(dst)
		return "", fmt.Errorf("打开更新包失败：%w", err)
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.Clean(f.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(dst, name)
		if target != dst && !strings.HasPrefix(target, dst+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
		in, err := f.Open()
		if err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			in.Close()
			_ = os.RemoveAll(dst)
			return "", err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			_ = os.RemoveAll(dst)
			return "", err
		}
	}
	return dst, nil
}

// replaceDir 用 src 覆盖 dst（先整目录删掉再拷）
func replaceDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyTree(src, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func downloadVerified(m updateManifest) (string, error) {
	u := strings.TrimSpace(m.URL)
	if u == "" {
		u = "/dl/baize-desktop.zip"
	}
	// 绝对 URL（GitHub Release 资产）直接请求，不要求先配置后端；
	// 相对路径才补上本机配置的后端地址和令牌。
	var req *http.Request
	var err error
	if strings.HasPrefix(u, "http") {
		req, err = http.NewRequest(http.MethodGet, u, nil)
	} else {
		req, err = newBackendRequest(http.MethodGet, u)
	}
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp("", "baize-desktop-*.exe")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("写临时文件失败：%w", err)
	}
	if closeErr != nil {
		os.Remove(f.Name())
		return "", closeErr
	}
	if m.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, m.SHA256) {
			os.Remove(f.Name())
			return "", errors.New("下载校验失败：sha256 与清单不符（包坏了或被改过，已丢弃）")
		}
	}
	return f.Name(), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// cleanupOldVersion 上次更新留下的 .old，能删就删（删不掉说明还被占着，下次再说）
func cleanupOldVersion() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(self + ".old")
}

func registerUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/local/update/check", func(w http.ResponseWriter, r *http.Request) {
		res, err := CheckUpdate()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		res["ok"] = true
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("/api/local/update/apply", func(w http.ResponseWriter, r *http.Request) {
		res, err := ApplyUpdate()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if res == nil {
			res = map[string]any{"ok": true}
		}
		writeJSON(w, http.StatusOK, res)
	})
	// 自动更新开关 + 检查间隔（分钟）
	mux.HandleFunc("/api/local/update/auto", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Enabled *bool `json:"enabled"`
				Minutes *int  `json:"minutes"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
			}
			setAutoUpdate(req.Enabled, req.Minutes)
		}
		en, mn := autoUpdateState()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": en, "minutes": mn})
	})
}

// startAutoUpdateLoop 后台定时检查更新；开着「自动更新」时，发现新版就自动下载并重启。
// 关着就只是空转（不打扰）。间隔取配置里的分钟数，最小 1 分钟。
func startAutoUpdateLoop(ctx context.Context) {
	go func() {
		for {
			en, mn := autoUpdateState()
			wait := time.Duration(mn) * time.Minute
			if wait < time.Minute {
				wait = time.Minute
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if !en {
				continue
			}
			m, err := fetchManifest()
			if err != nil {
				log.Printf("[自动更新] 检查失败：%v", err)
				continue
			}
			if !newerVersion(m.Version, desktopAppVersion) {
				continue
			}
			log.Printf("[自动更新] 发现新版本 %s（当前 %s），开始下载并重启", m.Version, desktopAppVersion)
			if _, err := ApplyUpdate(); err != nil {
				log.Printf("[自动更新] 升级失败：%v", err)
			}
			return // 成功会重启进程；失败也先停一轮，下轮再说
		}
	}()
}
