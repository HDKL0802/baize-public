/* 白泽待办中心 - 设置模块
   AI 接口配置（由 AI 排期页唤起）/ 系统级设置跳转 / Markdown 数据中心 / 通知 / 数据管理 / 语音微件 */
'use strict';
window.Settings = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;

  const TEMPLATES = {
    deepseek: { baseUrl: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
    openai: { baseUrl: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
    anthropic: { baseUrl: 'https://api.anthropic.com/v1', model: 'claude-3-5-haiku-latest' },
    custom: { baseUrl: '', model: '' },
  };

  /* ---------- AI 接口 ---------- */
  function loadForm() {
    const s = Store.getSettings();
    const m = s.model;
    $('sProvider').value = m.provider;
    $('sBaseUrl').value = m.baseUrl;
    $('sApiKey').value = m.apiKey;
    $('sModel').value = m.model;
    renderToggles();
    renderWidgetPicker();
  }

  function saveModel() {
    const s = Store.getSettings();
    s.model = {
      provider: $('sProvider').value,
      baseUrl: $('sBaseUrl').value.trim(),
      apiKey: $('sApiKey').value.trim(),
      model: $('sModel').value.trim(),
    };
    Store.saveSettings(s);
    if (window.Plan) Plan.refreshHeader();
  }

  function onProviderChange() {
    const t = TEMPLATES[$('sProvider').value];
    if (t) { $('sBaseUrl').value = t.baseUrl; $('sModel').value = t.model; }
  }

  async function testApi() {
    const baseUrl = $('sBaseUrl').value.trim().replace(/\/+$/, '');
    const key = $('sApiKey').value.trim();
    const model = $('sModel').value.trim();
    const res = $('apiTestResult');
    res.textContent = '测试中…'; res.className = 'test-result';
    if (!baseUrl || !key) { res.textContent = '请先填写 BaseURL 与 API Key'; res.className = 'test-result err'; return; }
    try {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 12000);
      const r = await fetch(baseUrl + '/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + key },
        body: JSON.stringify({ model, messages: [{ role: 'user', content: '说“连通”两个字即可' }], max_tokens: 8 }),
        signal: controller.signal,
      });
      clearTimeout(timer);
      if (r.ok) { res.textContent = '✓ 连通正常'; res.className = 'test-result ok'; saveModel(); }
      else { res.textContent = '✗ HTTP ' + r.status; res.className = 'test-result err'; }
    } catch (e) {
      res.textContent = '✗ ' + (e.name === 'AbortError' ? '超时' : '跨域/网络错误（APK 包装后可直连）');
      res.className = 'test-result err';
    }
  }

  /* ---------- 系统级设置跳转 ---------- */
  const SYS_ACTION = {
    notify: 'android.settings.APP_NOTIFICATION_SETTINGS',
    alarm: 'android.settings.APPLICATION_DETAILS_SETTINGS',
    vibrate: 'android.settings.SOUND_SETTINGS',
  };

  function jumpSystem(kind) {
    const action = SYS_ACTION[kind] || 'android.settings.SETTINGS';
    let left = false;
    const onHide = () => { left = true; };
    document.addEventListener('visibilitychange', onHide, { once: true });
    try {
      window.location.href = `intent:#Intent;action=${action};end`;
    } catch (e) { /* 忽略 */ }
    setTimeout(() => {
      document.removeEventListener('visibilitychange', onHide);
      if (!left && document.visibilityState === 'visible') {
        UI_.toast('浏览器形态无法跳转原生设置；打包为 APK 后可用');
      }
    }, 1300);
  }

  /* ---------- 通知 ---------- */
  function renderToggles() {
    const s = Store.getSettings();
    $('tglNotify').classList.toggle('on', s.notify);
    $('tglRemind').classList.toggle('on', s.remind);
  }

  async function toggleNotify(on) {
    if (on && !UI_.notifyEnabled()) {
      if (!('Notification' in window)) { UI_.toast('浏览器不支持通知，APK 版将用系统级通知'); return; }
      const p = await Notification.requestPermission();
      if (p !== 'granted') { UI_.toast('未授权通知'); return; }
    }
    const s = Store.getSettings(); s.notify = on; Store.saveSettings(s);
    renderToggles();
    UI_.toast(on ? '通知已开启' : '通知已关闭');
    UI_.startReminderLoop();
  }

  /* ---------- 语音微件 ---------- */
  function renderWidgetPicker() {
    const cur = Store.getSettings().widget;
    document.querySelectorAll('#widgetPicker .widget-box').forEach(b => {
      b.classList.toggle('active', b.dataset.widget === cur);
    });
  }

  /* ---------- Markdown 数据中心 ---------- */
  async function doExport() {
    const md = await Vault.exportMarkdown();
    const blob = new Blob([md], { type: 'text/markdown;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = '待办中心_导出_' + new Date().toISOString().slice(0, 10) + '.md';
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 4000);
    UI_.toast('已导出 Markdown');
  }

  function doImport() {
    if (Store.isVaultLocked()) { UI_.toast('密码本已锁定，请先解锁再导入'); return; }
    $('fileImport').click(); // 实际导入由 Vault 的 change 监听处理
  }

  /* ---------- 数据管理 ---------- */
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

  /* ---------- 悬浮语音微件 ---------- */
  let floatEl = null;
  function applyWidget() {
    const s = Store.getSettings();
    removeWidget();
    if (s.widget === 'off') return;
    const el = document.createElement('div');
    el.className = 'float-widget';
    el.innerHTML = s.widget === '1x1'
      ? `<button class="fw-ball">🎤</button>`
      : `<button class="fw-strip">🎤 按住说话</button>`;
    document.body.appendChild(el);
    floatEl = el;
    const btn = el.querySelector('button');
    btn.addEventListener('pointerdown', (e) => { e.preventDefault(); Voice.start(); btn.classList.add('on'); });
    btn.addEventListener('pointerup', () => { Voice.stop(); btn.classList.remove('on'); });
    btn.addEventListener('pointercancel', () => { Voice.stop(); btn.classList.remove('on'); });
  }
  function removeWidget() { if (floatEl) { floatEl.remove(); floatEl = null; } }

  function init() {
    loadForm();

    // AI 接口弹层
    $('sProvider').addEventListener('change', onProviderChange);
    $('btnTestApi').addEventListener('click', testApi);
    $('apiCancel').addEventListener('click', () => UI_.closeModal('apiModal'));
    $('apiSave').addEventListener('click', () => { saveModel(); UI_.closeModal('apiModal'); UI_.toast('AI 接口已保存'); });
    UI_.closeOnMask('apiModal');

    // 保存配置
    $('btnSaveCfg').addEventListener('click', () => {
      saveModel();
      const s = Store.getSettings();
      Store.saveSettings(s);
      UI_.toast('配置已保存');
    });

    // 系统设置跳转
    document.querySelectorAll('.link-item[data-sys]').forEach(b => {
      b.addEventListener('click', () => jumpSystem(b.dataset.sys));
    });

    // 通知
    $('tglNotify').addEventListener('click', () => { const s = Store.getSettings(); toggleNotify(!s.notify); });
    $('tglRemind').addEventListener('click', () => {
      const s = Store.getSettings(); s.remind = !s.remind; Store.saveSettings(s);
      renderToggles(); UI_.toast('提醒已' + (s.remind ? '开启' : '关闭')); UI_.startReminderLoop();
    });

    // 语音微件
    document.querySelectorAll('#widgetPicker .widget-box').forEach(b => {
      b.addEventListener('click', () => {
        const s = Store.getSettings(); s.widget = b.dataset.widget; Store.saveSettings(s);
        renderWidgetPicker(); applyWidget();
        UI_.toast(b.dataset.widget === 'off' ? '已关闭悬浮微件' : '已切换为 ' + b.dataset.widget.replace('x', '×'));
      });
    });

    // Markdown / 数据管理
    $('btnExport').addEventListener('click', doExport);
    $('btnImportMd').addEventListener('click', doImport);
    $('btnLoadSample').addEventListener('click', loadSample);
    $('btnClearAll').addEventListener('click', clearAll);

    applyWidget();
  }

  return { init, loadForm, applyWidget, renderWidgetPicker };
})();
