/* 白泽待办中心 - 设置模块
   AI 智能体（OpenAI 兼容 / Anthropic 两协议，最多 5 个模型可切换，改动即时生效）
   / 通知与提醒 / Markdown 数据中心（原生文件桥导入导出）/ 数据管理 */
'use strict';
window.Settings = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;
  const MAX_MODELS = 5;

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g,
      c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }
  function protoLabel(p) { return p === 'anthropic' ? 'Anthropic' : 'OpenAI 兼容'; }

  /* ================= AI 智能体 ================= */
  /* 选中的模型 = 正在编辑的模型 = 当前生效的模型，三者始终一致，
     避免「改了 A 却在用 B」这种迷惑状态。 */

  function modelOf(id) { return AI.list().find(m => m.id === id) || null; }

  function renderStatus() {
    const m = AI.active();
    const st = $('aiStatus');
    if (AI.ready()) {
      st.className = 'ai-status ok';
      st.textContent = '✓ AI 智能体已启用：' + m.name + ' · ' + protoLabel(m.protocol) + ' · ' + m.model;
    } else {
      st.className = 'ai-status err';
      st.textContent = '✗ 还没配置好：请选中下面某个模型，把 BaseURL / API Key / 模型 ID 填完。'
        + '未配置好之前，「AI 智能排期」不可用（语音录入仍可用本地规则）。';
    }
  }

  function renderRows() {
    const ms = AI.list();
    const activeId = AI.active().id;
    const box = $('aiModelList');
    box.innerHTML = '';
    ms.forEach(m => {
      const ready = !!(m.baseUrl && m.apiKey && m.model);
      const el = document.createElement('div');
      el.className = 'ai-model' + (m.id === activeId ? ' on' : '');
      el.innerHTML = `
        <span class="am-radio">✓</span>
        <span class="am-main">
          <span class="am-name">${esc(m.name || '未命名')}${m.id === activeId ? ' · 使用中' : ''}</span>
          <span class="am-sub">${esc(protoLabel(m.protocol) + ' · ' + (m.model || '未填模型') + (ready ? '' : ' · 缺 API Key'))}</span>
        </span>
        ${ms.length > 1 ? '<span class="am-del" data-del="1">删除</span>' : ''}`;
      el.addEventListener('click', (e) => {
        if (e.target.closest('[data-del]')) return;
        AI.setActive(m.id);
        refresh();
        UI_.toast('已切换为「' + m.name + '」');
      });
      const del = el.querySelector('[data-del]');
      if (del) del.addEventListener('click', (e) => { e.stopPropagation(); removeModel(m); });
      box.appendChild(el);
    });

    const full = ms.length >= MAX_MODELS;
    $('btnAddModel').disabled = full;
    $('btnAddModel').textContent = full ? '已达上限（5 个模型）' : '＋ 添加模型（最多 5 个）';
  }

  function renderEditor() {
    const m = AI.active();
    $('mName').value = m.name || '';
    $('sBaseUrl').value = m.baseUrl || '';
    $('sApiKey').value = m.apiKey || '';
    $('sModel').value = m.model || '';
    markProtocol();
    $('apiTestResult').textContent = '';
    $('apiTestResult').className = 'test-result';
  }

  function markProtocol() {
    const p = AI.active().protocol;
    document.querySelectorAll('#aiProtocol .chip-opt').forEach(b => {
      b.classList.toggle('on', b.dataset.v === (p || 'openai'));
    });
  }

  /** 即时写入 + 即时刷新（列表行 / 状态 / 排期页头），不留到退出界面才生效 */
  function patch(p) {
    AI.update(AI.active().id, p);
    renderRows();
    renderStatus();
    if (window.Plan) Plan.refreshHeader();
    window.dispatchEvent(new Event('bz:aichanged'));
  }

  /** 你在哪个入口里操作 + 现在的数据正本在哪 —— 排查「记进去却找不到」用 */
  function renderEnv() {
    const el = $('envInfo');
    if (!el) return;
    const info = Store.kbInfo() || {};
    let head;
    if (Store.kbMode()) {
      head = Store.kbOnline()
        ? '正本在 NAS 上的白泽知识库（' + (info.server || '') + '）：待办 ' + Store.getTodos().length + ' 条'
          + (Store.outboxCount() ? ' · 有 ' + Store.outboxCount() + ' 处改动还在排队等补推' : '')
        : '后端知识库现在连不上（' + (info.reason || info.error || '原因未知') + '）：显示的是本机缓存的待办 '
          + Store.getTodos().length + ' 条，这期间的改动会先排队，联网后自动补推';
    } else {
      const where = window.BzNative
        ? '手机 App（数据存在这台手机上）'
        : '浏览器 ' + location.origin + '（数据存在这个浏览器里）';
      head = '当前入口：' + where + ' · 本机待办 ' + Store.getTodos().length + ' 条。'
        + '换一个入口（电脑浏览器 / 手机浏览器 / 另一个端口）看到的是另外一份独立数据，互相看不到。';
    }
    el.textContent = head + (Store.lastSyncError() ? ' 上次同步失败：' + Store.lastSyncError() : '');
  }

  function refresh() {
    renderStatus(); renderRows(); renderEditor(); renderEnv(); renderDevice();
    if (window.Plan) Plan.refreshHeader();
    window.dispatchEvent(new Event('bz:aichanged'));
  }

  function addModel() {
    if (AI.list().length >= MAX_MODELS) { UI_.toast('最多只能存 5 个模型'); return; }
    UI_.actionSheet([
      { label: 'DeepSeek（OpenAI 兼容，推荐）', onTap: () => { AI.add('deepseek'); refresh(); UI_.toast('已添加 DeepSeek 预设，填上 API Key 即可用'); } },
      { label: 'OpenAI 兼容（自填 BaseURL）', onTap: () => { AI.add('openai'); refresh(); UI_.toast('已添加，请填 BaseURL / Key / 模型'); } },
      { label: 'Anthropic（Claude）', onTap: () => { AI.add('anthropic'); refresh(); UI_.toast('已添加 Anthropic 配置'); } },
      { label: '取消', cancel: true },
    ]);
  }

  function removeModel(m) {
    if (AI.list().length <= 1) { UI_.toast('至少保留一个模型'); return; }
    UI_.actionSheet([
      {
        label: `删除「${m.name}」`, danger: true, onTap: () => {
          AI.remove(m.id);
          refresh();
          if (window.Plan) Plan.refreshHeader();
          UI_.toast('已删除');
        },
      },
      { label: '取消', cancel: true },
    ]);
  }

  async function testApi() {
    const res = $('apiTestResult');
    if (!AI.ready()) {
      res.textContent = '请先填 BaseURL / API Key / 模型 ID';
      res.className = 'test-result err';
      return;
    }
    res.textContent = '测试中…';
    res.className = 'test-result';
    try {
      const out = await AI.test();
      // 「通了但没正文」是提醒不是通过，别用绿勾糊过去
      const warn = /^接口连通，但/.test(out);
      res.textContent = (warn ? '⚠ ' : '✓ ') + out;
      res.className = 'test-result ' + (warn ? '' : 'ok');
    } catch (e) {
      res.textContent = '✗ ' + e.message;
      res.className = 'test-result err';
    }
  }

  /* ================= 跨端（把这台手机注册成后端设备） ================= */

  function renderDevice() {
    const state = $('deviceState');
    const mirror = $('deviceMirror');
    if (!state || !mirror) return;
    const server = $('dwServer');
    const tok = $('dwToken');
    const toggle = $('tglDevice');
    const btns = [$('btnDeviceSave'), $('btnDeviceRefresh')];

    const off = (msg, err) => {
      toggle.classList.remove('on');
      [server, tok].concat(btns).forEach(x => { x.disabled = true; });
      state.textContent = msg;
      state.className = 'test-result' + (err ? ' err' : '');
      mirror.textContent = '';
    };

    // 浏览器里没有本机内核，这一整块就用不了——明确说清楚，别给个假开关
    if (!window.BzDevice || !BzDevice.available()) {
      off('当前入口不是手机 App：跨端要在 APK 里用（浏览器里没有本机内核）。', true);
      return;
    }

    const st = BzDevice.processStatus();
    [server, tok].concat(btns).forEach(x => { x.disabled = false; });
    server.value = st.server || '';
    tok.value = '';
    tok.placeholder = st.pairTokenSet ? '已保存（留空 = 不改）' : '后端数据目录 token 文件里的那串';
    toggle.classList.toggle('on', !!st.enabled);

    if (!st.enabled) {
      state.textContent = '未开启：现在是一个纯本地待办 App，没有连任何后端。';
      state.className = 'test-result';
      mirror.textContent = '';
      return;
    }
    if (!st.running) {
      state.textContent = '✗ 内核没跑起来：'
        + (st.error || (st.supported ? '原因不明' : '这个安装包里没有手机内核（libbzcore.so）'));
      state.className = 'test-result err';
      mirror.textContent = '';
      return;
    }

    // 内核在跑，再问它自己「连上后端没有」（谁连的谁知道，不猜日志）
    const link = BzDevice.linkStatus();
    if (link.error) {
      state.textContent = '✗ ' + link.error;
      state.className = 'test-result err';
    } else if (link.connected) {
      state.textContent = '✓ 已连上后端：设备 id ' + (link.deviceId || '?')
        + ' · ' + (link.server || st.server)
        + ' · 可被派活 ' + ((link.actions || []).join(' / ') || '-');
      state.className = 'test-result ok';
    } else {
      state.textContent = '✗ 内核在跑，但没连上后端：'
        + (link.lastError || '还在重试（1~30 秒退避）');
      state.className = 'test-result err';
    }

    // 连上就把本机待办推一份过去，免得后端读到空数据
    if (link.connected) BzDevice.pushTodos();
    const s = BzDevice.lastSyncInfo();
    if (!s) {
      mirror.textContent = '';
    } else {
      mirror.textContent = '内核里的待办镜像：' + s.count + ' 条 · '
        + new Date(s.at).toLocaleTimeString() + (s.error ? ' · 出错：' + s.error : '');
      mirror.className = 'test-result' + (s.error ? ' err' : '');
    }
  }

  function saveDevice() {
    if (!window.BzDevice || !BzDevice.available()) return;
    const addr = $('dwServer').value.trim();
    const on = $('tglDevice').classList.contains('on');
    if (on && !addr) { UI_.toast('要连后端得先填后端地址'); renderDevice(); return; }
    if (on && !/^https?:\/\//.test(addr)) { UI_.toast('地址要带 http://，例如 http://192.168.1.9:8787'); renderDevice(); return; }
    BzDevice.configure(addr, $('dwToken').value.trim(), on);
    $('dwToken').value = '';
    renderDevice();
    UI_.toast(on ? '已开启跨端，正在拉起内核…' : '已关闭跨端');
    // 内核要起来 + 连后端，一两秒后状态才准
    setTimeout(renderDevice, 1200);
    setTimeout(renderDevice, 3200);
  }

  /* ================= 通知与提醒 ================= */
  function renderRemind() {
    $('tglRemind').classList.toggle('on', Store.getSettings().remind);
  }

  function renderNotifyState() {
    const el = $('notifyStateText');
    const nb = UI_.nativeShell();
    if (nb) {
      const on = UI_.notifyEnabled();
      const exact = (typeof nb.exactAlarmAllowed === 'function') ? !!nb.exactAlarmAllowed() : true;
      el.textContent = on ? (exact ? '已开启（精确闹钟）' : '已开启（闹钟可能延后）') : '未开启';
      el.className = 'test-result ' + (on ? 'ok' : 'err');
      return;
    }
    if (!('Notification' in window)) { el.textContent = '当前环境不支持'; el.className = 'test-result err'; return; }
    const p = Notification.permission;
    el.textContent = p === 'granted' ? '已授权' : (p === 'denied' ? '已被系统拒绝' : '未授权');
    el.className = 'test-result ' + (p === 'granted' ? 'ok' : 'err');
  }

  function requestNotify() {
    const nb = UI_.nativeShell();
    if (nb) {
      if (typeof nb.requestNotificationPermission === 'function') nb.requestNotificationPermission();
      UI_.toast('请在系统弹窗里允许通知');
      setTimeout(renderNotifyState, 1500);
      return;
    }
    if ('Notification' in window) Notification.requestPermission().then(renderNotifyState);
    else UI_.toast('当前环境不支持通知');
  }

  function testNotify() {
    const nb = UI_.nativeShell();
    if (nb && typeof nb.notifyNow === 'function') {
      nb.notifyNow('⏰ 待办中心', '这是一条测试通知，说明提醒通道正常。');
      UI_.toast('已发送，请看通知栏');
      return;
    }
    if ('Notification' in window && Notification.permission === 'granted') {
      new Notification('⏰ 待办中心', { body: '这是一条测试通知' });
      UI_.toast('已发送');
      return;
    }
    UI_.toast('通知未授权，先点「开启通知权限」');
  }

  /* ================= Markdown 数据中心 ================= */
  async function doExport() {
    const md = await Vault.exportMarkdown();
    UI_.saveText('待办中心_导出_' + new Date().toISOString().slice(0, 10) + '.md', md);
  }

  function doImport() {
    if (Store.isVaultLocked()) { UI_.toast('密码本已锁定，请先解锁再导入'); return; }
    UI_.pickText(async (text, name) => {
      if (!text || !text.trim()) { UI_.toast('文件是空的，无法导入'); return; }
      try {
        const r = await Vault.importMarkdown(text);
        Todo.reload();
        Vault.reload();
        UI_.toast(`导入 ${name || ''}：待办 +${r.todos.added}/合并 ${r.todos.merged}，密码 +${r.vault.added}/合并 ${r.vault.merged}`);
      } catch (e) {
        UI_.toast('导入失败：' + (e.message || e));
      }
    });
  }

  /* ================= 数据管理 ================= */
  function loadSample() {
    UI_.actionSheet([
      { label: '恢复初始示例（覆盖当前全部数据）', danger: true, onTap: async () => {
        Store.saveTodos(Store.sampleTodos());
        const cur = await Store.getVault();
        if (!cur.length) {
          await Store.saveVault([{ id: Store.uid(), title: 'GitHub', account: 'user@example.com', password: '示例密码', url: 'https://github.com', note: '初始示例', source: 'manual', createdAt: Date.now() }]);
        }
        Todo.reload(); Vault.reload(); UI_.toast('已恢复示例');
      } },
      { label: '取消', cancel: true },
    ]);
  }

  function clearAll() {
    UI_.actionSheet([
      { label: '清空全部待办与密码（不可恢复）', danger: true, onTap: async () => {
        Store.saveTodos([]);
        await Store.saveVault([]);
        localStorage.removeItem('bz_reminded');
        Todo.reload(); Vault.reload(); UI_.toast('已清空');
      } },
      { label: '取消', cancel: true },
    ]);
  }

  /* ================= 初始化 ================= */
  function init() {
    renderRemind();

    // AI 智能体：协议只读切换，输入框改动即时保存并即时回显
    document.querySelectorAll('#aiProtocol .chip-opt').forEach(b => {
      b.addEventListener('click', () => { patch({ protocol: b.dataset.v }); markProtocol(); });
    });
    const bindField = (id, key) => {
      $(id).addEventListener('input', () => patch({ [key]: $(id).value.trim() }));
    };
    bindField('mName', 'name');
    bindField('sBaseUrl', 'baseUrl');
    bindField('sApiKey', 'apiKey');
    bindField('sModel', 'model');
    $('btnAddModel').addEventListener('click', addModel);
    $('btnTestApi').addEventListener('click', testApi);

    // 语音输入交给用户自己的输入法，应用里不再有语音设置

    // 跨端：地址/令牌要到保存时才生效（边打字边重启内核就乱了），开关点了就算数
    $('tglDevice').addEventListener('click', () => {
      $('tglDevice').classList.toggle('on');
      saveDevice();
    });
    $('btnDeviceSave').addEventListener('click', saveDevice);
    $('btnDeviceRefresh').addEventListener('click', () => { renderDevice(); UI_.toast('已刷新'); });

    // 通知
    $('tglRemind').addEventListener('click', () => {
      const s = Store.getSettings(); s.remind = !s.remind; Store.saveSettings(s);
      renderRemind(); UI_.toast('提醒已' + (s.remind ? '开启' : '关闭'));
      UI_.startReminderLoop();
      UI_.syncNativeReminders();
    });
    $('btnNotifyPerm').addEventListener('click', requestNotify);
    $('btnTestNotify').addEventListener('click', testNotify);

    // Markdown / 数据管理
    $('btnExport').addEventListener('click', doExport);
    $('btnImportMd').addEventListener('click', doImport);
    $('btnLoadSample').addEventListener('click', loadSample);
    $('btnClearAll').addEventListener('click', clearAll);

    refresh();
    setTimeout(renderNotifyState, 600);

    // 切到设置页时刷新「当前入口 / 数据条数」，免得数字是旧的。
    // 设置页不占底栏（从「其他功能」进去），所以监听切页广播而不是底栏点击。
    window.addEventListener('bz:page', (e) => {
      if (e && e.detail && e.detail.id === 'view-settings') setTimeout(refresh, 60);
    });
  }

  return { init, refresh, renderNotifyState, doExport, doImport };
})();
