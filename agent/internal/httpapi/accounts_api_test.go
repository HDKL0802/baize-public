package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// doAs 与 e.do 同款，但额外带登录会话（X-Baize-Session），用来模拟"以某用户身份访问"
func (e *env) doAs(method, path, session string, body any, out any) int {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("序列化请求失败：%v", err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Baize-Token", e.token)
	if session != "" {
		req.Header.Set("X-Baize-Session", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("请求失败 %s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && resp.StatusCode < 400 {
			e.t.Fatalf("解析响应失败 %s %s：%v", method, path, err)
		}
	}
	return resp.StatusCode
}

// createUser 建一个用户，返回 id
func createUser(t *testing.T, e *env, name, pwd string) string {
	t.Helper()
	var out struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if code := e.do("POST", "/api/agent/users", map[string]any{"name": name, "password": pwd}, true, &out); code != 200 {
		t.Fatalf("建用户 %s 失败：%d", name, code)
	}
	if out.User.ID == "" {
		t.Fatalf("建用户 %s 没回 id", name)
	}
	return out.User.ID
}

// login 登录并返回会话令牌
func login(t *testing.T, e *env, name, pwd string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if code := e.doAs("POST", "/api/agent/auth/login", "", map[string]any{"name": name, "password": pwd}, &out); code != 200 {
		t.Fatalf("登录 %s 失败：%d", name, code)
	}
	if out.Token == "" {
		t.Fatal("登录没回会话令牌")
	}
	return out.Token
}

// TestMultiUserIsolation 每个用户一份独立的记忆库/笔记库，互相看不到
func TestMultiUserIsolation(t *testing.T) {
	e, _ := newAgentEnv(t)

	createUser(t, e, "userA", "pw-a-123456")
	createUser(t, e, "userB", "pw-b-123456")
	sessA := login(t, e, "userA", "pw-a-123456")
	sessB := login(t, e, "userB", "pw-b-123456")

	// A 写一篇笔记
	if code := e.doAs("POST", "/api/agent/notes", sessA, map[string]any{
		"path": "a-only.md", "title": "A的笔记", "content": "只有 A 能看到的秘密",
	}, nil); code != 200 {
		t.Fatalf("A 写笔记失败：%d", code)
	}

	listNotes := func(session string) []map[string]any {
		var out struct {
			Notes []map[string]any `json:"notes"`
		}
		if code := e.doAs("GET", "/api/agent/notes", session, nil, &out); code != 200 {
			t.Fatalf("读笔记失败：%d", code)
		}
		return out.Notes
	}

	if n := len(listNotes(sessA)); n != 1 {
		t.Fatalf("A 应看到自己的 1 篇笔记，实得 %d", n)
	}
	if n := len(listNotes(sessB)); n != 0 {
		t.Fatalf("B 不该看到 A 的笔记，实得 %d", n)
	}
	// 没登录 = 默认主体，同样看不到 A 的私人笔记
	if n := len(listNotes("")); n != 0 {
		t.Fatalf("默认主体不该看到任何人的私人笔记，实得 %d", n)
	}

	// 知识库同样隔离：A 加一条待办，B 的快照里没有
	postTodo := func(session, title string) int {
		return e.doAs("POST", "/api/kb/op", session, map[string]any{
			"op": "todo.add", "args": map[string]any{"title": title},
		}, nil)
	}
	if code := postTodo(sessA, "A的待办"); code != 200 {
		t.Fatalf("A 加待办失败：%d", code)
	}
	var snapB map[string]any
	if code := e.doAs("GET", "/api/kb/stats", sessB, nil, &snapB); code != 200 {
		t.Fatalf("读 B 知识库失败：%d", code)
	}
	if snapB["todos"] != float64(0) {
		t.Fatalf("B 的知识库该是空的，实得 %+v", snapB)
	}
}

// TestGroupSharedDocs 组内共享文档：A 上传，B 立刻能看到并下载；非成员看不到
func TestGroupSharedDocs(t *testing.T) {
	e, _ := newAgentEnv(t)

	idA := createUser(t, e, "userA", "pw-a-123456")
	createUser(t, e, "userB", "pw-b-123456")
	createUser(t, e, "userC", "pw-c-123456")
	sessA := login(t, e, "userA", "pw-a-123456")
	sessB := login(t, e, "userB", "pw-b-123456")
	sessC := login(t, e, "userC", "pw-c-123456")

	// 建组（A 是 owner），把 B 拉进来；C 留在组外
	var g struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if code := e.do("POST", "/api/agent/groups", map[string]any{"name": "组1", "ownerId": idA}, true, &g); code != 200 {
		t.Fatalf("建组失败：%d", code)
	}
	gid := g.Group.ID
	if gid == "" {
		t.Fatal("建组没回 id")
	}
	var m struct {
		Members []map[string]any `json:"members"`
	}
	if code := e.do("POST", "/api/agent/groups/"+gid+"/members", map[string]any{
		"action": "add", "userId": mustUserID(t, e, "userB"),
	}, true, &m); code != 200 {
		t.Fatalf("加成员失败：%d", code)
	}
	if len(m.Members) != 2 {
		t.Fatalf("组里应有 2 人，实得 %d", len(m.Members))
	}

	docsPath := "/api/agent/groups/" + gid + "/docs"

	// 没登录碰共享文档应被挡
	if code := e.doAs("GET", docsPath, "", nil, nil); code != 403 {
		t.Fatalf("未登录访问共享文档应 403，实际 %d", code)
	}

	// A 上传 2 号文档
	upload := func(session, name, content string) int {
		return e.doAs("POST", docsPath, session, map[string]any{
			"name": name, "kind": "file",
			"dataBase64": base64.StdEncoding.EncodeToString([]byte(content)),
		}, nil)
	}
	if code := upload(sessA, "2号文档.txt", "A 写的第一版"); code != 200 {
		t.Fatalf("A 上传失败：%d", code)
	}

	// B 刷新就能看到（这就是用户要的"A上传 B立刻看到"）
	type docsResp struct {
		Docs     []map[string]any `json:"docs"`
		Versions []struct {
			Name     string           `json:"name"`
			Forked   bool             `json:"forked"`
			Versions []map[string]any `json:"versions"`
		} `json:"versions"`
	}
	var byB docsResp
	if code := e.doAs("GET", docsPath, sessB, nil, &byB); code != 200 {
		t.Fatalf("B 读共享文档失败：%d", code)
	}
	if len(byB.Docs) != 1 {
		t.Fatalf("B 应看到 A 传的 1 份文档，实得 %d", len(byB.Docs))
	}
	if len(byB.Versions) != 1 || byB.Versions[0].Forked {
		t.Fatalf("此时还不该分叉：%+v", byB.Versions)
	}
	if byB.Versions[0].Versions[0]["by"] != "userA" {
		t.Fatalf("上传者应记为 userA：%+v", byB.Versions[0].Versions[0])
	}

	// B 下载：内容拿得到
	docID, _ := byB.Docs[0]["id"].(string)
	var dl struct {
		DataBase64 string `json:"dataBase64"`
	}
	if code := e.doAs("GET", docsPath+"/"+docID, sessB, nil, &dl); code != 200 {
		t.Fatalf("B 下载失败：%d", code)
	}
	raw, _ := base64.StdEncoding.DecodeString(dl.DataBase64)
	if string(raw) != "A 写的第一版" {
		t.Fatalf("下载内容不对：%q", raw)
	}

	// 非成员 C 看不到
	if code := e.doAs("GET", docsPath, sessC, nil, nil); code != 403 {
		t.Fatalf("非成员应 403，实际 %d", code)
	}

	// 同名不同内容 = 分叉（A 的一版 / B 的一版）
	if code := upload(sessB, "2号文档.txt", "B 改了内容"); code != 200 {
		t.Fatalf("B 上传失败：%d", code)
	}
	var forked docsResp
	if code := e.doAs("GET", docsPath, sessA, nil, &forked); code != 200 {
		t.Fatalf("读分叉失败：%d", code)
	}
	if len(forked.Versions) != 1 || !forked.Versions[0].Forked || len(forked.Versions[0].Versions) != 2 {
		t.Fatalf("同名两份应标为分叉且带 2 版：%+v", forked.Versions)
	}
}

// mustUserID 按用户名查 id
func mustUserID(t *testing.T, e *env, name string) string {
	t.Helper()
	var out struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if code := e.do("GET", "/api/agent/users", nil, true, &out); code != 200 {
		t.Fatalf("读用户表失败：%d", code)
	}
	for _, u := range out.Users {
		if u.Name == name {
			return u.ID
		}
	}
	t.Fatalf("找不到用户 %s", name)
	return ""
}

// TestAccountsAuthFlow 登录/身份/登出与"非管理员不能冒充别人"
func TestAccountsAuthFlow(t *testing.T) {
	e, _ := newAgentEnv(t)

	// 首启应已有一个 admin
	var users struct {
		Users []struct {
			Name  string `json:"name"`
			Admin bool   `json:"admin"`
		} `json:"users"`
	}
	if code := e.do("GET", "/api/agent/users", nil, true, &users); code != 200 {
		t.Fatalf("读用户失败：%d", code)
	}
	if len(users.Users) != 1 || users.Users[0].Name != "admin" || !users.Users[0].Admin {
		t.Fatalf("首启应有一个 admin：%+v", users.Users)
	}

	createUser(t, e, "userA", "pw-a-123456")
	createUser(t, e, "userB", "pw-b-123456")
	sessA := login(t, e, "userA", "pw-a-123456")

	// whoami：登录后是用户主体
	var who struct {
		Principal string `json:"principal"`
		LoggedIn  bool   `json:"loggedIn"`
		Default   bool   `json:"default"`
	}
	if code := e.doAs("GET", "/api/agent/auth/whoami", sessA, nil, &who); code != 200 {
		t.Fatalf("whoami 失败：%d", code)
	}
	if !who.LoggedIn || who.Default || who.Principal == "" {
		t.Fatalf("登录后 whoami 应给出用户主体：%+v", who)
	}
	// 未登录 = 默认主体
	var anon struct {
		Default  bool `json:"default"`
		LoggedIn bool `json:"loggedIn"`
	}
	e.doAs("GET", "/api/agent/auth/whoami", "", nil, &anon)
	if !anon.Default || anon.LoggedIn {
		t.Fatalf("未登录应是默认主体：%+v", anon)
	}

	// 普通用户传 ?as= 冒充别人：应被忽略，仍回到自己的主体
	var spoof struct {
		Principal string `json:"principal"`
	}
	uidB := mustUserID(t, e, "userB")
	e.doAs("GET", "/api/agent/auth/whoami?as=user:"+uidB, sessA, nil, &spoof)
	if spoof.Principal == "user:"+uidB {
		t.Fatal("普通用户不该能通过 ?as= 冒充别人")
	}

	// 密码错 → 401
	if code := e.doAs("POST", "/api/agent/auth/login", "", map[string]any{"name": "userA", "password": "错的"}, nil); code != 401 {
		t.Fatalf("错密码应 401，实际 %d", code)
	}

	// 登出后原令牌失效
	if code := e.doAs("POST", "/api/agent/auth/logout", sessA, map[string]any{}, nil); code != 200 {
		t.Fatalf("登出失败：%d", code)
	}
	if code := e.doAs("GET", "/api/agent/auth/whoami", sessA, nil, &who); code != 200 {
		t.Fatalf("whoami 失败：%d", code)
	}
	if who.LoggedIn {
		t.Fatal("登出后不该还是登录态")
	}
}
