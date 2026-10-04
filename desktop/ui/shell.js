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

  /* ---------------- 导航 ---------------- */
  function buildNav() {
    $('nav').innerHTML = NAV.map(g => {
      const items = g.ids.map(id => {
        const a = window.APP_BY_ID[id];
        if (!a) return '';
        return '<a data-app="' + a.id + '"><span class="i">' + a.icon + '</span>' +
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
    $('viewIcon').textContent = app.icon;
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
