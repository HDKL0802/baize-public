package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"baize/internal/httpapi"
	"baize/internal/hub"
	"baize/internal/logx"
	"baize/shared/proto"
	"baize/internal/store"
)

const testToken = "test-token-1234"

type env struct {
	t     *testing.T
	srv   *httptest.Server
	hub   *hub.Hub
	store *store.Store
	token string
}

func newEnv(t *testing.T, receiptTimeout time.Duration) *env {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/agent.db")
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, lg, testToken, receiptTimeout)
	srv := httptest.NewServer(httpapi.New(h, lg, logx.NewRing(200), "test").Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, hub: h, store: st, token: testToken}
}

type stateResp struct {
	Version  string         `json:"version"`
	Actions  []string       `json:"actions"`
	Devices  []store.Device `json:"devices"`
	Tasks    []store.Task   `json:"tasks"`
	Records  []store.Record `json:"records"`
	Memories []store.Memory `json:"memories"`
	Stats    store.Stats    `json:"stats"`
}

func (e *env) do(method, path string, body any, withToken bool, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("序列化请求失败：%v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if withToken {
		req.Header.Set("X-Baize-Token", e.token)
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

func (e *env) state() stateResp {
	e.t.Helper()
	var s stateResp
	if code := e.do("GET", "/api/state", nil, true, &s); code != 200 {
		e.t.Fatalf("读取状态失败，HTTP %d", code)
	}
	return s
}

func (e *env) createTask(deviceID, action string, args map[string]any) store.Task {
	e.t.Helper()
	var out struct {
		Task store.Task `json:"task"`
	}
	if code := e.do("POST", "/api/tasks", map[string]any{
		"deviceId": deviceID, "action": action, "args": args, "origin": "user",
	}, true, &out); code != 200 {
		e.t.Fatalf("创建任务失败，HTTP %d", code)
	}
	return out.Task
}

func (e *env) taskByID(id string) store.Task {
	e.t.Helper()
	for _, t := range e.state().Tasks {
		if t.ID == id {
			return t
		}
	}
	return store.Task{}
}

func (e *env) waitTaskStatus(id, status string, timeout time.Duration) store.Task {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if t := e.taskByID(id); t.Status == status {
			return t
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("等待任务 %s 进入状态 %s 超时，当前：%s", id, status, e.taskByID(id).Status)
	return store.Task{}
}

func (e *env) waitDeviceOnline(id string, online bool, timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, d := range e.state().Devices {
			if d.ID == id && d.Online == online {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("等待设备 %s 上线状态=%v 超时", id, online)
}

/* ---------- 假设备（桌面端替身） ---------- */

type fakeDevice struct {
	t       *testing.T
	ws      *websocket.Conn
	id      string
	ack     proto.HelloAck
	in      chan proto.Envelope
	pending []proto.Envelope
	dead    chan struct{}
}

func newDevice(t *testing.T, srvURL, token, id string) *fakeDevice {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/ws/device"
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("设备连接失败：%v", err)
	}
	d := &fakeDevice{t: t, ws: ws, id: id, in: make(chan proto.Envelope, 64), dead: make(chan struct{})}
	go d.readLoop()
	d.send(proto.MustNew(proto.TypeHello, id, "", proto.Hello{
		DeviceID: id, Name: "测试设备", OS: "windows", Arch: "amd64",
		Hostname: "test-host", User: "tester", Version: "test", Protocol: proto.Version,
		Caps: []string{proto.CapWindow, proto.CapFs, proto.CapFsDelete}, Token: token,
	}))
	ack := d.waitType(proto.TypeHelloAck, 5*time.Second)
	var hello proto.HelloAck
	if err := ack.Decode(&hello); err != nil {
		t.Fatalf("解析 hello_ack 失败：%v", err)
	}
	if hello.Protocol != proto.Version {
		t.Fatalf("协议版本不一致：%s", hello.Protocol)
	}
	d.ack = hello
	t.Cleanup(func() { ws.Close() })
	return d
}

func (d *fakeDevice) readLoop() {
	defer close(d.dead)
	for {
		_, data, err := d.ws.ReadMessage()
		if err != nil {
			return
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		d.in <- env
	}
}

func (d *fakeDevice) send(env proto.Envelope) {
	d.t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		d.t.Fatalf("编码失败：%v", err)
	}
	if err := d.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		d.t.Fatalf("发送失败：%v", err)
	}
}

func (d *fakeDevice) waitType(typ string, timeout time.Duration) proto.Envelope {
	d.t.Helper()
	// 先看已缓存但类型不匹配的消息，避免把它们丢掉
	for i, env := range d.pending {
		if env.Type == typ {
			d.pending = append(d.pending[:i], d.pending[i+1:]...)
			return env
		}
	}
	deadline := time.After(timeout)
	for {
		select {
		case env, ok := <-d.in:
			if !ok {
				d.t.Fatalf("连接已断开，等不到 %s", typ)
			}
			if env.Type == typ {
				return env
			}
			d.pending = append(d.pending, env)
		case <-deadline:
			d.t.Fatalf("等待消息 %s 超时", typ)
		}
	}
}

// expectNoPending 断言没有已到达但未被消费的消息（用于验证审批闸门不放行）
func (d *fakeDevice) expectNoPending(typ string) {
	d.t.Helper()
	for _, env := range d.pending {
		if typ == "" || env.Type == typ {
			d.t.Fatalf("本不该收到消息，却收到了 %s（task=%s）", env.Type, env.TaskID)
		}
	}
}

func (d *fakeDevice) waitCommand(timeout time.Duration) proto.Command {
	d.t.Helper()
	env := d.waitType(proto.TypeCommand, timeout)
	var cmd proto.Command
	if err := env.Decode(&cmd); err != nil {
		d.t.Fatalf("解析指令失败：%v", err)
	}
	return cmd
}

// expectNothing 断言在 dur 内没收到任何消息（用于验证审批闸门不放行）
func (d *fakeDevice) expectNothing(dur time.Duration) {
	d.t.Helper()
	d.expectNoPending("")
	select {
	case env, ok := <-d.in:
		if ok {
			d.t.Fatalf("本不该收到消息，却收到了 %s（task=%s）", env.Type, env.TaskID)
		}
	case <-time.After(dur):
	}
}

func (d *fakeDevice) receipt(taskID, status string, result map[string]any, errMsg string) {
	d.send(proto.MustNew(proto.TypeReceipt, "", taskID, proto.Receipt{
		TaskID: taskID, Status: status, Result: result, Error: errMsg,
		StartedAt: time.Now().UnixMilli(), FinishedAt: time.Now().UnixMilli(),
	}))
}

func (d *fakeDevice) close() {
	_ = d.ws.Close()
	select {
	case <-d.dead:
	case <-time.After(2 * time.Second):
	}
}

/* ---------- 用例 ---------- */

// 审批闸门：危险动作未经审批不得下发；审批后下发，回执后落库并写记忆
func TestDangerousTaskRequiresApprovalThenExecutes(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-a")
	e.waitDeviceOnline("pc-a", true, 3*time.Second)

	// 事件上报（窗口）
	dev.send(proto.MustNew(proto.TypeEvent, "", "", proto.Event{
		Kind: proto.EventWindow, At: time.Now().UnixMilli(), Title: "记事本",
		Data: map[string]any{"process": "notepad.exe", "pid": 4242},
	}))

	task := e.createTask("pc-a", proto.ActionFsDelete, map[string]any{"paths": []string{"D:\\tmp\\target.txt"}})
	if task.Status != proto.TaskPendingApproval {
		t.Fatalf("危险动作必须进入待审批，实际：%s", task.Status)
	}
	if !task.NeedApproval {
		t.Fatal("needApproval 应为 true")
	}
	// 关键：未审批前设备侧不能收到任何指令
	dev.expectNothing(400 * time.Millisecond)

	// 审批通过 → 立刻下发
	var approved struct {
		Task store.Task `json:"task"`
	}
	if code := e.do("POST", "/api/tasks/"+task.ID+"/approve", map[string]any{"by": "tester"}, true, &approved); code != 200 {
		t.Fatalf("审批失败，HTTP %d", code)
	}
	if approved.Task.ApprovedBy != "tester" {
		t.Fatalf("审批人未记录：%+v", approved.Task)
	}
	cmd := dev.waitCommand(3 * time.Second)
	if cmd.TaskID != task.ID || cmd.Action != proto.ActionFsDelete {
		t.Fatalf("下发指令不对：%+v", cmd)
	}
	if !cmd.NeedApproval || cmd.ApprovedBy != "tester" {
		t.Fatalf("指令应带审批信息：%+v", cmd)
	}
	if paths, ok := cmd.Args["paths"].([]any); !ok || len(paths) != 1 || paths[0] != "D:\\tmp\\target.txt" {
		t.Fatalf("指令参数丢失：%+v", cmd.Args)
	}
	if got := e.waitTaskStatus(task.ID, proto.TaskDispatched, 2*time.Second); got.DispatchedAt == 0 {
		t.Fatal("dispatchedAt 未记录")
	}

	// 回执 → 任务收尾 + 记忆落盘
	dev.send(proto.MustNew(proto.TypeReceipt, "", task.ID, proto.Receipt{
		TaskID: task.ID, Status: proto.ReceiptRunning, StartedAt: time.Now().UnixMilli(),
	}))
	e.waitTaskStatus(task.ID, proto.TaskRunning, 2*time.Second)

	dev.receipt(task.ID, proto.ReceiptDone, map[string]any{"ok": 1, "failed": 0}, "")
	done := e.waitTaskStatus(task.ID, proto.TaskDone, 3*time.Second)
	if done.FinishedAt == 0 {
		t.Fatal("finishedAt 未记录")
	}
	if done.Result["ok"] != float64(1) {
		t.Fatalf("回执结果未落库：%+v", done.Result)
	}

	st := e.state()
	kinds := map[string]int{}
	for _, r := range st.Records {
		kinds[r.Kind]++
	}
	for _, want := range []string{proto.RecordEvent, proto.RecordCommand, proto.RecordReceipt, proto.RecordApproval} {
		if kinds[want] == 0 {
			t.Fatalf("三态记录缺少 %s：%+v", want, kinds)
		}
	}
	found := false
	for _, m := range st.Memories {
		if m.TaskID == task.ID && strings.Contains(m.Title, "任务完成") {
			found = true
		}
	}
	if !found {
		t.Fatalf("任务完成后应写入记忆：%+v", st.Memories)
	}
}

// 驳回：任务不得下发，状态为 rejected，并写记忆
func TestRejectedTaskNeverReachesDevice(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-b")
	e.waitDeviceOnline("pc-b", true, 3*time.Second)

	task := e.createTask("pc-b", proto.ActionFsDelete, map[string]any{"paths": []string{"D:\\tmp\\x.txt"}})
	var rejected struct {
		Task store.Task `json:"task"`
	}
	if code := e.do("POST", "/api/tasks/"+task.ID+"/reject", map[string]any{"by": "tester", "reason": "误删风险"}, true, &rejected); code != 200 {
		t.Fatalf("驳回失败，HTTP %d", code)
	}
	if rejected.Task.Status != proto.TaskRejected || rejected.Task.Error != "误删风险" {
		t.Fatalf("驳回状态/原因不对：%+v", rejected.Task)
	}
	dev.expectNothing(400 * time.Millisecond)

	// 重复驳回应报错（状态机保护）
	if code := e.do("POST", "/api/tasks/"+task.ID+"/reject", map[string]any{"by": "tester"}, true, nil); code == 200 {
		t.Fatal("已收尾的任务不应允许再次驳回")
	}
}

// 非危险动作直接下发；设备离线则排队，上线后自动补发
func TestOfflineQueueThenResendOnReconnect(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-c")
	e.waitDeviceOnline("pc-c", true, 3*time.Second)
	dev.close()
	e.waitDeviceOnline("pc-c", false, 3*time.Second)

	task := e.createTask("pc-c", proto.ActionPing, nil)
	if task.Status != proto.TaskQueued {
		t.Fatalf("设备离线时任务应排队，实际：%s", task.Status)
	}

	dev2 := newDevice(t, e.srv.URL, testToken, "pc-c")
	if dev2.ack.QueuedTasks != 1 {
		t.Fatalf("上线时应告知补发 1 条排队任务，实际 %d", dev2.ack.QueuedTasks)
	}
	cmd := dev2.waitCommand(3 * time.Second)
	if cmd.TaskID != task.ID || cmd.Action != proto.ActionPing {
		t.Fatalf("上线补发的指令不对：%+v", cmd)
	}
	dev2.receipt(task.ID, proto.ReceiptDone, map[string]any{"pong": true}, "")
	e.waitTaskStatus(task.ID, proto.TaskDone, 3*time.Second)
}

// 回执超时：设备在线但不回执，超时后任务判失败并写记忆
func TestReceiptTimeoutMarksFailed(t *testing.T) {
	e := newEnv(t, 150*time.Millisecond)
	dev := newDevice(t, e.srv.URL, testToken, "pc-d")
	e.waitDeviceOnline("pc-d", true, 3*time.Second)

	task := e.createTask("pc-d", proto.ActionSysInfo, nil)
	dev.waitCommand(3 * time.Second)
	e.waitTaskStatus(task.ID, proto.TaskDispatched, 2*time.Second)

	time.Sleep(200 * time.Millisecond)
	n, err := e.hub.SweepOnce()
	if err != nil {
		t.Fatalf("超时扫描失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("应扫出 1 个超时任务，实际 %d", n)
	}
	failed := e.waitTaskStatus(task.ID, proto.TaskFailed, 2*time.Second)
	if !strings.Contains(failed.Error, "回执超时") {
		t.Fatalf("失败原因应为回执超时：%+v", failed)
	}
	ok := false
	for _, m := range e.state().Memories {
		if m.TaskID == task.ID && strings.Contains(m.Title, "任务失败") {
			ok = true
		}
	}
	if !ok {
		t.Fatal("超时失败也应写记忆")
	}
}

// 端侧拒绝：状态落 rejected
func TestDeviceRejectionIsRecorded(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-e")
	e.waitDeviceOnline("pc-e", true, 3*time.Second)

	task := e.createTask("pc-e", proto.ActionFsDelete, map[string]any{"paths": []string{"D:\\tmp\\y.txt"}})
	if code := e.do("POST", "/api/tasks/"+task.ID+"/approve", map[string]any{"by": "tester"}, true, nil); code != 200 {
		t.Fatalf("审批失败，HTTP %d", code)
	}
	dev.waitCommand(3 * time.Second)
	dev.receipt(task.ID, proto.ReceiptRejected, nil, "本端为 dry-run 档位，拒绝真正删除")
	got := e.waitTaskStatus(task.ID, proto.TaskRejected, 3*time.Second)
	if !strings.Contains(got.Error, "dry-run") {
		t.Fatalf("端侧拒绝原因应落库：%+v", got)
	}
}

// 令牌：错误令牌注册失败；HTTP 接口无令牌 401
func TestTokenEnforcement(t *testing.T) {
	e := newEnv(t, 5*time.Second)

	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws/device"
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer ws.Close()
	b, _ := json.Marshal(proto.MustNew(proto.TypeHello, "pc-x", "", proto.Hello{DeviceID: "pc-x", Token: "wrong"}))
	if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("应收到错误消息，实际读取失败：%v", err)
	}
	var env proto.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != proto.TypeError {
		t.Fatalf("应返回 error 消息：%s", string(data))
	}
	var em proto.ErrorMsg
	if err := env.Decode(&em); err != nil || em.Code != "register_failed" {
		t.Fatalf("错误码不对：%+v", em)
	}

	if code := e.do("POST", "/api/tasks", map[string]any{"deviceId": "pc-x", "action": "ping"}, false, nil); code != 401 {
		t.Fatalf("无令牌访问接口应 401，实际 %d", code)
	}
}

// 非法输入：未知动作 / 未注册设备必须明确报错
func TestCreateTaskValidation(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	dev := newDevice(t, e.srv.URL, testToken, "pc-f")
	e.waitDeviceOnline("pc-f", true, 3*time.Second)
	_ = dev

	for _, tc := range []struct {
		name     string
		deviceID string
		action   string
	}{
		{"未知动作", "pc-f", "sys.exec"},
		{"未注册设备", "pc-ghost", "ping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := e.do("POST", "/api/tasks", map[string]any{
				"deviceId": tc.deviceID, "action": tc.action,
			}, true, nil); code != 400 {
				t.Fatalf("应返回 400，实际 %d", code)
			}
		})
	}
}

// 控制台页面与健康检查可用
func TestPanelAndHealth(t *testing.T) {
	e := newEnv(t, 5*time.Second)

	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatalf("打开控制台失败：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "白泽智能体") {
		t.Fatalf("控制台页面异常：HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), testToken) {
		t.Fatal("控制台页面应注入配对令牌供本机使用")
	}

	var health map[string]any
	if code := e.do("GET", "/api/health", nil, true, &health); code != 200 || health["ok"] != true {
		t.Fatalf("健康检查异常：HTTP %d %+v", code, health)
	}

	var st stateResp
	e.do("GET", "/api/state", nil, true, &st)
	if len(st.Actions) == 0 {
		t.Fatal("控制台需要动作白名单")
	}
}

// 面板页的令牌闸门：页面里嵌着配对令牌，跨机（非回环）访问必须先验证
func TestPanelTokenGate(t *testing.T) {
	e := newEnv(t, 5*time.Second)
	handler := e.srv.Config.Handler

	// 直接打 handler，好在测试里假装请求来自局域网
	hit := func(target, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.RemoteAddr = remoteAddr
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	// 跨机、没带令牌：只给登录页，不能把嵌着令牌的面板页发出去
	rr := hit("/", "192.168.1.50:41234", nil)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "配对令牌") {
		t.Fatalf("跨机裸访问应给登录页：HTTP %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), testToken) {
		t.Fatal("登录页里不该出现配对令牌")
	}

	// 令牌给错：一样只给登录页
	if rr := hit("/?token=nope", "192.168.1.50:41234", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401：HTTP %d", rr.Code)
	}

	// 令牌给对：种 cookie 并跳回干净的 /
	rr = hit("/?token="+testToken, "192.168.1.50:41234", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("正确令牌应 302 跳转：HTTP %d", rr.Code)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "bz_panel_token" || cookies[0].Value != testToken {
		t.Fatalf("验证通过后应种下面板 cookie：%+v", cookies)
	}

	// 带 cookie 再来：放行（页面里照旧注入令牌给本页 JS 调接口用）
	if rr := hit("/", "192.168.1.50:41234", map[string]string{"Cookie": "bz_panel_token=" + testToken}); rr.Code != 200 ||
		!strings.Contains(rr.Body.String(), "白泽智能体") || !strings.Contains(rr.Body.String(), testToken) {
		t.Fatalf("带 cookie 应放行面板页：HTTP %d", rr.Code)
	}
	// 脚本/curl 走头这条路：也放行
	if rr := hit("/", "192.168.1.50:41234", map[string]string{"X-Baize-Token": testToken}); rr.Code != 200 {
		t.Fatalf("带 X-Baize-Token 头应放行：HTTP %d", rr.Code)
	}

	// 本机回环：免验证，直接给面板
	if rr := hit("/", "127.0.0.1:45678", nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), "白泽智能体") {
		t.Fatalf("回环访问应免验证：HTTP %d", rr.Code)
	}
}
