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
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
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
const menuShot = 1004
const menuFloat = 1005

// 全局快捷键：截图提问 = Alt+Shift+S（豆包那套；先写死，之后进「快捷键可配」）
const (
	wmHotkey   = 0x0312
	modAlt     = 0x0001
	modShift   = 0x0004
	vkS        = 0x53
	hotkeyShot = 1
)

// SetWindowPos 的插入位序（HWND_TOPMOST = -1 / HWND_NOTOPMOST = -2，用补码表示）
const (
	hwndTopmost    = ^uintptr(0)
	hwndNotTopmost = ^uintptr(1)
)

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
	pRegisterHotKey    = ballUser32.NewProc("RegisterHotKey")
	pUnregisterHotKey  = ballUser32.NewProc("UnregisterHotKey")
	pSetWindowRgn      = ballUser32.NewProc("SetWindowRgn")

	pCreateSolidBrush  = ballGdi32.NewProc("CreateSolidBrush")
	pCreatePen         = ballGdi32.NewProc("CreatePen")
	pSelectObject      = ballGdi32.NewProc("SelectObject")
	pEllipse           = ballGdi32.NewProc("Ellipse")
	pDeleteObject      = ballGdi32.NewProc("DeleteObject")
	pCreateEllipticRgn = ballGdi32.NewProc("CreateEllipticRgn")
	pSetDIBitsToDevice = ballGdi32.NewProc("SetDIBitsToDevice")
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

	// 迷你对话小窗（左键单击悬浮球 = 开关；「工作模式」= 还原）
	ballMini     bool
	ballMiniRect struct{ Left, Top, Right, Bottom int32 } // 进入 mini 前的主窗口位置（还原用）
	ballClickMu  sync.Mutex
	ballClickGen int // 单击/双击区分用的世代号
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
	// 圆形：把 60×60 的方窗口裁成一个圆（不裁的话四个角会露出来，看着就是个方块）
	if hrgn, _, _ := pCreateEllipticRgn.Call(0, 0, ballSize, ballSize); hrgn != 0 {
		pSetWindowRgn.Call(hwnd, hrgn, 1)
	}
	// WS_EX_LAYERED 的窗口不显式设 alpha 就完全不显示 —— 这里设成不透明（球本身自带底色）
	pSetLayeredWinAttr.Call(hwnd, 0, 255, lwaAlpha)
	ballLoadIcon() // 预解码界面图标，画到球里
	// 按配置决定显不显示（隐藏状态也要把窗口建出来，设置页才能一键叫回来）
	if ballVisible() {
		pShowWindow.Call(hwnd, 4) // SW_SHOWNOACTIVATE：别抢焦点
		ballHotkey(true)
		log.Printf("[悬浮球] 已就绪（拖动移动 / 单击小窗 / 双击打开 / 右键菜单）")
	} else {
		log.Printf("[悬浮球] 按设置隐藏（可在「设置 → 常驻与自启」里打开）")
	}
	return nil
}

// ballHotkey 注册/注销全局快捷键 Alt+Shift+S = 截图提问（豆包同款）。失败只记日志、不致命。
func ballHotkey(on bool) {
	if ballHWND == 0 {
		return
	}
	if !on {
		pUnregisterHotKey.Call(ballHWND, hotkeyShot)
		return
	}
	if r, _, _ := pRegisterHotKey.Call(ballHWND, hotkeyShot, modAlt|modShift, vkS); r == 0 {
		log.Printf("[悬浮球] Alt+Shift+S 注册失败（可能被别的程序占用）：截图提问请走右键菜单")
	} else {
		log.Printf("[悬浮球] 快捷键已就绪：Alt+Shift+S = 截图提问")
	}
}

// ballSetVisibleNow 立刻显示/隐藏悬浮球（隐藏时顺手把全局快捷键让出去）
func ballSetVisibleNow(v bool) {
	if ballHWND == 0 {
		return
	}
	if v {
		pShowWindow.Call(ballHWND, 5) // SW_SHOW
		ballHotkey(true)
	} else {
		ballHotkey(false)
		pShowWindow.Call(ballHWND, 0) // SW_HIDE
	}
}

// ballDefaultPos 默认贴屏幕右下角；有记住的位置就用记住的（并吸到最近的那条边）。
func ballDefaultPos() (int32, int32) {
	w, _, _ := pGetSystemMetrics.Call(smCxScreen)
	h, _, _ := pGetSystemMetrics.Call(smCyScreen)
	if p, ok := ballLoadPos(); ok {
		// 保险：屏幕变小了也别把球甩到看不见的地方
		if p.X > 0 && p.Y > 0 && p.X < int32(w)-ballSize/4 && p.Y < int32(h)-ballSize/4 {
			return ballSnapToEdge(p.X, p.Y)
		}
	}
	return int32(w) - ballSize - ballMargin, int32(h) - ballSize - ballMargin
}

// ballSnapToEdge 把球吸到离它最近的那条屏幕边 —— 悬浮球就该靠着某条边待着，
// 而不是随便停在屏幕中间（豆包那类悬浮球都是靠边的）。
func ballSnapToEdge(x, y int32) (int32, int32) {
	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	w, h := int32(sw), int32(sh)
	dl, dr := x, w-(x+ballSize)
	dt, db := y, h-(y+ballSize)
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
	switch m {
	case dl:
		x = ballMargin
	case dr:
		x = w - ballSize - ballMargin
	case dt:
		y = ballMargin
	default:
		y = h - ballSize - ballMargin
	}
	// 兜底：无论如何别跑出屏幕
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > w-ballSize {
		x = w - ballSize
	}
	if y > h-ballSize {
		y = h - ballSize
	}
	return x, y
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
				nx, ny := ballSnapToEdge(rc.Left, rc.Top) // 松手就吸到最近的那条屏幕边
				if nx != rc.Left || ny != rc.Top {
					pSetWindowPos.Call(hwnd, 0, uintptr(nx), uintptr(ny), 0, 0,
						swpNoSize|swpNoZOrder|swpNoActivate)
				}
				ballSavePos(nx, ny)
			} else {
				// 没拖动 = 点了一下
				ballClick(hwnd)
			}
		}
		return 0
	case wmRButtonUp:
		ballShowMenu(hwnd)
		return 0
	case wmHotkey:
		if uint32(wparam) == hotkeyShot {
			startShotCapture()
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// ballPaintNow 画球：圆形窗口（已被 SetWindowRgn 裁成圆）里画界面图标，右下角叠一个状态点。
// 取不到图标就退回画一个渐变圆 —— 总之必须是个「圆」，不能是个方块。
func ballPaintNow(hwnd uintptr) {
	var ps ballPaint
	pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	hdc := ps.Hdc

	if !ballDrawIcon(hdc) {
		ballPaintGradient(hdc)
	}
	ballPaintStatusDot(hdc)
}

// ballPaintGradient 兜底：一圈圈同心圆近似「径向渐变」
func ballPaintGradient(hdc uintptr) {
	cx, cy := int32(ballSize/2), int32(ballSize/2)
	rad := int32(ballSize/2 - 1)
	for r := rad; r >= 0; r-- {
		t := float64(rad-r) / float64(rad)
		br, _, _ := pCreateSolidBrush.Call(ballRGB(int(30-16*t), int(37-19*t), int(48-24*t)))
		ob, _, _ := pSelectObject.Call(hdc, br)
		pEllipse.Call(hdc, uintptr(cx-r), uintptr(cy-r), uintptr(cx+r), uintptr(cy+r))
		pSelectObject.Call(hdc, ob)
		pDeleteObject.Call(br)
	}
}

// ballPaintStatusDot 右下角的状态点（先描一圈深色再画点，压在图标上也看得清）。
// 位置贴着圆的内侧对角：既在圆内，又不挡中间的图标。
func ballPaintStatusDot(hdc uintptr) {
	dot := ballRGB(56, 189, 248) // 空闲：天青
	r := int32(7)
	switch ballCursor {
	case 1:
		dot = ballRGB(245, 158, 11) // 有待审批：黄
		r = 8
	case 2:
		dot = ballRGB(130, 144, 162) // 连不上：灰
	}
	cx, cy := int32(ballSize/2+13), int32(ballSize/2+13)
	ring, _, _ := pCreateSolidBrush.Call(ballRGB(10, 13, 18))
	orng, _, _ := pSelectObject.Call(hdc, ring)
	pEllipse.Call(hdc, uintptr(cx-r-2), uintptr(cy-r-2), uintptr(cx+r+2), uintptr(cy+r+2))
	pSelectObject.Call(hdc, orng)
	pDeleteObject.Call(ring)

	db, _, _ := pCreateSolidBrush.Call(dot)
	odb, _, _ := pSelectObject.Call(hdc, db)
	pEllipse.Call(hdc, uintptr(cx-r), uintptr(cy-r), uintptr(cx+r), uintptr(cy+r))
	pSelectObject.Call(hdc, odb)
	pDeleteObject.Call(db)
}

/* ---------------- 球里的图标（界面那张 icon.png，缩到球尺寸） ---------------- */

var (
	ballIconMu  sync.Mutex
	ballIconPix []byte // ballSize×ballSize，BGRA（top-down）
)

// ballLoadIcon 预解码 ui/icon.png 到 ballSize×ballSize 的 BGRA；失败就留空（画的时候退回渐变）。
func ballLoadIcon() {
	dir, err := findUIDir()
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(dir, "icon.png"))
	if err != nil {
		return
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return
	}
	pix := scaleToBGRA(src, ballSize)
	if len(pix) == 0 {
		return
	}
	ballIconMu.Lock()
	ballIconPix = pix
	ballIconMu.Unlock()
	log.Printf("[悬浮球] 已载入球面图标 icon.png")
}

func ballDrawIcon(hdc uintptr) bool {
	ballIconMu.Lock()
	pix := ballIconPix
	ballIconMu.Unlock()
	if len(pix) == 0 {
		return false
	}
	var bih struct {
		Size          uint32
		Width         int32
		Height        int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPelsPerMeter int32
		YPelsPerMeter int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	bih.Size = 40
	bih.Width = int32(ballSize)
	bih.Height = -int32(ballSize) // 负 = top-down
	bih.Planes = 1
	bih.BitCount = 32
	bih.SizeImage = uint32(ballSize * ballSize * 4)
	r, _, _ := pSetDIBitsToDevice.Call(hdc, 0, 0, ballSize, ballSize, 0, 0, 0, ballSize,
		uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bih)), 0)
	return r != 0
}

// scaleToBGRA 双线性缩放到 size×size，输出 BGRA（top-down，GDI 直接吃）。
// 用非预乘的 NRGBA 取值：GDI 的 32bpp BI_RGB 不吃 alpha 字节，非预乘才不会在透明边缘发暗。
func scaleToBGRA(src image.Image, size int) []byte {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	out := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		sy := (float64(y)+0.5)*float64(sh)/float64(size) - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		if y0 < 0 {
			y0, fy = 0, 0
		}
		y1 := y0 + 1
		if y1 > sh-1 {
			y1 = sh - 1
		}
		for x := 0; x < size; x++ {
			sx := (float64(x)+0.5)*float64(sw)/float64(size) - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			if x0 < 0 {
				x0, fx = 0, 0
			}
			x1 := x0 + 1
			if x1 > sw-1 {
				x1 = sw - 1
			}
			c00 := nrgbaAt(src, sb.Min.X+x0, sb.Min.Y+y0)
			c10 := nrgbaAt(src, sb.Min.X+x1, sb.Min.Y+y0)
			c01 := nrgbaAt(src, sb.Min.X+x0, sb.Min.Y+y1)
			c11 := nrgbaAt(src, sb.Min.X+x1, sb.Min.Y+y1)
			o := (y*size + x) * 4
			out[o] = byte(bilerp(c00[2], c10[2], c01[2], c11[2], fx, fy) + 0.5) // B
			out[o+1] = byte(bilerp(c00[1], c10[1], c01[1], c11[1], fx, fy) + 0.5)
			out[o+2] = byte(bilerp(c00[0], c10[0], c01[0], c11[0], fx, fy) + 0.5) // R
			out[o+3] = byte(bilerp(c00[3], c10[3], c01[3], c11[3], fx, fy) + 0.5)
		}
	}
	return out
}

func bilerp(v00, v10, v01, v11, fx, fy float64) float64 {
	top := v00*(1-fx) + v10*fx
	bot := v01*(1-fx) + v11*fx
	return top*(1-fy) + bot*fy
}

func nrgbaAt(src image.Image, x, y int) [4]float64 {
	c := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
	return [4]float64{float64(c.R), float64(c.G), float64(c.B), float64(c.A)}
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
	// 照豆包的排布：窗口 / 对话浮窗 / 截图提问 / --- / 待审批 / --- / 退出
	appendMenu(hmenu, mfString, menuOpen, "打开白泽窗口")
	appendMenu(hmenu, mfString, menuFloat, "打开对话浮窗")
	appendMenu(hmenu, mfString, menuShot, "截图提问")
	appendMenu(hmenu, mfSeparator, 0, "")
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
	case menuFloat:
		if err := spawnFloatWindow(floatModeChat); err != nil {
			log.Printf("[悬浮球] 起对话浮窗失败：%v", err)
		}
	case menuShot:
		startShotCapture()
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

// ballClick 悬浮球左键单击：单击 = 迷你对话小窗（开关）、双击 = 打开完整窗口。
// 用 350ms 的双击窗口区分：第一下先挂个定时器，窗口内来了第二下就取消、当双击。
func ballClick(hwnd uintptr) {
	ballClickMu.Lock()
	ballClickGen++
	gen := ballClickGen
	ballClickMu.Unlock()

	if !ballLastClick.IsZero() && time.Since(ballLastClick) < ballDoubleClick {
		ballLastClick = time.Time{}
		ballClickMu.Lock()
		ballClickGen++ // 让挂起的单击失效
		ballClickMu.Unlock()
		ballOpenMain(false) // 双击：完整窗口
		return
	}
	ballLastClick = time.Now()
	time.AfterFunc(ballDoubleClick, func() {
		ballClickMu.Lock()
		still := ballClickGen == gen
		ballClickMu.Unlock()
		if still { // 没等来第二下 → 当单击
			// 单击＝唤起「对话浮窗」（独立进程的小窗，不缩主窗；这就是豆包的用法）
			if err := spawnFloatWindow(floatModeChat); err != nil {
				log.Printf("[悬浮球] 起对话浮窗失败：%v", err)
			}
		}
	})
}

// toggleMiniChat 在「迷你对话小窗」与「工作模式」之间切换。
func toggleMiniChat() {
	if mainHWND == 0 {
		return
	}
	if ballMini {
		exitMiniChat()
	} else {
		enterMiniChat()
	}
}

// enterMiniChat 把主窗口变成贴右下角的**置顶小窗**并跳到对话（= 豆包的「小窗」）。
func enterMiniChat() {
	var rc struct{ Left, Top, Right, Bottom int32 }
	pGetWindowRectBall.Call(mainHWND, uintptr(unsafe.Pointer(&rc)))
	ballMiniRect = rc
	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	const w, h = 400, 600
	x := int32(sw) - w - 24
	y := int32(sh) - h - 24
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	pSetWindowPos.Call(mainHWND, hwndTopmost, uintptr(x), uintptr(y), w, h, swpShowWindow)
	ballMini = true
	if mainWebView != nil {
		mainWebView.Dispatch(func() {
			mainWebView.Eval(`location.hash = '#chat'`)
		})
	}
	log.Printf("[悬浮球] 进入迷你对话小窗 %dx%d", w, h)
}

// exitMiniChat 还原成普通窗口（去掉置顶、恢复进入前的位置与大小）= 豆包的「工作模式」。
func exitMiniChat() {
	rc := ballMiniRect
	if rc.Right-rc.Left <= 0 || rc.Bottom-rc.Top <= 0 { // 没记住过就别乱设，保底给个常规尺寸
		rc = struct{ Left, Top, Right, Bottom int32 }{Left: 120, Top: 90, Right: 1400, Bottom: 890}
	}
	pSetWindowPos.Call(mainHWND, hwndNotTopmost,
		uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top), swpShowWindow)
	ballMini = false
	log.Printf("[悬浮球] 已切回工作模式")
}

// miniNow 供界面查询当前是否在迷你小窗
func miniNow() bool { return ballMini }

// registerBallRoutes 给界面用的本机接口：迷你小窗状态查询 / 切换。
func registerBallRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/local/ball/mini", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				On *bool `json:"on"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
			}
			switch {
			case req.On == nil:
				toggleMiniChat()
			case *req.On:
				if !ballMini {
					enterMiniChat()
				}
			default:
				if ballMini {
					exitMiniChat()
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mini": miniNow()})
	})

	// 悬浮球显示 / 隐藏。用户把球弄丢了（或主动关掉）之后，在这里能一键找回来。
	mux.HandleFunc("/api/local/ball/visible", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Visible *bool `json:"visible"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
			}
			v := ballVisible()
			if req.Visible != nil {
				v = *req.Visible
				setBallVisible(v)
			}
			ballSetVisibleNow(v)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "visible": ballVisible()})
	})
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
