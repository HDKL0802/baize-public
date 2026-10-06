//go:build windows

// 桌面端「临时浮窗」——照豆包那套思路：**每个临时窗口一个独立进程**。
//
//	对话浮窗 / 提取文字 / 翻译：主进程用同一个 exe + `-float=<模式>` 再起一个进程，
//	子进程自己建一块 WebView2、跑自己的消息循环，互不干扰。
//
// 为什么不塞进主进程：go-webview2 的 edge 包在 init() 里就 runtime.LockOSThread()+CoInitializeEx，
// 整包绑死在主线程；同进程再开第二块会踩 COM 单线程 / 消息循环 / WM_QUIT 的坑（关浮窗＝整个应用退出）。
// 多进程天然隔离，正好对上 Chromium「一堆进程」的模型（豆包任务管理器里那一列 Doubao 子进程同理）。
//
// 浮窗形态：无边框 + 置顶 + 不进任务栏（WS_EX_TOOLWINDOW）；顶部一条自绘标题栏可拖动（点「钉住」锁死）。
//
// 每种模式**只有一个**浮窗：再次触发时把新图 POST 给已开的那个（端口号写在 <数据目录>\webview-float\<模式>.port），
// 页面轮询版本号自动刷新并重跑 —— 既省一次起进程/建环境（快），又不让用户开出一堆重复窗口。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const (
	floatModeChat      = "chat"
	floatModeExtract   = "extract"
	floatModeTranslate = "translate"

	floatHeaderH = 40  // 顶部标题栏高度（原生拖动区，和 float.html 的 .fhead 对齐）
	floatBtnZone = 132 // 标题栏右侧留给按钮的宽度：这块不参与拖动，不然按钮点不到
	floatEdgeGap = 12  // 浮窗靠边时离屏幕边缘留的缝

	floatImgMax = 64 << 20 // 推图上限（一张截图远到不了）

	gwlStyle        = ^uintptr(15) // GWL_STYLE   = -16
	gwlExStyle      = ^uintptr(19) // GWL_EXSTYLE = -20
	swpFrameChanged = 0x0020
	wsClipChildren  = 0x02000000
	htCaption       = 2
	wmNCHitTest     = 0x0084
	wmExitSizeMove  = 0x0232
)

var (
	fpUser32 = syscall.NewLazyDLL("user32.dll")

	pGetWindowLongPtrWF = fpUser32.NewProc("GetWindowLongPtrW")
	pGetWindowRectF     = fpUser32.NewProc("GetWindowRect")
	pPostMessageWF      = fpUser32.NewProc("PostMessageW")

	floatHWND      uintptr
	floatOldProc   uintptr
	floatModeNow   string // 本浮窗进程的模式（chat/extract/translate）
	floatWndProcCb = syscall.NewCallback(floatWndProc)

	// floatPinned 钉住：锁死不可拖。窗口线程（WM_NCHITTEST）与 HTTP 线程都会碰，用原子量。
	floatPinned atomic.Bool
	// floatRev 图片版本号：主进程推来新图就 +1，页面靠它判断「该重跑了」。
	floatRev atomic.Int64

	floatImgMu  sync.Mutex
	floatImgPNG []byte
)

// floatWindowTitle 各模式浮窗的窗口标题（也当去重用的标识）
func floatWindowTitle(mode string) string {
	switch mode {
	case floatModeExtract:
		return "白泽 · 提取文字"
	case floatModeTranslate:
		return "白泽 · 翻译"
	default:
		return "白泽 · 对话浮窗"
	}
}

func floatSize(mode string) (int, int) {
	switch mode {
	case floatModeChat:
		return 380, 560
	default:
		return 400, 470
	}
}

// floatDir 浮窗的落盘目录（profile + 端口文件）；没有就建。
func floatDir() string {
	d := filepath.Join(dataDir(), "webview-float")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// floatProfileDir 每种模式一个固定用户数据目录：复用 → 开得快，也不会攒一堆。
// （WebView2 不允许两个环境共用同一个用户数据目录，所以必须「一模式一个」；同模式同一时刻只开一个。）
func floatProfileDir(mode string) string { return filepath.Join(floatDir(), mode) }

// floatPortFile 记录该模式浮窗本机服务的端口，主进程靠它把新图 POST 过去。
func floatPortFile(mode string) string { return filepath.Join(floatDir(), mode+".port") }

// spawnFloatWindow 唤起（或复用）某个模式的浮窗（不带截图）。
//
//	已有同模式窗口 → 叫到前台；没有 → 起一个新进程（同一个 exe + `-float=<mode>`）。
func spawnFloatWindow(mode string) error { return spawnFloat(mode, false) }

// spawnFloatWithShot 带「最近一次截图」地唤起浮窗：提取文字 / 翻译 / 问问白泽 都走这条。
func spawnFloatWithShot(mode string) error { return spawnFloat(mode, true) }

func spawnFloat(mode string, withShot bool) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = floatModeChat
	}
	var png []byte
	if withShot {
		if shot, ok := ShotLatest(); ok && len(shot.PNG) > 0 {
			png = shot.PNG
		}
	}
	// 已有窗口：推图 + 置前，不重复起进程
	if hwnd := floatFind(mode); hwnd != 0 {
		if len(png) > 0 && pushFloatImage(mode, png) != nil {
			log.Printf("[浮窗] 新图推送失败（窗口还在，显示的是上一张）")
		}
		pShowWindow.Call(hwnd, swRestore)
		pSetForegroundWindow.Call(hwnd)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"-float", mode}
	if len(png) > 0 {
		p, err := writeFloatTemp(png)
		if err != nil {
			return err
		}
		args = append(args, "-img", p)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}

// floatFind 找该模式已开的浮窗（按窗口标题匹配）
func floatFind(mode string) uintptr {
	title, err := syscall.UTF16PtrFromString(floatWindowTitle(mode))
	if err != nil {
		return 0
	}
	hwnd, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(title)))
	return hwnd
}

// pushFloatImage 把新图 POST 给已开的浮窗进程（读它的端口文件）。
func pushFloatImage(mode string, png []byte) error {
	b, err := os.ReadFile(floatPortFile(mode))
	if err != nil {
		return err
	}
	port := strings.TrimSpace(string(b))
	if port == "" {
		return errors.New("端口文件为空")
	}
	req, err := http.NewRequest(http.MethodPost,
		"http://127.0.0.1:"+port+"/api/local/float/img", bytes.NewReader(png))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// writeFloatTemp 把图写到一个临时文件，交给浮窗子进程读（子进程读完即删）。
func writeFloatTemp(png []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), "baize-float")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("shot-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(p, png, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// floatIsOwnTemp 判断路径是不是我们自己写的临时图（<TEMP>\baize-float\ 底下）。
func floatIsOwnTemp(p string) bool {
	dir, err := filepath.Abs(filepath.Join(os.TempDir(), "baize-float"))
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// runFloatMode 浮窗子进程的主流程：读图 → 起本机小服务 → 建一块置顶无边框 WebView2 → 跑消息循环。
func runFloatMode(mode, imgPath string) {
	if strings.TrimSpace(mode) == "" {
		mode = floatModeChat
	}
	floatModeNow = mode
	if strings.TrimSpace(imgPath) != "" {
		if b, err := os.ReadFile(imgPath); err == nil {
			floatImgMu.Lock()
			floatImgPNG = b
			floatImgMu.Unlock()
		}
		// 只删「我们自己写的临时文件」：万一有人手滑把真文件传成 -img，绝不去动它
		if floatIsOwnTemp(imgPath) {
			_ = os.Remove(imgPath) // 读完即删：截图不留在盘上
		}
	}
	log.Printf("[浮窗] 启动：mode=%s（独立进程 pid=%d）", mode, os.Getpid())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[浮窗] 监听失败：%v", err)
		return
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if err := os.WriteFile(floatPortFile(mode), []byte(port), 0o600); err != nil {
		log.Printf("[浮窗] 写端口文件失败（主进程将无法推新图）：%v", err)
	}
	defer os.Remove(floatPortFile(mode))
	localBaseURL = "http://" + ln.Addr().String()
	go func() { _ = http.Serve(ln, buildHandler()) }()

	w, h := floatSize(mode)
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		// 一模式一个固定 profile：复用开得快，也不攒磁盘
		DataPath: floatProfileDir(mode),
		WindowOptions: webview2.WindowOptions{
			Title:  floatWindowTitle(mode),
			Width:  uint(w),
			Height: uint(h),
		},
	})
	if wv == nil {
		log.Printf("[浮窗] WebView2 初始化失败（系统缺 WebView2 运行时？）")
		return
	}
	defer wv.Destroy()
	mainWebView = wv

	hwnd := uintptr(wv.Window())
	floatHWND = hwnd
	mainHWND = hwnd
	styleFloatWindow(hwnd, w, h)

	wv.Navigate(localBaseURL + "/float.html#mode=" + mode)
	wv.Run()
	log.Printf("[浮窗] 已关闭：mode=%s", mode)
}

// styleFloatWindow 把 WebView2 那个默认窗口改成「无边框 + 置顶 + 不进任务栏」，并挂上拖动用的子类。
func styleFloatWindow(hwnd uintptr, w, h int) {
	// 无边框（WS_POPUP｜WS_CLIPCHILDREN）；工具窗口（不进任务栏 / Alt-Tab）；置顶
	pSetWindowLongPtrW.Call(hwnd, gwlStyle, wsPopup|wsClipChildren)
	es, _, _ := pGetWindowLongPtrWF.Call(hwnd, gwlExStyle)
	pSetWindowLongPtrW.Call(hwnd, gwlExStyle, es|uintptr(wsExToolWindow)|uintptr(wsExTopmost))

	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	// 默认贴着屏幕右边（临时窗口就该靠边待着，不在桌面中间乱飘）
	x := int32(sw) - int32(w) - floatEdgeGap
	y := int32(sh)/2 - int32(h)/2
	if x < floatEdgeGap {
		x = floatEdgeGap
	}
	if y < floatEdgeGap {
		y = floatEdgeGap
	}
	pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y),
		uintptr(w), uintptr(h), swpFrameChanged|swpShowWindow)

	floatOldProc, _, _ = pSetWindowLongPtrW.Call(hwnd, gwlpWndProc, floatWndProcCb)
}

// floatSnapToEdge 把浮窗吸到离它最近的那条屏幕边（用户要求：必须挨着某一条边）。
func floatSnapToEdge(hwnd uintptr) {
	var rc struct{ L, T, R, B int32 }
	pGetWindowRectF.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := rc.R-rc.L, rc.B-rc.T
	if w <= 0 || h <= 0 {
		return
	}
	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	W, H := int32(sw), int32(sh)

	x, y := rc.L, rc.T
	dl, dr := rc.L, W-rc.R
	dt, db := rc.T, H-rc.B
	m := dl
	if dr < m {
		m = dr
	}
	if dt < m {
		m = dt
	}
	if db < m {
		m = db
	}
	clampY := func() {
		if y < floatEdgeGap {
			y = floatEdgeGap
		}
		if y+h > H-floatEdgeGap {
			y = H - h - floatEdgeGap
		}
	}
	clampX := func() {
		if x < floatEdgeGap {
			x = floatEdgeGap
		}
		if x+w > W-floatEdgeGap {
			x = W - w - floatEdgeGap
		}
	}
	switch m {
	case dl:
		x = floatEdgeGap
		clampY()
	case dr:
		x = W - w - floatEdgeGap
		clampY()
	case dt:
		y = floatEdgeGap
		clampX()
	default:
		y = H - h - floatEdgeGap
		clampX()
	}
	if x != rc.L || y != rc.T {
		pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y),
			uintptr(w), uintptr(h), swpNoActivate|swpNoSize)
	}
}

// floatWndProc 子类化：顶部标题栏返回 HTCAPTION → Windows 自己就把窗口拖起来了（不用 JS 拖）。
// 右侧按钮区留给页面点；钉住之后整条都不给拖。
func floatWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if uint32(msg) == wmNCHitTest && !floatPinned.Load() {
		var rc struct{ L, T, R, B int32 }
		pGetWindowRectF.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		x := int32(int16(lparam & 0xffff))
		y := int32(int16((lparam >> 16) & 0xffff))
		localX, localY := x-rc.L, y-rc.T
		if localY >= 0 && localY < floatHeaderH && localX < (rc.R-rc.L)-floatBtnZone {
			return htCaption
		}
	}
	if uint32(msg) == wmExitSizeMove {
		floatSnapToEdge(hwnd) // 拖完松手 → 吸到最近的那条屏幕边
	}
	if floatOldProc == 0 {
		return 0
	}
	r, _, _ := pCallWindowProcW.Call(floatOldProc, hwnd, msg, wparam, lparam)
	return r
}

/* ---------------- 浮窗自己的本机接口（页面调自己这个进程） ---------------- */

func registerFloatRoutes(mux *http.ServeMux) {
	// 当前浮窗模式 + 有没有带图 + 图片版本号
	mux.HandleFunc("/api/local/float/mode", func(w http.ResponseWriter, r *http.Request) {
		floatImgMu.Lock()
		has := len(floatImgPNG) > 0
		floatImgMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "mode": floatModeNow, "title": floatWindowTitle(floatModeNow),
			"hasImage": has, "rev": floatRev.Load(),
		})
	})

	// 版本号：页面轮询它，变了就说明主进程推了新图，该重跑
	mux.HandleFunc("/api/local/float/rev", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rev": floatRev.Load()})
	})

	// 主进程推新图过来（原始 PNG 体）；图始终只在内存里
	mux.HandleFunc("/api/local/float/img", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 POST"})
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, floatImgMax))
		if err != nil || len(b) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "空图"})
			return
		}
		floatImgMu.Lock()
		floatImgPNG = b
		floatImgMu.Unlock()
		rev := floatRev.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rev": rev})
	})

	// 图本体（给 <img> 直接显示）
	mux.HandleFunc("/api/local/float/image", func(w http.ResponseWriter, r *http.Request) {
		floatImgMu.Lock()
		b := floatImgPNG
		floatImgMu.Unlock()
		if len(b) == 0 {
			http.Error(w, "没有图片", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})

	// 钉住 / 取消钉住：钉住＝锁死不可拖（窗口本身始终置顶，临时窗口就该「不散去」）
	mux.HandleFunc("/api/local/float/pin", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On *bool `json:"on"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		}
		if req.On != nil {
			floatPinned.Store(*req.On)
		} else {
			floatPinned.Store(!floatPinned.Load())
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinned": floatPinned.Load()})
	})

	mux.HandleFunc("/api/local/float/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinned": floatPinned.Load()})
	})

	// 关掉这个浮窗（＝销毁窗口 → 消息循环结束 → 本进程退出）
	// ⚠️ DestroyWindow 只能由「创建窗口的那个线程」调用；HTTP 处理器跑在别的线程上，
	// 直接调等于没关 —— 这正是之前「关闭按钮是个摆设」的原因。
	// 改成 PostMessage(WM_CLOSE)，让窗口线程自己去销毁。
	mux.HandleFunc("/api/local/float/close", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		if floatHWND != 0 {
			pPostMessageWF.Call(floatHWND, wmClose, 0, 0)
		}
	})
}
