// Package sysinfo 采集本机信息（活动窗口/前台进程等），供桌面端上报。
package sysinfo

import (
	"hash/fnv"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"

	"baize/shared/proto"
)

// WindowInfo 活动窗口/前台进程
type WindowInfo struct {
	Title   string `json:"title"`
	Process string `json:"process"`
	PID     uint32 `json:"pid"`
	At      int64  `json:"at"`
}

// Local 本机基本信息
func Local() map[string]any {
	host, _ := os.Hostname()
	uname := ""
	if cu, err := user.Current(); err == nil {
		uname = cu.Username
	}
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	info := map[string]any{
		"os":        runtime.GOOS,
		"arch":      runtime.GOARCH,
		"hostname":  host,
		"user":      uname,
		"cwd":       wd,
		"exe":       exe,
		"pid":       os.Getpid(),
		"goVersion": runtime.Version(),
		"at":        time.Now().UnixMilli(),
	}
	return info
}

// OS 操作系统标识
func OS() string { return runtime.GOOS }

// Arch CPU 架构
func Arch() string { return runtime.GOARCH }

// Hostname 主机名
func Hostname() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "unknown-host"
	}
	return h
}

// Username 当前用户
func Username() string {
	if cu, err := user.Current(); err == nil {
		return cu.Username
	}
	return ""
}

// Caps 本端能力标记
func Caps() []string {
	return []string{proto.CapWindow, proto.CapFs, proto.CapFsDelete}
}

// DeviceID 稳定设备 id：主机名 + 用户 + 系统 的短哈希，重启不变
func DeviceID() string {
	raw := strings.ToLower(Hostname() + "|" + Username() + "|" + runtime.GOOS)
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	return sanitize(Hostname()) + "-" + toHex(h.Sum32())
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
