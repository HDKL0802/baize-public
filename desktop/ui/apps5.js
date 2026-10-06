/* 白泽桌面端 · 可观测性
   一次请求拿一整张观测面（后端 /api/agent/observability）：
   运行汇总 / 分桶、工具与通道的调用明细、日志概览、健康检查、当前活动。
   注意三块数据的时效性不同，界面上要分开说：
     - 运行汇总来自 runs.db → 跨重启；
     - 工具/通道明细是进程内计数 → 本次启动以来；
     - 健康与积压是现读现算。
   render 在文件末尾回填 window.APPS（同 apps2/apps3/apps4 的说明）。 */
'use strict';

async function renderObserve(root) {
  const WINDOWS = [['1h', '近 1 小时'], ['24h', '近 24 小时'], ['7d', '近 7 天'], ['30d', '近 30 天'], ['all', '全部']];
  let win = '24h';

  root.innerHTML = `
    <div class="page">
      <h3>可观测性</h3>
      <div class="sub">
        白泽最近干得怎么样：跑了多少活、慢在哪、哪里在报错、有什么还没配。
        <b>运行汇总</b>来自落盘的运行记录（跨重启）；<b>工具与通道的明细</b>是进程内计数
        （运行记录只存整次运行的汇总，不存"哪个工具调了几次"），所以那是<b>本次启动以来</b>的。
      </div>
      <div class="tabs" id="obWin">
        ${WINDOWS.map(([k, t]) => `<button data-win="${k}" class="${k === win ? 'on' : ''}">${t}</button>`).join('')}
      </div>
      <div id="obBody"><div class="empty">读取中…</div></div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let rep = null;

  const pct = (a, b) => b > 0 ? Math.round(a / b * 100) : 0;
  const ms = v => {
    if (!v) return '—';
    if (v < 1000) return v + ' ms';
    if (v < 60000) return (v / 1000).toFixed(1) + ' s';
    return Math.floor(v / 60000) + ' m ' + Math.round((v % 60000) / 1000) + ' s';
  };
  const num = v => (v == null ? '—' : Number(v).toLocaleString());
  const up = sec => sec < 3600 ? Math.max(1, Math.floor(sec / 60)) + ' 分钟' : (sec / 3600).toFixed(1) + ' 小时';

  const kpi = (label, value, sub) =>
    `<div class="ob-kpi"><b>${value}</b><span>${esc(label)}${sub ? ' · ' + esc(sub) : ''}</span></div>`;

  const healthRow = h => `
    <div class="row">
      <div class="who"><b><i class="dot ${h.status === 'error' ? 'err' : (h.status === 'warn' ? 'warn' : '')}"></i> ${esc(h.name)}</b>
        <span>${esc(h.detail || '')}</span>
        ${h.hint ? `<span class="mono">建议：${esc(h.hint)}</span>` : ''}</div>
      <div class="tags"><span class="tag ${h.status === 'error' ? 'warn' : (h.status === 'ok' ? 'on' : '')}">${esc(h.status)}</span></div>
    </div>`;

  const bars = buckets => {
    const list = (buckets || []).slice(-60);
    if (!list.length) return '<div class="empty">这段时间没有运行记录。</div>';
    const max = Math.max(1, ...list.map(b => b.runs));
    return `<div class="ob-bars">` + list.map(b => {
      const h = Math.max(2, Math.round(b.runs / max * 100));
      const cls = b.failed > 0 ? 'ob-bar bad' : 'ob-bar';
      const tip = `${fmtTime(b.at)} · ${b.runs} 次运行 / ${b.tokens} token`;
      return `<i class="${cls}" style="height:${h}%" title="${esc(tip)}"></i>`;
    }).join('') + `</div>
    <div class="legend"><span>共 ${(buckets || []).length} 个桶${(buckets || []).length > 60 ? '（只画最近 60 个）' : ''}</span>
      <span><i></i>正常</span><span><i class="bad"></i>有失败</span></div>`;
  };

  const toolTable = tools => {
    if (!tools || !tools.length) return '<div class="empty">本次启动以来还没调过工具。</div>';
    return `<table class="ob-table"><thead><tr>
      <th>工具</th><th>调用</th><th>失败</th><th>被拦</th><th>平均耗时</th><th>最近出错</th></tr></thead><tbody>` +
      tools.map(t => `<tr>
        <td class="mono">${esc(t.tool)}</td>
        <td>${num(t.calls)}</td>
        <td>${t.errors > 0 ? `<span class="err">${num(t.errors)}</span>` : '0'}</td>
        <td>${t.blocked > 0 ? `<span class="tag warn">${num(t.blocked)}</span>` : '0'}</td>
        <td>${ms(t.avgMs)}</td>
        <td class="mono">${esc(t.lastErr || '—')}</td>
      </tr>`).join('') + '</tbody></table>';
  };

  const provTable = provs => {
    if (!provs || !provs.length) return '<div class="empty">本次启动以来还没问过模型。</div>';
    return `<table class="ob-table"><thead><tr>
      <th>通道</th><th>请求</th><th>回复</th><th>重试</th><th>token</th><th>平均等待</th><th>最近出错</th></tr></thead><tbody>` +
      provs.map(p => `<tr>
        <td class="mono">${esc(p.provider)}</td>
        <td>${num(p.requests)}</td>
        <td>${num(p.replies)}</td>
        <td>${p.retries > 0 ? `<span class="tag warn">${num(p.retries)}</span>` : '0'}</td>
        <td>${num(p.tokens)}</td>
        <td>${ms(p.avgMs)}</td>
        <td class="mono">${esc(p.lastErr || '—')}</td>
      </tr>`).join('') + '</tbody></table>';
  };

  const draw = () => {
    if (!rep) return;
    const r = rep.runs || {};
    const logs = rep.logs || { recent: [] };
    const act = rep.activity || {};
    const q = ($i('obFilter') ? $i('obFilter').value : '').trim().toLowerCase();
    const recent = (logs.recent || []).filter(l => !q || String(l.text).toLowerCase().includes(q));

    $i('obBody').innerHTML = `
      <div class="sub" style="margin:10px 0 6px">
        当前：<b>${esc(act.active ? (act.stage + (act.detail ? '（' + act.detail + '）' : '')) : '空闲')}</b>
        · 后端已跑 <b>${up(rep.uptimeSec || 0)}</b>
        · 生成于 ${fmtTime(rep.generatedAt)}
      </div>

      <div class="ob-kpis">
        ${kpi('运行', num(r.total), (r.failed ? '失败 ' + r.failed : '全成功'))}
        ${kpi('平均耗时', ms(r.avgMs), 'P95 ' + ms(r.p95Ms))}
        ${kpi('最长一次', ms(r.maxMs))}
        ${kpi('token', num((r.promptTokens || 0) + (r.outTokens || 0)), '入 ' + num(r.promptTokens) + ' / 出 ' + num(r.outTokens))}
        ${kpi('工具调用', num(r.toolCalls), '重试 ' + num(r.retries))}
        ${kpi('日志', num(logs.total), '错误 ' + num(logs.error) + ' · 警告 ' + num(logs.warn))}
      </div>

      <div class="sect">
        <h3>健康（先看这里）</h3>
        <div class="sub">error = 配了但用不了（真问题）；warn = 没配或配置不稳妥（还能用，但建议看一眼）；ok = 本来就该这样。</div>
        ${(rep.health || []).map(healthRow).join('') || '<div class="empty">还没有可体检的通道 —— 先去「模型通道」配一条。</div>'}
      </div>

      <div class="sect">
        <h3>运行量</h3>
        <div class="sub">每个柱子是一个时间桶里的运行次数（高度），带颜色的桶表示里面有失败。窗口：${esc(rep.window)}。</div>
        ${bars(rep.buckets)}
      </div>

      <div class="sect">
        <h3>工具明细 <span class="tag">本次启动以来</span></h3>
        <div class="sub">被审批拦下的单独一列 —— 那不算"工具坏了"，别混着看。</div>
        ${toolTable(rep.tools)}
      </div>

      <div class="sect">
        <h3>模型通道明细 <span class="tag">本次启动以来</span></h3>
        <div class="sub">"平均等待"里含重试等待：它回答的是"等这一步到底花了多久"。</div>
        ${provTable(rep.providers)}
      </div>

      <div class="sect">
        <h3>日志</h3>
        <div class="sub">只列最近的告警与错误（最多 20 条）；日志环总 ${num(logs.total)} 行。</div>
        <div class="fields" style="grid-template-columns:1fr 90px">
          <div><label>过滤关键字</label><input id="obFilter" value="${esc(q)}" placeholder="例如 mcp / 超时 / 插件"></div>
          <div style="display:flex;align-items:flex-end"><button class="btn ghost" id="obFilterGo">过滤</button></div>
        </div>
        ${recent.length ? recent.map(l => `
          <div class="ob-log">
            <span class="mono">${fmtTime(l.at)}</span>
            <span class="lv ${l.level === 'ERROR' ? 'err' : 'warn'}">${esc(l.level)}</span>
            <span>${esc(l.text)}</span>
          </div>`).join('') : '<div class="empty">没有匹配的告警/错误。</div>'}
      </div>`;

    const f = $i('obFilterGo');
    if (f) f.onclick = draw;
    const inp = $i('obFilter');
    if (inp) inp.onkeydown = e => { if (e.key === 'Enter') draw(); };
  };

  const load = async () => {
    const r = await API.get('/api/agent/observability?window=' + encodeURIComponent(win));
    if (!r.ok) {
      $i('obBody').innerHTML = `<div class="empty err">读不到观测面：${esc(r.error || '')}</div>`;
      return;
    }
    rep = r.data || {};
    draw();
  };

  root.querySelectorAll('#obWin button').forEach(b => b.onclick = () => {
    win = b.dataset.win;
    root.querySelectorAll('#obWin button').forEach(x => x.classList.toggle('on', x === b));
    load();
  });

  await load();
  pollWhileMounted(root, load, 10000);
}

/* ================= 回填注册表（同 apps2/apps3/apps4 末尾的说明） ================= */
(function wireObservability() {
  const app = window.APP_BY_ID && window.APP_BY_ID.observe;
  if (app) app.render = renderObserve;
})();
