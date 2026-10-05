package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

/* 频道接口测试（QwenPaw 频道机制的 Go 版）。
   关注：清单广播；坏值（类型/格式/id/地址）一律门口拒绝；入站回调的令牌校验（404/409/401）；
   正确令牌真的跑一次 Agent 并回发出站；测试发送 / 启停 / 删除；没挂 Agent 时不注册。 */

type channelInfo struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Enabled  bool     `json:"enabled"`
	Format   string   `json:"format"`
	HasToken bool     `json:"hasToken"`
	ChatIDs  []string `json:"chatIds"`
	PollSec  int      `json:"pollSec"`
}

type channelsResp struct {
	Channels []channelInfo `json:"channels"`
	Kinds    []string      `json:"kinds"`
	Formats  []string      `json:"formats"`
}

func hasStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestChannelsAPI(t *testing.T) {
	e, _ := newAgentEnv(t)

	var base channelsResp
	if code := e.do("GET", "/api/agent/channels", nil, true, &base); code != 200 {
		t.Fatalf("GET /api/agent/channels 期望 200，实际 %d", code)
	}
	if len(base.Channels) != 0 {
		t.Fatalf("初始不该有频道：%+v", base.Channels)
	}
	if !hasStr(base.Kinds, "webhook") || !hasStr(base.Formats, "feishu") {
		t.Fatalf("类型 / 格式清单不全：%+v %+v", base.Kinds, base.Formats)
	}

	// 坏值一律 400（别让 normalize 悄悄"纠正"成合法值）
	bad := []map[string]any{
		{"action": "save", "id": "c1", "kind": "nope", "outboundUrl": "http://127.0.0.1:1/x"},
		{"action": "save", "id": "c1", "format": "nope", "outboundUrl": "http://127.0.0.1:1/x"},
		{"action": "save", "id": "c1"}, // 缺 outboundUrl
		{"action": "save", "id": "bad id", "outboundUrl": "http://127.0.0.1:1/x"},
		{"action": "save", "id": "c1", "outboundUrl": "ftp://x/y"},
		{"action": "wat", "id": "c1"},
	}
	for _, b := range bad {
		if code := e.do("POST", "/api/agent/channels", b, true, nil); code != 400 {
			t.Fatalf("坏请求应 400：%+v 实际 %d", b, code)
		}
	}

	// 出站接收端
	outCh := make(chan string, 4)
	out := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case outCh <- string(b):
		default:
		}
		w.WriteHeader(200)
	}))
	defer out.Close()

	// 合法保存
	var saved channelsResp
	if code := e.do("POST", "/api/agent/channels",
		map[string]any{"action": "save", "id": "c1", "kind": "webhook", "format": "feishu", "token": "sec", "outboundUrl": out.URL},
		true, &saved); code != 200 {
		t.Fatalf("保存频道期望 200，实际 %d", code)
	}
	if len(saved.Channels) != 1 || !saved.Channels[0].HasToken || saved.Channels[0].Format != "feishu" {
		t.Fatalf("保存结果不对：%+v", saved.Channels)
	}

	// 入站校验：不存在 → 404；没设令牌 → 409；令牌不对 → 401
	if code := e.do("POST", "/api/channels/nope/inbound", map[string]any{"text": "hi"}, false, nil); code != 404 {
		t.Fatalf("不存在频道应 404，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "save", "id": "c2", "outboundUrl": out.URL}, true, nil); code != 200 {
		t.Fatalf("保存 c2 失败：%d", code)
	}
	if code := e.do("POST", "/api/channels/c2/inbound", map[string]any{"text": "hi"}, false, nil); code != 409 {
		t.Fatalf("没设令牌应 409，实际 %d", code)
	}
	if code := e.do("POST", "/api/channels/c1/inbound?token=wrong", map[string]any{"text": "hi"}, false, nil); code != 401 {
		t.Fatalf("令牌不对应 401，实际 %d", code)
	}

	// 正确令牌 → 202；配个假模型后真的跑一次 Agent，并回发到出站地址
	openai := fakeOpenAI(t)
	if code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "local", "protocol": "openai", "baseUrl": openai.URL + "/v1", "model": "fake-openai", "apiKey": "sk-x"},
	}, true, nil); code != 200 {
		t.Fatalf("配模型失败：%d", code)
	}
	if code := e.do("POST", "/api/channels/c1/inbound?token=sec", map[string]any{"text": "帮我看看", "sender": "u1"}, false, nil); code != 202 {
		t.Fatalf("入站应 202，实际 %d", code)
	}
	select {
	case body := <-outCh:
		if !strings.Contains(body, "能通") {
			t.Fatalf("回发内容应为假模型回复：%s", body)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("没等到频道回发")
	}

	// 测试发送
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "test", "id": "c1", "text": "连通性"}, true, nil); code != 200 {
		t.Fatalf("test 应 200，实际 %d", code)
	}
	// 停用后不再接受入站
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "toggle", "id": "c1"}, true, nil); code != 200 {
		t.Fatalf("toggle 应 200，实际 %d", code)
	}
	if code := e.do("POST", "/api/channels/c1/inbound?token=sec", map[string]any{"text": "hi"}, false, nil); code != 409 {
		t.Fatalf("停用后入站应 409，实际 %d", code)
	}
	// 删除
	var after channelsResp
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "remove", "id": "c2"}, true, &after); code != 200 {
		t.Fatalf("remove 应 200，实际 %d", code)
	}
	for _, c := range after.Channels {
		if c.ID == "c2" {
			t.Fatalf("c2 应已删除：%+v", after.Channels)
		}
	}
}

// OneBot 反向 WS：令牌在升级前校验；连上后发一条群消息，能收到 send_group_msg 回发
func TestOneBotChannelWS(t *testing.T) {
	e, _ := newAgentEnv(t)

	// onebot 必须带令牌
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "save", "id": "qq", "kind": "onebot"}, true, nil); code != 400 {
		t.Fatalf("onebot 没令牌应 400，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "save", "id": "qq", "kind": "onebot", "token": "obtok"}, true, nil); code != 200 {
		t.Fatalf("保存 onebot 频道失败：%d", code)
	}

	wsBase := "ws" + strings.TrimPrefix(e.srv.URL, "http")
	// 令牌不对：升级前就该被挡（拿不到连接）
	if c, resp, err := websocket.DefaultDialer.Dial(wsBase+"/api/channels/qq/ws?access_token=wrong", nil); err == nil {
		c.Close()
		t.Fatal("令牌不对竟然连上了")
	} else if resp == nil || resp.StatusCode != 401 {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("令牌不对应 401，实际 %d（err=%v）", code, err)
	}

	// 配个假模型，正确令牌连上并发一条群消息
	openai := fakeOpenAI(t)
	if code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "local", "protocol": "openai", "baseUrl": openai.URL + "/v1", "model": "fake-openai", "apiKey": "sk-x"},
	}, true, nil); code != 200 {
		t.Fatalf("配模型失败：%d", code)
	}
	c, _, err := websocket.DefaultDialer.Dial(wsBase+"/api/channels/qq/ws?access_token=obtok", nil)
	if err != nil {
		t.Fatalf("正确令牌应能连上：%v", err)
	}
	defer c.Close()

	ev := `{"post_type":"message","message_type":"group","group_id":42,"user_id":7,"raw_message":"帮我看看"}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(ev)); err != nil {
		t.Fatalf("发事件失败：%v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("没等到回发：%v", err)
	}
	if !strings.Contains(string(data), "send_group_msg") || !strings.Contains(string(data), "能通") {
		t.Fatalf("回发不对：%s", data)
	}
}

// 飞书事件回调：challenge 握手 / 令牌闸门 / 消息事件真的跑一次
func TestFeishuChannelEvent(t *testing.T) {
	e, _ := newAgentEnv(t)

	// feishu 必须带 appId/appSecret 与验证令牌
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "save", "id": "fs", "kind": "feishu", "token": "vtok"}, true, nil); code != 400 {
		t.Fatalf("feishu 缺 appId/appSecret 应 400，实际 %d", code)
	}
	if code := e.do("POST", "/api/agent/channels", map[string]any{
		"action": "save", "id": "fs", "kind": "feishu", "token": "vtok",
		"appId": "cli_x", "appSecret": "sec", "domain": "http://127.0.0.1:1",
	}, true, nil); code != 200 {
		t.Fatalf("保存 feishu 频道失败：%d", code)
	}

	// URL 验证握手
	var chal map[string]any
	if code := e.do("POST", "/api/channels/fs/event", map[string]any{"type": "url_verification", "challenge": "c-1", "token": "vtok"}, false, &chal); code != 200 {
		t.Fatalf("握手应 200，实际 %d", code)
	}
	if chal["challenge"] != "c-1" {
		t.Fatalf("challenge 没回：%+v", chal)
	}
	// 令牌不对 → 401
	if code := e.do("POST", "/api/channels/fs/event", map[string]any{"header": map[string]any{"event_type": "im.message.receive_v1", "token": "bad"}}, false, nil); code != 401 {
		t.Fatalf("令牌不对应 401，实际 %d", code)
	}
	// webhook 频道的 /event 应 400（不是事件型频道）
	if code := e.do("POST", "/api/agent/channels", map[string]any{"action": "save", "id": "wh", "outboundUrl": "http://127.0.0.1:1/x"}, true, nil); code != 200 {
		t.Fatalf("保存 webhook 频道失败：%d", code)
	}
	if code := e.do("POST", "/api/channels/wh/event", map[string]any{"type": "url_verification", "challenge": "x", "token": "t"}, false, nil); code != 400 {
		t.Fatalf("webhook 走 /event 应 400，实际 %d", code)
	}

	// 配假模型，推一条消息事件 → 跑一次 Agent
	openai := fakeOpenAI(t)
	if code := e.do("POST", "/api/agent/providers", map[string]any{
		"action": "upsert",
		"config": map[string]any{"name": "local", "protocol": "openai", "baseUrl": openai.URL + "/v1", "model": "fake-openai", "apiKey": "sk-x"},
	}, true, nil); code != 200 {
		t.Fatalf("配模型失败：%d", code)
	}
	ev := map[string]any{
		"schema": "2.0",
		"header": map[string]any{"event_type": "im.message.receive_v1", "token": "vtok"},
		"event": map[string]any{
			"sender":  map[string]any{"sender_id": map[string]any{"open_id": "ou_1"}},
			"message": map[string]any{"message_id": "m1", "chat_id": "oc_1", "chat_type": "group", "message_type": "text", "content": `{"text":"飞书自检消息"}`},
		},
	}
	if code := e.do("POST", "/api/channels/fs/event", ev, false, nil); code != 200 {
		t.Fatalf("消息事件应 200，实际 %d", code)
	}
	hit := false
	for i := 0; i < 40 && !hit; i++ {
		time.Sleep(200 * time.Millisecond)
		var runs struct {
			Runs []struct {
				Goal string `json:"goal"`
			} `json:"runs"`
		}
		if code := e.do("GET", "/api/agent/runs?limit=10", nil, true, &runs); code == 200 {
			for _, r := range runs.Runs {
				if strings.Contains(r.Goal, "飞书自检消息") {
					hit = true
					break
				}
			}
		}
	}
	if !hit {
		t.Fatal("飞书消息事件没触发 Agent 运行")
	}
}

// 飞书轮询模式（免公网）：有 chatIds 即可，无需验证令牌；chatIds / pollSec 要能回显
func TestFeishuChannelPolling(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 既没验证令牌、也没轮询会话 → 收不到消息，应 400
	if code := e.do("POST", "/api/agent/channels", map[string]any{
		"action": "save", "id": "fs", "kind": "feishu", "appId": "cli_x", "appSecret": "sec",
	}, true, nil); code != 400 {
		t.Fatalf("feishu 无 token 无 chatIds 应 400，实际 %d", code)
	}
	// 轮询模式
	var saved channelsResp
	if code := e.do("POST", "/api/agent/channels", map[string]any{
		"action": "save", "id": "fs-poll", "kind": "feishu",
		"appId": "cli_x", "appSecret": "sec", "chatIds": []string{"oc_1", "oc_2"},
	}, true, &saved); code != 200 {
		t.Fatalf("轮询模式保存失败：%d", code)
	}
	var got *channelInfo
	for i := range saved.Channels {
		if saved.Channels[i].ID == "fs-poll" {
			got = &saved.Channels[i]
		}
	}
	if got == nil {
		t.Fatal("fs-poll 没保存上")
	}
	if len(got.ChatIDs) != 2 || got.PollSec != 5 {
		t.Fatalf("chatIds / pollSec 没回显：%+v", got)
	}
}

// 没挂 Agent 服务时频道接口不该注册
func TestChannelsAbsentWithoutAgent(t *testing.T) {
	e := newEnv(t, 5e9)
	if code := e.do("GET", "/api/agent/channels", nil, true, nil); code == 200 {
		t.Fatalf("没挂 Agent 时 /api/agent/channels 不该可用，实际 %d", code)
	}
}
