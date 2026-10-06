//go:build windows

// 白泽 · Windows 桌面端
//
// 架构（对齐手机端「内核 + WebView」，只是这里用桌面 WebView2）：
//  1. 本机回环 HTTP 服务：内嵌界面 + 把 /api/be/* 原样代理到 NAS 后端
//     （配对令牌只留在原生侧，不进网页，也绕开跨源限制）；
//  2. 一个 WebView2 原生窗口指向它。
//
// 为什么不用 Tauri/QwenPaw 那套：本机没有 Rust 工具链。Go + 纯 Go 的 WebView2 绑定
// 同样能做到「原生窗口 + WebView2 + 同一套 Web 界面」，且无 cgo / 无 Node。
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
)

// setupLogging: 以 GUI 子系统链接（-H windowsgui）启动时没有控制台窗口，日志默认
// 无处可去。这里把它落到 <数据目录>\desktop.log，同时仍写 stderr（有控制台时可见）。
// 数据目录默认 %APPDATA%\白泽，用户可以在「设置 → 数据目录」里改到 D 盘等位置。
// 超过 512KB 就先删掉重来，免得无限长大。
func setupLogging() {
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "desktop.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 512*1024 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return // 打不开就保持默认（stderr）
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
}

func main() {
	// 先读本地配置（数据目录可能被改过），再按数据目录去落日志
	loadConfig()
	setupLogging()

	addr := flag.String("addr", "127.0.0.1:0", "本机回环监听地址")
	server := flag.String("server", "", "NAS 后端地址，例如 http://192.168.1.100:8787（会写入本地配置）")
	token := flag.String("token", "", "配对令牌（会写入本地配置；留空 = 用已保存的那串）")
	debug := flag.Bool("debug", false, "打开 WebView2 调试（可用 CDP 连）")
	flag.Parse()

	// 单实例：已经有一个在跑就把它的窗口叫到前台（它可能正隐藏在后台）
	if !ensureSingleInstance() {
		log.Printf("已经有一个白泽在跑，已把它的窗口叫到前台")
		os.Exit(0)
	}
	cleanupOldVersion() // 上次自动更新留下的 .old 顺手删掉

	if strings.TrimSpace(*server) != "" || strings.TrimSpace(*token) != "" {
		setConfig(*server, *token)
	}
	if s, _ := getConfig(); s != "" {
		log.Printf("后端：%s", s)
	} else {
		log.Printf("后端未配置：请在窗口里的「设置」填地址与令牌")
	}

	// 设备连接在后台常驻；窗口关掉时随 ctx 一起收（下次开 App 会再连上）
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	baseCtx = ctx
	deviceRestart(ctx, "启动")

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听失败：%v", err)
	}
	go func() { _ = http.Serve(ln, buildHandler()) }()

	url := "http://" + ln.Addr().String() + "/"
	localBaseURL = "http://" + ln.Addr().String() // 悬浮球用它读「待审批」状态
	log.Printf("白泽桌面端启动：%s", url)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     *debug,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  appWindowTitle,
			Width:  1280,
			Height: 800,
		},
	})
	if w == nil {
		log.Fatal("WebView2 初始化失败：系统缺少 WebView2 运行时（Win11 自带；Win10 需安装）")
	}
	defer w.Destroy()
	w.Navigate(url)

	// 常驻三件套：托盘图标 + 「关窗=隐藏到后台」（失败只警告，不影响启动）
	installShell(w)
	// 悬浮球（B1）：常驻桌面的轻量入口（失败只记日志）
	startFloatingBall()

	w.Run()
	os.Exit(0)
}
