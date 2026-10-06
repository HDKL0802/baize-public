package observe

import (
	"context"
	"testing"

	"baize/internal/hooks"
	"baize/internal/logx"
)

// newMetricsWithClock 造一个时钟可控的计数器（测试里手动推进时间）
func newMetricsWithClock() (*Metrics, *int64) {
	m := NewMetrics()
	t := int64(1_000_000)
	m.now = func() int64 { return t }
	return m, &t
}

func TestMetricsToolCallsAndLatency(t *testing.T) {
	m, clk := newMetricsWithClock()
	bus := hooks.NewBus()
	m.Attach(bus)
	ctx := context.Background()

	*clk = 1000
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"tool": "web_fetch"})
	*clk = 1500
	bus.Emit(ctx, hooks.EventToolAfter, hooks.Payload{"tool": "web_fetch", "result": "ok"})

	*clk = 2000
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"tool": "web_fetch"})
	*clk = 2400
	bus.Emit(ctx, hooks.EventToolError, hooks.Payload{"tool": "web_fetch", "err": "连不上"})

	// 另一个工具各调一次
	*clk = 3000
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"tool": "note"})
	*clk = 3010
	bus.Emit(ctx, hooks.EventToolAfter, hooks.Payload{"tool": "note"})

	tools, _ := m.Snapshot()
	if len(tools) != 2 {
		t.Fatalf("应当统计到 2 个工具，实际 %d：%+v", len(tools), tools)
	}
	// 调用数多的排前面
	if tools[0].Tool != "web_fetch" || tools[0].Calls != 2 || tools[0].Errors != 1 {
		t.Fatalf("web_fetch 统计不对：%+v", tools[0])
	}
	if tools[0].TotalMs != 900 || tools[0].AvgMs != 450 {
		t.Fatalf("web_fetch 耗时应为 900/450，实际 %d/%d", tools[0].TotalMs, tools[0].AvgMs)
	}
	if tools[0].LastErr != "连不上" {
		t.Fatalf("最近出错原因没记下：%q", tools[0].LastErr)
	}
	if tools[1].Tool != "note" || tools[1].Calls != 1 || tools[1].AvgMs != 10 {
		t.Fatalf("note 统计不对：%+v", tools[1])
	}
}

// 被审批拦下时运行时会补发一次工具失败 —— 那次不该计进 Errors，
// 否则"拦下了"会被读成"工具坏了"，排查方向直接跑偏。
func TestMetricsBlockedIsNotCountedAsError(t *testing.T) {
	m, clk := newMetricsWithClock()
	bus := hooks.NewBus()
	m.Attach(bus)
	ctx := context.Background()

	*clk = 100
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{"tool": "fs_delete"})
	*clk = 110
	bus.Emit(ctx, hooks.EventToolBlocked, hooks.Payload{"tool": "fs_delete"})
	*clk = 120
	bus.Emit(ctx, hooks.EventToolError, hooks.Payload{"tool": "fs_delete", "err": "未获人工批准"})

	tools, _ := m.Snapshot()
	if len(tools) != 1 {
		t.Fatalf("应当只有 1 个工具：%+v", tools)
	}
	st := tools[0]
	if st.Blocked != 1 {
		t.Fatalf("拦下次数应为 1：%+v", st)
	}
	if st.Errors != 0 {
		t.Fatalf("被拦下的那次不该算工具失败：%+v", st)
	}
	if st.Calls != 1 || st.AvgMs != 20 {
		t.Fatalf("调用数/耗时不对：%+v", st)
	}
}

func TestMetricsProviders(t *testing.T) {
	m, clk := newMetricsWithClock()
	bus := hooks.NewBus()
	m.Attach(bus)
	ctx := context.Background()

	*clk = 1000
	bus.Emit(ctx, hooks.EventLLMRequest, hooks.Payload{"provider": "deepseek"})
	*clk = 1800
	bus.Emit(ctx, hooks.EventLLMResponse, hooks.Payload{"provider": "deepseek", "tokens": 120, "toolCalls": 1})

	*clk = 2000
	bus.Emit(ctx, hooks.EventLLMRequest, hooks.Payload{"provider": "deepseek"})
	bus.Emit(ctx, hooks.EventLLMRetry, hooks.Payload{"provider": "deepseek", "attempt": 1, "err": "超时"})
	*clk = 3000
	bus.Emit(ctx, hooks.EventLLMResponse, hooks.Payload{"provider": "deepseek", "tokens": 80})

	_, provs := m.Snapshot()
	if len(provs) != 1 {
		t.Fatalf("应当统计到 1 个通道：%+v", provs)
	}
	st := provs[0]
	if st.Provider != "deepseek" || st.Requests != 2 || st.Replies != 2 || st.Retries != 1 {
		t.Fatalf("通道统计不对：%+v", st)
	}
	if st.Tokens != 200 {
		t.Fatalf("token 累计应为 200，实际 %d", st.Tokens)
	}
	// 第一次 800ms、第二次 1000ms（含重试等待）→ 平均 900
	if st.TotalMs != 1800 || st.AvgMs != 900 {
		t.Fatalf("等待时长应为 1800/900，实际 %d/%d", st.TotalMs, st.AvgMs)
	}
	if st.LastErr != "超时" {
		t.Fatalf("最近出错原因没记下：%q", st.LastErr)
	}
}

func TestMetricsIgnoresNamelessEvents(t *testing.T) {
	m, _ := newMetricsWithClock()
	bus := hooks.NewBus()
	m.Attach(bus)
	ctx := context.Background()
	// LLM 事件里 runId 常为空、provider 也偶有空值：不要为它们造出空名条目
	bus.Emit(ctx, hooks.EventLLMRequest, hooks.Payload{})
	bus.Emit(ctx, hooks.EventToolBefore, hooks.Payload{})
	bus.Emit(ctx, hooks.EventRunStart, hooks.Payload{"goal": "x"})

	tools, provs := m.Snapshot()
	if len(tools) != 0 || len(provs) != 0 {
		t.Fatalf("空名字的事件不该建条目：tools=%+v provs=%+v", tools, provs)
	}
}

func TestWindowSpecs(t *testing.T) {
	now := int64(10_000_000_000)
	cases := []struct {
		spec       string
		wantLabel  string
		wantSince  int64
		wantBucket int64
	}{
		{"", "24h", now - 24*3600*1000, 3600 * 1000},
		{"1h", "1h", now - 3600*1000, 5 * 60 * 1000},
		{"7d", "7d", now - 7*24*3600*1000, 6 * 3600 * 1000},
		{"30d", "30d", now - 30*24*3600*1000, 24 * 3600 * 1000},
		{"all", "all", 0, 24 * 3600 * 1000},
	}
	for _, c := range cases {
		since, bucket, label, err := Window(c.spec, now)
		if err != nil {
			t.Fatalf("Window(%q) 报错：%v", c.spec, err)
		}
		if label != c.wantLabel || since != c.wantSince || bucket != c.wantBucket {
			t.Errorf("Window(%q) = (%d,%d,%s)，期望 (%d,%d,%s)", c.spec, since, bucket, label, c.wantSince, c.wantBucket, c.wantLabel)
		}
	}
	if _, _, _, err := Window("999y", now); err == nil {
		t.Fatal("不支持的档位应当报错（不然会把库扫爆）")
	}
}

func TestSummarizeLogs(t *testing.T) {
	lines := []logx.Line{
		{Seq: 1, Level: "INFO", Text: "a"},
		{Seq: 2, Level: "WARN", Text: "w1"},
		{Seq: 3, Level: "INFO", Text: "b"},
		{Seq: 4, Level: "ERROR", Text: "e1"},
		{Seq: 5, Level: "WARN", Text: "w2"},
	}
	s := SummarizeLogs(lines, 2)
	if s.Total != 5 || s.Info != 2 || s.Warn != 2 || s.Error != 1 {
		t.Fatalf("计数不对：%+v", s)
	}
	// 只留最近的 2 条告警/错误，且顺序保持时间递增
	if len(s.Recent) != 2 || s.Recent[0].Text != "e1" || s.Recent[1].Text != "w2" {
		t.Fatalf("最近的告警挑得不对：%+v", s.Recent)
	}
	if got := SummarizeLogs(nil, 0); got.Total != 0 || len(got.Recent) != 0 {
		t.Fatalf("空输入应当回空：%+v", got)
	}
}

func TestCountHealth(t *testing.T) {
	ok, warn, errs := CountHealth([]HealthItem{
		{Name: "a", Status: "ok"}, {Name: "b", Status: "warn"},
		{Name: "c", Status: "error"}, {Name: "d"}, // 空状态按 ok
	})
	if ok != 2 || warn != 1 || errs != 1 {
		t.Fatalf("健康计数不对：ok=%d warn=%d err=%d", ok, warn, errs)
	}
}
