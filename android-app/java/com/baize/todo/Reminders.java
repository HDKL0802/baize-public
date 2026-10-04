package com.baize.todo;

import android.app.AlarmManager;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Build;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.HashSet;
import java.util.Set;

/**
 * 原生待办提醒：用 AlarmManager 精确闹钟 + NotificationManager 系统通知。
 * WebView 不支持网页 Notification API，所以提醒必须由原生侧负责；
 * 前端把「需要提醒的待办」整体同步过来，这里做到幂等（先全撤再重排）。
 */
public class Reminders {

    static final String CHANNEL_ID = "baize_todo_reminder";
    private static final String PREF = "bz_reminders";
    private static final String KEY_IDS = "ids";
    private static final String KEY_JSON = "json";

    /* ---------------- 通知渠道 ---------------- */

    static void ensureChannel(Context ctx) {
        if (Build.VERSION.SDK_INT < 26) return;
        NotificationManager nm = ctx.getSystemService(NotificationManager.class);
        if (nm == null || nm.getNotificationChannel(CHANNEL_ID) != null) return;
        NotificationChannel ch = new NotificationChannel(
                CHANNEL_ID, "待办提醒", NotificationManager.IMPORTANCE_HIGH);
        ch.setDescription("待办到点提醒与每周固定提醒");
        ch.enableVibration(true);
        ch.setShowBadge(true);
        nm.createNotificationChannel(ch);
    }

    /* ---------------- 发通知 ---------------- */

    static void post(Context ctx, String id, String title, String text) {
        ensureChannel(ctx);
        NotificationManager nm = (NotificationManager) ctx.getSystemService(Context.NOTIFICATION_SERVICE);
        if (nm == null) return;

        Intent open = new Intent(ctx, MainActivity.class);
        open.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= 23) flags |= PendingIntent.FLAG_IMMUTABLE;
        PendingIntent pi = PendingIntent.getActivity(ctx, id.hashCode(), open, flags);

        Notification.Builder b = (Build.VERSION.SDK_INT >= 26)
                ? new Notification.Builder(ctx, CHANNEL_ID)
                : new Notification.Builder(ctx);
        b.setSmallIcon(android.R.drawable.ic_popup_reminder)
                .setContentTitle(title)
                .setContentText(text)
                .setStyle(new Notification.BigTextStyle().bigText(text))
                .setAutoCancel(true)
                .setContentIntent(pi)
                .setDefaults(Notification.DEFAULT_ALL)
                .setPriority(Notification.PRIORITY_HIGH);
        nm.notify(id.hashCode(), b.build());
    }

    /* ---------------- 同步提醒（前端整体下发） ---------------- */

    /**
     * json: [{"id":"todo-id","at":1690000000000,"title":"待办标题","body":"备注/时间"}]
     */
    static void sync(Context ctx, String json) {
        cancelAll(ctx);
        if (json == null || json.isEmpty()) return;
        AlarmManager am = (AlarmManager) ctx.getSystemService(Context.ALARM_SERVICE);
        if (am == null) return;

        Set<String> ids = new HashSet<>();
        try {
            JSONArray arr = new JSONArray(json);
            long now = System.currentTimeMillis();
            for (int i = 0; i < arr.length(); i++) {
                JSONObject o = arr.getJSONObject(i);
                String id = o.optString("id", "todo" + i);
                long at = o.optLong("at", 0L);
                if (at <= now) continue;
                String title = o.optString("title", "待办提醒");
                String body = o.optString("body", "");
                ids.add(id);

                PendingIntent pi = alarmIntent(ctx, id, title, body);
                try {
                    if (Build.VERSION.SDK_INT >= 23) {
                        am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, at, pi);
                    } else {
                        am.setExact(AlarmManager.RTC_WAKEUP, at, pi);
                    }
                } catch (SecurityException noExactPermission) {
                    // 未授予精确闹钟权限时退化为普通闹钟（可能被系统延后）
                    am.set(AlarmManager.RTC_WAKEUP, at, pi);
                }
            }
        } catch (Exception ignored) {
        }

        ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE).edit()
                .putStringSet(KEY_IDS, ids)
                .putString(KEY_JSON, json)
                .apply();
    }

    /** 开机后按上次同步的内容重建闹钟（闹钟会随重启丢失） */
    static void rescheduleFromPrefs(Context ctx) {
        String json = ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE).getString(KEY_JSON, null);
        if (json != null) sync(ctx, json);
    }

    /* ---------------- 内部工具 ---------------- */

    private static void cancelAll(Context ctx) {
        AlarmManager am = (AlarmManager) ctx.getSystemService(Context.ALARM_SERVICE);
        if (am == null) return;
        Set<String> ids = ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE)
                .getStringSet(KEY_IDS, new HashSet<>());
        for (String id : new HashSet<>(ids)) {
            am.cancel(alarmIntent(ctx, id, "", ""));
        }
    }

    private static PendingIntent alarmIntent(Context ctx, String id, String title, String body) {
        Intent i = new Intent(ctx, ReminderReceiver.class);
        i.putExtra("id", id);
        i.putExtra("title", title);
        i.putExtra("body", body);
        i.setAction("com.baize.todo.REMIND." + id);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= 23) flags |= PendingIntent.FLAG_IMMUTABLE;
        return PendingIntent.getBroadcast(ctx, id.hashCode(), i, flags);
    }
}
