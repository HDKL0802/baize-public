// Command agent 是白泽 Agent 的命令行入口：
// 跑一次任务（含工具链与记忆落盘）、管模型通道、管 MCP 外部工具、备份恢复、
// 查记忆、看运行记录、管理检查点。
//
// 例子：
//
//	agent provider add --name deepseek --base-url https://api.deepseek.com/v1 --model deepseek-chat --api-key sk-xxx --allow-remote --test
//	agent run "把这个目录里有几个文件、都是什么写进 summary.md" --provider fake
//	agent run "查一下 docs 目录有没有过期的说明文件" --provider openai --base-url http://127.0.0.1:11434/v1 --model qwen2.5:7b
//	agent memory search "过期的说明文件"
//	agent runs
//	agent checkpoints list
//	agent mcp list
//	agent backup create
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"baize/internal/agentrt"
	"baize/internal/backup"
	"baize/internal/config"
	"baize/internal/hooks"
	"baize/internal/llm"
	"baize/internal/logx"
	"baize/internal/mcp"
	"baize/internal/memory"
	"baize/internal/tools"
)

const version = "0.8.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, rest := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = cmdRun(rest)
	case "memory":
		err = cmdMemory(rest)
	case "runs":
		err = cmdRuns(rest)
	case "checkpoints", "ck":
		err = cmdCheckpoints(rest)
	case "mcp":
		err = cmdMCP(rest)
	case "provider", "pv":
		err = cmdProvider(rest)
	case "backup", "bk":
		err = cmdBackup(rest)
	case "version", "-v", "--version":
		fmt.Printf("baize agent %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		err = fmt.Errorf("未知命令：%s（agent help 看用法）", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`白泽 Agent（Go 版）用法：

  agent run "<一句话任务>" [参数]
       --provider fake|openai|anthropic   模型通道类型（默认 fake，便于离线自检）
       --base-url URL                     模型地址，如 http://127.0.0.1:11434/v1
       --model NAME                       模型名
       --api-key KEY                      （仅远端需要；默认只允许本地地址）
       --allow-remote                     允许使用非本机模型地址
       --fallback-base-url / --fallback-model   回退通道（可选）
       --workspace DIR                    工作目录（文件类工具只能在这里活动），默认 ./workspace
       --data DIR                         数据目录（记忆库/运行记录/检查点），默认 ./.agent
       --recipe NAME                      任务类型（chat|plan|code…），用于模型路由，默认 chat
       --steps N                          最大步数，默认 12
       --retries N                        单次模型调用重试次数，默认 2
       --budget N                         上下文 token 预算，默认 8000
       --allow-shell                      允许执行 shell_run（默认关闭）
       --approve ask|all|none             危险操作审批策略，默认 ask
       --no-checkpoint                    关闭"变更前自动快照"
       --json                             以 JSON 输出结果
       --verbose                          打印调试日志与事件钩子

  agent memory search "<关键词>" [--data DIR] [--limit N]
  agent memory tree [--data DIR] [--limit N]
  agent memory obsidian --out DIR [--data DIR]
  agent runs [--data DIR] [--limit N] [--json]
  agent checkpoints list [--data DIR]
  agent checkpoints rollback <id> [--data DIR] [--workspace DIR]

  agent provider list [--data DIR]
      看模型通道状态（哪些可用、不可用的原因）
  agent provider add --name NAME --base-url URL --model MODEL [--api-key KEY]
                     [--protocol openai|anthropic] [--kinds chat,plan] [--fallback]
                     [--allow-remote] [--test] [--data DIR]
      新增/更新一个模型通道；地址不是本机时默认拒绝，要显式加 --allow-remote
      --test 会做一次极小的真实调用验证连通（会消耗一点点额度）
  agent provider remove --name NAME [--data DIR]
  agent provider test [--name NAME] [--data DIR]
      对某个（或全部）通道做一次极小的真实调用

  agent mcp list [--data DIR]
      看已配置的 MCP 服务状态（连接不上会明确显示原因）
  agent mcp add --name NAME [--transport stdio|http] [--command CMD] [--args "a b"]
                [--url URL] [--trust] [--safe "tool1,tool2"] [--data DIR]
      新增/更新一个 MCP 服务并立刻连接（热插拔，不用重启后端）
  agent mcp remove --name NAME [--data DIR]
  agent mcp reload [--name NAME] [--data DIR]
  agent mcp call --server S --tool T [--call-args '{"k":"v"}'] [--data DIR]

  agent backup create [--out FILE] [--select all|config,memory,runs,hub,skills,checkpoints,workspace,vault] [--note N] [--data DIR]
  agent backup list [--data DIR]
  agent backup verify <文件> [--data DIR]
  agent backup restore <文件> [--data DIR] [--dry-run] [--only config.json] [--no-safepoint]
  agent backup delete <文件> [--data DIR]
  agent backup prune --keep N [--data DIR]
`)
}

/* ---------- run ---------- */

func cmdRun(argv []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		provider     = fs.String("provider", "fake", "fake | openai | anthropic")
		baseURL      = fs.String("base-url", "", "模型地址")
		model        = fs.String("model", "", "模型名")
		apiKey       = fs.String("api-key", "", "API Key")
		allowRemote  = fs.Bool("allow-remote", false, "允许远端模型地址")
		fbBaseURL    = fs.String("fallback-base-url", "", "回退通道地址")
		fbModel      = fs.String("fallback-model", "", "回退通道模型")
		workspace    = fs.String("workspace", "workspace", "工作目录")
		dataDir      = fs.String("data", ".agent", "数据目录")
		recipe       = fs.String("recipe", "chat", "任务类型")
		maxSteps     = fs.Int("steps", 12, "最大步数")
		retries      = fs.Int("retries", 2, "模型调用重试次数")
		budget       = fs.Int("budget", 8000, "上下文 token 预算")
		allowShell   = fs.Bool("allow-shell", false, "允许执行 shell_run")
		approveMode  = fs.String("approve", "ask", "危险操作审批：ask|all|none")
		noCheckpoint = fs.Bool("no-checkpoint", false, "关闭变更前快照")
		asJSON       = fs.Bool("json", false, "JSON 输出")
		verbose      = fs.Bool("verbose", false, "详细日志")
	)
	if err := fs.Parse(argv); err != nil {
		return err
	}
	goal := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if goal == "" {
		return errors.New(`用法：agent run "一句话任务" [参数]`)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	lg := slog.New(logx.NewHandler(os.Stdout, nil, level))

	ws, err := tools.NewWorkspace(*workspace, false)
	if err != nil {
		return err
	}
	reg := tools.NewRegistry()
	tools.RegisterFS(reg, ws)
	reg.Register(tools.NewShellRun(ws, *allowShell))
	reg.Register(tools.NewWebFetch())

	mem, err := memory.Open(*dataDir)
	if err != nil {
		return err
	}
	defer mem.Close()
	tools.RegisterMemory(reg, mem, memory.DefaultNamespace)

	runStore, err := agentrt.OpenStore(*dataDir)
	if err != nil {
		return err
	}
	defer runStore.Close()

	// 模型通道
	main0, err := buildProvider(*provider, *baseURL, *model, *apiKey, *allowRemote, "main")
	if err != nil {
		return err
	}
	var fallbacks []llm.Provider
	if *fbBaseURL != "" {
		p, err := buildProvider(*provider, *fbBaseURL, *fbModel, "", *allowRemote, "fallback")
		if err != nil {
			return err
		}
		fallbacks = append(fallbacks, p)
	}
	var summarizer memory.Summarizer
	if _, isFake := main0.(*llm.Fake); !isFake {
		summarizer = memory.NewLLMSummarizer(main0)
	}

	bus := hooks.NewBus()
	if *verbose {
		bus.OnAny(func(_ context.Context, ev hooks.Event, p hooks.Payload) {
			lg.Debug("事件", "ev", string(ev), "payload", briefJSON(p))
		})
	} else {
		// 默认只报关键节点，避免刷屏
		bus.On(hooks.EventToolBefore, func(_ context.Context, _ hooks.Event, p hooks.Payload) {
			lg.Info("调用工具", "tool", p["tool"], "args", briefJSON(p["args"]))
		})
		bus.On(hooks.EventToolBlocked, func(_ context.Context, _ hooks.Event, p hooks.Payload) {
			lg.Warn("工具被审批闸门拦下", "tool", p["tool"])
		})
		bus.On(hooks.EventCompress, func(_ context.Context, _ hooks.Event, p hooks.Payload) {
			lg.Info("上下文已压缩", "messages", p["messages"])
		})
		bus.On(hooks.EventCheckpoint, func(_ context.Context, _ hooks.Event, p hooks.Payload) {
			lg.Info("已打快照", "id", p["id"], "files", p["files"])
		})
	}

	var ck *agentrt.CheckpointManager
	if !*noCheckpoint {
		ck, err = agentrt.NewCheckpointManager(*dataDir, ws.Root(), 20)
		if err != nil {
			return err
		}
	}

	approver, err := makeApprover(*approveMode, lg)
	if err != nil {
		return err
	}

	runner := agentrt.New(agentrt.Config{
		Provider: main0, Fallbacks: fallbacks, Tools: reg, Memory: mem, Hooks: bus,
		Store: runStore, Workspace: ws, Checkpoints: ck, Summarizer: summarizer, Logger: lg,
		Recipe: *recipe, MaxSteps: *maxSteps, MaxRetries: *retries, TokenBudget: *budget,
		Approve: approver, CheckpointBeforeWrite: ck != nil,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := time.Now()
	res, runErr := runner.Run(ctx, goal)

	if *asJSON {
		out := map[string]any{"result": res, "error": errStr(runErr)}
		raw, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(raw))
		return runErr
	}

	fmt.Println("── 执行轨迹 ─────────────────────────────")
	for _, t := range res.Trace {
		line := fmt.Sprintf("  [%d] %s", t.Step, t.Tool)
		if t.Args != "" && t.Args != "{}" {
			line += " " + t.Args
		}
		if t.Error != "" {
			line += "  × " + t.Error
		} else {
			line += "  → " + t.Result
		}
		fmt.Println(briefLine(line, 300))
	}
	fmt.Println("── 结果 ─────────────────────────────────")
	fmt.Printf("  运行 id：%s（用时 %s，步数 %d，工具调用 %d，重试 %d）\n",
		res.RunID, time.Since(started).Round(time.Millisecond), res.Steps, res.ToolCalls, res.Retries)
	if len(res.Checkpoints) > 0 {
		fmt.Printf("  快照：%s\n", strings.Join(res.Checkpoints, ", "))
	}
	fmt.Printf("  记忆落盘：%d 块\n", res.MemoryChunks)
	if strings.TrimSpace(res.Text) != "" {
		fmt.Printf("  结论：%s\n", res.Text)
	}
	if len(res.Errors) > 0 {
		fmt.Printf("  过程中的问题：%s\n", strings.Join(res.Errors, " | "))
	}
	fmt.Printf("  产物：工作目录 %s ｜ 记忆库 %s\n", ws.Root(), mem.Path())
	return runErr
}

// buildProvider 按参数建通道
func buildProvider(kind, baseURL, model, apiKey string, allowRemote bool, name string) (llm.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "fake":
		return llm.NewFake("fake-"+name, fakeScript), nil
	case "openai", "anthropic":
		if strings.TrimSpace(baseURL) == "" {
			return nil, errors.New("用真实模型通道时必须给 --base-url")
		}
		cfg := llm.Config{
			Name: name, Protocol: kind, BaseURL: baseURL, APIKey: apiKey, Model: model,
		}
		// 本地优先策略：非本机地址必须显式放行
		router := llm.NewRouter(allowRemote)
		if err := router.CheckEndpoint(cfg); err != nil {
			return nil, err
		}
		return llm.New(cfg)
	}
	return nil, fmt.Errorf("不支持的 --provider：%s（可选 fake | openai | anthropic）", kind)
}

// fakeScript 确定性脚本：用来看"一句话任务 → 工具链 → 记忆落盘"整条链路通不通。
// 它不联网，只是按固定顺序调用工具，绝不会伪装成"模型已经想过了"。
func fakeScript(_ context.Context, call int, req llm.Request) (llm.Response, error) {
	switch call {
	case 1:
		return llm.CallTool("call-1", "fs_list", map[string]any{"path": "."}), nil
	case 2:
		goal := ""
		if len(req.Messages) > 0 {
			goal = req.Messages[0].Content
		}
		return llm.CallTool("call-2", "fs_write", map[string]any{
			"path": "summary.md",
			"content": "# 任务小结\n\n- 目标：" + goal +
				"\n- 说明：本文件由 fake 通道的确定性脚本写入，用于验证「一句话任务 → 工具链 → 记忆落盘」整条链路。\n",
		}), nil
	default:
		return llm.Say("已完成：列出了工作目录内容，并把小结写进 summary.md（fake 通道，确定性脚本）。"), nil
	}
}

// makeApprover 生成审批回调
func makeApprover(mode string, lg *slog.Logger) (func(string, map[string]any) bool, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "all":
		return func(tool string, args map[string]any) bool {
			lg.Warn("审批：自动放行危险操作", "tool", tool, "args", briefJSON(args))
			return true
		}, nil
	case "none":
		return func(tool string, _ map[string]any) bool {
			lg.Warn("审批：按策略拒绝", "tool", tool)
			return false
		}, nil
	case "ask", "":
		return func(tool string, args map[string]any) bool {
			fmt.Printf("⚠ 危险操作待审批：%s %s\n  执行？(y/N) ", tool, briefJSON(args))
			var answer string
			_, _ = fmt.Scanln(&answer)
			ok := strings.EqualFold(strings.TrimSpace(answer), "y") ||
				strings.EqualFold(strings.TrimSpace(answer), "yes")
			if !ok {
				fmt.Println("  已拒绝")
			}
			return ok
		}, nil
	}
	return nil, fmt.Errorf("不支持的 --approve：%s（可选 ask | all | none）", mode)
}

/* ---------- memory / runs / checkpoints ---------- */

func cmdMemory(argv []string) error {
	if len(argv) == 0 {
		return errors.New("用法：agent memory search|tree|obsidian …")
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	dataDir := fs.String("data", ".agent", "数据目录")
	limit := fs.Int("limit", 10, "条数")
	outDir := fs.String("out", "", "导出目录（obsidian）")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	mem, err := memory.Open(*dataDir)
	if err != nil {
		return err
	}
	defer mem.Close()

	switch sub {
	case "search":
		query := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if query == "" {
			return errors.New(`用法：agent memory search "关键词"`)
		}
		hits, err := mem.Search(query, *limit)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			fmt.Println("（没有命中任何记忆）")
			return nil
		}
		for i, h := range hits {
			fmt.Printf("%d  [%s] %s（评分 %.3f，%s）\n", i+1, h.Chunk.Kind, h.Chunk.Title, h.Score, h.Why)
			fmt.Printf("   %s\n", briefLine(strings.ReplaceAll(h.Chunk.Content, "\n", " "), 200))
		}
		return nil

	case "tree":
		nodes, err := mem.Tree(*limit)
		if err != nil {
			return err
		}
		if len(nodes) == 0 {
			fmt.Println("（记忆树还是空的）")
			return nil
		}
		for _, n := range nodes {
			label := n.Day
			if n.Level == 2 {
				label = n.Week + "（周）"
			}
			fmt.Printf("[L%d] %s ｜ %d 条 ｜ %d token\n", n.Level, label, n.Chunks, n.Tokens)
			if strings.TrimSpace(n.Summary) != "" {
				fmt.Printf("     %s\n", briefLine(strings.ReplaceAll(n.Summary, "\n", " "), 200))
			}
		}
		return nil

	case "obsidian":
		if strings.TrimSpace(*outDir) == "" {
			return errors.New("导出到 Obsidian 需要 --out DIR")
		}
		n, err := mem.WriteObsidian(*outDir)
		if err != nil {
			return err
		}
		fmt.Printf("已导出 %d 个日记忆文件到 %s\n", n, filepath.Join(*outDir, "memory"))
		return nil

	default:
		return fmt.Errorf("未知 memory 子命令：%s（可用 search | tree | obsidian）", sub)
	}
}

func cmdRuns(argv []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	dataDir := fs.String("data", ".agent", "数据目录")
	limit := fs.Int("limit", 20, "条数")
	asJSON := fs.Bool("json", false, "JSON 输出")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	st, err := agentrt.OpenStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	runs, err := st.Runs(*limit)
	if err != nil {
		return err
	}
	if *asJSON {
		raw, _ := json.MarshalIndent(runs, "", "  ")
		fmt.Println(string(raw))
		return nil
	}
	if len(runs) == 0 {
		fmt.Println("（还没有运行记录）")
		return nil
	}
	for _, r := range runs {
		fmt.Printf("%s  %s  %s  步数 %d/工具 %d/重试 %d  %.1fs\n", r.RunID, r.Status, r.Recipe,
			r.Steps, r.ToolCalls, r.Retries, float64(r.FinishedAt-r.StartedAt)/1000)
		fmt.Printf("   目标：%s\n", briefLine(r.Goal, 120))
		if r.Err != "" {
			fmt.Printf("   错误：%s\n", briefLine(r.Err, 160))
		}
	}
	return nil
}

func cmdCheckpoints(argv []string) error {
	if len(argv) == 0 {
		return errors.New("用法：agent checkpoints list|rollback <id>")
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("checkpoints", flag.ContinueOnError)
	dataDir := fs.String("data", ".agent", "数据目录")
	workspace := fs.String("workspace", "workspace", "工作目录")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	mgr, err := agentrt.NewCheckpointManager(*dataDir, *workspace, 20)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		list, err := mgr.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("（还没有快照）")
			return nil
		}
		for _, c := range list {
			fmt.Printf("%s  %s  %d 个文件 / %d 字节  %s\n", c.ID, c.Label, c.Files, c.Bytes,
				time.UnixMilli(c.CreatedAt).Format("2006-01-02 15:04:05"))
		}
		return nil
	case "rollback":
		id := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if id == "" {
			return errors.New("用法：agent checkpoints rollback <id>")
		}
		if err := mgr.Rollback(id); err != nil {
			return err
		}
		fmt.Printf("已回滚到快照 %s（工作目录 %s）\n", id, *workspace)
		return nil
	}
	return fmt.Errorf("未知 checkpoints 子命令：%s（可用 list | rollback）", sub)
}

/* ---------- mcp ---------- */

func cmdMCP(argv []string) error {
	if len(argv) == 0 {
		return errors.New("用法：agent mcp list|add|remove|reload|call …")
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	var (
		dataDir   = fs.String("data", ".agent", "数据目录（含 config.json）")
		name      = fs.String("name", "", "服务名")
		transport = fs.String("transport", "", "stdio | http")
		command   = fs.String("command", "", "stdio：命令")
		argsLine  = fs.String("args", "", "stdio：参数，空格分隔")
		url       = fs.String("url", "", "http：地址")
		trust     = fs.Bool("trust", false, "完全信任：该服务的工具免审批")
		safe      = fs.String("safe", "", "免审批的只读工具名，逗号分隔")
		enabled   = fs.Bool("enabled", true, "是否启用")
		server    = fs.String("server", "", "call：目标服务名")
		tool      = fs.String("tool", "", "call：远端工具名")
		callArgs  = fs.String("call-args", "{}", "call：参数 JSON")
		timeout   = fs.Int("timeout", 20, "连接/调用超时秒数")
	)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	lg := slog.New(logx.NewHandler(os.Stdout, nil, slog.LevelWarn))

	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}
	mgr := mcp.NewManager(lg, cfg.AllowRemote)
	defer mgr.Close()
	mgr.Apply(cfg.MCPServers) // 先按现有配置连上，list/call/reload 才有东西可用

	switch sub {
	case "list", "status":
		return printMCP(mgr)
	case "add":
		if strings.TrimSpace(*name) == "" {
			return errors.New("add 需要 --name")
		}
		one := mcp.ServerConfig{
			Name: *name, Transport: *transport, Command: *command, URL: *url,
			Enabled: *enabled, AutoApprove: *trust, TimeoutSec: *timeout,
			SafeTools: splitList(*safe),
		}
		if strings.TrimSpace(*argsLine) != "" {
			one.Args = strings.Fields(*argsLine)
		}
		// 能规范化就存规范化后的（transport 之类补全），不能的话也存下来并在状态里报错
		if norm, nerr := one.Normalized(cfg.AllowRemote); nerr == nil {
			one = norm
		}
		saved := false
		for i := range cfg.MCPServers {
			if cfg.MCPServers[i].Name == one.Name {
				cfg.MCPServers[i] = one
				saved = true
				break
			}
		}
		if !saved {
			cfg.MCPServers = append(cfg.MCPServers, one)
		}
		if err := config.Save(*dataDir, cfg); err != nil {
			return err
		}
		mgr.SetAllowRemote(cfg.AllowRemote)
		mgr.Apply(cfg.MCPServers)
		fmt.Printf("已保存并尝试连接 %s：\n", one.Name)
		return printMCP(mgr)
	case "remove":
		if strings.TrimSpace(*name) == "" {
			return errors.New("remove 需要 --name")
		}
		kept := make([]mcp.ServerConfig, 0, len(cfg.MCPServers))
		hit := false
		for _, c := range cfg.MCPServers {
			if c.Name == *name {
				hit = true
				continue
			}
			kept = append(kept, c)
		}
		if !hit {
			return fmt.Errorf("没有这个 MCP 服务：%s", *name)
		}
		cfg.MCPServers = kept
		if err := config.Save(*dataDir, cfg); err != nil {
			return err
		}
		mgr.Apply(cfg.MCPServers)
		fmt.Printf("已下线 %s\n", *name)
		return nil
	case "reload":
		if strings.TrimSpace(*name) == "" {
			mgr.Apply(cfg.MCPServers)
		} else {
			if _, ok := mgr.ServerConfig(*name); !ok {
				return fmt.Errorf("没有这个 MCP 服务：%s", *name)
			}
			mgr.Reload(*name)
		}
		fmt.Println("已重连：")
		return printMCP(mgr)
	case "call":
		if strings.TrimSpace(*server) == "" || strings.TrimSpace(*tool) == "" {
			return errors.New("call 需要 --server 与 --tool")
		}
		var argMap map[string]any
		if err := json.Unmarshal([]byte(*callArgs), &argMap); err != nil {
			return fmt.Errorf("--call-args 不是合法 JSON：%w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
		defer cancel()
		res, err := mgr.CallTool(ctx, *server, *tool, argMap)
		if err != nil {
			return err
		}
		fmt.Println(res.Text())
		if res.IsError {
			return fmt.Errorf("远端工具 %s 返回了错误", *tool)
		}
		return nil
	}
	return fmt.Errorf("未知 mcp 子命令：%s（可用 list | add | remove | reload | call）", sub)
}

func printMCP(mgr *mcp.Manager) error {
	states := mgr.Status()
	if len(states) == 0 {
		fmt.Println("（还没有配置任何 MCP 服务；用 agent mcp add 加一个）")
		return nil
	}
	for _, s := range states {
		line := fmt.Sprintf("%-16s %-6s %s", s.Name, s.Transport, s.Status)
		switch s.Status {
		case "connected":
			line += fmt.Sprintf("  工具 %d 个", s.ToolCount)
			if s.ServerName != "" {
				line += fmt.Sprintf("（%s %s，协议 %s，耗时 %dms）", s.ServerName, s.ServerVersion, s.Protocol, s.LatencyMs)
			}
			if s.Note != "" {
				line += "｜" + s.Note
			}
		case "error":
			line += "  " + s.Error
		}
		fmt.Println(line)
		if len(s.Tools) > 0 {
			fmt.Println("    工具：" + strings.Join(s.Tools, ", "))
		}
	}
	return nil
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

/* ---------- provider（模型通道） ---------- */

func cmdProvider(argv []string) error {
	if len(argv) == 0 {
		return errors.New("用法：agent provider list|add|remove|test …")
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("provider", flag.ContinueOnError)
	var (
		dataDir     = fs.String("data", ".agent", "数据目录（含 config.json）")
		name        = fs.String("name", "", "通道名")
		protocol    = fs.String("protocol", "openai", "openai | anthropic")
		baseURL     = fs.String("base-url", "", "接口地址，如 https://api.deepseek.com/v1")
		model       = fs.String("model", "", "模型名，如 deepseek-chat")
		apiKey      = fs.String("api-key", "", "API Key（只写进 config.json，不回显）")
		kinds       = fs.String("kinds", "", "该通道负责的任务类型，逗号分隔（留空=通吃）")
		fallback    = fs.Bool("fallback", false, "作为回退通道")
		timeout     = fs.Int("timeout", 120, "超时秒数")
		allowRemote = fs.Bool("allow-remote", false, "同时把 config.json 的 allowRemote 打开（用云端接口时必须）")
		doTest      = fs.Bool("test", false, "加完顺带做一次极小的真实调用验证连通")
	)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	cfg, err := config.Load(*dataDir)
	if err != nil {
		return err
	}

	switch sub {
	case "list", "status":
		printProviders(cfg)
		return nil

	case "add":
		if strings.TrimSpace(*name) == "" {
			return errors.New("add 需要 --name")
		}
		p := config.Provider{
			Name: strings.TrimSpace(*name), Protocol: strings.ToLower(strings.TrimSpace(*protocol)),
			BaseURL: strings.TrimSpace(*baseURL), Model: strings.TrimSpace(*model),
			APIKey: strings.TrimSpace(*apiKey), Kinds: splitList(*kinds),
			Fallback: *fallback, TimeoutSec: *timeout,
		}
		if p.BaseURL == "" {
			return errors.New("add 需要 --base-url（例如 https://api.deepseek.com/v1 或 http://127.0.0.1:11434/v1）")
		}
		if p.Model == "" {
			return errors.New("add 需要 --model（例如 deepseek-chat）")
		}
		if *allowRemote {
			cfg.AllowRemote = true
		}
		// 地址不是本机、又没开 allowRemote：明确拒绝并说清怎么办，不留"看着配好了其实调不通"
		router := llm.NewRouter(cfg.AllowRemote)
		if err := router.CheckEndpoint(llm.Config{Name: p.Name, BaseURL: p.BaseURL}); err != nil {
			return err
		}
		replaced := false
		for i := range cfg.Providers {
			if cfg.Providers[i].Name == p.Name {
				if p.APIKey == "" {
					p.APIKey = cfg.Providers[i].APIKey // 不传 key 就是"只改其他字段"，不把已有的 key 清掉
				}
				cfg.Providers[i] = p
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Providers = append(cfg.Providers, p)
		}
		if err := config.Save(*dataDir, cfg); err != nil {
			return err
		}
		fmt.Printf("已保存模型通道 %s（%s，模型 %s，allowRemote=%v）\n",
			p.Name, p.BaseURL, p.Model, cfg.AllowRemote)
		printProviders(cfg)
		if *doTest {
			return testProvider(context.Background(), cfg, p.Name)
		}
		return nil

	case "remove":
		if strings.TrimSpace(*name) == "" {
			return errors.New("remove 需要 --name")
		}
		kept := make([]config.Provider, 0, len(cfg.Providers))
		hit := false
		for _, p := range cfg.Providers {
			if p.Name == *name {
				hit = true
				continue
			}
			kept = append(kept, p)
		}
		if !hit {
			return fmt.Errorf("没有这个模型通道：%s", *name)
		}
		cfg.Providers = kept
		if err := config.Save(*dataDir, cfg); err != nil {
			return err
		}
		fmt.Printf("已删除模型通道 %s\n", *name)
		printProviders(cfg)
		return nil

	case "test":
		if strings.TrimSpace(*name) != "" {
			return testProvider(context.Background(), cfg, *name)
		}
		if len(cfg.Providers) == 0 {
			return errors.New("还没有配置任何模型通道")
		}
		for _, p := range cfg.Providers {
			if err := testProvider(context.Background(), cfg, p.Name); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("未知 provider 子命令：%s（可用 list | add | remove | test）", sub)
}

// printProviders 列出通道状态（key 只显示是否配置，绝不回显）
func printProviders(cfg config.Config) {
	if len(cfg.Providers) == 0 {
		fmt.Println("（还没有配置模型通道；用 agent provider add 加一个）")
		return
	}
	fmt.Printf("allowRemote=%v\n", cfg.AllowRemote)
	for _, p := range cfg.Providers {
		state := "可用"
		errMsg := ""
		switch {
		case strings.TrimSpace(p.BaseURL) == "":
			errMsg = "缺少 baseUrl"
		case strings.TrimSpace(p.Model) == "":
			errMsg = "缺少 model"
		default:
			router := llm.NewRouter(cfg.AllowRemote)
			if err := router.CheckEndpoint(llm.Config{Name: p.Name, BaseURL: p.BaseURL}); err != nil {
				errMsg = err.Error()
			} else if _, err := llm.New(llm.Config{Name: p.Name, Protocol: p.Protocol, BaseURL: p.BaseURL, Model: p.Model}); err != nil {
				errMsg = err.Error()
			}
		}
		if errMsg != "" {
			state = "不可用（" + errMsg + "）"
		}
		kind := "主通道"
		if p.Fallback {
			kind = "回退通道"
		}
		key := "未配置 key"
		if p.APIKey != "" {
			key = "已配置 key"
		}
		fmt.Printf("%-14s %-8s %-6s %-8s %s｜%s｜%s\n",
			p.Name, p.Protocol, kind, key, p.BaseURL, p.Model, state)
	}
}

// testProvider 做一次极小的真实调用：能通就报用量，不能通就报原文错误
func testProvider(ctx context.Context, cfg config.Config, name string) error {
	var target *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name {
			target = &cfg.Providers[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("没有这个模型通道：%s", name)
	}
	router := llm.NewRouter(cfg.AllowRemote)
	lc := llm.Config{
		Name: target.Name, Protocol: target.Protocol, BaseURL: target.BaseURL,
		APIKey: target.APIKey, Model: target.Model, TimeoutSec: target.TimeoutSec,
	}
	if err := router.CheckEndpoint(lc); err != nil {
		return err
	}
	if _, err := llm.New(lc); err != nil {
		return err
	}
	// 尽量省：一句话、限 16 token（逻辑在 llm.Ping，控制台探测用的是同一份）
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := llm.Ping(ctx, lc)
	if err != nil {
		return fmt.Errorf("通道 %s 调用失败：%w", name, err)
	}
	reply := strings.TrimSpace(resp.Text)
	if reply == "" {
		reply = "（模型没有返回文字）"
	}
	fmt.Printf("通道 %s 连通：模型 %s 回复「%s」，用量 %d+%d=%d token，耗时 %dms\n",
		name, resp.Model, reply, resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		resp.Usage.TotalTokens, time.Since(start).Milliseconds())
	return nil
}

/* ---------- backup ---------- */

func cmdBackup(argv []string) error {
	if len(argv) == 0 {
		return errors.New("用法：agent backup create|list|verify|restore|delete|prune …")
	}
	sub, rest := argv[0], argv[1:]
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	var (
		dataDir    = fs.String("data", ".agent", "数据目录")
		out        = fs.String("out", "", "输出文件（create；留空写到 <data>/backups/backup-<时间>.zip）")
		selectSpec = fs.String("select", "all", "备份内容：all 或逗号列表 config,memory,runs,hub,skills,checkpoints,workspace,vault")
		note       = fs.String("note", "", "备注")
		dryRun     = fs.Bool("dry-run", false, "restore：只校验不动数据")
		only       = fs.String("only", "", "restore：只恢复这些相对路径，逗号分隔")
		noSafe     = fs.Bool("no-safepoint", false, "restore：不先打安全点（不建议）")
		keep       = fs.Int("keep", 0, "prune：保留最近几份自动备份")
		asJSON     = fs.Bool("json", false, "JSON 输出")
	)
	if err := fs.Parse(rest); err != nil {
		return err
	}

	switch sub {
	case "create":
		sel, err := parseSelection(*selectSpec)
		if err != nil {
			return err
		}
		man, err := backup.Create(*dataDir, version, *out, sel, *note)
		if err != nil {
			return err
		}
		dest := *out
		if dest == "" {
			dest = "（默认位置）"
			list, lerr := backup.List(*dataDir)
			if lerr == nil && len(list) > 0 {
				dest = list[0].Path
			}
		}
		fmt.Printf("备份已生成：%s\n  文件 %d 个，共 %d 字节\n  内容：%s\n",
			dest, len(man.Entries), man.TotalSize, man.Selection.Label())
		return nil

	case "list":
		list, err := backup.List(*dataDir)
		if err != nil {
			return err
		}
		if *asJSON {
			raw, _ := json.MarshalIndent(list, "", "  ")
			fmt.Println(string(raw))
			return nil
		}
		if len(list) == 0 {
			fmt.Println("（还没有备份）")
			return nil
		}
		for _, a := range list {
			if a.Err != "" {
				fmt.Printf("%-40s 损坏：%s\n", a.Name, a.Err)
				continue
			}
			m := a.Manifest
			fmt.Printf("%-40s %s  %8d 字节  %s\n", a.Name,
				time.UnixMilli(m.CreatedAt).Format("2006-01-02 15:04"), a.Size, m.Selection.Label())
		}
		return nil

	case "verify":
		p, err := backupArchivePath(*dataDir, fs.Args())
		if err != nil {
			return err
		}
		man, err := backup.Verify(p)
		if err != nil {
			return err
		}
		fmt.Printf("校验通过：%s（%d 个文件，共 %d 字节）\n", p, len(man.Entries), man.TotalSize)
		return nil

	case "restore":
		p, err := backupArchivePath(*dataDir, fs.Args())
		if err != nil {
			return err
		}
		res, err := backup.Restore(*dataDir, p, version, backup.RestoreOptions{
			DryRun: *dryRun, NoSafePoint: *noSafe, Only: splitList(*only),
		})
		if err != nil {
			return err
		}
		mode := "已恢复"
		if *dryRun {
			mode = "试恢复通过"
		}
		fmt.Printf("%s：%d 个文件（%d 字节）\n", mode, len(res.Restored), res.Bytes)
		for _, f := range res.Restored {
			fmt.Println("  " + f)
		}
		if res.SafePoint != "" {
			fmt.Println("  安全点：" + res.SafePoint)
		}
		if res.Note != "" {
			fmt.Println("  说明：" + res.Note)
		}
		return nil

	case "delete":
		p, err := backupArchivePath(*dataDir, fs.Args())
		if err != nil {
			return err
		}
		if err := backup.Delete(*dataDir, p); err != nil {
			return err
		}
		fmt.Println("已删除：" + p)
		return nil

	case "prune":
		if *keep <= 0 {
			return errors.New("prune 需要 --keep N")
		}
		removed, err := backup.Prune(*dataDir, *keep)
		if err != nil {
			return err
		}
		if len(removed) == 0 {
			fmt.Println("（没有需要清理的自动备份）")
			return nil
		}
		fmt.Printf("已清理 %d 份：%s\n", len(removed), strings.Join(removed, ", "))
		return nil
	}
	return fmt.Errorf("未知 backup 子命令：%s（可用 create | list | verify | restore | delete | prune）", sub)
}

// parseSelection 解析 --select：all 或逗号列表
func parseSelection(spec string) (backup.Selection, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.EqualFold(spec, "all") {
		return backup.Full(), nil
	}
	sel := backup.Selection{}
	for _, p := range splitList(spec) {
		switch strings.ToLower(p) {
		case "config":
			sel.Config = true
		case "hub", "agent", "tasks":
			sel.Hub = true
		case "memory", "mem":
			sel.Memory = true
		case "runs":
			sel.Runs = true
		case "skills":
			sel.Skills = true
		case "checkpoints", "ck":
			sel.Checkpoints = true
		case "workspace", "ws":
			sel.Workspace = true
		case "vault", "pass":
			sel.Vault = true
		default:
			return sel, fmt.Errorf("不认识的备份项：%s（可选 config,memory,runs,hub,skills,checkpoints,workspace,vault）", p)
		}
	}
	if sel.Empty() {
		return sel, errors.New("--select 至少要选一项")
	}
	return sel, nil
}

// backupArchivePath 把命令行参数解析成备份文件路径：只允许备份目录里的文件
func backupArchivePath(dataDir string, args []string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("需要指定备份文件（可以是文件名，也可以是完整路径）")
	}
	p := strings.TrimSpace(args[0])
	if p == "" {
		return "", errors.New("备份文件名不能为空")
	}
	dir := backup.Dir(dataDir)
	if !strings.ContainsAny(p, `/\`) {
		p = filepath.Join(dir, p)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if filepath.Dir(abs) != absDir {
		return "", errors.New("只允许操作备份目录 " + absDir + " 下的文件")
	}
	return abs, nil
}

/* ---------- 小工具 ---------- */

func briefJSON(v any) string { return briefLine(toJSON(v), 200) }

func toJSON(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}

func briefLine(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
