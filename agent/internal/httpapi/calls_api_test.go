package httpapi_test

import (
	"encoding/base64"
	"net/http"
	"testing"
	"time"
)

// TestCallsSessionFlow 组内两人通话：开/复用、留言与信令中转、参与人隔离、挂断
func TestCallsSessionFlow(t *testing.T) {
	e, _ := newAgentEnv(t)

	idA := createUser(t, e, "alice", "pw-a-123456")
	_ = createUser(t, e, "bob", "pw-b-123456")
	_ = createUser(t, e, "carol", "pw-c-123456")
	sessA := login(t, e, "alice", "pw-a-123456")
	sessB := login(t, e, "bob", "pw-b-123456")
	sessC := login(t, e, "carol", "pw-c-123456")

	var g struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	e.do("POST", "/api/agent/groups", map[string]any{"name": "呼叫组", "ownerId": idA}, true, &g)
	gid := g.Group.ID
	e.do("POST", "/api/agent/groups/"+gid+"/members",
		map[string]any{"action": "add", "userId": mustUserID(t, e, "bob")}, true, nil)
	e.do("POST", "/api/agent/groups/"+gid+"/members",
		map[string]any{"action": "add", "userId": mustUserID(t, e, "carol")}, true, nil)

	// 开通话（幂等：同一对再开拿到同一条）
	type openResp struct {
		OK   bool           `json:"ok"`
		Call map[string]any `json:"call"`
	}
	var c1 openResp
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls", sessA,
		map[string]any{"peer": mustUserID(t, e, "bob")}, &c1); code != 200 {
		t.Fatalf("开通话失败：%d", code)
	}
	cid, _ := c1.Call["id"].(string)
	if cid == "" {
		t.Fatalf("没有通话 id：%+v", c1.Call)
	}
	var c2 openResp
	e.doAs("POST", "/api/agent/groups/"+gid+"/calls", sessB,
		map[string]any{"peer": mustUserID(t, e, "alice")}, &c2)
	if c2.Call["id"] != cid {
		t.Fatalf("同一对重复开应复用：%s vs %s", cid, c2.Call["id"])
	}

	// 对方不是组内成员（dave 没加进组）→ 400
	_ = createUser(t, e, "dave", "pw-d-123456")
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls", sessA,
		map[string]any{"peer": mustUserID(t, e, "dave")}, nil); code != http.StatusBadRequest {
		t.Fatalf("组外的人不该能开通话，实际 %d", code)
	}

	// 留言：A 发，B 拉得到
	type detailResp struct {
		Call     map[string]any   `json:"call"`
		Messages []map[string]any `json:"messages"`
		Signals  []map[string]any `json:"signals"`
		Me       string           `json:"me"`
	}
	detail := func(session string) detailResp {
		var d detailResp
		if code := e.doAs("GET", "/api/agent/groups/"+gid+"/calls/"+cid, session, nil, &d); code != 200 {
			t.Fatalf("读通话详情失败：%d", code)
		}
		return d
	}
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls/"+cid+"/messages",
		sessA, map[string]any{"text": "现在方便接电话吗？"}, nil); code != 200 {
		t.Fatalf("发留言失败：%d", code)
	}
	if d := detail(sessB); len(d.Messages) != 1 || d.Messages[0]["text"] != "现在方便接电话吗？" {
		t.Fatalf("B 应看到 A 的留言：%+v", d.Messages)
	}
	// 第三人（组内成员但非参与人）看不了这条通话
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/calls/"+cid, sessC, nil, nil); code != http.StatusNotFound {
		t.Fatalf("非参与人读通话应 404，实际 %d", code)
	}

	// 信令：A 发 offer 定向给 B，B 拉得到、A 自己拉不到
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls/"+cid+"/signal",
		sessA, map[string]any{"to": mustUserID(t, e, "bob"), "kind": "offer", "payload": `{"sdp":"1"}`}, nil); code != 200 {
		t.Fatalf("发信令失败：%d", code)
	}
	if d := detail(sessB); len(d.Signals) != 1 || d.Signals[0]["kind"] != "offer" {
		t.Fatalf("B 应收到 offer：%+v", d.Signals)
	}
	if d := detail(sessA); len(d.Signals) != 0 {
		t.Fatalf("A 不该看到自己发的信令：%+v", d.Signals)
	}

	// 挂断：状态翻 ended，"只看进行中"的列表里没了
	type endResp struct {
		Call map[string]any `json:"call"`
	}
	var er endResp
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls/"+cid+"/end", sessB, map[string]any{}, &er); code != 200 {
		t.Fatalf("挂断失败：%d", code)
	}
	if er.Call["status"] != "ended" {
		t.Fatalf("挂断后状态不对：%+v", er.Call)
	}
	var live struct {
		Calls []map[string]any `json:"calls"`
	}
	e.doAs("GET", "/api/agent/groups/"+gid+"/calls", sessA, nil, &live)
	if len(live.Calls) != 0 {
		t.Fatalf("挂断后不该还在进行中列表：%+v", live.Calls)
	}
}

// TestCallAIConfirmGate 「回复AI」确认闸：两位都确认才触发判定；
// 测试环境没配模型通道 → 判定如实回 failed + 一条系统留言（不许假装成功）
func TestCallAIConfirmGate(t *testing.T) {
	e, _ := newAgentEnv(t)

	_ = createUser(t, e, "anna", "pw-an-123456")
	_ = createUser(t, e, "bill", "pw-bl-123456")
	sessA := login(t, e, "anna", "pw-an-123456")
	sessB := login(t, e, "bill", "pw-bl-123456")

	var g struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	e.do("POST", "/api/agent/groups", map[string]any{"name": "合并组", "ownerId": mustUserID(t, e, "anna")}, true, &g)
	gid := g.Group.ID
	e.do("POST", "/api/agent/groups/"+gid+"/members",
		map[string]any{"action": "add", "userId": mustUserID(t, e, "bill")}, true, nil)

	// 同名两版 → 自动开协商
	docsPath := "/api/agent/groups/" + gid + "/docs"
	upload := func(session, content string) map[string]any {
		var out map[string]any
		if code := e.doAs("POST", docsPath, session, map[string]any{
			"name": "合同.md", "kind": "file",
			"dataBase64": base64.StdEncoding.EncodeToString([]byte(content)),
		}, &out); code != 200 {
			t.Fatalf("上传失败：%d", code)
		}
		return out
	}
	up1 := upload(sessA, "# 合同（anna 版）\n条款 1：金额 10 万")
	if up1["conflict"] != nil {
		t.Fatalf("只有一版不该开协商")
	}
	up2 := upload(sessB, "# 合同（bill 版）\n条款 1：金额 12 万\n条款 2：账期 30 天")
	conf, _ := up2["conflict"].(map[string]any)
	if conf == nil {
		t.Fatal("出现第二版应当自动开协商")
	}
	sid, _ := conf["id"].(string)
	aDocID, _ := up1["doc"].(map[string]any)["id"].(string)
	if aDocID == "" {
		t.Fatalf("取不到第一版的 docId：%+v", up1)
	}

	// 通话也挂到这次协商下（通话里的留言将来会交给 AI 通读）
	type openResp struct {
		Call map[string]any `json:"call"`
	}
	var c openResp
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls", sessA,
		map[string]any{"peer": mustUserID(t, e, "bill"), "conflictSid": sid}, &c); code != 200 {
		t.Fatalf("开协商通话失败：%d", code)
	}
	cid, _ := c.Call["id"].(string)
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/calls/"+cid+"/messages",
		sessA, map[string]any{"text": "电话里说好了：12 万、30 天，都写进去"}, nil); code != 200 {
		t.Fatalf("通话留言失败：%d", code)
	}

	// 先挂到协商里的通话详情应带出协商状态（通话浮窗要显示 AI 进度）
	type callDetail struct {
		Conflict map[string]any `json:"conflict"`
	}
	var cd callDetail
	e.doAs("GET", "/api/agent/groups/"+gid+"/calls/"+cid+"?afterMsg=0&afterSignal=0", sessA, nil, &cd)
	if cd.Conflict == nil || cd.Conflict["id"] != sid {
		t.Fatalf("通话详情应带出关联协商：%+v", cd.Conflict)
	}

	// 单人确认：还没齐
	type confirmResp struct {
		AllConfirmed bool     `json:"allConfirmed"`
		Missing      []string `json:"missing"`
		Triggered    bool     `json:"triggered"`
	}
	var r1 confirmResp
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/ai-confirm", sessA, map[string]any{}, &r1); code != 200 {
		t.Fatalf("确认失败：%d", code)
	}
	if r1.AllConfirmed || r1.Triggered || len(r1.Missing) != 1 {
		t.Fatalf("anna 一个人确认不该触发：all=%v missing=%v triggered=%v", r1.AllConfirmed, r1.Missing, r1.Triggered)
	}
	var r2 confirmResp
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/ai-confirm", sessB, map[string]any{}, &r2); code != 200 {
		t.Fatalf("确认失败：%d", code)
	}
	if !r2.AllConfirmed || !r2.Triggered {
		t.Fatalf("两人都确认应当触发 AI 判定：all=%v triggered=%v", r2.AllConfirmed, r2.Triggered)
	}

	// 判定异步跑（测试环境没有模型通道 → 应当如实回 failed + 系统留言）
	var detail struct {
		Conflict map[string]any   `json:"conflict"`
		Messages []map[string]any `json:"messages"`
	}
	deadline := time.Now().Add(10 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		if code := e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/"+sid, sessA, nil, &detail); code != 200 {
			t.Fatalf("读协商详情失败：%d", code)
		}
		status, _ = detail.Conflict["aiStatus"].(string)
		if status == "failed" || status == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("没配模型通道时判定应如实 failed，实际 %q", status)
	}
	sawAI := false
	for _, m := range detail.Messages {
		if m["fromName"] == "白泽AI" {
			sawAI = true
			break
		}
	}
	if !sawAI {
		t.Fatalf("失败应当留一条系统留言说明原因：%+v", detail.Messages)
	}

	// 已收场的协商再点确认：400（不重复触发）
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/resolve",
		sessA, map[string]any{"docId": aDocID}, nil); code != 200 {
		t.Fatalf("定稿失败：%d", code)
	}
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/ai-confirm",
		sessB, map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("已收场的协商再确认应 400，实际 %d", code)
	}
	// 定稿后通话详情应能正常读（协商还在，只是不再是 open；接口不该炸）
	var cd2 struct {
		Conflict map[string]any `json:"conflict"`
	}
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/calls/"+cid, sessA, nil, &cd2); code != 200 {
		t.Fatalf("定稿后读通话详情失败：%d", code)
	}
	if cd2.Conflict == nil {
		t.Fatalf("通话详情应仍带出关联协商：%+v", cd2)
	}
}
