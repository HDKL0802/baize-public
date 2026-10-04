package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stdioTransport 把 MCP 服务当子进程拉起，JSON-RPC 走 stdin/stdout（一行一条 JSON）
type stdioTransport struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	stderr *ringBuf

	gen     idGen
	mu      sync.Mutex
	pending map[string]chan rpcResponse
	fatal   error
	die     chan struct{}
	reaped  chan struct{}
	closed  bool
	waitErr error
}

// newStdioTransport 拉起子进程并开始读 stdout
func newStdioTransport(cfg ServerConfig) (*stdioTransport, error) {
	args := append([]string{}, cfg.Args...)
	cmd := exec.Command(cfg.Command, args...)
	if strings.TrimSpace(cfg.Dir) != "" {
		cmd.Dir = cfg.Dir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("建立 MCP 子进程 stdin 失败：%w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("建立 MCP 子进程 stdout 失败：%w", err)
	}
	t := &stdioTransport{
		cmd: cmd, in: stdin, stderr: newRingBuf(8 << 10),
		pending: map[string]chan rpcResponse{}, die: make(chan struct{}), reaped: make(chan struct{}),
	}
	cmd.Stderr = t.stderr // 子进程 stderr 收进环形缓冲，出错时能看见原因（绝不静默吞掉）
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 MCP 服务进程失败（%s）：%w", cfg.Command, err)
	}
	go t.readLoop(stdout)
	go func() {
		// 注意：Wait 会一直阻塞到子进程退出，绝不能在持锁的情况下调用，否则会卡死所有请求
		err := cmd.Wait()
		t.mu.Lock()
		t.waitErr = err
		t.mu.Unlock()
		close(t.reaped)
	}()
	return t, nil
}

// readLoop 持续读 stdout：响应交给等待方，通知忽略，服务端主动请求明确回"不支持"
func (t *stdioTransport) readLoop(stdout io.Reader) {
	dec := json.NewDecoder(bufio.NewReaderSize(stdout, 64<<10))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.fail(fmt.Errorf("MCP 服务连接断开：%w%s", err, t.stderrSuffix()))
			return
		}
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue // 不是合法 JSON-RPC，忽略
		}
		key := normalizeRPCID(probe.ID)
		if key == "" {
			continue // 服务端通知，无需处理
		}
		t.mu.Lock()
		ch, ok := t.pending[key]
		if ok {
			delete(t.pending, key)
		}
		t.mu.Unlock()
		if !ok {
			// 服务端主动发起的请求（sampling / roots 等）我们暂不支持：明确回错误，别让它干等
			if probe.Method != "" {
				t.replyUnsupported(probe.ID, probe.Method)
			}
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			ch <- rpcResponse{Error: &RPCError{Code: -32700, Message: "响应解析失败：" + err.Error()}}
			continue
		}
		ch <- resp
	}
}

func (t *stdioTransport) replyUnsupported(id json.RawMessage, method string) {
	var idNum int64
	if err := json.Unmarshal(id, &idNum); err != nil {
		return // 字符串 id 我们没生成过，无需回
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": idNum,
		"error": map[string]any{
			"code":    codeMethodNotFound,
			"message": "白泽 MCP 客户端暂不支持服务端发起的 " + method,
		},
	})
	t.mu.Lock()
	_, _ = t.in.Write(append(payload, '\n'))
	t.mu.Unlock()
}

// Call 发一次请求并等响应
func (t *stdioTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, key := t.gen.New()
	ch := make(chan rpcResponse, 1)

	t.mu.Lock()
	if t.fatal != nil {
		err := t.fatal
		t.mu.Unlock()
		return nil, err
	}
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("MCP 服务连接已关闭")
	}
	t.pending[key] = ch
	raw, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		delete(t.pending, key)
		t.mu.Unlock()
		return nil, err
	}
	_, werr := t.in.Write(append(raw, '\n'))
	t.mu.Unlock()

	if werr != nil {
		t.drop(key)
		return nil, fmt.Errorf("写入 MCP 服务进程失败：%w%s", werr, t.stderrSuffix())
	}
	select {
	case <-ctx.Done():
		t.drop(key)
		return nil, fmt.Errorf("MCP 调用超时/被取消（%s）：%w", method, ctx.Err())
	case <-t.die:
		t.drop(key)
		if t.fatal != nil {
			return nil, t.fatal
		}
		return nil, errors.New("MCP 服务连接已关闭")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify 发一条通知（不等响应）
func (t *stdioTransport) Notify(_ context.Context, method string, params any) error {
	raw, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fatal != nil {
		return t.fatal
	}
	if t.closed {
		return errors.New("MCP 服务连接已关闭")
	}
	_, werr := t.in.Write(append(raw, '\n'))
	if werr != nil {
		return fmt.Errorf("写入 MCP 服务进程失败：%w%s", werr, t.stderrSuffix())
	}
	return nil
}

// Close 关闭 stdin 并结束子进程（不听话就强杀，避免留孤儿进程）
func (t *stdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	_ = t.in.Close()
	select {
	case <-t.reaped:
	case <-time.After(2 * time.Second):
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		select {
		case <-t.reaped:
		case <-time.After(time.Second):
		}
	}
	t.fail(errors.New("MCP 服务连接已关闭"))
	return nil
}

// fail 记录致命错误并唤醒所有等待方
func (t *stdioTransport) fail(err error) {
	t.mu.Lock()
	if t.fatal != nil {
		t.mu.Unlock()
		return
	}
	t.fatal = err
	pending := t.pending
	t.pending = map[string]chan rpcResponse{}
	t.mu.Unlock()
	close(t.die)
	for _, ch := range pending {
		select {
		case ch <- rpcResponse{Error: &RPCError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
}

func (t *stdioTransport) drop(key string) {
	t.mu.Lock()
	delete(t.pending, key)
	t.mu.Unlock()
}

func (t *stdioTransport) stderrSuffix() string {
	s := strings.TrimSpace(t.stderr.String())
	if s == "" {
		return ""
	}
	return "（子进程 stderr：" + brief(s, 400) + "）"
}

/* ---------- 小工具 ---------- */

// ringBuf 只保留最后 N 字节的缓冲（收子进程 stderr，避免无限增长）
type ringBuf struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newRingBuf(max int) *ringBuf { return &ringBuf{max: max} }

func (r *ringBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
