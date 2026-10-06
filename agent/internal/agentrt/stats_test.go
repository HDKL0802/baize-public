package agentrt

import (
	"testing"
	"time"
)

// 观测面靠这两个查询：窗口内的运行汇总（次数/成败/token/耗时分位）与分桶（画柱子）。
func TestRunSummaryAndBuckets(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("打开运行库失败：%v", err)
	}
	defer st.Close()

	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC).UnixMilli()
	hour := time.Hour.Milliseconds()

	save := func(id, status string, steps, tools, retries, ptok, otok int, started, dur int64) {
		t.Helper()
		if err := st.SaveRun(RunRecord{
			RunID: id, Status: status, Steps: steps, ToolCalls: tools, Retries: retries,
			PromptTokens: ptok, OutTokens: otok, StartedAt: started, FinishedAt: started + dur,
		}); err != nil {
			t.Fatalf("落库失败：%v", err)
		}
	}
	save("r1", "done", 3, 2, 0, 100, 50, base, 1000)
	save("r2", "failed", 5, 4, 2, 200, 80, base+hour, 3000)
	save("r3", "done", 1, 0, 0, 10, 5, base+hour+10, 100)

	sum, err := st.RunSummary(0)
	if err != nil {
		t.Fatalf("汇总失败：%v", err)
	}
	if sum.Total != 3 || sum.Done != 2 || sum.Failed != 1 {
		t.Fatalf("次数不对：%+v", sum)
	}
	if sum.Steps != 9 || sum.ToolCalls != 6 || sum.Retries != 2 {
		t.Fatalf("步数/工具/重试不对：%+v", sum)
	}
	if sum.PromptTokens != 310 || sum.OutTokens != 135 {
		t.Fatalf("token 不对：%+v", sum)
	}
	if sum.AvgMs != 1366 || sum.MaxMs != 3000 || sum.P95Ms != 3000 {
		t.Fatalf("耗时不正确：avg=%d max=%d p95=%d", sum.AvgMs, sum.MaxMs, sum.P95Ms)
	}
	if sum.LastAt != base+hour+10 {
		t.Fatalf("最近一次应当是最新的那条：%d", sum.LastAt)
	}

	// 时间窗：只看第二小时起（r2、r3）
	win, err := st.RunSummary(base + hour)
	if err != nil {
		t.Fatalf("按窗口汇总失败：%v", err)
	}
	if win.Total != 2 || win.Failed != 1 {
		t.Fatalf("时间窗没生效：%+v", win)
	}

	buckets, err := st.RunBuckets(0, hour)
	if err != nil {
		t.Fatalf("分桶失败：%v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("应当有 2 个桶：%+v", buckets)
	}
	if buckets[0].At != base || buckets[0].Runs != 1 || buckets[0].Failed != 0 || buckets[0].Tokens != 150 {
		t.Fatalf("第 1 个桶不对：%+v", buckets[0])
	}
	if buckets[1].At != base+hour || buckets[1].Runs != 2 || buckets[1].Failed != 1 || buckets[1].Tokens != 295 {
		t.Fatalf("第 2 个桶不对：%+v", buckets[1])
	}
}

// 没有运行记录时应当回零值而不是错误（界面开屏就是这个状态）
func TestRunSummaryEmpty(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("打开运行库失败：%v", err)
	}
	defer st.Close()

	sum, err := st.RunSummary(0)
	if err != nil {
		t.Fatalf("空库汇总不该报错：%v", err)
	}
	if sum.Total != 0 || sum.AvgMs != 0 || sum.P95Ms != 0 || sum.LastAt != 0 {
		t.Fatalf("空库应当回零值：%+v", sum)
	}
	buckets, err := st.RunBuckets(0, time.Hour.Milliseconds())
	if err != nil || len(buckets) != 0 {
		t.Fatalf("空库不该有桶：%+v %v", buckets, err)
	}
}
