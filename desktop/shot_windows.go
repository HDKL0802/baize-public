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
	"image"
	"image/png"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
)

var (
	shotUser32 = syscall.NewLazyDLL("user32.dll")
	shotGdi32  = syscall.NewLazyDLL("gdi32.dll")

	pGetDC                  = shotUser32.NewProc("GetDC")
	pReleaseDC              = shotUser32.NewProc("ReleaseDC")
	pFrameRect              = shotUser32.NewProc("FrameRect")
	pCreateCompatibleDC     = shotGdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = shotGdi32.NewProc("CreateCompatibleBitmap")
	pBitBlt                 = shotGdi32.NewProc("BitBlt")
	pGetDIBits              = shotGdi32.NewProc("GetDIBits")
	pDeleteDC               = shotGdi32.NewProc("DeleteDC")

	shotWndProcCb = syscall.NewCallback(shotWndProc)

	shotRunMu   sync.Mutex
	shotRunning bool
)

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
}

var shotCur *shotWindow

// shotLast 最近一次截到的图（供剪贴板 / 网页接口用）
type shotResult struct {
	Path string
	W, H int
	BGRA []byte
	At   int64
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
		return callErr
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
			s.selecting = true
			s.hasSel = false
			s.selStart = shotPoint(lparam)
			s.selEnd = s.selStart
			pSetCapture.Call(hwnd)
		}
		return 0
	case wmMouseMove:
		if s != nil && s.selecting {
			s.selEnd = shotPoint(lparam)
			s.hasSel = true
			pInvalidateRect.Call(hwnd, 0, 0)
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
			_ = s.finish()
			pDestroyWindow.Call(hwnd)
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

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// paint 把抓到的全屏图画出来，再描一个选框
func (s *shotWindow) paint(hwnd uintptr) {
	var ps ballPaint
	pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	hdc := ps.Hdc
	if s.hbmp != 0 {
		// 把内存位图贴到窗口 DC 上（SRCCOPY）
		pBitBlt.Call(hdc, 0, 0, uintptr(s.w), uintptr(s.h), s.hdcMem, 0, 0, 0x00CC0020)
	}
	if s.hasSel {
		x0, y0 := s.selStart.X, s.selStart.Y
		x1, y1 := s.selEnd.X, s.selEnd.Y
		if x1 < x0 {
			x0, x1 = x1, x0
		}
		if y1 < y0 {
			y0, y1 = y1, y0
		}
		br, _, _ := pCreateSolidBrush.Call(ballRGB(56, 189, 248))
		var rc struct{ L, T, R, B int32 }
		rc.L, rc.T, rc.R, rc.B = x0, y0, x1, y1
		pFrameRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), br)
		pDeleteObject.Call(br)
	}
}

// finish 裁剪选区 → 编码 PNG → 落盘 → 打开主窗口的 #shot
func (s *shotWindow) finish() error {
	x0, y0 := s.selStart.X, s.selStart.Y
	x1, y1 := s.selEnd.X, s.selEnd.Y
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
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
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	latest := filepath.Join(dir, "shot-latest.png")
	if err := os.WriteFile(latest, buf.Bytes(), 0o600); err != nil {
		return err
	}
	stamped := filepath.Join(dir, "shot-"+time.Now().Format("20060102-150405")+".png")
	_ = os.WriteFile(stamped, buf.Bytes(), 0o600)

	shotMu.Lock()
	shotLast = &shotResult{Path: latest, W: cw, H: ch, BGRA: crop, At: time.Now().UnixMilli()}
	shotMu.Unlock()
	log.Printf("[截图] 已截图 %dx%d -> %s", cw, ch, latest)

	// 打开主窗口并跳到「截图提问」
	if mainHWND != 0 {
		pShowWindow.Call(mainHWND, swRestore)
		pSetForegroundWindow.Call(mainHWND)
	}
	if mainWebView != nil {
		mainWebView.Dispatch(func() {
			mainWebView.Eval(`location.hash = '#shot'`)
		})
	}
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
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "还没有截图"})
			return
		}
		b, err := os.ReadFile(shot.Path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "读截图失败：" + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "w": shot.W, "h": shot.H, "at": shot.At,
			"sizeBytes": len(b), "imageBase64": base64.StdEncoding.EncodeToString(b),
		})
	})

	// 原图（给 <img src> 直接显示，省得把 base64 一遍遍塞进 DOM）。
	// 界面加 ?t=<at> 破缓存。
	mux.HandleFunc("/api/local/shot/image", func(w http.ResponseWriter, r *http.Request) {
		shot, ok := ShotLatest()
		if !ok {
			http.Error(w, "还没有截图", http.StatusNotFound)
			return
		}
		b, err := os.ReadFile(shot.Path)
		if err != nil {
			http.Error(w, "读截图失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
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
}
