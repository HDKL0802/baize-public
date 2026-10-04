package com.baize.todo;

import android.content.Context;
import android.os.Build;
import android.util.Log;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.ArrayList;
import java.util.List;

/**
 * 手机内核（bzcore，Go 写的 linux/arm64 静态二进制）的壳侧管理。
 *
 * 为什么二进制要叫 libbzcore.so 并打进 lib/arm64-v8a/：
 * Android 10 起禁止执行「写进应用数据目录」的可执行文件（W^X），但允许执行从安装包
 * lib/<abi>/ 里解出来的文件。所以 Go 内核以 .so 的名字随安装包一起放，运行时从
 * nativeLibraryDir 拿路径直接 exec。改名的唯一目的就是让安装器把它当原生库解出来。
 *
 * 壳只做三件事：拉起来（带上后端地址/配对令牌）、把它的 stdout 读掉（拿监听端口）、
 * 代界面调它的本机回环接口（配对令牌不出原生层，界面拿不到也不该拿到）。
 */
public class CoreBridge {

    private static final String TAG = "BaizeTodo";
    private static final String BIN = "libbzcore.so";
    private static final String CFG = "core-bridge.json";

    private static CoreBridge shared;

    /** 进程级单例：Activity 重建时不能拉起第二个内核（会在后端多出一台幽灵设备） */
    public static synchronized CoreBridge shared(Context ctx) {
        if (shared == null) shared = new CoreBridge(ctx.getApplicationContext());
        return shared;
    }

    private final Context ctx;

    private boolean enabled = false;
    private String server = "";
    private String pairToken = "";

    private Process proc;
    private volatile int port = 0;
    private volatile String token = "";
    private String lastError = "";
    private volatile boolean wantRunning = false;   // 期望内核在跑（false = 已主动停或没开跨端）
    private Thread monitor;                          // 看护线程：内核崩了就拉起来

    private CoreBridge(Context app) {
        this.ctx = app;
        loadConfig();
    }

    /* ---------------- 路径与配置 ---------------- */

    private File dataDir() { return new File(ctx.getFilesDir(), "core"); }

    /** 内核二进制的实际路径（解包后的原生库目录） */
    private File binFile() { return new File(ctx.getApplicationInfo().nativeLibraryDir, BIN); }

    private File cfgFile() { return new File(ctx.getFilesDir(), CFG); }

    private void loadConfig() {
        try {
            byte[] raw = new byte[(int) cfgFile().length()];
            try (FileInputStream in = new FileInputStream(cfgFile())) {
                int n = in.read(raw);
                if (n <= 0) return;
            }
            JSONObject o = new JSONObject(new String(raw, "UTF-8"));
            enabled = o.optBoolean("enabled", false);
            server = o.optString("server", "");
            pairToken = o.optString("pairToken", "");
        } catch (Throwable ignored) {
            // 没有配置文件 = 还没开过跨端，用默认值即可
        }
    }

    private void saveConfig() {
        try (FileOutputStream out = new FileOutputStream(cfgFile())) {
            JSONObject o = new JSONObject();
            o.put("enabled", enabled);
            o.put("server", server);
            o.put("pairToken", pairToken);
            out.write(o.toString().getBytes("UTF-8"));
        } catch (Throwable t) {
            Log.w(TAG, "写跨端配置失败", t);
        }
    }

    private String deviceName() {
        String model = Build.MODEL == null ? "" : Build.MODEL.trim();
        return model.isEmpty() ? "白泽·手机（android）" : "白泽·手机（" + model + "）";
    }

    /* ---------------- 起停 ---------------- */

    /** 配置里没开跨端就什么都不做（本机仍是一个纯本地待办 App） */
    synchronized void start() {
        if (!enabled) { wantRunning = false; return; }
        wantRunning = true;
        ensureMonitor();
        if (alive(proc)) return;

        File bin = binFile();
        File dir = dataDir();
        if (!bin.isFile()) {
            lastError = "这个安装包里没有手机内核（" + bin.getAbsolutePath() + "），跨端用不了";
            return;
        }
        if (!dir.isDirectory() && !dir.mkdirs()) {
            lastError = "建不了内核数据目录：" + dir.getAbsolutePath();
            return;
        }

        List<String> cmd = new ArrayList<>();
        cmd.add(bin.getAbsolutePath());
        cmd.add("--data");        cmd.add(dir.getAbsolutePath());
        cmd.add("--addr");        cmd.add("127.0.0.1:0");   // 端口让内核自己挑，避免和别人撞
        cmd.add("--print-port");
        cmd.add("--platform");    cmd.add("android");
        cmd.add("--device-name"); cmd.add(deviceName());
        cmd.add("--server");      cmd.add(server);
        cmd.add("--pair-token");  cmd.add(pairToken);

        try {
            ProcessBuilder pb = new ProcessBuilder(cmd);
            pb.directory(dir);
            pb.redirectErrorStream(true);
            proc = pb.start();
            port = 0;
            token = "";
            lastError = "";
            watchOutput(proc);
        } catch (Throwable t) {
            proc = null;
            lastError = "内核拉不起来：" + t;
            Log.e(TAG, "bzcore 启动失败", t);
        }
    }

    synchronized void stop() {
        wantRunning = false;
        Process p = proc;
        proc = null;
        port = 0;
        token = "";
        if (p == null) return;
        try {
            p.destroy();
        } catch (Throwable ignored) {
        }
    }

    /**
     * 看护线程：内核是被 exec 出去的原生子进程，跑着跑着可能自己崩掉
     * （实测遇到过 Go 运行时的 fatal error，属于进程级退出，壳侧无法捕获）。
     * 已查明这类崩溃多发生在「x86 模拟器上经 ARM→x86 翻译层跑 arm64 内核」的场景：
     * 翻译层对原子/内存屏障/信号实现不完整，会随机破坏 Go 运行时状态。
     * 崩了以后 port 还停在旧值，界面每次调用都会连一个没人监听的端口，
     * 于是整个跨端就"死"在那里直到用户手动重启 App。这里每隔几秒看一眼：
     * 只要还期望它跑、而进程已经不在了，就把过期的端口清掉重新拉起来。
     * 只在 wantRunning 为真时动手，所以用户主动 stop()（例如退到后台/关掉跨端）
     * 之后不会被无端复活。
     */
    private void ensureMonitor() {
        if (monitor != null && monitor.isAlive()) return;
        Thread t = new Thread(() -> {
            while (true) {
                try {
                    Thread.sleep(3000);
                } catch (InterruptedException e) {
                    return;
                }
                try {
                    if (!enabled || !wantRunning) continue;
                    if (!alive(proc)) {
                        Log.w(TAG, "内核进程已退出，自动重启");
                        port = 0;
                        token = "";
                        start();
                    }
                } catch (Throwable err) {
                    Log.w(TAG, "内核看护异常", err);
                }
            }
        }, "bzcore-watch");
        t.setDaemon(true);
        monitor = t;
        t.start();
    }

    /** 内核的 stdout/stderr 必须一直读掉（否则管道写满会把内核卡死），顺带把端口捞出来 */
    private void watchOutput(final Process p) {
        Thread t = new Thread(() -> {
            try (BufferedReader r = new BufferedReader(
                    new InputStreamReader(p.getInputStream(), "UTF-8"))) {
                String line;
                while ((line = r.readLine()) != null) {
                    Log.i("bzcore", line);
                    if (line.startsWith("BZCORE_PORT=")) {
                        try {
                            port = Integer.parseInt(line.substring("BZCORE_PORT=".length()).trim());
                        } catch (NumberFormatException ignored) {
                        }
                        token = readTokenFile();
                    }
                }
            } catch (Throwable ignored) {
            }
        }, "bzcore-out");
        t.setDaemon(true);
        t.start();
    }

    private String readTokenFile() {
        File f = new File(dataDir(), "token");
        try (FileInputStream in = new FileInputStream(f)) {
            byte[] raw = new byte[(int) f.length()];
            int n = in.read(raw);
            return n <= 0 ? "" : new String(raw, 0, n, "UTF-8").trim();
        } catch (Throwable t) {
            return "";
        }
    }

    /* ---------------- 给界面看的接口 ---------------- */

    /** 跨端是否开启（CoreService / BootReceiver 据此决定要不要常驻，别去猜日志） */
    synchronized boolean isEnabled() { return enabled; }

    synchronized String statusJson() { return status().toString(); }

    private synchronized JSONObject status() {
        JSONObject o = new JSONObject();
        try {
            o.put("supported", binFile().isFile());
            o.put("enabled", enabled);
            o.put("server", server);
            o.put("pairTokenSet", !pairToken.isEmpty());
            o.put("running", alive(proc));
            o.put("port", port);
            o.put("error", lastError);
        } catch (Throwable ignored) {
        }
        return o;
    }

    /** 保存配置并立即生效；令牌留空 = 只改其它字段、保留原令牌（跟模型通道一个口径） */
    synchronized String configureJson(String srv, String tk, boolean on) {
        server = srv == null ? "" : srv.trim();
        if (tk != null && !tk.trim().isEmpty()) pairToken = tk.trim();
        enabled = on;
        if (enabled && server.isEmpty()) {
            enabled = false;
            lastError = "要连后端得先填后端地址（例如 http://192.168.1.9:8787）";
        } else {
            lastError = "";
        }
        saveConfig();
        stop();
        if (enabled) start();
        return status().toString();
    }

    /**
     * 代界面调内核的本机回环接口，返回内核的原始 JSON（形如 {"ok":true,"data":{...}}）。
     * 走原生层是为了绕开 WebView 的跨源限制，也让配对令牌只留在原生这边。
     */
    String call(String path, String method, String body) {
        int p = port;
        // 端口是 0（还没起来）或进程已经不在了（崩过、端口是旧的）：
        // 清掉过期状态、顺手拉一次，别让界面一直往一个没人监听的端口上撞。
        if (p <= 0 || !alive(proc)) {
            if (enabled && wantRunning) {
                port = 0;
                token = "";
                start();
            }
            return errJson(lastError.isEmpty() ? "内核正在启动或重启，稍等一两秒再试" : lastError);
        }
        HttpURLConnection c = null;
        try {
            URL u = new URL("http://127.0.0.1:" + p + (path == null || path.isEmpty() ? "/" : path));
            c = (HttpURLConnection) u.openConnection();
            c.setConnectTimeout(2500);
            // 读超时按用途给：附件上传/下载和 Agent 派活都可能跑一会儿，
            // 用默认那 4 秒会把正常请求掐死；其它接口仍然要快速失败。
            c.setReadTimeout(readTimeoutFor(path, method));
            c.setRequestMethod(method == null || method.isEmpty() ? "GET" : method.toUpperCase());
            String tk = token;
            if (!tk.isEmpty()) c.setRequestProperty("X-Baize-Token", tk);
            if (body != null) {
                c.setDoOutput(true);
                c.setRequestProperty("Content-Type", "application/json; charset=utf-8");
                try (OutputStream os = c.getOutputStream()) {
                    os.write(body.getBytes("UTF-8"));
                }
            }
            int code = c.getResponseCode();
            InputStream in = code >= 400 ? c.getErrorStream() : c.getInputStream();
            String text = in == null ? "" : readAll(in);
            if (text.isEmpty()) return errJson("内核返回了空响应（HTTP " + code + "）");
            return text;
        } catch (Throwable t) {
            return errJson("调内核失败：" + t);
        } finally {
            if (c != null) c.disconnect();
        }
    }

    /** 按路径/方法给读超时（毫秒） */
    private static int readTimeoutFor(String path, String method) {
        String p = path == null ? "" : path;
        String m = method == null ? "GET" : method.toUpperCase();
        if (p.startsWith("/api/kb/files")) return 180000;          // 附件可能几 MB，走 NAS 要时间
        if (p.startsWith("/api/agent/")) return 300000;            // Agent 派活/轮询可能等很久
        if (p.startsWith("/api/op") || p.startsWith("/api/state")) return 20000;  // 要经后端知识库
        return m.equals("POST") ? 20000 : 8000;
    }

    private static String errJson(String msg) {
        try {
            JSONObject o = new JSONObject();
            o.put("ok", false);
            o.put("error", msg);
            return o.toString();
        } catch (Throwable t) {
            return "{\"ok\":false,\"error\":\"未知错误\"}";
        }
    }

    /** 进程还活着吗。不用 Process.isAlive()：那个 API 26 才有，本包 minSdk 24 */
    private static boolean alive(Process p) {
        if (p == null) return false;
        try {
            p.exitValue();   // 还在跑会抛 IllegalThreadStateException
            return false;
        } catch (IllegalThreadStateException e) {
            return true;
        } catch (Throwable t) {
            return false;
        }
    }

    private static String readAll(InputStream in) throws Exception {
        StringBuilder sb = new StringBuilder();
        try (BufferedReader r = new BufferedReader(new InputStreamReader(in, "UTF-8"))) {
            String line;
            while ((line = r.readLine()) != null) sb.append(line).append('\n');
        }
        return sb.toString().trim();
    }
}
