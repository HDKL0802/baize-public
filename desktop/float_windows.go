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
	// 语音线（B1）：语音转写（录音→文字→存笔记）与实时双语字幕（贴屏幕底部的置顶横条）
	floatModeDictate  = "dictate"
	floatModeSubtitle = "subtitle"

	floatEdgeGap = 12       // 浮窗靠边时离屏幕边缘留的缝
	floatImgMax  = 64 << 20 // 推图上限（一张截图远到不了）

	wsClipChildren = 0x02000000 // 创建窗口时就带上：别让父窗口把 WebView2 子窗口画花
)

var (
	fpUser32 = syscall.NewLazyDLL("user32.dll")

	pGetWindowRectF     = fpUser32.NewProc("GetWindowRect")
	pPostMessageWF      = fpUser32.NewProc("PostMessageW")
	pSetWindowsHookExWF = fpUser32.NewProc("SetWindowsHookExW")
	pUnhookWinHookF     = fpUser32.NewProc("UnhookWindowsHookEx")
	pCallNextHookExF    = fpUser32.NewProc("CallNextHookEx")

	pGetCurrentThreadIdF = shKernel32.NewProc("GetCurrentThreadId")

	floatCbtCb = syscall.NewCallback(floatCbtProc)

	// CBT 钩子要拦截的目标位置（一次性：命中我们那个窗口后自动失效）
	floatCbtMu sync.Mutex
	floatCbtOn bool
	floatCbtW  int32
	floatCbtH  int32
	floatCbtX  int32
	floatCbtY  int32

	floatHWND    uintptr
	floatModeNow string // 本浮窗进程的模式（chat/extract/translate）

	// floatPinned 钉住：锁死不可拖（页面拖动时先问它）
	floatPinned atomic.Bool
	// floatRev 图片版本号：主进程推来新图就 +1，页面靠它判断「该重跑了」。
	floatRev atomic.Int64

	floatImgMu  sync.Mutex
	floatImgPNG []byte
)

/* ---------- 「创建即在位」：CBT 钩子改 CREATESTRUCT ----------
   go-webview2 建窗固定用 CW_USEDEFAULT，之后我们才 SetWindowPos —— 于是会先在默认位置
   闪出一个白框、再跳到目标位置（用户一眼就看到这一跳）。用线程级 WH_CBT 钩子拦
   HCBT_CREATEWND，在窗口真正创建前把 x/y 与样式改掉，就完全没有这一跳。
   必须是**线程级**（dwThreadId = 当前线程）：全局钩子会被注入别的进程，Go 回调会崩。 */

const (
	whCbt         = 5
	hcbtCreateWnd = 3
)

type cbtCreateWnd struct {
	Lpcs            uintptr // CREATESTRUCT*
	HwndInsertAfter uintptr
}

type createStructW struct {
	LpCreateParams uintptr
	HInstance      uintptr
	HMenu          uintptr
	HwndParent     uintptr
	Cy             int32
	Cx             int32
	Y              int32
	X              int32
	Style          uint32
	LpszName       uintptr
	LpszClass      uintptr
	DwExStyle      uint32
}

// floatCbtInstall 在当前线程装 CBT 钩子，并记下目标位置；返回钩子句柄（0 = 没装上）。
func floatCbtInstall(w, h, x, y int) uintptr {
	floatCbtMu.Lock()
	floatCbtW, floatCbtH, floatCbtX, floatCbtY = int32(w), int32(h), int32(x), int32(y)
	floatCbtOn = true
	floatCbtMu.Unlock()
	tid, _, _ := pGetCurrentThreadIdF.Call()
	hk, _, _ := pSetWindowsHookExWF.Call(whCbt, floatCbtCb, 0, tid)
	if hk == 0 {
		log.Printf("[浮窗] CBT 钩子没装上：窗口可能先闪一下再归位（功能不受影响）")
		floatCbtMu.Lock()
		floatCbtOn = false
		floatCbtMu.Unlock()
	}
	return hk
}

func floatCbtUninstall(hk uintptr) {
	if hk != 0 {
		pUnhookWinHookF.Call(hk)
	}
}

func floatCbtProc(nCode int32, wparam, lparam uintptr) uintptr {
	if nCode == hcbtCreateWnd {
		floatCbtMu.Lock()
		on, w, h, x, y := floatCbtOn, floatCbtW, floatCbtH, floatCbtX, floatCbtY
		floatCbtMu.Unlock()
		if on && lparam != 0 {
			// ⚠️ uintptr→unsafe.Pointer 会被 go vet 判成 unsafeptr，所以用 RtlMoveMemory 逐块搬
			// （和 clip_windows.go 一个套路）：把结构拷进来改，再拷回去。
			var cc cbtCreateWnd
			pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&cc)), lparam, unsafe.Sizeof(cc))
			if cc.Lpcs != 0 {
				var cs createStructW
				pRtlMoveMemory.Call(uintptr(unsafe.Pointer(&cs)), cc.Lpcs, unsafe.Sizeof(cs))
				// 只认我们那一个：顶层窗口，且尺寸正好是请求的 w×h
				if cs.HwndParent == 0 && cs.Cx == w && cs.Cy == h {
					cs.X, cs.Y = x, y
					cs.Style = wsPopup | wsClipChildren
					cs.DwExStyle |= wsExToolWindow | wsExTopmost
					// 字幕条：连点它都不该把当前窗口的焦点抢走（纯覆盖层）
					if floatModeNow == floatModeSubtitle {
						cs.DwExStyle |= wsExNoActivate
					}
					pRtlMoveMemory.Call(cc.Lpcs, uintptr(unsafe.Pointer(&cs)), unsafe.Sizeof(cs))
					floatCbtMu.Lock()
					floatCbtOn = false // 一次性，命中即失效
					floatCbtMu.Unlock()
				}
			}
		}
	}
	r, _, _ := pCallNextHookExF.Call(0, uintptr(int(nCode)), wparam, lparam)
	return r
}

// floatInitialPos 浮窗的初始位置。
//
//	默认贴屏幕右边、垂直居中；subtitle（实时字幕）例外 —— 它要的是「屏幕底部一条横杠」。
func floatInitialPos(mode string, w, h int) (int32, int32) {
	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	if mode == floatModeSubtitle {
		x := int32(sw)/2 - int32(w)/2
		y := int32(sh) - int32(h) - floatEdgeGap
		if x < floatEdgeGap {
			x = floatEdgeGap
		}
		if y < floatEdgeGap {
			y = floatEdgeGap
		}
		return x, y
	}
	x := int32(sw) - int32(w) - floatEdgeGap
	y := int32(sh)/2 - int32(h)/2
	if x < floatEdgeGap {
		x = floatEdgeGap
	}
	if y < floatEdgeGap {
		y = floatEdgeGap
	}
	return x, y
}

// floatWindowTitle 各模式浮窗的窗口标题（也当去重用的标识）
func floatWindowTitle(mode string) string {
	switch mode {
	case floatModeExtract:
		return "白泽 · 提取文字"
	case floatModeTranslate:
		return "白泽 · 翻译"
	case floatModeDictate:
		return "白泽 · 语音转写"
	case floatModeSubtitle:
		return "白泽 · 实时字幕"
	default:
		return "白泽 · 对话浮窗"
	}
}

// floatSize 各模式浮窗的尺寸。
//
//	subtitle（实时字幕）要的是「屏幕底部一整条」：宽度铺满工作区，高度只留两三行字幕，
//	所以这里直接按屏幕宽度算（屏幕变小/换显示器后重开会重新算）。
func floatSize(mode string) (int, int) {
	switch mode {
	case floatModeChat:
		return 380, 560
	case floatModeDictate:
		return 420, 540
	case floatModeSubtitle:
		sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
		w := int(sw) - 2*floatEdgeGap
		if w < 480 {
			w = 480
		}
		return w, 172
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
	ix, iy := floatInitialPos(mode, w, h)
	hk := floatCbtInstall(w, h, int(ix), int(iy))
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		// 字幕条不该抢焦点（用户多半正看着电影/别的窗口），其余浮窗照旧自动聚焦。
		AutoFocus: mode != floatModeSubtitle,
		// 一模式一个固定 profile：复用开得快，也不攒磁盘
		DataPath: floatProfileDir(mode),
		WindowOptions: webview2.WindowOptions{
			Title:  floatWindowTitle(mode),
			Width:  uint(w),
			Height: uint(h),
		},
	})
	floatCbtUninstall(hk)
	if wv == nil {
		log.Printf("[浮窗] WebView2 初始化失败（系统缺 WebView2 运行时？）")
		return
	}
	defer wv.Destroy()
	mainWebView = wv

	hwnd := uintptr(wv.Window())
	floatHWND = hwnd
	mainHWND = hwnd
	floatPlace(hwnd, w, h, ix, iy)

	wv.Navigate(localBaseURL + "/float.html#mode=" + mode)
	wv.Run()
	log.Printf("[浮窗] 已关闭：mode=%s", mode)
}

// floatPlace 兜底摆位 + 显示：正常情况下 CBT 钩子在窗口创建时就定好了位置与样式，
// 这里再确认一次（也顺带保证置顶）。字幕条额外带 SWP_NOACTIVATE，别把焦点抢走。
func floatPlace(hwnd uintptr, w, h int, x, y int32) {
	flags := uintptr(swpShowWindow)
	if floatModeNow == floatModeSubtitle {
		flags |= swpNoActivate
	}
	pSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y),
		uintptr(w), uintptr(h), flags)
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

/* ---------------- 浮窗自己的本机接口（页面调自己这个进程） ---------------- */

// floatModeKnown 只放行我们认识的浮窗模式。
// ⚠️ 必须白名单：模式名会被拼进 floatProfileDir(mode) / floatPortFile(mode) 这类**路径**，
// 放行任意字符串等于给本机接口开了一条路径穿越（`../` 就能写到目录外）。
func floatModeKnown(mode string) bool {
	switch mode {
	case floatModeChat, floatModeExtract, floatModeTranslate, floatModeDictate, floatModeSubtitle:
		return true
	}
	return false
}

func registerFloatRoutes(mux *http.ServeMux) {
	// 从主界面（设置页）唤起浮窗：字幕模式是「开/关」切换，其余是打开/置前
	mux.HandleFunc("/api/local/float/open", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		}
		mode := strings.TrimSpace(req.Mode)
		if !floatModeKnown(mode) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": "不认识的浮窗模式：" + mode + "（可用 chat / extract / translate / dictate / subtitle）"})
			return
		}
		if mode == floatModeSubtitle {
			ballToggleFloat(mode)
		} else {
			ballOpenFloat(mode)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

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

	// 拖动：页面按住标题栏 → 每次 pointermove 把增量发过来（本机回环，够跟手）。
	// 为什么不用原生 WM_NCHITTEST/HTCAPTION：WebView2 的客户区是**子窗口**，
	// 鼠标命中测试由子窗口接管，顶层窗口根本收不到 WM_NCHITTEST —— 那条路走不通。
	mux.HandleFunc("/api/local/float/move", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Dx int `json:"dx"`
			Dy int `json:"dy"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		}
		if floatHWND != 0 && (req.Dx != 0 || req.Dy != 0) {
			var rc struct{ L, T, R, B int32 }
			pGetWindowRectF.Call(floatHWND, uintptr(unsafe.Pointer(&rc)))
			pSetWindowPos.Call(floatHWND, hwndTopmost,
				uintptr(rc.L+int32(req.Dx)), uintptr(rc.T+int32(req.Dy)), 0, 0,
				swpNoSize|swpNoActivate)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 松开鼠标：吸到最近的那条屏幕边
	mux.HandleFunc("/api/local/float/snap", func(w http.ResponseWriter, r *http.Request) {
		if floatHWND != 0 {
			floatSnapToEdge(floatHWND)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
