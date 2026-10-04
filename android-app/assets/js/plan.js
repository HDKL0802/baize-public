/* 白泽待办中心 - AI 智能排期页
   策略偏好：balance 各项兼顾 / work 专注工作 / create 专注创作 / life 专注生活
   排序依据：前置依赖（拓扑）+ 截止时间 + 优先级 + 领域权重 + 预计耗时 */
'use strict';
window.Plan = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;

  const POLICIES = {
    balance: { name: '各项兼顾', boost: {} },
    work: { name: '专注于工作', boost: { '工作开发': 45 } },
    create: { name: '专注于创作', boost: { '创作（AI漫剧等）': 45 } },
    life: { name: '专注于生活', boost: { '日常生活': 45 } },
  };

  let policy = 'balance';
  let lastPlan = [];

  function todos() { return Store.getTodos(); }

  function activeList() {
    return todos().filter(t => t.status !== 'done' && t.status !== 'cancelled');
  }

  /* ---------- 检测前置依赖环 ---------- */
  function hasCycle(items) {
    const byId = new Map(items.map(t => [t.id, t]));
    const state = new Map(); // 0 未访问 1 访问中 2 完成
    const walk = (id) => {
      const s = state.get(id);
      if (s === 1) return true;
      if (s === 2) return false;
      if (!byId.has(id)) return false;
      state.set(id, 1);
      for (const d of (byId.get(id).deps || [])) if (walk(d)) return true;
      state.set(id, 2);
      return false;
    };
    return items.some(t => walk(t.id));
  }

  /* ---------- 单任务打分与理由 ---------- */
  function scoreOf(t, now) {
    let score = 0;
    const why = [];
    if (t.due) {
      const diff = new Date(t.due).getTime() - now;
      if (diff < 0) { score += 120; why.push('已逾期'); }
      else if (diff < 4 * 3600e3) { score += 70; why.push('4 小时内截止'); }
      else if (diff < 24 * 3600e3) { score += 40; why.push('今日截止'); }
      else { score += 12; why.push('有明确截止'); }
    } else {
      why.push('闲暇待办，可灵活安排');
    }
    if (t.priority === 'high') { score += 35; why.push('高优先级'); }
    else if (t.priority === 'mid') score += 18;

    const boost = POLICIES[policy].boost[t.category] || 0;
    if (boost) { score += boost; why.push(`${POLICIES[policy].name}加权`); }

    const nDep = (t.deps || []).length;
    if (nDep) { score += 15 + nDep * 5; why.push(`${nDep} 项前置需先完成`); }

    if (t.weekly) { score += 8; why.push('每周固定提醒'); }
    return { score, why: why.join(' · ') };
  }

  /* ---------- 生成排期：先满足依赖，再按策略打分 ---------- */
  function computePlan() {
    const items = activeList();
    const cyclic = hasCycle(items);
    const byId = new Map(items.map(t => [t.id, t]));
    const now = Date.now();

    const scored = new Map(items.map(t => {
      const s = scoreOf(t, now);
      return [t.id, s];
    }));

    // 依赖深度：作为第一优先级（依赖未完成 → 排后面）
    const depth = new Map();
    const depthOf = (id, seen) => {
      if (depth.has(id)) return depth.get(id);
      if (!byId.has(id) || seen.has(id)) return 0;
      seen.add(id);
      const deps = (byId.get(id).deps || []).filter(d => byId.has(d));
      const d = deps.length ? 1 + Math.max(...deps.map(x => depthOf(x, seen))) : 0;
      depth.set(id, d);
      return d;
    };
    items.forEach(t => depthOf(t.id, new Set()));

    return items
      .map(t => ({
        t,
        depth: depth.get(t.id) || 0,
        score: scored.get(t.id).score,
        why: scored.get(t.id).why,
      }))
      .sort((a, b) => a.depth - b.depth || b.score - a.score)
      .map((x, i, arr) => {
        const rank = i + 1;
        let reason = x.why;
        if (x.depth > 0) reason = `第 ${x.depth} 层（需先完成前置） · ` + reason;
        if (cyclic) reason += ' · ⚠ 检测到依赖闭环';
        return { t: x.t, score: x.score, rank, why: reason };
      });
  }

  /* ---------- 渲染 ---------- */
  function refreshHeader() {
    const s = Store.getSettings().model;
    const providerName = { deepseek: 'DeepSeek 官方 API', openai: 'OpenAI API', anthropic: 'Anthropic API', custom: '自定义 API' }[s.provider] || 'AI 接口';
    $('ppApiName').textContent = providerName;
    const all = activeList();
    const leisure = all.filter(t => t.form === 'leisure').length;
    $('ppApiSub').textContent = `${s.model || '未配置模型'} · 待排期 ${all.length} 项（含闲暇 ${leisure} 项）`;
  }

  function refreshPolicy() {
    document.querySelectorAll('#policyList .policy-item').forEach(b => {
      b.classList.toggle('on', b.dataset.v === policy);
    });
  }

  function renderResult() {
    if (!lastPlan.length) { $('ppResult').hidden = true; return; }
    const box = $('ppList');
    box.innerHTML = '';
    lastPlan.forEach((it, i) => {
      const el = document.createElement('div');
      el.className = 'plan-item' + (i === 0 ? ' first' : '');
      el.innerHTML = `
        <div class="plan-rank">${it.rank}</div>
        <div class="plan-main">
          <div class="plan-title">${Todo.esc(it.t.title)}</div>
          <div class="plan-why">${Todo.esc(it.why)}${it.t.due ? ' · ' + UI_.dueLabel(it.t.due) : ''}</div>
        </div>`;
      box.appendChild(el);
    });
    $('ppResult').hidden = false;
  }

  function open() {
    policy = Store.getSettings().planPolicy || 'balance';
    lastPlan = [];
    refreshHeader();
    refreshPolicy();
    renderResult();
    UI_.openPage('planPage');
  }

  function start() {
    const items = activeList();
    if (!items.length) { UI_.toast('没有待排期的任务'); return; }
    lastPlan = computePlan();
    renderResult();
    const s = Store.getSettings();
    s.lastPlanAt = Date.now();
    Store.saveSettings(s);
    UI_.toast(`已按「${POLICIES[policy].name}」生成 ${lastPlan.length} 项排期`);
  }

  function init() {
    $('ppBack').addEventListener('click', () => UI_.closePage('planPage'));
    $('ppCfg').addEventListener('click', () => UI_.openModal('apiModal'));
    $('ppSwitch').addEventListener('click', () => UI_.openModal('apiModal'));
    $('ppStart').addEventListener('click', start);

    document.querySelectorAll('#policyList .policy-item').forEach(b => {
      b.addEventListener('click', () => {
        policy = b.dataset.v;
        const s = Store.getSettings(); s.planPolicy = policy; Store.saveSettings(s);
        refreshPolicy();
        if (lastPlan.length) { lastPlan = computePlan(); renderResult(); }
      });
    });
  }

  return { init, open, start, computePlan, refreshHeader, POLICIES };
})();
