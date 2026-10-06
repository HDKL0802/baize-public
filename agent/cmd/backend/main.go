// Command backend 是白泽跨端智能体的后端 Agent：只跑日志、干活、编排。
//
// 形态：纯 HTTP + WebSocket 服务（可跑在低配 NAS 上），提供
//   - 设备注册表与心跳（桌面端/手机端接入）
//   - 统一任务库（task_id / payload / status / device_id / 回执）
//   - 危险操作人工审批闸门
//   - 三态记录（事件 / 指令 / 回执）与记忆落盘（SQLite）
//   - 控制台页面（日志流 / 设备 / 任务 / 审批 / 记忆）
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"baize/internal/agentsvc"
	"baize/internal/httpapi"
	"baize/internal/hub"
	"baize/internal/logx"
	"baize/internal/mcp"
	"baize/internal/store"
	"baize/shared/proto"
)

const version = "0.9.18"

func main() {
	var (
		addr        = flag.String("addr", "127.0.0.1:8787", "监听地址；要暴露给手机端时用 0.0.0.0:8787")
		dataDir     = flag.String("data", "data", "数据目录（agent.db / token / backend.log）")
		tokenFlag   = flag.String("token", "", "配对令牌；留空则首次自动生成并写入 <数据目录>/token")
		noToken     = flag.Bool("no-token", false, "关闭令牌校验（仅限本机调试）")
		receiptWait = flag.Duration("receipt-timeout", 120*time.Second, "指令下发后等回执的超时时间")
		levelFlag   = flag.String("log-level", "info", "日志级别 debug|info|warn|error")
		openPanel   = flag.Bool("open", false, "启动后用默认浏览器打开控制台")
		showVersion = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	// 让「服务版本 / MCP 客户端版本」与后端版本只保留一处来源（历史上是分开写死的，曾漂移）。
	agentsvc.SetVersion(version)
	mcp.SetVersion(version)

	if *showVersion {
		fmt.Printf("baize-backend %s（内部协议 %s，MIT）\n", version, proto.Version)
		return
	}

	level, err := logx.ParseLevel(*levelFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(2)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "错误：创建数据目录失败："+err.Error())
		os.Exit(1)
	}
	logPath := filepath.Join(*dataDir, "backend.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：打开日志文件失败："+err.Error())
		os.Exit(1)
	}
	defer logFile.Close()

	ring := logx.NewRing(1000)
	lg := slog.New(logx.NewHandler(io.MultiWriter(os.Stdout, logFile), ring, level))

	token, err := resolveToken(*dataDir, *tokenFlag, *noToken)
	if err != nil {
		lg.Error("准备配对令牌失败", "err", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(*dataDir, "agent.db")
	st, err := store.Open(dbPath)
	if err != nil {
		lg.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	h := hub.New(st, lg, token, *receiptWait)

	// Agent 运行时（模型通道/工具/记忆/审批/定时任务），配置在 <数据目录>/config.json；
	// 把设备中枢交给它，Agent 就能把活派到桌面端/手机端执行（跨端调度）
	agentSvc, err := agentsvc.New(*dataDir, lg, agentsvc.WithDevices(h))
	if err != nil {
		lg.Error("启动 Agent 服务失败", "err", err)
		os.Exit(1)
	}
	defer agentSvc.Close()

	apiServer := httpapi.New(h, lg, ring, version)
	apiServer.SetAgent(agentSvc)
	apiServer.SetKB(agentSvc.KB())
	srv := &http.Server{
		Addr:              *addr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h.StartSweeper(ctx)

	url := "http://" + displayAddr(*addr) + "/"
	lg.Info("白泽 Agent 后端启动", "version", version, "protocol", proto.Version)
	lg.Info("控制台", "url", url)
	lg.Info("设备接入", "ws", "ws://"+displayAddr(*addr)+"/ws/device", "token", maskToken(token))
	lg.Info("数据库", "path", dbPath, "log", logPath, "receiptTimeout", receiptWait.String())
	lg.Info("Agent 运行时已就绪",
		"config", filepath.Join(*dataDir, "config.json"),
		"workdir", agentSvc.Workdir(),
		"memory", filepath.Join(*dataDir, "memory.db"),
		"skills", filepath.Join(*dataDir, "skills"),
		"backups", agentSvc.BackupDir())
	lg.Info("知识库已就绪（待办 + 密码本正本）",
		"dir", agentSvc.KB().Dir(), "api", "/api/kb/state", "files", agentSvc.KB().Files().Dir())
	if hint := agentSvc.MCPHints(); hint != "" {
		lg.Info("MCP 外部工具", "status", hint)
	}
	if token == "" {
		lg.Warn("已关闭令牌校验（--no-token），请勿在非本机环境这样跑")
	}

	if *openPanel {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				lg.Warn("打开浏览器失败，请手动访问", "url", url, "err", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		lg.Info("收到退出信号，正在关闭")
	case err := <-errCh:
		lg.Error("服务异常退出", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		lg.Warn("关闭服务超时", "err", err)
	}
	lg.Info("已退出")
}

// resolveToken 决定配对令牌：--no-token 关闭；--token 指定；否则读/生成 <data>/token
func resolveToken(dataDir, flagToken string, noToken bool) (string, error) {
	if noToken {
		return "", nil
	}
	if strings.TrimSpace(flagToken) != "" {
		return strings.TrimSpace(flagToken), nil
	}
	path := filepath.Join(dataDir, "token")
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成令牌失败：%w", err)
	}
	token := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写入令牌文件失败：%w", err)
	}
	return token, nil
}

// displayAddr 把 0.0.0.0 / :: 换成 127.0.0.1 便于直接点开
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0") {
		return "127.0.0.1" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

func maskToken(token string) string {
	if token == "" {
		return "（未启用）"
	}
	if len(token) <= 6 {
		return token
	}
	return token[:4] + "…" + token[len(token)-4:]
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
