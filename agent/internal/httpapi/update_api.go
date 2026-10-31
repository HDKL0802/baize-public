package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 「更新后端」：客户端（桌面端 / 手机端）在设置页点一下，后端自己去发布源取新二进制，
// 落盘到 <dataDir>/bin/baize-backend.new，然后退出让容器重启；容器 entrypoint 启动时
// 把 .new 提升为正式二进制并优先执行它。这样不需要 docker socket、不需要 SSH。

const (
	// 发布仓（与官网 / 一键脚本同一组常量）
	updateGiteeOwner = "HDKL0037"
	updateGiteeRepo  = "baize-public"
	updateGitHubOwner = "HDKL0802"
	updateGitHubRepo  = "baize-public"

	updateAssetName = "baize-backend-linux-amd64"
)

// updateRelease 是「一次可用的发布信息」
type updateRelease struct {
	Mirror  string            `json:"mirror"`  // gitee | github
	Tag     string            `json:"tag"`     // desktop-v0.7.0
	Version string            `json:"version"` // 0.7.0
	Assets  map[string]string `json:"assets"`  // 资产名 → 下载地址
}

// registerUpdate 注册更新接口（只在挂了 Agent 服务时注册，和别的接口一个口径）
func (s *Server) registerUpdate(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	mux.HandleFunc("GET /api/agent/update", s.api(func(w http.ResponseWriter, r *http.Request) {
		rel, err := latestBackendRelease(r.URL.Query().Get("mirror"))
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"current":   s.version,
			"latest":    rel.Version,
			"tag":       rel.Tag,
			"mirror":    rel.Mirror,
			"hasUpdate": versionLess(s.version, rel.Version),
			"inDocker":  os.Getenv("BAIZE_IN_CONTAINER") == "1",
		})
	}))

	mux.HandleFunc("POST /api/agent/update", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mirror string `json:"mirror"` // auto | gitee | github
		}
		_ = decodeBody(r, &req)

		rel, err := latestBackendRelease(req.Mirror)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if !versionLess(s.version, rel.Version) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "upToDate": true, "current": s.version, "latest": rel.Version})
			return
		}
		url := rel.Assets[updateAssetName]
		if url == "" {
			writeErr(w, http.StatusNotFound, "该版本没有后端二进制（"+updateAssetName+"），暂时不能自动更新")
			return
		}
		sha := ""
		if u := rel.Assets[updateAssetName+".sha256"]; u != "" {
			if b, err := httpGet(u); err == nil {
				sha = strings.ToLower(strings.TrimSpace(strings.Fields(string(b))[0]))
			}
		}
		dataDir := s.agent.DataDir()
		dir := filepath.Join(dataDir, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeErr(w, http.StatusInternalServerError, "创建升级目录失败："+err.Error())
			return
		}
		tmp := filepath.Join(dir, updateAssetName+".tmp")
		got, err := downloadFile(url, tmp)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "下载失败："+err.Error())
			return
		}
		if sha != "" && sha != got {
			_ = os.Remove(tmp)
			writeErr(w, http.StatusBadRequest, "校验不通过（期望 "+sha+"，实际 "+got+"）")
			return
		}
		final := filepath.Join(dir, updateAssetName+".new")
		if err := os.Rename(tmp, final); err != nil {
			writeErr(w, http.StatusInternalServerError, "落盘失败："+err.Error())
			return
		}
		_ = os.Chmod(final, 0o755)
		s.lg.Info("后端更新已就绪，等待重启", "from", s.version, "to", rel.Version, "mirror", rel.Mirror, "path", final)

		inDocker := os.Getenv("BAIZE_IN_CONTAINER") == "1"
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"staged":   true,
			"from":     s.version,
			"to":       rel.Version,
			"mirror":   rel.Mirror,
			"inDocker": inDocker,
			"restart":  inDocker,
			"note":     map[bool]string{true: "2 秒后重启容器，完成后再刷新", false: "已下载到 " + final + "，重启后端后生效"}[inDocker],
		})
		if inDocker {
			// 容器 restart:unless-stopped 会把它拉起来；entrypoint 负责把 .new 变成正式二进制
			go func() {
				time.Sleep(2 * time.Second)
				s.lg.Info("退出以应用后端更新")
				os.Exit(3)
			}()
		}
	}))
}

// latestBackendRelease 取最新发布：默认先 Gitee 后 GitHub（国内 NAS 更快），
// 也可以显式指定；BAIZE_MIRROR 环境变量可覆盖默认顺序。
func latestBackendRelease(want string) (updateRelease, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || want == "auto" {
		want = strings.ToLower(strings.TrimSpace(os.Getenv("BAIZE_MIRROR")))
	}
	mirrors := []string{"gitee", "github"}
	switch want {
	case "gitee":
		mirrors = []string{"gitee"}
	case "github":
		mirrors = []string{"github"}
	}
	var lastErr error
	for _, m := range mirrors {
		rel, err := fetchRelease(m)
		if err != nil {
			lastErr = err
			continue
		}
		rel.Mirror = m
		return rel, nil
	}
	return updateRelease{}, fmt.Errorf("取不到最新版本（Gitee / GitHub 都失败）：%v", lastErr)
}

func fetchRelease(mirror string) (updateRelease, error) {
	var api string
	if mirror == "gitee" {
		api = "https://gitee.com/api/v5/repos/" + updateGiteeOwner + "/" + updateGiteeRepo + "/releases/latest"
	} else {
		api = "https://api.github.com/repos/" + updateGitHubOwner + "/" + updateGitHubRepo + "/releases/latest"
	}
	b, err := httpGet(api)
	if err != nil {
		return updateRelease{}, err
	}
	var raw struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
			Href string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return updateRelease{}, fmt.Errorf("%s 发布信息解析失败：%w", mirror, err)
	}
	rel := updateRelease{Tag: raw.TagName, Version: strings.TrimPrefix(raw.TagName, "desktop-v"), Assets: map[string]string{}}
	for _, a := range raw.Assets {
		u := a.Href
		if u == "" {
			u = a.URL
		}
		if a.Name != "" && u != "" {
			rel.Assets[a.Name] = u
		}
	}
	if rel.Version == "" {
		return updateRelease{}, fmt.Errorf("%s 没有可用的 Release", mirror)
	}
	return rel, nil
}

func httpGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "baize-backend")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("HTTP " + resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func downloadFile(url, dst string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "baize-backend")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("HTTP " + resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// versionLess 比较 a < b（够用的语义化比较：按点分段，非数字段按字符串比）
func versionLess(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	if a == "" || b == "" {
		return false
	}
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		return segmentLess(x, y)
	}
	return false
}

func segmentLess(x, y string) bool {
	xn, xok := atoiOK(x)
	yn, yok := atoiOK(y)
	switch {
	case xok && yok:
		return xn < yn
	case xok:
		return false // 数字段比非数字段新（1.0.1 > 1.0.0-beta）
	case yok:
		return true
	}
	return x < y
}

func atoiOK(s string) (int, bool) {
	digits := strings.TrimSpace(s)
	if digits == "" {
		return 0, false
	}
	n := 0
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
