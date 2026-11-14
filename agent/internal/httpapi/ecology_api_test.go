package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baize/internal/plugins"
)

// ecoRaw 发带令牌请求，返回状态码与原始 body（4xx 也读出来核对中文报错）。
func ecoRaw(e *env, method, path string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("序列化请求失败：%v", err)
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Baize-Token", e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("请求失败 %s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("读响应失败：%v", err)
	}
	return resp.StatusCode, b
}

// ecoDecode 把 JSON 解成 map，省掉 struct tag（也避免测试源码里出现反引号字符）。
func ecoDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("响应不是合法 JSON：%v；body=%s", err, body)
	}
	return m
}

func ecoNum(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return -1
}

func ecoStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// ecoWriteDSH 最小 DSH 格式 1 索引（顶层 format、无 schema）。
func ecoWriteDSH(t *testing.T) string {
	t.Helper()
	entry := func(name string, ver string, repo string) map[string]any {
		return map[string]any{
			"name": name, "title": name, "description": "目录条目",
			"repository": map[string]any{"url": repo},
			"metadata":   map[string]any{"category": "tool", "keywords": []string{}, "labels": []string{}, "sortKeys": map[string]any{}},
			"versions": []any{map[string]any{
				"version": ver, "publishTimestamp": 1,
				"source": map[string]any{"type": "npm"}, "engines": map[string]any{},
			}},
		}
	}
	doc := map[string]any{
		"format": 1,
		"title":  "端到端测试生态市场",
		"metadata": map[string]any{
			"categories": map[string]any{}, "labels": map[string]any{}, "sortKeys": map[string]any{},
		},
		"plugins": []any{
			entry("dsh-e2e-one", "1.0.0", "https://github.com/example/one"),
			entry("dsh-e2e-two", "2.0.0", "https://github.com/example/two"),
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化测试索引失败：%v", err)
	}
	p := filepath.Join(t.TempDir(), "dsh.json")
	if err := writeFile(p, string(b)); err != nil {
		t.Fatalf("写测试索引失败：%v", err)
	}
	return p
}

// TestEcologyGetDshCatalog probe=0 只列目录、不打网络，返回真实 Catalog。
func TestEcologyGetDshCatalog(t *testing.T) {
	e, _ := newAgentEnv(t)
	p := ecoWriteDSH(t)
	path := "/api/agent/ecology/dsh?index=" + url.QueryEscape(p) + "&probe=0"
	code, body := ecoRaw(e, "GET", path, nil)
	if code != http.StatusOK {
		t.Fatalf("GET 应 200，实际 %d：%s", code, body)
	}
	m := ecoDecode(t, body)
	if ecoStr(m, "source") != p || ecoStr(m, "title") != "端到端测试生态市场" {
		t.Fatalf("来源/标题不对：%+v", m)
	}
	if ecoNum(m, "total") != 2 || ecoNum(m, "directory") != 2 || ecoNum(m, "installable") != 0 {
		t.Fatalf("纯目录模式统计不对：total=%d dir=%d installable=%d", ecoNum(m, "total"), ecoNum(m, "directory"), ecoNum(m, "installable"))
	}
	entries, _ := m["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(entries))
	}
	for _, it := range entries {
		em, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("条目不是对象：%T", it)
		}
		if !strings.Contains(ecoStr(em, "reason"), "纯目录模式") {
			t.Errorf("%s 没写清纯目录模式：%q", ecoStr(em, "id"), ecoStr(em, "reason"))
		}
	}
}

// TestEcologySyncProducesBaizeIndex sync 产物是可被白泽市场解析的 index.json（plugins 是数组不是 null）。
func TestEcologySyncProducesBaizeIndex(t *testing.T) {
	e, _ := newAgentEnv(t)
	p := ecoWriteDSH(t)
	code, body := ecoRaw(e, "POST", "/api/agent/ecology/dsh/sync", map[string]any{
		"index": p, "name": "e2e-eco", "probe": false,
	})
	if code != http.StatusOK {
		t.Fatalf("sync 应 200，实际 %d：%s", code, body)
	}
	m := ecoDecode(t, body)
	if m["ok"] != true || ecoStr(m, "indexPath") == "" {
		t.Fatalf("sync 没成功或没给 indexPath：%+v", m)
	}
	if ecoNum(m, "built") != 0 || ecoNum(m, "skipped") != 2 {
		t.Fatalf("纯目录模式应 0 打包 / 2 跳过，实际 built=%d skipped=%d", ecoNum(m, "built"), ecoNum(m, "skipped"))
	}
	raw, err := os.ReadFile(ecoStr(m, "indexPath"))
	if err != nil {
		t.Fatalf("读 sync 产物 index.json 失败：%v", err)
	}
	var idx plugins.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("sync 产物不是白泽格式索引：%v；body=%s", err, raw)
	}
	if idx.Schema != plugins.SchemaVersion {
		t.Fatalf("产物 schema 应为 %d，实际 %d", plugins.SchemaVersion, idx.Schema)
	}
	if idx.Plugins == nil {
		t.Fatal("产物 plugins 不能是 null（空数组才合法），否则 register 会拒收纯目录产物")
	}
	if len(idx.Plugins) != 0 {
		t.Fatalf("纯目录模式不该产出可安装包，实际 %d 条", len(idx.Plugins))
	}
}

// TestEcologyRegisterRejectsRawDshIndex 原始 DSH 格式 1 索引必须 400 + 中文指引，且不写 config。
func TestEcologyRegisterRejectsRawDshIndex(t *testing.T) {
	e, svc := newAgentEnv(t)
	p := ecoWriteDSH(t)
	before := len(svc.PluginSources())
	code, body := ecoRaw(e, "POST", "/api/agent/ecology/dsh/register", map[string]any{
		"name": "不该被登记", "url": p,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("原始 DSH 索引应 400，实际 %d：%s", code, body)
	}
	s := string(body)
	if !strings.Contains(s, "DSH 生态格式 1 索引") || !strings.Contains(s, "/api/agent/ecology/dsh/sync") {
		t.Fatalf("400 没给出先 sync 的中文指引：%s", s)
	}
	if got := len(svc.PluginSources()); got != before {
		t.Fatalf("校验失败不该写 config：源数 %d -> %d", before, got)
	}
}

// TestEcologyRegisterAcceptsBaizeIndex 合法白泽索引登记成功并回显在 PluginSources 里。
func TestEcologyRegisterAcceptsBaizeIndex(t *testing.T) {
	e, svc := newAgentEnv(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	idx := map[string]any{
		"schema": 1,
		"name":   "端到端白泽源",
		"plugins": []any{map[string]any{
			"id": "eco-demo", "name": "eco-demo", "version": "1.0.0",
			"url": "https://example.com/eco-demo.zip",
		}},
	}
	b, _ := json.Marshal(idx)
	if err := writeFile(p, string(b)); err != nil {
		t.Fatalf("写白泽索引失败：%v", err)
	}
	code, body := ecoRaw(e, "POST", "/api/agent/ecology/dsh/register", map[string]any{
		"name": "端到端生态源", "url": p,
	})
	if code != http.StatusOK {
		t.Fatalf("合法白泽索引应 200，实际 %d：%s", code, body)
	}
	m := ecoDecode(t, body)
	if m["ok"] != true {
		t.Fatalf("register 没成功：%+v", m)
	}
	found := false
	for _, src := range svc.PluginSources() {
		if src.Name == "端到端生态源" && src.URL == p && src.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("svc.PluginSources() 里找不到刚登记的源：%+v", svc.PluginSources())
	}
}

// TestEcologyRegisterRejectsBadIndex 缺 schema / plugins 非数组 / 条目缺字段都要明确报错。
func TestEcologyRegisterRejectsBadIndex(t *testing.T) {
	e, svc := newAgentEnv(t)
	dir := t.TempDir()
	before := len(svc.PluginSources())
	cases := []struct {
		name string
		doc  map[string]any
		want string
	}{
		{"缺 schema", map[string]any{"plugins": []any{}}, "缺少 schema"},
		{"schema 不认识", map[string]any{"schema": 9, "plugins": []any{}}, "不认识"},
		{"plugins 不是数组", map[string]any{"schema": 1, "plugins": map[string]any{}}, "plugins 解析失败"},
		{"条目缺 id", map[string]any{"schema": 1, "plugins": []any{map[string]any{"url": "x.zip"}}}, "解析不出 id 或 url"},
		{"条目缺 url", map[string]any{"schema": 1, "plugins": []any{map[string]any{"id": "x"}}}, "解析不出 id 或 url"},
	}
	for _, c := range cases {
		p := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "_")+".json")
		b, _ := json.Marshal(c.doc)
		if err := writeFile(p, string(b)); err != nil {
			t.Fatalf("写坏索引失败：%v", err)
		}
		code, body := ecoRaw(e, "POST", "/api/agent/ecology/dsh/register", map[string]any{"name": c.name, "url": p})
		if code != http.StatusBadRequest {
			t.Errorf("%s 应 400，实际 %d：%s", c.name, code, body)
			continue
		}
		if !strings.Contains(string(body), c.want) {
			t.Errorf("%s 报错没说清原因（期望含 %q）：%s", c.name, c.want, body)
		}
	}
	if got := len(svc.PluginSources()); got != before {
		t.Fatalf("任何校验失败都不该写 config：源数 %d -> %d", before, got)
	}
}
