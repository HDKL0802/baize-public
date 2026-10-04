package httpapi_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"baize/shared/client"
	"baize/shared/proto"
)

// 跨端调度的"手机端"用例：用各端共用的客户端（baize/shared/client，手机内核用的就是这一份）
// 连真后端 → 注册 → 收指令 → 执行 → 回执。
//
// 这条用例同时钉住两件事：
//  1. 共享协议在"端"与"后端"两侧是真的一致（编译期 + 运行期都过一遍）
//  2. 手机内核那种"只读设备动作"（todo.list）能端到端跑通
func TestSharedDeviceClientRegistersAndExecutes(t *testing.T) {
	e := newEnv(t, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen atomic.Value // 记下收到的动作名
	handler := func(cmd proto.Command) proto.Receipt {
		seen.Store(cmd.Action)
		if cmd.Action != proto.ActionTodoList {
			return proto.Receipt{TaskID: cmd.TaskID, Status: proto.ReceiptFailed, Error: "本端只支持 todo.list"}
		}
		return proto.Receipt{
			TaskID: cmd.TaskID, Status: proto.ReceiptDone,
			Result: map[string]any{"count": 2, "source": "phone", "todos": []any{
				map[string]any{"id": "t1", "title": "写周报"},
				map[string]any{"id": "t2", "title": "买牛奶"},
			}},
		}
	}

	go func() {
		_ = client.Run(ctx, client.Config{
			URL:   e.srv.URL,
			Token: testToken,
			Info: proto.Hello{
				DeviceID: "phone-1", Name: "测试手机", OS: "android", Arch: "arm64",
				Version: "0.6.0", Caps: []string{proto.CapTodo},
			},
			Heartbeat: time.Second,
		}, handler, nil)
	}()

	e.waitDeviceOnline("phone-1", true, 5*time.Second)

	// 设备能力要能在注册表里看到（调度决策依据）
	list, err := e.hub.ListDevices()
	if err != nil {
		t.Fatalf("列设备失败：%v", err)
	}
	found := false
	for _, d := range list {
		if d.ID != "phone-1" {
			continue
		}
		found = true
		if d.OS != "android" {
			t.Fatalf("设备平台不对：%+v", d)
		}
		if !contains(d.Caps, proto.CapTodo) {
			t.Fatalf("设备能力里应有 todo：%+v", d.Caps)
		}
	}
	if !found {
		t.Fatalf("注册表里没有这台设备：%+v", list)
	}

	// 派一条只读动作过去
	task := e.createTask("phone-1", proto.ActionTodoList, map[string]any{"limit": 5})
	final := e.waitTaskStatus(task.ID, proto.TaskDone, 5*time.Second)
	if final.Result["source"] != "phone" {
		t.Fatalf("回执没透传：%+v", final.Result)
	}
	if final.Result["count"] != float64(2) {
		t.Fatalf("回执数字被转成了别的类型：%#v", final.Result["count"])
	}
	if got, _ := seen.Load().(string); got != proto.ActionTodoList {
		t.Fatalf("客户端收到的动作不对：%q", got)
	}

	// 客户端不支持的动作：要回"失败"回执，而不是假装成功
	task2 := e.createTask("phone-1", proto.ActionWindowNow, map[string]any{})
	final2 := e.waitTaskStatus(task2.ID, proto.TaskFailed, 5*time.Second)
	if !strings.Contains(final2.Error, "只支持 todo.list") {
		t.Fatalf("失败原因应透传回来：%q", final2.Error)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
