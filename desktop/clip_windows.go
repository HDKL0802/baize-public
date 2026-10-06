//go:build windows

// 把最近一次截图放到系统剪贴板（CF_DIB）。纯 Win32 syscall。
//
// CF_DIB 要求：BITMAPINFOHEADER(40) + 像素数据。这里用 32bpp、**bottom-up**（biHeight 为正），
// 所以拷的时候要逐行倒序 —— 系统拿到的是「图像在这块全局内存里」，我们之后不能再释放它。
//
// 拷贝走 RtlMoveMemory 而不是 uintptr→unsafe.Pointer：后者 go vet 会报 unsafeptr。
package main

import (
	"encoding/binary"
	"errors"
	"log"
	"syscall"
	"unsafe"
)

const (
	cfDIB        = 8
	gmemMoveable = 0x0002
)

var (
	clipUser32   = syscall.NewLazyDLL("user32.dll")
	clipKernel32 = syscall.NewLazyDLL("kernel32.dll")

	pOpenClipboard    = clipUser32.NewProc("OpenClipboard")
	pEmptyClipboard   = clipUser32.NewProc("EmptyClipboard")
	pCloseClipboard   = clipUser32.NewProc("CloseClipboard")
	pSetClipboardData = clipUser32.NewProc("SetClipboardData")
	pGlobalAlloc      = clipKernel32.NewProc("GlobalAlloc")
	pGlobalFree       = clipKernel32.NewProc("GlobalFree")
	pGlobalLock       = clipKernel32.NewProc("GlobalLock")
	pGlobalUnlock     = clipKernel32.NewProc("GlobalUnlock")
	pRtlMoveMemory    = clipKernel32.NewProc("RtlMoveMemory")
)

// copyShotToClipboard 把最近一次截图（BGRA, top-down）以 CF_DIB 放进剪贴板。
func copyShotToClipboard() (int, int, error) {
	shot, ok := ShotLatest()
	if !ok || len(shot.BGRA) == 0 {
		return 0, 0, errors.New("还没有截图")
	}
	w, h := shot.W, shot.H
	if w <= 0 || h <= 0 {
		return 0, 0, errors.New("截图尺寸不对")
	}

	// 先在 Go 侧把整块 DIB 拼好：头 + 像素（bottom-up，行序倒过来）
	const hdr = 40
	buf := make([]byte, hdr+w*h*4)
	binary.LittleEndian.PutUint32(buf[0:], 40)             // biSize
	binary.LittleEndian.PutUint32(buf[4:], uint32(w))      // biWidth
	binary.LittleEndian.PutUint32(buf[8:], uint32(h))      // biHeight（正 = bottom-up）
	binary.LittleEndian.PutUint16(buf[12:], 1)             // biPlanes
	binary.LittleEndian.PutUint16(buf[14:], 32)            // biBitCount
	binary.LittleEndian.PutUint32(buf[16:], 0)             // biCompression = BI_RGB
	binary.LittleEndian.PutUint32(buf[20:], uint32(w*h*4)) // biSizeImage
	for y := 0; y < h; y++ {
		srcRow := (h - 1 - y) * w * 4
		dstRow := hdr + y*w*4
		copy(buf[dstRow:dstRow+w*4], shot.BGRA[srcRow:srcRow+w*4])
	}

	if r, _, _ := pOpenClipboard.Call(0); r == 0 {
		return 0, 0, errors.New("打不开剪贴板（可能被别的程序占着）")
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()

	hmem, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(buf)))
	if hmem == 0 {
		return 0, 0, errors.New("剪贴板内存分配失败")
	}
	ptr, _, _ := pGlobalLock.Call(hmem)
	if ptr == 0 {
		pGlobalFree.Call(hmem)
		return 0, 0, errors.New("剪贴板内存锁定失败")
	}
	// 把 Go 侧那块字节拷进全局内存。这里是把**真实指针**转成 uintptr 传参（合法方向），
	// 不是 uintptr→unsafe.Pointer，所以 go vet 的 unsafeptr 不报。
	pRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	pGlobalUnlock.Call(hmem)

	if r, _, _ := pSetClipboardData.Call(cfDIB, hmem); r == 0 {
		pGlobalFree.Call(hmem) // 失败才由我们释放；成功则所有权归系统
		return 0, 0, errors.New("写入剪贴板失败")
	}
	log.Printf("[截图] 已复制到剪贴板 %dx%d", w, h)
	return w, h, nil
}
