//go:build windows

// 桌面端自动更新：从 NAS 拉 latest.json → 比版本 → 下载 → 校验 sha256 → 换掉自己 → 重启。
//
// 为什么把包放 NAS 而不是 GitHub Releases：NAS 本来就是这套系统的中枢，而且本机
// 没法用 gh 上传发布物；`tools/release-desktop.ps1` 一条命令就能把 exe 与清单传上去。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type updateManifest struct {
	Version    string `json:"version"`
	URL        string `json:"url"` // 建议写相对路径（/dl/baize-desktop.exe），跟着当前配置的后端地址走
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	Notes      string `json:"notes"`
	ReleasedAt string `json:"releasedAt"`
}

func init() { registerPlatformRoutes = registerUpdateRoutes }

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

func fetchManifest() (updateManifest, error) {
	var m updateManifest
	req, err := newBackendRequest(http.MethodGet, "/dl/latest.json")
	if err != nil {
		return m, err
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

// ApplyUpdate 下载 → 校验 → 换掉自己 → 起新进程 → 自己退出。
//
// ★为什么能覆盖「正在运行的自己」：Windows 不允许覆盖正在运行的 exe，但**允许改名**。
// 所以顺序是：把自己改名为 xxx.old → 把新版写到原路径 → 起新进程 → 退出。
// 另外必须在起新进程前**松开单实例锁**，否则新进程会被自己判成"已有实例"而退出。
func ApplyUpdate() (map[string]any, error) {
	m, err := fetchManifest()
	if err != nil {
		return nil, err
	}
	if !newerVersion(m.Version, desktopAppVersion) {
		return map[string]any{"ok": false, "error": "已经是最新版本（" + desktopAppVersion + "）"}, nil
	}

	tmp, err := downloadVerified(m)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		return nil, fmt.Errorf("挪开旧版本失败：%w", err)
	}
	if err := copyFile(tmp, self); err != nil {
		_ = os.Rename(old, self) // 换不成就回滚，别把自己搞没了
		return nil, fmt.Errorf("写入新版本失败（已回滚）：%w", err)
	}

	releaseSingleInstance() // 让即将起来的那个能拿到锁

	cmd := exec.Command(self)
	cmd.Dir = filepath.Dir(self)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("已替换但重启失败：%w（手动双击一次 exe 即可）", err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond) // 留点时间把 HTTP 响应写完
		quitApp()
	}()
	return map[string]any{"ok": true, "restarted": true, "version": m.Version}, nil
}

func downloadVerified(m updateManifest) (string, error) {
	u := strings.TrimSpace(m.URL)
	if u == "" {
		u = "/dl/baize-desktop.exe"
	}
	req, err := newBackendRequest(http.MethodGet, u)
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
}
