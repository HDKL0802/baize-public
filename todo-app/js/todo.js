/* 白泽待办中心 - 待办模块
   数据字段：title / category(所属领域) / priority / form(任务形式) / due / weekly(每周提醒)
             / estimate(预计耗时分钟) / deps(前置依赖) / note / owner / status / remind */
'use strict';
window.Todo = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;

  let list = [];
  let seg = 'user';            // user | agent
  let viewMode = 'schedule';   // schedule 日程排期 | leisure 闲暇待办
  let sortMode = 'time';       // time | priority
  let editingId = null;
  let draft = null;            // 表单草稿

  const DOMAINS = ['工作开发', '创作', '日常生活'];
  const PRI = { high: '高优先级', mid: '中优先级', low: '低优先级' };
  const ESTIMATES = [15, 30, 45, 60, 90, 120];

  /* 历史数据里的旧领域名（带括号说明）统一收敛为「创作」 */
  function migrate(list) {
    let changed = false;
    list.forEach(t => {
      if (t.category === '创作（AI漫剧等）') { t.category = '创作'; changed = true; }
    });
    return changed;
  }

  function reload() {
    list = Store.getTodos();
    if (migrate(list)) Store.saveTodos(list);
    render();
  }
  function find(id) { return list.find(x => x.id === id); }
  function getList() { return list; }

  // 每次跟后端同步完，把本模块的内存列表重新装载一遍。
  // 这份 list 是 saveTodos 的「用户意图来源」：它一旦滞后于后端正本，
  // 下一次任何操作都会把「后端已经删掉的待办」当成用户新增又推回去（真发生过）。
  window.addEventListener('bz:syncdone', () => { reload(); });

  /* ================= 统计 / 排序 ================= */
  function updateStat() {
    const mine = list.filter(t => t.owner === seg && t.status !== 'done' && t.status !== 'cancelled');
    const sched = mine.filter(t => t.form !== 'leisure').length;
    const leisure = mine.filter(t => t.form === 'leisure').length;
    $('todoStat').textContent = `日程 ${sched} 项 · 闲暇 ${leisure} 项`;
    $('cntSched').textContent = `(${sched})`;
    $('cntLeisure').textContent = `(${leisure})`;
  }

  function sortItems(arr) {
    const byTime = (a, b) => (a.due || '9999').localeCompare(b.due || '9999');
    const byPri = (a, b) => ({ high: 0, mid: 1, low: 2 }[a.priority] ?? 9) - ({ high: 0, mid: 1, low: 2 }[b.priority] ?? 9);
    return sortMode === 'priority' ? arr.sort((a, b) => byPri(a, b) || byTime(a, b)) : arr.sort(byTime);
  }

  /* ================= 渲染 ================= */
  function card(t) {
    const el = document.createElement('div');
    el.className = 'todo-card' + (t.status === 'done' ? ' done' : '');
    const st = (v) => `tag form-${v}`;
    const meta = [];
    if (t.category) meta.push(`<span class="tag cat">${esc(t.category)}</span>`);
    meta.push(`<span class="tag p-${t.priority}">${PRI[t.priority] || t.priority}</span>`);
    if (t.due) meta.push(`<span class="tag due">🕐 ${UI_.dueLabel(t.due)}</span>`);
    if (t.weekly) meta.push(`<span class="tag weekly">🔁 每周提醒</span>`);
    if (t.estimate) meta.push(`<span class="tag">⏱ ${t.estimate}m</span>`);
    if ((t.deps || []).length) meta.push(`<span class="tag dep">依赖 ${t.deps.length} 项</span>`);
    const atts = Array.isArray(t.atts) ? t.atts : [];
    if (atts.length) meta.push(`<span class="tag att">📎 附件 ${atts.length}</span>`);
    if (t.status === 'doing') meta.push('<span class="tag state">进行中</span>');
    if (t.status === 'done') meta.push('<span class="tag state done">已完成</span>');
    if (t.owner === 'agent' && t.status !== 'done' && t.status !== 'cancelled') {
      meta.push(`<span class="tag authorize" data-auth="${t.id}">授权执行</span>`);
    }

    el.innerHTML = `
      <div class="tc-row1">
        <div class="tc-check ${t.status === 'done' ? 'on' : ''}" data-check="${t.id}">✓</div>
        <div class="tc-title">${esc(t.title)}</div>
        ${t.owner === 'agent' ? '<span class="tc-owner">Agent</span>' : ''}
        <button class="card-more" data-more="${t.id}">⋯</button>
      </div>
      <div class="tc-meta">${meta.join('')}</div>
      ${t.note ? `<div class="tc-note">${esc(t.note)}</div>` : ''}
      ${atts.length ? `<div class="tc-atts">${atts.slice(0, 3).map((a, i) =>
        a.kind === 'image' && a.thumb
          ? `<img class="tc-att-thumb" data-att="${t.id}:${i}" src="${a.thumb}" alt="">`
          : `<span class="tc-att-file" data-att="${t.id}:${i}">📎 ${esc(a.name || '文件')}</span>`
      ).join('')}${atts.length > 3 ? `<span class="tc-att-more">+${atts.length - 3}</span>` : ''}</div>` : ''}`;

    el.addEventListener('click', (e) => {
      if (e.target.closest('[data-check]') || e.target.closest('[data-auth]') || e.target.closest('[data-more]')
        || e.target.closest('[data-att]')) return;
      openForm(t.id);
    });
    el.querySelectorAll('[data-att]').forEach(node => node.addEventListener('click', (e) => {
      e.stopPropagation();
      const a = atts[Number(node.dataset.att.split(':')[1])];
      if (a) openAtt(a, { name: t.title, onDelete: () => removeAttOfTodo(t, a.id) });
    }));
    el.querySelector('[data-check]').addEventListener('click', () => toggleDone(t.id));
    el.querySelector('[data-more]').addEventListener('click', (e) => { e.stopPropagation(); moreMenu(t); });
    const authEl = el.querySelector('[data-auth]');
    if (authEl) authEl.addEventListener('click', (e) => { e.stopPropagation(); authorizeAgent(t); });

    const press = pressDetector(() => moreMenu(t));
    el.addEventListener('pointerdown', press.down);
    el.addEventListener('pointerup', press.up);
    el.addEventListener('pointerleave', press.up);
    return el;
  }

  function moreMenu(t) {
    UI_.actionSheet([
      { label: '编辑', onTap: () => openForm(t.id) },
      ...(t.status !== 'done'
        ? [{ label: '标记完成', onTap: () => toggleDone(t.id) }]
        : [{ label: '恢复待办', onTap: () => setStatus(t.id, 'todo') }]),
      ...(t.form === 'leisure'
        ? [{ label: '转为日程排期', onTap: () => { t.form = 'schedule'; if (!t.due) t.due = defaultDue(); Store.saveTodos(list); render(); UI_.toast('已转为日程排期'); } }]
        : [{ label: '转为闲暇待办', onTap: () => { t.form = 'leisure'; t.due = ''; Store.saveTodos(list); render(); UI_.toast('已转为闲暇待办'); } }]),
      ...(t.owner === 'agent' && t.status !== 'done'
        ? [{ label: '授权 Agent 执行', onTap: () => authorizeAgent(t) }] : []),
      { label: '删除', danger: true, onTap: () => confirmDelete(t) },
      { label: '取消', cancel: true },
    ]);
  }

  function authorizeAgent(t) {
    t.auth = 'pending';
    Store.saveTodos(list);
    UI_.toast('已生成执行意向；接入 Agent 后由后端领取执行');
    render();
  }

  function toggleDone(id) { setStatus(id, find(id).status === 'done' ? 'todo' : 'done'); }
  function setStatus(id, s) { const t = find(id); if (!t) return; t.status = s; Store.saveTodos(list); render(); }

  function confirmDelete(t) {
    UI_.actionSheet([
      { label: `删除「${t.title}」`, danger: true, onTap: () => { list = list.filter(x => x.id !== t.id); Store.saveTodos(list); render(); UI_.toast('已删除'); } },
      { label: '取消', cancel: true },
    ]);
  }

  function render() {
    const items = list.filter(t => t.owner === seg && (t.form === 'leisure' ? 'leisure' : 'schedule') === viewMode);
    const box = $('todoList');
    box.innerHTML = '';

    if (viewMode === 'schedule') {
      const groups = new Map();
      items.forEach(t => {
        const k = !t.due ? '未排期' : new Date(t.due).toDateString();
        if (!groups.has(k)) groups.set(k, []);
        groups.get(k).push(t);
      });
      [...groups.entries()]
        .sort((a, b) => (a[0] === '未排期' ? 1 : new Date(a[0])) - (b[0] === '未排期' ? 1 : new Date(b[0])))
        .forEach(([k, arr]) => {
          const head = document.createElement('div');
          head.className = 'group-head';
          head.innerHTML = `<span>${k === '未排期' ? '📌 未排期' : '🗓 ' + fmtDay(new Date(k))}</span><span>${arr.length} 项</span>`;
          box.appendChild(head);
          sortItems(arr).forEach(t => box.appendChild(card(t)));
        });
    } else {
      sortItems(items).forEach(t => box.appendChild(card(t)));
    }

    $('todoEmpty').hidden = items.length > 0;
    updateStat();
    UI_.syncNativeReminders();   // 数据变化后重排原生提醒（APK 内生效）
  }

  function fmtDay(d) {
    const n = new Date();
    const same = (a, b) => a.toDateString() === b.toDateString();
    const tmr = new Date(n); tmr.setDate(tmr.getDate() + 1);
    if (same(d, n)) return '今天';
    if (same(d, tmr)) return '明天';
    return `${d.getMonth() + 1}/${d.getDate()} 周${'日一二三四五六'[d.getDay()]}`;
  }

  /* ================= 快捷添加 ================= */
  function defaultDue() {
    const d = new Date(Date.now() + 3600e3);
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  function quickAdd(text) {
    const title = text.trim();
    if (!title) return;
    list.unshift({
      id: Store.uid(), title, category: '日常生活',
      priority: 'mid', form: viewMode,
      due: viewMode === 'schedule' ? defaultDue() : '',
      weekly: false, estimate: 0, note: '', deps: [], atts: [],
      owner: seg === 'agent' ? 'agent' : 'user',
      status: 'todo', remind: false, createdAt: Date.now(),
    });
    Store.saveTodos(list); render();
    UI_.toast('已添加：' + title);
  }

  /* ================= 全屏表单 ================= */
  function openForm(id) {
    editingId = id;
    const t = id ? find(id) : null;
    draft = t ? JSON.parse(JSON.stringify(t)) : {
      title: '', category: '工作开发', priority: 'mid',
      form: viewMode, due: viewMode === 'schedule' ? defaultDue() : '',
      weekly: false, estimate: 0, note: '', deps: [], atts: [], remind: false,
    };
    if (!Array.isArray(draft.atts)) draft.atts = [];
    $('tpTitle').textContent = id ? '编辑待办任务' : '新建待办任务';
    $('tTitle').value = draft.title || '';
    $('tNote').value = draft.note || '';
    $('tDue').value = draft.due || '';
    markChip('tDomain', draft.category);
    markChip('tPriority', draft.priority);
    markChip('tForm', draft.form);
    markChip('tEstimate', String(draft.estimate || ''));
    $('tWeekly').classList.toggle('on', !!draft.weekly);
    $('tRemind').classList.toggle('on', !!draft.remind);
    syncFormCard();
    renderDeps();
    renderAtts();
    UI_.openPage('todoPage');
  }

  function syncFormCard() {
    const leisure = draft.form === 'leisure';
    const badge = $('tFormBadge');
    badge.textContent = leisure ? '闲暇待办' : '日程排期';
    badge.classList.toggle('sched', !leisure);
    $('tFormNote').textContent = leisure
      ? '闲暇待办专为创作、灵感项目、闲暇阅读等设计，不设死板日期，可在有空时随时挑选启动。'
      : '日程排期按具体日期时间推进，到点可提醒；适合有明确截止的任务。';
    $('tDueBlock').hidden = leisure;
  }

  function renderDeps() {
    const box = $('tDeps');
    box.innerHTML = '';
    const cands = list.filter(x => x.id !== editingId && x.status !== 'done');
    if (!cands.length) {
      box.innerHTML = '<div class="dep-empty">暂无可作为前置的任务</div>';
      return;
    }
    cands.forEach(x => {
      const on = (draft.deps || []).includes(x.id);
      const el = document.createElement('button');
      el.className = 'dep-item' + (on ? ' on' : '');
      el.innerHTML = `
        <span class="dep-box">✓</span>
        <span class="dep-main">
          <span class="dep-name">${esc(x.title)}</span>
          <span class="dep-sub">${x.form === 'leisure' ? '闲暇待办' : (x.due ? '排期 ' + x.due.slice(0, 10) : '日程排期')}</span>
        </span>`;
      el.addEventListener('click', () => {
        const i = (draft.deps || []).indexOf(x.id);
        if (i >= 0) draft.deps.splice(i, 1); else (draft.deps = draft.deps || []).push(x.id);
        el.classList.toggle('on');
      });
      box.appendChild(el);
    });
  }

  function saveForm() {
    const title = $('tTitle').value.trim();
    if (!title) { UI_.toast('任务标题不能为空'); return; }
    const leisure = draft.form === 'leisure';
    const payload = {
      title,
      note: $('tNote').value,
      category: draft.category,
      priority: draft.priority,
      form: draft.form,
      due: leisure ? '' : $('tDue').value,
      weekly: draft.weekly,
      estimate: Number(draft.estimate) || 0,
      deps: draft.deps || [],
      atts: draft.atts || [],
      remind: !!draft.remind,
    };
    if (editingId) Object.assign(find(editingId), payload);
    else list.unshift({
      id: Store.uid(), ...payload,
      owner: seg === 'agent' ? 'agent' : 'user',
      status: 'todo', createdAt: Date.now(),
    });
    Store.saveTodos(list);
    render();
    UI_.closePage('todoPage');
    UI_.toast(editingId ? '已保存' : '已创建任务');
  }

  function markChip(containerId, value) {
    document.querySelectorAll(`#${containerId} .chip-opt`).forEach(b => {
      b.classList.toggle('on', b.dataset.v === String(value));
    });
  }

  function bindChips(containerId, key, after) {
    document.querySelectorAll(`#${containerId} .chip-opt`).forEach(b => {
      b.addEventListener('click', () => {
        draft[key] = key === 'estimate' ? Number(b.dataset.v) : b.dataset.v;
        markChip(containerId, b.dataset.v);
        if (after) after();
      });
    });
  }

  /* ================= 附件（照片 / 文件） =================
     附件不塞进 localStorage：原生存一份到 App 私有目录，这里只存引用 + 图片小缩略图。
     原文件删掉、清缓存都不影响；点开可以交给系统应用打开，或另存到「下载」。 */

  const MAX_ATT = 9;

  function fmtSize(n) {
    if (!n) return '未知大小';
    return n > 1024 * 1024 ? (n / 1024 / 1024).toFixed(1) + 'MB' : Math.max(1, Math.round(n / 1024)) + 'KB';
  }

  function pickAtt() {
    if ((draft.atts || []).length >= MAX_ATT) { UI_.toast('一条待办最多 ' + MAX_ATT + ' 个附件'); return; }
    UI_.actionSheet([
      { label: '📷 拍照', onTap: () => chooseAtt('camera') },
      { label: '🖼 从相册选图', onTap: () => chooseAtt('album') },
      { label: '📎 选文件（pdf / docx / zip…）', onTap: () => chooseAtt('file') },
      { label: '取消', cancel: true },
    ]);
  }

  function chooseAtt(source) {
    const nb = window.BzNative;
    if (nb && typeof nb.pickAttachmentFor === 'function') {
      try { nb.pickAttachmentFor(source); } catch (e) { UI_.toast('打不开：' + e.message); }
      return;
    }
    UI_.toast('浏览器版存不了附件（文件拷不进 App），请用手机上的待办中心');
  }

  /** 原生把附件复制进私有目录后会叫我们一声；这里只认领 dest=todo 的 */
  function takeTodoAttachment() {
    const nb = window.BzNative;
    if (!nb || typeof nb.takeAttachmentFor !== 'function') return;
    let json = '';
    try { json = nb.takeAttachmentFor('todo') || ''; } catch (e) { return; }
    if (!json) return;
    let p = null;
    try { p = JSON.parse(json); } catch (e) { return; }
    if (!p || p.dest !== 'todo') return;
    if (!draft) return;
    draft.atts = draft.atts || [];
    const rec = {
      id: 'a' + Date.now().toString(36) + Math.random().toString(36).slice(2, 5),
      kind: p.kind === 'image' ? 'image' : 'file',
      name: p.name || '附件', mime: p.mime || '', size: p.size || 0,
      path: p.path || '', thumb: p.thumb || '',
    };
    draft.atts.push(rec);
    // 连了后端知识库：附件正本直接传上 NAS，本机那份删掉（用户要求"手机上不留文件"）
    if (rec.path && Store.kbMode && Store.kbMode()) uploadAttToNas(rec);
    renderAtts();
    UI_.toast(rec.nasId ? '附件已存到 NAS：' + rec.name : '已添加附件：' + rec.name);
  }

  /** 把附件正本传到 NAS。成功就删掉本机那份；失败保留本机文件并说清原因（不静默） */
  function uploadAttToNas(a, quiet) {
    const nb = window.BzNative;
    if (!nb || typeof nb.kbUploadAttachment !== 'function') return false;
    let out = '';
    try { out = nb.kbUploadAttachment(a.path, a.name, a.kind, a.mime) || ''; } catch (e) { out = ''; }
    let r = null;
    try { r = JSON.parse(out); } catch (e) { r = null; }
    if (!r || !r.ok || !r.file || !r.file.id) {
      if (!quiet) {
        const why = (r && r.error) ? r.error : '没拿到后端的回执';
        UI_.toast('附件没能传到 NAS：' + why + '（先留在手机上，下次打开再传）', 4000);
      }
      return false;
    }
    a.nasId = r.file.id;
    if (r.file.size) a.size = r.file.size;
    if (a.path && typeof nb.deleteAttachment === 'function') {
      try { nb.deleteAttachment(a.path); } catch (e) { /* 删不掉就算了，别拦着用户 */ }
    }
    a.path = '';
    return true;
  }

  /** 一次性搬家：以前存在手机上的附件补传到 NAS，传上去就把本机那份删掉 */
  function migrateAttsToNas() {
    const nb = window.BzNative;
    if (!nb || typeof nb.kbUploadAttachment !== 'function') return;
    if (!Store.kbMode || !Store.kbMode()) return;
    let moved = 0, failed = 0;
    list.forEach((t) => {
      (t.atts || []).forEach((a) => {
        if (!a.path || a.nasId) return;
        if (uploadAttToNas(a, true)) moved++; else failed++;
      });
    });
    if (moved) {
      Store.saveTodos(list);
      UI_.toast('已把 ' + moved + ' 个附件搬到 NAS' + (failed ? '（' + failed + ' 个没搬成）' : '，手机上不再留副本'), 4000);
    } else if (failed) {
      UI_.toast(failed + ' 个附件没能搬到 NAS，稍后再试（它们还在手机上，没丢）', 4000);
    }
  }

  /** 删附件时把 NAS 上那份也删掉（本机没有正本，只删本地等于删不干净） */
  function dropAttFile(a) {
    const nb = window.BzNative;
    if (!a || !nb) return;
    if (a.nasId && typeof nb.kbDeleteAttachment === 'function') {
      let out = '';
      try { out = nb.kbDeleteAttachment(a.nasId, a.path) || ''; } catch (e) { out = ''; }
      let r = null;
      try { r = JSON.parse(out); } catch (e) { r = null; }
      if (!r || !r.ok) UI_.toast('NAS 上那份没删掉：' + ((r && r.error) || '未知原因'), 3600);
      return;
    }
    if (a.path && typeof nb.deleteAttachment === 'function') {
      try { nb.deleteAttachment(a.path); } catch (e) { /* 文件已不在就算了 */ }
    }
  }

  function renderAtts() {
    const box = $('tAtts');
    if (!box) return;
    const arr = (draft && draft.atts) || [];
    box.innerHTML = '';
    box.hidden = arr.length === 0;
    arr.forEach((a, i) => {
      const el = document.createElement('div');
      el.className = 'att-item';
      el.innerHTML = (a.kind === 'image' && a.thumb)
        ? `<img src="${a.thumb}" alt="">`
        : `<div class="att-item-file">📎<br>${esc(a.name)}</div>`;
      const del = document.createElement('div');
      del.className = 'att-del';
      del.textContent = '×';
      del.title = '移除';
      del.addEventListener('click', (e) => { e.stopPropagation(); removeAtt(i); });
      el.appendChild(del);
      el.addEventListener('click', () => openAtt(a, { name: draft.title, onDelete: () => removeAtt(i) }));
      box.appendChild(el);
    });
    const tip = $('tAttCount');
    if (tip) tip.textContent = arr.length ? `已附 ${arr.length} 个（最多 ${MAX_ATT} 个）` : '';
  }

  function removeAtt(i) {
    const arr = (draft && draft.atts) || [];
    const a = arr[i];
    if (!a) return;
    dropAttFile(a);
    arr.splice(i, 1);
    renderAtts();
  }

  function removeAttOfTodo(t, attId) {
    const a = (t.atts || []).find(x => x.id === attId);
    dropAttFile(a);
    t.atts = (t.atts || []).filter(x => x.id !== attId);
    Store.saveTodos(list);
    render();
    UI_.toast('附件已删除');
  }

  /** 打开附件：图片在 App 内全屏看，其它交给系统应用（打不开就另存到「下载」） */
  function openAtt(a, opt) {
    const o = opt || {};
    const nb = window.BzNative;

    // 正本在 NAS：先取回来落一份本机临时文件（预览 / 交给系统应用都要有本地路径），
    // 关掉预览页就把它删掉 —— 手机上始终不留正本
    closeAttTemp();
    if (!a.path && a.nasId && nb && typeof nb.kbFetchAttachment === 'function') {
      let p = '';
      try { p = nb.kbFetchAttachment(a.nasId, a.name) || ''; } catch (e) { p = ''; }
      if (!p) {
        UI_.toast('取不回这个附件：连不上 NAS，或后端上已经没有这个文件了', 4000);
        return;
      }
      a.path = p;
      attTempPath = p;
    }

    $('apTitle').textContent = (a.name || '附件') + (o.name ? ' · ' + o.name : '');
    const body = $('apBody');
    body.innerHTML = '';
    if (a.kind === 'image' && (a.thumb || a.path)) {
      const img = document.createElement('img');
      img.className = 'att-full';
      img.src = a.thumb || '';
      body.appendChild(img);
      if (a.path && nb && typeof nb.readAttachmentBase64 === 'function') {
        try {
          const b64 = nb.readAttachmentBase64(a.path);
          if (b64) img.src = 'data:image/jpeg;base64,' + b64;
        } catch (e) { /* 用缩略图兜底 */ }
      }
    } else {
      const cardEl = document.createElement('div');
      cardEl.className = 'att-file-card';
      cardEl.innerHTML = `<div class="att-file-icon">📎</div>
        <div class="att-file-name">${esc(a.name || '附件')}</div>
        <div class="att-file-sub">${fmtSize(a.size)}${a.mime ? ' · ' + esc(a.mime) : ''}${a.nasId ? ' · 存在 NAS' : ''}</div>`;
      body.appendChild(cardEl);
    }
    const bar = document.createElement('div');
    bar.className = 'att-actions';
    const mkBtn = (label, cls, fn) => {
      const b = document.createElement('button');
      b.className = 'btn ' + (cls || 'ghost') + ' sm';
      b.textContent = label;
      b.addEventListener('click', fn);
      bar.appendChild(b);
    };
    if (a.path && nb && typeof nb.openAttachment === 'function') {
      mkBtn('用系统应用打开', 'primary', () => {
        try { nb.openAttachment(a.path); } catch (e) { UI_.toast('打不开：' + e.message); }
      });
    }
    if (a.path && nb && typeof nb.saveAttachment === 'function') {
      mkBtn('另存到下载', 'ghost', () => {
        try { nb.saveAttachment(a.path); } catch (e) { UI_.toast('另存失败'); }
      });
    }
    if (o.onDelete) {
      mkBtn('删除附件', 'danger-ghost', () => {
        UI_.actionSheet([
          { label: '删除这个附件（NAS 上那份也删掉）', danger: true, onTap: () => { UI_.closePage('attPage'); o.onDelete(); } },
          { label: '取消', cancel: true },
        ]);
      });
    }
    body.appendChild(bar);
    UI_.openPage('attPage');
  }

  /** 从 NAS 取回来预览用的那份临时文件（关掉预览页就删） */
  let attTempPath = '';
  function closeAttTemp() {
    if (!attTempPath) return;
    const nb = window.BzNative;
    if (nb && typeof nb.deleteAttachment === 'function') {
      try { nb.deleteAttachment(attTempPath); } catch (e) { /* 忽略 */ }
    }
    attTempPath = '';
  }

  /* ================= 长按 ================= */
  function pressDetector(cb) {
    let timer = null;
    return {
      down() { timer = setTimeout(() => { navigator.vibrate && navigator.vibrate(15); cb(); }, 480); },
      up() { clearTimeout(timer); },
    };
  }

  /* ================= 左右滑动 ================= */
  function bindSwipe() {
    let sx = 0, sy = 0, active = false;
    const area = $('view-todo');
    area.addEventListener('pointerdown', (e) => {
      if (e.target.closest('.todo-card') || e.target.closest('input')) return;
      sx = e.clientX; sy = e.clientY; active = true;
    });
    area.addEventListener('pointerup', (e) => {
      if (!active) return; active = false;
      const dx = e.clientX - sx, dy = e.clientY - sy;
      if (Math.abs(dx) < 60 || Math.abs(dy) > 50) return;
      setView(dx < 0 ? 'leisure' : 'schedule');
    });
  }

  function setView(v) {
    viewMode = v;
    document.querySelectorAll('#viewSwitch .vs-btn').forEach(b => b.classList.toggle('active', b.dataset.view === v));
    render();
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  /* 供语音 / AI 模块调用 */
  function addByIntent(intent) {
    const form = intent.form || (intent.due ? 'schedule' : 'leisure');
    list.unshift({
      id: Store.uid(), title: intent.title,
      category: intent.category || '日常生活', priority: intent.priority || 'mid',
      form, due: form === 'leisure' ? '' : (intent.due || ''),
      weekly: !!intent.weekly, estimate: Number(intent.estimate) || 0,
      note: intent.note || '', deps: [], atts: [], owner: 'user', status: 'todo',
      remind: !!intent.due, createdAt: Date.now(),
    });
    Store.saveTodos(list); render();
  }

  function init() {
    $('quickInput').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { quickAdd($('quickInput').value); $('quickInput').value = ''; }
    });
    $('btnQuickNew').addEventListener('click', () => openForm(null));
    $('btnAiPlan').addEventListener('click', () => Plan.open());

    $('btnSort').addEventListener('click', () => {
      sortMode = sortMode === 'time' ? 'priority' : 'time';
      render();
      UI_.toast(sortMode === 'time' ? '排序：按时间' : '排序：按优先级');
    });

    // 只认待办自己的这两排（知识库那排类别分段也用了同样的 .seg-btn / .vs-btn 样式，
    // 用全局选择器会把它们一起接管，点一下类别就把「我的/Agent」给顶掉了）
    document.querySelectorAll('#ownerSeg .seg-btn').forEach(b => b.addEventListener('click', () => {
      document.querySelectorAll('#ownerSeg .seg-btn').forEach(x => x.classList.remove('active'));
      b.classList.add('active'); seg = b.dataset.seg; render();
    }));
    document.querySelectorAll('#viewSwitch .vs-btn').forEach(b => b.addEventListener('click', () => setView(b.dataset.view)));

    // 表单
    $('tpClose').addEventListener('click', () => UI_.closePage('todoPage'));
    $('tpSave').addEventListener('click', saveForm);
    bindChips('tDomain', 'category');
    bindChips('tPriority', 'priority');
    bindChips('tForm', 'form', syncFormCard);
    bindChips('tEstimate', 'estimate');
    $('tWeekly').addEventListener('click', () => {
      draft.weekly = !draft.weekly;
      $('tWeekly').classList.toggle('on', draft.weekly);
    });
    $('tRemind').addEventListener('click', () => {
      draft.remind = !draft.remind;
      $('tRemind').classList.toggle('on', draft.remind);
    });

    // 附件
    $('tAttAdd').addEventListener('click', pickAtt);
    $('apClose').addEventListener('click', () => { UI_.closePage('attPage'); closeAttTemp(); });
    UI_.onAttachmentReady(takeTodoAttachment);

    bindSwipe();
    reload();
    // 老数据里附件正本还在手机上：连上 NAS 就补传过去，传完把本机那份删掉
    setTimeout(migrateAttsToNas, 2500);
  }

  return {
    init, reload, render, quickAdd, addByIntent, getList, esc,
    setView, openForm, find, DOMAINS, PRI, ESTIMATES,
    openAtt, renderAtts, pickAtt, takeTodoAttachment, MAX_ATT,
  };
})();
