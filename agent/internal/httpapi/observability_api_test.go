package httpapi_test

import (
	"testing"

	"baize/internal/observe"
)

func TestObservabilityEndpoint(t *testing.T) {
	e, _ := newAgentEnv(t)

	var rep observe.Report
	if code := e.do("GET", "/api/agent/observability?window=24h", nil, true, &rep); code != 200 {
		t.Fatalf("读观测面失败：HTTP %d", code)
	}
	if rep.Window != "24h" || rep.BucketMs != 3600*1000 {
		t.Fatalf("窗口不对：window=%q bucketMs=%d", rep.Window, rep.BucketMs)
	}
	if rep.GeneratedAt == 0 {
		t.Fatal("没给生成时间")
	}

	// 空环境：没有运行记录，但健康检查必须有内容
	if rep.Runs.Total != 0 {
		t.Fatalf("空环境不该有运行记录：%+v", rep.Runs)
	}
	if len(rep.Health) == 0 {
		t.Fatal("健康检查不能为空")
	}
	// 健康项按 error → warn → ok 排（界面直接照这个顺序画），且每项都得有名字与合法状态
	rank := map[string]int{"error": 0, "warn": 1, "ok": 2}
	last := -1
	hasModelErr := false
	for _, h := range rep.Health {
		if h.Name == "" {
			t.Fatalf("健康项没名字：%+v", h)
		}
		r, ok := rank[h.Status]
		if !ok {
			t.Fatalf("状态不认识：%q", h.Status)
		}
		if r < last {
			t.Fatalf("健康项没按 error→warn→ok 排序：%+v", rep.Health)
		}
		last = r
		if h.Name == "模型通道" && h.Status == "error" {
			hasModelErr = true
		}
	}
	// 没配模型时必须是 error —— 这是"真问题"，不许用绿灯糊过去
	if !hasModelErr {
		t.Fatalf("没配模型时应当有一条 error 级健康项：%+v", rep.Health)
	}

	// 工具/通道统计是空数组而不是 null（前端直接 .map，不用先判空）
	if rep.Tools == nil || rep.Providers == nil {
		t.Fatal("工具/通道统计应当是空数组而不是 null")
	}
	if rep.Buckets == nil {
		t.Fatal("分桶应当是空数组而不是 null")
	}

	// 坏档位 → 400（调用方的问题），不是 500
	if code := e.do("GET", "/api/agent/observability?window=999y", nil, true, nil); code != 400 {
		t.Fatalf("坏档位应当 400，实际 %d", code)
	}

	// all 档：不限时间，桶宽 1 天
	var all observe.Report
	if code := e.do("GET", "/api/agent/observability?window=all", nil, true, &all); code != 200 {
		t.Fatalf("all 档请求失败：HTTP %d", code)
	}
	if all.Window != "all" || all.SinceMs != 0 || all.BucketMs != 24*3600*1000 {
		t.Fatalf("all 档参数不对：%+v", all)
	}
}
