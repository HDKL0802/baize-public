//go:build windows

// 桌面端自动更新：从 NAS 拉 latest.json → 比版本 → 下载更新包（zip：exe + ui/）→ 校验 sha256
// → 换掉 exe 与界面 → 重启。多文件形态下，包内是 baize-desktop.exe 与 ui/ 目录。
//
// 为什么把包放 NAS 而不是 GitHub Releases：NAS 本来就是这套系统的中枢，而且本机
// 没法用 gh 上传发布物；`tools/release-desktop.ps1` 一条命令就能把 zip 与清单传上去。
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// ApplyUpdate 下载更新包（zip：含 baize-desktop.exe + ui/）→ 校验 → 换掉 exe 与界面 → 起新进程 → 自己退出。
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
