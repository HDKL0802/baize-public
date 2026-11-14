package httpapi

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"baize/internal/agentsvc"
	"baize/internal/cron"
	"baize/internal/skills"
)

// registerCronSkills 注册「定时任务」与「技能」的管理接口。
//
// 这两块原先只能读：定时任务只有 POST /api/agent/config 的「新增」，技能只能从
// /api/agent/state 的 skills 里列出来 —— 所以桌面端只能做成「只增 / 只读」。
// 这里补成完整的增删改，两处都刻意复用已有实现，不另起一套：
//   - 技能的增删改走 agentsvc.SkillManager()（就是 Agent 那个 skill_manage 的实现），
//     校验、落盘位置、归档语义全部与白泽自己建技能一致；
//   - 定时任务改的是同一份 config，调度器每 20 秒重读一次 config，所以改完最多 20 秒生效。
func (s *Server) registerCronSkills(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	a := s.agent

	/* ---------------- 定时任务 ---------------- */

	mux.HandleFunc("GET /api/agent/cron", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": a.Jobs()})
	}))

	// 立即执行一条定时任务（不等排期）。
	//
	// 单独一个接口而不是塞进 POST /api/agent/cron 的 action：立即执行会真的派活、
	// 真的占住顶层串行锁、真的花钱调模型 —— 与"改一条配置"完全不是一回事，
	// 混在一个接口里以后想给它加审批/限流都无处下手。
	// 返回 202 + runId：前端拿它去 /api/agent/runs/{id} 看进展，不在这里干等。
	mux.HandleFunc("POST /api/agent/cron/run", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		id, err := a.CronRun(req.ID)
		if err != nil {
			// 找不到这条任务 = 404；任务存在但现在不能跑（已有任务在跑 / 没有目标）= 409。
			// 两者对前端是两种提示语，所以按哨兵错误分，不靠匹配错误文本。
			code := http.StatusConflict
			if errors.Is(err, agentsvc.ErrCronNotFound) || errors.Is(err, agentsvc.ErrCronBadRequest) {
				code = http.StatusNotFound
			}
			writeErr(w, code, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "runId": id, "jobs": a.Jobs()})
	}))

	// action=add|update|toggle|remove
	// 表达式一律先用 cron.Parse 校验：不然坏表达式会以 parseError 的形式长期躺在列表里，
	// 用户还以为是"加上了但没跑"。
	mux.HandleFunc("POST /api/agent/cron", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action  string `json:"action"`
			ID      string `json:"id"`
			Expr    string `json:"expr"`
			Goal    string `json:"goal"`
			Recipe  string `json:"recipe"`
			Enabled *bool  `json:"enabled"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "add"
		}

		if action == "add" {
			expr, goal := strings.TrimSpace(req.Expr), strings.TrimSpace(req.Goal)
			if expr == "" || goal == "" {
				writeErr(w, http.StatusBadRequest, "add 需要 expr 与 goal")
				return
			}
			if _, err := cron.Parse(expr); err != nil {
				writeErr(w, http.StatusBadRequest, "cron 表达式不合法："+err.Error())
				return
			}
			job := cronJob(expr, goal)
			if rc := strings.TrimSpace(req.Recipe); rc != "" {
				job.Recipe = rc
			}
			if req.Enabled != nil {
				job.Enabled = *req.Enabled
			}
			cfg := a.Config()
			cfg.Cron = append(cfg.Cron, job)
			if err := a.SaveConfig(cfg); err != nil {
				writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
				return
			}
			// 新任务的排期由调度器每 20 秒重读配置时才算出来（nextAt / exprText 都在那一步）。
			// 若此刻立刻回列表，新任务会带着空的"下次触发时间 + 人话描述"回去，界面看着像没生效。
			// 所以这里直接把新任务排上（与调度器同一把锁、同一个函数），回给界面的就是最终态。
			a.ScheduleNow()
			writeJSON(w, http.StatusOK, map[string]any{"jobs": a.Jobs(), "added": job.ID})
			return
		}

		// update / toggle / remove 都要先按 id 找到那一条
		id := strings.TrimSpace(req.ID)
		if id == "" {
			writeErr(w, http.StatusBadRequest, action+" 需要 id")
			return
		}
		cfg := a.Config()
		idx := -1
		for i := range cfg.Cron {
			if cfg.Cron[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			writeErr(w, http.StatusNotFound, "没有这个定时任务："+id)
			return
		}

		switch action {
		case "remove":
			cfg.Cron = append(cfg.Cron[:idx], cfg.Cron[idx+1:]...)
		case "toggle":
			if req.Enabled == nil {
				writeErr(w, http.StatusBadRequest, "toggle 需要 enabled")
				return
			}
			cfg.Cron[idx].Enabled = *req.Enabled
		case "update":
			if e := strings.TrimSpace(req.Expr); e != "" {
				if _, err := cron.Parse(e); err != nil {
					writeErr(w, http.StatusBadRequest, "cron 表达式不合法："+err.Error())
					return
				}
				cfg.Cron[idx].Expr = e
			}
			if g := strings.TrimSpace(req.Goal); g != "" {
				cfg.Cron[idx].Goal = g
			}
			if rc := strings.TrimSpace(req.Recipe); rc != "" {
				cfg.Cron[idx].Recipe = rc
			}
			if req.Enabled != nil {
				cfg.Cron[idx].Enabled = *req.Enabled
			}
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 add | update | toggle | remove）")
			return
		}

		if err := a.SaveConfig(cfg); err != nil {
			writeErr(w, http.StatusBadRequest, "保存失败："+err.Error())
			return
		}
		// 改完立刻重排（同新增）：否则回给界面的列表里，这条任务的"下次触发时间"
		// 还是按旧表达式算的，用户会以为改动没生效。
		a.ScheduleNow()
		writeJSON(w, http.StatusOK, map[string]any{"jobs": a.Jobs(), "ok": true})
	}))

	/* ---------------- 技能 ---------------- */

	// 列表额外带一个 slug：技能目录名是 ASCII（Manager 按它定位），
	// 而 Skill.Name 是 front-matter 里的展示名（可以是中文）。界面要改/删时
	// 得用 slug，所以这里直接给出来，省得前端从 path 里反推。
	// GET 与 POST 回同一个形状（都走 skillViews），前端只写一处解析。
	mux.HandleFunc("GET /api/agent/skills", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"skills": s.skillViews()})
	}))

	// action=create|edit|patch|writeFile|delete|import，另有 save = 有则改写、无则新建
	// 复用 Agent 的 skill_manage / skill_delete：校验、路径、归档语义完全一致
	// 读取技能的原始 SKILL.md 全文（含 front-matter）。
	//
	// 为什么单独给一个接口：GET /api/agent/skills 只回解析后的 name/description/body，
	// 界面按这三个字段重建整篇，会把原文件里其它 front-matter 字段（triggers / version /
	// 自定义键）悄悄丢掉 —— 正是「看着能用其实不能用」。这里按目录名/展示名定位后直接读
	// 磁盘正本，一个字节不重组，编辑保存时就能保住全部 front-matter。
	mux.HandleFunc("GET /api/agent/skills/raw", s.api(func(w http.ResponseWriter, r *http.Request) {
		name := s.resolveSkillName(r.URL.Query().Get("name"))
		if name == "" {
			writeErr(w, http.StatusBadRequest, "需要技能名（name）")
			return
		}
		var found *skills.Skill
		for _, sk := range a.Skills() {
			if skillSlug(sk) == name {
				cp := sk
				found = &cp
				break
			}
		}
		if found == nil {
			writeErr(w, http.StatusNotFound, "找不到技能："+name)
			return
		}
		raw, err := os.ReadFile(found.Path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取 SKILL.md 失败："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name": found.Name, "slug": name, "content": string(raw), "bytes": len(raw),
		})
	}))

	mux.HandleFunc("POST /api/agent/skills", s.api(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action      string `json:"action"`
			Name        string `json:"name"`
			Category    string `json:"category"`
			Content     string `json:"content"`
			FilePath    string `json:"filePath"`
			FileContent string `json:"fileContent"`
			OldString   string `json:"oldString"`
			NewString   string `json:"newString"`
			ReplaceAll  bool   `json:"replaceAll"`
			// import 用：整包文件（path + content）。path 只允许根下的 SKILL.md，
			// 或 manage.go 里 AllowedSubdirs（references/templates/scripts/assets）下的文本文件。
			Files []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"files"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		m := a.SkillManager()
		if m == nil {
			writeErr(w, http.StatusServiceUnavailable, "技能库没启用")
			return
		}
		name := s.resolveSkillName(req.Name)
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action == "" {
			action = "save"
		}

		var res map[string]any
		var err error
		switch action {
		case "save": // 有则改写、无则新建：界面上就一个「保存」按钮，不用先判断存不存在
			if s.skillsExists(name) {
				res, err = m.Edit(name, req.Content)
			} else {
				res, err = m.Create(name, strings.TrimSpace(req.Category), req.Content)
			}
		case "create":
			res, err = m.Create(name, strings.TrimSpace(req.Category), req.Content)
		case "edit":
			res, err = m.Edit(name, req.Content)
		case "patch":
			res, err = m.Patch(name, req.OldString, req.NewString, strings.TrimSpace(req.FilePath), req.ReplaceAll)
		case "writefile":
			res, err = m.WriteFile(name, strings.TrimSpace(req.FilePath), req.FileContent)
		case "import":
			// 导入 = 先 Create 落 SKILL.md，再逐个 WriteFile 写支持文件 —— 全部复用 skills.Manager，
			// 不自己动文件系统，校验/落盘位置与白泽自己建技能完全一致。
			skillMD := ""
			for _, f := range req.Files {
				if importPath(f.Path) == "SKILL.md" {
					skillMD = f.Content
					break
				}
			}
			if strings.TrimSpace(name) == "" {
				writeErr(w, http.StatusBadRequest, "import 需要 name（技能目录名）")
				return
			}
			if strings.TrimSpace(skillMD) == "" {
				writeErr(w, http.StatusBadRequest, "import 需要一份非空的 SKILL.md（files 里给 path=SKILL.md 的 content）")
				return
			}
			// 二进制 / 非文本这轮不支持：assets 下直接拒绝，别落下个用不了的东西
			for _, f := range req.Files {
				if p := importPath(f.Path); p == "assets" || strings.HasPrefix(p, "assets/") {
					writeErr(w, http.StatusBadRequest, "这轮只支持文本文件：assets/ 下的二进制（图片等）暂不支持导入")
					return
				}
			}
			res, err = m.Create(name, strings.TrimSpace(req.Category), skillMD)
			if err != nil {
				break
			}
			for _, f := range req.Files {
				p := importPath(f.Path)
				if p == "" || p == "SKILL.md" {
					continue
				}
				if _, werr := m.WriteFile(name, p, f.Content); werr != nil {
					err = werr
					break
				}
			}
		case "delete":
			res, err = m.Delete(name)
		default:
			writeErr(w, http.StatusBadRequest, "不支持的 action："+action+"（可用 save | create | edit | patch | writeFile | import | delete）")
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res, "skills": s.skillViews()})
	}))
}

// importPath 归一 import 请求里的文件路径：去空白、统一成斜杠形式，便于判断是不是 SKILL.md / assets 下的文件。
func importPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// skillView 技能 + 它的目录名（slug）。嵌一个 skills.Skill，JSON 里会平铺展开。
type skillView struct {
	skills.Skill
	Slug string `json:"slug"`
}

// skillViews 技能清单（统一形状：GET /api/agent/skills 与 POST 的返回都用它）
func (s *Server) skillViews() []skillView {
	list := s.agent.Skills()
	views := make([]skillView, 0, len(list))
	for _, sk := range list {
		views = append(views, skillView{Skill: sk, Slug: skillSlug(sk)})
	}
	return views
}

// skillSlug 技能目录名：Skill.Path 是 <技能目录>/SKILL.md，取上一级目录名
func skillSlug(sk skills.Skill) string { return filepath.Base(filepath.Dir(sk.Path)) }

// resolveSkillName 把调用方给的「技能名」归一到技能目录名。
//
// 为什么需要：界面上看到的是 front-matter 里的展示名（可能是中文，例如「分诊」），
// 而 Manager.locate 是按**目录名**（ASCII，例如 triage）找的。两边都接受，
// 界面上不管用哪个名字改/删都能落在同一个技能上。
func (s *Server) resolveSkillName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, sk := range s.agent.Skills() {
		slug := skillSlug(sk)
		if slug == name {
			return name // 本来就是目录名
		}
		if sk.Name == name {
			return slug // 按展示名找到 → 换成目录名
		}
	}
	return name // 没命中：原样交给 Manager，让它回"找不到"的错
}

// skillsExists 技能库里有没有这个技能（save 做「有则改写、无则新建」用）
func (s *Server) skillsExists(slug string) bool {
	for _, sk := range s.agent.Skills() {
		if skillSlug(sk) == slug {
			return true
		}
	}
	return false
}
