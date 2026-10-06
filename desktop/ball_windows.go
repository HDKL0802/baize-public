//go:build windows

// 桌面端「悬浮球」（B1）：常驻桌面的轻量入口，照豆包电脑版那套做。
//
//	拖动 = 移动（位置记到 <数据目录>\ball.pos，下次还在原地）
//	单击 = 弹快捷菜单（打开白泽 / 待审批(N) / 退出）
//	双击 = 直接打开主窗口（多加的一个便利：不用先弹菜单）
//	球的颜色 = 状态：天青=空闲、黄=有待审批、灰=连不上后端
//
// 纯 Win32 syscall，不引新依赖；窗口跑在自己的线程上（Win32 窗口是线程亲和的，
// WebView2 那边已经占了主线程的消息循环，所以这里必须 LockOSThread 另起一套）。
// 每一步失败都只记日志、不致命 —— 壳必须能起来。
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	ballSize   = 60 // 直径（像素）
	ballMargin = 26 // 默认离屏幕右下角的间距

	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	lwaAlpha        = 0x00000002
	swpNoSize       = 0x0001
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	smCxScreen      = 0
	smCyScreen      = 1
	wmPaint         = 0x000F
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	ballDoubleClick = 350 * time.Millisecond
)

// 悬浮球菜单项（menuOpen / menuQuit 复用托盘那两个 id）
const menuTasks = 1003

var (
	ballUser32 = syscall.NewLazyDLL("user32.dll")
	ballGdi32  = syscall.NewLazyDLL("gdi32.dll")

	pRegisterClassExW  = ballUser32.NewProc("RegisterClassExW")
	pCreateWindowExW   = ballUser32.NewProc("CreateWindowExW")
	pDefWindowProcW    = ballUser32.NewProc("DefWindowProcW")
	pGetMessageW       = ballUser32.NewProc("GetMessageW")
	pTranslateMessage  = ballUser32.NewProc("TranslateMessage")
	pDispatchMessageW  = ballUser32.NewProc("DispatchMessageW")
	pPostQuitMessage   = ballUser32.NewProc("PostQuitMessage")
	pSetCapture        = ballUser32.NewProc("SetCapture")
	pReleaseCapture    = ballUser32.NewProc("ReleaseCapture")
	pSetWindowPos      = ballUser32.NewProc("SetWindowPos")
	pInvalidateRect    = ballUser32.NewProc("InvalidateRect")
	pBeginPaint        = ballUser32.NewProc("BeginPaint")
	pEndPaint          = ballUser32.NewProc("EndPaint")
	pGetClientRect     = ballUser32.NewProc("GetClientRect")
	pDestroyWindow     = ballUser32.NewProc("DestroyWindow")
	pLoadCursorW       = ballUser32.NewProc("LoadCursorW")
	pGetSystemMetrics  = ballUser32.NewProc("GetSystemMetrics")
	pSetLayeredWinAttr = ballUser32.NewProc("SetLayeredWindowAttributes")
	pGetCursorPosBall  = ballUser32.NewProc("GetCursorPos")
	pGetWindowRectBall = ballUser32.NewProc("GetWindowRect")

	pCreateSolidBrush = ballGdi32.NewProc("CreateSolidBrush")
	pCreatePen        = ballGdi32.NewProc("CreatePen")
	pSelectObject     = ballGdi32.NewProc("SelectObject")
	pEllipse          = ballGdi32.NewProc("Ellipse")
	pDeleteObject     = ballGdi32.NewProc("DeleteObject")
)

type ballWndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type ballMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type ballPaint struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     struct{ Left, Top, Right, Bottom int32 }
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type ballPoint struct{ X, Y int32 }

var (
	ballHWND   uintptr
	ballClass  = "BaizeFloatBall"
	ballOnce   sync.Once
	ballCursor int32 // 0=空闲 1=待审批 2=离线

	ballDrag       bool
	ballDragMoved  bool
	ballDragStart  ballPoint // 按下时光标位置
	ballDragOrigin ballPoint // 按下时窗口位置
	ballLastClick  time.Time
	ballPending    int
	ballWndProcCb  = syscall.NewCallback(ballWndProc)
)

// localBaseURL 本机回环服务的基址（main 里设置），悬浮球用它读状态。
var localBaseURL string

// startFloatingBall 起悬浮球（在自己的线程上建窗 + 跑消息循环）。失败只记日志。
func startFloatingBall() {
	ballOnce.Do(func() {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			if err := ballCreate(); err != nil {
				log.Printf("[悬浮球] 没起来：%v（其余功能不受影响）", err)
				return
			}
			ballPollLoop()
			ballMessageLoop()
		}()
	})
}

func ballCreate() error {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	cls, _ := syscall.UTF16PtrFromString(ballClass)
	wc := ballWndClassEx{
		Style:         0x0001 | 0x0002, // CS_VREDRAW | CS_HREDRAW
		LpfnWndProc:   ballWndProcCb,
		HInstance:     hInst,
		HCursor:       cursor,
		LpszClassName: cls,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, callErr := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return callErr
	}

	x, y := ballDefaultPos()
	title, _ := syscall.UTF16PtrFromString("白泽悬浮球")
	hwnd, _, callErr := pCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow|wsExLayered,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsPopup, uintptr(x), uintptr(y), ballSize, ballSize,
		0, 0, hInst, 0)
	if hwnd == 0 {
		return callErr
	}
	ballHWND = hwnd
	// 半透明（220/255），看起来更像"浮"在桌面上
	pSetLayeredWinAttr.Call(hwnd, 0, 220, lwaAlpha)
	pShowWindow.Call(hwnd, 4) // SW_SHOWNOACTIVATE：别抢焦点
	log.Printf("[悬浮球] 已就绪（拖动移动 / 单击菜单 / 双击打开）")
	return nil
}

// ballDefaultPos 默认贴屏幕右下角；有记住的位置就用记住的。
func ballDefaultPos() (int32, int32) {
	w, _, _ := pGetSystemMetrics.Call(smCxScreen)
	h, _, _ := pGetSystemMetrics.Call(smCyScreen)
	if p, ok := ballLoadPos(); ok {
		// 保险：屏幕变小了也别把球甩到看不见的地方
		if p.X > int32(w)-ballSize/2 && p.Y > int32(h)-ballSize/2 {
			return p.X, p.Y
		}
	}
	return int32(w) - ballSize - ballMargin, int32(h) - ballSize - ballMargin
}

func ballPosFile() string { return filepath.Join(dataDir(), "ball.pos") }

func ballLoadPos() (ballPoint, bool) {
	b, err := os.ReadFile(ballPosFile())
	if err != nil {
		return ballPoint{}, false
	}
	parts := strings.Split(strings.TrimSpace(string(b)), ",")
	if len(parts) != 2 {
		return ballPoint{}, false
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	y, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return ballPoint{}, false
	}
	return ballPoint{X: int32(x), Y: int32(y)}, true
}

func ballSavePos(x, y int32) {
	_ = os.WriteFile(ballPosFile(), []byte(strconv.Itoa(int(x))+","+strconv.Itoa(int(y))), 0o600)
}

func ballMessageLoop() {
	var m ballMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func ballWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		ballPaintNow(hwnd)
		return 0
	case wmLButtonDown:
		ballDrag = true
		ballDragMoved = false
		pGetCursorPosBall.Call(uintptr(unsafe.Pointer(&ballDragStart)))
		var rc struct{ Left, Top, Right, Bottom int32 }
		pGetWindowRectBall.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		ballDragOrigin = ballPoint{X: rc.Left, Y: rc.Top}
		pSetCapture.Call(hwnd)
		return 0
	case wmMouseMove:
		if ballDrag {
			var pt ballPoint
			pGetCursorPosBall.Call(uintptr(unsafe.Pointer(&pt)))
			dx, dy := pt.X-ballDragStart.X, pt.Y-ballDragStart.Y
			if dx > 3 || dx < -3 || dy > 3 || dy < -3 {
				ballDragMoved = true
			}
			pSetWindowPos.Call(hwnd, 0,
				uintptr(ballDragOrigin.X+dx), uintptr(ballDragOrigin.Y+dy), 0, 0,
				swpNoSize|swpNoZOrder|swpNoActivate)
		}
		return 0
	case wmLButtonUp:
		pReleaseCapture.Call()
		if ballDrag {
			ballDrag = false
			if ballDragMoved {
				var rc struct{ Left, Top, Right, Bottom int32 }
				pGetWindowRectBall.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
				ballSavePos(rc.Left, rc.Top)
			} else {
				// 没拖动 = 点了一下：双击直接打开，单击弹菜单
				now := time.Now()
				if !ballLastClick.IsZero() && now.Sub(ballLastClick) < ballDoubleClick {
					ballLastClick = time.Time{}
					ballOpenMain(false)
				} else {
					ballLastClick = now
					ballShowMenu(hwnd)
				}
			}
		}
		return 0
	case wmRButtonUp:
		ballShowMenu(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// ballPaintNow 画球：深色圆底 + 细描边 + 中心一个状态点。
func ballPaintNow(hwnd uintptr) {
	var ps ballPaint
	pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	hdc := ps.Hdc

	body, _, _ := pCreateSolidBrush.Call(ballRGB(18, 22, 29))
	pen, _, _ := pCreatePen.Call(0, 1, ballRGB(58, 70, 86))
	ob, _, _ := pSelectObject.Call(hdc, body)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pEllipse.Call(hdc, 1, 1, ballSize-1, ballSize-1)
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(body)
	pDeleteObject.Call(pen)

	dot := ballRGB(56, 189, 248) // 空闲：天青
	switch ballCursor {
	case 1:
		dot = ballRGB(245, 158, 11) // 有待审批：黄
	case 2:
		dot = ballRGB(100, 116, 139) // 连不上：灰
	}
	// 有待审批时，中心点画大一点（一眼看出"要你去放行"）
	r := 9
	if ballCursor == 1 {
		r = 12
	}
	db, _, _ := pCreateSolidBrush.Call(dot)
	odb, _, _ := pSelectObject.Call(hdc, db)
	cx, cy := ballSize/2, ballSize/2
	pEllipse.Call(hdc, uintptr(cx-r), uintptr(cy-r), uintptr(cx+r), uintptr(cy+r))
	pSelectObject.Call(hdc, odb)
	pDeleteObject.Call(db)
}

func ballRGB(r, g, b int) uintptr {
	return uintptr(r | g<<8 | b<<16)
}

// ballShowMenu 快捷菜单（打开白泽 / 待审批(N) / 退出）
func ballShowMenu(hwnd uintptr) {
	hmenu, _, _ := pCreatePopupMenu.Call()
	if hmenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hmenu)
	appendMenu(hmenu, mfString, menuOpen, "打开白泽")
	label := "待审批"
	if ballPending > 0 {
		label = "待审批（" + strconv.Itoa(ballPending) + "）"
	}
	appendMenu(hmenu, mfString, menuTasks, label)
	appendMenu(hmenu, mfSeparator, 0, "")
	appendMenu(hmenu, mfString, menuQuit, "退出")

	var pt ballPoint
	pGetCursorPosBall.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(hmenu, tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	switch cmd {
	case menuOpen:
		ballOpenMain(false)
	case menuTasks:
		ballOpenMain(true)
	case menuQuit:
		quitApp()
	}
}

// ballOpenMain 打开主窗口；toTasks=true 时顺带跳到「任务与审批」（去放行）。
func ballOpenMain(toTasks bool) {
	if mainHWND != 0 {
		pShowWindow.Call(mainHWND, swRestore)
		pSetForegroundWindow.Call(mainHWND)
	}
	if toTasks && mainWebView != nil {
		mainWebView.Dispatch(func() {
			mainWebView.Eval(`location.hash = '#tasks'`)
		})
	}
}

// ballPollLoop 每 10 秒问一次本机回环服务：有多少条待审批（工具审批 + 跨端任务）。
// 拿不到（后端没连/没起）就把球画成灰的 —— 如实反映，不装作正常。
func ballPollLoop() {
	go func() {
		for {
			ballRefreshState()
			time.Sleep(10 * time.Second)
		}
	}()
}

func ballRefreshState() {
	old := ballCursor
	base := strings.TrimRight(strings.TrimSpace(localBaseURL), "/")
	if base == "" {
		ballCursor = 2
	} else {
		client := &http.Client{Timeout: 4 * time.Second}
		n, ok := ballCountPending(client, base)
		switch {
		case !ok:
			ballCursor = 2
		case n > 0:
			ballCursor = 1
			ballPending = n
		default:
			ballCursor = 0
			ballPending = 0
		}
	}
	if ballHWND != 0 && ballCursor != old {
		pInvalidateRect.Call(ballHWND, 0, 1)
	}
}

func ballCountPending(client *http.Client, base string) (int, bool) {
	n := 0
	// 工具审批
	if body, ok := ballGet(client, base+"/api/be/api/agent/approvals"); ok {
		var out struct {
			Approvals []struct {
				Status string `json:"status"`
			} `json:"approvals"`
		}
		if json.Unmarshal(body, &out) == nil {
			for _, a := range out.Approvals {
				if a.Status == "pending" {
					n++
				}
			}
		}
	} else {
		return 0, false
	}
	// 跨端任务
	if body, ok := ballGet(client, base+"/api/be/api/state"); ok {
		var out struct {
			Tasks []struct {
				Status string `json:"status"`
			} `json:"tasks"`
		}
		if json.Unmarshal(body, &out) == nil {
			for _, t := range out.Tasks {
				if t.Status == "pending_approval" {
					n++
				}
			}
		}
	} else {
		return 0, false
	}
	return n, true
}

func ballGet(client *http.Client, url string) ([]byte, bool) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return b, true
}
