/* 白泽桌面端 · 外壳（窗口管理 / Dock / 启动台 / 提示 / 连接状态）
   没有外部依赖，全部原生 DOM。方案 A：冷灰金属 + 底部 Dock + 无桌面图标。 */
'use strict';

window.Shell = (function () {
  const $ = id => document.getElementById(id);
  const stage = $('stage'), dockEl = $('dock'), launcherEl = $('launcher'), lgGrid = $('lgGrid');
  const winState = {};         // id -> { el }
  let zTop = 10;

  /* ---------------- 窗口 ---------------- */
  function focusWin(el) {
    Object.values(winState).forEach(w => w.el.classList.remove('active'));
    el.classList.add('active');
    el.style.zIndex = ++zTop;
    syncDock();
  }

  function openApp(id) {
    const app = window.APP_BY_ID[id];
    if (!app) return;
    if (winState[id]) { focusWin(winState[id].el); return; }

    const win = document.createElement('div');
    win.className = 'win';
    const n = Object.keys(winState).length;
    const w = Math.min(app.w || 880, window.innerWidth - 40);
    const h = Math.min(app.h || 600, window.innerHeight - 110);
    win.style.width = w + 'px';
    win.style.height = h + 'px';
    win.style.left = Math.max(12, Math.min(70 + n * 26, window.innerWidth - w - 20)) + 'px';
    win.style.top = Math.max(12, Math.min(34 + n * 22, window.innerHeight - h - 90)) + 'px';

    win.innerHTML =
      '<div class="titlebar">' +
        '<span>' + app.icon + '</span>' +
        '<div class="t">' + esc(app.name) + (app.render ? '' : '<em>D2 接入</em>') + '</div>' +
        '<div class="spacer"></div>' +
        '<button class="close" title="关闭">✕</button>' +
      '</div>' +
      '<div class="body"></div>';

    const body = win.querySelector('.body');
    if (app.render) {
      Promise.resolve().then(() => app.render(body)).catch(e => {
        body.innerHTML = '<div class="empty err" style="padding:14px">渲染失败：' + esc(e && e.message ? e.message : e) + '</div>';
      });
    } else {
      renderSoon(app, body);
    }

    // 拖动（标题栏） + 聚焦
    const bar = win.querySelector('.titlebar');
    let dragging = null;
    bar.addEventListener('mousedown', ev => {
      if (ev.target.tagName === 'BUTTON') return;
      focusWin(win);
      dragging = { x: ev.clientX - win.offsetLeft, y: ev.clientY - win.offsetTop };
      ev.preventDefault();
    });
    window.addEventListener('mousemove', ev => {
      if (!dragging) return;
      const nx = ev.clientX - dragging.x, ny = ev.clientY - dragging.y;
      win.style.left = Math.max(-w + 120, Math.min(nx, window.innerWidth - 120)) + 'px';
      win.style.top = Math.max(0, Math.min(ny, window.innerHeight - 60)) + 'px';
    });
    window.addEventListener('mouseup', () => { dragging = null; });

    win.addEventListener('mousedown', () => focusWin(win), true);
    win.querySelector('.close').onclick = () => closeApp(id);

    stage.appendChild(win);
    winState[id] = { el: win };
    focusWin(win);
    hideLauncher();
  }

  function closeApp(id) {
    const w = winState[id];
    if (!w) return;
    w.el.dispatchEvent(new Event('shell:closed'));
    w.el.remove();
    delete winState[id];
    syncDock();
  }

  /* ---------------- Dock ---------------- */
  function buildDock() {
    dockEl.innerHTML = '';
    const launch = document.createElement('div');
    launch.className = 'it'; launch.title = '启动台'; launch.textContent = '⊞';
    launch.onclick = toggleLauncher;
    dockEl.appendChild(launch);
    dockEl.insertAdjacentHTML('beforeend', '<div class="sep"></div>');

    window.APPS.filter(a => a.dock).forEach(a => {
      const it = document.createElement('div');
      it.className = 'it' + (a.render ? '' : ' soon');
      it.dataset.app = a.id; it.title = a.name; it.textContent = a.icon;
      it.onclick = () => openApp(a.id);
      dockEl.appendChild(it);
    });
  }
  function syncDock() {
    dockEl.querySelectorAll('[data-app]').forEach(it => {
      it.classList.toggle('on', !!winState[it.dataset.app]);
    });
  }

  /* ---------------- 启动台 ---------------- */
  function buildLauncher() {
    lgGrid.innerHTML = '';
    window.APPS.forEach(a => {
      const c = document.createElement('div');
      c.className = 'cell' + (a.render ? '' : ' soon');
      c.innerHTML = '<div class="g">' + a.icon + '</div><span>' + esc(a.name) + '</span>';
      c.onclick = () => openApp(a.id);
      lgGrid.appendChild(c);
    });
  }
  function showLauncher() { launcherEl.hidden = false; }
  function hideLauncher() { launcherEl.hidden = true; }
  function toggleLauncher() { launcherEl.hidden ? showLauncher() : hideLauncher(); }

  /* ---------------- 提示 ---------------- */
  function toast(msg, kind) {
    const t = document.createElement('div');
    t.className = 'toast ' + (kind || '');
    t.textContent = msg;
    $('toasts').appendChild(t);
    setTimeout(() => t.remove(), 3800);
  }

  /* ---------------- 顶栏 / 连接状态 ---------------- */
  function buildMenu() {
    const items = ['对话', '设备', '任务与审批', '知识库', '记忆', '设置'];
    $('menu').innerHTML = items.map(n => '<span>' + n + '</span>').join('');
    $('menu').querySelectorAll('span').forEach(s => {
      s.onclick = () => {
        const hit = window.APPS.find(a => a.name === s.textContent);
        if (hit) openApp(hit.id);
      };
    });
  }

  async function conn() {
    const dot = $('connDot'), txt = $('connText');
    let r;
    try { r = await API.get('/api/health'); } catch (e) { r = { ok: false, error: String(e) }; }
    if (r.ok) {
      const d = r.data || {};
      dot.className = 'dot';
      txt.textContent = 'NAS 已连 · ' + (d.devicesOnline ?? '?') + ' 台在线';
    } else {
      dot.className = 'dot err';
      txt.textContent = '连不上后端';
    }
    const c = await API.localConfig();
    $('serverText').textContent = c.server || '未配置后端';
    $('serverText').title = r.error || '';
  }

  async function pollPending() {
    const r = await API.get('/api/state');
    const pill = $('approvalPill');
    if (!r.ok) { pill.hidden = true; return; }
    const tasks = (r.data && r.data.tasks) || [];
    const n = tasks.filter(t => t.status === 'pending_approval').length;
    pill.hidden = n === 0;
    $('approvalText').textContent = n + ' 待审批';
    pill.onclick = () => openApp('tasks');
  }

  function clock() {
    const d = new Date(), p = n => String(n).padStart(2, '0');
    $('clock').textContent = p(d.getHours()) + ':' + p(d.getMinutes());
  }

  /* ---------------- 桌面右键 ---------------- */
  function ctxMenu() {
    const m = $('ctxmenu');
    document.querySelector('.desktop').addEventListener('contextmenu', ev => {
      if (ev.target.closest('.win')) return;
      ev.preventDefault();
      m.innerHTML = '<div data-a="launcher">打开启动台</div><div data-a="refresh">刷新连接状态</div><hr><div data-a="about">关于白泽</div>';
      m.hidden = false;
      m.style.left = Math.min(ev.clientX, window.innerWidth - 200) + 'px';
      m.style.top = Math.min(ev.clientY, window.innerHeight - 140) + 'px';
      m.querySelectorAll('div[data-a]').forEach(d => d.onclick = () => {
        m.hidden = true;
        if (d.dataset.a === 'launcher') showLauncher();
        else if (d.dataset.a === 'refresh') { conn(); pollPending(); toast('已刷新'); }
        else toast('白泽桌面端 · Go + WebView2 · 直连 NAS 后端', 'ok');
      });
    });
    document.addEventListener('click', () => { m.hidden = true; });
    launcherEl.addEventListener('click', ev => { if (ev.target === launcherEl) hideLauncher(); });
    document.addEventListener('keydown', ev => { if (ev.key === 'Escape') { hideLauncher(); m.hidden = true; } });
  }

  /* ---------------- 启动 ---------------- */
  function boot() {
    buildMenu(); buildDock(); buildLauncher(); ctxMenu(); clock();
    conn(); pollPending();
    setInterval(clock, 20000);
    setInterval(conn, 8000);
    setInterval(pollPending, 12000);

    // 首次进入直接开「设备」（最直观），并给一句提示
    setTimeout(() => openApp('devices'), 250);
    setTimeout(() => toast('按 ⊞ 或右键桌面可以打开其它应用', 'ok'), 900);
  }

  window.addEventListener('DOMContentLoaded', boot);
  return { openApp, toast, conn };
})();
