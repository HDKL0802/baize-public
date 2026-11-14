package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"baize/internal/config"
	"baize/internal/plugins"
	"baize/internal/plugins/ecology"
)

const (
	// ecologyRegisterReadTimeout 登记前读索引的总超时。
	ecologyRegisterReadTimeout = 30 * time.Second
	// ecologyRegisterMaxBytes 登记前读索引的字节上限（32MB）。
	ecologyRegisterMaxBytes = 32 << 20
)

// registerEcology 注册「生态插件源」接口。
//
// 目的：把别的 Agent 生态的插件市场（当前是 DSH 的插件市场，格式 1）接进白泽。
//
// 诚实边界（与 ecology 包的约定一致）：
//   - GET  只拉目录：每条都带 kind 与 installable，装不了的如实写明原因；
//   - POST 转译成白泽插件包（zip + plugin.json + skills/SKILL.md）与索引 index.json，
//     产物落在数据目录下的 ecology/<源名>/，可整体当一个白泽插件源挂上去；
//   - 全程不执行第三方代码、不自动安装。
func (s *Server) registerEcology(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}

	// GET /api/agent/ecology/dsh?index=<地址或路径>&probe=0
	//
	// probe=1（默认）会逐条联网探测内容形态，慢但准；probe=0 只按索引元数据列目录，快。
	mux.HandleFunc("GET /api/agent/ecology/dsh", s.api(func(w http.ResponseWriter, r *http.Request) {
		index := strings.TrimSpace(r.URL.Query().Get("index"))
		if index == "" {
			writeErr(w, http.StatusBadRequest, "需要 index（生态市场索引的地址或本机路径）")
			return
		}
		var probe ecology.Probe
		if r.URL.Query().Get("probe") != "0" {
			probe = ecology.ProbeRepo
		}
		cat, err := ecology.Fetch(index, probe)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cat)
	}))

	// POST /api/agent/ecology/dsh/sync
	//
	// 请求体：{"index":"…", "name":"DSH 生态", "probe":1, "maxEntries":200}
	// 产物：<dataDir>/ecology/<name>/{index.json, packages/*.zip}
	mux.HandleFunc("POST /api/agent/ecology/dsh/sync", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Index      string `json:"index"`
			Name       string `json:"name"`
			Probe      *bool  `json:"probe"`
			MaxEntries int    `json:"maxEntries"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		index := strings.TrimSpace(req.Index)
		if index == "" {
			writeErr(w, http.StatusBadRequest, "需要 index（生态市场索引的地址或本机路径）")
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "dsh-eco"
		}
		var probe ecology.Probe
		if req.Probe == nil || *req.Probe {
			probe = ecology.ProbeRepo
		}
		cat, err := ecology.Fetch(index, probe)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if req.MaxEntries > 0 && len(cat.Entries) > req.MaxEntries {
			cat.Entries = cat.Entries[:req.MaxEntries]
		}
		outDir := filepath.Join(s.agent.DataDir(), "ecology", name)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := ecology.Build(cat, ecology.BuildOptions{
			OutDir:      outDir,
			SourceName:  name,
			SkillReader: ecology.GitHubSkillReader,
			MCPReader:   ecology.GitHubMCPReader,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"outDir":    outDir,
			"indexPath": res.IndexPath,
			"built":     res.Built,
			"skipped":   res.Skipped,
			"packages":  res.Packages,
			"errors":    res.Errors,
		})
	}))

	// POST /api/agent/ecology/dsh/register
	//
	// 请求体：{"name":"DSH 生态", "url":"<index.json 路径或地址>"}
	// 作用：把这条生态索引登记进白泽的插件源列表（config.pluginSources），
	// 之后白泽自带的插件市场就能像逛自己的源一样逛它。
	//
	// 登记前先真读一遍 url 校验：必须是白泽插件源索引（顶层 schema + plugins 数组，
	// 每条有 id/url）。DSH 生态原始索引（顶层 format、没有 schema）不是白泽源，
	// 直接登记会让市场报「索引格式版本 0 不认识」——这里明确挡住并指引先 sync。
	mux.HandleFunc("POST /api/agent/ecology/dsh/register", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		url := strings.TrimSpace(req.URL)
		if url == "" {
			writeErr(w, http.StatusBadRequest, "需要 url（生态索引的地址或本机路径）")
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "DSH 生态"
		}
		// 校验失败一律 400 + 中文原因，绝不静默登记（登记了却读不出来 = 伪装成功）。
		if err := validatePluginSourceIndex(url); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg := s.agent.Config()
		updated := false
		for i := range cfg.PluginSources {
			if cfg.PluginSources[i].Name == name {
				cfg.PluginSources[i].URL = url
				cfg.PluginSources[i].Enabled = true
				updated = true
				break
			}
		}
		if !updated {
			cfg.PluginSources = append(cfg.PluginSources, config.PluginSource{Name: name, URL: url, Enabled: true})
		}
		if err := s.agent.SaveConfig(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated, "name": name, "url": url, "sources": cfg.PluginSources})
	}))
}

// dshRawIndexHint DSH 生态原始索引的专属报错文案（用户要求原文照抄）。
const dshRawIndexHint = "这是 DSH 生态格式 1 索引，白泽插件市场只认白泽格式（schema + plugins 数组）；请先用 POST /api/agent/ecology/dsh/sync 转换成 index.json，再把生成的 index.json 登记进来"

// validatePluginSourceIndex 读一份索引并确认它是「白泽插件源索引」。
// 支持 http(s) 地址与本机绝对路径（离线开发用），带 32MB 上限与超时。
// 任何一步不合规都返回带原因的中文错误，调用方据此 400，不写 config。
func validatePluginSourceIndex(ref string) error {
	data, err := readPluginSourceIndex(ref)
	if err != nil {
		return err
	}
	var top struct {
		Schema  json.RawMessage `json:"schema"`
		Format  json.RawMessage `json:"format"`
		Plugins json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("索引不是合法的 JSON 对象（%s）：%w", ref, err)
	}
	has := func(raw json.RawMessage) bool {
		return len(raw) > 0 && string(raw) != "null"
	}

	// DSH 生态原始索引：顶层有 format、没有 schema。市场只认白泽格式，明确指引先 sync。
	if has(top.Format) && !has(top.Schema) {
		return fmt.Errorf("%s（原始 url：%s）", dshRawIndexHint, ref)
	}
	if !has(top.Schema) {
		return fmt.Errorf("索引缺少 schema 字段（白泽插件源索引必须有顶层 schema，0 或 %d 都接受）：%s", plugins.SchemaVersion, ref)
	}
	var schema int
	if err := json.Unmarshal(top.Schema, &schema); err != nil {
		return fmt.Errorf("索引 schema 不是整数（%s）：%w", ref, err)
	}
	if schema != 0 && schema != plugins.SchemaVersion {
		return fmt.Errorf("索引 schema 版本 %d 不认识（白泽只认 0 或 %d）：%s", schema, plugins.SchemaVersion, ref)
	}
	if !has(top.Plugins) {
		return fmt.Errorf("索引缺少 plugins 数组（%s）", ref)
	}
	var items []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(top.Plugins, &items); err != nil {
		return fmt.Errorf("索引 plugins 解析失败（必须是数组，且每条的 id、url 都要是字符串）（%s）：%w", ref, err)
	}
	for i, m := range items {
		if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.URL) == "" {
			return fmt.Errorf("索引 plugins 第 %d 条解析不出 id 或 url（白泽市场会静默丢掉它，不允许登记）：%s", i+1, ref)
		}
	}
	return nil
}

// readPluginSourceIndex 只读地取回索引字节：http(s) 走网络、本机绝对路径直接读。
func readPluginSourceIndex(ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		client := &http.Client{Timeout: ecologyRegisterReadTimeout}
		resp, err := client.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("读不到索引（%s）：%w", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("读不到索引（%s）：HTTP %d", ref, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, ecologyRegisterMaxBytes+1))
		if err != nil {
			return nil, fmt.Errorf("读索引失败（%s）：%w", ref, err)
		}
		if len(data) > ecologyRegisterMaxBytes {
			return nil, fmt.Errorf("索引超过 32MB 上限（%d 字节）：%s", ecologyRegisterMaxBytes, ref)
		}
		return data, nil
	}
	local := strings.TrimPrefix(ref, "file://")
	if !filepath.IsAbs(local) {
		return nil, fmt.Errorf("本机索引路径必须是绝对路径（相对路径会随进程工作目录漂移，白泽不猜）：%s", ref)
	}
	fi, err := os.Stat(local)
	if err != nil {
		return nil, fmt.Errorf("读不到索引（%s）：%w", ref, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("索引是个目录，不是文件：%s", ref)
	}
	if fi.Size() > ecologyRegisterMaxBytes {
		return nil, fmt.Errorf("索引超过 32MB 上限（%d 字节）：%s", ecologyRegisterMaxBytes, ref)
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, fmt.Errorf("读不到索引（%s）：%w", ref, err)
	}
	defer f.Close()
	// LimitReader 多读 1 字节用于复检，防止读取期间文件变大绕过上限。
	data, err := io.ReadAll(io.LimitReader(f, ecologyRegisterMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读索引失败（%s）：%w", ref, err)
	}
	if len(data) > ecologyRegisterMaxBytes {
		return nil, fmt.Errorf("索引超过 32MB 上限（%d 字节）：%s", ecologyRegisterMaxBytes, ref)
	}
	return data, nil
}
