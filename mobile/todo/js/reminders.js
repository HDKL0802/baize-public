/* 白泽待办中心 - 提醒计划
   把「哪些待办到点要提醒」这件事抽成一份纯逻辑，主界面（app.js）与桌面小组件的
   快速录入页（quick.js）共用，避免两处规则漂移导致小组件记下的提醒不响。 */
'use strict';
window.ReminderPlan = (function () {
  /** 返回 [{id, at, title, body}]，交给 Android 壳排精确闹钟 */
  function build() {
    const out = [];
    const s = Store.getSettings();
    if (!s.remind) return out;
    const now = Date.now();
    Store.getTodos().forEach(t => {
      if (t.status === 'done' || t.status === 'cancelled') return;
      if (!t.remind || !t.due) return;
      const base = new Date(t.due).getTime();
      if (isNaN(base)) return;
      const title = '⏰ ' + t.title;
      const body = ((t.note || '') + (t.form === 'leisure' ? '' : '  到点了')).trim();
      if (t.weekly) {
        // 每周提醒：往后排 8 周，覆盖一个较长的使用周期
        for (let k = 0; k < 8; k++) {
          const at = base + k * 7 * 86400000;
          if (at > now) out.push({ id: t.id + '-w' + k, at, title, body });
        }
      } else if (base > now) {
        out.push({ id: t.id, at: base, title, body });
      }
    });
    return out;
  }

  return { build };
})();
