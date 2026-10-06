package httpapi_test

import (
	"testing"

	"baize/internal/store"
)

// 设备删除：硬删（含备注）；删完 /api/state 里不该再有它；删不存在的 id 不该 500。
//
// 用同包的 newAgentEnv 而不是 newKBEnv：删除接口注册在 registerAgent 里，
// 只有挂了 Agent 服务才有这条路由；newAgentEnv 同时内嵌了 *env，能直接拿到 store 造设备。
func TestDeleteDevice(t *testing.T) {
	e, _ := newAgentEnv(t)

	dev := store.Device{
		ID: "dev-to-delete", Name: "待删设备", OS: "windows", Arch: "amd64",
		Hostname: "test-host", Online: false,
	}
	if err := e.store.UpsertDevice(dev); err != nil {
		t.Fatalf("造设备失败：%v", err)
	}

	has := func(id string) bool {
		for _, d := range e.state().Devices {
			if d.ID == id {
				return true
			}
		}
		return false
	}
	if !has(dev.ID) {
		t.Fatalf("设备没造出来，/api/state 里找不到：%s", dev.ID)
	}

	var out struct {
		OK      bool   `json:"ok"`
		Removed string `json:"removed"`
	}
	if code := e.do("DELETE", "/api/agent/devices/"+dev.ID, nil, true, &out); code != 200 {
		t.Fatalf("删除设备失败，HTTP %d", code)
	}
	if !out.OK || out.Removed != dev.ID {
		t.Fatalf("删除响应不对：%+v", out)
	}
	if has(dev.ID) {
		t.Fatalf("删完设备还留在 /api/state 里：%s", dev.ID)
	}

	// 删不存在的 id：不该 500（我们的实现是 200）
	if code := e.do("DELETE", "/api/agent/devices/no-such-device", nil, true, nil); code == 500 {
		t.Fatalf("删不存在的设备不该 500，实际 HTTP %d", code)
	}
}
