//go:build windows

// 电脑控制 · 浏览器双方案之一：**内置浏览器**（独立进程里的 WebView2 窗口）。
//
// 为什么单开一个进程：go-webview2 在 init 里就 LockOSThread + CoInitializeEx，
// 一个进程只有一条 UI 线程；主窗口已经占着它（同 float_windows.go 的理由）。
// 所以「打开浏览器」= 用同一个 exe 带 -browser 再起一个进程：
//
//	主进程（设备动作 browser.*） --HTTP(127.0.0.1, 随机端口+随机令牌)--> 浏览器进程
//
// 浏览器进程只做三件事：显示网页、执行白名单内的页面操作、把结果回给主进程。
// 它**不连后端、不碰设备连接**；控制口只监听 127.0.0.1，且每个请求都要带令牌。
//
// 移交（handoff）：AI 调用 browser.handoff{handoff:true} 或用户点页面顶部黄条上的
// 「交还给白泽」；移交期间除 status / handoff 外的动作一律拒绝，控制权真的在人手里。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	pGetWindowRectBr = u32.NewProc("GetWindowRect")
	pPrintWindow     = u32.NewProc("PrintWindow")
)

// winRect GetWindowRect 的矩形
type winRect struct {
	Left, Top, Right, Bottom int32
}

// browserState 浏览器进程的全部状态
type browserState struct {
	mu      sync.Mutex
	w       webview2.WebView
	hwnd    uintptr
	port    int
	token   string
	handoff bool
	ready   atomic.Bool

	seq     atomic.Int64
	pending sync.Map // id(string) -> chan string
}

// runBrowserMode 浏览器进程入口（main 里解析完参数后调用）
func runBrowserMode(port int, token, startURL string) {
	if port <= 0 || strings.TrimSpace(token) == "" {
		log.Print("内置浏览器启动失败：缺少 -browser-port / -browser-token")
		return
	}
	st := &browserState{port: port, token: token}

	// 控制口先起：主进程马上就会来探活/发动作
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		log.Printf("内置浏览器控制口监听失败（127.0.0.1:%d）：%v", port, err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", st.handleHealth)
	mux.HandleFunc("/ctl", st.handleCtl)
	go func() { _ = http.Serve(ln, mux) }()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "白泽浏览器",
			Width:  1200,
			Height: 820,
		},
	})
	if w == nil {
		log.Print("内置浏览器启动失败：WebView2 运行时不可用（Win10 需要安装 WebView2 Runtime）")
		return
	}
	defer w.Destroy()

	st.mu.Lock()
	st.w = w
	st.hwnd = uintptr(w.Window())
	st.mu.Unlock()

	// 页面桥：每个文档创建时都注入（__baizeRun / __baizeBanner）
	w.Init(browserBridgeJS)
	_ = w.Bind("__baizeReply", st.onReply)
	_ = w.Bind("__baizeHandoffBack", st.onHandoffBack)
	st.ready.Store(true)

	if strings.TrimSpace(startURL) == "" {
		startURL = "about:blank"
	}
	w.Navigate(startURL)
	log.Printf("内置浏览器已启动：%s", startURL)
	w.Run()
}

/* ---------------- 控制口 ---------------- */

func (s *browserState) authorized(r *http.Request) bool {
	return r.Header.Get("X-Baize-Token") == s.token
}

func (s *browserState) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !s.authorized(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"error":"令牌不对"}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ready": s.ready.Load()})
}

func (s *browserState) handleCtl(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"error":"令牌不对（请在设置页重新复制扩展/浏览器令牌）"}`)
		return
	}
	var req struct {
		Action string         `json:"action"`
		Args   map[string]any `json:"args"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "请求不是合法 JSON：" + err.Error()})
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	res, err := s.do(req.Action, req.Args)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
}

/* ---------------- 动作 ---------------- */

// do 串行执行一个动作。页面同一时刻只能干一件事，并发只会互相打断。
func (s *browserState) do(action string, args map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return nil, errors.New("内置浏览器还在启动，请稍等再试")
	}

	// 移交期间：只允许查状态与收回控制权（把控制权交回 AI 必须显式 handoff:false）
	if s.handoff && action != "status" && action != "handoff" {
		return nil, errors.New("控制权已经移交给你了（页面顶部黄条）；你操作完点「交还给白泽」，或让 AI 调 browser.handoff{handoff:false} 才能继续")
	}

	switch action {
	case "status":
		return s.statusLocked()
	case "open":
		return s.openLocked(args)
	case "read":
		return s.evalLocked("read", nil)
	case "click":
		return s.evalLocked("click", args)
	case "type":
		return s.evalLocked("type", args)
	case "key":
		return s.keyLocked(args)
	case "screenshot":
		return s.shotLocked()
	case "handoff":
		on := argBool(args, "handoff")
		s.handoff = on
		s.bannerLocked(on)
		return s.statusLocked()
	default:
		return nil, fmt.Errorf("不支持的浏览器动作：%s（可用 open / read / click / type / key / screenshot / status / handoff）", action)
	}
}

func (s *browserState) statusLocked() (map[string]any, error) {
	out := map[string]any{"open": true, "handoff": s.handoff}
	info, err := s.evalLocked("status", nil)
	if err != nil {
		// 页面可能还没加载完，但窗口确实开着：如实返回可用信息
		out["note"] = "页面信息暂时读不到：" + err.Error()
		return out, nil
	}
	for k, v := range info {
		out[k] = v
	}
	return out, nil
}

func (s *browserState) openLocked(args map[string]any) (map[string]any, error) {
	raw := ""
	if v, ok := args["url"]; ok && v != nil {
		raw = strings.TrimSpace(fmt.Sprint(v))
	}
	if raw == "" {
		return nil, errors.New("缺少参数 url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("只支持 http/https 地址：%s", raw)
	}
	s.w.Dispatch(func() { s.w.Navigate(u.String()) })
	// 给页面一点加载时间；读不到就只回地址，不假装读到内容
	time.Sleep(900 * time.Millisecond)
	out := map[string]any{"url": u.String()}
	if info, err := s.evalLocked("status", nil); err == nil {
		for k, v := range info {
			out[k] = v
		}
	}
	if s.handoff {
		s.bannerLocked(true)
	}
	return out, nil
}

func (s *browserState) keyLocked(args map[string]any) (map[string]any, error) {
	raw := ""
	if v, ok := args["keys"]; ok && v != nil {
		raw = strings.TrimSpace(fmt.Sprint(v))
	}
	if raw == "" {
		return nil, errors.New("缺少参数 keys（如 enter / ctrl+s / f5；多组用逗号串）")
	}
	// 按键是发给前台窗口的：先把浏览器窗口叫到前台，否则会打进别人那里
	pSetForegroundWindow.Call(s.hwnd)
	time.Sleep(120 * time.Millisecond)
	pressed := 0
	var combos []string
	for _, combo := range strings.Split(raw, ",") {
		combo = strings.TrimSpace(combo)
		if combo == "" {
			continue
		}
		specs, err := parseKeyCombo(combo)
		if err != nil {
			return nil, err
		}
		for _, sp := range specs {
			pressKey(sp)
			pressed++
		}
		for i := len(specs) - 1; i >= 0; i-- {
			pressKeyUp(specs[i])
		}
		combos = append(combos, combo)
		time.Sleep(40 * time.Millisecond)
	}
	return map[string]any{"ok": true, "keys": raw, "events": pressed, "combos": combos}, nil
}

func (s *browserState) shotLocked() (map[string]any, error) {
	bin, w, h, err := captureBrowserWindow(s.hwnd)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("baize-browser-shot-%d.png", time.Now().UnixMilli()))
	if err := os.WriteFile(path, bin, 0o644); err != nil {
		return nil, fmt.Errorf("写截图文件失败：%w", err)
	}
	return map[string]any{"path": path, "width": w, "height": h, "bytes": len(bin)}, nil
}

func (s *browserState) bannerLocked(on bool) {
	if s.w == nil {
		return
	}
	v := "false"
	if on {
		v = "true"
	}
	s.w.Dispatch(func() { s.w.Eval("window.__baizeBanner && window.__baizeBanner(" + v + ")") })
}

/* ---------------- 页面桥 ---------------- */

// evalLocked 在页面里跑一个**预定义**动作（不是任意 JS），等页面把结果回传。
// 调用方须持锁（do 已经持了）。
func (s *browserState) evalLocked(name string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	id := strconv.FormatInt(s.seq.Add(1), 10)
	ch := make(chan string, 1)
	s.pending.Store(id, ch)
	defer s.pending.Delete(id)

	argJSON, _ := json.Marshal(args)
	js := fmt.Sprintf("window.__baizeRun && window.__baizeRun(%s,%s,%s)",
		jsQuote(id), jsQuote(name), jsQuote(string(argJSON)))
	s.w.Dispatch(func() { s.w.Eval(js) })

	select {
	case raw := <-ch:
		var out struct {
			OK    bool           `json:"ok"`
			Value map[string]any `json:"value"`
			Error string         `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("页面回传的结果读不懂：%w", err)
		}
		if !out.OK {
			if out.Error == "" {
				out.Error = "页面操作失败（没有给出原因）"
			}
			return nil, errors.New(out.Error)
		}
		if out.Value == nil {
			out.Value = map[string]any{}
		}
		return out.Value, nil
	case <-time.After(15 * time.Second):
		return nil, errors.New("页面 15 秒没响应（可能还在加载，或页面把脚本拦了）")
	}
}

// onReply 页面 → Go 的回包（Bind：每个页面文档都会挂上 window.__baizeReply）
func (s *browserState) onReply(id, payload string) string {
	if v, ok := s.pending.Load(id); ok {
		select {
		case v.(chan string) <- payload:
		default:
		}
	}
	return ""
}

// onHandoffBack 用户点了黄条上的「交还给白泽」
func (s *browserState) onHandoffBack() string {
	s.mu.Lock()
	s.handoff = false
	s.mu.Unlock()
	return "ok"
}

/* ---------------- 窗口截图（PrintWindow 优先，BitBlt 兜底） ---------------- */

func captureBrowserWindow(hwnd uintptr) ([]byte, int, int, error) {
	if hwnd == 0 {
		return nil, 0, 0, errors.New("浏览器窗口句柄无效")
	}
	var rc winRect
	if r, _, _ := pGetWindowRectBr.Call(hwnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return nil, 0, 0, errors.New("读窗口位置失败")
	}
	w, h := int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("窗口尺寸无效（%dx%d）", w, h)
	}

	hScreen, _, _ := pGetDC.Call(0)
	if hScreen == 0 {
		return nil, 0, 0, errors.New("取屏幕 DC 失败")
	}
	defer pReleaseDC.Call(0, hScreen)
	dstDC, _, _ := pCreateCompatibleDC.Call(hScreen)
	if dstDC == 0 {
		return nil, 0, 0, errors.New("建内存 DC 失败")
	}
	defer pDeleteDC.Call(dstDC)
	bmp, _, _ := pCreateCompatibleBitmap.Call(hScreen, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, 0, 0, errors.New("建位图失败")
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dstDC, bmp)
	if old != 0 {
		defer pSelectObject.Call(dstDC, old)
	}

	// PW_RENDERFULLCONTENT = 2：WebView2 是合成器渲染，普通 PrintWindow 常拿到黑图
	if ok, _, _ := pPrintWindow.Call(hwnd, dstDC, 2); ok == 0 {
		// 兜底：窗口可见时直接从屏幕对应位置拷（被遮挡时可能不全，但总比没有强）
		pBitBlt.Call(dstDC, 0, 0, uintptr(w), uintptr(h), hScreen,
			uintptr(int32(rc.Left)), uintptr(int32(rc.Top)), 0x00CC0020|0x40000000)
	}

	buf := make([]byte, w*h*4)
	bmi := dibInfo{BiSize: 40, BiWidth: int32(w), BiHeight: int32(-h), BiPlanes: 1, BiBitCount: 32}
	n, _, _ := pGetDIBits.Call(dstDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bmi)), 0)
	if n == 0 {
		return nil, 0, 0, errors.New("读回窗口像素失败（0 行）")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := buf[y*w*4:]
		for x := 0; x < w; x++ {
			i := x * 4
			o := y*img.Stride + x*4
			img.Pix[o+0] = row[i+2]
			img.Pix[o+1] = row[i+1]
			img.Pix[o+2] = row[i+0]
			img.Pix[o+3] = 0xFF
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, 0, 0, fmt.Errorf("编码 PNG 失败：%w", err)
	}
	return out.Bytes(), w, h, nil
}

/* ---------------- 小工具 ---------------- */

// jsQuote 把 Go 字符串安全地塞进 JS 字面量
func jsQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func argBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	}
	return false
}

// browserBridgeJS 注入到每个页面文档：只暴露白名单动作，不执行任意脚本。
const browserBridgeJS = `(function(){
  if (window.__baizeBridge) return;
  window.__baizeBridge = true;
  function reply(id, obj) { try { window.__baizeReply(id, JSON.stringify(obj)); } catch(e) {} }
  function findByText(t) {
    if (!t) return null;
    var all = document.querySelectorAll('a,button,input,textarea,[role=button],[onclick]');
    var i, s;
    for (i = 0; i < all.length; i++) { s = (all[i].innerText || all[i].value || '').trim(); if (s === t) return all[i]; }
    for (i = 0; i < all.length; i++) { s = (all[i].innerText || all[i].value || '').trim(); if (s && s.indexOf(t) >= 0) return all[i]; }
    return null;
  }
  window.__baizeRun = function(id, name, argJson) {
    var a = {};
    try { a = JSON.parse(argJson || '{}') || {}; } catch(e) { reply(id, {ok:false, error:'参数不是合法 JSON'}); return; }
    try {
      var out = null, el = null;
      if (name === 'read') {
        out = { url: location.href, title: document.title,
                text: (document.body ? (document.body.innerText || '') : '') };
      } else if (name === 'status') {
        out = { url: location.href, title: document.title, readyState: document.readyState };
      } else if (name === 'click') {
        el = a.selector ? document.querySelector(a.selector) : findByText(a.text);
        if (!el) throw new Error('页面上没找到要点的元素（selector/text）');
        el.scrollIntoView({block:'center', inline:'center'});
        el.click();
        out = { clicked: a.selector || a.text };
      } else if (name === 'type') {
        el = a.selector ? document.querySelector(a.selector) : document.activeElement;
        if (!el) throw new Error('页面上没找到输入框（selector）');
        el.focus();
        var text = (a.text === undefined || a.text === null) ? '' : String(a.text);
        var setter = Object.getOwnPropertyDescriptor(el.constructor.prototype, 'value');
        if (setter && setter.set) setter.set.call(el, text); else el.value = text;
        el.dispatchEvent(new Event('input', {bubbles:true}));
        el.dispatchEvent(new Event('change', {bubbles:true}));
        if (a.submit) {
          el.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', code:'Enter', keyCode:13, which:13, bubbles:true}));
          if (el.form && el.form.requestSubmit) el.form.requestSubmit();
        }
        out = { typed: a.selector || '(active)', chars: text.length, submitted: !!a.submit };
      } else {
        throw new Error('不支持的页面动作：' + name);
      }
      reply(id, {ok:true, value: out});
    } catch(e) {
      reply(id, {ok:false, error: String((e && e.message) ? e.message : e)});
    }
  };
  window.__baizeBanner = function(on) {
    var id = '__baize_handoff_bar';
    var e = document.getElementById(id);
    if (!on) { if (e) e.remove(); return; }
    if (e) return;
    e = document.createElement('div');
    e.id = id;
    e.style.cssText = 'position:fixed;left:0;right:0;top:0;z-index:2147483647;height:38px;display:flex;align-items:center;justify-content:space-between;gap:12px;padding:0 12px;background:#fff3cd;color:#664d03;font:13px/1.4 "Microsoft YaHei",sans-serif;box-shadow:0 1px 4px rgba(0,0,0,.2)';
    var s = document.createElement('span');
    s.textContent = '白泽已暂停，请你自己操作这个页面；操作完成后点右边按钮交还。';
    var b = document.createElement('button');
    b.textContent = '交还给白泽';
    b.style.cssText = 'flex:0 0 auto;border:1px solid #664d03;background:#fff;color:#664d03;border-radius:4px;padding:4px 10px;cursor:pointer';
    b.onclick = function(){ try { window.__baizeHandoffBack(); } catch(err) {} };
    e.appendChild(s); e.appendChild(b);
    document.documentElement.appendChild(e);
  };
})()`
