package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	uaPC    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"
	uaPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
)

// webGet 按给定 UA 拉一次首页或静态资源，回 (状态码, Content-Type, 正文)
func webGet(t *testing.T, srv *httptest.Server, path, ua string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("X-Baize-Token", testToken) // 面板页认这个头（省去 cookie）
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败 %s：%v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

// WebUI 双版本：同一地址按 UA 分「电脑版 / 手机版」，?ver= 可强制切
func TestWebUIVariants(t *testing.T) {
	srv, _ := newKBEnv(t)

	code, ctype, body := webGet(t, srv, "/", uaPC)
	if code != 200 {
		t.Fatalf("电脑版首页状态 %d", code)
	}
	if !strings.Contains(ctype, "text/html") {
		t.Fatalf("电脑版首页 Content-Type=%q", ctype)
	}
	if !strings.Contains(body, "<title>白泽智能体 · 控制台</title>") {
		t.Fatal("电脑版没拿到 panel.html")
	}
	if strings.Contains(body, "webui-bridge.js") {
		t.Fatal("电脑版里混进了手机版脚本")
	}
	if !strings.Contains(body, testToken) {
		t.Fatal("电脑版没注入配对令牌")
	}
	if strings.Contains(body, "__BAIZE_TOKEN__") {
		t.Fatal("电脑版令牌占位符没替换干净")
	}
	if !strings.Contains(body, `src="/icon.png"`) {
		t.Fatal("电脑版没换成应用图标")
	}

	code, _, body = webGet(t, srv, "/", uaPhone)
	if code != 200 {
		t.Fatalf("手机版首页状态 %d", code)
	}
	if !strings.Contains(body, "<title>白泽 · Baize</title>") {
		t.Fatal("手机版没拿到 todo-app 那套前端")
	}
	if !strings.Contains(body, "js/webui-bridge.js") {
		t.Fatal("手机版没有换成「后端直连」层")
	}
	if strings.Contains(body, "js/device.js") {
		t.Fatal("手机版还挂着本机内核桥 device.js（浏览器里没有内核）")
	}
	if !strings.Contains(body, `name="baize-token" content="`+testToken+`"`) {
		t.Fatal("手机版没把配对令牌注入 <meta>")
	}
	if strings.Contains(body, "__BAIZE_TOKEN_ATTR__") {
		t.Fatal("手机版令牌占位符没替换干净")
	}
	// 手机版不许再出现 __BAIZE_TOKEN__：它曾被同时用作 JS 变量名，被替换成 `window. = "";` 直接语法错误
	if strings.Contains(body, "__BAIZE_TOKEN__") {
		t.Fatal("手机版里不该再出现 __BAIZE_TOKEN__（会跟 JS 变量名撞名）")
	}

	// ?ver= 强制切换：电脑 UA 看手机版、手机 UA 看电脑版，都得听
	if _, _, b := webGet(t, srv, "/?ver=mobile", uaPC); !strings.Contains(b, "webui-bridge.js") {
		t.Fatal("?ver=mobile 没强制成手机版")
	}
	if _, _, b := webGet(t, srv, "/?ver=desktop", uaPhone); !strings.Contains(b, "<title>白泽智能体 · 控制台</title>") {
		t.Fatal("?ver=desktop 没强制成电脑版")
	}
}

// 手机版的静态资源与图标：类型对、内容在、目录与穿越一律 404
func TestWebUIAssetsAndIcon(t *testing.T) {
	srv, _ := newKBEnv(t)

	code, ctype, body := webGet(t, srv, "/js/webui-bridge.js", uaPhone)
	if code != 200 || !strings.Contains(ctype, "javascript") {
		t.Fatalf("webui-bridge.js 状态 %d 类型 %q", code, ctype)
	}
	if !strings.Contains(body, "__bzWeb") {
		t.Fatal("webui-bridge.js 里没有网页版标志 __bzWeb")
	}
	if !strings.Contains(body, "XMLHttpRequest") {
		t.Fatal("webui-bridge.js 没用同步 XHR（Store/kb 依赖同步语义）")
	}
	if !strings.Contains(body, "/api/kb/op") {
		t.Fatal("webui-bridge.js 没把内核 /api/op 映到后端 /api/kb/op")
	}
	if !strings.Contains(body, "'/api/state': '/api/kb/state'") {
		t.Fatal("webui-bridge.js 没把内核 /api/state 映到后端 /api/kb/state（漏了它待办就会是空的）")
	}
	if !strings.Contains(body, "BE_PREFIX") {
		t.Fatal("webui-bridge.js 没照抄内核的 /api/be 通用透传前缀（任务审批/日志页会坏）")
	}

	if code, ctype, _ = webGet(t, srv, "/css/app.css", uaPhone); code != 200 || !strings.Contains(ctype, "text/css") {
		t.Fatalf("app.css 状态 %d 类型 %q", code, ctype)
	}
	if code, ctype, _ = webGet(t, srv, "/manifest.webmanifest", uaPhone); code != 200 || !strings.Contains(ctype, "manifest") {
		t.Fatalf("manifest 状态 %d 类型 %q", code, ctype)
	}

	// 朗读：网页版必须走同源（原先写死 127.0.0.1:<内核端口>，浏览器里必然取不到音频）
	vbody := ""
	if code, _, vbody = webGet(t, srv, "/js/voice.js", uaPhone); code != 200 || !strings.Contains(vbody, "origin: location.origin") {
		t.Fatalf("voice.js 没给网页版接上同源朗读（状态 %d）", code)
	}

	for _, p := range []string{"/icon.png", "/favicon.ico"} {
		code, ctype, body = webGet(t, srv, p, uaPC)
		if code != 200 || ctype != "image/png" {
			t.Fatalf("%s 状态 %d 类型 %q", p, code, ctype)
		}
		if len(body) < 8 || !strings.HasPrefix(body, "\x89PNG\r\n\x1a\n") {
			t.Fatalf("%s 不是 PNG", p)
		}
	}

	// 目录请求 / 不存在的文件 / 穿越包外：一律不许 200
	for _, p := range []string{"/js/", "/css/", "/js/nope.js", "/js/../../go.mod"} {
		if code, _, _ := webGet(t, srv, p, uaPhone); code == 200 {
			t.Fatalf("该拒绝的路径回了 200：%s", p)
		}
	}
}
