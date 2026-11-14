package ecology

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// dshFixture 造一条 DSH 格式 1 的插件记录（纯数据，不联网）。
func dshFixture(name, repo string) map[string]any {
	return map[string]any{
		"name":        name,
		"title":       name,
		"description": "示例插件",
		"repository":  map[string]any{"url": repo},
		"metadata": map[string]any{
			"category": "tool",
			"keywords": []string{},
			"labels":   []string{},
			"sortKeys": map[string]float64{},
		},
		"versions": []any{map[string]any{
			"version": "1.0.0", "publishTimestamp": 1,
			"source":  map[string]any{"type": "npm"},
			"engines": map[string]any{},
		}},
	}
}

// writeDSH 写一份临时 DSH 格式 1 索引（复用 ecology_test.go 的 writeIndex）。
func writeDSH(t *testing.T, plugins ...map[string]any) string {
	t.Helper()
	doc := map[string]any{
		"format": 1,
		"title":  "测试生态市场",
		"metadata": map[string]any{
			"categories": map[string]any{},
			"labels":     map[string]any{},
			"sortKeys":   map[string]any{},
		},
		"plugins": plugins,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化测试索引失败：%v", err)
	}
	return writeIndex(t, string(b))
}

// TestFetchProbeSameRepoOnlyOnce 同仓库只真调一次 probe（缓存命中削减调用）。
func TestFetchProbeSameRepoOnlyOnce(t *testing.T) {
	const n = 24
	plugins := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		plugins = append(plugins, dshFixture(fmt.Sprintf("dsh-shared-%02d", i), "https://github.com/example/shared"))
	}
	path := writeDSH(t, plugins...)

	var calls int32
	probe := func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(5 * time.Millisecond)
		return &ProbeInfo{Kind: KindSkill, SkillPath: "SKILL.md"}, nil
	}
	cat, err := FetchWithOptions(path, FetchOptions{
		Probe: probe, Concurrency: 8,
		ProbeTimeout: time.Second, TotalTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("同仓库应只真调 1 次探测，实际 %d 次", got)
	}
	if cat.Total != n || cat.Installable != n {
		t.Fatalf("条目统计不对：total=%d installable=%d", cat.Total, cat.Installable)
	}
}

// TestFetchProbeRunsConcurrentlyAndKeepsOrder 有界并发生效，且两次输出顺序一致。
func TestFetchProbeRunsConcurrentlyAndKeepsOrder(t *testing.T) {
	const n = 16
	plugins := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		plugins = append(plugins, dshFixture(fmt.Sprintf("dsh-c-%02d", i), fmt.Sprintf("https://github.com/example/r%02d", i)))
	}
	path := writeDSH(t, plugins...)

	var cur, peak int32
	probe := func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		c := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&peak)
			if c <= m || atomic.CompareAndSwapInt32(&peak, m, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		return &ProbeInfo{Kind: KindTool, MCP: []mcpServer{{Name: "srv", Type: "stdio", Command: "node"}}}, nil
	}
	run := func() []string {
		cat, err := FetchWithOptions(path, FetchOptions{
			Probe: probe, Concurrency: 4,
			ProbeTimeout: 2 * time.Second, TotalTimeout: 10 * time.Second,
		})
		if err != nil {
			t.Fatalf("Fetch 失败：%v", err)
		}
		ids := make([]string, 0, len(cat.Entries))
		for _, e := range cat.Entries {
			ids = append(ids, e.ID)
		}
		return ids
	}
	first, second := run(), run()
	if got := atomic.LoadInt32(&peak); got < 2 || got > 4 {
		t.Fatalf("并发峰值应在 2~4 之间（worker=4），实际 %d", got)
	}
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("并发下输出顺序不稳定：%s / %s", strings.Join(first, ","), strings.Join(second, ","))
	}
	if len(first) != n {
		t.Fatalf("条目数不对：%d", len(first))
	}
}

// TestFetchProbeTimeoutDoesNotDropEntries 探测超时的条目不丢，按失败写 Reason。
func TestFetchProbeTimeoutDoesNotDropEntries(t *testing.T) {
	const n = 6
	plugins := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		plugins = append(plugins, dshFixture(fmt.Sprintf("dsh-slow-%02d", i), fmt.Sprintf("https://github.com/example/slow%02d", i)))
	}
	path := writeDSH(t, plugins...)

	probe := func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		time.Sleep(200 * time.Millisecond)
		return &ProbeInfo{Kind: KindSkill}, nil
	}
	cat, err := FetchWithOptions(path, FetchOptions{
		Probe: probe, Concurrency: 3,
		ProbeTimeout: 20 * time.Millisecond, TotalTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if cat.Total != n {
		t.Fatalf("超时的条目不该丢，期望 %d 条，实际 %d", n, cat.Total)
	}
	if cat.Installable != 0 {
		t.Errorf("探测超时的条目不该标成可安装，实际 %d 条", cat.Installable)
	}
	for _, e := range cat.Entries {
		if !strings.Contains(e.Reason, "探测超时") {
			t.Errorf("%s 的 Reason 没写清超时：%q", e.ID, e.Reason)
		}
	}
	if len(cat.Errors) != 0 {
		t.Errorf("探测超时不是转换错误，cat.Errors 应为空：%v", cat.Errors)
	}
}

// TestFetchPureDirectoryModeDoesNotProbe probe=nil 是真·纯目录模式（不联网、不猜形态）。
func TestFetchPureDirectoryModeDoesNotProbe(t *testing.T) {
	path := writeDSH(t,
		dshFixture("dsh-a", "https://github.com/example/a"),
		dshFixture("dsh-b", "https://github.com/example/b"),
	)
	cat, err := Fetch(path, nil)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if cat.Total != 2 || cat.Installable != 0 {
		t.Fatalf("纯目录模式应 2 条都只展示：total=%d installable=%d", cat.Total, cat.Installable)
	}
	for _, e := range cat.Entries {
		if e.Kind != KindUnknown || !strings.Contains(e.Reason, "纯目录模式") {
			t.Errorf("%s 纯目录模式判定不对：kind=%s reason=%q", e.ID, e.Kind, e.Reason)
		}
	}
}

// TestFetchNegativeResultIsCached 探测失败也缓存（负缓存），同仓库不重复打。
func TestFetchNegativeResultIsCached(t *testing.T) {
	path := writeDSH(t,
		dshFixture("dsh-broken-one", "https://github.com/example/broken"),
		dshFixture("dsh-broken-two", "https://github.com/example/broken"),
	)
	var calls int32
	probe := func(name string, v dshVersion, repo string) (*ProbeInfo, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("仓库读不到")
	}
	cat, err := FetchWithOptions(path, FetchOptions{
		Probe: probe, Concurrency: 4,
		ProbeTimeout: time.Second, TotalTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("同仓库探测失败也应只真调 1 次（负缓存），实际 %d 次", got)
	}
	if cat.Total != 2 {
		t.Fatalf("两条都该保留，实际 %d", cat.Total)
	}
	for _, e := range cat.Entries {
		if !strings.Contains(e.Reason, "没能探测到内容形态") {
			t.Errorf("%s 没写清探测失败：%q", e.ID, e.Reason)
		}
	}
}

// TestFetchErrorsAreSortedByID 并发下 cat.Errors 按 id 稳定排序。
func TestFetchErrorsAreSortedByID(t *testing.T) {
	bad := func(name string) map[string]any {
		return map[string]any{"name": name, "versions": []any{}}
	}
	path := writeDSH(t, bad("dsh-zeta"), bad("dsh-alpha"), bad("dsh-mid"))
	cat, err := Fetch(path, nil)
	if err != nil {
		t.Fatalf("Fetch 失败：%v", err)
	}
	if len(cat.Errors) != 3 {
		t.Fatalf("应有 3 条转换错误，实际 %d：%v", len(cat.Errors), cat.Errors)
	}
	if !strings.Contains(cat.Errors[0], "dsh-alpha") || !strings.Contains(cat.Errors[2], "dsh-zeta") {
		t.Fatalf("错误没按 id 排序：%v", cat.Errors)
	}
}
