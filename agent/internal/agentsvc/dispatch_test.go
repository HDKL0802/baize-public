package agentsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"baize/internal/config"
	"baize/internal/tools"
)

type fakeDeviceExec struct {
	devices []tools.DeviceBrief
	res     tools.DeviceResult
	err     error
	calls   []string
}

func (f *fakeDeviceExec) ListDevices() ([]tools.DeviceBrief, error) { return f.devices, nil }

func (f *fakeDeviceExec) Dispatch(_ context.Context, deviceID, action string, _ map[string]any, _ time.Duration) (tools.DeviceResult, error) {
	f.calls = append(f.calls, deviceID+"/"+action)
	res := f.res
	res.DeviceID = deviceID
	res.Action = action
	return res, f.err
}

// 跨端调度：Agent 自己决定把活派到另一台设备，结果回流并落进长期记忆
func TestCrossDeviceDispatchThroughAgent(t *testing.T) {
	f := newFakeLLM(t,
		func(map[string]any) map[string]any { return toolBody("c1", "device_list", map[string]any{}) },
		func(map[string]any) map[string]any {
			return toolBody("c2", "device_run", map[string]any{
				"deviceId": "pc-1", "action": "fs.stat",
				"args": map[string]any{"paths": []any{"D:\\tmp"}},
			})
		},
		func(map[string]any) map[string]any { return sayBody("远端查好了：D:\\tmp 里有 1 个文件") },
	)
	exec := &fakeDeviceExec{
		devices: []tools.DeviceBrief{{ID: "pc-1", Name: "台式机", OS: "windows", Arch: "amd64", Online: true, Caps: []string{"fs"}}},
		res:     tools.DeviceResult{TaskID: "task-1", Status: "done", Output: map[string]any{"count": 1}},
	}

	dir := t.TempDir()
	s, err := New(dir, testLogger(), WithDevices(exec))
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	defer s.Close()
	cfg := s.Config()
	cfg.Providers = []config.Provider{{Name: "fake", Protocol: "openai", BaseURL: f.srv.URL + "/v1", Model: "m"}}
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("保存配置失败：%v", err)
	}

	res, err := s.Run(context.Background(), "看看那台电脑上 D:\\tmp 里有什么", "chat", "loose")
	if err != nil {
		t.Fatalf("运行失败：%v", err)
	}
	if res.ToolCalls != 2 {
		t.Fatalf("应调用两个工具：%+v", res.Trace)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "pc-1/fs.stat" {
		t.Fatalf("跨端派发目标不对：%+v", exec.calls)
	}
	if !strings.Contains(res.Text, "远端") {
		t.Fatalf("结论应来自远端结果：%q", res.Text)
	}

	// 结果统一落记忆
	hits, err := s.Memory().Search("跨端任务 fs.stat", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("跨端结果应写进长期记忆：%v %+v", err, hits)
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Chunk.Title, "跨端任务") && strings.Contains(h.Chunk.Content, "pc-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("记忆内容不对：%+v", hits)
	}

	// 系统提示里要带上"可调度设备"，模型才知道有别的端可用
	f.mu.Lock()
	joined, _ := json.Marshal(f.bodies[0]["messages"])
	f.mu.Unlock()
	if !strings.Contains(string(joined), "可调度设备") || !strings.Contains(string(joined), "台式机") {
		t.Fatalf("系统提示应带设备清单：%s", string(joined))
	}
	// 工具清单里也要有 device.*
	if !strings.Contains(string(joined), "device_run") {
		t.Fatalf("工具说明里应有 device_run：%s", string(joined))
	}

	// 没有设备中枢时不该注册这些工具（避免"看起来能派活"）
	s2 := newService(t, f, nil)
	reg := tools.NewRegistry()
	tools.RegisterDevices(reg, nil, nil)
	if _, ok := reg.Get("device_run"); ok {
		t.Fatal("没有设备中枢时不该注册 device_run")
	}
	if st := s2.State(); st.Workdir == "" {
		t.Fatal("状态里应有工作目录")
	}
}
