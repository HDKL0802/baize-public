/* 白泽桌面端 · 数据层 + 应用清单
   - API.call 走本机服务 /api/be/*，由 Go 侧代理到 NAS（令牌不进网页）
   - APPS 里每个应用只负责把自己画进给定的容器
   已做实 13 个：对话 / 设备 / 设置 / 任务与审批 / 知识库 / 记忆 / 模型通道 / MCP 服务 /
   技能 / 人设 / 定时任务 / 备份与恢复 / 活动追踪。
   后 10 个的 render 在 apps2.js 末尾回填（直接写 render: renderXxx 会是前向引用，见那边的说明）；
   协作 2 个在 apps3.js、插件市场在 apps4.js、可观测性在 apps5.js、外部 Agent 在 apps6.js、
   频道在 apps7.js，同样在文件末尾回填。截图提问不再是主界面里的一个「页」——
   它是独立进程的临时浮窗（`-float=extract|translate|chat`，见 float_windows.go / float.html）。 */
'use strict';

/* 登录会话（多用户）：后端按它决定"以谁的身份访问数据"。
   存 localStorage，只在 /api/be 的请求头上带（配对令牌永远不进网页）。 */
const SESSION_KEY = 'bz_session';
function sessionToken() { try { return localStorage.getItem(SESSION_KEY) || ''; } catch (e) { return ''; } }
function setSessionToken(t) {
  try { t ? localStorage.setItem(SESSION_KEY, t) : localStorage.removeItem(SESSION_KEY); } catch (e) { /* 隐私模式等忽略 */ }
}
window.BZ = { sessionToken: sessionToken, setSessionToken: setSessionToken };

/* 文件 → base64（不含 data: 前缀），共享文档上传用 */
function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onerror = () => reject(new Error('读文件失败'));
    fr.onload = () => {
      const s = String(fr.result || '');
      const i = s.indexOf(',');
      resolve(i >= 0 ? s.slice(i + 1) : s);
    };
    fr.readAsDataURL(file);
  });
}
/* base64 → 触发浏览器下载 */
function downloadBase64(name, b64) {
  const bin = atob(b64);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  const url = URL.createObjectURL(new Blob([buf]));
  const a = document.createElement('a');
  a.href = url; a.download = name || 'download';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 4000);
}

window.API = {
  async call(path, method, body) {
    const headers = {};
    if (body) headers['Content-Type'] = 'application/json';
    // 登录会话（多用户）：后端按它决定"以谁的身份访问数据"。
    // 只带在 /api/be 的请求上；本机侧接口（/api/local/*）不需要也不该带。
    const s = sessionToken();
    if (s) headers['X-Baize-Session'] = s;
    let r;
    try {
      r = await fetch('/api/be' + path, {
        method: method || 'GET',
        headers: headers,
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

  /* ---- 多用户：登录 / 登出 / 当前身份 ---- */
  async login(name, password) {
    const r = await this.post('/api/agent/auth/login', { name: name, password: password });
    if (r.ok && r.data && r.data.token) setSessionToken(r.data.token);
    return r;
  },
  async logout() {
    try { await this.post('/api/agent/auth/logout', {}); } catch (e) { /* 登出失败也要清本地 */ }
    setSessionToken('');
  },
  whoami() { return this.get('/api/agent/auth/whoami'); },
  async localConfig() {
    try {
      const r = await (await fetch('/api/local/config')).json();
      // 原生侧返回 {ok, config:{server,tokenSet,guiPerm,guiScopes,disclaimerAck,dataDir,...}}；
      // 这里摊平一层，调用方直接用 c.server / c.guiPerm。
      if (r && r.config) return Object.assign({ ok: r.ok !== false }, r.config);
      return r && typeof r === 'object' ? r : { ok: false };
    } catch (e) { return { ok: false, error: String(e) }; }
  },
  async saveConfig(server, token) {
    try {
      const r = await (await fetch('/api/local/config', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ server: server, token: token }),
      })).json();
      if (r && r.config) return Object.assign({ ok: r.ok !== false }, r.config);
      return r || { ok: false };
    } catch (e) { return { ok: false, error: String(e) }; }
  },
  // 本机 JSON 小工具：/api/local/* 都是这一层（含桌面控制权限、数据目录、自启、退出）
  async localGet(path) {
    try { return await (await fetch('/api/local' + path)).json(); }
    catch (e) { return { ok: false, error: String(e) }; }
  },
  async localPost(path, body) {
    try {
      return await (await fetch('/api/local' + path, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
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
  { id: 'memory', name: '记忆星图', icon: '🧠', w: 980, h: 640 },
  { id: 'providers', name: '模型通道', icon: '✨', w: 860, h: 560 },
  { id: 'mcp', name: 'MCP 服务', icon: '🔌', w: 820, h: 560 },
  { id: 'external', name: '外部 Agent', icon: '🤖', w: 1000, h: 700 },
  { id: 'channels', name: '频道', icon: '📻', w: 1040, h: 700 },
  { id: 'skills', name: '技能', icon: '🧩', w: 820, h: 560 },
  { id: 'plugins', name: '插件市场', icon: '📦', w: 1000, h: 660 },
  { id: 'persona', name: '人设', icon: '🎭', w: 940, h: 640 },
  { id: 'cron', name: '定时任务', icon: '⏰', w: 820, h: 520 },
  { id: 'backup', name: '备份与恢复', icon: '🗄️', w: 820, h: 520 },
  { id: 'activity', name: '活动追踪', icon: '📈', w: 760, h: 520 },
  { id: 'observe', name: '可观测性', icon: '📡', w: 1040, h: 700 },

  { id: 'accounts', name: '账号与共享', icon: '👥', w: 1000, h: 660 },
  { id: 'conflicts', name: '冲突协商', icon: '⚖️', w: 1080, h: 680 },
];
/* 注意：除前 3 个外，render 由 apps2.js / apps3.js 回填（见那些文件末尾）。
   写成 render: renderXxx 会是前向引用 —— 后加载的文件此刻还没定义，
   对象字面量求值会直接 ReferenceError，整个 window.APPS 都建不起来（导航空白、内容区不渲染）。 */

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
        <div class="sub">桌面端自己也注册成设备，可以被 NAS 派活；<b>能做什么由下面的「桌面控制」权限决定</b>。</div>
        <div id="setDev" class="pre">读取中…</div>
      </div>

      <div class="sect">
        <h3>桌面控制（智能体能对这台电脑做什么）</h3>
        <div class="sub">这是<b>设备侧真正的闸门</b>：权限不够的能力，本机根本不会上报给后端，后端也就派不过来。
          高权限档（只读指定盘 / 完全访问）需要先确认下面的风险提示。</div>
        <div id="permLevels" style="display:grid;gap:6px;margin-bottom:10px">读取中…</div>
        <div class="fields" id="permScopeWrap" style="grid-template-columns:1fr" hidden>
          <div><label>允许的范围（每行一条；第 2 档填目录如 D:\\文档，第 3 档填盘如 D:\\）</label>
            <textarea id="permScopes" style="min-height:70px" placeholder="D:\\文档&#10;E:\\项目"></textarea></div>
        </div>
        <div class="fields" id="permDiskWrap" style="grid-template-columns:1fr" hidden>
          <div><label>本机可选的盘（勾选允许读取的盘）</label><div id="permDisks" class="sub" style="margin:4px 0 0"></div></div>
        </div>
        <label class="sub" id="permAckWrap" style="display:flex;gap:6px;align-items:flex-start;margin:10px 0" hidden>
          <input type="checkbox" id="permAck">
          <span>我已了解：放开高权限后，智能体可以读取（甚至删除）本机文件；<b>由此产生的一切后果由我自己承担</b>，
            与白泽作者无关。</span></label>
        <div style="display:flex;gap:8px;align-items:center">
          <button class="btn" id="permSave">保存桌面控制权限</button>
          <span class="sub" id="permMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>数据目录</h3>
        <div class="sub">桌面端的配置与日志放哪儿。<b>不是必须放 C 盘</b> —— 默认在
          <span class="mono" id="ddDefault">…</span>，你可以改到 D 盘或任意文件夹（改完立即搬过去）。
          <br>（NAS 上后端的数据目录由部署时决定，改法见项目文档 §4.1。）</div>
        <div class="fields" style="grid-template-columns:1fr 150px">
          <div><label>数据目录</label><input id="ddPath" placeholder="例如 D:\\白泽数据"></div>
          <div style="display:flex;align-items:flex-end"><button class="btn ghost" id="ddReset">恢复默认</button></div>
        </div>
        <div style="display:flex;gap:8px;align-items:center">
          <button class="btn" id="ddSave">保存并迁移</button>
          <span class="sub" id="ddMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>常驻与自启</h3>
        <div class="sub">点窗口的 ✕ <b>不会退出</b> —— 只是隐藏到托盘，设备连接不掉；
          要真正退出用下面的按钮（或托盘图标右键菜单）。</div>
        <div style="display:flex;gap:12px;align-items:center;margin-bottom:10px">
          <label class="sub" style="margin:0"><input type="checkbox" id="setAuto"> 开机自启（写当前用户的 Run 键，不需要管理员）</label>
          <span class="sub" id="setAutoMsg" style="margin:0"></span>
        </div>
        <div style="display:flex;gap:12px;align-items:center;margin-bottom:10px">
          <label class="sub" style="margin:0"><input type="checkbox" id="setBall"> 显示桌面悬浮球</label>
          <span class="sub" id="setBallMsg" style="margin:0"></span>
        </div>
        <div class="sub" style="margin:-2px 0 10px">悬浮球丢了 / 被关掉了，在这里勾回来即可。球会<b>贴着屏幕最近的边</b>待着（拖动松手自动吸附），
          单击唤起「对话浮窗」、右键出菜单、双击开完整窗口。</div>
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
        <div style="display:flex;gap:12px;align-items:center;margin-top:12px;flex-wrap:wrap">
          <label class="sub" style="margin:0"><input type="checkbox" id="upAuto" style="margin-right:6px">自动更新（后台定时检查，有新版就自动下载并重启）</label>
          <label class="sub" style="margin:0">检查间隔
            <input id="upAutoMin" type="number" min="10" step="10" style="width:88px;display:inline-block;margin:0 4px"> 分钟</label>
          <button class="btn ghost sm" id="upAutoSave">保存</button>
          <span class="sub" id="upAutoMsg" style="margin:0"></span>
        </div>
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
    const bv = await (await fetch('/api/local/ball/visible')).json().catch(() => ({}));
    $i('setBall').checked = bv.visible !== false;
    $i('upCur').textContent = dv.version || '—';
    const au = await (await fetch('/api/local/update/auto')).json().catch(() => ({}));
    $i('upAuto').checked = !!au.enabled;
    $i('upAutoMin').value = au.minutes || 360;
    if (c.server) await test();
  };

  /* ---------- 桌面控制权限 ---------- */
  let permState = { perm: 1, scopes: [], levels: [], volumes: [], disclaimerAck: false };
  const permLevelEl = lv =>
    `<label class="row" style="align-items:flex-start;padding:6px 0">
       <input type="radio" name="permLv" value="${lv.level}" style="margin-top:3px">
       <span class="who"><b>${esc(lv.title)}${lv.risky ? ' <span class="tag warn">高风险</span>' : ''}</b>
       <span>${esc(lv.desc)}</span></span></label>`;
  const permDirty = () => {
    const lv = permChosen();
    const needScope = (permState.levels.find(x => x.level === lv) || {}).needScope;
    const risky = (permState.levels.find(x => x.level === lv) || {}).risky;
    $i('permScopeWrap').hidden = !needScope;
    $i('permDiskWrap').hidden = lv !== 3;      // 3 = 只读指定盘：给盘符勾选
    $i('permAckWrap').hidden = !risky;
  };
  const permChosen = () => {
    const el = root.querySelector('input[name=permLv]:checked');
    return el ? Number(el.value) : permState.perm;
  };
  const permScopesFromUI = () => {
    const lv = permChosen();
    if (lv === 3) {
      const out = [];
      root.querySelectorAll('#permDisks input[type=checkbox]').forEach(c => { if (c.checked) out.push(c.value); });
      return out;
    }
    return $i('permScopes').value.split('\n').map(s => s.trim()).filter(Boolean);
  };

  const loadPerm = async () => {
    const r = await API.localGet('/perm');
    if (!r || !r.ok) { $i('permLevels').innerHTML = `<span class="err">${esc((r && r.error) || '读不到')}</span>`; return; }
    permState = { perm: r.perm | 0, scopes: r.scopes || [], levels: r.levels || [], volumes: r.volumes || [], disclaimerAck: !!r.disclaimerAck };
    $i('permLevels').innerHTML = permState.levels.map(permLevelEl).join('');
    root.querySelectorAll('input[name=permLv]').forEach(el => {
      el.checked = Number(el.value) === permState.perm;
      el.onchange = permDirty;
    });
    $i('permScopes').value = (permState.scopes || []).join('\n');
    $i('permDisks').innerHTML = permState.volumes.length
      ? permState.volumes.map(v => {
          const on = (permState.scopes || []).some(s => String(s).toUpperCase().startsWith(v.toUpperCase().slice(0, 2)));
          return `<label class="sub" style="margin:0 14px 0 0"><input type="checkbox" value="${esc(v)}" ${on ? 'checked' : ''}> ${esc(v)}</label>`;
        }).join('')
      : '<span class="err">没读到任何盘（非 Windows 或权限不足）</span>';
    // 没确认过免责声明 → 首次启动也要提醒（用户要求：本台电脑第一次用就得看到）
    if (!permState.disclaimerAck) showDisclaimer(() => {});
    permDirty();
  };

  // showDisclaimer 免责提醒弹窗。onOk 在用户勾选并确认后回调；
  // 真正的闸门在原生侧（保存高权限时也要求 ack=true），这里只是别让人"不知情就点下去"。
  const showDisclaimer = (onOk) => {
    if (root.querySelector('#discBox')) return;
    const box = el(`<div id="discBox" style="position:fixed;inset:0;background:rgba(0,0,0,.62);z-index:120;display:grid;place-items:center">
      <div style="width:min(620px,92vw);background:var(--surface);border:1px solid var(--border-strong);padding:20px">
        <h3 style="margin:0 0 8px">风险与免责声明</h3>
        <div class="sub" style="line-height:1.7">
          白泽的「桌面控制」允许你把这个智能体接到本机上执行操作。放开权限前请清楚：
          <br>1. 高权限（<b>只读指定盘</b> / <b>完全访问</b>）意味着智能体可以读取、甚至删除本机文件；
          <br>2. 智能体可能出现误解指令、误删文件等不可预期行为，请务必先备份重要数据；
          <br>3. <b>你自行选择放开权限所产生的一切后果由你自己承担，与白泽作者无关</b>；
          <br>4. 建议从最低档（关闭 / 只读）开始，确认无误再逐步放开。
        </div>
        <label class="sub" style="display:flex;gap:6px;align-items:flex-start;margin:14px 0">
          <input type="checkbox" id="discAck"><span>我已阅读并理解上述风险，自愿承担相应后果。</span></label>
        <div style="display:flex;gap:10px">
          <button class="btn" id="discOk">我已了解</button>
          <button class="btn ghost" id="discLater">稍后再说</button>
        </div>
      </div></div>`);
    root.appendChild(box);
    box.querySelector('#discOk').onclick = async () => {
      if (!box.querySelector('#discAck').checked) return;
      await API.localPost('/perm', { ackDisclaimer: true }); // 只确认声明，不改权限
      box.remove();
      onOk && onOk();
      await loadPermQuiet();
    };
    box.querySelector('#discLater').onclick = () => box.remove();
  };
  const loadPermQuiet = async () => { permState.disclaimerAck = true; };

  $i('permSave').onclick = async () => {
    const lv = permChosen();
    const lvInfo = permState.levels.find(x => x.level === lv) || {};
    const body = { perm: lv, scopes: permScopesFromUI(), ackDisclaimer: !!(lvInfo.risky && $i('permAck').checked) };
    if (lvInfo.risky && !body.ackDisclaimer) {
      $i('permMsg').innerHTML = '<span class="err">这一档需要先勾选风险免责</span>';
      return;
    }
    if (lvInfo.needScope && body.scopes.length === 0) {
      $i('permMsg').innerHTML = '<span class="err">这一档需要先圈定范围（目录或盘）</span>';
      return;
    }
    $i('permMsg').textContent = '保存中…';
    const r = await API.localPost('/perm', body);
    if (!r || !r.ok) { $i('permMsg').innerHTML = `<span class="err">${esc((r && r.error) || '保存失败')}</span>`; return; }
    $i('permMsg').innerHTML = `<span class="ok">已保存：${esc(lvInfo.title || lv)}（设备会重连以更新能力）</span>`;
    await loadPerm();
    refresh();
  };

  /* ---------- 数据目录 ---------- */
  const loadDataDir = async () => {
    const r = await API.localGet('/datadir');
    if (!r || !r.ok) return;
    $i('ddPath').value = r.dataDir || '';
    $i('ddDefault').textContent = r.default || '—';
  };
  $i('ddSave').onclick = async () => {
    const dir = $i('ddPath').value.trim();
    if (!dir) { $i('ddMsg').innerHTML = '<span class="err">请填一个目录</span>'; return; }
    if (!confirm(`把数据目录改到：\n${dir}\n\n配置文件会搬过去（日志下次启动起在新位置）。继续？`)) return;
    $i('ddMsg').textContent = '迁移中…';
    const r = await API.localPost('/datadir', { dir: dir });
    if (!r || !r.ok) { $i('ddMsg').innerHTML = `<span class="err">${esc((r && r.error) || '失败')}</span>`; return; }
    $i('ddMsg').innerHTML = '<span class="ok">已迁移（重启后日志也在新位置）</span>';
    await loadDataDir();
  };
  $i('ddReset').onclick = async () => {
    $i('ddMsg').textContent = '恢复默认中…';
    const r = await API.localPost('/datadir', { dir: '' });
    if (!r || !r.ok) { $i('ddMsg').innerHTML = `<span class="err">${esc((r && r.error) || '失败')}</span>`; return; }
    $i('ddMsg').innerHTML = '<span class="ok">已恢复默认</span>';
    await loadDataDir();
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
  $i('setBall').onchange = async () => {
    const want = $i('setBall').checked;
    const r = await (await fetch('/api/local/ball/visible', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ visible: want }),
    })).json().catch(() => ({}));
    if (r && r.ok) {
      $i('setBall').checked = r.visible !== false;
      $i('setBallMsg').innerHTML = `<span class="ok">${r.visible ? '已显示' : '已隐藏'}</span>`;
    } else {
      $i('setBall').checked = !want;
      $i('setBallMsg').innerHTML = `<span class="err">${esc((r && r.error) || '改不了')}</span>`;
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
  $i('upAutoSave').onclick = async () => {
    const body = { enabled: $i('upAuto').checked, minutes: Number($i('upAutoMin').value) || 360 };
    const r = await (await fetch('/api/local/update/auto', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })).json().catch(() => ({}));
    $i('upAutoMsg').innerHTML = (r && r.ok) ? '<span class="ok">已保存</span>'
      : `<span class="err">${esc((r && r.error) || '保存失败')}</span>`;
  };

  await loadPerm();
  await loadDataDir();
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
    const d = r.data || {};
    // 魔法命令（以 / 开头）：后端直接回执，不派活也不占运行记录，把回复原样贴出来即可
    if (d.command) {
      $i('chState').innerHTML = '<span class="ok">魔法命令 /' + esc(d.name || '') +
        (d.action ? '（动作：' + esc(d.action) + '）' : '') + '</span><br>' +
        '<span class="pre" style="white-space:pre-wrap">' + esc(d.reply || '（命令已执行）') + '</span>';
      $i('chGoal').value = '';
      return;
    }
    const id = d.runId || d.id || '';
    $i('chState').innerHTML = `<span class="ok">已派发${id ? ' · ' + esc(id) : ''}</span>`;
    $i('chGoal').value = '';
    load();
  };

  await load();
  timer = setInterval(load, 3000);
  root.addEventListener('shell:closed', () => clearInterval(timer));
}
