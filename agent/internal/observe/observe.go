// Package observe 是白泽的「可观测性」汇总层：把散在各处的运行时信号（运行记录、
// 进程内计数器、日志环、活动追踪、通道/MCP 健康）收成一张**给人看的观测面**。
//
// 三条口径，先说清楚免得误读：
//   - **运行记录来自 runs.db**（落盘、可跨重启），所以"最近跑了多少次、多少 token"是准的；
//   - **工具/通道的调用明细只在进程内存里**（runs.db 只记整次运行的汇总，不记"哪个工具调了几次、
//     平均多久"），所以那部分**重启即清零** —— 宁可显示"本次启动以来的"，也不拿旧数字冒充现在；
//   - **健康检查只报事实**：能用就是 ok，配了但用不了就是 error 并写明原因，没配就是 warn 并指路，
//     绝不用一个绿色的勾把"其实没配"盖过去。
package observe

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"baize/internal/activity"
	"baize/internal/agentrt"
	"baize/internal/logx"
)

// ErrBadWindow 时间窗档位不认识（接口层据此回 400，而不是当成服务端错误）
var ErrBadWindow = errors.New("不支持的时间窗")

// ToolStat 单个工具的调用统计（进程内，重启即清零）
type ToolStat struct {
	Tool    string `json:"tool"`
	Calls   int    `json:"calls"`
	Errors  int    `json:"errors"`  // 执行失败（**不含**被审批拦下的）
	Blocked int    `json:"blocked"` // 被审批闸门拦下
	TotalMs int64  `json:"totalMs"`
	AvgMs   int64  `json:"avgMs"`
	LastErr string `json:"lastErr,omitempty"`
	LastAt  int64  `json:"lastAt,omitempty"`

	done int // 已收尾的次数（算平均耗时用；不导出，不进 JSON）
}

// ProviderStat 单个模型通道的调用统计（进程内，重启即清零）
type ProviderStat struct {
	Provider string `json:"provider"`
	// Model 不是计出来的，而是**汇总层按当前配置贴上去的标签**（见 agentsvc.Observability）。
	// 计数本身仍只按 provider 聚合、进程内、重启清零；找不到对应通道时留空。
	Model    string `json:"model,omitempty"`
	Requests int    `json:"requests"`
	Replies  int    `json:"replies"`
	Retries  int    `json:"retries"`
	Tokens   int    `json:"tokens"`
	// TotalMs / AvgMs 是"一次模型步骤"的等待时长（**含中间的重试等待**）——
	// 单次成功后不再有更细的耗时事件，这么算最贴近"这一步到底等了多久"。
	TotalMs int64  `json:"totalMs"`
	AvgMs   int64  `json:"avgMs"`
	LastErr string `json:"lastErr,omitempty"`
	LastAt  int64  `json:"lastAt,omitempty"`
}

// HealthItem 一条健康检查
type HealthItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | error
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// LogSummary 日志环的概览（计数 + 最近的告警/错误）
type LogSummary struct {
	Total  int         `json:"total"`
	Info   int         `json:"info"`
	Warn   int         `json:"warn"`
	Error  int         `json:"error"`
	Recent []logx.Line `json:"recent"` // 只留 warn / error，最近的在后
}

// Report 一次观测查询的完整结果（`GET /api/agent/observability` 就回这个）
type Report struct {
	GeneratedAt int64  `json:"generatedAt"`
	Window      string `json:"window"` // 1h | 24h | 7d | 30d | all
	SinceMs     int64  `json:"sinceMs,omitempty"`
	BucketMs    int64  `json:"bucketMs"`
	UptimeSec   int64  `json:"uptimeSec"`

	Runs      agentrt.RunSummary  `json:"runs"`
	Buckets   []agentrt.RunBucket `json:"buckets"`
	Tools     []ToolStat          `json:"tools"`
	Providers []ProviderStat      `json:"providers"`
	External  []TargetStat        `json:"external"`
	Logs      LogSummary          `json:"logs"`
	Health    []HealthItem        `json:"health"`
	Activity  activity.Snapshot   `json:"activity"`
}

// Window 把时间窗档位换算成 (起始毫秒, 分桶宽度, 规范档名)。
// 观测面只看"最近怎么样"，所以只给固定几档，不接受任意区间（免得有人拉十年数据把库扫爆）。
func Window(spec string, nowMs int64) (sinceMs, bucketMs int64, label string, err error) {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "", "24h", "1d", "day":
		return nowMs - time.Hour.Milliseconds()*24, time.Hour.Milliseconds(), "24h", nil
	case "1h", "hour":
		return nowMs - time.Hour.Milliseconds(), 5 * time.Minute.Milliseconds(), "1h", nil
	case "7d", "week":
		return nowMs - time.Hour.Milliseconds()*24*7, 6 * time.Hour.Milliseconds(), "7d", nil
	case "30d", "month":
		return nowMs - time.Hour.Milliseconds()*24*30, 24 * time.Hour.Milliseconds(), "30d", nil
	case "all":
		return 0, 24 * time.Hour.Milliseconds(), "all", nil
	}
	return 0, 0, "", fmt.Errorf("%w：%q（可用 1h | 24h | 7d | 30d | all）", ErrBadWindow, spec)
}

// SummarizeLogs 汇总日志环：按级别计数，并挑出最近的 warn/error（keep 条，太多了没人看）。
// 级别名由 logx 产出（DEBUG/INFO/WARN/ERROR），这里按前缀认，认不出的按 info 计。
func SummarizeLogs(lines []logx.Line, keep int) LogSummary {
	if keep <= 0 {
		keep = 20
	}
	out := LogSummary{Total: len(lines), Recent: []logx.Line{}}
	bad := []logx.Line{}
	for _, l := range lines {
		switch strings.ToUpper(strings.TrimSpace(l.Level)) {
		case "ERROR":
			out.Error++
			bad = append(bad, l)
		case "WARN":
			out.Warn++
			bad = append(bad, l)
		default:
			out.Info++
		}
	}
	if len(bad) > keep {
		bad = bad[len(bad)-keep:]
	}
	out.Recent = bad // 环里本来就是按时间递增，取尾巴就是"最近的"
	return out
}

// CountHealth 数一下各状态几条（界面顶部那排小灯用）
func CountHealth(items []HealthItem) (ok, warn, errs int) {
	for _, it := range items {
		switch it.Status {
		case "error":
			errs++
		case "warn":
			warn++
		default:
			ok++
		}
	}
	return
}
