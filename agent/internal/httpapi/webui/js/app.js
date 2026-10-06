/* 白泽待办中心 - UI 公共层（tab 路由 / 弹层 / 操作单 / toast） */
'use strict';

/* 线性图标（24×24，stroke=currentColor）—— 与桌面端 shell.js 的 NAV_ICONS 同一套。
   emoji 在深色界面里又跳又廉价，统一换成这套。用法：Icon.svg('chat') 或 Icon.svg('file', 'ic2')。 */
window.Icon = (function () {
  const P = {
    chat: '<path d="M4 6.5A2.5 2.5 0 0 1 6.5 4h11A2.5 2.5 0 0 1 20 6.5v6a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4H6.5A2.5 2.5 0 0 1 4 12.5z"/>',
    kb: '<path d="M12 6.6C10.4 5.1 7.9 4.5 4 4.5v13c3.9 0 6.4.6 8 2.1 1.6-1.5 4.1-2.1 8-2.1v-13c-3.9 0-6.4.6-8 2.1z"/><path d="M12 6.6v13"/>',
    devices: '<rect x="3" y="4.5" width="18" height="11.5" rx="1.5"/><path d="M9 20h6M12 16v4"/>',
    tasks: '<rect x="6" y="4.5" width="12" height="16" rx="2"/><path d="M9.5 4.5h5v2.5h-5z"/><path d="m9.5 12.5 2 2 3.5-4"/>',
    memory: '<rect x="7" y="7" width="10" height="10" rx="1.6"/><path d="M10 3.5v3.5M14 3.5v3.5M10 17v3.5M14 17v3.5M3.5 10h3.5M3.5 14h3.5M17 10h3.5M17 14h3.5"/>',
    skills: '<path d="M12 3.2l2 5.3 5.3 2-5.3 2-2 5.3-2-5.3-5.3-2 5.3-2z"/><path d="M18.5 16.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z"/>',
    cron: '<circle cx="12" cy="12" r="8.4"/><path d="M12 7.4V12l3.2 2"/>',
    mcp: '<path d="M9 3.5v4M15 3.5v4"/><path d="M6.5 7.5h11v3.5a5.5 5.5 0 0 1-11 0z"/><path d="M12 16.5v4"/>',
    channels: '<circle cx="12" cy="12" r="2"/><path d="M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4M4.9 4.9a10 10 0 0 0 0 14.2M19.1 4.9a10 10 0 0 1 0 14.2"/>',
    backups: '<rect x="3.5" y="4.5" width="17" height="4.6" rx="1.4"/><path d="M5.5 9.1V19a1.4 1.4 0 0 0 1.4 1.4h10.2A1.4 1.4 0 0 0 18.5 19V9.1"/><path d="M10 13h4"/>',
    runs: '<path d="M8.5 6.5h11M8.5 12h11M8.5 17.5h7"/><path d="M4 6.5h.01M4 12h.01M4 17.5h.01"/>',
    logs: '<path d="M6 3.5h8l4 4V20a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V4.5a1 1 0 0 1 1-1z"/><path d="M14 3.5V8h4"/><path d="M8 12h7M8 15.5h5"/>',
    settings: '<circle cx="12" cy="12" r="3.2"/><path d="M12 3.4v2.3M12 18.3v2.3M3.4 12h2.3M18.3 12h2.3M5.9 5.9l1.6 1.6M16.5 16.5l1.6 1.6M18.1 5.9l-1.6 1.6M7.5 16.5l-1.6 1.6"/>',
    voice: '<rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5.5 11a6.5 6.5 0 0 0 13 0"/><path d="M12 17.5V21M8.5 21h7"/>',
    approve: '<path d="M12 3.4l7 2.9v5.3c0 4.3-2.9 7.6-7 8.9-4.1-1.3-7-4.6-7-8.9V6.3z"/><path d="m9.3 11.8 1.9 1.9 3.6-3.9"/>',
    apikeys: '<circle cx="8" cy="15" r="4"/><path d="M10.8 12.2 20 3M17 6l2.3 2.3M14.7 8.3 17 10.6"/>',
    file: '<path d="M20 11.5 12.6 19a4.2 4.2 0 0 1-6-6l7.4-7.4a2.8 2.8 0 0 1 4 4L10.5 17a1.4 1.4 0 0 1-2-2l6.6-6.6"/>',
    image: '<rect x="3" y="4.5" width="18" height="15" rx="2"/><circle cx="8.5" cy="9.5" r="1.6"/><path d="m4 17 5-5 4 4 3-3 4 4"/>',
    camera: '<path d="M4 8.5h3l1.4-2h7.2L17 8.5h3a1 1 0 0 1 1 1V18a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9.5a1 1 0 0 1 1-1z"/><circle cx="12" cy="13" r="3.2"/>',
    magic: '<path d="M5 19 15.5 8.5"/><path d="M13.5 6.5 17 10"/><path d="M18 3.5v3M19.5 5h-3M6 12.6v2.4M7.2 13.8H4.8"/>',
    speak: '<path d="M4 9.5h3l4-3.5v12l-4-3.5H4z"/><path d="M14.5 9a4 4 0 0 1 0 6"/><path d="M17 6.8a7 7 0 0 1 0 10.4"/>',
    warn: '<path d="M12 4.2 21 19.5H3z"/><path d="M12 10v4.2M12 17.2h.01"/>',
  };
  function svg(id, cls) {
    const p = P[id];
    if (!p) return '';
    return '<svg class="' + (cls || 'ic') + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
      'stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + p + '</svg>';
  }
  return { svg, has: (id) => !!P[id] };
})();

window.UI = (function () {
  function $(id) { return document.getElementById(id); }

  /* tab 路由：对话（view-baize）→ 知识库（内含 主要/资源/配置 三类）→ 其他功能（view-more）
     第三类「配置」里放的是 模型站密钥（API 服务）与 模型审批。 */
  const KB_CATS = [
    { id: 'main', name: '主要', items: [
      { id: 'view-todo',  name: '待办' },
      { id: 'view-vault', name: '密码本' },
    ] },
    { id: 'res', name: '资源', items: [
      { id: 'view-files',  name: '附件' },
      { id: 'view-memory', name: '记忆' },
    ] },
    { id: 'cfg', name: '配置', items: [
      { id: 'view-keys',     name: 'API 服务' },
      { id: 'view-approval', name: '模型审批' },
    ] },
  ];
  const KB_PANES = KB_CATS.reduce((acc, c) => acc.concat(c.items.map(i => i.id)), []);
  let currentTab = 'view-baize';
  let kbPane = 'view-todo';   // 知识库里当前显示的那一页

  function catOfPane(paneId) {
    return KB_CATS.find(c => c.items.some(i => i.id === paneId)) || KB_CATS[0];
  }

  /** 知识库的两级切换条：上面选类别（主要 / 资源 / 配置），下面选该类里的条目。
      每个知识库页各注入一份，行为一致，这样底栏只需要一个「知识库」入口。 */
  function injectKbSeg() {
    KB_PANES.forEach((id) => {
      const pane = $(id);
      if (!pane || pane.querySelector('.kb-seg')) return;
      const cat = catOfPane(id);
      const wrap = document.createElement('div');
      wrap.className = 'kb-seg';

      const top = document.createElement('div');
      top.className = 'seg';
      KB_CATS.forEach((c) => {
        const b = document.createElement('button');
        b.className = 'seg-btn';
        b.dataset.kbcat = c.id;
        b.textContent = c.name;
        top.appendChild(b);
      });

      const sub = document.createElement('div');
      sub.className = 'seg soft';
      cat.items.forEach((it) => {
        const b = document.createElement('button');
        b.className = 'vs-btn';
        b.dataset.kb = it.id;
        b.textContent = it.name;
        sub.appendChild(b);
      });

      wrap.appendChild(top);
      wrap.appendChild(sub);
      const head = pane.querySelector('.page-head');
      if (head) head.insertAdjacentElement('afterend', wrap);
      else pane.insertBefore(wrap, pane.firstChild);
    });
  }

  function syncKbSeg() {
    const cat = catOfPane(kbPane);
    document.querySelectorAll('.kb-seg .seg-btn').forEach((b) => {
      b.classList.toggle('active', b.dataset.kbcat === cat.id);
    });
    document.querySelectorAll('.kb-seg .vs-btn').forEach((b) => {
      b.classList.toggle('active', b.dataset.kb === kbPane);
    });
  }

  function switchTab(tabId) {
    if (tabId === 'view-kb') tabId = kbPane;
    const inKb = KB_PANES.indexOf(tabId) >= 0;
    if (inKb) kbPane = tabId;
    currentTab = tabId;
    document.querySelectorAll('.tab-view').forEach(v => v.hidden = v.id !== tabId);
    // 不占底栏的页面（设置是从「其他功能」进去的）算在「其他功能」下面，
    // 免得底栏出现"一个都不高亮"的状态。
    const NAV_OF = { 'view-settings': 'view-more' };
    const activeNav = inKb ? 'view-kb' : (NAV_OF[tabId] || tabId);
    document.querySelectorAll('.nav-item').forEach(n => n.classList.toggle('active', n.dataset.tab === activeNav));
    syncKbSeg();
    // 切到某一页时广播一次，各页按需去后端取最新（附件 / 记忆 / 密钥 / 审批都要联网）
    try { window.dispatchEvent(new CustomEvent('bz:page', { detail: { id: tabId } })); } catch (e) { /* 忽略 */ }
  }
  document.addEventListener('click', (e) => {
    const catBtn = e.target.closest('.kb-seg .seg-btn');
    if (catBtn) {
      const c = KB_CATS.find(x => x.id === catBtn.dataset.kbcat);
      if (c && c.items.length) switchTab(c.items[0].id);
      return;
    }
    const seg = e.target.closest('.kb-seg .vs-btn');
    if (seg) { switchTab(seg.dataset.kb); return; }
    const nav = e.target.closest('.nav-item');
    if (nav) switchTab(nav.dataset.tab);
  });
  injectKbSeg();

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

  /* 通用全屏页（「其他功能」的各个子页、以及知识库里的新增表单，都复用它） */
  function openTool(title, html, onMount) {
    $('toolTitle').textContent = title || '';
    const body = $('toolBody');
    body.innerHTML = html || '';
    const act = $('toolAction');
    act.hidden = true; act.textContent = ''; act.onclick = null;
    openPage('toolPage');
    if (typeof onMount === 'function') onMount(body);
    return body;
  }
  /** 给通用全屏页右上角挂一个动作按钮（保存 / 上传 …） */
  function setToolAction(label, onTap) {
    const act = $('toolAction');
    if (!label) { act.hidden = true; act.onclick = null; return; }
    act.hidden = false;
    act.textContent = label;
    act.onclick = onTap;
  }
  $('toolClose').addEventListener('click', () => closePage('toolPage'));

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

  /* ================= 提醒调度 =================
     Android 壳里走原生 AlarmManager + 系统通知（WebView 不支持网页 Notification API）；
     纯浏览器环境回退到网页通知。 */

  let remindTimer = null;

  function nativeShell() {
    return (window.BzNative && typeof window.BzNative.syncReminders === 'function') ? window.BzNative : null;
  }

  /** 把需要提醒的待办整体下发给原生壳（幂等：原生侧重排全部闹钟） */
  function syncNativeReminders() {
    const nb = nativeShell();
    if (!nb) return false;
    try { nb.syncReminders(JSON.stringify(ReminderPlan.build())); } catch (e) { /* 忽略 */ }
    return true;
  }

  function startReminderLoop() {
    if (syncNativeReminders()) return;   // 原生壳：交给系统闹钟，无需网页定时器
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
          try { new Notification('⏰ 待办提醒：' + t.title); } catch (e) { /* 无权限忽略 */ }
          doneIds.push(t.id);
        }
      });
      localStorage.setItem('bz_reminded', JSON.stringify(doneIds));
    }, 20000);
  }

  /** 通知是否可用（原生壳看系统开关，浏览器看授权） */
  function notifyEnabled() {
    const nb = nativeShell();
    if (nb && typeof nb.notificationsEnabled === 'function') {
      try { return !!nb.notificationsEnabled(); } catch (e) { return false; }
    }
    return 'Notification' in window && Notification.permission === 'granted';
  }

  /* 返回键：供 Android 壳调用，返回 true 表示已消费 */
  function handleBack() {
    const pages = ['todoPage', 'vaultPage', 'planPage', 'toolPage'];
    for (const id of pages) {
      const el = $(id);
      if (el && !el.hidden) { el.hidden = true; return true; }
    }
    const modals = ['agentModal', 'pwdModal'];
    for (const id of modals) {
      const el = $(id);
      if (el && !el.hidden) { el.hidden = true; return true; }
    }
    const sheet = $('actionSheet');
    if (sheet && !sheet.hidden) { sheet.hidden = true; return true; }
    return false;
  }

  /* ================= 附件回调分发 =================
     原生把附件暂存在自己那边，只发一个"好了"的信号。对话页和待办页都要接这个信号，
     所以这里统一分发（各自按 dest 认领），不要各自去覆盖 window.__bzAttachmentReady。 */
  const attHandlers = [];
  function onAttachmentReady(fn) { if (typeof fn === 'function') attHandlers.push(fn); }
  window.__bzAttachmentReady = function () {
    attHandlers.forEach(f => { try { f(); } catch (e) { /* 单个页面出错不影响其它 */ } });
  };
  document.addEventListener('visibilitychange', () => { if (!document.hidden) window.__bzAttachmentReady(); });
  window.addEventListener('focus', () => window.__bzAttachmentReady());

  /* ================= 文件导入导出 =================
     APK 里 WebView 的 <a download> 与 <input type=file> 都不好用，改走原生桥：
       · saveTextFile(name, content)  → 用 MediaStore 写入「下载」目录
       · pickTextFile()               → 系统文件选择器，选完回调 window.__bzFileContent(text, name)
     纯浏览器环境自动回退到 Blob 下载 / 隐藏 input。 */

  function saveText(name, content) {
    const nb = window.BzNative;
    if (nb && typeof nb.saveTextFile === 'function') {
      try {
        const saved = nb.saveTextFile(name, content);
        if (saved) { toast('已保存到 ' + saved); return true; }
        toast('保存失败：无法写入下载目录');
        return false;
      } catch (e) {
        toast('保存失败：' + e.message);
        return false;
      }
    }
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([content], { type: 'text/markdown;charset=utf-8' }));
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 4000);
    toast('已导出：' + name);
    return true;
  }

  let pickCb = null;
  function pickText(cb) {
    const nb = window.BzNative;
    if (nb && typeof nb.pickTextFile === 'function') {
      pickCb = cb;
      try { nb.pickTextFile(); } catch (e) { toast('无法打开文件选择器'); }
      return;
    }
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.md,.markdown,.txt,text/*';
    input.addEventListener('change', () => {
      const f = input.files && input.files[0];
      if (!f) return;
      f.text().then(t => cb(t, f.name)).catch(() => toast('读取文件失败'));
    });
    input.click();
  }
  /** 原生选完文件后的回调（由 MainActivity 调用）
      文件较大的时候原生只传文件名、内容走 takePickedText() 取，避免超长 JS 字符串转义 */
  window.__bzFileContent = function (text, name) {
    const cb = pickCb; pickCb = null;
    if (!cb) return;
    const nb = window.BzNative;
    if ((text === null || text === undefined) && nb && typeof nb.takePickedText === 'function') {
      try { text = nb.takePickedText(); } catch (e) { text = ''; }
    }
    cb(text || '', name);
  };

  return {
    $, switchTab, toast, openModal, closeModal, closeOnMask, openPage, closePage,
    openTool, setToolAction,
    actionSheet, hideSheet, handleBack,
    fmtDate, todayStr, dueLabel, startReminderLoop, notifyEnabled,
    nativeShell, syncNativeReminders, saveText, pickText, onAttachmentReady,
  };
})();