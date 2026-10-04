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
		store := a.Memory()
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
		nodes, err := a.Memory().Tree(atoiDefault(r.URL.Query().Get("limit"), 40))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 顺手告诉界面"有几天摘要还没跟上"：不然节点列表里空空如也，说不清是没记忆还是没摘要
		stale, err := a.Memory().StaleDays(8)
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
		n, err := a.RebuildMemoryTree(ctx)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "days": n})
	}))

	// 整理重复记忆：同类回执 / 同一目标的重复结论，每组只留最新的一条。
	// 这是删除操作，所以只提供手动接口，绝不自动跑。
	mux.HandleFunc("POST /api/agent/memory/compact", s.api(func(w http.ResponseWriter, r *http.Request) {
		res, err := a.Memory().CompactDuplicates()
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
		res, err := a.Memory().Ingest(req.Content, memory.IngestOptions{
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

	mux.HandleFunc("POST /api/agent/approvals/{id}/approve", s.api(func(w http.ResponseWriter, r *http.Request) {
		by := operatorFrom(r)
		if err := a.Approve(r.PathValue("id"), by); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"approved": r.PathValue("id"), "by": by})
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
		writeJSON(w, http.StatusOK, map[string]any{"providers": a.Providers(), "allowRemote": cfg.AllowRemote})
	}))

	// 模型通道的加/改/删/探活：action=upsert|remove|test
	mux.HandleFunc("POST /api/agent/providers", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string           `json:"action"`
			Name        string           `json:"name"`
			Config      *config.Provider `json:"config"`
			AllowRemote bool             `json:"allowRemote"`
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
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 upsert | remove | test）")
		}
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
		writeJSON(w, http.StatusOK, map[string]any{
			"allowRemote": cfg.AllowRemote, "allowShell": cfg.AllowShell,
			"workdir": cfg.Workdir, "skillsDir": cfg.SkillsDir,
			"maxSteps": cfg.MaxSteps, "maxRetries": cfg.MaxRetries,
			"tokenBudget": cfg.TokenBudget, "approvalTimeoutSec": cfg.ApprovalTimeoutSec,
			"backupKeep": cfg.BackupKeep, "autoRunOnStart": cfg.AutoRunOnStart,
			"providers": providers, "cron": len(cfg.Cron),
			"embedding": emb, "memory": cfg.Memory, "voice": vc,
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
