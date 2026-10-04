/* 白泽桌面端 · 外壳（左侧导航 + 单视图）
   正常智能体界面：一侧导航、一个内容区；没有 Dock / 启动台 / 浮动窗口。
   没有外部依赖，全部原生 DOM。 */
'use strict';

window.Shell = (function () {
  const $ = id => document.getElementById(id);
  const view = $('view');

  let current = null;      // 当前应用 id
  let cleanup = null;      // 当前应用返回的清理函数（可选）

  /* 导航分组（引用 apps.js 里的 id；这样不用给每个 app 加字段） */
  const NAV = [
    { title: '核心', ids: ['chat', 'devices', 'tasks'] },
    { title: '知识', ids: ['kb', 'memory', 'activity'] },
    { title: '能力', ids: ['providers', 'mcp', 'skills', 'cron'] },
    { title: '系统', ids: ['backup', 'settings'] },
  ];

  /* 线性图标（24×24，stroke=currentColor）。emoji 在深色界面里又跳又廉价，换成这套。 */
  const NAV_ICONS = {
    chat: '<path d="M4 6.5A2.5 2.5 0 0 1 6.5 4h11A2.5 2.5 0 0 1 20 6.5v6a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4H6.5A2.5 2.5 0 0 1 4 12.5z"/>',
    devices: '<rect x="3" y="4.5" width="18" height="11.5" rx="1.5"/><path d="M9 20h6M12 16v4"/>',
    tasks: '<rect x="6" y="4.5" width="12" height="16" rx="2"/><path d="M9.5 4.5h5v2.5h-5z"/><path d="m9.5 12.5 2 2 3.5-4"/>',
    kb: '<path d="M12 6.6C10.4 5.1 7.9 4.5 4 4.5v13c3.9 0 6.4.6 8 2.1 1.6-1.5 4.1-2.1 8-2.1v-13c-3.9 0-6.4.6-8 2.1z"/><path d="M12 6.6v13"/>',
    memory: '<rect x="7" y="7" width="10" height="10" rx="1.6"/><path d="M10 3.5v3.5M14 3.5v3.5M10 17v3.5M14 17v3.5M3.5 10h3.5M3.5 14h3.5M17 10h3.5M17 14h3.5"/>',
    activity: '<path d="M3 12h4l2.5-6 4 12L16 12h5"/>',
    providers: '<path d="M13.5 3 5.5 13.5H11l-1 7.5 8.5-10.5H13z"/>',
    mcp: '<path d="M9 3.5v4M15 3.5v4"/><path d="M6.5 7.5h11v3.5a5.5 5.5 0 0 1-11 0z"/><path d="M12 16.5v4"/>',
    skills: '<path d="M12 3.2l2 5.3 5.3 2-5.3 2-2 5.3-2-5.3-5.3-2 5.3-2z"/><path d="M18.5 16.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z"/>',
    cron: '<circle cx="12" cy="12" r="8.4"/><path d="M12 7.4V12l3.2 2"/>',
    backup: '<rect x="3.5" y="4.5" width="17" height="4.6" rx="1.4"/><path d="M5.5 9.1V19a1.4 1.4 0 0 0 1.4 1.4h10.2A1.4 1.4 0 0 0 18.5 19V9.1"/><path d="M10 13h4"/>',
    settings: '<circle cx="12" cy="12" r="3.2"/><path d="M12 3.4v2.3M12 18.3v2.3M3.4 12h2.3M18.3 12h2.3M5.9 5.9l1.6 1.6M16.5 16.5l1.6 1.6M18.1 5.9l-1.6 1.6M7.5 16.5l-1.6 1.6"/>',
  };

  function iconSvg(id, cls) {
    const p = NAV_ICONS[id];
    if (!p) return '';
    return '<svg class="' + (cls || 'ic') + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" ' +
           'stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + p + '</svg>';
  }

  /* ---------------- 导航 ---------------- */
  function buildNav() {
    $('nav').innerHTML = NAV.map(g => {
      const items = g.ids.map(id => {
        const a = window.APP_BY_ID[id];
        if (!a) return '';
        return '<a data-app="' + a.id + '"><span class="i">' + (NAV_ICONS[a.id] || esc(a.icon)) + '</span>' +
               '<span class="n">' + esc(a.name) + '</span></a>';
      }).join('');
      return '<div class="grp">' + g.title + '</div>' + items;
    }).join('');
    $('nav').querySelectorAll('a[data-app]').forEach(el => {
      el.onclick = () => openApp(el.dataset.app);
    });
  }

  function syncNav() {
    $('nav').querySelectorAll('a[data-app]').forEach(el => {
      el.classList.toggle('on', el.dataset.app === current);
    });
  }

  /* ---------------- 打开应用（换内容区） ----------------
     每次用一个全新的 pane 承载应用内容：旧 pane 会被摘掉，
     应用里那些「靠 DOM 断链自动停轮询」的逻辑（pollWhileMounted）才能生效。
     摘之前再补一发 shell:closed，兼容显式监听的应用。 */
  function openApp(id) {
    const app = window.APP_BY_ID[id];
    if (!app) return;

    const prev = view.firstElementChild;
    if (prev) {
      prev.dispatchEvent(new Event('shell:closed'));
      if (typeof cleanup === 'function') { try { cleanup(); } catch (e) { /* 清理失败不影响切换 */ } }
      prev.remove();
    }
    cleanup = null;

    current = id;
    syncNav();
    $('viewTitle').textContent = app.name;
    $('viewIcon').innerHTML = iconSvg(id) || esc(app.icon);
    if (location.hash !== '#' + id) history.replaceState(null, '', '#' + id);
    view.scrollTop = 0;

    const pane = document.createElement('div');
    pane.className = 'pane';
    view.appendChild(pane);

    if (!app.render) { renderSoon(app, pane); return; }
    Promise.resolve()
      .then(() => app.render(pane))
      .then(fn => { if (typeof fn === 'function') cleanup = fn; })
      .catch(e => {
        pane.innerHTML = '<div class="empty err" style="padding:14px">渲染失败：' +
          esc(e && e.message ? e.message : e) + '</div>';
      });
  }

  /* ---------------- 提示 ---------------- */
  function toast(msg, kind) {
    const t = document.createElement('div');
    t.className = 'toast ' + (kind || '');
    t.textContent = msg;
    $('toasts').appendChild(t);
    setTimeout(() => t.remove(), 3800);
  }

  /* ---------------- 连接状态 / 待审批 / 时钟 ---------------- */
  async function conn() {
    const dot = $('connDot'), txt = $('connText');
    let r;
    try { r = await API.get('/api/health'); } catch (e) { r = { ok: false, error: String(e) }; }
    const d = (r && r.data) || {};
    if (r && r.ok) {
      dot.className = 'dot';
      txt.textContent = 'NAS 已连 · ' + (d.devicesOnline ?? '?') + ' 台在线';
    } else {
      dot.className = 'dot err';
      txt.textContent = '连不上后端';
    }
    const c = await API.localConfig();
    const st = $('serverText');
    st.textContent = c.server || '未配置后端';
    st.title = c.server || '未配置后端';
  }

  async function pollPending() {
    const r = await API.get('/api/state');
    const pill = $('approvalPill');
    const badge = $('nav').querySelector('a[data-app="tasks"] .badge');
    if (!r.ok) { pill.hidden = true; if (badge) badge.remove(); return; }
    const tasks = (r.data && r.data.tasks) || [];
    const n = tasks.filter(t => t.status === 'pending_approval').length;
    pill.hidden = n === 0;
    $('approvalText').textContent = n + ' 待审批';
    pill.onclick = () => openApp('tasks');
    const item = $('nav').querySelector('a[data-app="tasks"]');
    if (item) {
      let b = item.querySelector('.badge');
      if (n > 0) {
        if (!b) { b = document.createElement('span'); b.className = 'badge'; item.appendChild(b); }
        b.textContent = String(n);
      } else if (b) { b.remove(); }
    }
  }

  function clock() {
    const d = new Date(), p = n => String(n).padStart(2, '0');
    $('clock').textContent = p(d.getHours()) + ':' + p(d.getMinutes());
  }

  /* ---------------- 启动 ---------------- */
  function boot() {
    buildNav(); clock(); conn(); pollPending();
    setInterval(clock, 20000);
    setInterval(conn, 8000);
    setInterval(pollPending, 12000);

    /* 深链 #appid 直接打开某个应用；否则默认开「设备」 */
    const want = (location.hash || '').replace(/^#/, '').trim();
    openApp(window.APP_BY_ID[want] ? want : 'devices');
  }

  window.addEventListener('DOMContentLoaded', boot);
  window.addEventListener('hashchange', () => {
    const want = (location.hash || '').replace(/^#/, '').trim();
    if (want && want !== current && window.APP_BY_ID[want]) openApp(want);
  });

  return { openApp, toast, conn };
})();
