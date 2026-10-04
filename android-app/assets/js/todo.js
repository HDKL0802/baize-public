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

  const DOMAINS = ['工作开发', '创作（AI漫剧等）', '日常生活'];
  const PRI = { high: '高优先级', mid: '中优先级', low: '低优先级' };
  const ESTIMATES = [15, 30, 45, 60, 90, 120];

  function reload() { list = Store.getTodos(); render(); }
  function find(id) { return list.find(x => x.id === id); }
  function getList() { return list; }

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
      ${t.note ? `<div class="tc-note">${esc(t.note)}</div>` : ''}`;

    el.addEventListener('click', (e) => {
      if (e.target.closest('[data-check]') || e.target.closest('[data-auth]') || e.target.closest('[data-more]')) return;
      openForm(t.id);
    });
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
      weekly: false, estimate: 0, note: '', deps: [],
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
      weekly: false, estimate: 0, note: '', deps: [], remind: false,
    };
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
    document.querySelectorAll('.vs-btn').forEach(b => b.classList.toggle('active', b.dataset.view === v));
    render();
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  /* 供语音模块调用 */
  function addByIntent(intent) {
    const form = intent.due ? 'schedule' : 'leisure';
    list.unshift({
      id: Store.uid(), title: intent.title,
      category: intent.category || '日常生活', priority: intent.priority || 'mid',
      form, due: intent.due || '', weekly: false, estimate: 0,
      note: intent.note || '', deps: [], owner: 'user', status: 'todo',
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

    $('btnVoiceQuick').addEventListener('click', () => {
      UI_.switchTab('view-voice');
      setTimeout(() => { try { Voice.start(); } catch (e) {} }, 250);
    });

    document.querySelectorAll('.seg-btn').forEach(b => b.addEventListener('click', () => {
      document.querySelectorAll('.seg-btn').forEach(x => x.classList.remove('active'));
      b.classList.add('active'); seg = b.dataset.seg; render();
    }));
    document.querySelectorAll('.vs-btn').forEach(b => b.addEventListener('click', () => setView(b.dataset.view)));

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

    bindSwipe();
    reload();
  }

  return {
    init, reload, render, quickAdd, addByIntent, getList, esc,
    setView, openForm, find, DOMAINS, PRI, ESTIMATES,
  };
})();
