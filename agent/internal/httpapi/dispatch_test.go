package httpapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"baize/internal/hub"
	"baize/shared/proto"
)

// 跨端调度端到端：Agent 派活 → 设备中枢下发 → 设备回执 → Agent 拿到结果
func TestAgentDispatchToDevice(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-agent")
	e.waitDeviceOnline("pc-agent", true, 3*time.Second)

	// 1) 设备清单（调度决策依据）
	list, err := e.hub.ListDevices()
	if err != nil {
		t.Fatalf("列设备失败：%v", err)
	}
	found := false
	for _, d := range list {
		if d.ID == "pc-agent" && d.Online {
			found = true
		}
	}
	if !found {
		t.Fatalf("设备清单里应有在线设备：%+v", list)
	}

	// 2) 非危险动作：直接派发并等回执
	done := make(chan struct{})
	go func() {
		defer close(done)
		cmd := dev.waitCommand(3 * time.Second)
		if cmd.Action != proto.ActionFsStat {
			t.Errorf("设备收到的动作不对：%+v", cmd)
		}
		dev.receipt(cmd.TaskID, proto.ReceiptDone, map[string]any{"count": 2, "ok": true}, "")
	}()

	res, err := e.hub.Dispatch(context.Background(), "pc-agent", proto.ActionFsStat,
		map[string]any{"paths": []string{"D:\\tmp"}}, 5*time.Second)
	<-done
	if err != nil {
		t.Fatalf("派发失败：%v", err)
	}
	if res.Status != proto.TaskDone || res.TaskID == "" {
		t.Fatalf("回执不对：%+v", res)
	}
	if res.Output["count"] != float64(2) {
		t.Fatalf("结果没透传：%+v", res.Output)
	}

	// 3) 危险动作（经上层审批闸门后派发）：应带上审批记录，任务来源是 agent
	go func() {
		cmd := dev.waitCommand(3 * time.Second)
		dev.receipt(cmd.TaskID, proto.ReceiptDone, map[string]any{"ok": 1}, "")
	}()
	res2, err := e.hub.Dispatch(context.Background(), "pc-agent", proto.ActionFsDelete,
		map[string]any{"paths": []string{"D:\\tmp\\gone.txt"}}, 5*time.Second)
	if err != nil {
		t.Fatalf("危险动作派发失败：%v", err)
	}
	task, ok, err := e.store.Task(res2.TaskID)
	if err != nil || !ok {
		t.Fatalf("读任务失败：%v", err)
	}
	if task.Origin != "agent" {
		t.Fatalf("任务来源应记成 agent：%+v", task)
	}
	if task.ApprovedBy == "" {
		t.Fatalf("应记录审批来源：%+v", task)
	}
	if task.Status != proto.TaskDone {
		t.Fatalf("任务应完成：%+v", task)
	}

	// 4) 幂等：同设备 + 同动作 + 同幂等键 的在途任务会被复用
	arg := map[string]any{"paths": []string{"D:\\tmp\\same.txt"}}
	key := "idem-test-1"
	first, reused, err := e.hub.CreateTaskEx(hubCreate("pc-agent", proto.ActionFsDelete, arg, key))
	if err != nil || reused {
		t.Fatalf("首次创建不该复用：%v %v", reused, err)
	}
	// 上面这次会直接下发（带审批），设备没回执 → 处在"已下发"的在途状态
	second, reused2, err := e.hub.CreateTaskEx(hubCreate("pc-agent", proto.ActionFsDelete, arg, key))
	if err != nil {
		t.Fatalf("二次创建失败：%v", err)
	}
	if !reused2 || second.ID != first.ID {
		t.Fatalf("同键在途任务应被复用：first=%s second=%s reused=%v", first.ID, second.ID, reused2)
	}
	// 换个键就应该新建
	third, reused3, err := e.hub.CreateTaskEx(hubCreate("pc-agent", proto.ActionFsDelete, arg, "idem-test-2"))
	if err != nil || reused3 || third.ID == first.ID {
		t.Fatalf("不同键应新建任务：%+v reused=%v err=%v", third, reused3, err)
	}

	// 5) 设备离线：任务排队，等待超时要明确报错（不能假装成功）
	dev.close()
	e.waitDeviceOnline("pc-agent", false, 3*time.Second)
	timedOut, err := e.hub.Dispatch(context.Background(), "pc-agent", proto.ActionPing, nil, 700*time.Millisecond)
	if err == nil {
		t.Fatal("设备离线时等待应超时")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("应明确报超时：%v", err)
	}
	if timedOut.Status != proto.TaskQueued {
		t.Fatalf("离线设备的任务应保持排队：%+v", timedOut)
	}
}

// hubCreate 构造一个"已审批"的跨端派发选项
func hubCreate(deviceID, action string, args map[string]any, idemKey string) hub.CreateOptions {
	return hub.CreateOptions{
		DeviceID: deviceID, Action: action, Args: args,
		Origin: "agent", ApprovedBy: "agent-approval-gate", IdemKey: idemKey,
	}
}
