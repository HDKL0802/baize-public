package httpapi_test

import (
	"strings"
	"testing"
)

/* 心跳接口测试（QwenPaw heartbeat 机制的 Go 版）。
   关注四件事：状态/文件读得到；配置校验挡得住坏值（表达式、target、活跃时段、超时）；
   保存与手动触发走通；没挂 Agent 时不注册。 */

type heartbeatStateView struct {
	Enabled     bool   `json:"enabled"`
	Every       string `json:"every"`
	Target      string `json:"target"`
	TimeoutSec  int    `json:"timeoutSec"`
	ActiveHours string `json:"activeHours"`
	NextAt      int64  `json:"nextAt"`
	Path        string `json:"path"`
	HasFile     bool   `json:"hasFile"`
	Size        int64  `json:"size"`
	LastStatus  string `json:"lastStatus"`
	LastError   string `json:"lastError"`
}

type heartbeatResp struct {
	State   heartbeatStateView `json:"state"`
	Targets []string           `json:"targets"`
	RunID   string             `json:"runId"`
}

type heartbeatFileResp struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

func TestHeartbeatAPI(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 初始状态：未启用、没有 HEARTBEAT.md，target 清单齐
	var base heartbeatResp
	if code := e.do("GET", "/api/agent/heartbeat", nil, true, &base); code != 200 {
		t.Fatalf("GET /api/agent/heartbeat 期望 200，实际 %d", code)
	}
	if base.State.HasFile {
		t.Fatalf("初始不该有 HEARTBEAT.md：%+v", base.State)
	}
	joined := strings.Join(base.Targets, ",")
	for _, want := range []string{"main", "last", "inbox"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("target 清单应包含 %s：%v", want, base.Targets)
		}
	}

	// 文件不存在是正常状态（exists=false），不是 404
	var nofile heartbeatFileResp
	if code := e.do("GET", "/api/agent/heartbeat/file", nil, true, &nofile); code != 200 {
		t.Fatalf("读文件期望 200，实际 %d", code)
	}
	if nofile.Exists {
		t.Fatalf("初始 exists 应为 false：%+v", nofile)
	}

	// 没有 HEARTBEAT.md 时手动触发要明确报错，不能拿空查询去跑
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "run"}, true, nil); code != 400 {
		t.Fatalf("无文件时 run 期望 400，实际 %d", code)
	}

	// 保存 HEARTBEAT.md
	var saved heartbeatResp
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "save", "content": "每次心跳：看看收件箱有没有急事。"}, true, &saved); code != 200 {
		t.Fatalf("保存期望 200，实际 %d", code)
	}
	if !saved.State.HasFile || saved.State.Size == 0 {
		t.Fatalf("保存后状态里应看到文件：%+v", saved.State)
	}
	var got heartbeatFileResp
	if code := e.do("GET", "/api/agent/heartbeat/file", nil, true, &got); code != 200 {
		t.Fatalf("读文件期望 200，实际 %d", code)
	}
	if !got.Exists || !strings.Contains(got.Content, "收件箱") {
		t.Fatalf("读回的内容不对：%+v", got)
	}
	// 空内容要挡：不想让心跳干活请关开关，而不是清空文件
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "save", "content": "   "}, true, nil); code != 400 {
		t.Fatalf("空内容期望 400，实际 %d", code)
	}

	// 配置校验：坏值一律 400，不让 normalize 悄悄"纠正"
	bad := []map[string]any{
		{"action": "config", "every": "* * * *"},                    // 坏表达式
		{"action": "config", "every": "@every 1ms"},                 // 间隔过小
		{"action": "config", "target": "lasst"},                     // 坏 target
		{"action": "config", "activeHours": "8点到22点"},               // 坏时段
		{"action": "config", "activeHours": "25:00-26:00"},          // 越界时段
		{"action": "config", "timeoutSec": -1},                      // 负超时
	}
	for _, body := range bad {
		if code := e.do("POST", "/api/agent/heartbeat", body, true, nil); code != 400 {
			t.Fatalf("坏配置 %+v 期望 400，实际 %d", body, code)
		}
	}

	// 合法配置：立即回显（状态里的配置字段是实时读的）
	var cfgd heartbeatResp
	if code := e.do("POST", "/api/agent/heartbeat", map[string]any{
		"action": "config", "enabled": true, "every": "@every 30m",
		"target": "last", "timeoutSec": 60, "activeHours": "08:00-22:00",
	}, true, &cfgd); code != 200 {
		t.Fatalf("合法配置期望 200，实际 %d", code)
	}
	st := cfgd.State
	if !st.Enabled || st.Every != "@every 30m" || st.Target != "last" ||
		st.TimeoutSec != 60 || st.ActiveHours != "08:00-22:00" {
		t.Fatalf("配置回显不对：%+v", st)
	}

	// 活跃时段可以清空（= 全天）
	var cleared heartbeatResp
	empty := ""
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "config", "activeHours": &empty}, true, &cleared); code != 200 {
		t.Fatalf("清空活跃时段期望 200，实际 %d", code)
	}
	if cleared.State.ActiveHours != "" {
		t.Fatalf("清空后应为全天：%+v", cleared.State)
	}

	// 手动触发：拿到 runId（后台真跑一次；测试环境没有模型，跑失败也算触发成功）
	var ran heartbeatResp
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "run"}, true, &ran); code != 200 {
		t.Fatalf("run 期望 200，实际 %d", code)
	}
	if ran.RunID == "" {
		t.Fatalf("run 应带回 runId：%+v", ran)
	}

	// 未知 action
	if code := e.do("POST", "/api/agent/heartbeat",
		map[string]any{"action": "wat"}, true, nil); code != 400 {
		t.Fatalf("未知 action 期望 400，实际 %d", code)
	}
}

// 没挂 Agent 服务时心跳接口不该注册
func TestHeartbeatAbsentWithoutAgent(t *testing.T) {
	e := newEnv(t, 5e9)
	if code := e.do("GET", "/api/agent/heartbeat", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/heartbeat 不该可用，实际 %d", code)
	}
}
