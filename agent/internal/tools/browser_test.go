package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// mockCDP 一个假浏览器调试端点：够验证 CDP 客户端的"发命令 / 收事件 / 取结果"。
// 不跑真浏览器 —— 真浏览器只在人工验收时用（tests 里要确定、要快）。
func mockCDP(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var wsURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
	})
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
	})
	mux.HandleFunc("/devtools/page/1", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			var params struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(msg.Params, &params)

			reply := func(result any, emitEvent string) {
				if emitEvent != "" {
					b, _ := json.Marshal(map[string]any{"method": emitEvent, "params": map[string]any{}})
					_ = c.WriteMessage(websocket.TextMessage, b)
				}
				b, _ := json.Marshal(map[string]any{"id": msg.ID, "result": result})
				_ = c.WriteMessage(websocket.TextMessage, b)
			}
			switch msg.Method {
			case "Page.navigate":
				reply(map[string]any{"frameId": "F1"}, "Page.loadEventFired")
			case "Page.captureScreenshot":
				reply(map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("PNGDATA"))}, "")
			case "Runtime.evaluate":
				reply(map[string]any{"result": map[string]any{"type": "string", "value": mockEval(params.Expression)}}, "")
			default: // Page.enable / Input.dispatchKeyEvent / ...
				reply(map[string]any{}, "")
			}
		}
	})

	srv := httptest.NewServer(mux)
	wsURL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools/page/1"
	t.Cleanup(srv.Close)
	return srv
}

// mockEval 按表达式"假装"算个结果出来
func mockEval(expr string) string {
	switch {
	case strings.Contains(expr, "#nope"):
		return "NONE" // 找不到元素
	case strings.HasPrefix(expr, "!!"):
		return "true" // waitSelector / querySelector 存在性探测
	case strings.Contains(expr, "location.href") && strings.Contains(expr, "title"):
		return `{"url":"https://example.com/","title":"示例站","text":"渲染后的正文"}`
	case strings.Contains(expr, "outerHTML"):
		return "<html>mock</html>"
	case strings.Contains(expr, "location.href"):
		return "https://example.com/"
	default:
		return "42"
	}
}

func TestBrowserSessionDo(t *testing.T) {
	srv := mockCDP(t)
	sess := NewBrowserSession(BrowserOptions{Enabled: true, CDPURL: srv.URL, TimeoutSec: 5, MaxBytes: 1024}, nil)
	defer sess.Close()
	ctx := context.Background()

	// open：导航 + 等 load + 取渲染后的标题/正文
	out, err := sess.Do(ctx, "open", map[string]any{"url": "example.com"})
	if err != nil {
		t.Fatalf("open 失败：%v", err)
	}
	m := out.(map[string]any)
	if m["title"] != "示例站" {
		t.Fatalf("标题不对：%+v", m)
	}
	if s, _ := m["text"].(string); !strings.Contains(s, "渲染后的正文") {
		t.Fatalf("正文不对：%+v", m)
	}

	// text / html / eval
	if _, err := sess.Do(ctx, "text", nil); err != nil {
		t.Fatalf("text 失败：%v", err)
	}
	if _, err := sess.Do(ctx, "html", nil); err != nil {
		t.Fatalf("html 失败：%v", err)
	}
	res, err := sess.Do(ctx, "eval", map[string]any{"expression": "1+1"})
	if err != nil {
		t.Fatalf("eval 失败：%v", err)
	}
	if res.(map[string]any)["result"] != "42" {
		t.Fatalf("eval 结果不对：%+v", res)
	}

	// click 找不到元素要报错；type 正常
	if _, err := sess.Do(ctx, "click", map[string]any{"selector": "#nope"}); err == nil {
		t.Fatal("找不到元素应报错")
	}
	typed, err := sess.Do(ctx, "type", map[string]any{"selector": "input", "text": "你好"})
	if err != nil {
		t.Fatalf("type 失败：%v", err)
	}
	if typed.(map[string]any)["chars"] != 2 {
		t.Fatalf("type 字数不对：%+v", typed)
	}

	// 未知 action 明确报错，并列出可用项
	if _, err := sess.Do(ctx, "nope", nil); err == nil || !strings.Contains(err.Error(), "不支持的 action") {
		t.Fatalf("未知 action 应报错：%v", err)
	}
	// open 缺 url
	if _, err := sess.Do(ctx, "open", map[string]any{}); err == nil {
		t.Fatal("open 缺 url 应报错")
	}
	// 非 http(s) 地址
	if _, err := sess.Do(ctx, "open", map[string]any{"url": "file:///etc/passwd"}); err == nil {
		t.Fatal("非 http(s) 地址应报错")
	}
}

func TestBrowserToolScreenshot(t *testing.T) {
	srv := mockCDP(t)
	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewBrowserSession(BrowserOptions{Enabled: true, CDPURL: srv.URL, TimeoutSec: 5}, nil)
	defer sess.Close()

	tool := &BrowserTool{Sess: sess, WS: ws}
	if tool.Name() != "browser" {
		t.Fatalf("工具名应为 browser：%s", tool.Name())
	}
	out, err := tool.Run(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("截图失败：%v", err)
	}
	m := out.(map[string]any)
	rel, _ := m["path"].(string)
	if !strings.HasPrefix(rel, ".browser") {
		t.Fatalf("截图应落在 .browser 下：%s", rel)
	}
	abs, _ := m["absPath"].(string)
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("截图没落盘：%v", err)
	}
	if string(b) != "PNGDATA" {
		t.Fatalf("截图内容不对：%q", b)
	}

	// 不带 action 默认走 open
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("默认 action=open 缺 url 应报错")
	}
}

// 没配浏览器 / 配了连不上：一律明确报错，不静默
func TestBrowserErrors(t *testing.T) {
	off := NewBrowserSession(BrowserOptions{Enabled: false}, nil)
	if _, err := off.Do(context.Background(), "text", nil); err == nil || !strings.Contains(err.Error(), "没启用") {
		t.Fatalf("未启用应报明确错误：%v", err)
	}
	off.Close()

	dead := NewBrowserSession(BrowserOptions{Enabled: true, CDPURL: "http://127.0.0.1:1", TimeoutSec: 3}, nil)
	if _, err := dead.Do(context.Background(), "text", nil); err == nil {
		t.Fatal("调试端口连不上应报错")
	}
	dead.Close()

	bad := NewBrowserSession(BrowserOptions{Enabled: true, ChromePath: "definitely-not-a-browser-xyz"}, nil)
	if _, err := bad.Do(context.Background(), "text", nil); err == nil {
		t.Fatal("浏览器可执行文件不存在应报错")
	}
	bad.Close()

	closed := NewBrowserSession(BrowserOptions{Enabled: true, CDPURL: "http://127.0.0.1:1"}, nil)
	closed.Close()
	if _, err := closed.Do(context.Background(), "text", nil); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("会话关闭后应报错：%v", err)
	}
}

// truncate 截断要给提示，不静默丢内容
func TestBrowserTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); !strings.HasPrefix(got, "abc") || !strings.Contains(got, "截断") {
		t.Fatalf("截断结果不对：%q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("没超限不该动：%q", got)
	}
}

// TestBrowserRealChrome 真浏览器验证（默认跳过，靠 BAIZE_TEST_CHROME 开启）。
//
// 它证明的是 mock 证明不了的那件事：**页面里的 JS 真的跑了**——服务端渲染前
// 正文只有"还没跑JS"，脚本执行后才变成"JS渲染OK"。顺便验证"自己拉起浏览器"这条路径。
func TestBrowserRealChrome(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("BAIZE_TEST_CHROME"))
	if path == "" {
		t.Skip("未设 BAIZE_TEST_CHROME，跳过真浏览器验证")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>真浏览器验证</title></head>`+
			`<body><div id="x">还没跑JS</div><script>document.getElementById('x').textContent='JS渲染OK';</script></body></html>`)
	}))
	defer srv.Close()

	ws, err := NewWorkspace(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewBrowserSession(BrowserOptions{
		Enabled: true, Headless: true, ChromePath: path, TimeoutSec: 30,
	}, nil)
	defer sess.Close()

	out, err := sess.Do(context.Background(), "open", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("真浏览器打开失败：%v", err)
	}
	m := out.(map[string]any)
	if s, _ := m["text"].(string); !strings.Contains(s, "JS渲染OK") {
		t.Fatalf("没拿到 JS 渲染后的内容：%+v", m)
	}
	if m["title"] != "真浏览器验证" {
		t.Fatalf("标题不对：%+v", m)
	}

	// eval 也走真页面
	ev, err := sess.Do(context.Background(), "eval", map[string]any{"expression": "1+2"})
	if err != nil {
		t.Fatalf("真页面 eval 失败：%v", err)
	}
	if ev.(map[string]any)["result"] != "3" {
		t.Fatalf("eval 结果不对：%+v", ev)
	}

	// 截图：真图应该远大于 1KB
	tool := &BrowserTool{Sess: sess, WS: ws}
	sc, err := tool.Run(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("真浏览器截图失败：%v", err)
	}
	if n, _ := sc.(map[string]any)["bytes"].(int); n < 1000 {
		t.Fatalf("截图太小，不像真图：%+v", sc)
	}
}
