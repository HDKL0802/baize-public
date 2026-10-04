package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func newDeviceSvc(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(t.TempDir(), "test-token")
	if err != nil {
		t.Fatalf("创建服务失败：%v", err)
	}
	return svc
}

func addTodoForTest(t *testing.T, svc *Service, args map[string]any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Do(OpRequest{Op: "todo.add", Args: raw}); err != nil {
		t.Fatalf("加待办失败：%v", err)
	}
}

// 手机内核对后端只提供只读动作：ping / sys.info / todo.list / todo.stats
func TestRunDeviceActionReadOnly(t *testing.T) {
	svc := newDeviceSvc(t)

	if len(DeviceActionList()) == 0 || len(DeviceCaps()) == 0 {
		t.Fatal("应声明本端支持的动作与能力")
	}

	// ping
	res, why := svc.RunDeviceAction("ping", nil)
	if why != "" || res["pong"] != true {
		t.Fatalf("ping 结果不对：%v %v", res, why)
	}

	// sys.info
	res, why = svc.RunDeviceAction("sys.info", nil)
	if why != "" {
		t.Fatalf("sys.info 失败：%s", why)
	}
	for _, k := range []string{"os", "arch", "version", "dataDir", "todos"} {
		if _, ok := res[k]; !ok {
			t.Fatalf("sys.info 少了字段 %s：%+v", k, res)
		}
	}

	// 未知动作必须明确报错，并列出支持的动作
	if _, why := svc.RunDeviceAction("fs.delete", nil); why == "" {
		t.Fatal("不支持的动作必须明确报错")
	} else if !strings.Contains(why, "不支持") || !strings.Contains(why, "todo.list") {
		t.Fatalf("错误信息应说明支持哪些动作：%s", why)
	}
	// 手机端不提供任何密码本动作（密码不出本机）
	if _, why := svc.RunDeviceAction("vault.list", nil); why == "" {
		t.Fatal("密码本相关动作必须拒绝（密码不出本机）")
	}
}

func TestDeviceTodoListAndStats(t *testing.T) {
	svc := newDeviceSvc(t)

	// 空数据
	res, why := svc.RunDeviceAction("todo.list", nil)
	if why != "" || res["count"] != 0 {
		t.Fatalf("空数据应返回 0 条：%v %v", res, why)
	}

	addTodoForTest(t, svc, map[string]any{"title": "写周报", "category": "工作", "due": "2030-01-02T10:00", "priority": "high"})
	addTodoForTest(t, svc, map[string]any{"title": "买牛奶", "category": "生活", "form": "leisure"})
	addTodoForTest(t, svc, map[string]any{"title": "过期的活", "due": "2020-01-01T09:00"})

	// 全量
	res, why = svc.RunDeviceAction("todo.list", map[string]any{"limit": 10})
	if why != "" {
		t.Fatalf("列待办失败：%s", why)
	}
	if res["matched"] != 3 || res["count"] != 3 {
		t.Fatalf("应列出 3 条：%+v", res)
	}
	todos, _ := res["todos"].([]map[string]any)
	if len(todos) != 3 {
		t.Fatalf("todos 结构不对：%+v", res["todos"])
	}
	// 排序：有截止时间的在前，最早的排最前
	if todos[0]["title"] != "过期的活" || todos[2]["title"] != "买牛奶" {
		t.Fatalf("排序不对（有 due 的在前、早的在前、无 due 的后）：%+v", todos)
	}

	// 关键词过滤
	res, _ = svc.RunDeviceAction("todo.list", map[string]any{"keyword": "牛奶"})
	if res["matched"] != 1 {
		t.Fatalf("关键词过滤不对：%+v", res)
	}

	// limit 截断要明确标出来
	res, _ = svc.RunDeviceAction("todo.list", map[string]any{"limit": 2})
	if res["count"] != 2 || res["truncated"] != true || res["matched"] != 3 {
		t.Fatalf("截断标记不对：%+v", res)
	}

	// 统计
	res, why = svc.RunDeviceAction("todo.stats", nil)
	if why != "" {
		t.Fatalf("统计失败：%s", why)
	}
	if res["total"] != 3 || res["overdue"] != 1 || res["sched"] != 2 || res["leisure"] != 1 {
		t.Fatalf("统计数据不对：%+v", res)
	}
}

// 手机把本机待办整表推给内核后，跨端 todo.list / todo.stats 必须读到这份真数据；
// 整表替换意味着删掉的待办不能留在内核里变成幽灵条目。
func TestTodoReplaceAllFeedsDeviceList(t *testing.T) {
	svc := newDeviceSvc(t)
	addTodoForTest(t, svc, map[string]any{"title": "内核里原本就有的"})

	push := func(t *testing.T, todos []map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"todos": todos})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Do(OpRequest{Op: "todo.replaceAll", Args: raw}); err != nil {
			t.Fatalf("整表替换失败：%v", err)
		}
	}

	push(t, []map[string]any{
		{"id": "a1", "title": "手机上的活", "due": "2030-01-02T10:00", "owner": "user", "status": "todo"},
		{"id": "a2", "title": "手机上删掉的活", "form": "leisure", "owner": "user", "status": "todo"},
	})
	push(t, []map[string]any{
		{"id": "a1", "title": "手机上的活", "due": "2030-01-02T10:00", "owner": "user", "status": "done"},
	})

	res, why := svc.RunDeviceAction("todo.list", nil)
	if why != "" {
		t.Fatalf("列待办失败：%s", why)
	}
	if res["matched"] != 1 || res["count"] != 1 {
		t.Fatalf("整表替换后应只剩 1 条（删掉的不能留下）：%+v", res)
	}
	todos, _ := res["todos"].([]map[string]any)
	if len(todos) != 1 || todos[0]["title"] != "手机上的活" || todos[0]["status"] != "done" {
		t.Fatalf("推过来的待办没落进内核：%+v", todos)
	}

	// 重新打开一次服务，确认是落盘了而不是只在内存里
	svc2, err := NewService(svc.store.dir, "test-token")
	if err != nil {
		t.Fatalf("重开服务失败：%v", err)
	}
	res, _ = svc2.RunDeviceAction("todo.stats", nil)
	if res["total"] != 1 || res["done"] != 1 {
		t.Fatalf("重启后数据丢了：%+v", res)
	}
}

// 参数容错：给乱七八糟的类型也不能 panic，取不到就用默认值
func TestDeviceActionArgTolerance(t *testing.T) {
	svc := newDeviceSvc(t)
	addTodoForTest(t, svc, map[string]any{"title": "只有一条"})

	res, why := svc.RunDeviceAction("todo.list", map[string]any{
		"limit": map[string]any{"bad": true}, "keyword": 123, "owner": nil,
	})
	if why != "" {
		t.Fatalf("参数类型不对时应走默认值而不是报错：%s", why)
	}
	if res["count"] != 1 {
		t.Fatalf("默认 limit 应能列出这 1 条：%+v", res)
	}
}
