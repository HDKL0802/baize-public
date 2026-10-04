/* 白泽待办中心 - UI 公共层（tab 路由 / 弹层 / 操作单 / toast） */
'use strict';
window.UI = (function () {
  function $(id) { return document.getElementById(id); }

  /* tab 路由 */
  let currentTab = 'view-todo';
  function switchTab(tabId) {
    currentTab = tabId;
    document.querySelectorAll('.tab-view').forEach(v => v.hidden = v.id !== tabId);
    document.querySelectorAll('.nav-item').forEach(n => n.classList.toggle('active', n.dataset.tab === tabId));
  }
  document.addEventListener('click', (e) => {
    const nav = e.target.closest('.nav-item');
    if (nav) switchTab(nav.dataset.tab);
  });

  /* toast */
  let toastTimer = null;
  function toast(msg, ms = 1800) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove('show'), ms);
  }

  /* 弹层 */
  function openModal(id) { const m = $(id); if (m) m.hidden = false; }
  function closeModal(id) { const m = $(id); if (m) m.hidden = true; }
  function closeOnMask(maskId) {
    $(maskId).addEventListener('click', (e) => { if (e.target.id === maskId) $(maskId).hidden = true; });
  }

  /* 全屏页 */
  function openPage(id) {
    const p = $(id);
    if (!p) return;
    p.hidden = false;
    const body = p.querySelector('.po-body');
    if (body) body.scrollTop = 0;
  }
  function closePage(id) { const p = $(id); if (p) p.hidden = true; }

  /* 操作单 */
  function actionSheet(items) {
    const box = $('actionSheetBox');
    box.innerHTML = '';
    items.forEach((it) => {
      const b = document.createElement('button');
      b.className = 'as-item' + (it.danger ? ' danger' : '') + (it.cancel ? ' cancel' : '');
      b.textContent = it.label;
      b.addEventListener('click', () => {
        $('actionSheet').hidden = true;
        if (it.onTap) it.onTap();
      });
      box.appendChild(b);
    });
    $('actionSheet').hidden = false;
  }
  function hideSheet() { $('actionSheet').hidden = true; }
  $('actionSheet').addEventListener('click', (e) => { if (e.target.id === 'actionSheet') hideSheet(); });

  /* 时间工具 */
  function fmtDate(t) {
    if (!t) return '';
    const d = new Date(t);
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  function todayStr() {
    const d = new Date(); const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}年${pad(d.getMonth() + 1)}月${pad(d.getDate())}日 星期${'日一二三四五六'[d.getDay()]}`;
  }
  function dueLabel(t) {
    if (!t) return '';
    const d = new Date(t), n = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    if (d.toDateString() === n.toDateString()) return '今天 ' + hm;
    const tm = new Date(n); tm.setDate(tm.getDate() + 1);
    if (d.toDateString() === tm.toDateString()) return '明天 ' + hm;
    return `${d.getMonth() + 1}/${d.getDate()} ${hm}`;
  }

  /* 提醒调度：到点发浏览器通知 */
  let remindTimer = null;
  function startReminderLoop() {
    clearInterval(remindTimer);
    remindTimer = setInterval(() => {
      const s = Store.getSettings();
      if (!s.remind || !s.notify || !('Notification' in window)) return;
      const now = Date.now();
      const doneIds = new Set(JSON.parse(localStorage.getItem('bz_reminded') || '[]'));
      Store.getTodos().forEach((t) => {
        if (t.status === 'done' || t.status === 'cancelled') return;
        if (!t.remind || !t.due) return;
        const dueT = new Date(t.due).getTime();
        if (dueT <= now + 60000 && dueT > now - 30000 && !doneIds.has(t.id)) {
          try {
            new Notification('⏰ 待办提醒：' + t.title);
          } catch (e) { /* 无权限忽略 */ }
          doneIds.push(t.id);
          setTimeout(() => {
            const arr = JSON.parse(localStorage.getItem('bz_reminded') || '[]').concat(t.id);
            localStorage.setItem('bz_reminded', JSON.stringify(arr));
          }, 0);
        }
        // 清理已完成的提醒记录
      });
      localStorage.setItem('bz_reminded', JSON.stringify(doneIds));
    }, 20000);
  }
  function notifyEnabled() {
    return 'Notification' in window && Notification.permission === 'granted';
  }

  /* 返回键：供 Android 壳调用，返回 true 表示已消费 */
  function handleBack() {
    const pages = ['todoPage', 'vaultPage', 'planPage'];
    for (const id of pages) {
      const el = $(id);
      if (el && !el.hidden) { el.hidden = true; return true; }
    }
    const modals = ['apiModal', 'voiceModal', 'pwdModal'];
    for (const id of modals) {
      const el = $(id);
      if (el && !el.hidden) { el.hidden = true; return true; }
    }
    const sheet = $('actionSheet');
    if (sheet && !sheet.hidden) { sheet.hidden = true; return true; }
    return false;
  }

  return {
    $, switchTab, toast, openModal, closeModal, closeOnMask, openPage, closePage,
    actionSheet, hideSheet, handleBack,
    fmtDate, todayStr, dueLabel, startReminderLoop, notifyEnabled,
  };
})();