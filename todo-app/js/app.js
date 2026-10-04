/* 白泽待办中心 - UI 公共层（tab 路由 / 弹层 / 操作单 / toast） */
'use strict';
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