package com.baize.todo;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.util.Log;

/**
 * 开机 / 覆盖安装后的自启：重建闹钟 + 把跨端常驻服务拉回来。
 *
 * 注意（三星等激进 OEM）：BOOT_COMPLETED 是「顺序广播」，接收者按 priority 排序派发；
 * 三星自带 MARs 省电策略常在开机几秒后强停我们，若那时广播还没轮到本接收者，就会被跳过。
 * 所以这里给 intent-filter 挂了高 priority 去抢时间窗，并留下日志便于事后在 logcat 里确认
 * 到底「广播没送到」还是「送到了但服务没起来」。
 */
public class BootReceiver extends BroadcastReceiver {

    private static final String TAG = "BaizeTodo";

    @Override
    public void onReceive(Context context, Intent intent) {
        if (Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction())
                || Intent.ACTION_MY_PACKAGE_REPLACED.equals(intent.getAction())) {
            Log.i(TAG, "自启广播到达：" + intent.getAction() + "，准备拉起跨端常驻服务");
            Reminders.rescheduleFromPrefs(context);
            // 关机/升级会让常驻服务停掉；这里主动拉起，设备不必等用户打开 App 才上线
            // （跨端没开时 CoreService.start 会自己什么都不做）
            CoreService.start(context);
        }
    }
}
