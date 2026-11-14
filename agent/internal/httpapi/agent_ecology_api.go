package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"baize/internal/config"
	"baize/internal/plugins/ecology"
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