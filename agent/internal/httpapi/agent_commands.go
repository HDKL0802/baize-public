package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"baize/internal/skills"
	"baize/internal/slash"
)

// 魔法命令（QwenPaw 的斜杠命令，Go 重写）。
//
// 位置说明：命令处理器放在 httpapi 这一层，而不是 agentsvc —— 因为它们要同时碰两样东西：
// 后端的版本号（只有 Server 知道）和 Agent 的能力（Service 已经全部导出）。放这里既能
// 复用 Service 的 Skills/Memory/Checkpoints/Rollback/State，又不用为命令反向污染 agentsvc。
//
// 两条纪律：
//  1. 命令一律"秒回"，不经过模型（除非是技能注入，那种本来就是要模型照着做）。
//  2. 后端没有常驻会话状态（历史由手机端本地存），所以 /new、/clear 只回一个 action 指令，
//     由客户端去清本地历史 —— 不假装后端清了什么。

// commandCategories 类别名（给 /help 分组）
const (
	cmdCatChat  = "对话"
	cmdCatState = "状态"
	cmdCatSkill = "技能"
	cmdCatData  = "数据"
)

// registerCommands 注册魔法命令相关接口
func (s *Server) registerCommands(mux *http.ServeMux) {
	if s.agent == nil {
		return
	}
	mux.HandleFunc("GET /api/agent/commands", s.api(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"commands": s.cmds.Advertise()})
	}))
}

// buildCommands 装配命令注册表。只做注册，不执行。
func (s *Server) buildCommands() *slash.Registry {
	reg := slash.New()
	reg.Register(slash.Spec{
		Name: "help", Aliases: []string{"h", "?"}, Category: cmdCatState,
		Help:    "看命令清单；/help <命令名> 看单条说明",
		Handler: s.cmdHelp,
	})
	reg.Register(slash.Spec{
		Name: "status", Category: cmdCatState,
		Help:    "看运行状态（版本 / 模型 / 在跑的任务 / 记忆条数）",
		Handler: s.cmdStatus,
	})
	reg.Register(slash.Spec{
		Name: "skills", Category: cmdCatSkill,
		Help:    "列出全部技能；用 /<技能名> <要求> 强制用它",
		Handler: s.cmdSkills,
	})
	reg.Register(slash.Spec{
		Name: "new", Category: cmdCatChat,
		Help:    "开新对话（本机聊天上下文清空）",
		Handler: s.cmdNew,
	})
	reg.Register(slash.Spec{
		Name: "clear", Category: cmdCatChat,
		Help:    "清空当前上下文（本机清空，不留存）",
		Handler: s.cmdClear,
	})
	reg.Register(slash.Spec{
		Name: "compact", Category: cmdCatData,
		Help:    "整理长期记忆里的重复条目（每组只留最新一条）",
		Handler: s.cmdCompact,
	})
	reg.Register(slash.Spec{
		Name: "checkpoint", Category: cmdCatData,
		Help:    "看工作目录快照；/checkpoint rollback <id> 回滚",
		Handler: s.cmdCheckpoint,
	})
	reg.RegisterFallback(s.skillFallback)
	return reg
}

// dispatchCommand 尝试把一条用户输入当魔法命令执行。
// handled=false：不是命令（不以 "/" 开头，或既不是内置命令也不是技能名），按普通任务走。
// handled=true 且 res.Reply!=""：命令已处理完，直接回复，不跑模型。
// handled=true 且 res.Goal!=""：技能注入，调用方用 res.Goal 继续跑模型。
func (s *Server) dispatchCommand(ctx context.Context, text string) (slash.Result, bool) {
	if s.cmds == nil {
		return slash.Result{}, false
	}
	res, handled, err := s.cmds.Dispatch(ctx, text)
	if err != nil {
		return slash.Result{Reply: "命令没执行成功：" + err.Error()}, true
	}
	return res, handled
}

/* ---------- 各命令处理器 ---------- */

// cmdHelp 命令清单 / 单条帮助
func (s *Server) cmdHelp(_ context.Context, args, _ string) (slash.Result, error) {
	if args != "" {
		spec, _, ok := s.cmds.Resolve("/" + args)
		if !ok {
			return slash.Result{Reply: "没有这条命令：" + args + "。用 /help 看清单。"}, nil
		}
		var b strings.Builder
		b.WriteString("/" + spec.Name)
		if len(spec.Aliases) > 0 {
			b.WriteString("（别名：/" + strings.Join(spec.Aliases, "、/") + "）")
		}
		if spec.Help != "" {
			b.WriteString("\n" + spec.Help)
		}
		return slash.Result{Reply: b.String()}, nil
	}
	list := s.cmds.Advertise()
	if len(list) == 0 {
		return slash.Result{Reply: "（还没有注册任何命令）"}, nil
	}
	var b strings.Builder
	b.WriteString("魔法命令（以 / 开头，直接控制对话，不用等模型理解）：\n")
	lastCat := ""
	for _, c := range list {
		if c.Category != lastCat {
			b.WriteString("\n【" + c.Category + "】\n")
			lastCat = c.Category
		}
		fmt.Fprintf(&b, "  /%s", c.Name)
		if len(c.Aliases) > 0 {
			fmt.Fprintf(&b, "、/%s", strings.Join(c.Aliases, "、/"))
		}
		b.WriteString(" — " + c.Help + "\n")
	}
	b.WriteString("\n【技能】\n  /<技能名> <要求> — 强制用某个技能（/skills 看有哪些）\n")
	b.WriteString("\n命令不经过模型，秒回。")
	return slash.Result{Reply: b.String()}, nil
}

// cmdStatus 运行状态快照
func (s *Server) cmdStatus(_ context.Context, _, _ string) (slash.Result, error) {
	st := s.agent.State()
	var b strings.Builder
	fmt.Fprintf(&b, "白泽 v%s\n", s.version)
	fmt.Fprintf(&b, "工作目录：%s\n", st.Workdir)
	if st.Running {
		fmt.Fprintf(&b, "正在跑：%s\n", st.CurrentRunID)
	} else {
		b.WriteString("当前空闲\n")
	}
	var models []string
	for _, p := range st.Providers {
		if p.Usable {
			models = append(models, p.Name+"（"+p.Model+"）")
		}
	}
	if len(models) == 0 {
		b.WriteString("模型：没有可用的通道\n")
	} else {
		b.WriteString("模型：" + strings.Join(models, "、") + "\n")
	}
	fmt.Fprintf(&b, "技能：%d 个\n", len(st.Skills))
	fmt.Fprintf(&b, "记忆：%d 条（其中 %d 条有向量）", st.Memory.Chunks, st.Memory.Embedded)
	return slash.Result{Reply: b.String()}, nil
}

// cmdSkills 技能清单
func (s *Server) cmdSkills(_ context.Context, _, _ string) (slash.Result, error) {
	list := s.agent.Skills()
	if len(list) == 0 {
		return slash.Result{Reply: "还没有技能。让白泽把反复用到的做法沉淀成技能，或用控制台的技能页新建。"}, nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var b strings.Builder
	b.WriteString("技能（用 /<技能名> <要求> 强制调用）：\n")
	for _, sk := range list {
		desc := sk.Description
		if strings.TrimSpace(desc) == "" {
			desc = "（没有说明）"
		}
		fmt.Fprintf(&b, "  /%s — %s\n", sk.Name, desc)
	}
	return slash.Result{Reply: strings.TrimRight(b.String(), "\n")}, nil
}

// cmdNew 开新对话（清理由客户端完成）
func (s *Server) cmdNew(_ context.Context, _, _ string) (slash.Result, error) {
	return slash.Result{
		Reply:  "已开始新对话：这台设备上的聊天上下文清空了。",
		Action: "new",
	}, nil
}

// cmdClear 清空上下文（清理由客户端完成）
func (s *Server) cmdClear(_ context.Context, _, _ string) (slash.Result, error) {
	return slash.Result{
		Reply:  "已清空当前上下文。",
		Action: "clear",
	}, nil
}

// cmdCompact 整理长期记忆里的重复条目
func (s *Server) cmdCompact(_ context.Context, _, _ string) (slash.Result, error) {
	res, err := s.agent.Memory().CompactDuplicates()
	if err != nil {
		return slash.Result{}, err
	}
	return slash.Result{Reply: fmt.Sprintf(
		"已整理长期记忆：发现 %d 组重复，合并 %d 条，保留 %d 条。", res.Groups, res.Merged, res.Kept)}, nil
}

// cmdCheckpoint 看快照 / 回滚
func (s *Server) cmdCheckpoint(_ context.Context, args, _ string) (slash.Result, error) {
	fields := strings.Fields(args)
	if len(fields) > 0 && (fields[0] == "rollback" || fields[0] == "回滚") {
		if len(fields) < 2 {
			return slash.Result{Reply: "用法：/checkpoint rollback <快照id>（先用 /checkpoint 看有哪些）"}, nil
		}
		id := fields[1]
		if err := s.agent.Rollback(id); err != nil {
			return slash.Result{Reply: "回滚失败：" + err.Error()}, nil
		}
		s.lg.Warn("魔法命令回滚了工作目录", "id", id)
		return slash.Result{Reply: "已把工作目录回滚到快照 " + id + "。"}, nil
	}
	list, err := s.agent.Checkpoints()
	if err != nil {
		return slash.Result{}, err
	}
	if len(list) == 0 {
		return slash.Result{Reply: "还没有快照。白泽在改动工作目录前会自动存一份。"}, nil
	}
	var b strings.Builder
	b.WriteString("工作目录快照（用 /checkpoint rollback <id> 回滚）：\n")
	for _, c := range list {
		label := c.Label
		if strings.TrimSpace(label) == "" {
			label = "—"
		}
		fmt.Fprintf(&b, "  %s  %s  %d 文件 / %s  %s\n",
			c.ID, label, c.Files, humanBytes(c.Bytes),
			time.UnixMilli(c.CreatedAt).Format("2006-01-02 15:04"))
	}
	return slash.Result{Reply: strings.TrimRight(b.String(), "\n")}, nil
}

/* ---------- 技能回退 ---------- */

// skillFallback 技能回退：/<技能名> [要求] 强制使用某个技能。
//   - 只给技能名：返回该技能的说明与位置（回话，不跑模型）
//   - 技能名 + 要求：把技能正文注入目标，照常跑模型
//
// 没有同名技能时返回 matched=false，让调用方按普通文本处理。
func (s *Server) skillFallback(_ context.Context, raw string) (slash.Result, bool) {
	name, input := parseSkillQuery(raw)
	if name == "" {
		return slash.Result{}, false
	}
	var sk skills.Skill
	var found bool
	for _, x := range s.agent.Skills() {
		if strings.EqualFold(x.Name, name) {
			sk, found = x, true
			break
		}
	}
	if !found {
		return slash.Result{}, false
	}
	if strings.TrimSpace(input) == "" {
		return slash.Result{
			Name: sk.Name,
			Reply: fmt.Sprintf("技能「%s」：%s\n位置：%s\n\n要让它照做，发 /%s <你的要求>。",
				sk.Name, sk.Description, sk.Path, sk.Name),
		}, true
	}
	goal := "<skill name=\"" + sk.Name + "\">\n" + strings.TrimSpace(sk.Body) + "\n</skill>\n\n" + input
	return slash.Result{Name: sk.Name, Goal: goal}, true
}

// parseSkillQuery 解析技能写法：/<name> [input] 或 /[<带空格的 name>] [input]
func parseSkillQuery(raw string) (name, input string) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(t, "/") {
		return "", ""
	}
	body := strings.TrimSpace(t[1:])
	if body == "" {
		return "", ""
	}
	if strings.HasPrefix(body, "[") {
		if end := strings.Index(body, "]"); end > 0 {
			return strings.TrimSpace(body[1:end]), strings.TrimSpace(body[end+1:])
		}
		return "", ""
	}
	n, rest, _ := strings.Cut(body, " ")
	return strings.TrimSpace(n), strings.TrimSpace(rest)
}

// humanBytes 人类可读的字节数
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
