package com.baize.todo;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/** 闹钟到点 → 弹出系统通知 */
public class ReminderReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context context, Intent intent) {
        String id = intent.getStringExtra("id");
        String title = intent.getStringExtra("title");
        String body = intent.getStringExtra("body");
        Reminders.post(
                context,
                id == null ? "baize-todo" : id,
                title == null || title.isEmpty() ? "待办提醒" : title,
                body == null ? "" : body);
    }
}
