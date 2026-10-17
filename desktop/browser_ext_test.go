package main

import (
	"strconv"
	"testing"
	"time"

	"baize/shared/proto"

	"github.com/gorilla/websocket"
)

// 假扩展：连上本机 WS 控制口，收到命令就回一个固定结果。
// 这条测试不依赖 Chrome，验证的是「主进程 → 扩展 → 回执」整条通道。
func TestExtRoundTrip(t *testing.T) {
	port, err := browserFreePort()
	if err != nil {
		t.Fatalf("取空闲端口失败：%v", err)
	}
	cfgMu.Lock()
	oldOn, oldChrome, oldToken, oldPort := cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken, cfg.BrowserExtPort
	cfg.BrowserControl, cfg.BrowserAllowChrome = true, true
	cfg.BrowserExtToken, cfg.BrowserExtPort = "test-token", port
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.BrowserControl, cfg.BrowserAllowChrome = oldOn, oldChrome
		cfg.BrowserExtToken, cfg.BrowserExtPort = oldToken, oldPort
		cfgMu.Unlock()
		closeExtClient()
		restartExtServer()
	})
	restartExtServer()

	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	c, _, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/ext/ws?token=test-token", nil)
	if err != nil {
		t.Fatalf("假扩展连不上控制口：%v", err)
	}
	defer c.Close()

	go func() {
		for {
			var msg struct {
				ID     string         `json:"id"`
				Action string         `json:"action"`
				Args   map[string]any `json:"args"`
			}
			if err := c.ReadJSON(&msg); err != nil {
				return
			}
			_ = c.WriteJSON(map[string]any{
				"id": msg.ID, "ok": true,
				"result": map[string]any{"url": "https://example.com/", "title": "Example Domain", "tabId": 1},
			})
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !extConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !extConnected() {
		t.Fatal("扩展连接没有登记上")
	}

	res, err := extCall(proto.ActionBrowserRead, nil)
	if err != nil {
		t.Fatalf("extCall 失败：%v", err)
	}
	if res["url"] != "https://example.com/" || res["title"] != "Example Domain" {
		t.Fatalf("回执内容不对：%v", res)
	}
	if res["target"] != "chrome" {
		t.Fatalf("回执应标明 target=chrome：%v", res)
	}
}

// 令牌不对：连接必须被拒（不能靠"只有本机"当安全边界）
func TestExtRejectsBadToken(t *testing.T) {
	port, err := browserFreePort()
	if err != nil {
		t.Fatalf("取空闲端口失败：%v", err)
	}
	cfgMu.Lock()
	oldOn, oldChrome, oldToken, oldPort := cfg.BrowserControl, cfg.BrowserAllowChrome, cfg.BrowserExtToken, cfg.BrowserExtPort
	cfg.BrowserControl, cfg.BrowserAllowChrome = true, true
	cfg.BrowserExtToken, cfg.BrowserExtPort = "right-token", port
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.BrowserControl, cfg.BrowserAllowChrome = oldOn, oldChrome
		cfg.BrowserExtToken, cfg.BrowserExtPort = oldToken, oldPort
		cfgMu.Unlock()
		restartExtServer()
	})
	restartExtServer()

	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	c, resp, err := d.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/ext/ws?token=wrong-token", nil)
	if err == nil {
		c.Close()
		t.Fatal("错误令牌不该连上")
	}
	if resp == nil || resp.StatusCode != 403 {
		t.Fatalf("应返回 403，实际：%v", resp)
	}
}
