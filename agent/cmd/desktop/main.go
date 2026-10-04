// Command desktop 是白泽跨端智能体的桌面端（Go CLI）：
// 连接后端 → 上报活动窗口/前台进程 → 接收指令就地执行 → 回执。
//
// 用法示例：
//
//	baize-desktop --once                                  # 自检：打印本机信息与当前窗口
//	baize-desktop --server http://127.0.0.1:8787 --token <令牌>
//	baize-desktop --server http://127.0.0.1:8787 --token <令牌> --dry-run
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"baize/shared/client"
	"baize/internal/executor"
	"baize/internal/logx"
	"baize/shared/proto"
	"baize/internal/sysinfo"
)

const version = "0.1.0"

func main() {
	var (
		server      = flag.String("server", "http://127.0.0.1:8787", "后端地址")
		token       = flag.String("token", "", "配对令牌（后端启动时会打印，也存在 <后端数据目录>/token）")
		tokenFile   = flag.String("token-file", "", "从文件读取配对令牌（优先于 --token）")
		name        = flag.String("name", "", "设备显示名，默认取主机名")
		interval    = flag.Duration("interval", 5*time.Second, "活动窗口采集间隔")
		heartbeat   = flag.Duration("heartbeat", 10*time.Second, "心跳间隔")
		dryRun      = flag.Bool("dry-run", false, "危险动作只回执计划、不真正执行（演示/联调安全档）")
		allowDanger = flag.Bool("allow-danger", false, "允许执行删除等危险动作（不带 --allow-root 时范围不受限，请谨慎）")
		allowRoots  = flag.String("allow-root", "", "只允许删除这些根目录下的内容，多个用英文逗号分隔（推荐）")
		noReport    = flag.Bool("no-report", false, "不上报活动窗口（只连接、只干活）")
		once        = flag.Bool("once", false, "打印本机信息与当前活动窗口后退出（不连后端）")
		levelFlag   = flag.String("log-level", "info", "日志级别 debug|info|warn|error")
		logFileFlag = flag.String("log", "", "把日志同时写入该文件")
		showVersion = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("baize-desktop %s（内部协议 %s，MIT）\n", version, proto.Version)
		return
	}

	level, err := logx.ParseLevel(*levelFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(2)
	}

	var writers []io.Writer
	writers = append(writers, os.Stdout)
	var logFile *os.File
	if *logFileFlag != "" {
		if dir := filepath.Dir(*logFileFlag); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		logFile, err = os.OpenFile(*logFileFlag, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误：打开日志文件失败："+err.Error())
			os.Exit(1)
		}
		defer logFile.Close()
		writers = append(writers, logFile)
	}
	lg := slog.New(logx.NewHandler(io.MultiWriter(writers...), nil, level))

	deviceID := sysinfo.DeviceID()
	hostname := sysinfo.Hostname()
	displayName := strings.TrimSpace(*name)
	if displayName == "" {
		displayName = hostname
	}

	if *once {
		printSelfCheck(lg, deviceID, displayName)
		return
	}

	tk := strings.TrimSpace(*token)
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			lg.Error("读取令牌文件失败", "file", *tokenFile, "err", err)
			os.Exit(1)
		}
		tk = strings.TrimSpace(string(b))
	}

	opt := executor.Options{DryRun: *dryRun, AllowDanger: *allowDanger, AllowRoots: splitList(*allowRoots)}
	var busy atomic.Bool

	info := proto.Hello{
		DeviceID:  deviceID,
		Name:      displayName,
		OS:        sysinfo.OS(),
		Arch:      sysinfo.Arch(),
		Hostname:  hostname,
		User:      sysinfo.Username(),
		Version:   version,
		Protocol:  proto.Version,
		Caps:      sysinfo.Caps(),
		StartedAt: time.Now().UnixMilli(),
	}

	lg.Info("白泽桌面端启动", "version", version, "device", deviceID, "name", displayName,
		"caps", strings.Join(info.Caps, ","), "dangerMode", dangerMode(opt))

	cfg := client.Config{
		URL:          *server,
		Token:        tk,
		Info:         info,
		Logger:       lg,
		Heartbeat:    *heartbeat,
		HelloTimeout: 15 * time.Second,
		Busy:         busy.Load,
	}

	onCommand := func(cmd proto.Command) proto.Receipt {
		busy.Store(true)
		defer busy.Store(false)
		start := time.Now().UnixMilli()
		res, err := executor.Run(cmd.Action, cmd.Args, opt)
		rc := proto.Receipt{TaskID: cmd.TaskID, StartedAt: start, FinishedAt: time.Now().UnixMilli()}
		switch {
		case errors.Is(err, executor.ErrDangerDisabled):
			rc.Status = proto.ReceiptRejected
			rc.Error = err.Error()
		case err != nil:
			rc.Status = proto.ReceiptFailed
			rc.Error = err.Error()
			rc.Result = res
		default:
			rc.Status = proto.ReceiptDone
			rc.Result = res
		}
		return rc
	}

	onReady := func(sess *client.Session) {
		if *noReport {
			lg.Info("已按 --no-report 关闭活动窗口上报")
			return
		}
		go newSampler(lg, sess, *interval).run()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		err := client.Run(ctx, cfg, onCommand, onReady)
		if ctx.Err() != nil {
			lg.Info("收到退出信号，桌面端退出")
			return
		}
		if errors.Is(err, client.ErrFatal) {
			lg.Error("连接被拒绝，请检查 --server 与 --token（不会自动重试）", "err", err)
			os.Exit(1)
		}
		lg.Warn("与后端断开，稍后重连", "err", err, "retryInSec", int(backoff.Seconds()), "attempt", attempt)
		select {
		case <-ctx.Done():
			lg.Info("收到退出信号，桌面端退出")
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

/* ---------- 活动窗口采集 ---------- */

type sampler struct {
	lg       *slog.Logger
	sess     *client.Session
	interval time.Duration
	last     string
	lastErr  string
}

func newSampler(lg *slog.Logger, sess *client.Session, interval time.Duration) *sampler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &sampler{lg: lg, sess: sess, interval: interval}
}

func (s *sampler) run() {
	s.lg.Info("开始采集活动窗口", "interval", s.interval.String())
	s.sample() // 先来一次，便于立刻在控制台看到
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.sess.Done():
			s.lg.Debug("连接结束，停止采集")
			return
		case <-t.C:
			s.sample()
		}
	}
}

func (s *sampler) sample() {
	w, err := sysinfo.ActiveWindow()
	if err != nil {
		if s.lastErr != err.Error() {
			s.lastErr = err.Error()
			s.lg.Warn("读取活动窗口失败（将按间隔重试）", "err", err)
		}
		return
	}
	s.lastErr = ""
	key := w.Process + "\x00" + w.Title
	if key == s.last {
		return
	}
	s.last = key

	env, err := proto.New(proto.TypeEvent, "", "", proto.Event{
		Kind:  proto.EventWindow,
		At:    w.At,
		Title: w.Title,
		Data:  map[string]any{"process": w.Process, "pid": w.PID},
	})
	if err != nil {
		s.lg.Warn("事件编码失败", "err", err)
		return
	}
	if err := s.sess.Send(env); err != nil {
		s.lg.Debug("事件上报失败", "err", err)
		return
	}
	s.lg.Info("上报窗口", "process", w.Process, "pid", w.PID, "title", w.Title)
}

/* ---------- 自检 ---------- */

// dangerMode 用一句话说明当前危险动作策略（启动日志里显示，避免“看起来能用其实不能用”）
func dangerMode(opt executor.Options) string {
	switch {
	case opt.DryRun:
		return "dry-run（只预演，不真删）"
	case len(opt.AllowRoots) > 0:
		return "白名单模式：" + strings.Join(opt.AllowRoots, " | ")
	case opt.AllowDanger:
		return "已显式放行（--allow-danger，范围不受限）"
	default:
		return "未开启（删除类指令会被本端拒绝）"
	}
}

// splitList 解析逗号分隔的参数
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printSelfCheck(lg *slog.Logger, deviceID, name string) {
	local := sysinfo.Local()
	fmt.Printf("设备 id   : %s\n", deviceID)
	fmt.Printf("设备名    : %s\n", name)
	fmt.Printf("系统      : %v/%v  主机 %v  用户 %v\n", local["os"], local["arch"], local["hostname"], local["user"])
	fmt.Printf("能力      : %s\n", strings.Join(sysinfo.Caps(), ", "))
	fmt.Printf("工作目录  : %v\n", local["cwd"])
	fmt.Printf("进程      : pid=%v exe=%v\n", local["pid"], local["exe"])

	w, err := sysinfo.ActiveWindow()
	if err != nil {
		fmt.Printf("当前窗口  : 读取失败 - %v\n", err)
		return
	}
	fmt.Printf("当前窗口  : %s\n", w.Title)
	fmt.Printf("前台进程  : %s (pid=%d)\n", w.Process, w.PID)
	lg.Debug("自检完成")
}
