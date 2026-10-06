package observe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"baize/internal/hooks"
)

// Metrics 进程内的观测计数器：订阅生命周期事件，按「工具」与「模型通道」累积
// 调用数 / 失败数 / 被审批拦下数 / 耗时 / 最近一次出错原因。
//
// 为什么自己记而不从 runs.db 算：那张表只记**整次运行的汇总**（步数、总 token），
// 不记"哪个工具调了几次、平均多久" —— 而那恰恰是排查"这次为什么这么慢 / 老失败"最需要的。
//
// 只留内存、重启即清零：与 activity 追踪同一口径，不拿一份过期旧统计冒充"现在的状况"。
type Metrics struct {
	mu sync.Mutex

	tools map[string]*ToolStat
	provs map[string]*ProviderStat

	toolStart   map[string]int64 // 工具：before 的时间（与 after/error 配对算耗时）
	provStart   map[string]int64 // 通道：request 的时间（与 response 配对算等待）
	blockedTool map[string]bool  // 刚被审批拦下的工具：紧接着的那次 error 不重复计失败

	now func() int64
}

// NewMetrics 创建计数器
func NewMetrics() *Metrics {
	return &Metrics{
		tools:       map[string]*ToolStat{},
		provs:       map[string]*ProviderStat{},
		toolStart:   map[string]int64{},
		provStart:   map[string]int64{},
		blockedTool: map[string]bool{},
		now:         func() int64 { return time.Now().UnixMilli() },
	}
}

// Attach 订阅全部生命周期事件（bus 为 nil 时静默不订阅，不报错）
func (m *Metrics) Attach(bus *hooks.Bus) {
	if m == nil || bus == nil {
		return
	}
	bus.OnAny(func(_ context.Context, ev hooks.Event, p hooks.Payload) {
		m.note(ev, p)
	})
}

// note 处理一条事件
func (m *Metrics) note(ev hooks.Event, p hooks.Payload) {
	if m == nil {
		return
	}
	tool := str(p["tool"])
	provider := str(p["provider"])
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	switch ev {
	case hooks.EventToolBefore:
		if tool == "" {
			return
		}
		st := m.toolLocked(tool)
		st.Calls++
		st.LastAt = now
		m.blockedTool[tool] = false
		m.toolStart[tool] = now

	case hooks.EventToolBlocked:
		if tool == "" {
			return
		}
		st := m.toolLocked(tool)
		st.Blocked++
		st.LastAt = now
		m.blockedTool[tool] = true

	case hooks.EventToolAfter, hooks.EventToolError:
		if tool == "" {
			return
		}
		st := m.toolLocked(tool)
		if start, ok := m.toolStart[tool]; ok {
			if d := now - start; d >= 0 {
				st.TotalMs += d
				st.done++
			}
			delete(m.toolStart, tool)
		}
		if ev == hooks.EventToolError {
			// 被审批拦下时，运行时会把那次拒绝也报成一次工具失败 —— 那种不计进 Errors，
			// 否则"拦下了"会被读成"工具坏了"，排查方向直接跑偏。
			if m.blockedTool[tool] {
				m.blockedTool[tool] = false
			} else {
				st.Errors++
				if e := str(p["err"]); e != "" {
					st.LastErr = e
				}
			}
		} else {
			m.blockedTool[tool] = false
		}
		st.LastAt = now

	case hooks.EventLLMRequest:
		if provider == "" {
			return
		}
		st := m.provLocked(provider)
		st.Requests++
		st.LastAt = now
		m.provStart[provider] = now

	case hooks.EventLLMResponse:
		if provider == "" {
			return
		}
		st := m.provLocked(provider)
		st.Replies++
		st.Tokens += intOf(p["tokens"])
		if start, ok := m.provStart[provider]; ok {
			if d := now - start; d >= 0 {
				st.TotalMs += d
			}
			delete(m.provStart, provider)
		}
		st.LastAt = now

	case hooks.EventLLMRetry:
		if provider == "" {
			return
		}
		st := m.provLocked(provider)
		st.Retries++
		if e := str(p["err"]); e != "" {
			st.LastErr = e
		}
		st.LastAt = now
	}
}

func (m *Metrics) toolLocked(name string) *ToolStat {
	st := m.tools[name]
	if st == nil {
		st = &ToolStat{Tool: name}
		m.tools[name] = st
	}
	return st
}

func (m *Metrics) provLocked(name string) *ProviderStat {
	st := m.provs[name]
	if st == nil {
		st = &ProviderStat{Provider: name}
		m.provs[name] = st
	}
	return st
}

// Snapshot 当前统计：工具按调用数降序、通道按请求数降序（并列时按名字，保证顺序稳定）
func (m *Metrics) Snapshot() ([]ToolStat, []ProviderStat) {
	if m == nil {
		return []ToolStat{}, []ProviderStat{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	tools := make([]ToolStat, 0, len(m.tools))
	for _, st := range m.tools {
		c := *st
		if c.done > 0 {
			c.AvgMs = c.TotalMs / int64(c.done)
		}
		tools = append(tools, c)
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Calls != tools[j].Calls {
			return tools[i].Calls > tools[j].Calls
		}
		return tools[i].Tool < tools[j].Tool
	})

	provs := make([]ProviderStat, 0, len(m.provs))
	for _, st := range m.provs {
		c := *st
		if c.Replies > 0 {
			c.AvgMs = c.TotalMs / int64(c.Replies)
		}
		provs = append(provs, c)
	}
	sort.Slice(provs, func(i, j int) bool {
		if provs[i].Requests != provs[j].Requests {
			return provs[i].Requests > provs[j].Requests
		}
		return provs[i].Provider < provs[j].Provider
	})
	return tools, provs
}

/* ---------- 取值小工具 ---------- */

// str 把负载里的任意值转成字符串（取不到就空串）
func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case fmt.Stringer:
		return strings.TrimSpace(t.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// intOf 把负载里的任意值转成 int（取不到 / 转不了就 0）
func intOf(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
