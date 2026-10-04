package com.baize.todo;

import android.content.Context;
import android.content.res.AssetManager;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * 极简本地 HTTP 服务：把 APK assets 里的前端资源以 http://127.0.0.1:<port> 暴露给 WebView。
 * 之所以不直接用 file:// 或 WebViewAssetLoader，是因为：
 *   - file:// 不是安全上下文，crypto.subtle（密码本 AES-GCM）与语音 API 会失效；
 *   - 127.0.0.1 属于「potentially trustworthy」，算安全上下文，且无需引入 AndroidX 依赖。
 *
 * 端口必须是「固定的」：WebView 的 localStorage 按 origin（含端口）隔离，
 * 端口一变数据就全部看不见了。所以首次运行把端口存进 SharedPreferences，之后永远沿用；
 * 主界面与桌面小组件的快速录入页也因此处于同一个 origin，共用同一份数据。
 */
public class LocalServer {

    private static final String TAG = "BaizeTodo";
    private static final String PREF = "bz_http";
    private static final String KEY_PORT = "port";
    private static final int FALLBACK_PORT = 47411;
    private static final int[] EXTRA_PORTS = { 47412, 47413, 47414 };

    private static LocalServer shared;

    public static synchronized LocalServer shared(Context ctx) throws IOException {
        if (shared == null || shared.server == null || shared.server.isClosed()) {
            Context app = ctx.getApplicationContext();
            shared = new LocalServer(app.getAssets());
            int port = shared.start(resolvePort(app));
            // 记录「实际绑上的端口」而不是「想用的端口」，两者不一致时以实际为准
            app.getSharedPreferences(PREF, Context.MODE_PRIVATE)
                    .edit().putInt(KEY_PORT, port).apply();
        }
        return shared;
    }

    /** 已固定的端口；首次运行返回默认端口（实际绑定结果由 {@link #shared} 写回偏好设置） */
    static synchronized int resolvePort(Context ctx) {
        int saved = ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE).getInt(KEY_PORT, 0);
        if (saved > 0) return saved;
        Log.i(TAG, "first run, local server port -> " + FALLBACK_PORT);
        return FALLBACK_PORT;
    }

    private final AssetManager assets;
    private final ExecutorService pool = Executors.newFixedThreadPool(4);
    private volatile boolean running = true;
    private ServerSocket server;
    private int port = -1;

    public LocalServer(AssetManager assets) {
        this.assets = assets;
    }

    /** 启动服务并返回实际监听端口（优先用固定端口，被占用再退到备用端口） */
    public int start(int preferred) throws IOException {
        int[] candidates = { preferred, FALLBACK_PORT, EXTRA_PORTS[0], EXTRA_PORTS[1], EXTRA_PORTS[2] };
        for (int p : candidates) {
            if (p <= 0) continue;
            try {
                server = new ServerSocket(p, 50, InetAddress.getByName("127.0.0.1"));
                break;
            } catch (IOException ignored) {
                // 端口被占，试下一个
            }
        }
        if (server == null) {
            server = new ServerSocket(0, 50, InetAddress.getByName("127.0.0.1"));
        }
        port = server.getLocalPort();
        Thread t = new Thread(this::acceptLoop, "bz-http");
        t.setDaemon(true);
        t.start();
        return port;
    }

    public int getPort() {
        return port;
    }

    public void stop() {
        running = false;
        try {
            if (server != null) server.close();
        } catch (IOException ignored) {
        }
        pool.shutdownNow();
    }

    private void acceptLoop() {
        while (running) {
            try {
                final Socket socket = server.accept();
                pool.execute(() -> handle(socket));
            } catch (IOException e) {
                if (!running) return;
            }
        }
    }

    private void handle(Socket socket) {
        try (Socket s = socket) {
            s.setSoTimeout(15000);
            InputStream in = s.getInputStream();
            OutputStream out = s.getOutputStream();

            String requestLine = readLine(in);
            if (requestLine == null) return;
            String headerLine;
            while ((headerLine = readLine(in)) != null && !headerLine.isEmpty()) {
                // 请求头无需处理
            }

            String[] parts = requestLine.split(" ");
            if (parts.length < 2) {
                send(out, 400, "text/plain", "Bad Request".getBytes("UTF-8"), false);
                return;
            }
            String method = parts[0];
            boolean headOnly = "HEAD".equals(method);
            if (!"GET".equals(method) && !headOnly) {
                send(out, 405, "text/plain", "Method Not Allowed".getBytes("UTF-8"), false);
                return;
            }

            String path = parts[1];
            int cut = path.indexOf('?');
            if (cut >= 0) path = path.substring(0, cut);
            cut = path.indexOf('#');
            if (cut >= 0) path = path.substring(0, cut);
            if (path.isEmpty() || path.equals("/")) path = "/index.html";
            if (path.startsWith("/")) path = path.substring(1);

            if (path.contains("..")) {
                send(out, 403, "text/plain", "Forbidden".getBytes("UTF-8"), false);
                return;
            }

            byte[] body;
            try (InputStream is = assets.open(path, AssetManager.ACCESS_STREAMING)) {
                body = readAll(is);
            } catch (IOException notFound) {
                send(out, 404, "text/plain", ("Not Found: " + path).getBytes("UTF-8"), headOnly);
                return;
            }
            send(out, 200, mimeOf(path), body, headOnly);
        } catch (Exception ignored) {
            // 单次请求失败不影响服务
        }
    }

    private void send(OutputStream out, int code, String type, byte[] body, boolean headOnly) throws IOException {
        StringBuilder sb = new StringBuilder();
        sb.append("HTTP/1.1 ").append(code).append(' ').append(reason(code)).append("\r\n");
        sb.append("Content-Type: ").append(type).append("\r\n");
        sb.append("Content-Length: ").append(body.length).append("\r\n");
        sb.append("Cache-Control: no-store, no-cache, must-revalidate\r\n");
        sb.append("Pragma: no-cache\r\n");
        sb.append("Connection: close\r\n");
        sb.append("\r\n");
        out.write(sb.toString().getBytes("UTF-8"));
        if (!headOnly) out.write(body);
        out.flush();
    }

    private static String reason(int code) {
        switch (code) {
            case 200: return "OK";
            case 400: return "Bad Request";
            case 403: return "Forbidden";
            case 404: return "Not Found";
            case 405: return "Method Not Allowed";
            default: return "Error";
        }
    }

    private static String mimeOf(String path) {
        String p = path.toLowerCase();
        if (p.endsWith(".html") || p.endsWith(".htm")) return "text/html; charset=utf-8";
        if (p.endsWith(".css")) return "text/css; charset=utf-8";
        if (p.endsWith(".js") || p.endsWith(".mjs")) return "application/javascript; charset=utf-8";
        if (p.endsWith(".json")) return "application/json; charset=utf-8";
        if (p.endsWith(".webmanifest")) return "application/manifest+json; charset=utf-8";
        if (p.endsWith(".svg")) return "image/svg+xml";
        if (p.endsWith(".png")) return "image/png";
        if (p.endsWith(".jpg") || p.endsWith(".jpeg")) return "image/jpeg";
        if (p.endsWith(".webp")) return "image/webp";
        if (p.endsWith(".ico")) return "image/x-icon";
        if (p.endsWith(".woff2")) return "font/woff2";
        if (p.endsWith(".txt") || p.endsWith(".md")) return "text/plain; charset=utf-8";
        return "application/octet-stream";
    }

    private static String readLine(InputStream in) throws IOException {
        ByteArrayOutputStream buf = new ByteArrayOutputStream(128);
        int c;
        while ((c = in.read()) != -1) {
            if (c == '\n') break;
            if (c != '\r') buf.write(c);
        }
        if (c == -1 && buf.size() == 0) return null;
        return new String(buf.toByteArray(), "UTF-8");
    }

    private static byte[] readAll(InputStream in) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream(64 * 1024);
        byte[] buf = new byte[16 * 1024];
        int n;
        while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
        return out.toByteArray();
    }
}
