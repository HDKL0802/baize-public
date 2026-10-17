/* 白泽桌面端 · 浏览器（电脑控制 · 浏览器双方案）
   内置浏览器 = 独立进程的 WebView2 窗口（browser_windows.go），这里只是它的遥控面板：
   - 打开/导航、看当前地址与标题；
   - 「移交给我」/「交还给白泽」：AI 暂停，用户自己登录/操作，完成后交还；
   - Chrome 扩展的接入状态与目录入口（真正的开关在「设置 → 桌面控制 → 允许控制浏览器」）。

   口径：本机回环接口 /api/local/browser，不经 NAS；AI 的控制动作仍然走后端审批。 */

async function renderBrowser(root) {
  root.innerHTML = `
    <div class="page">
      <h3>浏览器</h3>
      <div class="sub">白泽自带的内置浏览器。AI 控制时每个 <span class="mono">browser.*</span> 动作仍需你在
        「任务与审批」里批准；遇到要登录的页面，点「移交给我」自己操作，完成后点「交还给白泽」。</div>
      <div class="bar" style="margin-top:8px">
        <input id="brUrl" placeholder="输入网址，例如 https://www.baidu.com" style="flex:1;min-width:240px">
        <button class="btn primary" id="brGo">打开</button>
        <button class="btn ghost" id="brRefresh">刷新状态</button>
      </div>
      <div class="sub" id="brPaneMsg" style="margin:8px 0"></div>
      <div id="brCard"></div>

      <div class="sect" style="margin-top:16px">
        <h3>Chrome 扩展（控制你已装的浏览器）</h3>
        <div class="sub" id="brExtInfo">读取中…</div>
        <div style="display:flex;gap:8px;margin-top:8px;flex-wrap:wrap">
          <button class="btn ghost sm" id="brCfg">去设置里配置</button>
          <button class="btn ghost sm" id="brOpenExt">打开扩展目录</button>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let timer = null;

  const paint = (state) => {
    const st = state || {};
    const b = st.builtin || {};
    const msg = $i('brPaneMsg');
    const card = $i('brCard');
    if (b.url) $i('brUrl').value = b.url;

    if (!st.control) {
      msg.innerHTML = '<span class="warn">「允许控制浏览器」还没开：AI 不能控制任何浏览器；你仍可以自己用这个内置浏览器。</span>';
    } else if (!b.open) {
      msg.innerHTML = '<span class="sub">内置浏览器还没打开。</span>';
    } else if (b.handoff) {
      msg.innerHTML = '<span class="warn">控制权已经移交给你：AI 的动作会被拒绝，你操作完点「交还给白泽」。</span>';
    } else {
      msg.innerHTML = '<span class="ok">内置浏览器开着；AI 可以控制（每个动作仍需审批）。</span>';
    }

    if (!b.open) {
      card.innerHTML = '<div class="empty">点上面的「打开」，白泽会弹出一个独立浏览器窗口。</div>';
      return;
    }
    const handoff = !!b.handoff;
    card.innerHTML = `
      <div class="row">
        <div class="who"><b>${esc(b.title || '(无标题)')}</b>
          <span class="mono">${esc(b.url || '')}</span></div>
        <div class="tags">
          <span class="tag ${handoff ? 'warn' : 'on'}">${handoff ? '已移交给你' : 'AI 可控制'}</span>
          ${handoff
            ? '<button class="btn ghost sm" id="brBack">交还给白泽</button>'
            : '<button class="btn ghost sm" id="brHandoff">移交给我</button>'}
          <button class="btn ghost sm" id="brClose">关闭浏览器</button>
        </div>
      </div>`;
    const back = $i('brBack'), hand = $i('brHandoff');
    if (back) back.onclick = () => act('handoff', { handoff: false }, '已交还给白泽');
    if (hand) hand.onclick = () => act('handoff', { handoff: true }, '已移交给你，操作完点「交还给白泽」');
    const close = $i('brClose');
    if (close) close.onclick = async () => {
      if (!confirm('关闭内置浏览器窗口？AI 之后的 browser 动作会失败，需要重新打开。')) return;
      await API.localPost('/browser', { action: 'close' });
      await refresh();
    };
  };

  const paintExt = (st) => {
    const ext = $i('brExtInfo');
    if (!st || !st.allowChrome) {
      ext.innerHTML = '未允许扩展接入。要控制你已装的 Chrome，去「设置 → 桌面控制 → 允许控制浏览器」勾上「允许 Chrome 扩展接入」。';
      return;
    }
    ext.innerHTML = (st.extConnected
      ? '<span class="ok">扩展已连接</span>'
      : '<span class="warn">扩展还没连上</span>')
      + ' · 地址 <span class="mono">ws://127.0.0.1:' + esc(st.extPort || 47121) + '/ext/ws</span>'
      + '<br>在 Chrome/Edge 里加载扩展目录 <span class="mono">' + esc(st.extDir || 'browser-ext') + '</span>，'
      + '点扩展图标填上面地址与设置页里的令牌。';
  };

  const refresh = async () => {
    if (!root.isConnected) { if (timer) clearInterval(timer); return; }
    const r = await API.localGet('/browser');
    if (!r || r.ok === false) return;
    const st = r.state || {};
    paint(st);
    paintExt(st);
  };

  const act = async (action, extra, okMsg) => {
    const body = Object.assign({ action: action }, extra || {});
    const r = await API.localPost('/browser', body);
    if (!r || r.ok === false) {
      $i('brPaneMsg').innerHTML = `<span class="err">${esc((r && r.error) || '操作失败')}</span>`;
      return;
    }
    if (okMsg) $i('brPaneMsg').textContent = okMsg;
    if (r.state) { paint(r.state); paintExt(r.state); }
    else await refresh();
  };

  $i('brGo').onclick = async () => {
    const url = ($i('brUrl').value || '').trim();
    if (!url) { $i('brPaneMsg').textContent = '先输入网址'; return; }
    $i('brPaneMsg').textContent = '正在打开…';
    await act('open', { url: url });
  };
  $i('brRefresh').onclick = () => refresh();
  $i('brCfg').onclick = () => { location.hash = '#settings'; };
  $i('brOpenExt').onclick = () => act('opendir', {}, '已在资源管理器里打开扩展目录');

  await refresh();
  timer = setInterval(refresh, 2500);
}

/* 回填注册表（同 apps8.js 末尾：不能写 render: renderXxx 前向引用） */
(function wireBrowserApp() {
  const app = window.APP_BY_ID && window.APP_BY_ID['browser'];
  if (app) app.render = renderBrowser;
})();
