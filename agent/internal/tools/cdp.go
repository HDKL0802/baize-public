package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

/* ---------- 极简 CDP 客户端 ---------- */

// cdpConn 一条到「页面」的 CDP（Chrome DevTools Protocol）连接：发命令、等回包、订阅事件。
//
// 只连页面级 ws（/json/new 或 /json/list 里的 type=page），不用 Target/session 那套扁平化会话——
// 对个人助手来说"一个专用标签页"就够了：代码少一半，坑也少一半。
type cdpConn struct {
	ws     *websocket.Conn
	writeM sync.Mutex

	mu     sync.Mutex
	seq    int
	pend   map[int]chan cdpReply
	subs   map[string][]chan json.RawMessage
	err    error
	closed bool

	done chan struct{}
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

// cdpError CDP 协议里的 error 对象
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("CDP 报错 %d：%s", e.Code, e.Message) }

type cdpEnvelope struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpError       `json:"error,omitempty"`
}

// dialCDP 连上一条页面 ws
func dialCDP(ctx context.Context, wsURL string) (*cdpConn, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	c, resp, err := d.DialContext(ctx, wsURL, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return nil, fmt.Errorf("连接浏览器调试端口失败（HTTP %d）：%w", code, err)
	}
	c.SetReadLimit(64 << 20) // 截图 / 大 HTML：别被默认 32KB 掐断
	conn := &cdpConn{
		ws:   c,
		pend: map[int]chan cdpReply{},
		subs: map[string][]chan json.RawMessage{},
		done: make(chan struct{}),
	}
	go conn.readLoop()
	return conn, nil
}

func (c *cdpConn) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.fail(fmt.Errorf("浏览器连接断开：%w", err))
			return
		}
		var env cdpEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.ID > 0 { // 命令回包
			c.mu.Lock()
			ch := c.pend[env.ID]
			delete(c.pend, env.ID)
			c.mu.Unlock()
			if ch != nil {
				if env.Error != nil {
					ch <- cdpReply{err: env.Error}
				} else {
					ch <- cdpReply{result: env.Result}
				}
			}
			continue
		}
		if env.Method != "" { // 事件
			c.mu.Lock()
			subs := append([]chan json.RawMessage{}, c.subs[env.Method]...)
			c.mu.Unlock()
			for _, ch := range subs {
				select {
				case ch <- env.Params:
				default: // 订阅方没及时取就丢，别把读循环堵死
				}
			}
		}
	}
}

// fail 记录错误并唤醒所有等待者
func (c *cdpConn) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	pend := c.pend
	c.pend = map[int]chan cdpReply{}
	c.closed = true
	c.mu.Unlock()
	for _, ch := range pend {
		ch <- cdpReply{err: err}
	}
}

// call 发一条命令并等回包
func (c *cdpConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw := json.RawMessage("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.seq++
	id := c.seq
	ch := make(chan cdpReply, 1)
	c.pend[id] = ch
	c.mu.Unlock()

	msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": raw})
	c.writeM.Lock()
	err := c.ws.WriteMessage(websocket.TextMessage, msg)
	c.writeM.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pend, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("发送 CDP 命令失败：%w", err)
	}

	select {
	case rep := <-ch:
		return rep.result, rep.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pend, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("CDP 命令超时（%s）：%w", method, ctx.Err())
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("浏览器连接已关闭")
		}
		return nil, err
	}
}

// evalString 在页面里跑一段表达式，把结果当字符串取回（对象会自动 JSON 化）
func (c *cdpConn) evalString(ctx context.Context, expr string) (string, error) {
	res, err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("解析 CDP 结果失败：%w", err)
	}
	if out.ExceptionDetails != nil {
		msg := strings.TrimSpace(out.ExceptionDetails.Text)
		if out.ExceptionDetails.Exception != nil && strings.TrimSpace(out.ExceptionDetails.Exception.Description) != "" {
			msg = out.ExceptionDetails.Exception.Description
		}
		if msg == "" {
			msg = "未知错误"
		}
		return "", fmt.Errorf("页面里执行出错：%s", msg)
	}
	if len(out.Result.Value) == 0 || string(out.Result.Value) == "null" {
		return out.Result.Description, nil
	}
	var s string
	if json.Unmarshal(out.Result.Value, &s) == nil {
		return s, nil
	}
	return string(out.Result.Value), nil
}

// cdpSub 一条事件订阅
type cdpSub struct {
	c      *cdpConn
	method string
	ch     chan json.RawMessage
}

func (s *cdpSub) C() <-chan json.RawMessage { return s.ch }

func (s *cdpSub) off() {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	list := s.c.subs[s.method]
	for i, ch := range list {
		if ch == s.ch {
			s.c.subs[s.method] = append(list[:i], list[i+1:]...)
			break
		}
	}
}

// subscribe 订阅某类事件（要先订阅再发触发它的命令）
func (c *cdpConn) subscribe(method string) *cdpSub {
	ch := make(chan json.RawMessage, 8)
	c.mu.Lock()
	c.subs[method] = append(c.subs[method], ch)
	c.mu.Unlock()
	return &cdpSub{c: c, method: method, ch: ch}
}

func (c *cdpConn) close() {
	if c.ws != nil {
		_ = c.ws.Close()
	}
}

/* ---------- CDP 的 HTTP 端点（/json/*） ---------- */

// cdpHTTP 和调试端口的 HTTP 接口打交道
func cdpHTTP(ctx context.Context, method, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("调试端口 %s %s 返回 %d：%s", method, url, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// cdpAlive 探一下调试端口在不在（顺便拿到浏览器 ws）
func cdpAlive(ctx context.Context, base string) bool {
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	return cdpHTTP(ctx, http.MethodGet, base+"/json/version", &v) == nil && v.WebSocketDebuggerURL != ""
}

// cdpNewPage 让浏览器开一个新标签页，返回它的 ws 地址。
// 优先 /json/new（Chrome 111 起要求 PUT），不支持再退回 /json/list 里现成的 page。
func cdpNewPage(ctx context.Context, base string) (string, error) {
	var t struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := cdpHTTP(ctx, http.MethodPut, base+"/json/new?url=about:blank", &t); err == nil && t.WebSocketDebuggerURL != "" {
		return t.WebSocketDebuggerURL, nil
	}
	var list []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := cdpHTTP(ctx, http.MethodGet, base+"/json/list", &list); err != nil {
		return "", err
	}
	for _, it := range list {
		if it.Type == "page" && it.WebSocketDebuggerURL != "" {
			return it.WebSocketDebuggerURL, nil
		}
	}
	return "", errors.New("浏览器里没有可用标签页（试过 /json/new 与 /json/list）")
}
