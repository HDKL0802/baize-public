package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"baize/internal/agentsvc"
	"baize/internal/backup"
	"baize/internal/config"
	"baize/internal/llm"
	"baize/internal/mcp"
	"baize/internal/memory"
)

// SetAgent 挂上 Agent 服务（必须在 Handler() 之前调用）
func (s *Server) SetAgent(a *agentsvc.Service) { s.agent = a }

// registerAgent 注册 Agent 相关接口（没挂服务时不注册，控制台也不会显示对应卡片）
func (s *Server) registerAgent(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	// 魔法命令注册表：命令处理器要同时用到版本号与 Agent 能力，所以在这里装配
	s.cmds = s.buildCommands()
	s.registerCommands(mux)
	// 频道（IM / webhook 接入）
	s.registerChannels(mux)
	// 记忆星图（笔记 + 双向链接 + 图谱）
	s.registerNotes(mux)
	// 多用户底座（登录 / 账号 / 用户组）
	s.registerAccounts(mux)
	// 用户组共享文档（组内成员互相可见）
	s.registerSharing(mux)
	s.registerCalls(mux)
	// 冲突协商（同名两版的对照/聊天/定稿 + 音视频信令中转）
	s.registerConflicts(mux)
	// 插件市场（静态源 + 插件包 → 技能目录 / MCP 配置）
	s.registerPlugins(mux)
	// 可观测性（运行汇总 + 工具/通道计数 + 日志概览 + 健康检查）
	s.registerObservability(mux)
	// 外部 Agent（委托执行：把一段独立的活派给另一个 Agent）
	s.registerExternalAgents(mux)
	mux.HandleFunc("GET /api/agent/state", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.State())
	}))

	// 智能体活动追踪（始终开启，AlwaysOn）：控制台/手机端轮询它显示「现在在干什么」
	mux.HandleFunc("GET /api/agent/activity", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.Activity())
	}))

	mux.HandleFunc("POST /api/agent/run", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Goal        string `json:"goal"`
			Recipe      string `json:"recipe"`
			AutoApprove bool   `json:"autoApprove"` // 旧字段（松档的布尔写法），还认，别把老客户端弄坏
			// Strictness 审批松紧度：loose（松，免审批）/ mid（中，默认）/ strict（严，全部工具都要人批）。
			// 与 autoApprove 同时给时 strictness 优先。
			Strictness string `json:"strictness"`
			Wait       bool   `json:"wait"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Goal == "" {
			writeErr(w, http.StatusBadRequest, "goal 不能为空")
			return
		}
		// 魔法命令：以 "/" 开头且命中命令的，直接回执（不派给模型、不占运行记录）。
		// 技能注入类命令会改写 goal，然后照常往下跑。
		if res, handled := s.dispatchCommand(r.Context(), req.Goal); handled {
			if res.Reply != "" {
				out := map[string]any{"command": true, "reply": res.Reply}
				if res.Name != "" {
					out["name"] = res.Name
				}
				if res.Action != "" {
					out["action"] = res.Action
				}
				writeJSON(w, http.StatusOK, out)
				return
			}
			if res.Goal != "" {
				req.Goal = res.Goal
			}
		}
		mode := req.Strictness
		if mode == "" && req.AutoApprove {
			mode = "loose"
		}
		if req.Wait {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			res, err := a.Run(ctx, req.Goal, req.Recipe, mode)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"result": res, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"result": res})
			return
		}
		// 异步：立刻返回运行 id，控制台拿它跟踪这次运行（危险操作会挂在审批队列里等人批）
		id, err := a.Start(req.Goal, req.Recipe, mode)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "runId": id, "goal": req.Goal})
	}))

	mux.HandleFunc("GET /api/agent/runs", s.api(func(w http.ResponseWriter, r *http.Request) {
		runs, err := a.Runs(atoiDefault(r.URL.Query().Get("limit"), 20))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	}))

	mux.HandleFunc("GET /api/agent/runs/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		detail, err := a.RunDetail(r.PathValue("id"))
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}))

	// 记忆检索。参数：
	//   q           查询词（空 = 要"最近入库的"，不卡任何阈值）
	//   limit       条数（默认 8）
	//   minScore    最低语义相似度；不传 = 用配置里的 memory.recallMinScore，传 0 = 关掉这道闸门
	//   minFloor    混合分下限（一般不用，留给调试）
	//   profile     权重档位 balanced|semantic|lexical|graph_first
	//   diverse     是否 MMR 去重（默认 1）；库里同一次任务往往落好几条几乎一样的记录
	//   namespace / category / days  过滤
	mux.HandleFunc("GET /api/agent/memory", s.api(func(w http.ResponseWriter, r *http.Request) {
		// 记忆库按登录身份路由：登录了就看他自己的分区，没登录就是默认主体
		store, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		urlq := r.URL.Query()
		q := urlq.Get("q")
		limit := atoiDefault(urlq.Get("limit"), 8)

		// 搜索框要的是"语义相关"，不是"最近 N 条"：配了向量通道就按配置里的
		// 最低语义相似度卡一道（与派活时的自动召回同一个口径，两处行为一致）。
		minVector := a.Config().Memory.RecallMinScore
		if raw := strings.TrimSpace(urlq.Get("minScore")); raw != "" {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "minScore 不是数字："+raw)
				return
			}
			minVector = f
		}
		if strings.TrimSpace(q) == "" {
			minVector = 0 // 没给查询词 = 只是想看最近入库的
		}
		minFloor := 0.0
		if raw := strings.TrimSpace(urlq.Get("minFloor")); raw != "" {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "minFloor 不是数字："+raw)
				return
			}
			minFloor = f
		}
		res, err := store.HybridSearch(r.Context(), memory.HybridQuery{
			Text:           q,
			Limit:          limit,
			Namespace:      urlq.Get("namespace"),
			Category:       urlq.Get("category"),
			Profile:        urlq.Get("profile"),
			MinVector:      minVector,
			MinScore:       minFloor,
			TimeWindowDays: atoiDefault(urlq.Get("days"), 0),
			Diverse:        urlq.Get("diverse") != "0",
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query": q, "hits": res.Hits,
			"profile": res.Profile, "weights": res.Weights,
			"vectorUsed": res.VectorUsed, "vectorNote": res.VectorNote,
			"candidates": res.Candidates, "filtered": res.Filtered,
			"minVector": minVector,
		})
	}))

	mux.HandleFunc("GET /api/agent/memory/tree", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		nodes, err := mem.Tree(atoiDefault(r.URL.Query().Get("limit"), 40))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 顺手告诉界面"有几天摘要还没跟上"：不然节点列表里空空如也，说不清是没记忆还是没摘要
		stale, err := mem.StaleDays(8)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": nodes, "staleDays": stale, "staleCount": len(stale),
		})
	}))

	// 手动重建记忆树摘要（后台每小时也会自己补）。要调模型，所以只在过期的天上花调用。
	mux.HandleFunc("POST /api/agent/memory/tree/rebuild", s.api(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		n, err := a.RebuildMemoryTreeFor(ctx, s.principal(r))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "days": n})
	}))

	// 整理重复记忆：同类回执 / 同一目标的重复结论，每组只留最新的一条。
	// 这是删除操作，所以只提供手动接口，绝不自动跑。
	mux.HandleFunc("POST /api/agent/memory/compact", s.api(func(w http.ResponseWriter, r *http.Request) {
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := mem.CompactDuplicates()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "groups": res.Groups, "merged": res.Merged, "kept": res.Kept,
		})
	}))

	mux.HandleFunc("POST /api/agent/memory/write", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content    string  `json:"content"`
			Title      string  `json:"title"`
			Kind       string  `json:"kind"`
			Importance float64 `json:"importance"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		mem, err := s.memOf(r)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		res, err := mem.Ingest(req.Content, memory.IngestOptions{
			Source: "user", Kind: req.Kind, Title: req.Title, Importance: req.Importance,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	}))

	mux.HandleFunc("GET /api/agent/checkpoints", s.api(func(w http.ResponseWriter, r *http.Request) {
		list, err := a.Checkpoints()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checkpoints": list})
	}))

	mux.HandleFunc("POST /api/agent/checkpoints/{id}/rollback", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := a.Rollback(id); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.lg.Warn("已回滚工作目录到快照", "id", id)
		writeJSON(w, http.StatusOK, map[string]any{"rolledBack": id})
	}))

	mux.HandleFunc("GET /api/agent/approvals", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"approvals": a.Approvals()})
	}))

	// 放行清单：agent 侧工具审批里选过「记住」的工具（永久 = 配置里，会话 = 本进程）
	mux.HandleFunc("GET /api/agent/approvals/allow", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.ApprovalAllow())
	}))

	// 撤销放行：action=clear（清空）| remove（只撤一个）；scope=always|session|all；tool 可选
	mux.HandleFunc("POST /api/agent/approvals/allow", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
			Scope  string `json:"scope"`
			Tool   string `json:"tool"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "clear"
		}
		if action != "clear" && action != "remove" {
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 clear | remove）")
			return
		}
		tool := strings.TrimSpace(req.Tool)
		if action == "remove" && tool == "" {
			writeErr(w, http.StatusBadRequest, "remove 需要 tool")
			return
		}
		if action == "clear" {
			tool = "" // clear = 清空该 scope 下的全部
		}
		if err := a.ClearApprovalAllow(req.Scope, tool); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, a.ApprovalAllow())
	}))

	mux.HandleFunc("POST /api/agent/approvals/{id}/approve", s.api(func(w http.ResponseWriter, r *http.Request) {
		// remember 可选：session = 本进程内不再问该工具；always = 永久放行（写进配置）；
		// 留空 = 只批这一次（默认，最保守）
		var body struct {
			By       string `json:"by"`
			Remember string `json:"remember"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		by := body.By
		if by == "" {
			by = operatorFrom(r)
		}
		if err := a.ApproveWith(r.PathValue("id"), by, body.Remember); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"approved": r.PathValue("id"), "by": by, "remember": body.Remember,
		})
	}))

	mux.HandleFunc("POST /api/agent/approvals/{id}/reject", s.api(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			By     string `json:"by"`
			Reason string `json:"reason"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		by := body.By
		if by == "" {
			by = operatorFrom(r)
		}
		if err := a.Reject(r.PathValue("id"), by, body.Reason); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rejected": r.PathValue("id"), "by": by})
	}))

	mux.HandleFunc("GET /api/agent/providers", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		writeJSON(w, http.StatusOK, map[string]any{
			"providers":   a.Providers(),
			"allowRemote": cfg.AllowRemote,
			"presets":     llm.Presets(), // 预置模板（界面「从模板新建」用）
		})
	}))

	// 模型通道的加/改/删/探活：action=upsert|remove|test
	mux.HandleFunc("POST /api/agent/providers", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string           `json:"action"`
			Name        string           `json:"name"`
			Config      *config.Provider `json:"config"`
			AllowRemote bool             `json:"allowRemote"`
			Protocol    string           `json:"protocol"`
			BaseURL     string           `json:"baseUrl"`
			APIKey      string           `json:"apiKey"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "upsert"
		}
		switch action {
		case "upsert":
			if req.Config == nil {
				writeErr(w, http.StatusBadRequest, "upsert 需要 config")
				return
			}
			list, err := a.ProviderSave(*req.Config, req.AllowRemote)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"providers": list, "saved": req.Config.Name})
		case "remove":
			list, err := a.ProviderRemove(req.Name)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"providers": list, "removed": req.Name})
		case "test":
			name := strings.TrimSpace(req.Name)
			if name == "" && req.Config != nil {
				name = req.Config.Name
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			res, err := a.ProviderTest(ctx, name)
			out := map[string]any{"result": res}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, out)
		case "discover":
			// 自动发现模型名（只读）：读 OpenAI 兼容 /models，Ollama 另读 /api/tags
			proto, base, key := req.Protocol, req.BaseURL, req.APIKey
			if req.Config != nil {
				if proto == "" {
					proto = req.Config.Protocol
				}
				if base == "" {
					base = req.Config.BaseURL
				}
				if key == "" {
					key = req.Config.APIKey
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res, err := a.ProviderDiscover(ctx, proto, base, key, req.AllowRemote)
			out := map[string]any{"models": res.Models, "source": res.Source}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, out)
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 upsert | remove | test | discover）")
		}
	}))

	// 回显单条通道的某个字段（apiKey | baseUrl），供控制台/桌面端「复制」用。
	//
	// ⚠️ 这是全项目唯一回显明文 Key 的接口：控制台本身已是令牌闸门之后，
	// 用户要复制 Key 去别处粘贴。取值走 agentsvc.ProviderSecret（只读），
	// 明文不进 ProviderInfo / 列表响应。
	mux.HandleFunc("POST /api/agent/providers/secret", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name  string `json:"name"`
			Field string `json:"field"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		field := strings.TrimSpace(req.Field)
		if field != "apiKey" && field != "baseUrl" {
			writeErr(w, http.StatusBadRequest, "field 只支持 apiKey | baseUrl，收到："+field)
			return
		}
		value, err := a.ProviderSecret(req.Name, field)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		s.lg.Warn("接口：回显通道密钥", "name", req.Name, "field", field, "addr", r.RemoteAddr)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "field": field, "value": value})
	}))

	// 通道充值记账：GET 读全部；POST action=add|remove。
	// 只记「金额 / 时间 / 备注」，**不做汇率与单价换算** —— 换算口径多变，交给界面/使用者自己算。
	mux.HandleFunc("GET /api/agent/providers/recharge", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"recharges": a.ProviderRecharges()})
	}))

	mux.HandleFunc("POST /api/agent/providers/recharge", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string  `json:"action"`
			Name   string  `json:"name"`
			Amount float64 `json:"amount"`
			Note   string  `json:"note"`
			ID     string  `json:"id"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "add"
		}
		switch action {
		case "add":
			entry, err := a.AddProviderRecharge(req.Name, req.Amount, req.Note)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entry": entry})
		case "remove":
			if err := a.RemoveProviderRecharge(req.Name, req.ID); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": req.ID})
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 add | remove）")
		}
	}))

	// 截图提问：把一张截图交给多模态模型做「提取文字 / 翻译」
	mux.HandleFunc("POST /api/agent/vision", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string `json:"action"` // extract | translate
			ImageBase64 string `json:"imageBase64"`
			Lang        string `json:"lang"` // translate 用：zh | en
		}
		// 截图 base64 可能好几 MB：这个口子把 body 上限放大（默认 1MB 装不下）
		if err := decodeBodyMax(r, &req, 32<<20); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		res, err := a.Vision(ctx, req.Action, req.ImageBase64, req.Lang)
		out := map[string]any{
			"action": res.Action, "text": res.Text,
			"model": res.Model, "latencyMs": res.LatencyMs,
		}
		if err != nil {
			out["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("GET /api/agent/mcp", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"servers": a.MCP(), "hint": a.MCPHints()})
	}))

	// 一条接口搞定 MCP 的加/改/删/重连/手动调用：
	// action=upsert|remove|reload|call；upsert 用 config，remove/reload 用 server，call 用 server+tool+args
	mux.HandleFunc("POST /api/agent/mcp", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string            `json:"action"`
			Server string            `json:"server"`
			Config *mcp.ServerConfig `json:"config"`
			Tool   string            `json:"tool"`
			Args   map[string]any    `json:"args"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "upsert"
		}
		switch action {
		case "upsert":
			if req.Config == nil {
				writeErr(w, http.StatusBadRequest, "upsert 需要 config")
				return
			}
			servers, err := a.MCPUpsert(*req.Config)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"servers": servers, "saved": req.Config.Name})
		case "remove":
			servers, err := a.MCPRemove(req.Server)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"servers": servers, "removed": req.Server})
		case "reload":
			servers, err := a.MCPReload(req.Server)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
		case "call":
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			res, err := a.MCPCall(ctx, req.Server, req.Tool, req.Args)
			out := map[string]any{"result": map[string]any{"text": res.Text(), "isError": res.IsError, "structured": res.StructuredContent}}
			if err != nil {
				out["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, out)
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 upsert | remove | reload | call）")
		}
	}))

	mux.HandleFunc("GET /api/agent/backups", s.api(func(w http.ResponseWriter, r *http.Request) {
		list, err := a.Backups()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"backups": list, "dir": a.BackupDir(), "hint": a.BackupHint()})
	}))

	mux.HandleFunc("POST /api/agent/backups", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Selection *backup.Selection `json:"selection"`
			Note      string            `json:"note"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sel := backup.Selection{}
		if req.Selection != nil {
			sel = *req.Selection
		}
		man, err := a.CreateBackup(sel, req.Note)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"manifest": man, "dir": a.BackupDir()})
	}))

	mux.HandleFunc("POST /api/agent/backups/verify", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		man, err := a.VerifyBackup(req.Path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"manifest": man, "ok": true})
	}))

	mux.HandleFunc("POST /api/agent/backups/restore", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path   string   `json:"path"`
			DryRun bool     `json:"dryRun"`
			Only   []string `json:"only"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := a.RestoreBackup(req.Path, backup.RestoreOptions{DryRun: req.DryRun, Only: req.Only})
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"result": res, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res})
	}))

	mux.HandleFunc("POST /api/agent/backups/delete", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.DeleteBackup(req.Path); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": req.Path})
	}))

	mux.HandleFunc("POST /api/agent/device-remark", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeviceID string `json:"deviceId"`
			Remark   string `json:"remark"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.SetDeviceRemark(req.DeviceID, req.Remark); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deviceId": req.DeviceID, "remark": strings.TrimSpace(req.Remark)})
	}))

	// 删除设备：硬删。离线设备记录会一直留着，用户要能清掉；备注一并清。
	// 备注存在 config.json（见 DeviceRemarks），不在设备表里，所以两张地方都要清。
	mux.HandleFunc("DELETE /api/agent/devices/{id}", s.api(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			writeErr(w, http.StatusBadRequest, "缺少设备 id")
			return
		}
		if err := s.hub.Store().DeleteDevice(id); err != nil {
			writeErr(w, http.StatusInternalServerError, "删除设备失败："+err.Error())
			return
		}
		// Agent 服务可能没挂（s.agent 为 nil）：没挂就没有备注要清
		if s.agent != nil {
			if err := s.agent.ClearDeviceRemark(id); err != nil {
				writeErr(w, http.StatusInternalServerError, "设备已删除，但清理备注失败："+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": id})
	}))

	mux.HandleFunc("GET /api/agent/config", s.api(func(w http.ResponseWriter, r *http.Request) {
		// 控制台要能回显当前参数（否则设置页只能显示写死默认值，改了也看不出来）。
		// 密钥一律不回显，只告诉前端「配没配」。
		cfg := a.Config()
		providers := make([]map[string]any, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			providers = append(providers, map[string]any{
				"name": p.Name, "protocol": p.Protocol, "baseUrl": p.BaseURL,
				"model": p.Model, "timeoutSec": p.TimeoutSec, "hasApiKey": p.APIKey != "",
			})
		}
		// 向量化通道只回"配没配 key"，永不回显 key 本身
		emb := map[string]any{
			"baseUrl": cfg.Embedding.BaseURL, "model": cfg.Embedding.Model,
			"dim": cfg.Embedding.Dim, "hasApiKey": cfg.Embedding.APIKey != "",
		}
		// 语音通道同样只回"配没配 key"，详情/写入走 /api/agent/voice
		vc := map[string]any{
			"enabled": cfg.Voice.Enabled, "protocol": cfg.Voice.Protocol,
			"baseUrl": cfg.Voice.BaseURL, "ttsModel": cfg.Voice.TTSModel,
			"sttModel": cfg.Voice.STTModel, "voice": cfg.Voice.Voice,
			"autoSpeak": cfg.Voice.AutoSpeak, "hasApiKey": cfg.Voice.APIKey != "",
		}
		// 浏览器工具：路径 / 地址都不是密钥，可以直接回显
		br := map[string]any{
			"enabled": cfg.Browser.Enabled, "headless": cfg.Browser.Headless,
			"chromePath": cfg.Browser.ChromePath, "cdpUrl": cfg.Browser.CDPURL,
			"timeoutSec": cfg.Browser.TimeoutSec, "maxBytes": cfg.Browser.MaxBytes,
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"allowRemote": cfg.AllowRemote, "allowShell": cfg.AllowShell,
			"workdir": cfg.Workdir, "skillsDir": cfg.SkillsDir,
			"maxSteps": cfg.MaxSteps, "maxRetries": cfg.MaxRetries,
			"tokenBudget": cfg.TokenBudget, "approvalTimeoutSec": cfg.ApprovalTimeoutSec,
			"backupKeep": cfg.BackupKeep, "autoRunOnStart": cfg.AutoRunOnStart,
			"providers": providers, "cron": len(cfg.Cron),
			"embedding": emb, "memory": cfg.Memory, "voice": vc,
			"browser":        br,
			"shellAllowCmds": cfg.ShellAllowCmds, "approvalAllow": cfg.ApprovalAllow,
			"search": map[string]any{
				"provider": cfg.Search.Provider, "baseUrl": cfg.Search.BaseURL,
				"hasApiKey": cfg.Search.APIKey != "", "maxResults": cfg.Search.MaxResults,
			},
		})
	}))

	mux.HandleFunc("POST /api/agent/config", s.api(func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		var patch struct {
			AllowShell         *bool   `json:"allowShell"`
			AllowRemote        *bool   `json:"allowRemote"`
			ApprovalTimeoutSec *int    `json:"approvalTimeoutSec"`
			MaxSteps           *int    `json:"maxSteps"`
			TokenBudget        *int    `json:"tokenBudget"`
			AutoRunOnStart     *bool   `json:"autoRunOnStart"`
			Workdir            *string `json:"workdir"`
			CronGoal           *string `json:"cronGoal"`
			CronExpr           *string `json:"cronExpr"`
			// 向量化通道（记忆语义检索用）。embeddingApiKey 传空 = 只改其它字段、保留原来的 key
			EmbeddingBaseURL *string `json:"embeddingBaseUrl"`
			EmbeddingModel   *string `json:"embeddingModel"`
			EmbeddingAPIKey  *string `json:"embeddingApiKey"`
			EmbeddingDim     *int    `json:"embeddingDim"`
			// 记忆术设置
			MemoryAutoRecall     *bool    `json:"memoryAutoRecall"`
			MemoryRecallProfile  *string  `json:"memoryRecallProfile"`
			MemoryRecallBudget   *int     `json:"memoryRecallBudget"`
			MemoryRecallMinScore *float64 `json:"memoryRecallMinScore"`
			MemoryNamespace      *string  `json:"memoryNamespace"`
			// 浏览器工具
			BrowserEnabled    *bool   `json:"browserEnabled"`
			BrowserHeadless   *bool   `json:"browserHeadless"`
			BrowserChromePath *string `json:"browserChromePath"`
			BrowserCDPURL     *string `json:"browserCdpUrl"`
			BrowserTimeoutSec *int    `json:"browserTimeoutSec"`
			// shell 命令白名单（空数组 = 不限制命令）
			ShellAllowCmds *[]string `json:"shellAllowCmds"`
			// 搜索通道（web_search 用）：provider = searxng | duckduckgo；留空 = 没配
			SearchProvider   *string `json:"searchProvider"`
			SearchBaseURL    *string `json:"searchBaseUrl"`
			SearchAPIKey     *string `json:"searchApiKey"`
			SearchMaxResults *int    `json:"searchMaxResults"`
		}
		if err := decodeBody(r, &patch); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if patch.AllowShell != nil {
			cfg.AllowShell = *patch.AllowShell
		}
		if patch.AllowRemote != nil {
			cfg.AllowRemote = *patch.AllowRemote
		}
		if patch.ApprovalTimeoutSec != nil && *patch.ApprovalTimeoutSec > 0 {
			cfg.ApprovalTimeoutSec = *patch.ApprovalTimeoutSec
		}
		if patch.MaxSteps != nil && *patch.MaxSteps > 0 {
			cfg.MaxSteps = *patch.MaxSteps
		}
		if patch.TokenBudget != nil && *patch.TokenBudget > 0 {
			cfg.TokenBudget = *patch.TokenBudget
		}
		if patch.AutoRunOnStart != nil {
			cfg.AutoRunOnStart = *patch.AutoRunOnStart
		}
		if patch.Workdir != nil {
			cfg.Workdir = *patch.Workdir
		}
		if patch.CronExpr != nil && patch.CronGoal != nil && *patch.CronGoal != "" {
			cfg.Cron = append(cfg.Cron, cronJob(*patch.CronExpr, *patch.CronGoal))
		}
		if patch.EmbeddingBaseURL != nil {
			cfg.Embedding.BaseURL = strings.TrimSpace(*patch.EmbeddingBaseURL)
		}
		if patch.EmbeddingModel != nil {
			cfg.Embedding.Model = strings.TrimSpace(*patch.EmbeddingModel)
		}
		if patch.EmbeddingAPIKey != nil && strings.TrimSpace(*patch.EmbeddingAPIKey) != "" {
			cfg.Embedding.APIKey = strings.TrimSpace(*patch.EmbeddingAPIKey) // 空 = 保留原 key
		}
		if patch.EmbeddingDim != nil && *patch.EmbeddingDim >= 0 {
			cfg.Embedding.Dim = *patch.EmbeddingDim
		}
		// 记忆术设置
		if patch.MemoryAutoRecall != nil {
			cfg.Memory.AutoRecall = *patch.MemoryAutoRecall
		}
		if patch.MemoryRecallProfile != nil {
			cfg.Memory.RecallProfile = *patch.MemoryRecallProfile
		}
		if patch.MemoryRecallBudget != nil && *patch.MemoryRecallBudget > 0 {
			cfg.Memory.RecallBudget = *patch.MemoryRecallBudget
		}
		if patch.MemoryRecallMinScore != nil && *patch.MemoryRecallMinScore > 0 && *patch.MemoryRecallMinScore <= 1 {
			cfg.Memory.RecallMinScore = *patch.MemoryRecallMinScore
		}
		if patch.MemoryNamespace != nil {
			cfg.Memory.Namespace = *patch.MemoryNamespace
		}
		// 浏览器工具
		if patch.BrowserEnabled != nil {
			cfg.Browser.Enabled = *patch.BrowserEnabled
		}
		if patch.BrowserHeadless != nil {
			cfg.Browser.Headless = *patch.BrowserHeadless
		}
		if patch.BrowserChromePath != nil {
			cfg.Browser.ChromePath = strings.TrimSpace(*patch.BrowserChromePath)
		}
		if patch.BrowserCDPURL != nil {
			cfg.Browser.CDPURL = strings.TrimSpace(*patch.BrowserCDPURL)
		}
		if patch.BrowserTimeoutSec != nil && *patch.BrowserTimeoutSec > 0 {
			cfg.Browser.TimeoutSec = *patch.BrowserTimeoutSec
		}
		// shell 命令白名单（传空数组 = 显式取消白名单，回到"不限制"）
		if patch.ShellAllowCmds != nil {
			cfg.ShellAllowCmds = append([]string{}, (*patch.ShellAllowCmds)...)
		}
		// 搜索通道：provider 传空字符串 = 取消配置（web_search 会明确报"没配"）
		if patch.SearchProvider != nil {
			cfg.Search.Provider = strings.TrimSpace(*patch.SearchProvider)
		}
		if patch.SearchBaseURL != nil {
			cfg.Search.BaseURL = strings.TrimSpace(*patch.SearchBaseURL)
		}
		if patch.SearchAPIKey != nil {
			cfg.Search.APIKey = strings.TrimSpace(*patch.SearchAPIKey)
		}
		if patch.SearchMaxResults != nil && *patch.SearchMaxResults > 0 {
			cfg.Search.MaxResults = *patch.SearchMaxResults
		}
		if err := a.SaveConfig(cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	// 探活向量化通道：真发一次极小请求，确认能用（会消耗一点点额度）
	mux.HandleFunc("POST /api/agent/embedding/test", s.api(func(w http.ResponseWriter, r *http.Request) {
		dim, err := a.EmbeddingPing(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dim": dim})
	}))

	// 给还没有向量的存量记忆补向量（刚配好通道时用一次；不补的话旧记忆永远搜不到语义）
	mux.HandleFunc("POST /api/agent/embedding/reindex", s.api(func(w http.ResponseWriter, r *http.Request) {
		res, err := a.ReindexEmbeddings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(),
				"done": res.Done, "remaining": res.Remaining})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "done": res.Done, "remaining": res.Remaining})
	}))

	// 定时任务与技能的增删改（原来只有"新增/只读"，见该文件里的说明）
	s.registerCronSkills(mux)
	// 人设文件的管理（QwenPaw 人设机制的 Go 版）
	s.registerPersona(mux)
	// 心跳任务的管理（配置 / HEARTBEAT.md / 手动触发）
	s.registerHeartbeat(mux)
}

func operatorFrom(r *http.Request) string {
	if v := r.URL.Query().Get("by"); v != "" {
		return v
	}
	return "console"
}

// cronJob 生成一条定时任务（控制台里临时添加用）
func cronJob(expr, goal string) config.CronJob {
	return config.CronJob{
		ID:   "job" + strconv.FormatInt(time.Now().UnixMilli(), 36),
		Expr: expr, Goal: goal, Recipe: "chat", Enabled: true,
	}
}
