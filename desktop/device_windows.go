//go:build windows

// 本机设备能力：桌面端自己也注册成「设备」，可以被 NAS 派活。
//
// 复用共享契约 baize/shared（与手机内核、agent/cmd/desktop 同一份协议），
// 但**只实现只读动作**：ping / sys.info / window.now / fs.stat。
// 故意不声明 fs.delete —— 删除类动作要护栏 + 审批，本端不做，宁可如实拒绝。
package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"baize/shared/client"
	"baize/shared/proto"
)

const desktopAppVersion = "0.5.23"

/* ---------------- 本机信息（与 agent/internal/sysinfo 同一口径） ---------------- */

func hostname() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "unknown-host"
	}
	return h
}

func username() string {
	if cu, err := user.Current(); err == nil {
		return cu.Username
	}
	return ""
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.' || r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "device"
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

func toHex(v uint32) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 0, 8)
	for i := 7; i >= 0; i-- {
		b = append(b, digits[(v>>(uint(i)*4))&0xf])
	}
	return string(b)
}

// deviceID 与 agent 的 sysinfo.DeviceID() 完全一致 → 设备表里还是同一条「这台电脑」。
func deviceID() string {
	raw := strings.ToLower(hostname() + "|" + username() + "|" + runtime.GOOS)
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	return sanitize(hostname()) + "-" + toHex(h.Sum32())
}

// caps 本机能力标记：**完全由桌面控制权限等级决定**（权限不够的能力根本不上报，
// 后端也就不会派这类活过来）。等级说明见 perm.go。
func caps() []string { return permCaps() }

func localInfo() map[string]any {
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	return map[string]any{
		"os": runtime.GOOS, "arch": runtime.GOARCH,
		"hostname": hostname(), "user": username(),
		"cwd": wd, "exe": exe, "pid": os.Getpid(),
		"goVersion": runtime.Version(), "at": time.Now().UnixMilli(),
	}
}

/* ---------------- 活动窗口（Win32） ---------------- */

var (
	u32 = syscall.NewLazyDLL("user32.dll")
	k32 = syscall.NewLazyDLL("kernel32.dll")

	pGetForegroundWindow        = u32.NewProc("GetForegroundWindow")
	pGetWindowTextW             = u32.NewProc("GetWindowTextW")
	pGetWindowThreadProcessID   = u32.NewProc("GetWindowThreadProcessId")
	pOpenProcess                = k32.NewProc("OpenProcess")
	pQueryFullProcessImageNameW = k32.NewProc("QueryFullProcessImageNameW")
	pCloseHandle                = k32.NewProc("CloseHandle")
)

const (
	processQueryLimitedInformation = 0x1000
	maxWindowTitle                 = 512
)

func activeWindow() (map[string]any, error) {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return nil, errors.New("没有前台窗口（可能已锁屏或当前会话不可见）")
	}
	buf := make([]uint16, maxWindowTitle)
	n, _, _ := pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title := strings.TrimSpace(syscall.UTF16ToString(buf[:int(n)]))

	var pid uint32
	pGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

	return map[string]any{
		"title": title, "pid": pid, "process": processName(pid),
		"at": time.Now().UnixMilli(),
	}, nil
}

func processName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, _, _ := pOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf))
	r, _, _ := pQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}

/* ---------------- 动作执行（只读） ---------------- */

func statPaths(paths []string) map[string]any {
	items := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		item := map[string]any{"path": p, "exists": false}
		info, err := os.Lstat(p)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				item["error"] = err.Error()
			}
			items = append(items, item)
			continue
		}
		item["exists"] = true
		item["isDir"] = info.IsDir()
		item["size"] = info.Size()
		item["mode"] = info.Mode().String()
		item["modTime"] = info.ModTime().UnixMilli()
		if abs, err := filepath.Abs(p); err == nil {
			item["abs"] = abs
		}
		items = append(items, item)
	}
	return map[string]any{"count": len(items), "items": items}
}

func pathsFrom(args map[string]any) ([]string, error) {
	raw, ok := args["paths"]
	if !ok {
		return nil, errors.New("缺少参数 paths")
	}
	out := []string{}
	switch v := raw.(type) {
	case []any:
		for _, it := range v {
			if s := strings.TrimSpace(fmt.Sprint(it)); s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	default:
		return nil, fmt.Errorf("paths 参数类型不支持：%T", raw)
	}
	if len(out) == 0 {
		return nil, errors.New("paths 为空")
	}
	return out, nil
}

func runAction(action string, args map[string]any) (map[string]any, error) {
	// 桌面控制权限闸门：**先过闸门再动手**。fs 类动作先把 paths 抽出来给闸门看范围，
	// 越权直接在这儿被挡下（错误信息会如实回给后端与用户）。
	var gatePaths []string
	if action == proto.ActionFsStat || action == proto.ActionFsDelete {
		if ps, err := pathsFrom(args); err == nil {
			gatePaths = ps
		}
	}
	if err := permGate(action, gatePaths); err != nil {
		return nil, err
	}

	switch action {
	case proto.ActionPing:
		return map[string]any{"pong": true, "at": time.Now().UnixMilli()}, nil
	case proto.ActionSysInfo:
		return localInfo(), nil
	case proto.ActionWindowNow:
		w, err := activeWindow()
		if err != nil {
			return nil, err
		}
		return map[string]any{"window": w}, nil
	case proto.ActionFsStat:
		paths, err := pathsFrom(args)
		if err != nil {
			return nil, err
		}
		return statPaths(paths), nil
	case proto.ActionFsDelete:
		paths, err := pathsFrom(args)
		if err != nil {
			return nil, err
		}
		return deletePaths(paths), nil
	default:
		return nil, fmt.Errorf("本端不支持的动作：%s（支持：ping / %s / %s / %s / %s）",
			action, proto.ActionSysInfo, proto.ActionWindowNow, proto.ActionFsStat, proto.ActionFsDelete)
	}
}

// deletePaths 删除文件/目录。**只有在「完全访问」档 + 后端人工审批通过后**才会走到这里，
// 所以这里再做一层"危险目标"护栏：盘根与系统目录一律拒绝，避免手滑把系统删了。
func deletePaths(paths []string) map[string]any {
	items := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		item := map[string]any{"path": p, "removed": false}
		abs, err := filepath.Abs(p)
		if err != nil {
			item["error"] = "路径不合法"
			items = append(items, item)
			continue
		}
		abs = filepath.Clean(abs)
		if guardedPath(abs) {
			item["abs"] = abs
			item["error"] = "拒绝删除盘根或系统目录（护栏）"
			items = append(items, item)
			continue
		}
		if err := os.RemoveAll(abs); err != nil {
			item["error"] = err.Error()
			items = append(items, item)
			continue
		}
		item["abs"] = abs
		item["removed"] = true
		items = append(items, item)
	}
	return map[string]any{"count": len(items), "items": items}
}

// guardedPath 是不是"不许删"的路径：盘根、以及系统目录的第一层
func guardedPath(abs string) bool {
	// 盘根：D:\ 这种（去掉了末尾反斜杠也就剩 "D:"）
	if len(abs) <= 3 && strings.HasSuffix(abs, `:\`) {
		return true
	}
	vol := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(strings.TrimPrefix(abs, vol), `\`)
	first := rest
	if i := strings.Index(rest, `\`); i >= 0 {
		first = rest[:i]
	}
	switch strings.ToLower(strings.TrimSpace(first)) {
	case "windows", "program files", "program files (x86)", "programdata", "system volume information", "$recycle.bin":
		return true
	}
	return false
}

/* ---------------- 设备连接状态（给界面看） ---------------- */

type devState struct {
	mu        sync.Mutex
	enabled   bool
	deviceID  string
	connected bool
	server    string
	lastError string
}

var devSt = &devState{}

func (d *devState) begin(server, id string) {
	d.mu.Lock()
	d.enabled, d.server, d.deviceID = true, server, id
	d.connected, d.lastError = false, ""
	d.mu.Unlock()
}
func (d *devState) online() {
	d.mu.Lock()
	d.connected, d.lastError = true, ""
	d.mu.Unlock()
}
func (d *devState) offline(why string) {
	d.mu.Lock()
	d.connected = false
	if why != "" {
		d.lastError = why
	}
	d.mu.Unlock()
}
func (d *devState) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{
		"enabled": d.enabled, "connected": d.connected,
		"deviceId": d.deviceID, "server": d.server, "lastError": d.lastError,
		"caps": caps(), "version": desktopAppVersion,
	}
}

/* ---------------- 设备客户端（可随配置变化重启） ---------------- */

// 平台钩子在 server.go 里声明；这里注册 Windows 的真实实现。
func init() {
	deviceRestart = restartDevice
	deviceSnapshot = func() map[string]any { return devSt.snapshot() }
}

var (
	devMu     sync.Mutex
	devCancel context.CancelFunc
)

func onCommand(cmd proto.Command) proto.Receipt {
	started := time.Now().UnixMilli()
	res, err := runAction(cmd.Action, cmd.Args)
	rc := proto.Receipt{TaskID: cmd.TaskID, StartedAt: started, FinishedAt: time.Now().UnixMilli()}
	if err != nil {
		rc.Status = proto.ReceiptFailed
		rc.Error = err.Error()
		if res != nil {
			rc.Result = res
		}
		return rc
	}
	rc.Status = proto.ReceiptDone
	rc.Result = res
	return rc
}

// restartDevice 按当前配置（重新）建立设备连接；server 为空则不连。
func restartDevice(ctx context.Context, trigger string) {
	server, token := getConfig()
	devMu.Lock()
	if devCancel != nil {
		devCancel()
		devCancel = nil
	}
	if strings.TrimSpace(server) == "" {
		devMu.Unlock()
		devSt.offline("还没配置后端地址")
		log.Printf("[设备] 未配置后端，设备连接不启动")
		return
	}
	c, cancel := context.WithCancel(ctx)
	devCancel = cancel
	devMu.Unlock()

	id := deviceID()
	devSt.begin(server, id)
	log.Printf("[设备] 以设备身份连接后端：%s（%s，id=%s，caps=%s）", server, trigger, id, strings.Join(caps(), ","))

	cfg := client.Config{
		URL:       server,
		Token:     token,
		Logger:    slog.Default(),
		Heartbeat: 10 * time.Second,
		Info: proto.Hello{
			DeviceID: id, Name: hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH,
			Hostname: hostname(), User: username(), Version: desktopAppVersion,
			Protocol: proto.Version, Caps: caps(), StartedAt: time.Now().UnixMilli(),
			GuiPerm: permNow(), GuiScopes: scopesNow(),
		},
	}
	onReady := func(sess *client.Session) {
		devSt.online()
		log.Printf("[设备] 注册成功：%s（后端 %s）", sess.Ack().DeviceID, sess.Ack().Server)
		go func() {
			<-sess.Done()
			devSt.offline("")
		}()
	}
	go func() {
		if err := client.RunForever(c, cfg, onCommand, onReady); err != nil && c.Err() == nil {
			devSt.offline(err.Error())
			log.Printf("[设备] 连接已停止：%v", err)
		}
	}()
}
