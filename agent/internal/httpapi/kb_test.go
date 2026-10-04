package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"baize/core"
	"baize/internal/httpapi"
	"baize/internal/hub"
	"baize/internal/kb"
	"baize/internal/logx"
	"baize/internal/store"
)

// newKBEnv 起一个带知识库的后端（手机端读的那两个接口就在这儿）
func newKBEnv(t *testing.T) (*httptest.Server, *kb.Service) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/agent.db")
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, lg, testToken, 5*time.Second)

	k, err := kb.Open(t.TempDir(), lg)
	if err != nil {
		t.Fatalf("打开知识库失败：%v", err)
	}
	s := httpapi.New(h, lg, logx.NewRing(100), "test")
	s.SetKB(k)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, k
}

func kbDo(t *testing.T, srv *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Baize-Token", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败 %s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// 后端知识库：加待办 / 加密码 / 取快照，走的是与手机内核同一套 op 口径
func TestKBStateAndOps(t *testing.T) {
	srv, _ := newKBEnv(t)

	var state core.Snapshot
	if code := kbDo(t, srv, "GET", "/api/kb/state", nil, &state); code != 200 {
		t.Fatalf("取知识库快照失败：HTTP %d", code)
	}
	if len(state.Todos) != 0 {
		t.Fatalf("新知识库应该是空的，实际 %d 条待办", len(state.Todos))
	}

	var added struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	code := kbDo(t, srv, "POST", "/api/kb/op", map[string]any{
		"op": "todo.add", "args": map[string]any{"title": "买牛奶", "form": "leisure"},
	}, &added)
	if code != 200 || !added.OK {
		t.Fatalf("加待办失败：HTTP %d %+v", code, added)
	}

	code = kbDo(t, srv, "POST", "/api/kb/op", map[string]any{
		"op": "vault.merge",
		"args": map[string]any{
			"title": "GitHub", "account": "me@a.com", "password": "s3cret",
		},
	}, &added)
	if code != 200 || !added.OK {
		t.Fatalf("加密码失败：HTTP %d %+v", code, added)
	}

	kbDo(t, srv, "GET", "/api/kb/state", nil, &state)
	if len(state.Todos) != 1 || state.Todos[0].Title != "买牛奶" {
		t.Fatalf("待办没写进去：%+v", state.Todos)
	}
	if len(state.Vault) != 1 || state.Vault[0].Title != "GitHub" {
		t.Fatalf("密码没写进去：%+v", state.Vault)
	}

	// 操作失败要如实回错误，不能只看 HTTP 200
	var failed struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	kbDo(t, srv, "POST", "/api/kb/op", map[string]any{"op": "todo.remove", "args": map[string]any{"id": "不存在"}}, &failed)
	if failed.OK || failed.Error == "" {
		t.Fatalf("删不存在的待办必须明确报错：%+v", failed)
	}

	// 没令牌一律拒绝
	req, _ := http.NewRequest("GET", srv.URL+"/api/kb/state", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("没带令牌应 401，实际 %d", resp.StatusCode)
	}
}

// 附件：手机上不留文件，上传到后端、取回、列出、删除都要能用
func TestKBFileRoundTrip(t *testing.T) {
	srv, _ := newKBEnv(t)
	content := []byte("白泽知识库附件测试内容")
	b64 := base64.StdEncoding.EncodeToString(content)

	var up struct {
		File kb.FileInfo `json:"file"`
	}
	code := kbDo(t, srv, "POST", "/api/kb/files", map[string]any{
		"name": "笔记.md", "kind": "file", "mime": "text/markdown", "dataBase64": b64,
	}, &up)
	if code != 200 || up.File.ID == "" {
		t.Fatalf("上传附件失败：HTTP %d %+v", code, up)
	}

	var got struct {
		File       kb.FileInfo `json:"file"`
		DataBase64 string      `json:"dataBase64"`
	}
	if code := kbDo(t, srv, "GET", "/api/kb/files/"+up.File.ID, nil, &got); code != 200 {
		t.Fatalf("下载附件失败：HTTP %d", code)
	}
	back, err := base64.StdEncoding.DecodeString(got.DataBase64)
	if err != nil || !bytes.Equal(back, content) {
		t.Fatalf("取回的内容和上传的不一致（err=%v）", err)
	}
	if got.File.Name != "笔记.md" {
		t.Fatalf("文件名丢了：%+v", got.File)
	}

	var list struct {
		Files []kb.FileInfo `json:"files"`
	}
	kbDo(t, srv, "GET", "/api/kb/files", nil, &list)
	if len(list.Files) != 1 {
		t.Fatalf("附件列表应有 1 条，实际 %d", len(list.Files))
	}

	var del struct {
		Removed string `json:"removed"`
	}
	if code := kbDo(t, srv, "DELETE", "/api/kb/files/"+up.File.ID, nil, &del); code != 200 {
		t.Fatalf("删附件失败：HTTP %d", code)
	}
	kbDo(t, srv, "GET", "/api/kb/files", nil, &list)
	if len(list.Files) != 0 {
		t.Fatalf("删完列表应为空，实际 %d", len(list.Files))
	}
}

// 附件 id 只认内容哈希，带 ../ 这种路径穿越的必须被挡掉
func TestKBFileRejectsBadID(t *testing.T) {
	srv, _ := newKBEnv(t)
	var out map[string]any
	code := kbDo(t, srv, "GET", "/api/kb/files/..%2f..%2fdata.json", nil, &out)
	if code == 200 {
		t.Fatalf("非法附件 id 不该成功：%+v", out)
	}
}
