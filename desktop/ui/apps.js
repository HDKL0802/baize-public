/* 白泽桌面端 · 数据层 + 应用清单
   - API.call 走本机服务 /api/be/*，由 Go 侧代理到 NAS（令牌不进网页）
   - APPS 里每个应用只负责把自己画进给定的容器
   已做实 12 个：对话 / 设备 / 设置 / 任务与审批 / 知识库 / 记忆 / 模型通道 / MCP 服务 /
   技能 / 定时任务 / 备份与恢复 / 活动追踪。
   后 9 个的 render 在 apps2.js 末尾回填（直接写 render: renderXxx 会是前向引用，见那边的说明）。 */
'use strict';

window.API = {
  async call(path, method, body) {
    let r;
    try {
      r = await fetch('/api/be' + path, {
        method: method || 'GET',
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch (e) {
      return { ok: false, error: '调本机服务失败：' + (e && e.message) };
    }
    const text = await r.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (e) { data = { raw: text }; }
    if (!r.ok) return { ok: false, error: (data && data.error) || ('HTTP ' + r.status), data };
    return { ok: true, data };
  },
  get(p) { return this.call(p, 'GET'); },
  post(p, b) { return this.call(p, 'POST', b); },
  async localConfig() {
    try { return await (await fetch('/api/local/config')).json(); }
    catch (e) { return { ok: false, error: String(e) }; }
  },
  async saveConfig(server, token) {
    try {
      return await (await fetch('/api/local/config', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ server: server, token: token }),
      })).json();
    } catch (e) { return { ok: false, error: String(e) }; }
  },
};

function el(html) { const d = document.createElement('div'); d.innerHTML = html.trim(); return d.firstElementChild; }
function esc(s) { return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function fmtTime(ts) { if (!ts) return '—'; const d = new Date(ts), p = n => String(n).padStart(2, '0'); return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`; }

/* 本机设备状态（桌面端自己也注册成设备） */
async function localDeviceInfo() {
  try { const r = await (await fetch('/api/local/device')).json(); return (r && r.device) || {}; }
  catch (e) { return {}; }
}
async function localDeviceId() { return (await localDeviceInfo()).deviceId || ''; }

/* ---------------- 应用清单 ---------------- */
window.APPS = [
  { id: 'chat', name: '对话', icon: '💬', w: 900, h: 620, render: renderChat },
  { id: 'devices', name: '设备', icon: '🖥️', w: 1000, h: 620, render: renderDevices },
  { id: 'settings', name: '设置', icon: '⚙️', w: 720, h: 540, render: renderSettings },

  { id: 'tasks', name: '任务与审批', icon: '📋', w: 900, h: 600 },
  { id: 'kb', name: '知识库', icon: '📚', w: 980, h: 640 },
  { id: 'memory', name: '记忆', icon: '🧠', w: 900, h: 600 },
  { id: 'providers', name: '模型通道', icon: '✨', w: 860, h: 560 },
  { id: 'mcp', name: 'MCP 服务', icon: '🔌', w: 820, h: 560 },
  { id: 'skills', name: '技能', icon: '🧩', w: 820, h: 560 },
  { id: 'cron', name: '定时任务', icon: '⏰', w: 820, h: 520 },
  { id: 'backup', name: '备份与恢复', icon: '🗄️', w: 820, h: 520 },
  { id: 'activity', name: '活动追踪', icon: '📈', w: 760, h: 520 },
];
/* 注意：这 9 个的 render 由 apps2.js 回填（见那个文件末尾）。
   写成 render: renderXxx 会是前向引用 —— apps2.js 后加载，这里求值时就 ReferenceError，
   整个 window.APPS 都建不起来（导航空白、内容区不渲染）。 */

window.APP_BY_ID = {};
window.APPS.forEach(a => { window.APP_BY_ID[a.id] = a; });

/* ---------------- 占位（D2 接入） ---------------- */
function renderSoon(app, root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>${esc(app.name)}</h3>
      <div class="sub">这一块排在 D2：先跑通外壳 + 对话/设备/设置，再逐个接上控制台的功能。</div>
      <div class="empty">接口已经就绪（Go 侧统一代理 /api/be/*），接上去只是画界面的事。</div>
    </div>`;
}

/* ---------------- 设置 ---------------- */
async function renderSettings(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>连接</h3>
      <div class="sub">桌面端直连 NAS 后端；令牌只存在本机（%APPDATA%\\白泽\\desktop.json），不下发给网页。</div>
      <div class="fields" style="grid-template-columns:1fr 1fr">
        <div><label>后端地址</label><input id="setServer" placeholder="http://192.168.1.100:8787"></div>
        <div><label>配对令牌（留空 = 不改）</label><input id="setToken" type="password" placeholder="留空则保留已保存的"></div>
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn" id="setSave">保存</button>
        <button class="btn ghost" id="setTest">连接测试</button>
        <span id="setState" class="sub" style="margin:0"></span>
      </div>

      <div class="sect">
        <h3>当前状态</h3>
        <div id="setInfo" class="pre">读取中…</div>
      </div>

      <div class="sect">
        <h3>本机设备（这台电脑）</h3>
        <div class="sub">桌面端自己也注册成设备，可以被 NAS 派活；只读能力：ping / sys.info / window.now / fs.stat（不做删除）。</div>
        <div id="setDev" class="pre">读取中…</div>
      </div>

      <div class="sect">
        <h3>常驻与自启</h3>
        <div class="sub">点窗口的 ✕ <b>不会退出</b> —— 只是隐藏到托盘，设备连接不掉；
          要真正退出用下面的按钮（或托盘图标右键菜单）。</div>
        <div style="display:flex;gap:12px;align-items:center;margin-bottom:10px">
          <label class="sub" style="margin:0"><input type="checkbox" id="setAuto"> 开机自启（写当前用户的 Run 键，不需要管理员）</label>
          <span class="sub" id="setAutoMsg" style="margin:0"></span>
        </div>
        <button class="btn ghost" id="setQuit">退出白泽桌面端</button>
      </div>

      <div class="sect">
        <h3>版本与更新</h3>
        <div class="sub">当前版本 <b id="upCur">…</b> · 更新包由 NAS 分发（后端的 <span class="mono">/dl/</span>）</div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn ghost" id="upCheck">检查更新</button>
          <button class="btn" id="upApply" hidden>下载并重启</button>
          <span class="sub" id="upMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>关于</h3>
        <div class="sub">白泽桌面端（Go + WebView2，纯 Go 无 cgo）· 冷色暗色直角<br>
          控制台 12 个应用已全部接入；本机作为设备的能力已接（只读）。<br>
          已接：开机自启、关窗隐藏到托盘、自动更新、NSIS 安装包。</div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const refresh = async () => {
    const c = await API.localConfig();
    $i('setServer').value = c.server || '';
    $i('setInfo').textContent =
      '后端地址：' + (c.server || '（未配置）') + '\n' +
      '令牌：' + (c.tokenSet ? '已保存' : '未保存');

    const dv = await localDeviceInfo();
    $i('setDev').textContent =
      '设备 id：' + (dv.deviceId || '（未生成）') + '\n' +
      '连接：' + (dv.connected ? '在线（可被派活）' : (dv.enabled ? '未连上' : '未启动')) +
      (dv.lastError ? ' · ' + dv.lastError : '') + '\n' +
      '能力：' + ((dv.caps || []).join(' / ') || '—') + ' · 版本 ' + (dv.version || '—');
    const auto = await (await fetch('/api/local/autostart')).json().catch(() => ({}));
    $i('setAuto').checked = !!auto.enabled;
    $i('upCur').textContent = dv.version || '—';
    if (c.server) await test();
  };
  const test = async () => {
    $i('setState').textContent = '测试中…';
    const r = await API.get('/api/health');
    if (r.ok) {
      const d = r.data || {};
      $i('setState').innerHTML = `<span class="ok">连通 · 后端 ${esc(d.version || '?')} · 在线设备 ${esc(d.devicesOnline ?? '?')}</span>`;
    } else {
      $i('setState').innerHTML = `<span class="err">${esc(r.error || '连不上')}</span>`;
    }
  };

  $i('setSave').onclick = async () => {
    const s = $i('setServer').value.trim(), t = $i('setToken').value;
    const r = await API.saveConfig(s, t);
    if (!r.ok) { $i('setState').innerHTML = `<span class="err">${esc(r.error || '保存失败')}</span>`; return; }
    $i('setToken').value = '';
    $i('setState').innerHTML = '<span class="ok">已保存</span>';
    Shell.conn();
    refresh();
  };
  $i('setTest').onclick = test;

  $i('setAuto').onchange = async () => {
    const want = $i('setAuto').checked;
    const r = await (await fetch('/api/local/autostart', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: want }),
    })).json().catch(() => ({}));
    if (r && r.ok) {
      $i('setAuto').checked = !!r.enabled;
      $i('setAutoMsg').innerHTML = `<span class="ok">${r.enabled ? '已开启' : '已关闭'}</span>`;
    } else {
      $i('setAuto').checked = !want;
      $i('setAutoMsg').innerHTML = `<span class="err">${esc((r && r.error) || '改不了')}</span>`;
    }
  };
  $i('setQuit').onclick = async () => {
    if (!confirm('退出白泽桌面端？设备会离线，直到下次启动。')) return;
    await fetch('/api/local/quit', { method: 'POST' });
  };

  $i('upCheck').onclick = async () => {
    $i('upMsg').textContent = '检查中…';
    const r = await (await fetch('/api/local/update/check')).json().catch(() => ({}));
    if (!r || !r.ok) {
      $i('upApply').hidden = true;
      $i('upMsg').innerHTML = `<span class="err">${esc((r && r.error) || '检查失败')}</span>`;
      return;
    }
    if (r.hasUpdate) {
      $i('upMsg').innerHTML = `<span class="ok">有新版本 ${esc(r.latest)}</span>${r.notes ? '：' + esc(r.notes) : ''}`;
      $i('upApply').hidden = false;
    } else {
      $i('upApply').hidden = true;
      $i('upMsg').innerHTML = `<span class="ok">已是最新（${esc(r.latest || '—')}）</span>`;
    }
  };
  $i('upApply').onclick = async () => {
    if (!confirm('下载并重启到新版本？会短暂断线（设备会先离线再回来）。')) return;
    $i('upApply').disabled = true;
    $i('upMsg').textContent = '下载并替换中…';
    const r = await (await fetch('/api/local/update/apply', { method: 'POST' })).json().catch(() => ({}));
    if (r && r.ok) { $i('upMsg').innerHTML = `<span class="ok">已升级到 ${esc(r.version || '')}，正在重启…</span>`; return; }
    $i('upApply').disabled = false;
    $i('upMsg').innerHTML = `<span class="err">${esc((r && r.error) || '升级失败')}</span>`;
  };

  refresh();
}

/* ---------------- 设备 ---------------- */
async function renderDevices(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>接入的设备</h3>
      <div class="sub">电脑 / 手机 / NAS 注册上来的执行端。点「派活」把任务发到某台设备。</div>
      <div id="devList"><div class="empty">读取中…</div></div>

      <div class="sect">
        <h3>待审批</h3>
        <div class="sub">危险动作（删除、命令、跨端执行）要人工放行后才真的动手。</div>
        <div id="devPending"><div class="empty">读取中…</div></div>
      </div>

      <div class="sect">
        <h3>派活</h3>
        <div class="fields">
          <div><label>目标设备</label><select id="dvDev"></select></div>
          <div><label>动作</label><input id="dvAction" placeholder="例如 ping / todo.list / fs_list"></div>
        </div>
        <div><label>参数（JSON，可留空）</label><input id="dvArgs" placeholder='{"limit":10}'></div>
        <div style="margin-top:10px;display:flex;gap:8px;align-items:center">
          <button class="btn" id="dvSend">派发任务</button>
          <span id="dvState" class="sub" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const refresh = async () => {
    const r = await API.get('/api/state');
    if (!r.ok) {
      $i('devList').innerHTML = `<div class="empty err">读不到：${esc(r.error || '')}</div>`;
      $i('devPending').innerHTML = '';
      return;
    }
    const localId = await localDeviceId();
    const d = r.data || {};
    const devs = Array.isArray(d.devices) ? d.devices : [];
    const sel = $i('dvDev');
    sel.innerHTML = devs.filter(x => x.online).map(x => `<option value="${esc(x.id)}">${esc(x.name || x.id)}（${esc(x.os || '')}）</option>`).join('')
      || '<option value="">（没有在线设备）</option>';

    $i('devList').innerHTML = devs.length ? devs.map(x => `
      <div class="row">
        <div class="who"><b>${esc(x.name || x.id)}</b><span class="mono">${esc(x.id)} · ${esc(x.os || '')}/${esc(x.arch || '')} · v${esc(x.version || '?')}</span></div>
        <div class="tags">
          ${x.id === localId ? '<span class="tag on">本机</span>' : ''}
          <span class="tag ${x.online ? 'on' : 'off'}">${x.online ? '在线' : '离线'}</span>
          ${(x.caps || []).map(c => `<span class="tag">${esc(c)}</span>`).join('')}
        </div>
      </div>`).join('') : '<div class="empty">还没有设备注册上来</div>';

    const tasks = Array.isArray(d.tasks) ? d.tasks : [];
    const pend = tasks.filter(t => t.status === 'pending_approval');
    $i('devPending').innerHTML = pend.length ? pend.map(t => `
      <div class="row">
        <div class="who"><b>${esc(t.action)}</b><span class="mono">${esc(t.id)} · → ${esc(t.deviceId)} · ${fmtTime(t.createdAt)}</span></div>
        <div class="tags">
          <button class="btn sm" data-ok="${esc(t.id)}">批准</button>
          <button class="btn ghost sm" data-no="${esc(t.id)}">拒绝</button>
        </div>
      </div>`).join('') : '<div class="empty">没有在等人工放行的任务</div>';

    $i('devPending').querySelectorAll('[data-ok]').forEach(b => b.onclick = async () => {
      const r2 = await API.post('/api/tasks/' + encodeURIComponent(b.dataset.ok) + '/approve', {});
      Shell.toast(r2.ok ? '已批准' : ('批准失败：' + r2.error), r2.ok ? 'ok' : 'err'); refresh();
    });
    $i('devPending').querySelectorAll('[data-no]').forEach(b => b.onclick = async () => {
      const r2 = await API.post('/api/tasks/' + encodeURIComponent(b.dataset.no) + '/reject', { reason: '桌面端驳回' });
      Shell.toast(r2.ok ? '已驳回' : ('驳回失败：' + r2.error), r2.ok ? 'ok' : 'err'); refresh();
    });
  };

  $i('dvSend').onclick = async () => {
    const deviceId = $i('dvDev').value, action = $i('dvAction').value.trim();
    if (!deviceId || !action) { $i('dvState').innerHTML = '<span class="err">设备与动作都要填</span>'; return; }
    let args = undefined;
    const raw = $i('dvArgs').value.trim();
    if (raw) { try { args = JSON.parse(raw); } catch (e) { $i('dvState').innerHTML = '<span class="err">参数不是合法 JSON</span>'; return; } }
    $i('dvState').textContent = '派发中…';
    const r = await API.post('/api/tasks', { deviceId, action, args, origin: 'user' });
    if (r.ok) { $i('dvState').innerHTML = `<span class="ok">已派发${r.data && r.data.needApproval ? '（等审批）' : ''}</span>`; }
    else { $i('dvState').innerHTML = `<span class="err">${esc(r.error || '派发失败')}</span>`; }
    refresh();
  };
  refresh();
}

/* ---------------- 对话 ---------------- */
async function renderChat(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>跟白泽说话</h3>
      <div class="sub">一句话派活，跑完结果落到下面。危险操作会在「待审批」里等你放行。</div>
      <textarea id="chGoal" placeholder="例如：看看手机上还有哪些待办"></textarea>
      <div style="margin-top:10px;display:flex;gap:8px;align-items:center">
        <button class="btn" id="chSend">派给白泽</button>
        <span id="chState" class="sub" style="margin:0"></span>
      </div>

      <div class="sect">
        <h3>当前状态</h3>
        <div id="chNow" class="pre">—</div>
      </div>
      <div class="sect">
        <h3>最近运行</h3>
        <div id="chRuns"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let timer = null;

  const load = async () => {
    const st = await API.get('/api/agent/state');
    if (st.ok) {
      const s = st.data || {};
      const act = s.activity || {};
      $i('chNow').textContent =
        '运行中：' + (s.running ? '是' : '否') +
        (s.currentRunId ? '\n当前运行：' + s.currentRunId : '') +
        (act && act.phase ? '\n阶段：' + act.phase + (act.detail ? '（' + act.detail + '）' : '') : '');
    } else {
      $i('chNow').innerHTML = `<span class="err">读不到运行状态：${esc(st.error || '')}</span>`;
    }

    const runs = await API.get('/api/agent/runs?limit=8');
    if (runs.ok) {
      const list = (runs.data && (runs.data.runs || runs.data.items)) || (Array.isArray(runs.data) ? runs.data : []);
      $i('chRuns').innerHTML = list.length ? list.map(r => `
        <div class="row">
          <div class="who"><b>${esc(r.goal || r.title || '(无目标)')}</b>
          <span class="mono">${esc(r.id || '')} · ${esc(r.status || '')} · ${esc(r.steps ?? '?')} 步 · ${esc(r.totalTokens ?? '?')} token</span></div>
          <div class="tags"><span class="tag ${r.status === 'done' ? 'on' : (r.status === 'failed' ? 'warn' : '')}">${esc(r.status || '')}</span></div>
        </div>`).join('') : '<div class="empty">还没有运行记录</div>';
    } else {
      $i('chRuns').innerHTML = `<div class="empty err">读不到运行记录：${esc(runs.error || '')}</div>`;
    }
  };

  $i('chSend').onclick = async () => {
    const goal = $i('chGoal').value.trim();
    if (!goal) { $i('chState').innerHTML = '<span class="err">先写一句要做什么</span>'; return; }
    $i('chState').textContent = '已派发，跑着…';
    const r = await API.post('/api/agent/run', { goal, wait: false });
    if (!r.ok) { $i('chState').innerHTML = `<span class="err">${esc(r.error || '派发失败')}</span>`; return; }
    const id = (r.data && (r.data.runId || r.data.id)) || '';
    $i('chState').innerHTML = `<span class="ok">已派发${id ? ' · ' + esc(id) : ''}</span>`;
    $i('chGoal').value = '';
    load();
  };

  await load();
  timer = setInterval(load, 3000);
  root.addEventListener('shell:closed', () => clearInterval(timer));
}
