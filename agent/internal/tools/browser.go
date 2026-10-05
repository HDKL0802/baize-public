package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

/* ---------- 浏览器会话 ---------- */

// BrowserOptions 浏览器工具的运行参数。
//
// 单独定义一份、不直接引用 config.Browser：tools 包不能 import config ——
// config 依赖 mcp，mcp 又依赖 tools，会成环。调用方（agentsvc）负责映射。
type BrowserOptions struct {
	Enabled    bool
	Headless   bool
	ChromePath string
	CDPURL     string
	TimeoutSec int
	MaxBytes   int
}

// BrowserSession 浏览器会话：懒连接、可复用。
//
// 工具每次运行都会重建（要带上不同的深度/审批），但浏览器不该跟着重建——
// 起一个 Chromium 要一两秒。所以会话由 Service 持有，工具只是引用它。
type BrowserSession struct {
	lg   *slog.Logger
	opts BrowserOptions

	mu     sync.Mutex
	conn   *cdpConn
	proc   *exec.Cmd // 自己拉起的浏览器（关的时候要连它一起收掉）
	tmp    string
	sig    string
	closed bool
}

// NewBrowserSession 造一个会话（不立即连；第一次调用时才连）
func NewBrowserSession(opts BrowserOptions, lg *slog.Logger) *BrowserSession {
	if lg == nil {
		lg = slog.Default()
	}
	return &BrowserSession{lg: lg, opts: opts}
}

// SetConfig 换配置：目标变了就把旧连接收掉，下次调用按新配置重连
func (b *BrowserSession) SetConfig(opts BrowserOptions) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sig != "" && b.sig != browserSig(opts) {
		b.dropLocked()
	}
	b.opts = opts
}

// Close 收摊（服务关停 / 换配置时调）
func (b *BrowserSession) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.dropLocked()
}

func (b *BrowserSession) dropLocked() {
	if b.conn != nil {
		b.conn.close()
		b.conn = nil
	}
	if b.proc != nil && b.proc.Process != nil {
		_ = b.proc.Process.Kill()
		_, _ = b.proc.Process.Wait()
		b.proc = nil
	}
	if b.tmp != "" {
		_ = os.RemoveAll(b.tmp)
		b.tmp = ""
	}
	b.sig = ""
}

// browserSig 配置签名：决定"要不要重连"
func browserSig(o BrowserOptions) string {
	return fmt.Sprintf("%s|%s|%v", o.CDPURL, o.ChromePath, o.Headless)
}

// ensureLocked 保证有连接（调用方须持锁）
func (b *BrowserSession) ensureLocked(ctx context.Context) (*cdpConn, error) {
	if b.closed {
		return nil, errors.New("浏览器会话已关闭")
	}
	if !b.opts.Enabled {
		return nil, errors.New("浏览器工具没启用（配置 browser.enabled = true 后可用）")
	}
	sig := browserSig(b.opts)
	if b.conn != nil && b.sig == sig {
		return b.conn, nil
	}
	b.dropLocked()

	base := strings.TrimRight(b.opts.CDPURL, "/")
	if base == "" {
		path := strings.TrimSpace(b.opts.ChromePath)
		if path == "" {
			path = findChromium()
		}
		if path == "" {
			return nil, errors.New("没有可用浏览器：请在配置里设 browser.cdpUrl（外部 Chrome/Edge 的调试端口，如 http://192.168.1.10:9222）或 browser.chromePath（浏览器可执行文件）")
		}
		proc, tmp, pbase, err := launchChromium(ctx, path, b.opts.Headless)
		if err != nil {
			return nil, err
		}
		b.proc, b.tmp, base = proc, tmp, pbase
		b.lg.Info("已拉起浏览器", "path", path, "headless", b.opts.Headless, "cdp", base)
	}

	ws, err := cdpNewPage(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("打开浏览器标签页失败（%s）：%w", base, err)
	}
	conn, err := dialCDP(ctx, ws)
	if err != nil {
		return nil, err
	}
	b.conn, b.sig = conn, sig
	return conn, nil
}

// Do 执行一个动作（open / text / html / eval / click / type）。
// 全程串行：一个会话只有一个标签页，并发跑反而互相打断。
func (b *BrowserSession) Do(ctx context.Context, action string, args map[string]any) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	conn, err := b.ensureLocked(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()

	switch action {
	case "open", "goto", "navigate":
		return b.open(cctx, conn, args)
	case "text":
		return b.pageText(cctx, conn)
	case "html":
		return b.pageHTML(cctx, conn)
	case "eval", "js":
		expr := ArgString(args, "expression")
		if expr == "" {
			expr = ArgString(args, "code")
		}
		if expr == "" {
			return nil, errors.New("eval 需要 expression 参数")
		}
		out, err := conn.evalString(cctx, expr)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": truncate(out, b.maxBytes())}, nil
	case "click":
		return b.click(cctx, conn, args)
	case "type":
		return b.typeText(cctx, conn, args)
	default:
		return nil, fmt.Errorf("不支持的 action：%s（可用 open | text | html | eval | screenshot | click | type）", action)
	}
}

// Screenshot 抓一张 PNG（落盘交给工具，会话不碰文件系统）
func (b *BrowserSession) Screenshot(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	conn, err := b.ensureLocked(ctx)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	res, err := conn.call(cctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	bin, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return nil, fmt.Errorf("截图数据解码失败：%w", err)
	}
	return bin, nil
}

func (b *BrowserSession) timeout() time.Duration {
	s := b.opts.TimeoutSec
	if s <= 0 {
		s = 30
	}
	return time.Duration(s) * time.Second
}

func (b *BrowserSession) maxBytes() int {
	n := b.opts.MaxBytes
	if n <= 0 {
		n = 256 * 1024
	}
	return n
}

func (b *BrowserSession) open(ctx context.Context, conn *cdpConn, args map[string]any) (any, error) {
	raw := ArgString(args, "url")
	if raw == "" {
		return nil, errors.New("url 不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("只支持 http/https 地址：%s", raw)
	}
	sub := conn.subscribe("Page.loadEventFired")
	defer sub.off()
	_, _ = conn.call(ctx, "Page.enable", nil) // 只是为了能收到 loadEventFired，失败不致命
	if _, err := conn.call(ctx, "Page.navigate", map[string]any{"url": u.String()}); err != nil {
		return nil, err
	}
	waitLoad(ctx, sub.C(), 10*time.Second)
	if sel := ArgString(args, "waitFor"); sel != "" {
		if err := waitSelector(ctx, conn, sel, 10*time.Second); err != nil {
			return nil, err
		}
	}
	if ms := ArgInt(args, "waitMs", 0); ms > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(ms) * time.Millisecond):
		}
	}
	return b.pageText(ctx, conn)
}

func (b *BrowserSession) pageText(ctx context.Context, conn *cdpConn) (any, error) {
	out, err := conn.evalString(ctx, jsPageInfo)
	if err != nil {
		return nil, err
	}
	var info struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return nil, fmt.Errorf("解析页面信息失败：%w", err)
	}
	return map[string]any{
		"url": info.URL, "title": info.Title,
		"text": truncate(info.Text, b.maxBytes()),
	}, nil
}

func (b *BrowserSession) pageHTML(ctx context.Context, conn *cdpConn) (any, error) {
	out, err := conn.evalString(ctx, "document.documentElement.outerHTML")
	if err != nil {
		return nil, err
	}
	u, _ := conn.evalString(ctx, "location.href")
	return map[string]any{"url": u, "html": truncate(out, b.maxBytes())}, nil
}

func (b *BrowserSession) click(ctx context.Context, conn *cdpConn, args map[string]any) (any, error) {
	sel := ArgString(args, "selector")
	if sel == "" {
		return nil, errors.New("click 需要 selector 参数（CSS 选择器）")
	}
	out, err := conn.evalString(ctx, "(() => { const el = document.querySelector("+jsString(sel)+"); if (!el) return 'NONE'; el.scrollIntoView({block:'center'}); el.click(); return 'OK'; })()")
	if err != nil {
		return nil, err
	}
	if out == "NONE" {
		return nil, fmt.Errorf("页面上没找到元素：%s", sel)
	}
	return map[string]any{"clicked": sel}, nil
}

func (b *BrowserSession) typeText(ctx context.Context, conn *cdpConn, args map[string]any) (any, error) {
	sel := ArgString(args, "selector")
	if sel == "" {
		return nil, errors.New("type 需要 selector 参数（CSS 选择器）")
	}
	text := ArgString(args, "text")
	expr := "(() => { const el = document.querySelector(" + jsString(sel) + "); if (!el) return 'NONE';" +
		" el.focus(); const setter = Object.getOwnPropertyDescriptor(el.constructor.prototype, 'value');" +
		" if (setter && setter.set) setter.set.call(el, " + jsString(text) + "); else el.value = " + jsString(text) + ";" +
		" el.dispatchEvent(new Event('input', {bubbles:true}));" +
		" el.dispatchEvent(new Event('change', {bubbles:true})); return 'OK'; })()"
	out, err := conn.evalString(ctx, expr)
	if err != nil {
		return nil, err
	}
	if out == "NONE" {
		return nil, fmt.Errorf("页面上没找到元素：%s", sel)
	}
	submitted := false
	if ArgBool(args, "submit") {
		if _, err := conn.call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type": "keyDown", "windowsVirtualKeyCode": 13, "key": "Enter",
		}); err != nil {
			return nil, err
		}
		_, _ = conn.call(ctx, "Input.dispatchKeyEvent", map[string]any{
			"type": "keyUp", "windowsVirtualKeyCode": 13, "key": "Enter",
		})
		submitted = true
	}
	return map[string]any{"typed": sel, "chars": len([]rune(text)), "submitted": submitted}, nil
}

/* ---------- JS 片段与等待助手 ---------- */

const jsPageInfo = `(() => { const b = document.body; return JSON.stringify({ url: location.href, title: document.title, text: b ? (b.innerText || b.textContent || "") : "" }); })()`

// jsString 把 Go 字符串安全地塞进 JS 字面量
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func waitLoad(ctx context.Context, ch <-chan json.RawMessage, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C: // 有些页面不发 load（或已经在加载完的状态），超时就往下走，不硬失败
	case <-ctx.Done():
	}
}

func waitSelector(ctx context.Context, conn *cdpConn, sel string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		v, err := conn.evalString(ctx, "!!document.querySelector("+jsString(sel)+")")
		if err == nil && v == "true" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等元素超时（%s 内没出现）：%s", d, sel)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// truncate 按字节截断（超量时给出明确提示，不静默丢内容）
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…（内容过长，已截断到 %d 字节）", max)
}

/* ---------- 自己拉起浏览器 ---------- */

// findChromium 在 PATH 里找一个常见浏览器
func findChromium() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// launchChromium 起一个无头浏览器，等它的调试端口就绪
func launchChromium(ctx context.Context, path string, headless bool) (*exec.Cmd, string, string, error) {
	tmp, err := os.MkdirTemp("", "baize-chrome-")
	if err != nil {
		return nil, "", "", fmt.Errorf("建临时目录失败：%w", err)
	}
	port, err := freePort()
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, "", "", err
	}
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir=" + tmp,
		"--no-first-run", "--no-default-browser-check", "--disable-extensions",
		"--disable-background-networking", "--disable-sync", "--disable-gpu",
		"--disable-dev-shm-usage", // 容器里 /dev/shm 常常很小，不开这个容易崩
		"--no-sandbox",            // 容器里通常没有 user namespace，不加起不来
		"--disable-setuid-sandbox",
		"about:blank",
	}
	if headless {
		args = append([]string{"--headless=new"}, args...)
	}
	cmd := exec.Command(path, args...)
	// HOME 指到临时目录：精简容器里常常没设 HOME，浏览器会因此起不来
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	// 浏览器起不来时的原因基本只在 stderr 里（缺库、沙箱、参数不支持……），
	// 丢掉它就只能报"端口没起来"这种没用的话
	var errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &errBuf
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, "", "", fmt.Errorf("启动浏览器失败（%s）：%w", path, err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if cdpAlive(ctx, base) {
			return cmd, tmp, base, nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(tmp)
			return nil, "", "", fmt.Errorf("浏览器调试端口没起来（%s）：%s", base, brief(errBuf.String()))
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(tmp)
			return nil, "", "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// brief 把浏览器 stderr 压成一行好读的提示
func brief(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "（浏览器没有输出任何错误信息）"
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

/* ---------- 工具外壳 ---------- */

// BrowserTool 浏览器工具（模型可调）：打开网页、读渲染后的文本、跑 JS、截图、点击、输入
type BrowserTool struct {
	Sess *BrowserSession
	WS   *Workspace
}

// Name 工具名
func (t *BrowserTool) Name() string { return "browser" }

// Description 说明
func (t *BrowserTool) Description() string {
	return "用真实浏览器打开网页（能执行 JS、渲染后再取内容）：open 打开并返回标题与正文、text/html 取当前页、eval 跑一段 JS、screenshot 截图存到工作目录、click/type 点按与输入。适合 web_fetch 抓不到的动态页面"
}

// Schema 参数说明
func (t *BrowserTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"open", "text", "html", "eval", "screenshot", "click", "type"}, "description": "要做的事，默认 open"},
			"url":        map[string]any{"type": "string", "description": "open：地址（http/https）"},
			"waitFor":    map[string]any{"type": "string", "description": "open：可选，等这个 CSS 选择器出现再取内容"},
			"waitMs":     map[string]any{"type": "integer", "description": "open：可选，额外再等多少毫秒"},
			"expression": map[string]any{"type": "string", "description": "eval：要执行的 JS 表达式"},
			"selector":   map[string]any{"type": "string", "description": "click / type：CSS 选择器"},
			"text":       map[string]any{"type": "string", "description": "type：要输入的内容"},
			"submit":     map[string]any{"type": "boolean", "description": "type：输入后是否按回车提交"},
		},
	}
}

// Run 执行
func (t *BrowserTool) Run(ctx context.Context, args map[string]any) (any, error) {
	if t.Sess == nil {
		return nil, errors.New("浏览器工具没接上（内部错误）")
	}
	action := strings.ToLower(ArgString(args, "action"))
	if action == "" {
		action = "open"
	}
	if action == "screenshot" {
		bin, err := t.Sess.Screenshot(ctx)
		if err != nil {
			return nil, err
		}
		rel := filepath.Join(".browser", "shot-"+time.Now().Format("20060102-150405-000")+".png")
		abs, err := t.WS.Resolve(rel)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("建截图目录失败：%w", err)
		}
		if err := os.WriteFile(abs, bin, 0o644); err != nil {
			return nil, fmt.Errorf("写截图失败：%w", err)
		}
		return map[string]any{"path": rel, "absPath": abs, "bytes": len(bin)}, nil
	}
	return t.Sess.Do(ctx, action, args)
}
