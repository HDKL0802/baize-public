/* 白泽桌面端 · 外壳（左侧导航 + 单视图）
   正常智能体界面：一侧导航、一个内容区；没有 Dock / 启动台 / 浮动窗口。
   没有外部依赖，全部原生 DOM。 */
'use strict';

window.Shell = (function () {
  const $ = id => document.getElementById(id);
  const view = $('view');

  /* ---------------- 主题：亮 / 暗 / 跟随系统 ----------------
     先按本地缓存同步打上 data-theme（避免亮色用户看到一瞬黑底），再向原生确认一次。 */
  const THEME_KEY = 'bz_theme';
  let themePref = 'system';
  try { themePref = localStorage.getItem(THEME_KEY) || 'system'; } catch (e) { /* 隐私模式忽略 */ }

  function applyTheme() {
    const sysLight = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
    const t = (themePref === 'light' || themePref === 'dark') ? themePref : (sysLight ? 'light' : 'dark');
    document.documentElement.setAttribute('data-theme', t);
  }
  applyTheme();

  function setThemePref(t) {
    themePref = (t === 'light' || t === 'dark') ? t : 'system';
    try { localStorage.setItem(THEME_KEY, themePref); } catch (e) { /* 忽略 */ }
    applyTheme();
  }
  async function loadTheme() {
    try {
      const r = await (await fetch('/api/local/theme')).json();
      if (r && r.theme) setThemePref(r.theme);
    } catch (e) { /* 读不到就用本地缓存 */ }
  }
  if (window.matchMedia) {
    try { window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', applyTheme); }
    catch (e) { /* 老 Edge 不支持 addEventListener，忽略 */ }
  }

  let current = null;      // 当前应用 id
  let cleanup = null;      // 当前应用返回的清理函数（可选）

  /* 导航分组（引用 apps.js 里的 id；这样不用给每个 app 加字段） */
  const NAV = [
    { title: '核心', ids: ['chat', 'devices', 'tasks'] },
    { title: '知识', ids: ['kb', 'memory', 'activity'] },
    { title: '协作', ids: ['accounts', 'conflicts'] },
    { title: '能力', ids: ['providers', 'mcp', 'channels', 'external', 'skills', 'plugins', 'persona', 'cron'] },
    { title: '系统', ids: ['observe', 'backup', 'settings'] },
  ];

  /* 线性图标（24×24，stroke=currentColor）。emoji 在深色界面里又跳又廉价，换成这套。 */
  const NAV_ICONS = {
    chat: '<path d="M4 6.5A2.5 2.5 0 0 1 6.5 4h11A2.5 2.5 0 0 1 20 6.5v6a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4H6.5A2.5 2.5 0 0 1 4 12.5z"/>',
    devices: '<rect x="3" y="4.5" width="18" height="11.5" rx="1.5"/><path d="M9 20h6M12 16v4"/>',
    tasks: '<rect x="6" y="4.5" width="12" height="16" rx="2"/><path d="M9.5 4.5h5v2.5h-5z"/><path d="m9.5 12.5 2 2 3.5-4"/>',
    kb: '<path d="M12 6.6C10.4 5.1 7.9 4.5 4 4.5v13c3.9 0 6.4.6 8 2.1 1.6-1.5 4.1-2.1 8-2.1v-13c-3.9 0-6.4.6-8 2.1z"/><path d="M12 6.6v13"/>',
    memory: '<rect x="7" y="7" width="10" height="10" rx="1.6"/><path d="M10 3.5v3.5M14 3.5v3.5M10 17v3.5M14 17v3.5M3.5 10h3.5M3.5 14h3.5M17 10h3.5M17 14h3.5"/>',
    activity: '<path d="M3 12h4l2.5-6 4 12L16 12h5"/>',
    observe: '<path d="M4.5 19.5v-6M9.5 19.5v-13M14.5 19.5v-9M19.5 19.5v-4"/><path d="M2.5 21.5h19"/>',
    shot: '<rect x="3" y="7" width="18" height="13" rx="2"/><path d="M8.6 7l1.2-2.2h4.4L15.4 7"/><circle cx="12" cy="13.4" r="3.3"/>',
    providers: '<path d="M13.5 3 5.5 13.5H11l-1 7.5 8.5-10.5H13z"/>',
    mcp: '<path d="M9 3.5v4M15 3.5v4"/><path d="M6.5 7.5h11v3.5a5.5 5.5 0 0 1-11 0z"/><path d="M12 16.5v4"/>',
    external: '<circle cx="6" cy="12" r="2.6"/><circle cx="18" cy="6" r="2.6"/><circle cx="18" cy="18" r="2.6"/><path d="M8.3 10.9 15.7 7.2M8.3 13.1l7.4 3.7"/>',
    channels: '<circle cx="12" cy="12" r="2"/><path d="M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4M4.9 4.9a10 10 0 0 0 0 14.2M19.1 4.9a10 10 0 0 1 0 14.2"/>',
    skills: '<path d="M12 3.2l2 5.3 5.3 2-5.3 2-2 5.3-2-5.3-5.3-2 5.3-2z"/><path d="M18.5 16.5l.8 2 2 .8-2 .8-.8 2-.8-2-2-.8 2-.8z"/>',
    plugins: '<rect x="3.5" y="3.5" width="7" height="7" rx="1.4"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.4"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.4"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.4"/>',
    persona: '<rect x="3.5" y="5" width="17" height="14" rx="2"/><circle cx="9" cy="10.6" r="2.1"/><path d="M5.7 16.4c.6-1.6 1.8-2.4 3.3-2.4s2.7.8 3.3 2.4"/><path d="M15 10.2h3.6M15 13.4h3.6"/>',
    cron: '<circle cx="12" cy="12" r="8.4"/><path d="M12 7.4V12l3.2 2"/>',
    backup: '<rect x="3.5" y="4.5" width="17" height="4.6" rx="1.4"/><path d="M5.5 9.1V19a1.4 1.4 0 0 0 1.4 1.4h10.2A1.4 1.4 0 0 0 18.5 19V9.1"/><path d="M10 13h4"/>',
    accounts: '<circle cx="9" cy="8.4" r="3.1"/><path d="M3.6 19.2c.5-3 2.8-4.7 5.4-4.7s4.9 1.7 5.4 4.7"/><path d="M16.2 5.5a3 3 0 0 1 0 5.9"/><path d="M17.6 19.2c-.3-1.9-1-3.3-2.1-4.2"/>',
    conflicts: '<path d="M12 3.8v16.4"/><path d="M5.5 7.5h13"/><path d="M5.5 7.5 3.4 13.4h4.2z"/><path d="M18.5 7.5l-2.1 5.9h4.2z"/><path d="M8.6 20.2h6.8"/>',
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
        return '<a data-app="' + a.id + '"><span class="i">' + (iconSvg(a.id) || esc(a.icon)) + '</span>' +
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

  /* ---------------- 迷你对话小窗（左键点悬浮球） ----------------
     原生侧把主窗口变成置顶小窗并跳到 #chat；这里按原生状态给 body 加 .mini、
     藏掉侧栏与顶栏（CSS 在 shell.css），并挂一个「工作模式」按钮切回去。 */
  let miniState = false;

  function ensureMiniBar() {
    if (document.getElementById('miniBar')) return;
    const bar = document.createElement('div');
    bar.id = 'miniBar';
    bar.innerHTML = '<span class="mb-t">白泽 · 对话</span>' +
      '<button class="btn ghost sm" id="miniExpand">⤢ 工作模式</button>';
    document.body.appendChild(bar);
    bar.querySelector('#miniExpand').onclick = async () => {
      await fetch('/api/local/ball/mini', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ on: false }),
      }).catch(() => {});
      toast('已切回工作模式', 'ok');
    };
  }
  function removeMiniBar() {
    const b = document.getElementById('miniBar');
    if (b) b.remove();
  }

  async function pollMini() {
    let r = {};
    try { r = await (await fetch('/api/local/ball/mini')).json(); } catch (e) { r = {}; }
    const on = !!(r && r.mini);
    if (on === miniState) return;
    miniState = on;
    document.body.classList.toggle('mini', on);
    if (on) { ensureMiniBar(); openApp('chat'); }
    else { removeMiniBar(); }
  }

  /* ---------------- 授权 / 审批弹窗（B4）----------------
     危险操作执行前，置顶弹一个确认框：操作描述 / 目标 / 影响范围 / 建议 + 确认·拒绝。
     无论当前在哪个应用里都会弹；一次只处理一条，其余排队。超时由后端按「拒绝」处理，
     这里只如实显示倒计时并说明「超时按拒绝」。批准依据分两类：
       - 工具调用（/api/agent/approvals）：可顺带「本会话不再问 / 永久放行」
       - 跨端任务（/api/state 里 pending_approval）：直接批 / 驳 */
  let approvalKey = null;      // 当前弹窗锁定的 item 键（kind:id）
  let approvalTick = null;     // 倒计时
  const decidedKeys = new Set(); // 本次会话已处理过的，避免重复弹

  function ensureApprovalModal() {
    let m = document.getElementById('approvalModal');
    if (m) return m;
    m = document.createElement('div');
    m.id = 'approvalModal';
    m.hidden = true;
    m.innerHTML = `
      <div class="am-box" role="dialog" aria-modal="true" aria-label="危险操作确认">
        <div class="am-head">
          <span class="am-badge">需要你确认</span>
          <span class="am-kind" id="amKind"></span>
          <span class="am-count" id="amCount"></span>
        </div>
        <h3 class="am-title" id="amTitle">—</h3>
        <div class="am-desc" id="amDesc"></div>
        <div class="am-args mono" id="amArgs"></div>
        <div class="am-meta">
          <div><b>影响范围</b><span id="amImpact">—</span></div>
          <div><b>建议</b><span id="amAdvice">—</span></div>
          <div><b>剩余时间</b><span id="amLeft">—</span></div>
        </div>
        <label class="am-remember" id="amRememberWrap" hidden>
          <span><input type="checkbox" id="amRememberSession"> 本会话内不再问这个工具</span>
          <span><input type="checkbox" id="amRememberAlways"> 永久放行（写进配置，可撤）</span>
        </label>
        <div class="am-actions">
          <button class="btn ghost" id="amReject">拒绝</button>
          <button class="btn" id="amApprove">批准并执行</button>
        </div>
        <div class="am-foot" id="amFoot">批准之前，什么都不会发生。超时未确认按「拒绝」处理，后端会记录在案。</div>
      </div>`;
    document.body.appendChild(m);
    $('amApprove').onclick = () => decideApproval(true);
    $('amReject').onclick = () => decideApproval(false);
    return m;
  }

  function showApproval(kind, it, total) {
    const m = ensureApprovalModal();
    approvalKey = kind + ':' + it.id;
    $('amKind').textContent = kind === 'task' ? '跨端任务' : '工具调用';
    $('amCount').textContent = total > 1 ? ('还有 ' + (total - 1) + ' 条排队') : '';
    if (kind === 'task') {
      $('amTitle').textContent = it.action || '（无动作名）';
      $('amDesc').textContent = '来源 ' + (it.origin || 'agent') + ' · 目标设备 ' + (it.deviceId || '?') + ' · ' + fmtTime(it.createdAt);
      $('amImpact').textContent = '这条动作会真的下发到设备「' + (it.deviceId || '?') + '」执行';
      $('amAdvice').textContent = '确认这台设备与这个动作是你派的，再放行；拿不准就拒绝';
    } else {
      $('amTitle').textContent = it.tool || '（未知工具）';
      $('amDesc').textContent = '白泽想调用这个工具' + (it.runId ? ' · 运行 ' + it.runId : '');
      $('amImpact').textContent = '只影响这台 NAS 上的后端（执行工具「' + it.tool + '」本身）';
      $('amAdvice').textContent = '看清参数是不是你要的；拿不准就拒绝，白泽会当成「你不许这么做」';
    }
    $('amArgs').textContent = JSON.stringify(it.args || {}, null, 2);
    $('amRememberWrap').hidden = (kind === 'task');
    $('amRememberSession').checked = false;
    $('amRememberAlways').checked = false;
    m.hidden = false;
    $('amApprove').disabled = false;
    $('amReject').disabled = false;
    startCountdown(it.expiresAt);
  }

  function startCountdown(expiresAt) {
    clearInterval(approvalTick);
    const el = $('amLeft');
    const tick = () => {
      if (!expiresAt) { el.textContent = '不限时（等你确认）'; el.classList.remove('err'); return; }
      const left = expiresAt - Date.now();
      if (left <= 0) { el.textContent = '已超时（后端按拒绝处理）'; el.classList.add('err'); return; }
      const s = Math.floor(left / 1000);
      el.classList.remove('err');
      el.textContent = s >= 60 ? (Math.floor(s / 60) + ' 分 ' + String(s % 60).padStart(2, '0') + ' 秒') : (s + ' 秒');
    };
    tick();
    approvalTick = setInterval(tick, 1000);
  }

  function closeApproval() {
    const m = document.getElementById('approvalModal');
    if (m) m.hidden = true;
  }

  async function decideApproval(ok) {
    const [kind, id] = String(approvalKey || '').split(':');
    if (!kind || !id) return;
    $('amApprove').disabled = true;
    $('amReject').disabled = true;
    let r;
    if (kind === 'task') {
      r = ok
        ? await API.post('/api/tasks/' + encodeURIComponent(id) + '/approve', { by: '桌面端' })
        : await API.post('/api/tasks/' + encodeURIComponent(id) + '/reject', { by: '桌面端', reason: '桌面端驳回' });
    } else {
      const remember = $('amRememberAlways').checked ? 'always' : ($('amRememberSession').checked ? 'session' : '');
      r = ok
        ? await API.post('/api/agent/approvals/' + encodeURIComponent(id) + '/approve', { by: '桌面端', remember: remember })
        : await API.post('/api/agent/approvals/' + encodeURIComponent(id) + '/reject', { by: '桌面端', reason: '桌面端拒绝' });
    }
    clearInterval(approvalTick);
    decidedKeys.add(kind + ':' + id);
    approvalKey = null;
    closeApproval();
    if (!r.ok) toast('操作失败：' + r.error, 'err');
    else if (kind === 'agent' && ok && $('amRememberAlways') && $('amRememberAlways').checked) toast('已批准，并永久放行该工具', 'ok');
    else toast(ok ? '已批准' : '已拒绝', ok ? 'ok' : 'warn');
    pollApprovals();
  }

  async function pollApprovals() {
    if (approvalKey) return; // 已经有弹窗在等，不抢
    const [r1, r2] = await Promise.all([API.get('/api/agent/approvals'), API.get('/api/state')]);
    const queue = [];
    if (r1.ok) {
      ((r1.data && r1.data.approvals) || [])
        .filter(a => a.status === 'pending' && !decidedKeys.has('agent:' + a.id))
        .forEach(a => queue.push({ kind: 'agent', it: a }));
    }
    if (r2.ok) {
      ((r2.data && r2.data.tasks) || [])
        .filter(t => t.status === 'pending_approval' && !decidedKeys.has('task:' + t.id))
        .forEach(t => queue.push({ kind: 'task', it: t }));
    }
    if (!queue.length) return;
    queue.sort((a, b) => (a.it.at || a.it.createdAt || 0) - (b.it.at || b.it.createdAt || 0)); // 先到先确认
    showApproval(queue[0].kind, queue[0].it, queue.length);
  }

  function clock() {
    const d = new Date(), p = n => String(n).padStart(2, '0');
    $('clock').textContent = p(d.getHours()) + ':' + p(d.getMinutes());
  }

  /* ---------------- 启动 ---------------- */
  function boot() {
    loadTheme();
    buildNav(); clock(); conn(); pollPending(); pollApprovals(); pollMini();
    setInterval(clock, 20000);
    setInterval(conn, 8000);
    setInterval(pollPending, 12000);
    setInterval(pollApprovals, 4000); // 审批弹窗：4 秒一次，尽量不让危险操作等太久
    setInterval(pollMini, 2500);      // 迷你小窗：原生侧随时可能切，跟紧点

    /* 深链 #appid 直接打开某个应用；否则默认开「设备」 */
    const want = (location.hash || '').replace(/^#/, '').trim();
    openApp(window.APP_BY_ID[want] ? want : 'devices');
  }

  window.addEventListener('DOMContentLoaded', boot);
  window.addEventListener('hashchange', () => {
    const want = (location.hash || '').replace(/^#/, '').trim();
    if (want && want !== current && window.APP_BY_ID[want]) openApp(want);
  });

  return { openApp, toast, conn, setThemePref, themePref: () => themePref };
})();
