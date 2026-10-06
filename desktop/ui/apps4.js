/* 白泽桌面端 · 插件市场
   - 插件 = 静态文件：源的 index.json + 若干 .zip，所以市场本身不需要任何后端服务
   - 装插件 = 技能落进技能目录 + MCP 服务登记进配置；同名一律跳过（不覆盖用户自己的东西）
   - 卸载是归档、停用是移位，都能从数据目录里捞回来
   render 在文件末尾回填 window.APPS（同 apps2/apps3 的说明：不能写前向引用）。 */
'use strict';

async function renderPlugins(root) {
  root.innerHTML = `
    <div class="page">
      <h3>插件市场</h3>
      <div class="sub">
        插件就是一堆静态文件：源的 <span class="mono">index.json</span> + 若干 <span class="mono">.zip</span>。
        安装 = 把包里的<b>技能</b>落进技能目录、把 <b>MCP 服务</b>登记进配置；
        <b>与现有同名的技能/MCP 一律跳过</b>，绝不覆盖你自己的东西。
        插件目录：<span class="mono" id="plDir">…</span>
      </div>
      <div class="tabs" id="plTabs">
        <button data-tab="market" class="on">市场</button>
        <button data-tab="installed">已安装</button>
        <button data-tab="sources">插件源</button>
      </div>
      <div id="plBody"><div class="empty">读取中…</div></div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const state = { dir: '', sources: [], available: [], installed: [], tab: 'market' };
  let busy = false;

  const draw = () => {
    $i('plDir').textContent = state.dir || '—';
    root.querySelectorAll('#plTabs button').forEach(b => b.classList.toggle('on', b.dataset.tab === state.tab));
    const body = $i('plBody');
    if (state.tab === 'installed') body.innerHTML = viewInstalled();
    else if (state.tab === 'sources') body.innerHTML = viewSources();
    else body.innerHTML = viewMarket();
    wire();
  };

  /* ---------- 三个页签的视图 ---------- */

  const tagOf = a => a.hasUpdate ? '<span class="tag warn">有更新</span>'
    : (a.installed ? '<span class="tag on">已装</span>' : '');

  // 货架分组：板块（官方/社区）· 阶段（正式版/测试版）· 分类。
  // 归一（没标注的算社区、没阶段算正式版）是后端做的，这里只负责排好序再画。
  // 分类的展示次序按这张偏好表（后端是按字节序排的，直接画出来会很莫名，所以这里收一下）；
  // 表里没有的分类排在最后、彼此按字节序 —— 社区新开的分类不会被漏掉。
  const CAT_ORDER = ['调研写作', '开发', '知识管理', '跨端设备', '自动化', '生活', '其他'];
  const groupAvailable = list => {
    const rank = (ch, st) => (ch === 'official' ? 0 : 10) + (st === 'beta' ? 1 : 0);
    const catRank = c => { const i = CAT_ORDER.indexOf(c); return i < 0 ? CAT_ORDER.length : i; };
    const byCat = (x, y) => catRank(x) - catRank(y) || (x < y ? -1 : (x > y ? 1 : 0));
    const map = new Map();
    list.forEach(a => {
      const ch = a.channel === 'official' ? 'official' : 'community';
      const st = a.stage === 'beta' ? 'beta' : 'stable';
      const cat = a.category || '未分类';
      const key = ch + '|' + st + '|' + cat;
      if (!map.has(key)) {
        map.set(key, {
          ch: ch, st: st, cat: cat, items: [],
          title: (ch === 'official' ? '官方' : '社区') + ' · ' + (st === 'beta' ? '测试版' : '正式版') + ' · ' + cat,
        });
      }
      map.get(key).items.push(a);
    });
    return Array.from(map.values()).sort((x, y) =>
      rank(x.ch, x.st) - rank(y.ch, y.st) || byCat(x.cat, y.cat));
  };

  const shelfRow = a => `
    <div class="row">
      <div class="who">
        <b>${esc(a.name || a.id)} <span class="tag">${esc(a.version || '')}</span></b>
        <span>${esc(a.description || '（没有说明）')}</span>
        <span class="mono">${esc(a.id)} · 来自「${esc(a.source || '')}」${a.author ? ' · ' + esc(a.author) : ''}${a.license ? ' · ' + esc(a.license) : ''}</span>
      </div>
      <div class="tags">
        ${(a.tags || []).map(x => `<span class="tag">${esc(x)}</span>`).join('')}
        ${a.builtin ? '<span class="tag on">内置</span>' : ''}
        ${a.stage === 'beta' ? '<span class="tag warn">测试版</span>' : ''}
        ${tagOf(a)}
        ${a.installed
          ? `<button class="btn ghost sm" data-uninstall="${esc(a.id)}">卸载</button>`
          : `<button class="btn sm" data-install="${esc(a.id)}">安装</button>`}
      </div>
    </div>`;

  const viewMarket = () => {
    const groups = groupAvailable(state.available);
    const official = state.available.filter(a => a.channel === 'official').length;
    const community = state.available.length - official;

    const shelf = groups.length ? groups.map(g =>
      `<div class="shelf-head">${esc(g.title)} <span class="tag">${g.items.length}</span></div>` +
      g.items.map(shelfRow).join('')
    ).join('') : '<div class="empty">还没有插件源（或源里没有插件）—— 去「插件源」页加一个，或直接给个 zip 装上。</div>';

    return `
      <div class="sect" style="margin-top:0">
        <h3>直接安装</h3>
        <div class="sub">源里没有的、或者你自己打的包，可以直接给一个 <span class="mono">.zip</span> 地址或本机路径。</div>
        <div class="fields" style="grid-template-columns:1fr 120px">
          <div><label>插件包地址 / 本机路径</label><input id="plDl" placeholder="https://…/demo.zip 或 D:\\plugins\\demo.zip"></div>
          <div style="display:flex;align-items:flex-end"><button class="btn" id="plDlGo">安装</button></div>
        </div>
      </div>

      <div class="sect">
        <h3>货架（${state.available.length}）</h3>
        <div class="sub" style="margin-bottom:8px">分板块查看：<b>官方 ${official}</b> · <b>社区 ${community}</b>
          （官方里再分正式版 / 测试版，板块内按分类排）。</div>
        <div id="plAva">${shelf}</div>
        ${community === 0 ? '<div class="empty" style="padding-top:8px">社区板块还没有插件 —— 想投稿的话，往 <span class="mono">baize-plugins</span> 仓提一个 PR 就行（细节看那个仓的 README）。</div>' : ''}
      </div>`;
  };

  const viewInstalled = () => {
    if (!state.installed.length) {
      return '<div class="empty">还没装任何插件。去「市场」页看看，或直接给一个本地 zip 装上。</div>';
    }
    return `<div id="plIns">` + state.installed.map(it => `
      <div class="row">
        <div class="who">
          <b>${esc(it.name || it.id)} <span class="tag">${esc(it.version || '')}</span>
             <span class="tag ${it.enabled ? 'on' : 'off'}">${it.enabled ? '已启用' : '已停用'}</span></b>
          <span class="mono">${esc(it.id)}${it.source ? ' · 来自 ' + esc(it.source) : ''} · ${fmtTime(it.installedAt)}</span>
          <span>技能：${(it.skills || []).join('、') || '—'} ｜ MCP：${(it.mcp || []).join('、') || '—'}</span>
          ${it.note ? `<span class="mono">说明：${esc(it.note)}</span>` : ''}
        </div>
        <div class="tags">
          <button class="btn ghost sm" data-toggle="${esc(it.id)}" data-on="${it.enabled ? '0' : '1'}">${it.enabled ? '停用' : '启用'}</button>
          <button class="btn ghost sm" data-uninstall="${esc(it.id)}">卸载</button>
        </div>
      </div>`).join('') + '</div>';
  };

  const viewSources = () => {
    const rows = state.sources.length ? state.sources.map(s => `
      <div class="row">
        <div class="who">
          <b>${esc(s.name || s.url)} <span class="tag ${s.enabled ? 'on' : 'off'}">${s.enabled ? '启用' : '停用'}</span>${s.builtin ? ' <span class="tag on">内置</span>' : ''}</b>
          <span class="mono">${s.builtin ? '随后端一起分发，离线可用（不用管，也删不掉）' : esc(s.url)}</span>
          <span>${s.error ? `<span class="err">${esc(s.error)}</span>`
            : `${s.count} 个插件${s.indexName ? ' · ' + esc(s.indexName) : ''}${s.updated ? ' · ' + esc(s.updated) : ''}`}</span>
        </div>
        <div class="tags">
          ${s.builtin ? '' : `
          <button class="btn ghost sm" data-addsrc="${esc(s.url)}" data-name="${esc(s.name)}" data-on="${s.enabled ? '0' : '1'}">${s.enabled ? '停用' : '启用'}</button>
          <button class="btn ghost sm" data-rmsrc="${esc(s.url)}">删除</button>`}
        </div>
      </div>`).join('') : '<div class="empty">还没有插件源。</div>';

    return `
      <div class="sect" style="margin-top:0">
        <h3>添加插件源</h3>
        <div class="sub">填 <span class="mono">index.json</span> 的地址：可以是 http(s)（任何静态托管都行），也可以是本机路径。
          同一个地址重复添加只会更新名字/开关。<br>
          内置的<b>「官方插件源」</b>随后端一起分发（离线也能用），它<b>一定在下面的列表里</b>，不用也不要手动添加。</div>
        <div class="fields" style="grid-template-columns:1fr 1fr 110px">
          <div><label>名字（可留空）</label><input id="plSrcName" placeholder="例如 团队共享源"></div>
          <div><label>index.json 地址</label><input id="plSrcUrl" placeholder="https://…/index.json"></div>
          <div style="display:flex;align-items:flex-end"><button class="btn" id="plSrcAdd">添加</button></div>
        </div>
      </div>
      <div class="sect">
        <h3>插件源（${state.sources.length}）</h3>
        <div id="plSrcList">${rows}</div>
      </div>`;
  };

  /* ---------- 事件绑定 ---------- */

  const wire = () => {
    root.querySelectorAll('[data-install]').forEach(b => b.onclick = async () => {
      await act({ action: 'install', id: b.dataset.install }, '安装完成', b);
    });
    root.querySelectorAll('[data-uninstall]').forEach(b => b.onclick = async () => {
      if (!confirm('卸载这个插件？它带的技能会被归档到插件目录（能捞回来），登记的 MCP 服务会被移除。')) return;
      await act({ action: 'uninstall', id: b.dataset.uninstall }, '已卸载', b);
    });
    root.querySelectorAll('[data-toggle]').forEach(b => b.onclick = async () => {
      const on = b.dataset.on === '1';
      await act({ action: on ? 'enable' : 'disable', id: b.dataset.toggle }, on ? '已启用' : '已停用', b);
    });
    root.querySelectorAll('[data-rmsrc]').forEach(b => b.onclick = async () => {
      if (!confirm('删掉这个插件源？（已安装的插件不受影响）')) return;
      await act({ action: 'removeSource', url: b.dataset.rmsrc }, '已删除源', b);
    });
    root.querySelectorAll('[data-addsrc]').forEach(b => b.onclick = async () => {
      await act({ action: 'addSource', name: b.dataset.name, url: b.dataset.addsrc, enabled: b.dataset.on !== '1' }, '已更新源', b);
    });

    const dl = $i('plDlGo');
    if (dl) dl.onclick = async () => {
      const url = $i('plDl').value.trim();
      if (!url) { Shell.toast('先填插件包地址或路径', 'err'); return; }
      await act({ action: 'install', url: url }, '安装完成', dl);
    };
    const add = $i('plSrcAdd');
    if (add) add.onclick = async () => {
      const url = $i('plSrcUrl').value.trim();
      if (!url) { Shell.toast('先填 index.json 地址', 'err'); return; }
      await act({ action: 'addSource', name: $i('plSrcName').value.trim(), url: url }, '已添加源', add);
    };
  };

  // act 所有动作的公共走法：装/卸可能慢（要下载），按钮禁用 + 完事整体刷新
  const act = async (body, okMsg, btn) => {
    if (busy) return;
    busy = true;
    const old = btn ? btn.textContent : '';
    if (btn) { btn.disabled = true; btn.textContent = '处理中…'; }
    const r = await API.post('/api/agent/plugins', body);
    busy = false;
    if (btn) { btn.disabled = false; btn.textContent = old; }
    if (!r.ok) { Shell.toast('失败：' + (r.error || '未知错误'), 'err'); return; }
    if (r.data && r.data.installedError) Shell.toast('读已装清单失败：' + r.data.installedError, 'err');
    if (okMsg) Shell.toast(okMsg, 'ok');
    await load();
  };

  const load = async () => {
    const r = await API.get('/api/agent/plugins');
    if (!r.ok) {
      $i('plBody').innerHTML = `<div class="empty err">读不到插件市场：${esc(r.error || '')}</div>`;
      return;
    }
    const d = r.data || {};
    state.dir = d.dir || '';
    state.sources = d.sources || [];
    state.available = d.available || [];
    state.installed = d.installed || [];
    draw();
  };

  root.querySelectorAll('#plTabs button').forEach(b => b.onclick = () => {
    state.tab = b.dataset.tab;
    draw();
  });

  await load();
}

/* ================= 回填注册表（同 apps2/apps3 末尾的说明） ================= */
(function wirePluginMarket() {
  const app = window.APP_BY_ID && window.APP_BY_ID.plugins;
  if (app) app.render = renderPlugins;
})();
