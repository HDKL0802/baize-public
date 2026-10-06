package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"baize/internal/plugins"
)

// registerPlugins 注册「插件市场」接口。
//
// 市场里的东西全是静态文件（一个 index.json + 若干 .zip），所以后端这边只负责：
// 拉索引 → 下载包 → 校验 → 落盘（技能目录 + MCP 配置）。装/卸/启停都要真的动文件，
// 因此这些动作**不设"试运行"**：结果如实返回（装了什么、跳过了什么、有没有校验）。
func (s *Server) registerPlugins(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	// 逛市场：源 + 货架 + 已装。拉索引要走网络/磁盘，给个 30 秒上限（某个源挂了不会把整个请求拖死）
	mux.HandleFunc("GET /api/agent/plugins", s.api(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		cat := a.PluginCatalog(ctx)
		dir := ""
		if m := a.Plugins(); m != nil {
			dir = m.Dir()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"dir":       dir,
			"sources":   cat.Sources,
			"available": cat.Available,
			"installed": cat.Installed,
		})
	}))

	// action=install|update|uninstall|enable|disable|addSource|removeSource
	mux.HandleFunc("POST /api/agent/plugins", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action  string `json:"action"`
			ID      string `json:"id"`
			URL     string `json:"url"`
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "install"
		}
		switch action {
		case "install":
			// 下载 + 解包可能慢（尤其远端源），给足 5 分钟；但也必须有上限，
			// 否则一个卡住的下载会让界面永远转圈。
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			it, err := a.PluginInstall(ctx, plugins.InstallRequest{ID: req.ID, URL: req.URL})
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, s.pluginOK(map[string]any{"plugin": it}))
		case "update":
			// 同 id 覆盖升级：直接复用 Install —— 它发现同 id 已装时会先 teardown 旧版本、
			// 再装新包（见 plugins.Manager.Install），从当前配置的源里按 id 取最新版，
			// 所以这里不另写一套升级逻辑。
			if strings.TrimSpace(req.ID) == "" {
				writeErr(w, http.StatusBadRequest, "update 需要 id")
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			it, err := a.PluginInstall(ctx, plugins.InstallRequest{ID: req.ID})
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, s.pluginOK(map[string]any{"plugin": it}))
		case "uninstall":
			if strings.TrimSpace(req.ID) == "" {
				writeErr(w, http.StatusBadRequest, "uninstall 需要 id")
				return
			}
			it, err := a.PluginUninstall(req.ID)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, s.pluginOK(map[string]any{"plugin": it}))
		case "enable", "disable":
			if strings.TrimSpace(req.ID) == "" {
				writeErr(w, http.StatusBadRequest, action+" 需要 id")
				return
			}
			on := action == "enable"
			if req.Enabled != nil {
				on = *req.Enabled
			}
			if err := a.PluginSetEnabled(req.ID, on); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, s.pluginOK(nil))
		case "addsource":
			srcs, err := a.PluginAddSource(req.Name, req.URL, req.Enabled)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sources": srcs})
		case "removesource":
			srcs, err := a.PluginRemoveSource(req.URL)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sources": srcs})
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+
				"（可用 install | update | uninstall | enable | disable | addSource | removeSource）")
		}
	}))
}

// pluginOK 插件动作的通用响应：ok + 附带的字段 + 当前已安装清单（省界面一次往返）。
// 读记录失败时如实带一个 installedError，而不是假装"一个都没装"。
func (s *Server) pluginOK(extra map[string]any) map[string]any {
	out := map[string]any{"ok": true}
	for k, v := range extra {
		out[k] = v
	}
	list := []plugins.Installed{}
	if m := s.agent.Plugins(); m != nil {
		if l, err := m.List(); err != nil {
			out["installedError"] = err.Error()
		} else {
			list = l
		}
	}
	out["installed"] = list
	return out
}
