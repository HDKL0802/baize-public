// Command bzcore 是白泽待办中心的核心内核（Go）：
// 以本地 HTTP 接口的形式，把 core 包的领域能力提供给界面层（手机端 WebView、桌面控制面板）；
// 同时可以连到白泽后端注册成一台"设备"，让后端 Agent 把活派到这台手机上（只读动作）。
//
// 手机端的用法（由 Java 壳拉起，界面通过 127.0.0.1 回环访问）：
//
//	bzcore --data /data/data/com.baize.todo/files/core --addr 127.0.0.1:0 --print-port
//
// 要让后端能调度这台手机，再加上后端地址与配对令牌：
//
//	bzcore --data ... --server http://192.168.1.9:8787 --pair-token <后端令牌> --platform android
//
// 电脑上也可以直接跑，用来联调和排查数据：
//
//	bzcore --data ./data --addr 127.0.0.1:8890 --token dev
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"baize/core"
	"baize/shared/client"
	"baize/shared/proto"
)

func main() {
	var (
		dataDir    = flag.String("data", "data", "数据目录（data.json / settings.json / vault.*.json 都放这里）")
		addr       = flag.String("addr", "127.0.0.1:0", "监听地址；端口 0 表示随机取一个空闲端口")
		tokenFlag  = flag.String("token", "", "本地接口令牌；留空则自动生成并写入 <数据目录>/token")
		printPort  = flag.Bool("print-port", false, "启动后在标准输出打印一行 BZCORE_PORT=<端口>（供壳进程解析）")
		importJSON = flag.String("import-json", "", "首次迁移：从 App 的 localStorage JSON 文件导入（{todos,vault} 或数组）")
		showVer    = flag.Bool("version", false, "打印版本后退出")

		// 跨端调度：连到白泽后端，注册成一台设备
		server     = flag.String("server", "", "白泽后端地址（http://host:port）；留空则只做本地内核，不连后端")
		pairToken  = flag.String("pair-token", "", "后端配对令牌（见后端数据目录的 token 文件）")
		deviceID   = flag.String("device-id", "", "设备 id；留空则用 <数据目录>/device-id（首次自动生成，保证重启不变）")
		deviceName = flag.String("device-name", "", "设备显示名；留空按平台给个默认名")
		platform   = flag.String("platform", "", "对外声明的平台名（交叉编译成 linux 跑在安卓上时建议传 android）；留空用 runtime.GOOS")
		heartbeat  = flag.Duration("heartbeat", 15*time.Second, "心跳间隔")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("bzcore %s（核心协议 core-1）\n", core.Version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fatal("创建数据目录失败：" + err.Error())
	}

	if *importJSON != "" {
		if err := migrateFromJSON(*importJSON, *dataDir); err != nil {
			fatal("导入失败：" + err.Error())
		}
	}

	token, err := resolveToken(*dataDir, *tokenFlag)
	if err != nil {
		fatal(err.Error())
	}

	svc, err := core.NewService(*dataDir, token)
	if err != nil {
		fatal(err.Error())
	}

	// 跨端模式：配了后端地址，正本就在后端知识库（NAS）——
	// 本机只留待办缓存，密码本不落本机。先把本机旧数据迁过去，再挂上远端。
	var remote *core.Remote
	if strings.TrimSpace(*server) != "" {
		remote = core.NewRemote(*server, *pairToken)
		migrateToRemote(svc, remote, *server, *dataDir)
		svc.SetRemote(remote)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal("监听失败：" + err.Error())
	}
	port := ln.Addr().(*net.TCPAddr).Port

	// 跨端连接状态由内核自己上报（谁连的谁最清楚），界面问 /api/device 就行，不用去猜日志
	devState := &deviceState{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/device", deviceStatusHandler(token, devState))
	mux.HandleFunc("GET /api/remote", remoteStatusHandler(token, svc, remote))
	if remote != nil {
		// App 的白泽页、附件读写都走这两条：原样转给后端，令牌不出原生层
		mux.Handle("/api/agent/", remoteProxyHandler(remote))
		mux.Handle("/api/kb/", remoteProxyHandler(remote))
		// 通用透传：/api/be/api/xxx → 后端 /api/xxx。
		// 手机端还要看后端的设备 / 任务与审批 / 日志，而本机内核自己占着 /api/state，
		// 所以统一走这个前缀，免得跟本机自己的接口撞车。
		mux.Handle("/api/be/", http.StripPrefix("/api/be", remoteProxyHandler(remote)))
	}
	mux.Handle("/", svc.Handler())

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.SetFlags(log.Ltime)
	log.Printf("bzcore %s 已启动：http://127.0.0.1:%d/  数据目录 %s", core.Version, port, *dataDir)
	if *printPort {
		fmt.Printf("BZCORE_PORT=%d\n", port)
		os.Stdout.Sync()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// 跨端调度：连后端注册成设备（连不上或令牌不对都不影响本地内核继续服务界面）
	if strings.TrimSpace(*server) != "" {
		go runDeviceClient(ctx, svc, deviceConfig{
			Server: *server, Token: *pairToken, DataDir: *dataDir,
			DeviceID: *deviceID, Name: *deviceName, Platform: *platform, Heartbeat: *heartbeat,
		}, devState)
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fatal("服务异常退出：" + err.Error())
	}
}

/* ---------- 跨端连接状态（界面问它「连上后端没有」） ---------- */

// deviceState 记录跨端客户端的连接情况。
// 内核自己最清楚有没有连上后端，所以由它通过 /api/device 如实上报；
// 界面就不用去猜日志（猜日志很容易出现「明明没连上却显示已连接」）。
type deviceState struct {
	mu        sync.Mutex
	enabled   bool
	server    string
	deviceID  string
	connected bool
	since     int64
	lastError string
}

func (d *deviceState) begin(server, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled, d.server, d.deviceID = true, server, id
	d.connected, d.since, d.lastError = false, 0, ""
}

func (d *deviceState) online() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connected, d.since, d.lastError = true, time.Now().UnixMilli(), ""
}

// offline 记下断开与原因（原因空着表示只是正常断开，保留上一次的错误信息）
func (d *deviceState) offline(why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connected, d.since = false, 0
	if why != "" {
		d.lastError = why
	}
}

func (d *deviceState) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{
		"enabled": d.enabled, "server": d.server, "deviceId": d.deviceID,
		"connected": d.connected, "connectedAt": d.since, "lastError": d.lastError,
		"actions": core.DeviceActionList(), "caps": core.DeviceCaps(),
	}
}

// deviceStatusHandler 本机回环的状态查询，令牌口径与其它接口一致
func deviceStatusHandler(token string, d *deviceState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			tk := r.Header.Get("X-Baize-Token")
			if tk == "" {
				tk = r.URL.Query().Get("token")
			}
			if tk != token {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]any{"ok": false, "error": "配对令牌不正确"})
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true, "data": d.snapshot()})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

/* ---------- 跨端：正本迁到后端知识库 ---------- */

// migrateToRemote 首次把本机数据搬进后端知识库，**只做一次**（成功后落一个标记文件）。
//
// 为什么必须落标记、不能只看"后端为空"：跨端模式下本机**本来就会存一份后端待办缓存**
// （remoteSnapshot → saveTodoCache），所以"本机有数据"在迁移完成之后依然成立。
// 只按"后端为空"判断的话，一旦后端被清空（用户把待办全删了 / 换了个空库），
// 下一次内核启动就会把这份**过期缓存**当成"本机真实数据"迁回后端 —— 已删的条目集体复活。
// （实测踩到过：清空后端后，内核一重启就把缓存里的待办迁了回去。）
//
// 迁移失败不阻断启动：本地内核照旧服务界面，只是还没同步（原因会打到日志里），
// 下次启动会再试（因为没落标记）。
func migrateToRemote(svc *core.Service, remote *core.Remote, server, dataDir string) {
	lg := slog.Default()
	marker := filepath.Join(dataDir, "migrated-to-remote")
	if _, err := os.Stat(marker); err == nil {
		return // 已经迁过（或已确认后端本来就有数据），不再重复
	}
	local, err := svc.LocalSnapshot()
	if err != nil {
		lg.Warn("读取本机数据失败，跳过首次迁移", "err", err)
		return
	}
	remoteSnap, err := remote.State()
	if err != nil {
		lg.Warn("后端知识库暂时连不上，先继续用本机数据（联网后重启内核会自动迁移）",
			"server", server, "err", err)
		return
	}
	if len(remoteSnap.Todos) > 0 || len(remoteSnap.Vault) > 0 || remoteSnap.HasMasterPwd {
		lg.Info("后端知识库已有数据，不做首次迁移", "server", server,
			"todos", len(remoteSnap.Todos), "vault", len(remoteSnap.Vault))
		_ = os.WriteFile(marker, []byte("remote already has data\n"), 0o600)
		return
	}
	if len(local.Todos) == 0 && len(local.Vault) == 0 {
		_ = os.WriteFile(marker, []byte("nothing to migrate\n"), 0o600)
		return
	}
	if local.VaultLocked && len(local.Vault) == 0 && local.HasMasterPwd {
		lg.Warn("本机密码本处于锁定状态，这次只迁移待办；解锁后再重启内核即可把密码也搬过去")
	}
	res, err := svc.Import(remote, local.Todos, local.Vault)
	if err != nil {
		lg.Error("首次迁移到后端知识库失败", "err", err)
		return
	}
	lg.Info("首次迁移完成：本机数据已搬进后端知识库", "server", server, "result", res)
	_ = os.WriteFile(marker, []byte("migrated\n"), 0o600)
}

// remoteStatusHandler 告诉界面「后端知识库通不通 / 现在读的是不是正本」
func remoteStatusHandler(token string, svc *core.Service, r *core.Remote) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if token != "" {
			tk := req.Header.Get("X-Baize-Token")
			if tk == "" {
				tk = req.URL.Query().Get("token")
			}
			if tk != token {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]any{"ok": false, "error": "配对令牌不正确"})
				return
			}
		}
		snap, err := svc.Snapshot()
		out := map[string]any{
			"enabled": r != nil,
			"remote":  snap.Remote,
			"stale":   snap.Stale,
			"reason":  snap.StaleReason,
			"cachedAt": snap.CachedAt,
			"todos":   len(snap.Todos),
			"vault":   len(snap.Vault),
		}
		if r != nil {
			out["server"] = r.Server()
		}
		if err != nil {
			out["error"] = err.Error()
		}
		writeJSON(w, map[string]any{"ok": true, "data": out})
	}
}

// remoteProxyHandler 把 App 发来的 /api/agent/... 与 /api/kb/... 原样转给后端。
// 前端拿不到后端令牌，所有鉴权都在这层完成。
func remoteProxyHandler(r *core.Remote) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// 附件走 base64，请求体比普通接口大得多（16MB 的文件 → 约 21MB），上限要单独给。
		// 注意 /api/be/ 前缀的通用透传也走这里，所以按"路径里含不含 kb/files"判断。
		limit := int64(2 << 20)
		if strings.Contains(req.URL.Path, "/kb/files") {
			limit = 48 << 20
		}
		// 语音听写：一段几十秒的 16k 单声道 WAV 就有 1~2MB，比普通接口大得多
		if strings.Contains(req.URL.Path, "/agent/voice") {
			limit = 48 << 20
		}
		body, _ := io.ReadAll(io.LimitReader(req.Body, limit))
		path := req.URL.Path
		if req.URL.RawQuery != "" {
			path += "?" + req.URL.RawQuery
		}
		out, code, err := r.Agent(req.Method, path, body, req.Header.Get("Content-Type"))
		if err != nil {
			writeJSONCode(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSONCode(w, code, json.RawMessage(out))
	})
}

// writeJSONCode 带状态码的 JSON 输出（writeJSON 固定 200，代理转发需要透传后端状态码）
func writeJSONCode(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

/* ---------- 跨端设备客户端 ---------- */

type deviceConfig struct {
	Server    string
	Token     string
	DataDir   string
	DeviceID  string
	Name      string
	Platform  string
	Heartbeat time.Duration
}

// runDeviceClient 常驻后台：连后端 → 注册 → 收指令 → 执行 → 回执，断了自动重连
func runDeviceClient(ctx context.Context, svc *core.Service, dc deviceConfig, st *deviceState) {
	lg := slog.Default()
	platform := strings.TrimSpace(dc.Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	host, _ := os.Hostname()
	name := strings.TrimSpace(dc.Name)
	if name == "" {
		name = "手机·待办中心（" + platform + "）"
	}
	id, err := resolveDeviceID(dc.DataDir, dc.DeviceID)
	if err != nil {
		st.offline("准备设备 id 失败：" + err.Error())
		lg.Error("准备设备 id 失败，跨端调度不可用", "err", err)
		return
	}
	st.begin(dc.Server, id)

	cfg := client.Config{
		URL:   dc.Server,
		Token: dc.Token,
		Heartbeat: dc.Heartbeat,
		Logger: lg,
		Info: proto.Hello{
			DeviceID:  id,
			Name:      name,
			OS:        platform,
			Arch:      runtime.GOARCH,
			Hostname:  host,
			Version:   core.Version,
			Caps:      core.DeviceCaps(),
			StartedAt: time.Now().UnixMilli(),
		},
	}
	lg.Info("以设备身份连接白泽后端", "device", id, "server", dc.Server, "caps", cfg.Info.Caps)
	err = client.RunForever(ctx, cfg, deviceCommandHandler(svc, lg), func(sess *client.Session) {
		st.online()
		// 这次会话一断就标记成未连接，界面上立刻能看到「掉了」
		go func() {
			<-sess.Done()
			st.offline("")
		}()
	})
	if err != nil && ctx.Err() == nil {
		st.offline(err.Error())
		lg.Error("跨端连接已停止", "err", err)
	}
}

// deviceCommandHandler 把后端指令转成 core 的只读设备动作
func deviceCommandHandler(svc *core.Service, lg *slog.Logger) client.CommandHandler {
	return func(cmd proto.Command) proto.Receipt {
		res, why := svc.RunDeviceAction(cmd.Action, cmd.Args)
		if why != "" {
			lg.Warn("设备动作执行失败", "task", cmd.TaskID, "action", cmd.Action, "why", why)
			return proto.Receipt{TaskID: cmd.TaskID, Status: proto.ReceiptFailed, Result: res, Error: why}
		}
		return proto.Receipt{TaskID: cmd.TaskID, Status: proto.ReceiptDone, Result: res}
	}
}

// resolveDeviceID 设备 id 的取法：命令行 > <数据目录>/device-id > 新生成并落盘
// （落盘是为了重启/升级后仍是同一台设备，不会在后端多出一堆幽灵设备）
func resolveDeviceID(dataDir, flagID string) (string, error) {
	if s := strings.TrimSpace(flagID); s != "" {
		return s, nil
	}
	path := filepath.Join(dataDir, "device-id")
	if raw, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			return s, nil
		}
	}
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成设备 id 失败：%w", err)
	}
	id := "phone-" + hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写入设备 id 失败：%w", err)
	}
	return id, nil
}


// resolveToken 决定接口令牌：命令行指定 > 数据目录里的 token 文件 > 新生成
func resolveToken(dataDir, flagToken string) (string, error) {
	if t := strings.TrimSpace(flagToken); t != "" {
		return t, nil
	}
	path := filepath.Join(dataDir, "token")
	if raw, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(raw)); t != "" {
			return t, nil
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

// migrateFromJSON 把 App 旧数据（localStorage 里的 JSON）导入到核心数据目录。
// 已有数据时不覆盖，只做合并（同「平台+账号」的密码走历史合并，待办按标题+截止去重）。
func migrateFromJSON(src, dataDir string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	incoming, err := core.ImportJSON(raw)
	if err != nil {
		return err
	}
	st := core.Open(dataDir)
	doc, err := st.Load()
	if err != nil {
		return err
	}
	res := doc.ImportDoc(incoming)
	if err := st.Save(doc); err != nil {
		return err
	}
	log.Printf("迁移完成：待办 +%d（跳过重复 %d），密码 +%d（合并 %d）",
		res.TodoAdded, res.TodoSkipped, res.PassAdded, res.PassMerged)
	return nil
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "错误："+msg)
	os.Exit(1)
}
