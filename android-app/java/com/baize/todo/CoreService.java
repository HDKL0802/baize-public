package com.baize.todo;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;

/**
 * 跨端内核的前台常驻壳。
 *
 * 为什么要有这一层：内核（libbzcore.so）是「这台手机 = 白泽后端的一台设备」的唯一载体，
 * 它一退，WebSocket 就断，后端立刻把设备标离线。以前内核是跟着 MainActivity 走的
 * （onCreate 拉起 / onDestroy 停掉），于是「划掉 App / 系统回收后台进程 / 覆盖安装升级」
 * 之后设备就永久离线、只能等用户再打开 App —— 表现就是「设备注册了却上不了线」。
 *
 * 现在把内核生命周期交给这个前台服务：
 *   - 前台通知（低打扰、常驻）让进程不被系统随手回收，也满足 Android 8+ 对后台服务的限制；
 *   - START_STICKY：被系统杀掉也会被重新拉起（onStartCommand 里再按配置决定拉不拉内核）；
 *   - 开机 / 覆盖安装由 BootReceiver 再拉一次（设备不必等用户打开 App 才上线）；
 *   - 用户主动关掉跨端（bzCoreConfigure(enabled=false)）时由界面显式 stopService，
 *     服务退出即内核退出，不会留下「没人管却还在线」的设备。
 */
public class CoreService extends Service {

    static final String CHANNEL_ID = "baize_core";
    private static final String TAG = "BaizeTodo";
    private static final int NOTI_ID = 7301;

    /** 界面与开机接收器统一用这个入口拉起服务；跨端没开就顺手把服务收掉，不弹前台通知 */
    static void start(Context ctx) {
        if (!CoreBridge.shared(ctx).isEnabled()) {
            Log.i(TAG, "跨端未开启，不启动常驻服务");
            ctx.stopService(new Intent(ctx, CoreService.class));
            return;
        }
        Log.i(TAG, "请求启动跨端常驻前台服务");
        Intent i = new Intent(ctx, CoreService.class);
        if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i);
        else ctx.startService(i);
    }

    /** 主动关闭（用户把跨端开关关掉时调用） */
    static void stop(Context ctx) {
        ctx.stopService(new Intent(ctx, CoreService.class));
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        CoreBridge bridge = CoreBridge.shared(this);
        if (!bridge.isEnabled()) {
            // 没开跨端就不该常驻。stopSelf 早于 startForeground 的 5s 期限是允许的收尾方式
            Log.i(TAG, "常驻服务被拉起但跨端已关闭，直接退出");
            stopSelf();
            return START_NOT_STICKY;
        }
        startForegroundCompat();
        Log.i(TAG, "常驻服务已转前台，拉取内核");
        bridge.start();
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        // 服务退 = 不再需要常驻（用户关了跨端 / 系统彻底回收）→ 内核跟着退
        CoreBridge.shared(this).stop();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    /* ---------------- 前台通知 ---------------- */

    private void startForegroundCompat() {
        ensureChannel();
        Intent open = new Intent(this, MainActivity.class);
        open.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        int piFlags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= 23) piFlags |= PendingIntent.FLAG_IMMUTABLE;
        PendingIntent pi = PendingIntent.getActivity(this, 0, open, piFlags);

        Notification.Builder b = (Build.VERSION.SDK_INT >= 26)
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        b.setSmallIcon(android.R.drawable.ic_popup_reminder)
                .setContentTitle("白泽正在保持设备在线")
                .setContentText("跨端已开启，可随时接收电脑派来的任务")
                .setOngoing(true)
                .setShowWhen(false)
                .setContentIntent(pi);
        if (Build.VERSION.SDK_INT >= 29) {
            // targetSdk 34 起必须声明并传入前台服务类型；这类「跟后端保持连接/同步」用 dataSync
            startForeground(NOTI_ID, b.build(), ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
        } else {
            startForeground(NOTI_ID, b.build());
        }
    }

    private void ensureChannel() {
        if (Build.VERSION.SDK_INT < 26) return;
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm == null || nm.getNotificationChannel(CHANNEL_ID) != null) return;
        NotificationChannel ch = new NotificationChannel(
                CHANNEL_ID, "跨端常驻", NotificationManager.IMPORTANCE_LOW);
        ch.setDescription("保持这台设备连在白泽后端，可被派活");
        ch.setShowBadge(false);
        nm.createNotificationChannel(ch);
    }
}
