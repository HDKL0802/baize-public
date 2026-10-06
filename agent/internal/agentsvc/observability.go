package agentsvc

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"baize/internal/observe"
)

// Observability 汇总一次"观测面"（窗口见 observe.Window）。
//
// 三块数据三个来源，各自的时效性不同，界面要分开说清楚：
//   - 运行汇总/分桶：来自 runs.db（落盘的），所以是**跨重启**的；
//   - 工具/通道明细：来自进程内计数（runs.db 不记这个），**本次启动以来**；
//   - 健康与积压：现读现算，不问缓存。
func (s *Service) Observability(window string) (observe.Report, error) {
	now := time.Now().UnixMilli()
	since, bucketMs, label, err := observe.Window(window, now)
	if err != nil {
		return observe.Report{}, err
	}
	rep := observe.Report{
		GeneratedAt: now,
		Window:      label,
		SinceMs:     since,
		BucketMs:    bucketMs,
		UptimeSec:   int64(time.Since(s.startedAt).Seconds()),
	}

	// 运行汇总（落盘数据；读不出来就如实报错，不拿空报告糊过去）
	if s.runs != nil {
		sum, err := s.runs.RunSummary(since)
		if err != nil {
			return observe.Report{}, fmt.Errorf("读取运行汇总失败：%w", err)
		}
		rep.Runs = sum
		if buckets, err := s.runs.RunBuckets(since, bucketMs); err == nil {
			rep.Buckets = buckets
		}
	}

	// 进程内计数（本次启动以来）
	if s.metrics != nil {
		rep.Tools, rep.Providers = s.metrics.Snapshot()
	}

	// 日志环概览（没接环就回空，不假装有日志）
	if s.logRing != nil {
		rep.Logs = observe.SummarizeLogs(s.logRing.Since(0), 20)
	} else {
		rep.Logs = observe.LogSummary{Recent: nil}
	}

	rep.Activity = s.Activity()
	rep.Health = s.health()
	return rep, nil
}

// health 一堆"有没有不对的地方"。状态口径：
//
//	ok    —— 能用 / 本来就该这样；
//	warn  —— 没配或配置得不太稳妥（还能用，但建议看一眼），一律带 Hint 指路；
//	error —— 配了但用不了（这是真问题，Detail 里给原因）。
func (s *Service) health() []observe.HealthItem {
	cfg := s.Config()
	items := []observe.HealthItem{}

	/* 模型通道 */
	s.mu.RLock()
	provs := append([]ProviderInfo{}, s.providers...)
	s.mu.RUnlock()
	usable, broken := 0, []string{}
	for _, p := range provs {
		if p.Usable {
			usable++
		} else {
			broken = append(broken, p.Name)
		}
	}
	switch {
	case len(provs) == 0:
		items = append(items, observe.HealthItem{Name: "模型通道", Status: "error",
			Detail: "一个都没配，白泽现在干不了活",
			Hint:   "在「模型通道」里加一个（OpenAI 兼容或 Anthropic）"})
	case usable == 0:
		items = append(items, observe.HealthItem{Name: "模型通道", Status: "error",
			Detail: fmt.Sprintf("%d 个通道都不可用：%s", len(provs), strings.Join(broken, "、")),
			Hint:   "在「模型通道」里点探活看具体报错"})
	case len(broken) > 0:
		items = append(items, observe.HealthItem{Name: "模型通道", Status: "warn",
			Detail: fmt.Sprintf("%d 个可用，%d 个不可用：%s", usable, len(broken), strings.Join(broken, "、"))})
	default:
		items = append(items, observe.HealthItem{Name: "模型通道", Status: "ok",
			Detail: fmt.Sprintf("%d 个可用", usable)})
	}

	/* 向量化通道 + 向量补全 */
	emb := s.embeddingInfo()
	switch {
	case emb.Err != "":
		items = append(items, observe.HealthItem{Name: "向量化通道", Status: "error",
			Detail: emb.Err, Hint: "记忆会退回关键词检索（语义搜不到）"})
	case !emb.Usable:
		items = append(items, observe.HealthItem{Name: "向量化通道", Status: "warn",
			Detail: "没配：记忆退回关键词检索",
			Hint:   "配一条 embedding 通道，记忆的语义检索与笔记的自动连边才有用"})
	default:
		items = append(items, observe.HealthItem{Name: "向量化通道", Status: "ok",
			Detail: strings.TrimSpace(emb.Model + " " + emb.BaseURL)})
	}
	if st, err := s.Memory().Stats(); err == nil && st.Missing > 0 && emb.Usable {
		items = append(items, observe.HealthItem{Name: "向量补全", Status: "warn",
			Detail: fmt.Sprintf("还有 %d 块记忆没向量（语义检索搜不到它们）", st.Missing),
			Hint:   "在「记忆星图 → 检索」里补一次向量"})
	}

	/* MCP 服务 */
	servers := s.MCP()
	connected, mcpErr, disabled := 0, []string{}, 0
	for _, sv := range servers {
		switch sv.Status {
		case "connected":
			connected++
		case "error":
			mcpErr = append(mcpErr, sv.Name)
		case "disabled":
			disabled++
		}
	}
	switch {
	case len(servers) == 0:
		items = append(items, observe.HealthItem{Name: "MCP 服务", Status: "ok", Detail: "没配（不需要就不用管）"})
	case len(mcpErr) > 0:
		items = append(items, observe.HealthItem{Name: "MCP 服务", Status: "warn",
			Detail: fmt.Sprintf("%d 个连不上：%s", len(mcpErr), strings.Join(mcpErr, "、")),
			Hint:   "在「MCP 服务」里点重连看具体报错"})
	default:
		items = append(items, observe.HealthItem{Name: "MCP 服务", Status: "ok",
			Detail: fmt.Sprintf("已连 %d 个（停用 %d 个）", connected, disabled)})
	}

	/* 频道 */
	chans := s.Channels().List()
	enabledCh := 0
	for _, c := range chans {
		if c.Enabled {
			enabledCh++
		}
	}
	if len(chans) == 0 {
		items = append(items, observe.HealthItem{Name: "频道", Status: "warn",
			Detail: "还没配：IM / webhook 接不进来",
			Hint:   "在「模型通道」旁边加一个频道（webhook / onebot / feishu）"})
	} else {
		items = append(items, observe.HealthItem{Name: "频道", Status: "ok",
			Detail: fmt.Sprintf("%d 个（启用 %d）", len(chans), enabledCh)})
	}

	/* 定时任务 */
	jobs := s.Jobs()
	enabledJobs, badJobs := 0, 0
	for _, j := range jobs {
		if j.Enabled {
			enabledJobs++
		}
		if j.ParseErr != "" {
			badJobs++
		}
	}
	if badJobs > 0 {
		items = append(items, observe.HealthItem{Name: "定时任务", Status: "error",
			Detail: fmt.Sprintf("%d 条的 cron 表达式不合法（永远不会跑）", badJobs),
			Hint:   "在「定时任务」里改表达式（接口层现在会当场挡下坏表达式，这里是历史遗留的）"})
	} else {
		items = append(items, observe.HealthItem{Name: "定时任务", Status: "ok",
			Detail: fmt.Sprintf("%d 条（启用 %d）", len(jobs), enabledJobs)})
	}

	/* 待审批积压 */
	if n := len(s.Approvals()); n > 0 {
		items = append(items, observe.HealthItem{Name: "待审批", Status: "warn",
			Detail: fmt.Sprintf("%d 件事在等人放行", n),
			Hint:   "去「任务与审批」放行或驳回——攒着不批，派出去的活就一直卡在半路"})
	} else {
		items = append(items, observe.HealthItem{Name: "待审批", Status: "ok", Detail: "没有积压"})
	}

	/* 搜索通道 */
	if strings.TrimSpace(cfg.Search.Provider) == "" {
		items = append(items, observe.HealthItem{Name: "搜索通道", Status: "warn",
			Detail: "没配：web_search 用不了",
			Hint:   "配 searxng（推荐）或 duckduckgo；没配时联网调研会明确报「去哪儿配」"})
	} else {
		items = append(items, observe.HealthItem{Name: "搜索通道", Status: "ok",
			Detail: strings.TrimSpace(cfg.Search.Provider + " " + cfg.Search.BaseURL)})
	}

	/* 命令白名单 */
	switch {
	case !cfg.AllowShell:
		items = append(items, observe.HealthItem{Name: "命令执行", Status: "ok", Detail: "已关闭"})
	case len(cfg.ShellAllowCmds) == 0:
		items = append(items, observe.HealthItem{Name: "命令执行", Status: "warn",
			Detail: "开着，但没设命令白名单（只靠人工审批兜底）",
			Hint:   "在配置里加 shellAllowCmds：白名单外的命令会被直接拒掉，比等人审批可靠"})
	default:
		items = append(items, observe.HealthItem{Name: "命令执行", Status: "ok",
			Detail: fmt.Sprintf("白名单 %d 条命令", len(cfg.ShellAllowCmds))})
	}

	/* 记忆与知识库（纯信息，不算问题） */
	if memStats, err := s.Memory().Stats(); err == nil && memStats.Chunks > 0 {
		items = append(items, observe.HealthItem{Name: "记忆库", Status: "ok",
			Detail: fmt.Sprintf("%d 块 / %d 天（已向量 %d）", memStats.Chunks, memStats.Days, memStats.Embedded)})
	}
	if st := s.KB().Stats(); st.Todos > 0 || st.Files > 0 {
		items = append(items, observe.HealthItem{Name: "知识库", Status: "ok",
			Detail: fmt.Sprintf("待办 %d（未完成 %d）· 附件 %d", st.Todos, st.Todos-st.Done, st.Files)})
	}

	// 先 error 再 warn 再 ok：界面按这个顺序，最该看的排最前
	rank := map[string]int{"error": 0, "warn": 1, "ok": 2}
	sort.SliceStable(items, func(i, j int) bool { return rank[items[i].Status] < rank[items[j].Status] })
	return items
}
