//go:build windows

// 桌面端「截图提问」的原生部分：抓屏 → 全屏框选 → 存 PNG。
//
//	抓屏：GetDC(NULL) 抓**整个虚拟屏**（多显示器也算上），BitBlt 到内存 DC，再 GetDIBits 取 BGRA。
//	框选：再起一个覆盖全屏的置顶无边框窗口，把刚抓的图当背景画上去，拖出一个选框；Esc 取消。
//	落盘：把选中的那块裁出来、编码成 PNG，存到 <数据目录>\shot-latest.png（并留一份带时间戳的）。
//
// 之后交给网页界面（#shot）去「复制 / 提取文字 / 翻译」。纯 Win32 syscall，不引新依赖。
package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCxVirtualScreen = 78
	smCyVirtualScreen = 79

	wmKeyDown = 0x0100
	vkEscape  = 0x1B

	shotOverlayClass = "BaizeShotOverlay"

	// RegisterClassExW 第二次注册同一个类会失败（ERROR_CLASS_ALREADY_EXISTS）。这不是错，
	// 说明进程里已经截过一次了 —— 必须放行，否则「一个进程只能截一次」。
	errClassAlreadyExists = 1410

	// 框选阶段
	shotPhaseSelect  = 0 // 正在拖框
	shotPhaseToolbar = 1 // 已框好、操作条已弹出

	// 操作条按钮
	shotActCopy      = 1
	shotActExtract   = 2
	shotActTranslate = 3
	shotActCancel    = 4
	shotActAsk       = 5
	shotActSave      = 6

	shotBarH = 42 // 操作条高度

	// PatBlt 的 PATINVERT：dst = dst XOR 图案 —— 配 50% 棋盘格笔刷就是"选区外压暗"
	patInvert = 0x005A0049
)

var (
	shotUser32   = syscall.NewLazyDLL("user32.dll")
	shotGdi32    = syscall.NewLazyDLL("gdi32.dll")
	shotComdlg32 = syscall.NewLazyDLL("comdlg32.dll")

	pGetDC                  = shotUser32.NewProc("GetDC")
	pReleaseDC              = shotUser32.NewProc("ReleaseDC")
	pFrameRect              = shotUser32.NewProc("FrameRect")
	pDrawTextW              = shotUser32.NewProc("DrawTextW")
	pFillRect               = shotUser32.NewProc("FillRect")
	pGetSaveFileNameW       = shotComdlg32.NewProc("GetSaveFileNameW")
	pCreateCompatibleDC     = shotGdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = shotGdi32.NewProc("CreateCompatibleBitmap")
	pBitBlt                 = shotGdi32.NewProc("BitBlt")
	pGetDIBits              = shotGdi32.NewProc("GetDIBits")
	pDeleteDC               = shotGdi32.NewProc("DeleteDC")
	pPatBlt                 = shotGdi32.NewProc("PatBlt")
	pCreateBitmap           = shotGdi32.NewProc("CreateBitmap")
	pCreatePatternBrush     = shotGdi32.NewProc("CreatePatternBrush")
	pCreateFontW            = shotGdi32.NewProc("CreateFontW")
	pSetBkMode              = shotGdi32.NewProc("SetBkMode")
	pSetTextColor           = shotGdi32.NewProc("SetTextColor")
	pRoundRect              = shotGdi32.NewProc("RoundRect")

	shotWndProcCb = syscall.NewCallback(shotWndProc)

	shotRunMu   sync.Mutex
	shotRunning bool
)

// shotRect 屏幕坐标矩形（左上右下）
type shotRect struct{ L, T, R, B int32 }

// shotBarBtn 操作条上的一个按钮
type shotBarBtn struct {
	id    int
	label string
	w     int32
	rc    shotRect
}

// shotBarItems 操作条按钮（照豆包的：复制 / 提取文字 / 翻译 / 取消 / 问问豆包；另加「保存」）
var shotBarItems = []struct {
	id    int
	label string
	w     int32
}{
	{shotActCopy, "复制", 58},
	{shotActSave, "保存", 58},
	{shotActExtract, "提取文字", 84},
	{shotActTranslate, "翻译", 58},
	{shotActCancel, "取消", 58},
	{shotActAsk, "问问白泽", 84},
}

type shotWindow struct {
	hwnd             uintptr
	hdcMem           uintptr
	hbmp             uintptr
	oldBmp           uintptr
	originX, originY int32  // 虚拟屏左上角（窗口就贴在那儿）
	w, h             int    // 全屏位图尺寸
	bgra             []byte // 全屏 BGRA（top-down）

	selecting bool
	hasSel    bool
	selStart  ballPoint
	selEnd    ballPoint

	phase int      // shotPhaseSelect / shotPhaseToolbar
	hover int      // 悬停的按钮 id（0=没有）
	bar   shotRect // 操作条外框
	btns  []shotBarBtn

	lastSel shotRect // 上一次的选区（鼠标移动时只重画"旧框 ∪ 新框"这一小块，避免整屏重画）
}

var shotCur *shotWindow

// shotLast 最近一次截到的图（供剪贴板 / 网页接口/保存用）。
// ★只留在内存里，**不自动落盘** —— 只有用户点「保存」才写文件。
type shotResult struct {
	PNG    []byte
	W, H   int
	BGRA   []byte
	At     int64
	Action string // 这次截图随带要做的事："" | extract | translate | ask
}

var (
	shotMu   sync.Mutex
	shotLast *shotResult
)

// startShotCapture 截屏 + 框选（在独立线程上建窗跑消息循环）。失败只记日志。
// 一次只允许一个截图在进行中；跑完就放开，可以再截。
func startShotCapture() {
	shotRunMu.Lock()
	if shotRunning {
		shotRunMu.Unlock()
		log.Printf("[截图] 已经有一个截图在进行中")
		return
	}
	shotRunning = true
	shotRunMu.Unlock()

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() {
			shotRunMu.Lock()
			shotRunning = false
			shotRunMu.Unlock()
		}()
		if err := shotRun(); err != nil {
			log.Printf("[截图] 没起来：%v", err)
		}
	}()
}

// ShotLatest 返回最近一次截图（没有就 ok=false）
func ShotLatest() (shotResult, bool) {
	shotMu.Lock()
	defer shotMu.Unlock()
	if shotLast == nil {
		return shotResult{}, false
	}
	return *shotLast, true
}

func shotRun() error {
	sw, err := shotGrab()
	if err != nil {
		return err
	}
	shotCur = sw
	return sw.openOverlay()
}

// shotGrab 抓整个虚拟屏
func shotGrab() (*shotWindow, error) {
	vx, _, _ := pGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := pGetSystemMetrics.Call(smYVirtualScreen)
	vw, _, _ := pGetSystemMetrics.Call(smCxVirtualScreen)
	vh, _, _ := pGetSystemMetrics.Call(smCyVirtualScreen)
	if int(vw) <= 0 || int(vh) <= 0 {
		return nil, syscall.EINVAL
	}
	screen, _, _ := pGetDC.Call(0)
	if screen == 0 {
		return nil, syscall.EINVAL
	}
	defer pReleaseDC.Call(0, screen)

	mem, _, _ := pCreateCompatibleDC.Call(screen)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, vw, vh)
	if mem == 0 || bmp == 0 {
		return nil, syscall.EINVAL
	}
	old, _, _ := pSelectObject.Call(mem, bmp)
	// SRCCOPY = 0x00CC0020
	if r, _, _ := pBitBlt.Call(mem, 0, 0, vw, vh, screen, vx, vy, 0x00CC0020); r == 0 {
		return nil, syscall.EINVAL
	}
	// BITMAPINFOHEADER（biHeight 取负 = top-down）
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
	bih.Width = int32(vw)
	bih.Height = -int32(vh)
	bih.Planes = 1
	bih.BitCount = 32
	bih.Compression = 0 // BI_RGB
	bih.SizeImage = uint32(int(vw) * int(vh) * 4)
	buf := make([]byte, int(vw)*int(vh)*4)
	if r, _, _ := pGetDIBits.Call(mem, bmp, 0, vh,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bih)), 0); r == 0 {
		return nil, syscall.EINVAL
	}
	return &shotWindow{
		hdcMem: mem, hbmp: bmp, oldBmp: old,
		originX: int32(vx), originY: int32(vy), w: int(vw), h: int(vh), bgra: buf,
	}, nil
}

func (s *shotWindow) openOverlay() error {
	hInst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, 32514) // IDC_CROSS
	cls, _ := syscall.UTF16PtrFromString(shotOverlayClass)
	wc := ballWndClassEx{
		Style:         0x0001 | 0x0002,
		LpfnWndProc:   shotWndProcCb,
		HInstance:     hInst,
		HCursor:       cursor,
		LpszClassName: cls,
	}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if r, _, callErr := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		// 类已注册过（这个进程第二次截图）→ 不算错，接着用现成的类
		if e, ok := callErr.(syscall.Errno); !ok || uintptr(e) != errClassAlreadyExists {
			return callErr
		}
	}
	title, _ := syscall.UTF16PtrFromString("白泽截图")
	hwnd, _, callErr := pCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsPopup, uintptr(s.originX), uintptr(s.originY), uintptr(s.w), uintptr(s.h),
		0, 0, hInst, 0)
	if hwnd == 0 {
		return callErr
	}
	s.hwnd = hwnd
	pShowWindow.Call(hwnd, 5) // SW_SHOW
	pSetForegroundWindow.Call(hwnd)

	var m ballMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	s.release()
	shotCur = nil
	return nil
}

func (s *shotWindow) release() {
	if s.hdcMem != 0 {
		pSelectObject.Call(s.hdcMem, s.oldBmp)
		pDeleteDC.Call(s.hdcMem)
		s.hdcMem = 0
	}
	if s.hbmp != 0 {
		pDeleteObject.Call(s.hbmp)
		s.hbmp = 0
	}
}

func shotWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	s := shotCur
	switch uint32(msg) {
	case wmPaint:
		if s != nil {
			s.paint(hwnd)
		}
		return 0
	case wmLButtonDown:
		if s != nil {
			pt := shotPoint(lparam)
			// 已弹出操作条：先看是不是点在按钮上
			if s.phase == shotPhaseToolbar {
				if id := s.hitToolbar(pt); id != 0 {
					s.act(hwnd, id)
					return 0
				}
			}
			// 否则（重新）开始框选
			s.phase = shotPhaseSelect
			s.hover = 0
			s.selecting = true
			s.hasSel = false
			s.selStart = pt
			s.selEnd = pt
			pSetCapture.Call(hwnd)
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmMouseMove:
		if s == nil {
			return 0
		}
		if s.selecting {
			s.selEnd = shotPoint(lparam)
			s.hasSel = true
			s.invalidateSel(hwnd) // 只重画「旧框 ∪ 新框」那一圈：整屏位图不重画，框选才跟手
		} else if s.phase == shotPhaseToolbar {
			if h := s.hitToolbar(shotPoint(lparam)); h != s.hover {
				s.hover = h
				s.invalidateBar(hwnd) // 悬停变化只重画操作条
			}
		}
		return 0
	case wmLButtonUp:
		if s != nil && s.selecting {
			s.selecting = false
			pReleaseCapture.Call()
			s.selEnd = shotPoint(lparam)
			if s.selW() < 4 || s.selH() < 4 {
				// 太小当误触：当作全屏
				s.selStart = ballPoint{X: 0, Y: 0}
				s.selEnd = ballPoint{X: int32(s.w), Y: int32(s.h)}
			}
			// 框好了 → 原地弹出操作条（照豆包），不直接落盘
			s.phase = shotPhaseToolbar
			s.hover = 0
			s.layoutToolbar()
			pInvalidateRect.Call(hwnd, 0, 0) // 这一次整窗重画（要压暗 + 画操作条）
		}
		return 0
	case wmKeyDown:
		if uint32(wparam) == vkEscape {
			log.Printf("[截图] 已取消")
			pDestroyWindow.Call(hwnd)
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func shotPoint(lparam uintptr) ballPoint {
	return ballPoint{X: int32(int16(lparam & 0xffff)), Y: int32(int16((lparam >> 16) & 0xffff))}
}

func (s *shotWindow) selW() int { return int(abs32(s.selEnd.X - s.selStart.X)) }
func (s *shotWindow) selH() int { return int(abs32(s.selEnd.Y - s.selStart.Y)) }

// selRect 选区（已归一化：左<右、上<下）
func (s *shotWindow) selRect() shotRect {
	x0, y0 := s.selStart.X, s.selStart.Y
	x1, y1 := s.selEnd.X, s.selEnd.Y
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	return shotRect{L: x0, T: y0, R: x1, B: y1}
}

// layoutToolbar 把操作条摆在选区下方（下面放不下就摆上方），并算好每个按钮的位置
func (s *shotWindow) layoutToolbar() {
	sel := s.selRect()
	const pad, gap = int32(6), int32(2)
	var total int32 = pad
	for _, it := range shotBarItems {
		total += it.w + gap
	}
	total += pad - gap

	bx := sel.L
	if bx+total > int32(s.w) {
		bx = int32(s.w) - total
	}
	if bx < 0 {
		bx = 0
	}
	by := sel.B + 8
	if by+shotBarH > int32(s.h) {
		by = sel.T - shotBarH - 8 // 下方放不下 → 摆上方
	}
	if by < 0 {
		by = 0
	}
	s.bar = shotRect{L: bx, T: by, R: bx + total, B: by + shotBarH}

	s.btns = s.btns[:0]
	cx := bx + pad
	for _, it := range shotBarItems {
		s.btns = append(s.btns, shotBarBtn{
			id: it.id, label: it.label, w: it.w,
			rc: shotRect{L: cx, T: by, R: cx + it.w, B: by + shotBarH},
		})
		cx += it.w + gap
	}
}

func inRect(r shotRect, p ballPoint) bool {
	return p.X >= r.L && p.X < r.R && p.Y >= r.T && p.Y < r.B
}

// hitToolbar 返回点中的按钮 id（没点中返回 0）
func (s *shotWindow) hitToolbar(p ballPoint) int {
	if !inRect(s.bar, p) {
		return 0
	}
	for _, b := range s.btns {
		if inRect(b.rc, p) {
			return b.id
		}
	}
	return 0
}

/* ---------- 脏区重画（性能）---------- */

func inflate(r shotRect, d int32) shotRect { return shotRect{r.L - d, r.T - d, r.R + d, r.B + d} }
func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
func unionRect(a, b shotRect) shotRect {
	return shotRect{min32(a.L, b.L), min32(a.T, b.T), max32(a.R, b.R), max32(a.B, b.B)}
}
func intersects(a, b shotRect) bool {
	return a.L < b.R && b.L < a.R && a.T < b.B && b.T < a.B
}

// invalidateSel 只让「旧选框 ∪ 新选框」那一小圈重画（整屏位图不重画，框选才跟手）
func (s *shotWindow) invalidateSel(hwnd uintptr) {
	cur := s.selRect()
	u := unionRect(inflate(s.lastSel, 3), inflate(cur, 3))
	var rc struct{ L, T, R, B int32 }
	rc.L, rc.T, rc.R, rc.B = u.L, u.T, u.R, u.B
	pInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)), 0)
	s.lastSel = cur
}

// invalidateBar 悬停变化时只重画操作条那一块
func (s *shotWindow) invalidateBar(hwnd uintptr) {
	var rc struct{ L, T, R, B int32 }
	rc.L, rc.T, rc.R, rc.B = s.bar.L-2, s.bar.T-2, s.bar.R+2, s.bar.B+2
	pInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)), 0)
}

// act 点了操作条上的按钮
func (s *shotWindow) act(hwnd uintptr, id int) {
	switch id {
	case shotActCancel:
		log.Printf("[截图] 已取消（图直接丢掉，不留存）")
		pDestroyWindow.Call(hwnd)
	case shotActCopy:
		if err := s.finish(""); err != nil {
			log.Printf("[截图] 取样失败：%v", err)
		} else if w, h, err := copyShotToClipboard(); err != nil {
			log.Printf("[截图] 复制失败：%v", err)
		} else {
			log.Printf("[截图] 已复制 %dx%d", w, h)
		}
		pDestroyWindow.Call(hwnd)
	case shotActSave:
		if err := s.finish(""); err != nil {
			log.Printf("[截图] 取样失败：%v", err)
		} else if path, err := saveShotWithDialog(hwnd); err != nil {
			log.Printf("[截图] 保存失败：%v", err)
		} else if path != "" {
			log.Printf("[截图] 已保存 -> %s", path)
		}
		pDestroyWindow.Call(hwnd)
	case shotActExtract:
		if err := s.finish(""); err != nil {
			log.Printf("[截图] 取样失败：%v", err)
		} else if err := spawnFloatWithShot(floatModeExtract); err != nil {
			log.Printf("[截图] 起「提取文字」浮窗失败：%v", err)
		}
		pDestroyWindow.Call(hwnd)
	case shotActTranslate:
		if err := s.finish(""); err != nil {
			log.Printf("[截图] 取样失败：%v", err)
		} else if err := spawnFloatWithShot(floatModeTranslate); err != nil {
			log.Printf("[截图] 起「翻译」浮窗失败：%v", err)
		}
		pDestroyWindow.Call(hwnd)
	case shotActAsk:
		if err := s.finish(""); err != nil {
			log.Printf("[截图] 取样失败：%v", err)
		} else if err := spawnFloatWithShot(floatModeChat); err != nil {
			log.Printf("[截图] 起「问问白泽」浮窗失败：%v", err)
		}
		pDestroyWindow.Call(hwnd)
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// paint 只重画「脏区」（`GetDC` 抓来的整屏位图不重画）—— 框选/悬停才跟手
func (s *shotWindow) paint(hwnd uintptr) {
	var ps ballPaint
	pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	hdc := ps.Hdc
	r := ps.RcPaint
	px, py := r.Left, r.Top
	pw, ph := r.Right-r.Left, r.Bottom-r.Top
	if px < 0 {
		px = 0
	}
	if py < 0 {
		py = 0
	}
	if pw <= 0 || ph <= 0 {
		return
	}
	// 只把这一小块从内存位图拷过来（每帧全拷整屏会很卡）
	if s.hbmp != 0 {
		pBitBlt.Call(hdc, uintptr(px), uintptr(py), uintptr(pw), uintptr(ph),
			s.hdcMem, uintptr(px), uintptr(py), 0x00CC0020)
	}
	if !s.hasSel {
		return
	}
	sel := s.selRect()
	if s.phase == shotPhaseToolbar {
		dirty := shotRect{L: px, T: py, R: px + pw, B: py + ph}
		if intersects(dirty, sel) {
			s.dimOutside(hdc, sel) // 跨选区的重画：分四块压暗
		} else {
			patDim(hdc, dirty) // 小脏区（操作条那块，必在选区外）：直接压暗
		}
	}
	// 选区边框（GDI 会按更新区裁剪，只画脏区里那一段）
	br, _, _ := pCreateSolidBrush.Call(ballRGB(56, 189, 248))
	rc := struct{ L, T, R, B int32 }{sel.L, sel.T, sel.R, sel.B}
	pFrameRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
	pDeleteObject.Call(br)

	if s.phase == shotPhaseToolbar {
		s.paintToolbar(hdc)
	}
}

// shotChecker 8x8 黑白棋盘格（32bpp）；配 PatBlt(PATINVERT) 把像素 50% 反色 = 压暗一半
var shotChecker = func() []byte {
	b := make([]byte, 8*8*4)
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if (x+y)%2 == 0 {
				o := (y*8 + x) * 4
				b[o], b[o+1], b[o+2] = 0xFF, 0xFF, 0xFF
			}
		}
	}
	return b
}()

// patDim 用棋盘格对矩形做 PATINVERT（等效压暗一半）；空矩形直接跳过
func patDim(hdc uintptr, r shotRect) {
	if r.R <= r.L || r.B <= r.T {
		return
	}
	hbm, _, _ := pCreateBitmap.Call(8, 8, 1, 32, uintptr(unsafe.Pointer(&shotChecker[0])))
	if hbm == 0 {
		return
	}
	defer pDeleteObject.Call(hbm)
	pbr, _, _ := pCreatePatternBrush.Call(hbm)
	if pbr == 0 {
		return
	}
	defer pDeleteObject.Call(pbr)
	old, _, _ := pSelectObject.Call(hdc, pbr)
	pPatBlt.Call(hdc, uintptr(r.L), uintptr(r.T), uintptr(r.R-r.L), uintptr(r.B-r.T), patInvert)
	pSelectObject.Call(hdc, old)
}

// dimOutside 选区以外分四块压暗
func (s *shotWindow) dimOutside(hdc uintptr, sel shotRect) {
	patDim(hdc, shotRect{L: 0, T: 0, R: int32(s.w), B: sel.T})          // 上
	patDim(hdc, shotRect{L: 0, T: sel.B, R: int32(s.w), B: int32(s.h)}) // 下
	patDim(hdc, shotRect{L: 0, T: sel.T, R: sel.L, B: sel.B})           // 左
	patDim(hdc, shotRect{L: sel.R, T: sel.T, R: int32(s.w), B: sel.B})  // 右
}

// paintToolbar 画操作条：深色圆角底 + 按钮文字（悬停高亮）
func (s *shotWindow) paintToolbar(hdc uintptr) {
	bgb, _, _ := pCreateSolidBrush.Call(ballRGB(28, 33, 43))
	pen, _, _ := pCreatePen.Call(0, 1, ballRGB(58, 70, 86))
	ob, _, _ := pSelectObject.Call(hdc, bgb)
	op, _, _ := pSelectObject.Call(hdc, pen)
	pRoundRect.Call(hdc, uintptr(s.bar.L), uintptr(s.bar.T), uintptr(s.bar.R), uintptr(s.bar.B), 10, 10)
	pSelectObject.Call(hdc, ob)
	pSelectObject.Call(hdc, op)
	pDeleteObject.Call(bgb)
	pDeleteObject.Call(pen)

	if s.hover != 0 {
		for _, b := range s.btns {
			if b.id == s.hover {
				hb, _, _ := pCreateSolidBrush.Call(ballRGB(45, 54, 70))
				rc := struct{ L, T, R, B int32 }{b.rc.L + 2, b.rc.T + 4, b.rc.R - 2, b.rc.B - 4}
				pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), hb)
				pDeleteObject.Call(hb)
			}
		}
	}

	face, _ := syscall.UTF16FromString("Microsoft YaHei")
	ch := int32(-14) // CreateFontW 的 cHeight：负数 = 字符高度 14px
	font, _, _ := pCreateFontW.Call(
		uintptr(ch), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&face[0])))
	oldFont, _, _ := pSelectObject.Call(hdc, font)
	pSetBkMode.Call(hdc, 1) // TRANSPARENT
	pSetTextColor.Call(hdc, ballRGB(226, 234, 245))
	for _, b := range s.btns {
		txt, _ := syscall.UTF16FromString(b.label)
		rc := struct{ L, T, R, B int32 }{b.rc.L, b.rc.T, b.rc.R, b.rc.B}
		// DT_CENTER|DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX
		pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&txt[0])), uintptr(len(txt)-1),
			uintptr(unsafe.Pointer(&rc)), 0x1|0x4|0x20|0x800)
	}
	pSelectObject.Call(hdc, oldFont)
	pDeleteObject.Call(font)
}

// finish 裁剪选区 → 编码 PNG（只在内存）→ 记到 shotLast，供浮窗 / 保存 / 复制取用。
// ★不落盘；「取消」时什么都不留。
func (s *shotWindow) finish(action string) error {
	sel := s.selRect()
	x0, y0, x1, y1 := sel.L, sel.T, sel.R, sel.B
	cw, ch := int(x1-x0), int(y1-y0)
	if cw <= 0 || ch <= 0 {
		return syscall.EINVAL
	}
	crop := make([]byte, cw*ch*4)
	for y := 0; y < ch; y++ {
		srcOff := (int(y0)+y)*s.w*4 + int(x0)*4
		copy(crop[y*cw*4:], s.bgra[srcOff:srcOff+cw*4])
	}
	img := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for i := 0; i < cw*ch; i++ {
		b, g, r := crop[i*4], crop[i*4+1], crop[i*4+2]
		img.Pix[i*4] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	// ★不落盘：PNG 只放内存，只有用户点「保存」才写文件
	shotMu.Lock()
	shotLast = &shotResult{PNG: buf.Bytes(), W: cw, H: ch, BGRA: crop, At: time.Now().UnixMilli(), Action: action}
	shotMu.Unlock()
	log.Printf("[截图] 已取样 %dx%d（action=%q，只在内存、未落盘）", cw, ch, action)
	return nil
}

/*
---------------- 本机路由：给网页界面用（/api/local/shot/*） ----------------

	截屏/框选是原生能力（浏览器抓不了全屏），所以由原生侧开路；界面只负责画结果与做事。
	取图走 /latest（base64）与 /image（原图），复制走 /copy，触发走 /capture。
*/
func registerShotRoutes(mux *http.ServeMux) {
	// 触发一次截图：抓屏 + 全屏框选。异步执行、立即返回；框选结果由 /latest 取。
	mux.HandleFunc("/api/local/shot/capture", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 POST"})
			return
		}
		startShotCapture()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 最近一次截图：base64 PNG + 尺寸 + 时间戳。界面靠 at 变化判断"这次框选完了没"。
	mux.HandleFunc("/api/local/shot/latest", func(w http.ResponseWriter, r *http.Request) {
		shot, ok := ShotLatest()
		if !ok || len(shot.PNG) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "还没有截图"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "w": shot.W, "h": shot.H, "at": shot.At, "action": shot.Action,
			"sizeBytes": len(shot.PNG), "imageBase64": base64.StdEncoding.EncodeToString(shot.PNG),
		})
	})

	// 原图（给 <img src> 直接显示，省得把 base64 一遍遍塞进 DOM）。
	// 界面加 ?t=<at> 破缓存。图只在内存里，不落盘。
	mux.HandleFunc("/api/local/shot/image", func(w http.ResponseWriter, r *http.Request) {
		shot, ok := ShotLatest()
		if !ok || len(shot.PNG) == 0 {
			http.Error(w, "还没有截图", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(shot.PNG)
	})

	// 复制最近一次截图到系统剪贴板（CF_DIB）。
	mux.HandleFunc("/api/local/shot/copy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 POST"})
			return
		}
		cw, ch, err := copyShotToClipboard()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "w": cw, "h": ch})
	})

	// 保存最近一次截图：弹系统「另存为」由用户选位置（只有这一步才落盘）。
	mux.HandleFunc("/api/local/shot/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "只支持 POST"})
			return
		}
		path, err := saveShotWithDialog(mainHWND)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
	})
}

/* ---------------- 保存：系统「另存为」对话框 ---------------- */

// openFileNameW 是 Win32 的 OPENFILENAMEW（字段顺序/尺寸必须严格对齐）
type openFileNameW struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       uintptr
	LpstrCustomFilter uintptr
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         uintptr
	NMaxFile          uint32
	LpstrFileTitle    uintptr
	NMaxFileTitle     uint32
	LpstrInitialDir   uintptr
	LpstrTitle        uintptr
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       uintptr
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    uintptr
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

const (
	ofnOverwritePrompt = 0x00000002
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnExplorer        = 0x00080000
)

// utf16List 拼成对话框过滤器的格式：每段各自 NUL 结尾，整体再补一个 NUL
func utf16List(parts ...string) []uint16 {
	var out []uint16
	for _, p := range parts {
		u, _ := syscall.UTF16FromString(p) // 自带结尾 NUL
		out = append(out, u...)
	}
	out = append(out, 0)
	return out
}

// saveShotWithDialog 弹「另存为」让用户选位置，把内存里的截图写过去。用户取消时返回 ("", nil)。
func saveShotWithDialog(owner uintptr) (string, error) {
	shot, ok := ShotLatest()
	if !ok || len(shot.PNG) == 0 {
		return "", errors.New("还没有截图")
	}
	var fileBuf [1024]uint16
	if u, err := syscall.UTF16FromString("白泽截图-" + time.Now().Format("20060102-150405") + ".png"); err == nil {
		copy(fileBuf[:], u)
	}
	filter := utf16List("PNG 图片 (*.png)", "*.png", "所有文件 (*.*)", "*.*")
	defExt, _ := syscall.UTF16FromString("png")
	title, _ := syscall.UTF16FromString("保存截图")

	var ofn openFileNameW
	ofn.LStructSize = uint32(unsafe.Sizeof(ofn))
	ofn.HwndOwner = owner
	ofn.LpstrFilter = uintptr(unsafe.Pointer(&filter[0]))
	ofn.NFilterIndex = 1
	ofn.LpstrFile = uintptr(unsafe.Pointer(&fileBuf[0]))
	ofn.NMaxFile = uint32(len(fileBuf))
	ofn.LpstrDefExt = uintptr(unsafe.Pointer(&defExt[0]))
	ofn.LpstrTitle = uintptr(unsafe.Pointer(&title[0]))
	ofn.Flags = ofnOverwritePrompt | ofnPathMustExist | ofnNoChangeDir | ofnExplorer

	if r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return "", nil // 用户取消
	}
	path := syscall.UTF16ToString(fileBuf[:])
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, shot.PNG, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
