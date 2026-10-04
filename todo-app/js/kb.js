/* 白泽 · 知识库扩展页
   ─────────────────────────────────────────────
   资源：附件（正本在 NAS 的文件仓库）· 记忆（长期记忆 + 语义检索）
   配置：API 服务（各模型站的 Key + baseURL）· 模型审批（危险动作人工放行）

   这些数据全在后端（NAS），手机端只负责展示与操作。没有本机内核、或连不上后端时，
   一律把原因如实写出来 —— 绝不显示成「空列表」让人以为东西没了。 */
'use strict';
window.KBExt = (function () {
  const $ = (id) => document.getElementById(id);

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }
  function toast(m, ms) { try { UI.toast(m, ms); } catch (e) { /* 界面还没起来 */ } }

  /* ---------- 到后端的通道 ---------- */
  function dev() { return window.BzDevice || null; }
  function native() { return window.BzNative || null; }
  function ready() { const d = dev(); return !!(d && d.available && d.available()); }

  /** 走本机内核转发到后端；没有内核时如实说明，不静默返回空 */
  function call(path, method, body) {
    if (!ready()) {
      return { error: '当前入口不是手机 App（网页版没有本机内核），这些数据都在 NAS 后端上' };
    }
    const r = dev().call(path, method || 'GET', body == null ? null : body);
    return r || { error: '内核没有响应（可能还没起来）' };
  }
  /** 从返回里挑出错误原因（成功返回空串） */
  function errOf(r) {
    if (!r) return '没有响应';
    if (r.ok === false) return r.error || '未知错误';
    if (r.ok === undefined && r.error) return r.error;
    return '';
  }

  /* ---------- 小工具 ---------- */
  function fmtSize(n) {
    if (!n) return '0KB';
    if (n > 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + 'MB';
    if (n > 1024) return Math.round(n / 1024) + 'KB';
    return n + 'B';
  }
  function fmtTime(ts) {
    if (!ts) return '—';
    const d = new Date(ts), p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  /* ================= 资源 · 附件 ================= */

  let fileList = [];
  let fileTemp = '';   // 从 NAS 取回来的临时文件（关掉预览就删，手机上不留正本）

  function closeTemp() {
    if (fileTemp && native() && typeof native().deleteAttachment === 'function') {
      try { native().deleteAttachment(fileTemp); } catch (e) { /* 删不掉就算了 */ }
    }
    fileTemp = '';
  }

  function loadFiles() {
    if (!ready()) {
      fileList = [];
      $('fileStat').textContent = '要在手机 App 里才读得到 NAS 上的附件';
      renderFiles();
      return;
    }
    const r = call('/api/kb/files?limit=500', 'GET');
    const why = errOf(r);
    if (why) {
      fileList = [];
      $('fileStat').textContent = '读附件列表失败：' + why;
      renderFiles();
      return;
    }
    fileList = Array.isArray(r.files) ? r.files : [];
    const bytes = fileList.reduce((a, f) => a + (f.size || 0), 0);
    $('fileStat').textContent = fileList.length
      ? `共 ${fileList.length} 个 · ${fmtSize(bytes)} · 正本在 NAS，手机上不留`
      : '正本在 NAS 的知识库 · 手机上只临时取回';
    renderFiles();
  }

  function renderFiles() {
    const q = ($('fileSearch').value || '').trim().toLowerCase();
    const items = fileList.filter(f => !q || String(f.name || '').toLowerCase().includes(q));
    const box = $('fileList');
    box.innerHTML = '';
    items.forEach(f => box.appendChild(fileCard(f)));
    $('fileEmpty').hidden = items.length > 0;
    $('fileCount').textContent = `${items.length} 个附件`;
  }

  function fileCard(f) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-site">
          <div class="vc-icon">${f.kind === 'image' ? '🖼' : '📎'}</div>
          <div style="min-width:0">
            <div class="vc-title">${esc(f.name || '附件')}</div>
            <div class="vc-sub">${fmtSize(f.size)} · ${fmtTime(f.at)}${f.refs ? ' · 被引用 ' + f.refs + ' 处' : ''}</div>
          </div>
        </div>
        <span class="kb-chip">${f.kind === 'image' ? '图片' : '文件'}</span>
      </div>
      <div class="vc-actions">
        <button class="btn ghost sm" data-open style="flex:1">打开</button>
        <button class="btn ghost sm" data-save style="flex:1">另存</button>
        <button class="btn danger-ghost sm" data-del style="flex:1">删除</button>
      </div>`;
    el.querySelector('[data-open]').addEventListener('click', () => openFile(f));
    el.querySelector('[data-save]').addEventListener('click', () => saveFile(f));
    el.querySelector('[data-del]').addEventListener('click', () => delFile(f));
    return el;
  }

  /** 把某份附件从 NAS 取回本机临时目录，返回本地路径（顺手把上一份临时文件删掉） */
  function fetchFile(f) {
    const nb = native();
    if (!nb || typeof nb.kbFetchAttachment !== 'function') { toast('要在手机 App 里才能取回附件'); return ''; }
    closeTemp();
    const p = nb.kbFetchAttachment(f.id, f.name || 'attachment');
    if (!p) { toast('取不回这个附件：连不上 NAS，或后端上已经没有它了', 3600); return ''; }
    fileTemp = p;
    return p;
  }

  function openFile(f) {
    const nb = native();
    const p = fetchFile(f);
    if (!p) return;
    if (f.kind === 'image' && typeof nb.readAttachmentBase64 === 'function') {
      const b64 = nb.readAttachmentBase64(p);
      $('apTitle').textContent = f.name || '附件';
      const body = $('apBody');
      body.innerHTML = '';
      if (b64) {
        const img = document.createElement('img');
        img.className = 'att-full';
        img.src = 'data:image/jpeg;base64,' + b64;
        body.appendChild(img);
        UI.openPage('attPage');
      } else {
        // 不是常见图片格式：交给系统应用，别在这儿装作能预览
        if (typeof nb.openAttachment === 'function') { nb.openAttachment(p); toast('已交给系统应用打开'); }
        else toast('这个文件在本机打不开，可以「另存」到下载再用别的应用看');
      }
      return;
    }
    if (typeof nb.openAttachment === 'function') { nb.openAttachment(p); toast('已交给系统应用打开'); }
    else toast('这个入口打不开附件');
  }

  function saveFile(f) {
    const p = fetchFile(f);
    if (!p) return;
    const nb = native();
    if (nb && typeof nb.saveAttachment === 'function') nb.saveAttachment(p);
    else toast('要在手机 App 里才能另存到「下载」');
  }

  function delFile(f) {
    UI.actionSheet([
      {
        label: `删除「${f.name || '附件'}」`, danger: true, onTap: () => {
          const nb = native();
          if (!nb || typeof nb.kbDeleteAttachment !== 'function') { toast('要在手机 App 里才能删附件'); return; }
          let r = null;
          try { r = JSON.parse(nb.kbDeleteAttachment(f.id, '') || '{}'); } catch (e) { r = null; }
          if (!r || r.ok === false) { toast('没删掉：' + ((r && r.error) || '未知原因'), 3600); return; }
          toast('已从 NAS 删除');
          loadFiles();
        },
      },
      { label: '取消', cancel: true },
    ]);
  }

  /** 上传：走原生选文件（选好会叫 __bzAttachmentReady，再由 takePicked 认领） */
  function addFiles() {
    const nb = native();
    if (!nb || typeof nb.pickKbAttachment !== 'function') {
      toast('网页版选不了附件（文件拷不进 App），请在手机 App 里上传', 3600);
      return;
    }
    UI.actionSheet([
      { label: '📷 拍照', onTap: () => nb.pickKbAttachment('camera') },
      { label: '🖼 从相册选图', onTap: () => nb.pickKbAttachment('album') },
      { label: '📎 选文件（pdf / docx / zip…）', onTap: () => nb.pickKbAttachment('file') },
      { label: '取消', cancel: true },
    ]);
  }

  /** 原生选好附件后叫一声；这里只认领 dest=kb 的那份：先传 NAS，再把本机那份删掉 */
  function takePicked() {
    const nb = native();
    if (!nb || typeof nb.takeAttachmentFor !== 'function') return;
    let json = '';
    try { json = nb.takeAttachmentFor('kb') || ''; } catch (e) { return; }
    if (!json) return;
    let p = null;
    try { p = JSON.parse(json); } catch (e) { return; }
    if (!p || p.dest !== 'kb') return;
    if (p.size && p.size > 16 * 1024 * 1024) {
      toast('这个文件超过 16MB，先在手机上压一压再传', 3600);
      if (p.path && typeof nb.deleteAttachment === 'function') { try { nb.deleteAttachment(p.path); } catch (e) { /* 忽略 */ } }
      return;
    }
    toast('正在传到 NAS：' + (p.name || '附件'));
    let out = '';
    try { out = nb.kbUploadAttachment(p.path, p.name, p.kind, p.mime) || ''; } catch (e) { out = ''; }
    if (p.path && typeof nb.deleteAttachment === 'function') {
      try { nb.deleteAttachment(p.path); } catch (e) { /* 删不掉就算了 */ }
    }
    let r = null;
    try { r = JSON.parse(out); } catch (e) { r = null; }
    if (!r || r.ok === false || !r.file) {
      toast('附件没能传到 NAS：' + ((r && r.error) || '没拿到后端回执'), 4000);
      return;
    }
    toast('已存到 NAS：' + (r.file.name || p.name || '附件'));
    loadFiles();
  }

  /* ================= 资源 · 记忆 ================= */

  const KIND_NAME = { note: '笔记', event: '事件', task: '任务', decision: '决定', error: '问题', doc: '文档' };
  let memHits = [];
  let memMeta = {};            // 后端这次检索的元信息（阈值 / 被挡掉几条 / 有没有走上语义）
  let memProfile = 'balanced'; // 权重档位：均衡 / 偏语义 / 偏关键词 / 偏联想
  let memQuery = '';

  function loadMem() {
    if (!ready()) {
      memHits = []; memMeta = {};
      $('memStat').textContent = '要在手机 App 里才读得到 NAS 上的记忆';
      renderMem();
      return;
    }
    memQuery = ($('memSearch').value || '').trim();
    const r = call('/api/agent/memory?q=' + encodeURIComponent(memQuery)
      + '&limit=' + (memQuery ? 20 : 100)
      + '&profile=' + encodeURIComponent(memProfile), 'GET');
    const why = errOf(r);
    if (why) {
      memHits = []; memMeta = {};
      $('memStat').textContent = '读记忆失败：' + why;
      renderMem();
      return;
    }
    memMeta = r || {};
    memHits = Array.isArray(r.hits) ? r.hits : [];
    $('memStat').textContent = memQuery ? memSummary() : `长期记忆 ${memHits.length} 条 · 搜索框里说人话就是语义搜索`;
    // 没在搜索时顺手把记忆树也取回来（只在过期/新增时才需要重取，代价很小）
    if (!memQuery) loadTree(true);
    renderMem();
  }

  function memSummary() {
    const bits = [`「${memQuery}」命中 ${memHits.length} 条`];
    if (memMeta.filtered) bits.push(`挡掉 ${memMeta.filtered} 条不够相关的`);
    if (!memMeta.vectorUsed && memMeta.vectorNote) bits.push(memMeta.vectorNote);
    return bits.join(' · ');
  }

  /* ---- 记忆树：按天/周压出来的摘要。搜索时藏起来，让结果独占视线 ---- */

  let memTree = null;   // null = 还没取过

  function loadTree(force) {
    if (!ready()) { memTree = { nodes: [], staleCount: 0 }; renderTree(); return; }
    if (memTree && !force) { renderTree(); return; }
    const r = call('/api/agent/memory/tree?limit=12', 'GET');
    const why = errOf(r);
    if (why) { memTree = { nodes: [], staleCount: 0, error: why }; renderTree(); return; }
    memTree = { nodes: Array.isArray(r.nodes) ? r.nodes : [], staleCount: r.staleCount || 0 };
    renderTree();
  }

  function renderTree() {
    const box = $('memTree');
    const nodes = (memTree && memTree.nodes) || [];
    const stale = (memTree && memTree.staleCount) || 0;
    const show = !memQuery && (nodes.length > 0 || stale > 0 || (memTree && memTree.error));
    box.hidden = !show;
    if (!show) return;
    $('memTreeHead').textContent = stale > 0
      ? `记忆树（有 ${stale} 天摘要没跟上，点右上角按钮补一次）`
      : '记忆树（按天/周压出来的摘要）';
    const list = $('memTreeList');
    list.innerHTML = '';
    if (memTree && memTree.error) {
      list.innerHTML = '<div class="empty-tip">读记忆树失败：' + esc(memTree.error) + '</div>';
      return;
    }
    nodes.forEach(n => list.appendChild(treeCard(n)));
  }

  function treeCard(n) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    const lv = n.level === 2 ? '周' : '天';
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-title">${esc(n.title || String(n.id))}</div>
        <span class="kb-chip">${lv} · ${n.chunkCount || 0} 条</span>
      </div>
      <div class="kb-body-text">${esc(n.summary || '（摘要还没生成，点右上角按钮补一次）')}</div>`;
    return el;
  }

  function rebuildTree() {
    if (!ready()) { toast('要在手机 App 里才能重建记忆树'); return; }
    const r = call('/api/agent/memory/tree/rebuild', 'POST', JSON.stringify({}));
    const why = errOf(r);
    if (why) { toast('重建失败：' + why, 4000); return; }
    toast('已重建 ' + (r.days || 0) + ' 天的摘要');
    loadMem();
  }

  /** 整理重复记忆：合并同类的机械回执（删除操作，所以只由人在这里手动点） */
  function compactMemory() {
    if (!ready()) { toast('要在手机 App 里才能整理记忆'); return; }
    UI.actionSheet([
      {
        label: '整理重复记忆（每组只留最新一次）', danger: true, onTap: () => {
          const r = call('/api/agent/memory/compact', 'POST', JSON.stringify({}));
          const why = errOf(r);
          if (why) { toast('整理失败：' + why, 4000); return; }
          toast(`整理了 ${r.groups || 0} 组，合并掉 ${r.merged || 0} 条`);
          memTree = null;
          loadMem();
        },
      },
      { label: '取消', cancel: true },
    ]);
  }

  function renderMem() {
    const box = $('memList');
    box.innerHTML = '';
    memHits.forEach(h => box.appendChild(memCard(h)));
    renderTree();
    $('memEmpty').hidden = memHits.length > 0 || !$('memTree').hidden;
    $('memCount').textContent = `${memHits.length} 条`;
    // 空结果必须说清"是被阈值挡了"还是"真没有"——不然看着像记忆丢了
    if (!memHits.length) {
      const tip = $('memEmpty');
      if (memQuery) {
        const cut = memMeta.minVector ? `没有够上「最低语义相似度 ${memMeta.minVector}」的记忆` : '没有足够相关的记忆';
        tip.innerHTML = esc(cut) + (memMeta.vectorNote ? '<br>' + esc(memMeta.vectorNote) : '');
      } else {
        tip.innerHTML = '还没有记忆<br>白泽每次派活的结论会自动落进来，也可以点「＋ 记一条」';
      }
    }
  }

  function memCard(h) {
    const c = h.chunk || h;   // 后端回的是 {chunk:{…}, score, why, parts}
    const el = document.createElement('div');
    el.className = 'vault-card';
    const content = String(c.content || '');
    const title = c.title || content.split('\n')[0].slice(0, 40);
    const p = h.parts || {};
    // 把"为什么召回它"摊开：语义 / 关键词 / 联想 三项谁在起作用，一眼能看出检索是不是真的在工作
    const whyBits = [];
    if (p.vector) whyBits.push('语义 ' + Math.round(p.vector * 100) + '%');
    if (p.keyword) whyBits.push('关键词 ' + Math.round(p.keyword * 100) + '%');
    if (p.graph) whyBits.push('联想 ' + Math.round(p.graph * 100) + '%');
    if (!whyBits.length) whyBits.push('新鲜度');
    const pct = h.score ? Math.min(100, Math.round(h.score * 100)) : 0;
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-title">${esc(title || '（无标题）')}</div>
        <span class="kb-chip ${c.hasVector ? 'ok' : ''}">${c.hasVector ? '有向量' : '关键词'}</span>
      </div>
      <div class="vc-sub" style="margin-top:4px">${esc(KIND_NAME[c.kind] || c.kind || '记忆')}${c.source ? ' · 来自 ' + esc(c.source) : ''} · ${fmtTime(c.createdAt)}${pct ? ' · 综合 ' + pct + '%' : ''}</div>
      <div class="kb-body-text">${esc(content.slice(0, 700))}${content.length > 700 ? '…' : ''}</div>
      <div class="vc-line">召回原因：${esc(h.why || '新鲜度')}（${whyBits.join(' · ')}）</div>`;
    return el;
  }

  function openMemForm() {
    const html = `
      <div class="f-block">
        <label class="f-label">标题（选填）</label>
        <input class="f-input" id="km-title" placeholder="例如：白泽 NAS 部署备忘">
      </div>
      <div class="f-block">
        <label class="f-label">内容<span class="req">*</span></label>
        <textarea class="f-textarea" id="km-content" rows="6" placeholder="要长期记住的事，写人话就行"></textarea>
      </div>
      <div class="f-block">
        <label class="f-label">类型</label>
        <div class="chip-group" id="km-kind">
          ${['note', 'decision', 'event', 'task', 'error', 'doc']
            .map((k, i) => `<button class="chip-opt${i === 0 ? ' on' : ''}" data-v="${k}">${KIND_NAME[k]}</button>`).join('')}
        </div>
      </div>
      <button class="btn-block" id="km-save">记进长期记忆</button>`;
    UI.openTool('记一条记忆', html, () => {
      const grp = document.getElementById('km-kind');
      grp.addEventListener('click', (e) => {
        const b = e.target.closest('.chip-opt');
        if (!b) return;
        grp.querySelectorAll('.chip-opt').forEach(x => x.classList.toggle('on', x === b));
      });
      const save = () => {
        const content = document.getElementById('km-content').value.trim();
        if (!content) { toast('内容不能为空'); return; }
        const picked = grp.querySelector('.chip-opt.on');
        const r = call('/api/agent/memory/write', 'POST', JSON.stringify({
          content,
          title: document.getElementById('km-title').value.trim(),
          kind: picked ? picked.dataset.v : 'note',
        }));
        const why = errOf(r);
        if (why) { toast('没记上去：' + why, 3600); return; }
        UI.closePage('toolPage');
        toast('已记进长期记忆');
        loadMem();
      };
      document.getElementById('km-save').addEventListener('click', save);
      UI.setToolAction('保存', save);
    });
  }

  /* ================= 配置 · API 服务（模型站密钥） ================= */

  let provList = [];
  let embInfo = {};
  let allowRemote = false;

  function loadKeys() {
    const warn = $('keyWarn');
    if (!ready()) {
      provList = []; embInfo = {};
      warn.hidden = false;
      warn.firstElementChild.textContent = '还没连上白泽后端，模型站密钥读不到。去「其他功能 → 跨端」填上后端地址和令牌。';
      renderKeys();
      return;
    }
    const r = call('/api/agent/providers', 'GET');
    const why = errOf(r);
    if (why) {
      provList = []; embInfo = {};
      warn.hidden = false;
      warn.firstElementChild.textContent = '读模型站失败：' + why;
      renderKeys();
      return;
    }
    warn.hidden = true;
    provList = Array.isArray(r.providers) ? r.providers : [];
    allowRemote = !!r.allowRemote;
    const c = call('/api/agent/config', 'GET');
    embInfo = (c && c.embedding) ? c.embedding : {};
    renderKeys();
  }

  function renderKeys() {
    const box = $('keyList');
    box.innerHTML = '';
    provList.forEach(p => box.appendChild(provCard(p)));
    const hasEmb = !!(embInfo && (embInfo.baseUrl || embInfo.model || embInfo.hasApiKey));
    if (hasEmb) box.appendChild(embCard());
    $('keyEmpty').hidden = provList.length > 0 || hasEmb;
  }

  function provCard(p) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-title">${esc(p.name || '(未命名)')}</div>
        <span class="kb-chip ${p.usable ? 'ok' : p.hasApiKey ? 'warn' : 'danger'}">${p.usable ? '可用' : p.hasApiKey ? '未探通' : '缺 Key'}</span>
      </div>
      <div class="vc-line kb-code">${esc(p.baseUrl || '（没填 baseURL）')}</div>
      <div class="vc-sub" style="margin-top:4px">${esc(p.protocol || 'openai')} · ${esc(p.model || '（没填模型 ID）')} · Key ${p.hasApiKey ? '已配置' : '未配置'}${p.local ? ' · 本机' : ''}</div>
      ${p.error ? `<div class="vc-line" style="color:var(--rose)">${esc(p.error)}</div>` : ''}
      <div class="vc-actions">
        <button class="btn ghost sm" data-test style="flex:1">测连通</button>
        <button class="btn ghost sm" data-edit style="flex:1">编辑</button>
        <button class="btn danger-ghost sm" data-del style="flex:1">删除</button>
      </div>`;
    el.querySelector('[data-test]').addEventListener('click', (e) => {
      const btn = e.currentTarget;
      btn.textContent = '测试中…';
      btn.disabled = true;
      const r = call('/api/agent/providers', 'POST', JSON.stringify({ action: 'test', name: p.name }));
      btn.disabled = false;
      btn.textContent = '测连通';
      const why = errOf(r);
      if (why) { toast('测试失败：' + why, 4000); return; }
      const res = r.result || {};
      toast(`通了 · ${res.latencyMs || '?'}ms · ${res.reply || ''}`, 4000);
    });
    el.querySelector('[data-edit]').addEventListener('click', () => openKeyForm(p));
    el.querySelector('[data-del]').addEventListener('click', () => {
      UI.actionSheet([
        {
          label: `删除模型站「${p.name}」`, danger: true, onTap: () => {
            const r = call('/api/agent/providers', 'POST', JSON.stringify({ action: 'remove', name: p.name }));
            const why = errOf(r);
            if (why) { toast('没删掉：' + why, 3600); return; }
            toast('已删除');
            loadKeys();
          },
        },
        { label: '取消', cancel: true },
      ]);
    });
    return el;
  }

  /** 向量化通道（记忆的语义检索用它；这是独立的一条，通常跟对话模型不是同一家） */
  function embCard() {
    const el = document.createElement('div');
    el.className = 'vault-card';
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-title">向量化通道（记忆用）</div>
        <span class="kb-chip ${embInfo.usable ? 'ok' : 'warn'}">${embInfo.usable ? '语义检索已启用' : '未启用'}</span>
      </div>
      <div class="vc-line kb-code">${esc(embInfo.baseUrl || '（没填 baseUrl）')}</div>
      <div class="vc-sub" style="margin-top:4px">${esc(embInfo.model || '（没填模型）')} · 维度 ${embInfo.dim || '—'} · Key ${embInfo.hasApiKey ? '已配置' : '未配置'}</div>
      ${embInfo.err ? `<div class="vc-line" style="color:var(--rose)">${esc(embInfo.err)}</div>` : ''}
      <div class="vc-actions"><button class="btn ghost sm" data-edit style="flex:1">编辑向量化通道</button></div>`;
    el.querySelector('[data-edit]').addEventListener('click', () => openEmbForm());
    return el;
  }

  function openKeyForm(p) {
    const one = p || {};
    const html = `
      <div class="f-block">
        <label class="f-label">名称<span class="req">*</span></label>
        <input class="f-input" id="kp-name" value="${esc(one.name || '')}" placeholder="例如：阿里云百炼 / DeepSeek 主力">
      </div>
      <div class="f-block">
        <label class="f-label">协议</label>
        <div class="chip-group" id="kp-proto">
          <button class="chip-opt${(one.protocol || 'openai') === 'openai' ? ' on' : ''}" data-v="openai">OpenAI 兼容</button>
          <button class="chip-opt${one.protocol === 'anthropic' ? ' on' : ''}" data-v="anthropic">Anthropic</button>
        </div>
      </div>
      <div class="f-block">
        <label class="f-label">baseURL<span class="req">*</span></label>
        <input class="f-input" id="kp-base" value="${esc(one.baseUrl || '')}" placeholder="https://dashscope.aliyuncs.com/compatible-mode/v1">
      </div>
      <div class="f-block">
        <label class="f-label">API Key ${one.hasApiKey ? '<span class="kb-chip ok">已配置</span>' : ''}</label>
        <input class="f-input" id="kp-key" type="password" placeholder="${one.hasApiKey ? '留空 = 不改原来的 Key' : 'sk-...'}">
      </div>
      <div class="f-block">
        <label class="f-label">模型 ID</label>
        <input class="f-input" id="kp-model" value="${esc(one.model || '')}" placeholder="qwen-plus">
      </div>
      <p class="f-note">地址不是本机时必须允许远端模型地址（「其他功能 → 设置 → 安全开关」里有开关），否则会明确报错。</p>
      <button class="btn-block" id="kp-save">保存</button>`;
    UI.openTool(p ? '编辑模型站' : '新增模型站', html, () => {
      const grp = document.getElementById('kp-proto');
      grp.addEventListener('click', (e) => {
        const b = e.target.closest('.chip-opt');
        if (!b) return;
        grp.querySelectorAll('.chip-opt').forEach(x => x.classList.toggle('on', x === b));
      });
      const save = () => {
        const name = document.getElementById('kp-name').value.trim();
        const base = document.getElementById('kp-base').value.trim();
        if (!name) { toast('名称不能为空'); return; }
        if (!base) { toast('baseURL 不能为空'); return; }
        const picked = grp.querySelector('.chip-opt.on');
        const body = {
          action: 'upsert',
          allowRemote: true,
          config: {
            name,
            protocol: picked ? picked.dataset.v : 'openai',
            baseUrl: base,
            apiKey: document.getElementById('kp-key').value,
            model: document.getElementById('kp-model').value.trim(),
          },
        };
        const r = call('/api/agent/providers', 'POST', JSON.stringify(body));
        const why = errOf(r);
        if (why) { toast('没保存：' + why, 4000); return; }
        UI.closePage('toolPage');
        toast('已保存到后端');
        loadKeys();
      };
      document.getElementById('kp-save').addEventListener('click', save);
      UI.setToolAction('保存', save);
    });
  }

  function openEmbForm() {
    const html = `
      <div class="f-block">
        <label class="f-label">baseUrl<span class="req">*</span></label>
        <input class="f-input" id="ke-base" value="${esc(embInfo.baseUrl || '')}" placeholder="https://dashscope.aliyuncs.com/compatible-mode/v1">
      </div>
      <div class="f-block">
        <label class="f-label">模型<span class="req">*</span></label>
        <input class="f-input" id="ke-model" value="${esc(embInfo.model || '')}" placeholder="text-embedding-v3">
      </div>
      <div class="f-block">
        <label class="f-label">API Key ${embInfo.hasApiKey ? '<span class="kb-chip ok">已配置</span>' : ''}</label>
        <input class="f-input" id="ke-key" type="password" placeholder="${embInfo.hasApiKey ? '留空 = 不改原来的 Key' : 'sk-...'}">
      </div>
      <p class="f-note">这条通道专给记忆的语义检索用，跟对话模型分开配。改完保存即生效；维度由通道自己报，不用填。</p>
      <button class="btn-block" id="ke-save">保存</button>`;
    UI.openTool('向量化通道', html, () => {
      const save = () => {
        const base = document.getElementById('ke-base').value.trim();
        const model = document.getElementById('ke-model').value.trim();
        if (!base || !model) { toast('baseUrl 与模型都要填'); return; }
        const body = { embeddingBaseUrl: base, embeddingModel: model };
        const key = document.getElementById('ke-key').value;
        if (key) body.embeddingApiKey = key;
        const r = call('/api/agent/config', 'POST', JSON.stringify(body));
        const why = errOf(r);
        if (why) { toast('没保存：' + why, 4000); return; }
        UI.closePage('toolPage');
        toast('已保存，向量通道会立刻重建');
        loadKeys();
      };
      document.getElementById('ke-save').addEventListener('click', save);
      UI.setToolAction('保存', save);
    });
  }

  /* ================= 配置 · 模型审批 ================= */

  let approvals = [];

  function loadApprovals() {
    if (!ready()) {
      approvals = [];
      $('apprStat').textContent = '要在手机 App 里才读得到后端的审批队列';
      renderApprovals();
      return;
    }
    const r = call('/api/agent/approvals', 'GET');
    const why = errOf(r);
    if (why) {
      approvals = [];
      $('apprStat').textContent = '读审批队列失败：' + why;
      renderApprovals();
      return;
    }
    approvals = (Array.isArray(r.approvals) ? r.approvals : []).filter(a => a.status === 'pending');
    $('apprStat').textContent = approvals.length
      ? `有 ${approvals.length} 个危险操作在等你放行`
      : '危险动作先挂起，人工放行后才真的执行';
    renderApprovals();
  }

  function renderApprovals() {
    const box = $('apprList');
    box.innerHTML = '';
    approvals.forEach(a => box.appendChild(apprCard(a)));
    $('apprEmpty').hidden = approvals.length > 0;
  }

  function apprCard(a) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    const args = a.args ? JSON.stringify(a.args, null, 1) : '';
    el.innerHTML = `
      <div class="kb-head-row">
        <div class="vc-title">${esc(a.tool || '危险操作')}</div>
        <span class="kb-chip warn">待放行</span>
      </div>
      <div class="vc-sub" style="margin-top:4px">${fmtTime(a.at)}${a.runId ? ' · 运行 ' + esc(String(a.runId).slice(0, 8)) : ''}</div>
      ${args ? `<div class="kb-code" style="margin-top:6px">${esc(args.slice(0, 500))}</div>` : ''}
      <div class="vc-actions">
        <button class="btn primary sm" data-ok style="flex:1">放行</button>
        <button class="btn danger-ghost sm" data-no style="flex:1">驳回</button>
      </div>`;
    el.querySelector('[data-ok]').addEventListener('click', (e) => {
      e.currentTarget.disabled = true;
      const r = call('/api/agent/approvals/' + encodeURIComponent(a.id) + '/approve', 'POST', JSON.stringify({ by: '手机' }));
      const why = errOf(r);
      if (why) { toast('没放行成功：' + why, 3600); e.currentTarget.disabled = false; return; }
      toast('已放行');
      loadApprovals();
    });
    el.querySelector('[data-no]').addEventListener('click', () => {
      UI.actionSheet([
        {
          label: '驳回这个操作', danger: true, onTap: () => {
            const r = call('/api/agent/approvals/' + encodeURIComponent(a.id) + '/reject', 'POST', JSON.stringify({ by: '手机', reason: '手机端驳回' }));
            const why = errOf(r);
            if (why) { toast('没驳回成功：' + why, 3600); return; }
            toast('已驳回');
            loadApprovals();
          },
        },
        { label: '取消', cancel: true },
      ]);
    });
    return el;
  }

  function loadApprTimeout() {
    if (!ready()) { $('apprTimeout').value = ''; return; }
    const r = call('/api/agent/config', 'GET');
    if (errOf(r)) { $('apprTimeout').value = ''; return; }
    $('apprTimeout').value = r.approvalTimeoutSec || 300;
  }

  function saveApprTimeout() {
    const sec = parseInt($('apprTimeout').value, 10);
    if (!sec || sec <= 0) { toast('超时要填一个大于 0 的秒数'); return; }
    const r = call('/api/agent/config', 'POST', JSON.stringify({ approvalTimeoutSec: sec }));
    const why = errOf(r);
    if (why) { toast('没保存：' + why, 3600); return; }
    toast('已保存：超时 ' + sec + ' 秒按拒绝处理');
  }

  /* ================= 接线 ================= */

  function refresh(paneId) {
    switch (paneId) {
      case 'view-files': loadFiles(); break;
      case 'view-memory': loadMem(); break;
      case 'view-keys': loadKeys(); break;
      case 'view-approval': loadApprovals(); loadApprTimeout(); break;
    }
  }

  /** 当前正显示的是哪一页（不在知识库里就返回空串） */
  function currentPane() {
    const ids = ['view-files', 'view-memory', 'view-keys', 'view-approval'];
    return ids.find(id => $(id) && !$(id).hidden) || '';
  }

  function init() {
    $('fileSearch').addEventListener('input', renderFiles);
    $('btnFileRefresh').addEventListener('click', loadFiles);
    $('btnFileAdd').addEventListener('click', addFiles);

    let memTimer = null;
    $('memSearch').addEventListener('input', () => {
      clearTimeout(memTimer);
      memTimer = setTimeout(loadMem, 400);   // 语义搜索要打后端，别每敲一个字就发一次
    });
    $('btnMemRefresh').addEventListener('click', loadMem);
    $('btnMemAdd').addEventListener('click', openMemForm);
    // 记忆整理：重建摘要 / 合并同类回执（后者是删除操作，所以在操作单里二次确认）
    $('btnMemTree').addEventListener('click', () => {
      UI.actionSheet([
        { label: '重建记忆树摘要', onTap: () => rebuildTree() },
        { label: '整理重复记忆（合并同类回执）', danger: true, onTap: () => compactMemory() },
        { label: '取消', cancel: true },
      ]);
    });
    // 权重档位：均衡 / 偏语义 / 偏关键词 / 偏联想（换档立刻重查，结果顺序会变）
    const prof = $('memProfile');
    prof.addEventListener('click', (e) => {
      const b = e.target.closest('.vs-btn');
      if (!b) return;
      prof.querySelectorAll('.vs-btn').forEach(x => x.classList.toggle('active', x === b));
      memProfile = b.dataset.p || 'balanced';
      loadMem();
    });

    $('btnKeyRefresh').addEventListener('click', loadKeys);
    $('btnKeyAdd').addEventListener('click', () => openKeyForm(null));
    $('keyGoCfg').addEventListener('click', () => UI.switchTab('view-settings'));

    $('btnApprRefresh').addEventListener('click', loadApprovals);
    $('btnApprSave').addEventListener('click', saveApprTimeout);

    // 关掉附件预览就把临时文件删掉（手机上不留正本）
    $('apClose').addEventListener('click', closeTemp);

    // 切到本模块的页面时取最新数据；原生选完附件后认领 dest=kb 的那份
    window.addEventListener('bz:page', (e) => {
      const id = e && e.detail ? e.detail.id : '';
      if (id) refresh(id);
    });
    UI.onAttachmentReady(takePicked);

    // 后端状态变化时，若正停在某一页就顺手刷新
    window.addEventListener('bz:syncdone', () => { const p = currentPane(); if (p) refresh(p); });
  }

  return { init, refresh, closeTemp };
})();
