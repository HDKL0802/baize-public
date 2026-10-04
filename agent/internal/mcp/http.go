package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport MCP Streamable HTTP 传输：
// 每次请求 POST 一条 JSON-RPC，响应可能是 application/json，也可能是 text/event-stream。
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	gen idGen
	mu  sync.Mutex
	sid string // Mcp-Session-Id
}

func newHTTPTransport(cfg ServerConfig) (*httpTransport, error) {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &httpTransport{
		url:     cfg.URL,
		headers: cfg.Headers,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (t *httpTransport) session() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sid
}

// Call 发一次请求并取回结果
func (t *httpTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, key := t.gen.New()
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := t.captureSession(resp); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("MCP HTTP 服务返回 %d：%s", resp.StatusCode, brief(string(raw), 300))
	}
	if isSSE(resp) {
		return readSSE(resp.Body, key, method)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 MCP HTTP 响应失败：%w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("MCP HTTP 服务对 %s 没有返回任何内容（HTTP %d）", method, resp.StatusCode)
	}
	return decodeSingle(raw, key, method)
}

// Notify 发一条通知（不等响应）
func (t *httpTransport) Notify(ctx context.Context, method string, params any) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if err := t.captureSession(resp); err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("MCP HTTP 服务返回 %d（通知 %s）", resp.StatusCode, method)
	}
	return nil
}

// Close HTTP 是无状态传输：有会话就发一个 DELETE，没有就直接结束
func (t *httpTransport) Close() error {
	sid := t.session()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil // 服务端不支持 DELETE 很正常，不算失败
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造 MCP HTTP 请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	if sid := t.session(); sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 MCP HTTP 服务失败（%s）：%w", t.url, err)
	}
	return resp, nil
}

// captureSession 记下服务端给的会话 id（后续请求要带上）
func (t *httpTransport) captureSession(resp *http.Response) error {
	v := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	t.mu.Lock()
	if v != "" {
		t.sid = v
	}
	has := t.sid != ""
	t.mu.Unlock()
	if resp.StatusCode == http.StatusNotFound && has {
		return errors.New("MCP HTTP 会话已失效（服务端返回 404），请重新加载该 MCP 服务")
	}
	return nil
}

func isSSE(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// readSSE 在 SSE 流里找到我们这个 id 的响应
func readSSE(body io.Reader, wantID, method string) (json.RawMessage, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var data strings.Builder

	handle := func() (json.RawMessage, bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return nil, false, nil
		}
		res, err := decodeSingle([]byte(payload), wantID, method)
		if err != nil {
			var re *RPCError
			if errors.As(err, &re) {
				return nil, true, err
			}
			return nil, false, nil // 不是我们关心的消息（比如服务端通知）
		}
		return res, true, nil
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if res, ok, err := handle(); ok {
				return res, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if res, ok, err := handle(); ok {
		return res, err
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 MCP SSE 流失败：%w", err)
	}
	return nil, fmt.Errorf("MCP SSE 流结束，但没有收到 %s 的响应", method)
}

// decodeSingle 解析单条 JSON-RPC 响应；id 对不上就明确说"不是这次的响应"
func decodeSingle(raw []byte, wantID, method string) (json.RawMessage, error) {
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析 MCP 响应失败（%s）：%w", method, err)
	}
	if wantID != "" && len(resp.ID) > 0 && normalizeRPCID(resp.ID) != wantID {
		return nil, fmt.Errorf("MCP 响应 id 对不上（期望 %s，收到 %s）", wantID, normalizeRPCID(resp.ID))
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}
