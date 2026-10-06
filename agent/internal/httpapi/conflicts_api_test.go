package httpapi_test

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// TestConflictNegotiation 同名两份 → 自动开协商 → 左右对照 / 聊天 / 信令 / 一人放弃 → 只留正本
func TestConflictNegotiation(t *testing.T) {
	e, _ := newAgentEnv(t)

	idA := createUser(t, e, "userA", "pw-a-123456")
	createUser(t, e, "userB", "pw-b-123456")
	createUser(t, e, "userC", "pw-c-123456")
	sessA := login(t, e, "userA", "pw-a-123456")
	sessB := login(t, e, "userB", "pw-b-123456")
	sessC := login(t, e, "userC", "pw-c-123456")

	var g struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	e.do("POST", "/api/agent/groups", map[string]any{"name": "组1", "ownerId": idA}, true, &g)
	gid := g.Group.ID
	e.do("POST", "/api/agent/groups/"+gid+"/members",
		map[string]any{"action": "add", "userId": mustUserID(t, e, "userB")}, true, nil)

	docsPath := "/api/agent/groups/" + gid + "/docs"
	upload := func(session, content string) map[string]any {
		var out map[string]any
		code := e.doAs("POST", docsPath, session, map[string]any{
			"name": "doc2.txt", "kind": "file",
			"dataBase64": base64.StdEncoding.EncodeToString([]byte(content)),
		}, &out)
		if code != 200 {
			t.Fatalf("上传失败：%d", code)
		}
		return out
	}

	up1 := upload(sessA, "A 的第一版")
	if up1["conflict"] != nil {
		t.Fatalf("只有一版时不该开协商：%+v", up1["conflict"])
	}

	// B 传了同名不同内容 → 自动开协商，响应里带 conflict
	up2 := upload(sessB, "B 改了内容")
	conf, _ := up2["conflict"].(map[string]any)
	if conf == nil {
		t.Fatal("出现第二版时应当自动开协商会话")
	}
	sid, _ := conf["id"].(string)
	if sid == "" {
		t.Fatalf("协商会话没有 id：%+v", conf)
	}
	if conf["status"] != "open" || conf["versionCount"] != float64(2) {
		t.Fatalf("协商会话字段不对：%+v", conf)
	}

	// 组内两版都记为 A / B 各自的
	var vers struct {
		Docs []map[string]any `json:"docs"`
	}
	e.doAs("GET", docsPath, sessA, nil, &vers)
	if len(vers.Docs) != 2 {
		t.Fatalf("应有两版：%+v", vers.Docs)
	}
	aDoc, bDoc := "", ""
	for _, v := range vers.Docs {
		if v["by"] == "userA" {
			aDoc, _ = v["id"].(string)
		}
		if v["by"] == "userB" {
			bDoc, _ = v["id"].(string)
		}
	}
	if aDoc == "" || bDoc == "" || aDoc == bDoc {
		t.Fatalf("两版归属不对：A=%s B=%s", aDoc, bDoc)
	}

	// 未登录 / 非成员都不能碰协商
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts", "", nil, nil); code != 403 {
		t.Fatalf("未登录访问协商应 403，实际 %d", code)
	}
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts", sessC, nil, nil); code != 403 {
		t.Fatalf("非成员访问协商应 403，实际 %d", code)
	}

	// 会话列表 + 详情（详情里两版都在，且带 me）
	type sessResp struct {
		Conflict map[string]any   `json:"conflict"`
		Versions []map[string]any `json:"versions"`
		Messages []map[string]any `json:"messages"`
		Signals  []map[string]any `json:"signals"`
		Me       string           `json:"me"`
	}
	var detail sessResp
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/"+sid, sessB, nil, &detail); code != 200 {
		t.Fatalf("读协商详情失败：%d", code)
	}
	if detail.Me != mustUserID(t, e, "userB") {
		t.Fatalf("me 应是 B：%s", detail.Me)
	}
	if len(detail.Versions) != 2 {
		t.Fatalf("详情应有两版：%+v", detail.Versions)
	}

	// 聊天：A 发，B 拉得到
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/messages",
		sessA, map[string]any{"text": "我改了预算那一段"}, nil); code != 200 {
		t.Fatalf("发消息失败：%d", code)
	}
	var mb sessResp
	e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/"+sid, sessB, nil, &mb)
	if len(mb.Messages) != 1 || mb.Messages[0]["text"] != "我改了预算那一段" {
		t.Fatalf("B 应看到 A 的消息：%+v", mb.Messages)
	}

	// 信令：A 发 offer，B 拉得到；A 拉不到自己发的
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/signal",
		sessA, map[string]any{"kind": "offer", "payload": `{"sdp":"x"}`}, nil); code != 200 {
		t.Fatalf("发信令失败：%d", code)
	}
	var sigB, sigA sessResp
	e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/signal?after=0", sessB, nil, &sigB)
	e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/signal?after=0", sessA, nil, &sigA)
	if len(sigB.Signals) != 1 || sigB.Signals[0]["kind"] != "offer" {
		t.Fatalf("B 应收到 A 的 offer：%+v", sigB.Signals)
	}
	if len(sigA.Signals) != 0 {
		t.Fatalf("A 不该收到自己发的信令：%+v", sigA.Signals)
	}

	// B 放弃自己那版 → 保留 A 那版，且 B 的那份被清掉
	var gu struct {
		Conflict map[string]any `json:"conflict"`
		Removed  []string       `json:"removed"`
	}
	if code := e.doAs("POST", "/api/agent/groups/"+gid+"/conflicts/"+sid+"/giveup", sessB, map[string]any{}, &gu); code != 200 {
		t.Fatalf("放弃失败：%d", code)
	}
	if gu.Conflict["status"] != "resolved" || gu.Conflict["resolvedDoc"] != aDoc {
		t.Fatalf("放弃后应保留 A 那版：%+v", gu.Conflict)
	}
	if len(gu.Removed) != 1 || gu.Removed[0] != bDoc {
		t.Fatalf("B 那版应被清掉：%+v", gu.Removed)
	}

	// 定稿后只剩一份文档，且不再是分叉
	var after struct {
		Docs     []map[string]any `json:"docs"`
		Versions []struct {
			Forked bool `json:"forked"`
		} `json:"versions"`
	}
	e.doAs("GET", docsPath, sessA, nil, &after)
	if len(after.Docs) != 1 || after.Versions[0].Forked {
		t.Fatalf("定稿后应只剩一版且不分叉：%+v", after)
	}
	// 已定稿的会话不再出现在"只看 open"的列表里
	var open struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts", sessA, nil, &open)
	if len(open.Conflicts) != 0 {
		t.Fatalf("已定稿不该还在 open 列表：%+v", open.Conflicts)
	}
	// 全量列表里在
	var all struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts?all=1", sessA, nil, &all)
	if len(all.Conflicts) != 1 {
		t.Fatalf("全量列表应有 1 条：%+v", all.Conflicts)
	}

	// 我的待办冲突（跨组聚合）：定稿后应为空
	var mine struct {
		Conflicts []map[string]any `json:"conflicts"`
	}
	e.doAs("GET", "/api/agent/conflicts", sessA, nil, &mine)
	if len(mine.Conflicts) != 0 {
		t.Fatalf("定稿后我的待办冲突应为空：%+v", mine.Conflicts)
	}

	// 拿别组的 sid 来撞：应当 404（会话不属于该组）
	if code := e.doAs("GET", "/api/agent/groups/"+gid+"/conflicts/deadbeefdeadbeef", sessA, nil, nil); code != http.StatusNotFound {
		t.Fatalf("不存在的会话应 404，实际 %d", code)
	}
}
