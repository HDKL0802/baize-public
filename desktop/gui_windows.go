//go:build windows

// 电脑控制 · 第 3 档：让白泽控制本机 GUI（截图 / 点击 / 输入 / 按键）。
//
// 高危能力，三重闸门叠加，缺一不可（与第 2 档「允许执行命令」同一纪律）：
//  1. **本机开关**：必须在「设置 → 桌面控制 → 允许控制 GUI」显式打开（默认关，要过风险确认）；
//  2. **目标窗口白名单**：点击/输入/按键的"目标窗口所属进程"必须在清单里，
//     **空清单 = 一个窗都不许碰**（只剩截图这种只读动作可用——控制前先截图确认目标，
//     是这套设计的一部分，不是漏网）；
//  3. **后端人工审批**：`gui.*` 全部在危险动作前缀里，**每次**派下来都要点「批准并执行」。
//
// 判定全部发生在**本机**（permGate 过开关、guiGate 过白名单），前端/后端改不掉。
//
// 实现全部走原生 Win32 syscall（不用 cgo）：
//   - 点击：SetCursorPos + mouse_event
//   - 按键：keybd_event（组合键按 按下顺序 down、倒序 up）
//   - 输入：SendInput + KEYEVENTF_UNICODE（按 UTF-16 码元逐位发，中文/emoji 都能进）
//   - 截图：GetWindowDC(桌面) + BitBlt + GetDIBits → 写本地临时 PNG，回执只带路径
//
// DPI 说明：进程没有声明 DPI 感知，所有坐标都是系统 DPI 下的"虚拟坐标"——
// 鼠标、窗口矩形、屏幕尺寸三者口径一致，互相不串。截图与点击用的是同一套坐标，
// 所以「截图 → 看坐标 → 点击」的闭环是准的（高分屏上截到的图会被缩放，但坐标对得上）。
package main

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"baize/shared/proto"
)

// 复用同包里 shot_windows.go / ball_windows.go 已有的 GDI/user32 proc 声明
// （pGetSystemMetrics、pReleaseDC、pCreateCompatibleDC、pBitBlt、pGetDIBits、
// pCreateDIBSection、pSelectObject、pDeleteObject、pDeleteDC、smX/YVirtualScreen…），
// 这里只补 GUI 控制独有的：
var imm32 = syscall.NewLazyDLL("imm32.dll")

var (
	pSetCursorPos         = u32.NewProc("SetCursorPos")
	pMouseEvent           = u32.NewProc("mouse_event")
	pKeybdEvent           = u32.NewProc("keybd_event")
	pSendInput            = u32.NewProc("SendInput")
	pGetWindowDC          = u32.NewProc("GetWindowDC")
	pWindowFromPoint      = u32.NewProc("WindowFromPoint")
	pGetAncestor          = u32.NewProc("GetAncestor")
	pImmSetConversionStat = imm32.NewProc("ImmSetConversionStatus")
)

// SendInput 的 INPUT 结构（Windows 布局：type(4) + 对齐(4) + union（含 64 位对齐到 8））。
// cbSize 用 unsafe.Sizeof 取，32/64 位自动各归其位。
type mouseInputEvent struct {
	Dx, Dy, MouseData, Flags uint32
	Time                     uint32
	Extra                    uintptr
}

type keyInputEvent struct {
	WVk, Scan   uint16
	Flags, Time uint32
	Extra       uintptr
}

type hardwareInputEvent struct {
	Msg            uint32
	ParamL, ParamH uint16
}

type inputUnion struct {
	Mi mouseInputEvent
	Ki keyInputEvent
	Hi hardwareInputEvent
}

type inputStruct struct {
	Type uint32
	_    uint32 // Windows 会在这里塞 4 字节对齐
	U    inputUnion
}

const (
	inputKeyboard         = 1
	keyEventFExtendedKey  = 0x0001
	keyEventFKeyUp        = 0x0002
	keyEventFUnicode      = 0x0004
	mouseEventFLeftDown   = 0x0002
	mouseEventFLeftUp     = 0x0004
	mouseEventFRightDown  = 0x0008
	mouseEventFRightUp    = 0x0010
	mouseEventFMiddleDown = 0x0020
	mouseEventFMiddleUp   = 0x0040

	guiInputMaxChars = 500 // 单次输入上限：防"一口气把 10 万字符打进记事本"
)

/* ---------------- 闸门（设备侧第二道：目标窗口白名单） ---------------- */

// guiGate 逐动作校验：开关（permGate 已过）+ 目标窗口进程白名单。
//
// 关键设计（防"点错地方"）：
//   - 截图：开关过了就放行（只读；审批在后端）；
//   - 点击：**在移动鼠标之前**先用 WindowFromPoint 算出坐标落点的窗口进程，
//     命中白名单才真正动手——目标变了/落点在白名单外的窗上，就地拒绝；
//   - 输入/按键：目标是**当前前台窗口**，认不出前台（锁屏等）一律拒绝，不盲发。
func guiGate(action, targetApp string) error {
	// 开关在 permGate 已经判过一次，这里再判一次（纵深防御：任何调用路径都过不了）
	if !guiControlEnabled() {
		return errors.New("本机没开「允许控制 GUI」（设置 → 桌面控制）；这是高危能力，默认关闭")
	}
	switch action {
	case proto.ActionGuiScreenshot:
		return nil
	}
	allow := guiAllowApps()
	if len(allow) == 0 {
		return errors.New("本机 GUI 白名单是空的（一个窗都不许碰）；要允许控制请在「设置 → 桌面控制 → 允许控制 GUI」里写应用名")
	}
	if targetApp == "" {
		return errors.New("认不出目标窗口（没有前台窗口或坐标落点为空，可能锁屏）；GUI 控制需要明确目标，已拒绝盲发")
	}
	tn := normalizeAppName(targetApp)
	for _, a := range allow {
		if normalizeAppName(a) == tn {
			return nil
		}
	}
	return fmt.Errorf("目标窗口进程 %q 不在本机 GUI 白名单（当前允许：%s）；要放开请到「设置 → 桌面控制 → 允许控制 GUI」里加",
		targetApp, strings.Join(allow, "、"))
}

// guiTargetApp 这个动作要"碰"哪个应用（归一前的原始进程名；认不出 = 空串，交给闸门拒）。
func guiTargetApp(action string, args map[string]any) string {
	switch action {
	case proto.ActionGuiClick:
		x, _ := argInt(args, "x")
		y, _ := argInt(args, "y")
		return windowAppAt(x, y)
	case proto.ActionGuiInput, proto.ActionGuiKey:
		if w, err := activeWindow(); err == nil {
			if p, _ := w["process"].(string); p != "" {
				return p
			}
		}
	}
	return ""
}

// windowAppAt 坐标落点处的顶层窗口属于哪个进程（认不出 = ""）。
func windowAppAt(x, y int) string {
	h, _, _ := pWindowFromPoint.Call(uintptr(int32(x)), uintptr(int32(y)))
	if h == 0 {
		return ""
	}
	if r, _, _ := pGetAncestor.Call(h, 2 /* GA_ROOT */); r != 0 {
		h = r
	}
	var pid uint32
	pGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	return processName(pid)
}

/* ---------------- gui.screenshot：截屏存本地 PNG ---------------- */

// BITMAPINFO 的最小头（GetDIBits 只需要 BiSize 起的前 40 字节）
type dibInfo struct {
	BiSize         uint32
	BiWidth        int32
	BiHeight       int32
	BiPlanes       uint16
	BiBitCount     uint16
	BiCompression  uint32
	BiSizeImage    uint32
	BiXPelsPerM    int32
	BiYPelsPerM    int32
	BiClrUsed      uint32
	BiClrImportant uint32
}

// guiScreenshot 截屏（缺省 = 全部虚拟屏幕），写临时 PNG，回执只带路径与尺寸。
func guiScreenshot(args map[string]any) map[string]any {
	sm := func(id int) int {
		v, _, _ := pGetSystemMetrics.Call(uintptr(id))
		return int(v)
	}
	vx, vy, vw, vh := sm(smXVirtualScreen), sm(smYVirtualScreen), sm(smCxVirtualScreen), sm(smCyVirtualScreen)
	x, hasX := argInt(args, "x")
	y, hasY := argInt(args, "y")
	w, _ := argInt(args, "width")
	h, _ := argInt(args, "height")
	// 缺省 = 整个虚拟屏幕；给了就夹在虚拟屏幕范围内（越界截不了，如实说）
	if !hasX {
		x = vx
	}
	if !hasY {
		y = vy
	}
	if x < vx {
		x = vx
	}
	if y < vy {
		y = vy
	}
	if w <= 0 {
		w = vw - (x - vx)
	}
	if h <= 0 {
		h = vh - (y - vy)
	}
	if w <= 0 || h <= 0 {
		return map[string]any{"ok": false, "error": fmt.Sprintf("截屏区域无效（x=%d y=%d width=%d height=%d；虚拟屏幕 %d,%d 起 %dx%d）",
			x, y, w, h, vx, vy, vw, vh)}
	}

	hScreen, _, _ := pGetWindowDC.Call(0) // 桌面窗口 DC = 全屏
	dstDC, _, _ := pCreateCompatibleDC.Call(0)
	var bmpHandle uintptr
	{
		// 32 位 DIB（top-down 用负高度），像素直接读出来
		bmi := dibInfo{BiSize: 40, BiWidth: int32(w), BiHeight: int32(-h), BiPlanes: 1, BiBitCount: 32}
		bmpHandle, _, _ = pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bmi)), 0, 0, 0)
	}
	if hScreen == 0 || dstDC == 0 || bmpHandle == 0 {
		if dstDC != 0 {
			pDeleteDC.Call(dstDC)
		}
		if hScreen != 0 {
			pReleaseDC.Call(0, hScreen)
		}
		return map[string]any{"ok": false, "error": "截屏失败：拿不到位图句柄（GDI 资源不足？）"}
	}
	defer pDeleteObject.Call(bmpHandle)
	defer pDeleteDC.Call(dstDC)
	defer pReleaseDC.Call(0, hScreen)

	sel, _, _ := pSelectObject.Call(dstDC, bmpHandle)
	defer pSelectObject.Call(dstDC, sel)
	// SRCCOPY | CAPTUREBLT：把分层窗口（如置顶的小浮窗）也一起截进来
	r, _, _ := pBitBlt.Call(dstDC, 0, 0, uintptr(w), uintptr(h),
		hScreen, uintptr(int32(x-vx)), uintptr(int32(y-vy)), 0x00CC0020|0x40000000)
	if r == 0 {
		return map[string]any{"ok": false, "error": "截屏失败：BitBlt 返回 0"}
	}

	// 读像素（32 位 BGRA，top-down）
	bpp := w * 4
	buf := make([]byte, int64(bpp)*int64(h))
	getBmi := dibInfo{BiSize: 40, BiWidth: int32(w), BiHeight: int32(h), BiPlanes: 1, BiBitCount: 32}
	n, _, _ := pGetDIBits.Call(hScreen, bmpHandle, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&getBmi)))
	if int(n) != h {
		return map[string]any{"ok": false, "error": fmt.Sprintf("截屏失败：读回 %d/%d 行像素", n, h)}
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b, g, rr, a := buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3]
		img.Pix[i*4] = rr
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = a
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("baize-gui-shot-%d.png", time.Now().UnixMilli()))
	f, err := os.Create(path)
	if err == nil {
		err = png.Encode(f, img)
		_ = f.Close()
	}
	if err != nil {
		return map[string]any{"ok": false, "error": "截屏失败：写 PNG " + err.Error()}
	}
	fi, _ := os.Stat(path)
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	return map[string]any{
		"ok": true, "path": path, "x": x - vx, "y": y - vy,
		"width": w, "height": h, "bytes": size,
		"at":   time.Now().UnixMilli(),
		"note": "PNG 存在本机临时目录；要看内容用 fs.stat/读文件类动作或在本机直接打开该路径",
	}
}

/* ---------------- gui.click：移动 + 点按 ---------------- */

// guiClick 在 (x,y) 点鼠标。button：left（默认）/ right / middle。
// 注意：白名单校验已在 guiGate 里用 WindowFromPoint 做过（动手前），这里只管执行。
func guiClick(args map[string]any) map[string]any {
	x, _ := argInt(args, "x")
	y, _ := argInt(args, "y")
	button := strings.ToLower(strings.TrimSpace(fmt.Sprint(args["button"])))
	if button == "" {
		button = "left"
	}
	var down, up uintptr
	switch button {
	case "left":
		down, up = mouseEventFLeftDown, mouseEventFLeftUp
	case "right":
		down, up = mouseEventFRightDown, mouseEventFRightUp
	case "middle":
		down, up = mouseEventFMiddleDown, mouseEventFMiddleUp
	default:
		return map[string]any{"ok": false, "error": fmt.Sprintf("不认识的鼠标键 %q（支持 left/right/middle）", button)}
	}
	if ok, _, _ := pSetCursorPos.Call(uintptr(int32(x)), uintptr(int32(y))); ok == 0 {
		return map[string]any{"ok": false, "error": "移动鼠标失败（SetCursorPos 返回 0）"}
	}
	time.Sleep(50 * time.Millisecond) // 给目标应用一点点时间接受新位置
	pMouseEvent.Call(down, 0, 0, 0, 0)
	time.Sleep(60 * time.Millisecond) // 短按下：快闪一下某些按钮会吞掉
	pMouseEvent.Call(up, 0, 0, 0, 0)
	return map[string]any{
		"ok": true, "x": x, "y": y, "button": button,
		"at": time.Now().UnixMilli(),
	}
}

/* ---------------- gui.input：逐字输入（UTF-16，中文可） ---------------- */

// guiInput 向前台窗口输入文字。SendInput + KEYEVENTF_UNICODE：
// 每个 UTF-16 码元一次"按下+抬起"，中文/emoji 直接进，不依赖键盘布局。
// 先把输入法切到关闭转换（IMC_OFF，best-effort）——不然打中文会弹出候选窗串味。
func guiInput(args map[string]any) map[string]any {
	raw, hasText := args["text"]
	text := strings.TrimSpace(fmt.Sprint(raw))
	if !hasText || text == "" {
		return map[string]any{"ok": false, "error": "缺少参数 text（要输入的文字）"}
	}
	if len([]rune(text)) > guiInputMaxChars {
		return map[string]any{"ok": false, "error": fmt.Sprintf("文字太长（%d 字）：单次最多 %d 字，请拆开多次发", len([]rune(text)), guiInputMaxChars)}
	}
	// IME 归位（失败不影响：多数场景前台没有挂 IME，ImmSetConversionStatus 返回 0）
	if pImmSetConversionStat.Find() == nil {
		pImmSetConversionStat.Call(0, 1 /* IMC_OFF */)
	}

	units := utf16Encode(text) // UTF-16 码元（代理对拆开）
	events := make([]inputStruct, 0, len(units)*2)
	for _, u := range units {
		events = append(events,
			inputStruct{Type: inputKeyboard, U: inputUnion{Ki: keyInputEvent{WVk: uint16(u), Flags: keyEventFUnicode}}},
			inputStruct{Type: inputKeyboard, U: inputUnion{Ki: keyInputEvent{WVk: uint16(u), Flags: keyEventFUnicode | keyEventFKeyUp}}},
		)
	}
	sent, err := sendInputs(events)
	if err != nil {
		return map[string]any{"ok": false, "error": "发送按键失败：" + err.Error(), "chars": len(units)}
	}
	return map[string]any{
		"ok": true, "chars": len(units), "sent": sent,
		"note": "输入发给了当时的前台窗口；若其间前台变了，内容可能落到别的窗上（这就是先截图确认目标的意义）",
		"at":   time.Now().UnixMilli(),
	}
}

// sendInputs 一次 SendInput 发整批（比逐条发稳，也保证原子顺序）。
func sendInputs(events []inputStruct) (uint32, error) {
	if len(events) == 0 {
		return 0, nil
	}
	// 必须传指针到**第一个**元素：SendInput 按 cbSize 连续读 n 个 INPUT
	var first *inputStruct
	if len(events) > 0 {
		first = &events[0]
	}
	n, _, syscallErr := pSendInput.Call(uintptr(len(events)),
		uintptr(unsafe.Pointer(first)),
		uintptr(int32(unsafe.Sizeof(inputStruct{}))))
	if n == 0 && syscallErr != nil {
		return 0, fmt.Errorf("SendInput 失败（%v）", syscallErr)
	}
	return uint32(n), nil
}

/* ---------------- gui.key：按键（含组合键） ---------------- */

type keySpec struct {
	vk  uint16
	ext bool // 需要 KEYEVENTF_EXTENDEDKEY（方向键/功能键/小键盘类）
}

// 具名键 → 虚拟键码（未列出的单字符按 a-z / 0-9 / f1-f24 自动推）
var namedKeys = map[string]keySpec{
	"enter": {0x0D, false}, "esc": {0x1B, false}, "escape": {0x1B, false},
	"tab": {0x09, false}, "backspace": {0x08, false}, "space": {0x20, false},
	"delete": {0x2E, true}, "insert": {0x2D, true},
	"home": {0x24, true}, "end": {0x23, true},
	"pageup": {0x21, true}, "pagedown": {0x22, true},
	"up": {0x28, true}, "down": {0x26, true}, "left": {0x25, true}, "right": {0x27, true},
	"ctrl": {0x11, false}, "alt": {0x12, false}, "shift": {0x10, false}, "win": {0x5B, true},
}

// parseKeySpec 一个键名（"s" / "enter" / "ctrl" / "f5"）→ 虚拟键码；认不出返回错。
func parseKeySpec(name string) (keySpec, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if s, ok := namedKeys[name]; ok {
		return s, nil
	}
	if strings.HasPrefix(name, "f") {
		n := 0
		for _, c := range name[1:] {
			if c < '1' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 24 {
			return keySpec{vk: uint16(0x6F + n), ext: true}, nil
		}
	}
	if len(name) == 1 {
		if c := name[0]; c >= 'a' && c <= 'z' {
			return keySpec{vk: uint16(0x40 + c - 'a' + 1)}, nil
		}
		if c := name[0]; c >= '0' && c <= '9' {
			return keySpec{vk: uint16(0x30 + c - '0')}, nil
		}
	}
	return keySpec{}, fmt.Errorf("认不出按键 %q（支持：单字符 a-z/0-9、enter/esc/tab/backspace/space/delete/insert/home/end/pageup/pagedown/up/down/left/right、f1-f24、ctrl/alt/shift/win）", name)
}

// parseKeyCombo "ctrl+shift+s" → 按键顺序（按下从左到右，抬起倒过来）；任一环节认不出就整组失败。
func parseKeyCombo(combo string) ([]keySpec, error) {
	parts := strings.Split(combo, "+")
	specs := make([]keySpec, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("组合键 %q 里有空片段", combo)
		}
		s, err := parseKeySpec(p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, nil
}

// guiKey 按一个（组）键。keys 参数可逗号串多组："ctrl+s, enter"。
func guiKey(args map[string]any) map[string]any {
	kv, hasKeys := args["keys"]
	raw := strings.TrimSpace(fmt.Sprint(kv))
	if !hasKeys || raw == "" {
		return map[string]any{"ok": false, "error": "缺少参数 keys（要按的键，如 enter / ctrl+s / f5；多组用逗号串）"}
	}
	pressed := 0
	var notes []string
	for _, combo := range strings.Split(raw, ",") {
		if strings.TrimSpace(combo) == "" {
			continue
		}
		specs, err := parseKeyCombo(combo)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error(), "pressedSoFar": pressed}
		}
		pressEvents := make([]keySpec, 0, len(specs))
		copy(pressEvents, specs)
		for _, s := range pressEvents { // 按下：顺序 down
			pressKey(s)
			pressed++
		}
		for i := len(specs) - 1; i >= 0; i-- { // 抬起：倒序 up
			pressKeyUp(specs[i])
		}
		notes = append(notes, strings.TrimSpace(combo))
		time.Sleep(40 * time.Millisecond)
	}
	return map[string]any{
		"ok": true, "keys": raw, "events": pressed, "combos": notes,
		"at": time.Now().UnixMilli(),
	}
}

// pressKey / pressKeyUp 发一次 keybd_event（扩展键要带 EXTENDEDKEY 标志）
func pressKey(s keySpec) {
	flags := uintptr(0)
	if s.ext {
		flags = keyEventFExtendedKey
	}
	pKeybdEvent.Call(uintptr(uint8(s.vk)), 0, flags, 0)
}

func pressKeyUp(s keySpec) {
	flags := uintptr(keyEventFKeyUp)
	if s.ext {
		flags |= keyEventFExtendedKey
	}
	pKeybdEvent.Call(uintptr(uint8(s.vk)), 0, flags, 0)
}

/* ---------------- 参数小工具 ---------------- */

// argInt 从动作参数里取整数（JSON 数字反序列化是 float64）；没有 = 0
func argInt(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// utf16Encode 字符串 → UTF-16 码元序列（代理对拆开成 2 个 uint16，SendInput 只认 16 位）
func utf16Encode(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		hi, lo := surrogateFromRune(r)
		out = append(out, hi, lo)
	}
	return out
}

// surrogateFromRune 基础平面内是 (0, r)；超出 BMP 拆成高低代理（SendInput 按码元逐个发）
func surrogateFromRune(r rune) (uint16, uint16) {
	if r < 0x10000 {
		return 0, uint16(r)
	}
	r -= 0x10000
	hi := uint16(0xD800 + r>>10&0x3FF)
	lo := uint16(0xDC00 + r&0x3FF)
	return hi, lo
}

// guiActions 第 3 档的四个动作（runAction 里判断用）
func guiActions() []string {
	return []string{
		proto.ActionGuiScreenshot, proto.ActionGuiClick,
		proto.ActionGuiInput, proto.ActionGuiKey,
	}
}
