/* 白泽桌面端 · 外部 Agent（委托执行）
   把一段独立的活派给「外部 Agent」（另一个白泽实例，或任意 HTTP 端点）去干，只把结论拿回来。
   外部 Agent 一律当**不可信执行体**：只传任务文本、不外泄本机文件/记忆/密钥，回来的内容按外部输入处理。

   数据：GET /api/agent/external-agents → {agents[{id,name,type,url,note,enabled,timeoutSec,hasToken,local}], types[], allowRemote, recent[]}
   动作：POST /api/agent/external-agents {action:upsert|remove|test|call, id, goal, config{...}, allowRemote}
   注意：「最近委托记录」是进程内的，重启即清零 —— 与观测面同一口径，界面里标清。 */
'use strict';

async function renderExternal(root) {
  let data = { agents: [], types: [], allowRemote: false, recent: [] };

  root.innerHTML = `
    <div class="page">
      <h3>外部 Agent（委托执行）</h3>
      <div class="sub">
        白泽可以把一段独立的活<b>委托给别的 Agent</b>去做，只把结论拿回来（适合把长过程外包出去）。
        类型：<span class="mono">http</span> = 任意 HTTP 端点（OpenAI 兼容的 agent / 别的编排服务）；
        <span class="mono">baize</span> = 另一个白泽实例（喂它的 /api/agent/run + 配对令牌）。
        <br><b>安全口径</b>：外部 Agent 是不可信执行体 —— 只传任务文本，别在委托里放本机文件 / 密钥；
        回来的内容按外部输入处理。Agent 自主委托时用 <span class="mono">agent_call</span>，属危险操作会走人工审批。
      </div>

      <div class="sect" style="margin-top:10px">
        <h3>已配置</h3>
        <div id="exList"></div>
      </div>

      <div class="sect">
        <h3 id="exFormTitle">加一个外部 Agent</h3>
        <div class="fields" style="grid-template-columns:140px 140px 1fr 120px">
          <div><label>id（唯一，委托时用它）</label><input id="exId" placeholder="peer-a"></div>
          <div><label>显示名</label><input id="exName" placeholder="对端白泽"></div>
          <div><label>类型</label><select id="exType"></select></div>
          <div><label>超时（秒）</label><input id="exTimeout" placeholder="120"></div>
        </div>
        <div><label>地址 URL</label><input id="exUrl" placeholder="http://192.168.1.50:8787（baize）或 https://agent.example.com/run（http）"></div>
        <div class="fields" style="grid-template-columns:1fr 1fr">
          <div><label>令牌（留空 = 保留原令牌）</label><input id="exToken" type="password"></div>
          <div><label>说明（会进系统提示，帮白泽决定派给谁）</label><input id="exNote" placeholder="例如：专门查资料"></div>
        </div>
        <div style="display:flex;gap:16px;align-items:center;margin:6px 0 4px">
          <label class="sub" style="margin:0"><input type="checkbox" id="exEnabled" style="margin-right:6px" checked>启用</label>
          <label class="sub" style="margin:0"><input type="checkbox" id="exRemote" style="margin-right:6px">地址不是本机（需要放行远端）</label>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="exSave">保存</button>
          <button class="btn ghost" id="exClear">清空表单</button>
          <span class="sub" id="exMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>手动委托</h3>
        <div class="sub">这里是你（人）的手动委托，点下去直接派（人工意图，不再走审批队列）。Agent 自主委托才走审批。</div>
        <div class="fields" style="grid-template-columns:220px 1fr">
          <div><label>派给谁</label><select id="exCallTarget"></select></div>
          <div style="display:flex;align-items:flex-end"><button class="btn" id="exCallGo">委托</button></div>
        </div>
        <div><label>任务</label><textarea id="exCallGoal" placeholder="例如：帮我查一下这几个关键词的资料，给我一页带来源的摘要"></textarea></div>
        <div id="exCallMsg" class="sub" style="margin:8px 0 0"></div>
      </div>

      <div class="sect">
        <h3>最近委托记录 <span class="tag">本次启动以来</span></h3>
        <div class="sub">只留最近 50 条，<b>重启即清零</b>（完整轨迹在运行记录里；这里看的是"刚才派过什么"）。</div>
        <div id="exRecent"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const renderList = () => {
    const list = data.agents || [];
    $i('exList').innerHTML = list.length ? list.map(a => `
      <div class="row">
        <div class="who"><b>${esc(a.name || a.id)}</b>
          <span class="mono">${esc(a.id)} · ${esc(a.type)} · ${esc(a.url || '（没填地址）')}${a.timeoutSec ? ' · ' + a.timeoutSec + 's' : ''}</span>
          ${a.note ? `<span>${esc(a.note)}</span>` : ''}</div>
        <div class="tags">
          <span class="tag ${a.enabled ? 'on' : 'off'}">${a.enabled ? '启用' : '停用'}</span>
          <span class="tag ${a.local ? '' : 'warn'}">${a.local ? '本机' : '远端'}</span>
          ${a.hasToken ? '<span class="tag on">有令牌</span>' : '<span class="tag">无令牌</span>'}
          <button class="btn ghost sm" data-test="${esc(a.id)}">探活</button>
          <button class="btn ghost sm" data-edit="${esc(a.id)}">编辑</button>
          <button class="btn ghost sm danger" data-rm="${esc(a.id)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有配置外部 Agent。白泽现在派不出去活。</div>';

    $i('exList').querySelectorAll('[data-test]').forEach(b => b.onclick = async () => {
      b.disabled = true; b.textContent = '探测中…';
      const r = await API.post('/api/agent/external-agents', { action: 'test', id: b.dataset.test });
      b.disabled = false; b.textContent = '探活';
      const res = (r.data && r.data.result) || {};
      if (r.ok && !(r.data && r.data.error)) Shell.toast(`「${b.dataset.test}」通了：${(res.text || '').slice(0, 40)}（${res.latencyMs} ms）`, 'ok');
      else Shell.toast('探活失败：' + ((r.data && r.data.error) || r.error), 'err');
      await load();
    });
    $i('exList').querySelectorAll('[data-edit]').forEach(b => b.onclick = () => {
      const a = (data.agents || []).find(x => x.id === b.dataset.edit);
      if (!a) return;
      $i('exId').value = a.id; $i('exName').value = a.name || '';
      $i('exType').value = a.type || 'http'; $i('exUrl').value = a.url || '';
      $i('exNote').value = a.note || ''; $i('exTimeout').value = a.timeoutSec || 120;
      $i('exEnabled').checked = !!a.enabled; $i('exRemote').checked = !a.local;
      $i('exToken').value = '';
      $i('exFormTitle').textContent = '改：' + (a.name || a.id);
      $i('exMsg').textContent = '（令牌留空 = 不改）';
    });
    $i('exList').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
      if (!confirm('删除外部 Agent「' + b.dataset.rm + '」？')) return;
      const r = await API.post('/api/agent/external-agents', { action: 'remove', id: b.dataset.rm });
      if (!r.ok) Shell.toast('删除失败：' + r.error, 'err');
      await load();
    });
  };

  const renderRecent = () => {
    const list = data.recent || [];
    $i('exRecent').innerHTML = list.length ? `<table class="ob-table"><thead><tr>
      <th>时间</th><th>派给</th><th>任务</th><th>结果</th><th>耗时</th><th>出错</th></tr></thead><tbody>` +
      list.map(d => `<tr>
        <td class="mono">${fmtTime(d.at)}</td>
        <td class="mono">${esc(d.name || d.target)}</td>
        <td>${esc(d.goal || '')}</td>
        <td><span class="tag ${d.status === 'done' ? 'on' : 'warn'}">${esc(d.status)}</span></td>
        <td>${d.latencyMs || 0} ms</td>
        <td class="mono">${esc(d.error || '—')}</td>
      </tr>`).join('') + '</tbody></table>'
      : '<div class="empty">本次启动以来还没委托过。</div>';
  };

  const fillTargets = () => {
    const opts = (data.agents || []).filter(a => a.enabled)
      .map(a => `<option value="${esc(a.id)}">${esc(a.name || a.id)}（${esc(a.type)}）</option>`).join('');
    $i('exCallTarget').innerHTML = opts || '<option value="">（没有启用的外部 Agent）</option>';
  };

  const load = async () => {
    const r = await API.get('/api/agent/external-agents');
    if (!r.ok) { showErr($i('exList'), r); return; }
    data = r.data || data;
    const sel = $i('exType');
    if (sel.options.length === 0) {
      sel.innerHTML = (data.types || ['http', 'baize']).map(t => `<option value="${esc(t)}">${esc(t)}</option>`).join('');
    }
    renderList(); renderRecent(); fillTargets();
  };

  $i('exSave').onclick = async () => {
    const id = $i('exId').value.trim();
    if (!id) { $i('exMsg').innerHTML = '<span class="err">id 不能为空</span>'; return; }
    const cfg = {
      id: id,
      name: $i('exName').value.trim(),
      type: $i('exType').value,
      url: $i('exUrl').value.trim(),
      note: $i('exNote').value.trim(),
      enabled: $i('exEnabled').checked,
      timeoutSec: Number($i('exTimeout').value.trim() || 120),
    };
    const token = $i('exToken').value.trim();
    if (token) cfg.token = token; // 留空 = 后端保留原令牌
    $i('exMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/external-agents', { action: 'upsert', config: cfg, allowRemote: $i('exRemote').checked });
    if (r.ok) {
      $i('exMsg').innerHTML = '<span class="ok">已保存</span>';
      $i('exToken').value = ''; $i('exFormTitle').textContent = '加一个外部 Agent';
      await load();
    } else {
      $i('exMsg').innerHTML = `<span class="err">${esc(r.error || '保存失败')}</span>`;
    }
  };
  $i('exClear').onclick = () => {
    ['exId', 'exName', 'exUrl', 'exToken', 'exNote'].forEach(k => $i(k).value = '');
    $i('exTimeout').value = ''; $i('exEnabled').checked = true; $i('exRemote').checked = false;
    $i('exFormTitle').textContent = '加一个外部 Agent'; $i('exMsg').textContent = '';
  };

  $i('exCallGo').onclick = async () => {
    const id = $i('exCallTarget').value, goal = $i('exCallGoal').value.trim();
    if (!id) { $i('exCallMsg').innerHTML = '<span class="err">没有可派的外部 Agent</span>'; return; }
    if (!goal) { $i('exCallMsg').innerHTML = '<span class="err">先写一句要委托什么</span>'; return; }
    $i('exCallMsg').textContent = '委托中…';
    const r = await API.post('/api/agent/external-agents', { action: 'call', id: id, goal: goal });
    const res = (r.data && r.data.result) || {};
    if (r.ok && !(r.data && r.data.error)) {
      $i('exCallMsg').innerHTML = `<span class="ok">完成（${res.latencyMs || 0} ms${res.truncated ? '，输出已截断' : ''}）</span>` +
        `<div class="pre" style="white-space:pre-wrap;margin-top:6px">${esc(res.text || '（没有回文）')}</div>`;
    } else {
      $i('exCallMsg').innerHTML = `<span class="err">${esc((r.data && r.data.error) || r.error || '委托失败')}</span>`;
    }
    await load();
  };

  await load();
  pollWhileMounted(root, load, 8000);
}

/* ================= 回填注册表（同 apps2/apps3/apps4/apps5 末尾的说明） ================= */
(function wireExternal() {
  const app = window.APP_BY_ID && window.APP_BY_ID.external;
  if (app) app.render = renderExternal;
})();
