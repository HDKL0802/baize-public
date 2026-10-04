package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeExecutor struct {
	devices  []DeviceBrief
	result   DeviceResult
	err      error
	calls    []string
	recvArgs map[string]any
}

func (f *fakeExecutor) ListDevices() ([]DeviceBrief, error) { return f.devices, nil }

func (f *fakeExecutor) Dispatch(_ context.Context, deviceID, action string, args map[string]any, _ time.Duration) (DeviceResult, error) {
	f.calls = append(f.calls, deviceID+"/"+action)
	f.recvArgs = args
	res := f.result
	res.DeviceID = deviceID
	res.Action = action
	if res.TaskID == "" {
		res.TaskID = "t1"
	}
	return res, f.err
}

func TestDeviceList(t *testing.T) {
	exec := &fakeExecutor{devices: []DeviceBrief{
		{ID: "pc-b", Name: "B 机", OS: "windows", Arch: "amd64", Online: false, Caps: []string{"fs"}},
		{ID: "pc-a", Name: "A 机", OS: "windows", Arch: "amd64", Online: true, Caps: []string{"window.report"}},
	}}
	reg := NewRegistry()
	RegisterDevices(reg, exec, nil)

	out, err := reg.Call(context.Background(), "device_list", nil)
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	m := out.(map[string]any)
	if m["count"].(int) != 2 {
		t.Fatalf("设备数不对：%+v", m)
	}
	// 在线设备排前面
	first := m["devices"].([]DeviceBrief)[0]
	if first.ID != "pc-a" || !first.Online {
		t.Fatalf("在线设备应排在前面：%+v", first)
	}
	if !reg.IsDangerous("device_run") || reg.IsDangerous("device_list") {
		t.Fatal("device_run 应危险、device_list 不应危险")
	}
}

func TestDeviceRunSuccessAndRecorder(t *testing.T) {
	exec := &fakeExecutor{result: DeviceResult{
		TaskID: "task-9", Status: "done", Output: map[string]any{"ok": 1},
	}}
	var recorded []DeviceResult
	reg := NewRegistry()
	RegisterDevices(reg, exec, func(res DeviceResult) { recorded = append(recorded, res) })

	out, err := reg.Call(context.Background(), "device_run", map[string]any{
		"deviceId": "pc-a", "action": "fs.stat", "args": map[string]any{"paths": []any{"D:\\tmp\\a.txt"}},
	})
	if err != nil {
		t.Fatalf("调用失败：%v", err)
	}
	m := out.(map[string]any)
	if m["status"] != "done" || m["taskId"] != "task-9" {
		t.Fatalf("返回不对：%+v", m)
	}
	if len(exec.calls) != 1 || exec.calls[0] != "pc-a/fs.stat" {
		t.Fatalf("派发目标不对：%+v", exec.calls)
	}
	if exec.recvArgs["paths"] == nil {
		t.Fatalf("参数没有透传：%+v", exec.recvArgs)
	}
	if len(recorded) != 1 || recorded[0].Status != "done" {
		t.Fatalf("结果应被记录（落记忆用）：%+v", recorded)
	}
}

func TestDeviceRunFailureAndValidation(t *testing.T) {
	exec := &fakeExecutor{result: DeviceResult{TaskID: "t2", Status: "failed", Error: "拒绝删除"}, err: errors.New("设备执行失败")}
	var recorded []DeviceResult
	reg := NewRegistry()
	RegisterDevices(reg, exec, func(res DeviceResult) { recorded = append(recorded, res) })

	// 即使执行失败，也要把回执交给记录器（失败同样要落记忆）
	if _, err := reg.Call(context.Background(), "device_run", map[string]any{"deviceId": "pc-a", "action": "fs.delete"}); err == nil {
		t.Fatal("失败时应返回错误")
	}
	if len(recorded) != 1 || recorded[0].Status != "failed" {
		t.Fatalf("失败回执也应被记录：%+v", recorded)
	}

	if _, err := reg.Call(context.Background(), "device_run", map[string]any{"deviceId": "", "action": "ping"}); err == nil {
		t.Fatal("缺 deviceId 应报错")
	}
	if _, err := reg.Call(context.Background(), "device_run", map[string]any{"deviceId": "pc-a", "action": ""}); err == nil {
		t.Fatal("缺 action 应报错")
	}
	if _, err := reg.Call(context.Background(), "device_run", map[string]any{
		"deviceId": "pc-a", "action": "ping", "args": "不是对象",
	}); err == nil || !strings.Contains(err.Error(), "必须是对象") {
		t.Fatalf("args 类型错误应明确报错：%v", err)
	}
}

func TestDeviceToolsWithoutExecutor(t *testing.T) {
	reg := NewRegistry()
	RegisterDevices(reg, nil, nil)
	if _, ok := reg.Get("device_run"); ok {
		t.Fatal("没有设备中枢时不该注册设备工具")
	}
	// 直接构造也不该崩，而是明确报错
	if _, err := NewDeviceRun(nil, nil).Run(context.Background(), map[string]any{"deviceId": "x", "action": "ping"}); err == nil ||
		!strings.Contains(err.Error(), "没有接入设备中枢") {
		t.Fatalf("应明确说明没有设备中枢：%v", err)
	}
}

func TestDeviceCapabilitiesHint(t *testing.T) {
	hint := DeviceCapabilitiesHint([]DeviceBrief{
		{Name: "台式机", OS: "windows", Arch: "amd64", Online: true, Caps: []string{"fs", "fs.delete"}},
		{Name: "手机", OS: "android", Arch: "arm64", Online: false},
	})
	for _, want := range []string{"已注册设备 2 台", "在线 1", "台式机", "手机", "离线"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("提示缺少 %q：%s", want, hint)
		}
	}
	if DeviceCapabilitiesHint(nil) != "" {
		t.Fatal("没有设备时应返回空串")
	}
}
