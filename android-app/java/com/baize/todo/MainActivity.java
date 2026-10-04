package com.baize.todo;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.MediaStore;
import android.view.ViewGroup;
import android.webkit.ConsoleMessage;
import android.webkit.JavascriptInterface;
import android.webkit.PermissionRequest;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.Toast;

import java.util.ArrayList;

/**
 * 待办中心 Android 壳。
 * 前端资源由 LocalServer 通过 http://127.0.0.1 提供（安全上下文）。
 *
 * 语音：说话（朗读）由网页直接用内核源上的 /api/agent/voice/tts 播放，不需要原生代码；
 * 听话（按住说话）由网页 getUserMedia 录音 —— 这里只负责把 RECORD_AUDIO 权限要下来，
 * 再把 WebView 的音频采集请求放行（见 onPermissionRequest）。
 */
public class MainActivity extends Activity {

    private static final int REQ_NOTIFY = 1002;
    private static final int REQ_PICK = 1003;
    /** 网页里要麦克风（按住说话）时弹的系统权限 */
    private static final int REQ_MIC = 1004;
    /** 用户在弹窗里点了「允许」之后，把那次待批的 WebView 请求放行 */
    private android.webkit.PermissionRequest pendingMicRequest = null;

    private WebView web;
    private LocalServer server;
    private CoreBridge coreBridge;
    private String baseUrl = "";
    private String pendingAction = null;
    private String pickedName = "";
    private String pickedText = null;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        pendingAction = getIntent() != null ? getIntent().getAction() : null;

        FrameLayout root = new FrameLayout(this);
        web = new WebView(this);
        root.addView(web, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(root);

        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        s.setSupportZoom(false);
        s.setBuiltInZoomControls(false);
        s.setMediaPlaybackRequiresUserGesture(false);
        s.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        s.setCacheMode(WebSettings.LOAD_NO_CACHE);

        Reminders.ensureChannel(this);
        Reminders.rescheduleFromPrefs(this);

        web.addJavascriptInterface(new Bridge(), "BzNative");

        // 个人自用版：允许 adb / Chrome DevTools 调试 WebView，并把页面里的 JS 报错打到 logcat，
        // 免得界面出问题时只剩一句「用不了」，看不到现场。
        WebView.setWebContentsDebuggingEnabled(true);
        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onConsoleMessage(ConsoleMessage m) {
                android.util.Log.i("bz", "console[" + m.messageLevel() + "] " + m.message()
                        + " @" + m.sourceId() + ":" + m.lineNumber());
                return true;
            }

            /**
             * 网页里申请麦克风（getUserMedia）：先要系统级 RECORD_AUDIO，再把 WebView 这一层放行。
             * 系统权限是异步弹的，所以这里先同步要一次，用户点了「允许」之后下一次录音就能成。
             */
            @Override
            public void onPermissionRequest(final PermissionRequest request) {
                boolean wantsAudio = false;
                for (String res : request.getResources()) {
                    if (PermissionRequest.RESOURCE_AUDIO_CAPTURE.equals(res)) wantsAudio = true;
                }
                if (!wantsAudio) {
                    request.deny();
                    return;
                }
                if (hasRecordPermission()) {
                    runOnUiThread(() -> request.grant(new String[]{PermissionRequest.RESOURCE_AUDIO_CAPTURE}));
                    return;
                }
                pendingMicRequest = request;
                runOnUiThread(() -> requestPermissions(
                        new String[]{android.Manifest.permission.RECORD_AUDIO}, REQ_MIC));
            }
        });

        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri u = request.getUrl();
                String url = u.toString();
                if (!baseUrl.isEmpty() && url.startsWith(baseUrl)) return false;
                // 外部链接交给系统浏览器；intent: 用于系统设置跳转
                try {
                    if (url.startsWith("intent:")) {
                        Intent i = Intent.parseUri(url, Intent.URI_INTENT_SCHEME);
                        startActivity(i);
                    } else {
                        startActivity(new Intent(Intent.ACTION_VIEW, u));
                    }
                } catch (Exception e) {
                    toast("无法打开：" + url);
                }
                return true;
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                if (pendingAction != null) {
                    final String a = pendingAction;
                    pendingAction = null;
                    // 等前端 init 跑完再派发
                    view.postDelayed(() -> dispatchAction(a), 400);
                }
            }
        });

        try {
            server = LocalServer.shared(this);
            baseUrl = "http://127.0.0.1:" + server.getPort() + "/";
            // 跨端设备注册：交给 CoreService 前台常驻（服务自己判断跨端开没开）——
            // 内核不再跟着界面生死，划掉 App / 重启 / 覆盖安装后设备都能自己回到线上
            coreBridge = CoreBridge.shared(this);
            CoreService.start(this);
            web.loadUrl(baseUrl + "index.html");
        } catch (Exception e) {
            web.loadData("<h3 style='font:16px sans-serif;padding:20px'>本地服务启动失败：" + e + "</h3>",
                    "text/html", "utf-8");
        }
    }

    /* ---------------- 原生桥 ---------------- */

    private class Bridge {
        @JavascriptInterface
        public void vibrate(int ms) {
            try {
                android.os.Vibrator v = (android.os.Vibrator) getSystemService(VIBRATOR_SERVICE);
                if (v != null) v.vibrate(ms);
            } catch (Throwable ignored) {
            }
        }

        /* ---------------- 原生通知与提醒 ---------------- */

        @JavascriptInterface
        public boolean notificationsEnabled() {
            try {
                android.app.NotificationManager nm =
                        (android.app.NotificationManager) getSystemService(NOTIFICATION_SERVICE);
                if (nm == null) return false;
                if (Build.VERSION.SDK_INT >= 24) return nm.areNotificationsEnabled();
                return true;
            } catch (Throwable t) {
                return false;
            }
        }

        @JavascriptInterface
        public void requestNotificationPermission() {
            if (Build.VERSION.SDK_INT >= 33) {
                runOnUiThread(() -> requestPermissions(
                        new String[]{"android.permission.POST_NOTIFICATIONS"}, REQ_NOTIFY));
            }
        }

        /** 前端整体下发需要提醒的待办，原生侧重排精确闹钟 */
        @JavascriptInterface
        public void syncReminders(String json) {
            Reminders.sync(MainActivity.this, json);
        }

        /** 立即发一条通知（设置页「测试通知」用） */
        @JavascriptInterface
        public void notifyNow(String title, String body) {
            Reminders.post(MainActivity.this, "baize-test", title, body);
        }

        @JavascriptInterface
        public boolean exactAlarmAllowed() {
            if (Build.VERSION.SDK_INT < 31) return true;
            try {
                android.app.AlarmManager am =
                        (android.app.AlarmManager) getSystemService(ALARM_SERVICE);
                return am == null || am.canScheduleExactAlarms();
            } catch (Throwable t) {
                return true;
            }
        }

        /* ---------------- 文件导入导出 ----------------
           WebView 里的 <a download> 和 <input type=file> 在壳里都不工作，
           所以导出走 MediaStore 写「下载」目录，导入走系统文件选择器。 */

        /** 写一个文本文件到系统「下载」目录，返回可读的位置说明；失败返回空串 */
        @JavascriptInterface
        public String saveTextFile(String name, String content) {
            try {
                String safe = (name == null || name.trim().isEmpty())
                        ? "baize-export.md" : name.replaceAll("[\\\\/:*?\"<>|]", "_");
                byte[] bytes = (content == null ? "" : content).getBytes("UTF-8");
                if (Build.VERSION.SDK_INT >= 29) {
                    android.content.ContentValues cv = new android.content.ContentValues();
                    cv.put(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, safe);
                    cv.put(android.provider.MediaStore.MediaColumns.MIME_TYPE, "text/markdown");
                    cv.put(android.provider.MediaStore.MediaColumns.RELATIVE_PATH,
                            android.os.Environment.DIRECTORY_DOWNLOADS);
                    Uri uri = getContentResolver().insert(
                            android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, cv);
                    if (uri == null) return "";
                    java.io.OutputStream os = getContentResolver().openOutputStream(uri);
                    if (os == null) return "";
                    os.write(bytes);
                    os.flush();
                    os.close();
                    return "下载/" + safe;
                }
                java.io.File dir = getExternalFilesDir(null);
                if (dir == null) dir = getFilesDir();
                java.io.File f = new java.io.File(dir, safe);
                java.io.FileOutputStream fos = new java.io.FileOutputStream(f);
                fos.write(bytes);
                fos.flush();
                fos.close();
                return f.getAbsolutePath();
            } catch (Throwable t) {
                return "";
            }
        }

        /** 打开系统文件选择器；选完通过 window.__bzFileContent(null, name) 通知前端来取内容 */
        @JavascriptInterface
        public void pickTextFile() {
            runOnUiThread(() -> {
                try {
                    Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT);
                    i.addCategory(Intent.CATEGORY_OPENABLE);
                    // 不加 EXTRA_MIME_TYPES 白名单：加了以后 .md/.csv 之类的文件在选择器里会变灰选不中
                    i.setType("*/*");
                    startActivityForResult(i, REQ_PICK);
                } catch (Throwable t) {
                    toast("无法打开文件选择器：" + t.getMessage());
                }
            });
        }

        /** 取走刚选中的文件内容（只取一次，取完即清，避免超长字符串走 JS 转义） */
        @JavascriptInterface
        public String takePickedText() {
            String t = pickedText;
            pickedText = null;
            return t == null ? "" : t;
        }

        /* ---------------- 对话附件：拍照 / 相册 / 文件 ----------------
           WebView 里的 <input type=file> 在壳内不能用，所以走原生：
           source = camera | album | file，取好后由前端调 takeAttachment() 取走。 */

        @JavascriptInterface
        public void pickAttachment(String source) {
            runOnUiThread(() -> startPick(source));
        }

        /** 待办附件：选好后原生把文件复制进 App 私有目录（原文件删了也不影响），
            只把「引用 + 缩略图」给前端，不占 localStorage 配额 */
        @JavascriptInterface
        public void pickAttachmentFor(String source) {
            runOnUiThread(() -> { keepPick = true; keepDest = "todo"; startPick(source); });
        }

        /** 知识库「附件」页专用：选好后同样复制进私有目录，但 dest 标成 kb，
            附件页认领后直接传到 NAS 再把本机那份删掉（手机上不留正本）。 */
        @JavascriptInterface
        public void pickKbAttachment(String source) {
            runOnUiThread(() -> { keepPick = true; keepDest = "kb"; startPick(source); });
        }

        /** 打开附件：交给系统应用看（相册 / WPS / 阅读器等），走自带的 AttProvider 外发 */
        @JavascriptInterface
        public void openAttachment(String path) {
            runOnUiThread(() -> openAttachmentNow(path));
        }

        /** 另存一份到系统「下载」目录 */
        @JavascriptInterface
        public void saveAttachment(String path) {
            runOnUiThread(() -> saveAttachmentNow(path));
        }

        /** 删除附件（只删 App 私有目录里的那一份） */
        @JavascriptInterface
        public void deleteAttachment(String path) {
            try {
                java.io.File f = attFile(path);
                if (f != null && f.isFile()) f.delete();
            } catch (Throwable ignored) {
            }
        }

        /** 读回图片内容（App 内全屏预览用）：返回 base64，失败给空串 */
        @JavascriptInterface
        public String readAttachmentBase64(String path) {
            try {
                java.io.File f = attFile(path);
                if (f == null || !f.isFile()) return "";
                String b64 = imageToBase64(Uri.fromFile(f), 1600, 85);
                return b64 == null ? "" : b64;
            } catch (Throwable t) {
                return "";
            }
        }

        /** 前端来取刚选好的附件（取完即清）；空字符串代表这次没有 */
        @JavascriptInterface
        public String takeAttachment() {
            String s = pendingAttachment;
            pendingAttachment = null;
            return s == null ? "" : s;
        }

        /** 按用途来取：dest = chat（对话附件）| todo（待办附件）。
            两边都在等这个信号，只按类型取走自己的那份，否则会互相抢。 */
        @JavascriptInterface
        public String takeAttachmentFor(String dest) {
            String s = pendingAttachment;
            if (s == null || s.isEmpty()) return "";
            String d = "";
            try {
                d = new org.json.JSONObject(s).optString("dest", "chat");
            } catch (Throwable t) {
                d = "chat";
            }
            if (!d.equals(dest == null ? "" : dest)) return "";
            pendingAttachment = null;
            return s;
        }

        /* ---------------- 直接 @路径 读文件（电脑上 Agent 的玩法） ---------------- */

        /** 有没有"所有文件访问"权限 */
        @JavascriptInterface
        public boolean hasAllFiles() {
            if (Build.VERSION.SDK_INT < 30) return true;
            try {
                return android.os.Environment.isExternalStorageManager();
            } catch (Throwable t) {
                return false;
            }
        }

        /** 跳到系统那个开关（用户开一次就行） */
        @JavascriptInterface
        public void openAllFilesSetting() {
            runOnUiThread(() -> {
                try {
                    Intent i = new Intent("android.settings.MANAGE_APP_ALL_FILES_ACCESS_PERMISSION");
                    i.setData(Uri.parse("package:" + getPackageName()));
                    startActivity(i);
                } catch (Throwable t) {
                    try {
                        startActivity(new Intent("android.settings.MANAGE_ALL_FILES_ACCESS_PERMISSION"));
                    } catch (Throwable t2) {
                        toast("请到 系统设置 → 应用 → 特殊访问权限 里开启");
                    }
                }
            });
        }

        /** 按路径读一个文件/文件夹：结果和附件走同一条路（前端来取） */
        @JavascriptInterface
        public void attachPath(String path) {
            runOnUiThread(() -> {
                try {
                    String p = (path == null ? "" : path.trim())
                            .replace("~", System.getProperty("user.home", ""))
                            .replace("\\", "/");
                    // 家里常写的简写：/sdcard、/download 之类
                    if (p.startsWith("/sdcard")) p = p.replaceFirst("^/sdcard", android.os.Environment.getExternalStorageDirectory().getAbsolutePath());
                    java.io.File f = new java.io.File(p);
                    if (!f.exists()) { attachError("找不到这个路径：" + p); return; }
                    if (f.isDirectory()) { attachFolder(f); return; }
                    deliverFile(f);
                } catch (Throwable t) {
                    attachError("读路径失败：" + t.getMessage());
                }
            });
        }

        /* ---------------- 跨端：把这台手机注册成白泽后端的设备 ---------------- */

        /** 内核状态：{supported, enabled, running, port, error, server, pairTokenSet} */
        @JavascriptInterface
        public String bzCoreStatus() {
            return coreBridge == null ? "{}" : coreBridge.statusJson();
        }

        /** 配置后端地址与配对令牌并立即重启内核；令牌留空 = 不改（不把已存的令牌回显出来） */
        @JavascriptInterface
        public String bzCoreConfigure(String server, String pairToken, boolean enabled) {
            if (coreBridge == null) return "{\"error\":\"本机没有内核管理器\"}";
            String st = coreBridge.configureJson(server, pairToken, enabled);
            // 开关联动常驻服务：开了就拉起常驻，关了把服务连同内核一起收掉
            if (enabled) CoreService.start(MainActivity.this);
            else CoreService.stop(MainActivity.this);
            return st;
        }

        /** 代界面调内核的本机接口（走原生层绕开跨源限制），返回内核原始 JSON */
        @JavascriptInterface
        public String bzCoreCall(String path, String method, String body) {
            return coreBridge == null ? "" : coreBridge.call(path, method, body);
        }

        /* ---------------- 附件正本放 NAS（手机上不留文件） ----------------
           二进制一律不过 JS 桥（大 base64 字符串回传会静默失败），
           所以读文件、编码、发请求、落盘全在这边做完，只把小结果/本地路径给前端。 */

        /** 把附件上传到后端知识库。成功回 {ok:true,file:{id,name,…}}，失败回 {ok:false,error} */
        @JavascriptInterface
        public String kbUploadAttachment(String path, String name, String kind, String mime) {
            try {
                java.io.File f = attFile(path);
                if (f == null || !f.isFile()) return kbErr("找不到本机附件文件（可能已被清理）");
                byte[] data = readAllBytes(f);
                if (data == null || data.length == 0) return kbErr("附件是空的");
                if (data.length > 16L * 1024 * 1024) return kbErr("附件超过 16MB，先压一压再传");
                if (coreBridge == null) return kbErr("本机没有内核管理器");
                org.json.JSONObject body = new org.json.JSONObject();
                body.put("name", (name == null || name.trim().isEmpty()) ? f.getName() : name.trim());
                body.put("kind", (kind == null || kind.isEmpty()) ? "file" : kind);
                body.put("mime", mime == null ? "" : mime);
                body.put("dataBase64", android.util.Base64.encodeToString(data, android.util.Base64.NO_WRAP));
                String out = coreBridge.call("/api/kb/files", "POST", body.toString());
                if (out == null || out.isEmpty()) return kbErr("内核没有响应（可能还没起来）");
                return out;
            } catch (Throwable t) {
                return kbErr("上传到 NAS 失败：" + t.getMessage());
            }
        }

        /** 从后端知识库把附件取回来落到本机私有目录，返回本地路径（失败给空串） */
        @JavascriptInterface
        public String kbFetchAttachment(String fileId, String name) {
            try {
                if (fileId == null || fileId.trim().isEmpty()) return "";
                if (coreBridge == null) return "";
                String out = coreBridge.call("/api/kb/files/" + fileId.trim(), "GET", null);
                if (out == null || out.isEmpty()) return "";
                org.json.JSONObject o = new org.json.JSONObject(out);
                String b64 = o.optString("dataBase64", "");
                if (b64.isEmpty()) return "";
                byte[] data = android.util.Base64.decode(b64, android.util.Base64.DEFAULT);
                String safe = (name == null || name.trim().isEmpty()) ? "attachment" : name.trim();
                safe = safe.replaceAll("[\\\\/:*?\"<>|]", "_");
                java.io.File f = new java.io.File(attDir(), "kb-" + fileId.trim() + "-" + safe);
                try (java.io.FileOutputStream fos = new java.io.FileOutputStream(f)) {
                    fos.write(data);
                }
                return f.getAbsolutePath();
            } catch (Throwable t) {
                return "";
            }
        }

        /** 从后端知识库删掉附件（连同本机那份临时缓存） */
        @JavascriptInterface
        public String kbDeleteAttachment(String fileId, String localPath) {
            try {
                if (localPath != null && !localPath.isEmpty()) {
                    java.io.File f = attFile(localPath);
                    if (f != null && f.isFile()) f.delete();
                }
                if (fileId == null || fileId.trim().isEmpty() || coreBridge == null) {
                    return "{\"ok\":true,\"data\":{\"localOnly\":true}}";
                }
                String out = coreBridge.call("/api/kb/files/" + fileId.trim(), "DELETE", null);
                return (out == null || out.isEmpty()) ? kbErr("内核没有响应") : out;
            } catch (Throwable t) {
                return kbErr("删除失败：" + t.getMessage());
            }
        }
    }

    private static String kbErr(String msg) {
        try {
            org.json.JSONObject o = new org.json.JSONObject();
            o.put("ok", false);
            o.put("error", msg);
            return o.toString();
        } catch (Throwable t) {
            return "{\"ok\":false,\"error\":\"未知错误\"}";
        }
    }

    /** 读整个文件（minSdk 24 用不了 Files.readAllBytes，手写一遍） */
    private static byte[] readAllBytes(java.io.File f) throws java.io.IOException {
        try (java.io.FileInputStream in = new java.io.FileInputStream(f)) {
            long len = f.length();
            java.io.ByteArrayOutputStream bos = new java.io.ByteArrayOutputStream((int) Math.min(len, 1 << 20));
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) bos.write(buf, 0, n);
            return bos.toByteArray();
        }
    }

    /** 文件夹：列一层清单交给模型看 */
    private void attachFolder(java.io.File dir) {
        java.io.File[] kids = dir.listFiles();
        StringBuilder sb = new StringBuilder();
        sb.append("目录：").append(dir.getAbsolutePath()).append('\n');
        if (kids == null || kids.length == 0) {
            sb.append("（空目录）");
        } else {
            java.util.Arrays.sort(kids, (a, b) -> a.getName().compareToIgnoreCase(b.getName()));
            int n = Math.min(kids.length, 200);
            for (int i = 0; i < n; i++) {
                java.io.File k = kids[i];
                sb.append(k.isDirectory() ? "[目录] " : "       ").append(k.getName())
                  .append(k.isDirectory() ? "" : "  (" + (k.length() / 1024) + "KB)").append('\n');
            }
            if (kids.length > n) sb.append("…还有 ").append(kids.length - n).append(" 项\n");
        }
        attachJson("file", dir.getName() + "/", "inode/directory", null, 0, sb.toString());
    }

    /** 单个文件：图片降采样、文本读正文、其它只给名字 */
    private void deliverFile(java.io.File f) {
        String name = f.getName();
        String lower = name.toLowerCase();
        boolean isImage = lower.endsWith(".jpg") || lower.endsWith(".jpeg") || lower.endsWith(".png")
                || lower.endsWith(".webp") || lower.endsWith(".gif") || lower.endsWith(".heic");
        try {
            if (isImage) {
                String b64 = imageToBase64(Uri.fromFile(f), 1600, 80);
                if (b64 == null) { attachError("这张图解析不了：" + name); return; }
                attachJson("image", name, "image/jpeg", b64, f.length(), null);
                return;
            }
            boolean textLike = lower.endsWith(".md") || lower.endsWith(".markdown") || lower.endsWith(".txt")
                    || lower.endsWith(".json") || lower.endsWith(".csv") || lower.endsWith(".log")
                    || lower.endsWith(".js") || lower.endsWith(".ts") || lower.endsWith(".html")
                    || lower.endsWith(".yml") || lower.endsWith(".yaml") || lower.endsWith(".xml");
            if (!textLike || f.length() > 512 * 1024) {
                attachJson("file", name, "", null, f.length(), null);
                return;
            }
            java.io.InputStream is = new java.io.FileInputStream(f);
            byte[] all = readAll(is);
            is.close();
            attachJson("file", name, "text/plain", null, f.length(), new String(all, "UTF-8"));
        } catch (Throwable t) {
            attachError("读文件失败：" + t.getMessage());
        }
    }

    private static final int REQ_CAMERA = 1010;
    private static final int REQ_ALBUM = 1011;
    private static final int REQ_ATTACH_FILE = 1012;
    private Uri cameraOut = null;
    /** 本次选择是不是"待办附件"（要复制进私有目录），由 pickAttachmentFor 置位 */
    private boolean keepPick = false;
    /** 这批附件归谁认领：todo（待办表单）| kb（知识库附件页）。前端按 dest 取走自己那份。 */
    private String keepDest = "todo";

    private void startPick(String source) {
        try {
            if ("camera".equals(source)) {
                Intent i = new Intent(MediaStore.ACTION_IMAGE_CAPTURE);
                // 不再用 resolveActivity 做前置判断：Android 11+ 包可见性会让它误报"没有相机"
                if (Build.VERSION.SDK_INT >= 29) {
                    android.content.ContentValues cv = new android.content.ContentValues();
                    cv.put(MediaStore.MediaColumns.DISPLAY_NAME, "bz-" + System.currentTimeMillis() + ".jpg");
                    cv.put(MediaStore.MediaColumns.MIME_TYPE, "image/jpeg");
                    cv.put(MediaStore.MediaColumns.RELATIVE_PATH, android.os.Environment.DIRECTORY_PICTURES + "/白泽");
                    try {
                        cameraOut = getContentResolver().insert(
                                MediaStore.Images.Media.EXTERNAL_CONTENT_URI, cv);
                    } catch (Throwable ignored) {
                        cameraOut = null;
                    }
                    if (cameraOut != null) i.putExtra(MediaStore.EXTRA_OUTPUT, cameraOut);
                }
                startActivityForResult(i, REQ_CAMERA);
                return;
            }
            Intent i = new Intent(Intent.ACTION_OPEN_DOCUMENT);
            i.addCategory(Intent.CATEGORY_OPENABLE);
            if ("album".equals(source)) {
                i.setType("image/*");
            } else {
                // 不限定类型：docx/pdf/zip 这些也要能选，选到什么就按什么处理
                i.setType("*/*");
            }
            startActivityForResult(i, "album".equals(source) ? REQ_ALBUM : REQ_ATTACH_FILE);
        } catch (android.content.ActivityNotFoundException noApp) {
            toast("系统里找不到能打开它的应用");
        } catch (Throwable t) {
            toast("打不开选择器：" + t.getMessage());
        }
    }

    /** 相机 / 相册 / 文件选择器的回执统一在这里收（之前这个方法是缺失的，所以附件全丢） */
    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        Uri got = (data == null) ? null : data.getData();
        android.util.Log.i("bz", "onActivityResult req=" + requestCode + " res=" + resultCode + " uri=" + got);
        if (resultCode != RESULT_OK) { keepPick = false; return; }   // 用户自己取消的，不打扰

        if (requestCode == REQ_CAMERA) {
            final Uri u = (cameraOut != null) ? cameraOut : got;
            cameraOut = null;
            if (u == null) { keepPick = false; attachError("相机没有返回照片"); return; }
            // 有的机型刚拍完还没落盘，稍等一下再读
            new android.os.Handler(getMainLooper()).postDelayed(() -> deliverAttachment(u, null), 400);
            return;
        }
        if (requestCode == REQ_ALBUM || requestCode == REQ_ATTACH_FILE) {
            if (got == null) { keepPick = false; attachError("选择器没有返回文件"); return; }
            try {
                getContentResolver().takePersistableUriPermission(got, Intent.FLAG_GRANT_READ_URI_PERMISSION);
            } catch (Throwable ignored) { /* 有的来源不支持持久化，能读就行 */ }
            deliverAttachment(got, null);
        }
    }

    /** 把选到的内容压一压交给前端：图片降采样成 JPEG，文本文件读正文，其它只给名字 */
    private void deliverAttachment(Uri uri, String fallbackName) {
        if (keepPick) {                 // 待办附件：复制进私有目录，只回引用 + 缩略图
            keepPick = false;
            deliverKept(uri, fallbackName);
            return;
        }
        String name = fallbackName;
        if (name == null || name.isEmpty()) name = uri.getLastPathSegment();
        if (name == null || name.isEmpty()) name = "attachment";
        long size = sizeOf(uri);
        String mime = getContentResolver().getType(uri);
        try {
            if (size <= 0) size = 0;
            boolean isImage = (mime != null && mime.startsWith("image/"));
            if (!isImage) {
                String lower = name.toLowerCase();
                isImage = lower.endsWith(".jpg") || lower.endsWith(".jpeg") || lower.endsWith(".png")
                        || lower.endsWith(".webp") || lower.endsWith(".heic") || lower.endsWith(".gif");
            }
            if (isImage) {
                String b64 = imageToBase64(uri, 1600, 80);
                if (b64 == null) { attachError("这张图解析不了（可能是不支持的格式，例如 HEIC 原图）"); return; }
                attachJson("image", name, "image/jpeg", b64, size, null);
                return;
            }
            // 非图片：文本类读正文（上限 200KB），其它只回名字
            String lower = name.toLowerCase();
            boolean textLike = (mime != null && (mime.startsWith("text/") || mime.contains("json") || mime.contains("xml")))
                    || lower.endsWith(".md") || lower.endsWith(".markdown") || lower.endsWith(".txt")
                    || lower.endsWith(".json") || lower.endsWith(".csv") || lower.endsWith(".log")
                    || lower.endsWith(".js") || lower.endsWith(".ts") || lower.endsWith(".yml") || lower.endsWith(".yaml");
            if (!textLike) {
                attachJson("file", name, mime, null, size, null);
                return;
            }
            if (size > 200 * 1024) {
                attachJson("file", name, mime, null, size, "文件超过 200KB，只记下了名字");
                return;
            }
            byte[] all;
            try (java.io.InputStream s3 = getContentResolver().openInputStream(uri)) {
                all = readAll(s3);
            }
            attachJson("file", name, mime, null, size, new String(all, "UTF-8"));
        } catch (Throwable t) {
            attachError("读取失败：" + t.getMessage());
        }
    }

    /* ---------------- 待办附件：复制进私有目录 + 外发/另存/删除 ---------------- */

    /** 把选择器/相机给的文件复制进 App 私有目录；失败给 null */
    private java.io.File copyIntoAppDir(Uri uri, String name) {
        try {
            String safe = (name == null || name.trim().isEmpty()) ? "file" : name.replaceAll("[\\\\/:*?\"<>|]", "_");
            String ext = "";
            int dot = safe.lastIndexOf('.');
            if (dot > 0) ext = safe.substring(dot);
            java.io.File out = new java.io.File(attDir(),
                    System.currentTimeMillis() + "-" + Math.abs(safe.hashCode()) + ext);
            try (java.io.InputStream in = getContentResolver().openInputStream(uri);
                 java.io.OutputStream os = new java.io.FileOutputStream(out)) {
                if (in == null) return null;
                byte[] buf = new byte[32 * 1024];
                int n;
                while ((n = in.read(buf)) > 0) os.write(buf, 0, n);
                os.flush();
            }
            return out;
        } catch (Throwable t) {
            android.util.Log.i("bz", "复制附件失败 " + t);
            return null;
        }
    }

    private java.io.File attDir() {
        java.io.File d = new java.io.File(getFilesDir(), AttProvider.DIR);
        if (!d.exists()) d.mkdirs();
        return d;
    }

    /** 只认私有附件目录里的文件：前端传任意路径也删不到/开不了别的东西 */
    private java.io.File attFile(String path) {
        if (path == null || path.trim().isEmpty()) return null;
        try {
            java.io.File f = new java.io.File(path.trim());
            String dir = getFilesDir().getCanonicalPath() + java.io.File.separator + AttProvider.DIR + java.io.File.separator;
            if (!f.getCanonicalPath().startsWith(dir)) return null;
            return f;
        } catch (Throwable t) {
            return null;
        }
    }

    private static boolean isImageName(String name) {
        String lower = name == null ? "" : name.toLowerCase();
        return lower.endsWith(".jpg") || lower.endsWith(".jpeg") || lower.endsWith(".png")
                || lower.endsWith(".webp") || lower.endsWith(".gif") || lower.endsWith(".heic");
    }

    /** 待办附件：复制进私有目录后只回「引用 + 小缩略图」 */
    private void deliverKept(Uri uri, String fallbackName) {
        String name = (fallbackName == null || fallbackName.isEmpty()) ? uri.getLastPathSegment() : fallbackName;
        if (name == null || name.isEmpty()) name = "attachment";
        long size = sizeOf(uri);
        String mime = getContentResolver().getType(uri);
        boolean isImage = (mime != null && mime.startsWith("image/")) || isImageName(name);
        java.io.File saved = copyIntoAppDir(uri, name);
        if (saved == null) { attachError("附件复制失败（云盘文件或没有读取权限？）"); return; }
        try {
            org.json.JSONObject o = new org.json.JSONObject();
            o.put("dest", keepDest);
            o.put("kind", isImage ? "image" : "file");
            o.put("name", name);
            o.put("mime", isImage ? "image/jpeg" : (mime == null ? "" : mime));
            o.put("size", size > 0 ? size : saved.length());
            o.put("path", saved.getAbsolutePath());
            if (isImage) {
                String thumb = imageToBase64(Uri.fromFile(saved), 360, 60);
                if (thumb != null) o.put("thumb", thumb);
            }
            pendingAttachment = o.toString();
            keepDest = "todo";   // 用掉即复位，免得下一次没指定用途时被认成 kb
            android.util.Log.i("bz", "attachKept name=" + name + " path=" + saved.getAbsolutePath());
            js("window.__bzAttachmentReady && window.__bzAttachmentReady()");
        } catch (Throwable t) {
            attachError("附件打包失败：" + t.getMessage());
        }
    }

    private void openAttachmentNow(String path) {
        java.io.File f = attFile(path);
        if (f == null || !f.isFile()) { toast("附件不在了（可能被清理过）"); return; }
        try {
            Uri u = AttProvider.uriFor(f);
            Intent i = new Intent(Intent.ACTION_VIEW);
            i.setDataAndType(u, getContentResolver().getType(u));
            i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            startActivity(i);
        } catch (android.content.ActivityNotFoundException noApp) {
            saveAttachmentNow(path);       // 没有应用能打开它 → 直接另存到下载
        } catch (Throwable t) {
            toast("打不开：" + t.getMessage());
        }
    }

    /** 另存到系统「下载/待办中心」 */
    private void saveAttachmentNow(String path) {
        java.io.File f = attFile(path);
        if (f == null || !f.isFile()) { toast("附件不在了（可能被清理过）"); return; }
        if (Build.VERSION.SDK_INT < 29) { toast("系统版本太低，请用文件管理器查看 App 私有目录"); return; }
        try {
            String mime = getContentResolver().getType(AttProvider.uriFor(f));
            android.content.ContentValues cv = new android.content.ContentValues();
            cv.put(android.provider.MediaStore.MediaColumns.DISPLAY_NAME, f.getName());
            cv.put(android.provider.MediaStore.MediaColumns.MIME_TYPE,
                    mime == null ? "application/octet-stream" : mime);
            cv.put(android.provider.MediaStore.MediaColumns.RELATIVE_PATH,
                    android.os.Environment.DIRECTORY_DOWNLOADS + "/白泽");
            Uri target = getContentResolver().insert(
                    android.provider.MediaStore.Downloads.EXTERNAL_CONTENT_URI, cv);
            if (target == null) { toast("另存失败：系统没给出写入位置"); return; }
            try (java.io.InputStream in = new java.io.FileInputStream(f);
                 java.io.OutputStream os = getContentResolver().openOutputStream(target)) {
                byte[] buf = new byte[32 * 1024];
                int n;
                while ((n = in.read(buf)) > 0) os.write(buf, 0, n);
                os.flush();
            }
            toast("已另存到「下载/白泽」，去文件管理里打开");
        } catch (Throwable t) {
            toast("另存失败：" + t.getMessage());
        }
    }

    /** 问系统要文件大小（不再整份读一遍来算，大文件/云盘会卡住） */
    private long sizeOf(Uri uri) {
        try (android.database.Cursor c = getContentResolver().query(uri, null, null, null, null)) {
            if (c != null && c.moveToFirst()) {
                int i = c.getColumnIndex(android.provider.OpenableColumns.SIZE);
                if (i >= 0 && !c.isNull(i)) return c.getLong(i);
            }
        } catch (Throwable ignored) {
        }
        return 0;
    }

    private static byte[] readAll(java.io.InputStream in) throws java.io.IOException {
        java.io.ByteArrayOutputStream out = new java.io.ByteArrayOutputStream(64 * 1024);
        byte[] buf = new byte[16 * 1024];
        int n;
        while ((n = in.read(buf)) > 0) out.write(buf, 0, n);
        return out.toByteArray();
    }
    /** 降采样 + 转 JPEG + base64（避免把整张原图塞进 JS） */
    private String imageToBase64(Uri uri, int maxSide, int quality) {
        try (java.io.InputStream in = getContentResolver().openInputStream(uri)) {
            if (in == null) return null;
            android.graphics.BitmapFactory.Options probe = new android.graphics.BitmapFactory.Options();
            probe.inJustDecodeBounds = true;
            android.graphics.BitmapFactory.decodeStream(in, null, probe);
            int sample = 1;
            while (Math.max(probe.outWidth, probe.outHeight) / sample > maxSide * 2) sample *= 2;
            android.graphics.BitmapFactory.Options opt = new android.graphics.BitmapFactory.Options();
            opt.inSampleSize = sample;
            android.graphics.Bitmap bmp;
            try (java.io.InputStream in2 = getContentResolver().openInputStream(uri)) {
                bmp = android.graphics.BitmapFactory.decodeStream(in2, null, opt);
            }
            if (bmp == null) return null;
            int longSide = Math.max(bmp.getWidth(), bmp.getHeight());
            if (longSide > maxSide) {
                float k = (float) maxSide / longSide;
                android.graphics.Bitmap scaled = android.graphics.Bitmap.createScaledBitmap(
                        bmp, Math.max(1, Math.round(bmp.getWidth() * k)),
                        Math.max(1, Math.round(bmp.getHeight() * k)), true);
                if (scaled != bmp) bmp.recycle();
                bmp = scaled;
            }
            java.io.ByteArrayOutputStream bos = new java.io.ByteArrayOutputStream();
            bmp.compress(android.graphics.Bitmap.CompressFormat.JPEG, quality, bos);
            bmp.recycle();
            return android.util.Base64.encodeToString(bos.toByteArray(), android.util.Base64.NO_WRAP);
        } catch (Throwable t) {
            return null;
        }
    }

    /** 附件先存在原生侧，等前端主动来取。
        为什么不直接 evaluateJavascript 把 JSON 塞过去：图片 base64 动辄几百 KB，
        超大字符串会静默失败（照片就是这么"没反应"的）。 */
    private String pendingAttachment = null;

    private void attachJson(String kind, String name, String mime, String base64, long size, String text) {
        try {
            org.json.JSONObject o = new org.json.JSONObject();
            o.put("dest", "chat");       // 对话附件：只给模型看，不落盘
            o.put("kind", kind);
            o.put("name", name);
            o.put("mime", mime == null ? "" : mime);
            o.put("size", size);
            if (base64 != null) o.put("base64", base64);
            if (text != null) o.put("text", text);
            pendingAttachment = o.toString();
            android.util.Log.i("bz", "attachJson kind=" + kind + " name=" + name + " size=" + size
                    + " text=" + (text == null ? "无" : text.length() + "字") + " b64=" + (base64 == null ? "无" : base64.length() + "字符"));
            // 附件的 JSON 留在原生侧，这里只发一个"好了"的信号（大图不走字符串转义）
            js("window.__bzAttachmentReady && window.__bzAttachmentReady()");
        } catch (Throwable t) {
            attachError("附件打包失败：" + t.getMessage());
        }
    }

    private void attachError(String msg) {
        toast(msg);
        js("window.__bzAttachError && window.__bzAttachError(" + jsStr(msg) + ")");
    }

    /* ---------------- 工具 ---------------- */

    private void js(String code) {
        if (web != null) web.post(() -> web.evaluateJavascript(code, null));
    }

    private static String jsStr(String s) {
        if (s == null) return "''";
        StringBuilder sb = new StringBuilder("'");
        for (char c : s.toCharArray()) {
            switch (c) {
                case '\\': sb.append("\\\\"); break;
                case '\'': sb.append("\\'"); break;
                case '\n': sb.append("\\n"); break;
                case '\r': sb.append("\\r"); break;
                // 行分隔符会截断 JS 字符串字面量（附件里的文件正文可能带）
                case '\u2028': sb.append("\\u2028"); break;
                case '\u2029': sb.append("\\u2029"); break;
                default: sb.append(c);
            }
        }
        return sb.append("'").toString();
    }

    private void toast(String msg) {
        runOnUiThread(() -> Toast.makeText(this, msg, Toast.LENGTH_SHORT).show());
    }

    /* ---------------- 麦克风（按住说话） ---------------- */

    private boolean hasRecordPermission() {
        try {
            return checkSelfPermission(android.Manifest.permission.RECORD_AUDIO)
                    == android.content.pm.PackageManager.PERMISSION_GRANTED;
        } catch (Throwable t) {
            return false;
        }
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
        super.onRequestPermissionsResult(code, perms, results);
        if (code != REQ_MIC) return;
        boolean ok = results.length > 0 && results[0]
                == android.content.pm.PackageManager.PERMISSION_GRANTED;
        final android.webkit.PermissionRequest req = pendingMicRequest;
        pendingMicRequest = null;
        if (req == null) {
            if (!ok) toast("没给麦克风权限，按住说话用不了");
            return;
        }
        if (ok) {
            runOnUiThread(() -> req.grant(new String[]{android.webkit.PermissionRequest.RESOURCE_AUDIO_CAPTURE}));
            toast("麦克风已允许，再按一次「按住说话」");
        } else {
            runOnUiThread(req::deny);
            toast("没给麦克风权限，按住说话用不了（系统设置 → 应用 → 待办中心 → 权限）");
        }
    }

    /** 把快捷方式的启动意图交给前端路由 */
    private void dispatchAction(String action) {
        if (action == null || web == null) return;
        js("window.__bzLaunchAction && window.__bzLaunchAction(" + jsStr(action) + ")");
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        dispatchAction(intent != null ? intent.getAction() : null);
    }

    @Override
    public void onBackPressed() {
        // 先让前端处理（关闭全屏页 / 弹层），未处理再走系统返回
        web.evaluateJavascript(
                "(window.UI && UI.handleBack) ? (UI.handleBack() ? '1' : '0') : '0'",
                value -> {
                    if (!"\"1\"".equals(value)) {
                        if (web.canGoBack()) web.goBack();
                        else finish();
                    }
                });
    }

    @Override
    protected void onDestroy() {
        // LocalServer 是进程级单例（多个页面共用同一个 origin），此处不关闭。
        // 内核也不再跟着界面退：它由 CoreService 前台常驻，设备才能长期在线。
        // 用户主动关掉跨端时，bzCoreConfigure 会显式停服务、停内核，不会留下没人管的在线设备。
        if (web != null) web.destroy();
        super.onDestroy();
    }
}
