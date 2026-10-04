package httpapi_test

import (
	"os"
	"path/filepath"
	"testing"
)

// 桌面端分发包的下载路由：/dl/{name}
// 它要满足：能取到包；挡住路径穿越 / 隐藏文件 / 不存在的名字；没令牌不给。
func TestDownloadRoute(t *testing.T) {
	e, svc := newAgentEnv(t)

	dir := filepath.Join(svc.DataDir(), "dl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), []byte(`{"version":"9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if code := e.do("GET", "/dl/latest.json", nil, true, &got); code != 200 {
		t.Fatalf("取升级清单期望 200，实际 %d", code)
	}
	if got["version"] != "9.9.9" {
		t.Fatalf("清单内容不对：%+v", got)
	}

	for _, bad := range []string{"/dl/.hidden", "/dl/nope.json", "/dl/a/b"} {
		var out map[string]any
		if code := e.do("GET", bad, nil, true, &out); code == 200 {
			t.Fatalf("%s 不该被放行", bad)
		}
	}

	var noTok map[string]any
	if code := e.do("GET", "/dl/latest.json", nil, false, &noTok); code == 200 {
		t.Fatalf("没令牌不该给升级包")
	}
}
