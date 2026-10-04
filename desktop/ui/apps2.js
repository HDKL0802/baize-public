/* 白泽桌面端 · D2：控制台其余 9 个应用
   任务与审批 / 知识库 / 记忆 / 模型通道 / MCP 服务 / 技能 / 定时任务 / 备份与恢复 / 活动追踪

   约定：
   - 每个 render(root) 自己画进窗口 body；数据一律走 /api/be/*（Go 侧代理，令牌不进网页）。
   - 字段名全部按后端真实响应来（不猜）：见各函数里的注释。
   - 后端确实没有的能力，就如实标注「只读 / 需后端接口」，不假装有。
   'use strict'; */
'use strict';

/* ---------------- 公共小工具 ---------------- */

function bytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}

function tile(label, value, cls) {
  return `<div class="tag ${cls || ''}" style="display:inline-block;padding:4px 10px;margin:0 6px 6px 0">
    <span style="color:var(--text-3)">${esc(label)}</span> <b style="color:var(--text)">${esc(value)}</b></div>`;
}

function stateTag(s) {
  const map = {
    done: 'on', ok: 'on', todo: '', open: '', running: 'warn', dispatched: 'warn',
    pending_approval: 'warn', failed: 'off', rejected: 'off', canceled: 'off', blocked: 'warn',
  };
  return map[s] !== undefined ? map[s] : '';
}

/* 窗口关掉就停止轮询（shell 没有关闭回调，靠 DOM 断链判断） */
function pollWhileMounted(root, fn, ms) {
  const tick = async () => {
    if (!document.body.contains(root)) { clearInterval(timer); return; }
    try { await fn(); } catch (e) { /* 单次失败不打断轮询 */ }
  };
  fn();
  const timer = setInterval(tick, ms);
  return timer;
}

/* 把「后端错误」统一显示成一行提示 */
function showErr(node, r) {
  node.innerHTML = `<div class="empty err">失败：${esc((r && r.error) || '未知错误')}</div>`;
}

/* ================= 1. 任务与审批 ================= */
/* 数据：GET /api/state → tasks[{id,deviceId,action,args,status,needApproval,origin,createdAt,
   dispatchedAt,finishedAt,result}] / devices[] / actions[]（动作白名单）
   动作：POST /api/tasks {deviceId,action,args,origin}；POST /api/tasks/{id}/approve|reject */
async function renderTasks(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" id="tkPendingWrap" hidden style="margin-top:0">
        <h3>待审批 <span class="tag warn" id="tkPendingN">0</span></h3>
        <div class="sub">危险动作（删除 / 执行命令等）必须人工放行，才会下发给设备。</div>
        <div id="tkPending"></div>
      </div>

      <div class="sect" id="tkSendWrap">
        <h3>派活给设备</h3>
        <div class="sub">动作取自后端白名单；参数按动作要求填 JSON（可空）。</div>
        <div class="fields" style="grid-template-columns:200px 170px 1fr">
          <div><label>目标设备</label><select id="tkDev"></select></div>
          <div><label>动作</label><select id="tkAct"></select></div>
          <div><label>参数（JSON）</label><input id="tkArgs" class="mono" placeholder='{"paths":["D:/a.txt"]}'></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="tkSend">派发</button>
          <span class="sub" id="tkSendMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>最近任务</h3>
        <div class="sub">新 → 旧（最多 40 条）。</div>
        <div id="tkList"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let timer = 0;

  const act = async (path, body, msgNode) => {
    const r = body === undefined ? await API.post(path, {}) : await API.post(path, body);
    if (msgNode) msgNode.textContent = r.ok ? '已下发' : ('失败：' + r.error);
    await refresh();
  };

  const refresh = async () => {
    const r = await API.get('/api/state');
    if (!r.ok) { showErr($i('tkList'), r); return; }
    const d = r.data || {};
    const tasks = d.tasks || [], devs = d.devices || [], actions = d.actions || [];

    const sel = $i('tkDev'), cur = sel.value;
    sel.innerHTML = devs.filter(x => x.online).map(x =>
      `<option value="${esc(x.id)}">${esc(x.name || x.id)}（${esc(x.os || '')}）</option>`).join('')
      || '<option value="">（没有在线设备）</option>';
    if (cur) sel.value = cur;

    const as = $i('tkAct');
    if (!as.dataset.filled) {
      as.innerHTML = actions.map(a => `<option value="${esc(a)}">${esc(a)}</option>`).join('');
      as.dataset.filled = '1';
    }

    const pending = tasks.filter(t => t.status === 'pending_approval');
    $i('tkPendingWrap').hidden = pending.length === 0;
    $i('tkPendingN').textContent = String(pending.length);
    $i('tkPending').innerHTML = pending.map(t => `
      <div class="row">
        <div class="who"><b>${esc(t.action)}</b>
          <span class="mono">${esc(t.deviceId)} · ${esc(JSON.stringify(t.args || {}))}</span></div>
        <div class="tags">
          <button class="btn sm" data-ok="${esc(t.id)}">批准</button>
          <button class="btn sm ghost" data-no="${esc(t.id)}">驳回</button>
        </div>
      </div>`).join('');
    $i('tkPending').querySelectorAll('[data-ok]').forEach(b => b.onclick = () => act('/api/tasks/' + b.dataset.ok + '/approve'));
    $i('tkPending').querySelectorAll('[data-no]').forEach(b => b.onclick = () => act('/api/tasks/' + b.dataset.no + '/reject', { reason: '控制台驳回' }));

    const recent = tasks.slice().sort((a, b) => (b.createdAt || 0) - (a.createdAt || 0)).slice(0, 40);
    $i('tkList').innerHTML = recent.length ? recent.map(t => {
      const res = t.result == null ? '' : (typeof t.result === 'string' ? t.result : JSON.stringify(t.result));
      return `<div class="row">
        <div class="who"><b>${esc(t.action)} <span class="tag ${stateTag(t.status)}">${esc(t.status)}</span></b>
          <span class="mono">${esc(t.deviceId)} · ${esc(t.origin || '')} · ${fmtTime(t.createdAt)}${t.finishedAt ? ' → ' + fmtTime(t.finishedAt) : ''}</span>
          ${res ? `<span class="mono" style="color:var(--text-3)">${esc(res.slice(0, 180))}</span>` : ''}
        </div>
      </div>`;
    }).join('') : '<div class="empty">还没有任务</div>';
  };

  $i('tkSend').onclick = async () => {
    const dev = $i('tkDev').value, action = $i('tkAct').value;
    if (!dev) { $i('tkSendMsg').textContent = '没有在线设备'; return; }
    let args;
    const raw = ($i('tkArgs').value || '').trim();
    if (raw) { try { args = JSON.parse(raw); } catch (e) { $i('tkSendMsg').textContent = '参数不是合法 JSON'; return; } }
    const r = await API.post('/api/tasks', { deviceId: dev, action: action, args: args, origin: 'user' });
    $i('tkSendMsg').textContent = r.ok ? '已派发' : ('失败：' + r.error);
    await refresh();
  };

  await refresh();
  timer = pollWhileMounted(root, refresh, 5000);
  return () => clearInterval(timer);
}

/* ================= 2. 知识库 ================= */
/* 数据：GET /api/kb/stats {todos,user,agent,done,overdue,passwords,vaultLocked,files,fileBytes}
        GET /api/kb/state → {todos[], vault[], vaultLocked, hasMasterPwd, settings, ...}
            todo 字段：id,title,category,priority,form,due,weekly,estimate,deps[],atts[],note,owner,status,remind,createdAt
        GET /api/kb/files → {files[{id,name,kind,mime,size,at,refs}], dir}
   操作：POST /api/kb/op {op,args}（todo.toggleDone / todo.remove 的参数是 {id}）
        POST /api/kb/files {name,kind,mime,dataBase64}；GET /api/kb/files/{id} 回 base64；DELETE /api/kb/files/{id}
        POST /api/kb/import {todos,vault} */
async function renderKB(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>概览</h3>
        <div id="kbTiles" class="sub" style="margin-bottom:6px">读取中…</div>
      </div>

      <div class="sect">
        <h3>待办 <span class="sub" id="kbTodoN" style="margin:0"></span></h3>
        <div class="sub">正本在 NAS；手机上只是缓存。勾选 = 完成，✕ = 删除。</div>
        <div id="kbTodos"></div>
      </div>

      <div class="sect">
        <h3>附件 <span class="sub" id="kbFileN" style="margin:0"></span></h3>
        <div class="sub">手机上不留文件，附件统一存在后端。</div>
        <div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
          <input type="file" id="kbFile" style="font-size:12px">
          <button class="btn sm" id="kbUp">上传</button>
          <span class="sub" id="kbUpMsg" style="margin:0"></span>
        </div>
        <div id="kbFiles"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const op = async (name, args) => {
    const r = await API.post('/api/kb/op', { op: name, args: args || {} });
    if (!r.ok) Shell.toast('操作失败：' + r.error, 'err');
    await refresh();
  };

  const refresh = async () => {
    const st = await API.get('/api/kb/stats');
    if (st.ok) {
      const d = st.data || {};
      $i('kbTiles').innerHTML =
        tile('待办', d.todos) + tile('我派的', d.user) + tile('白泽派的', d.agent) +
        tile('已完成', d.done) + tile('逾期', d.overdue, d.overdue ? 'warn' : '') +
        tile('密码', d.passwords) + tile('附件', d.files + ' 个 / ' + bytes(d.fileBytes)) +
        tile('密码本', d.hasMasterPwd ? (d.vaultLocked ? '已加密·锁着' : '已加密·开着') : '未设主密码');
    } else {
      $i('kbTiles').innerHTML = `<span class="err">${esc(st.error)}</span>`;
    }

    const sn = await API.get('/api/kb/state');
    const todos = (sn.ok && sn.data && sn.data.todos) || [];
    $i('kbTodoN').textContent = todos.length ? `（${todos.length}）` : '';
    $i('kbTodos').innerHTML = todos.length ? todos.map(t => `
      <div class="row" data-id="${esc(t.id)}">
        <input type="checkbox" data-done ${t.status === 'done' ? 'checked' : ''} style="margin:0">
        <div class="who"><b style="${t.status === 'done' ? 'text-decoration:line-through;color:var(--text-3)' : ''}">${esc(t.title || t.id)}</b>
          <span class="mono">${esc(t.category || '')}${t.due ? ' · 截止 ' + esc(t.due) : ''} · ${esc(t.owner === 'agent' ? '白泽派的' : '我的')} · ${esc(t.status || '')}</span></div>
        <div class="tags">
          ${t.priority ? `<span class="tag">${esc(t.priority)}</span>` : ''}
          <button class="btn sm ghost" data-del title="删除">✕</button>
        </div>
      </div>`).join('') : '<div class="empty">知识库里还没有待办</div>';

    $i('kbTodos').querySelectorAll('.row').forEach(row => {
      const id = row.dataset.id;
      const cb = row.querySelector('[data-done]');
      if (cb) cb.onchange = () => op('todo.toggleDone', { id: id });
      const del = row.querySelector('[data-del]');
      if (del) del.onclick = () => { if (confirm('删除这条待办？')) op('todo.remove', { id: id }); };
    });

    const fl = await API.get('/api/kb/files?limit=200');
    const files = (fl.ok && fl.data && fl.data.files) || [];
    $i('kbFileN').textContent = files.length ? `（${files.length}）` : '';
    $i('kbFiles').innerHTML = files.length ? files.map(f => `
      <div class="row">
        <div class="who"><b>${esc(f.name)}</b>
          <span class="mono">${esc(f.mime || '')} · ${bytes(f.size)} · ${fmtTime(f.at)}${f.refs ? ' · 被引 ' + f.refs + ' 次' : ''}</span></div>
        <div class="tags">
          <button class="btn sm ghost" data-get="${esc(f.id)}">下载</button>
          <button class="btn sm ghost" data-rm="${esc(f.id)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有附件</div>';

    $i('kbFiles').querySelectorAll('[data-get]').forEach(b => b.onclick = async () => {
      const r = await API.get('/api/kb/files/' + b.dataset.get);
      if (!r.ok) { Shell.toast('下载失败：' + r.error, 'err'); return; }
      const bin = atob(r.data.dataBase64 || '');
      const arr = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
      const a = document.createElement('a');
      a.href = URL.createObjectURL(new Blob([arr], { type: (r.data.file && r.data.file.mime) || 'application/octet-stream' }));
      a.download = (r.data.file && r.data.file.name) || 'download';
      a.click();
      URL.revokeObjectURL(a.href);
    });
    $i('kbFiles').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
      if (!confirm('删除这个附件？')) return;
      const r = await API.call('/api/kb/files/' + b.dataset.rm, 'DELETE');
      if (!r.ok) Shell.toast('删除失败：' + r.error, 'err');
      await refresh();
    });
  };

  $i('kbUp').onclick = async () => {
    const f = $i('kbFile').files && $i('kbFile').files[0];
    if (!f) { $i('kbUpMsg').textContent = '先选一个文件'; return; }
    if (f.size > 32 * 1024 * 1024) { $i('kbUpMsg').textContent = '太大了（上限约 32MB）'; return; }
    $i('kbUpMsg').textContent = '上传中…';
    const b64 = await new Promise((res, rej) => {
      const fr = new FileReader();
      fr.onload = () => res(String(fr.result).split(',')[1] || '');
      fr.onerror = () => rej(new Error('读文件失败'));
      fr.readAsDataURL(f);
    }).catch(() => '');
    if (!b64) { $i('kbUpMsg').textContent = '读文件失败'; return; }
    const r = await API.post('/api/kb/files', { name: f.name, kind: 'file', mime: f.type || 'application/octet-stream', dataBase64: b64 });
    $i('kbUpMsg').textContent = r.ok ? '已上传' : ('失败：' + r.error);
    if (r.ok) { $i('kbFile').value = ''; await refresh(); }
  };

  await refresh();
}

/* ================= 3. 记忆 ================= */
/* 数据：GET /api/agent/memory?q&limit&profile&minScore&days
        → {query,hits[{chunk,score,why,parts{graph,vector,keyword,freshness}}],profile,weights,
           vectorUsed,vectorNote,candidates,filtered,minVector}
        chunk 字段：id,namespace,category,docKey,source,sourceRef,kind,title,content,tokens,
           importance,recency,richness,score,hits,nodeId,createdAt,hasVector,embedModel
        GET /api/agent/memory/tree → {nodes[{id,level,parentId,title,summary,tokens,week,day,chunkCount,createdAt,updatedAt}],staleDays,staleCount}
        GET /api/agent/state → memory{chunks,nodes,tokens,days,embedded,missingEmbedding}
   动作：POST /api/agent/memory/tree/rebuild、/compact、/write {content,title,kind,importance} */
async function renderMemory(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>记忆库</h3>
        <div id="mmTiles" class="sub" style="margin-bottom:6px">读取中…</div>
        <div class="fields" style="grid-template-columns:1fr 150px 90px 110px">
          <div><label>搜什么</label><input id="mmQ" placeholder="留空 = 看最近入库的"></div>
          <div><label>权重档</label><select id="mmProfile">
            <option value="">默认</option><option value="balanced">balanced</option>
            <option value="semantic">semantic</option><option value="lexical">lexical</option>
            <option value="graph_first">graph_first</option></select></div>
          <div><label>条数</label><input id="mmLimit" value="8"></div>
          <div><label>天数窗</label><input id="mmDays" placeholder="不限"></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="mmGo">检索</button>
          <button class="btn ghost" id="mmTree">看记忆树</button>
          <button class="btn ghost" id="mmRebuild">重建摘要</button>
          <button class="btn ghost" id="mmCompact">整理重复</button>
          <span class="sub" id="mmMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>手动写一条记忆</h3>
        <div class="fields" style="grid-template-columns:1fr 200px">
          <div><label>内容</label><input id="mmWContent" placeholder="要白泽记住的事"></div>
          <div><label>标题（可空）</label><input id="mmWTitle"></div>
        </div>
        <button class="btn sm" id="mmWrite">写入</button>
      </div>

      <div class="sect">
        <h3>结果</h3>
        <div id="mmOut"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const loadTiles = async () => {
    const r = await API.get('/api/agent/state');
    const m = (r.ok && r.data && r.data.memory) || {};
    $i('mmTiles').innerHTML =
      tile('片段', m.chunks) + tile('树节点', m.nodes) + tile('token', m.tokens) +
      tile('覆盖天数', m.days) + tile('已有向量', m.embedded) +
      (m.missingEmbedding ? tile('缺向量', m.missingEmbedding, 'warn') : '');
  };

  const renderHits = (d) => {
    const hits = (d && d.hits) || [];
    const head = `<div class="sub">档位 <b>${esc(d.profile || '-')}</b> · 候选 ${esc(d.candidates || 0)} → 留下 ${esc(hits.length)}` +
      (d.vectorUsed ? ' · 语义通道已用' : ' · 未用语义通道') +
      (d.vectorNote ? `（${esc(d.vectorNote)}）` : '') + '</div>';
    if (!hits.length) { $i('mmOut').innerHTML = head + '<div class="empty">没有命中</div>'; return; }
    $i('mmOut').innerHTML = head + hits.map(h => {
      const c = h.chunk || {}, p = h.parts || {};
      return `<div class="row" style="align-items:flex-start">
        <div class="who" style="flex:1">
          <b>${esc(c.title || c.docKey || ('#' + c.id))}</b>
          <span class="mono">${esc(c.source || '')}/${esc(c.kind || '')} · ${esc(c.namespace || '')} · ${fmtTime(c.createdAt)} · 命中 ${c.hits || 0} 次</span>
          <div class="pre" style="margin-top:6px;max-height:180px;overflow:auto">${esc((c.content || '').slice(0, 700))}</div>
        </div>
        <div class="tags">
          <span class="tag on">分 ${(Number(h.score) || 0).toFixed(3)}</span>
          <span class="tag">${esc(h.why || '')}</span>
          <span class="tag">图 ${(p.graph || 0).toFixed(2)}</span>
          <span class="tag">语义 ${(p.vector || 0).toFixed(2)}</span>
          <span class="tag">词 ${(p.keyword || 0).toFixed(2)}</span>
          <span class="tag">新 ${(p.freshness || 0).toFixed(2)}</span>
        </div>
      </div>`;
    }).join('');
  };

  const search = async () => {
    const q = $i('mmQ').value.trim();
    const limit = $i('mmLimit').value.trim() || '8';
    const days = $i('mmDays').value.trim();
    let path = `/api/agent/memory?q=${encodeURIComponent(q)}&limit=${encodeURIComponent(limit)}`;
    const prof = $i('mmProfile').value;
    if (prof) path += '&profile=' + encodeURIComponent(prof);
    if (days) path += '&days=' + encodeURIComponent(days);
    const r = await API.get(path);
    if (!r.ok) { showErr($i('mmOut'), r); return; }
    renderHits(r.data);
  };

  $i('mmGo').onclick = search;
  $i('mmTree').onclick = async () => {
    const r = await API.get('/api/agent/memory/tree?limit=60');
    if (!r.ok) { showErr($i('mmOut'), r); return; }
    const d = r.data || {}, nodes = d.nodes || [];
    $i('mmOut').innerHTML = `<div class="sub">记忆树 ${nodes.length} 个节点${d.staleCount ? ` · <span class="err">${d.staleCount} 天摘要过期</span>` : ''}</div>` +
      (nodes.length ? nodes.map(n => `<div class="row" style="align-items:flex-start">
        <div class="who" style="flex:1"><b>${esc(n.title || ('节点#' + n.id))}</b>
          <span class="mono">层级 ${esc(n.level)} · ${esc(n.week || n.day || '')} · ${esc(n.tokens || 0)} token · 更新 ${fmtTime(n.updatedAt)}</span>
          <div class="pre" style="margin-top:6px;max-height:150px;overflow:hidden">${esc((n.summary || '').slice(0, 420))}</div>
        </div></div>`).join('') : '<div class="empty">还没有记忆树节点（点「重建摘要」试试）</div>');
  };
  $i('mmRebuild').onclick = async () => {
    $i('mmMsg').textContent = '重建中（要调模型，可能要一会儿）…';
    const r = await API.post('/api/agent/memory/tree/rebuild', {});
    $i('mmMsg').textContent = r.ok ? `已重建 ${r.data.days || 0} 天` : ('失败：' + r.error);
    await loadTiles();
  };
  $i('mmCompact').onclick = async () => {
    const r = await API.post('/api/agent/memory/compact', {});
    if (!r.ok) { $i('mmMsg').textContent = '失败：' + r.error; return; }
    $i('mmMsg').textContent = `合并 ${r.data.merged || 0} 条（涉及 ${r.data.groups || 0} 组）`;
    await loadTiles();
  };
  $i('mmWrite').onclick = async () => {
    const content = $i('mmWContent').value.trim();
    if (!content) { $i('mmMsg').textContent = '内容不能为空'; return; }
    const r = await API.post('/api/agent/memory/write', { content: content, title: $i('mmWTitle').value.trim(), kind: 'note' });
    $i('mmMsg').textContent = r.ok ? '已写入' : ('失败：' + r.error);
    if (r.ok) { $i('mmWContent').value = ''; $i('mmWTitle').value = ''; await loadTiles(); }
  };

  await loadTiles();
  await search();
}

/* ================= 4. 模型通道 ================= */
/* 数据：GET /api/agent/providers → {allowRemote, providers[{name,protocol,baseUrl,model,fallback,hasApiKey,usable,local}]}
        GET /api/agent/config → {embedding{baseUrl,model,dim,hasApiKey}, ...}
   动作：POST /api/agent/providers {action:upsert|remove|test, name, config{name,protocol,baseUrl,model,apiKey}, allowRemote}
        POST /api/agent/embedding/test、/api/agent/embedding/reindex */
async function renderProviders(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>对话模型通道</h3>
        <div class="sub">密钥只存在后端，这里只显示「配没配」。</div>
        <div id="pvList"></div>
      </div>

      <div class="sect">
        <h3>加 / 改一条</h3>
        <div class="fields" style="grid-template-columns:130px 120px 1fr 150px">
          <div><label>名字</label><input id="pvName" placeholder="deepseek"></div>
          <div><label>协议</label><select id="pvProto"><option value="openai">openai</option><option value="anthropic">anthropic</option></select></div>
          <div><label>Base URL</label><input id="pvBase" placeholder="https://api.deepseek.com/v1"></div>
          <div><label>模型</label><input id="pvModel" placeholder="deepseek-chat"></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr 150px">
          <div><label>API Key（留空 = 保留原 key）</label><input id="pvKey" type="password"></div>
          <div><label>&nbsp;</label>
            <label class="sub" style="margin:0"><input type="checkbox" id="pvRemote" style="margin-right:6px">允许远程</label></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="pvSave">保存</button>
          <span class="sub" id="pvMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>向量化通道（记忆语义检索用）</h3>
        <div id="pvEmb" class="pre">读取中…</div>
        <div style="display:flex;gap:10px;align-items:center;margin-top:8px">
          <button class="btn sm ghost" id="pvEmbTest">探活</button>
          <button class="btn sm ghost" id="pvEmbReindex">给旧记忆补向量</button>
          <span class="sub" id="pvEmbMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const refresh = async () => {
    const r = await API.get('/api/agent/providers');
    if (!r.ok) { showErr($i('pvList'), r); return; }
    const ps = (r.data && r.data.providers) || [];
    $i('pvList').innerHTML = ps.length ? ps.map(p => `
      <div class="row">
        <div class="who"><b>${esc(p.name)}</b>
          <span class="mono">${esc(p.protocol)} · ${esc(p.model || '')} · ${esc(p.baseUrl || '')}</span></div>
        <div class="tags">
          ${p.hasApiKey ? '<span class="tag on">有 key</span>' : '<span class="tag warn">无 key</span>'}
          ${p.usable ? '<span class="tag on">可用</span>' : '<span class="tag off">不可用</span>'}
          ${p.local ? '<span class="tag">本地</span>' : ''}
          ${p.fallback ? '<span class="tag">兜底</span>' : ''}
          <button class="btn sm ghost" data-test="${esc(p.name)}">探活</button>
          <button class="btn sm ghost" data-rm="${esc(p.name)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有配置模型通道</div>';

    $i('pvList').querySelectorAll('[data-test]').forEach(b => b.onclick = async () => {
      b.disabled = true; b.textContent = '探测中…';
      const rr = await API.post('/api/agent/providers', { action: 'test', name: b.dataset.test });
      b.disabled = false; b.textContent = '探活';
      if (rr.ok && !rr.data.error) Shell.toast(`「${b.dataset.test}」可用`, 'ok');
      else Shell.toast('探活失败：' + ((rr.data && rr.data.error) || rr.error), 'err');
    });
    $i('pvList').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
      if (!confirm('删除通道「' + b.dataset.rm + '」？')) return;
      const rr = await API.post('/api/agent/providers', { action: 'remove', name: b.dataset.rm });
      if (!rr.ok) Shell.toast('删除失败：' + rr.error, 'err');
      await refresh();
    });

    const cf = await API.get('/api/agent/config');
    if (cf.ok) {
      const e = (cf.data && cf.data.embedding) || {};
      $i('pvEmb').textContent =
        'Base URL：' + (e.baseUrl || '（未配）') + '\n模型：' + (e.model || '（未配）') +
        '\n维度：' + (e.dim || 0) + '\n密钥：' + (e.hasApiKey ? '已配' : '未配');
    }
  };

  $i('pvSave').onclick = async () => {
    const name = $i('pvName').value.trim();
    if (!name) { $i('pvMsg').textContent = '名字不能为空'; return; }
    const key = $i('pvKey').value.trim();
    const cfg = {
      name: name, protocol: $i('pvProto').value,
      baseUrl: $i('pvBase').value.trim(), model: $i('pvModel').value.trim(),
    };
    if (key) cfg.apiKey = key;
    $i('pvMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/providers', { action: 'upsert', config: cfg, allowRemote: $i('pvRemote').checked });
    $i('pvMsg').textContent = r.ok ? '已保存' : ('失败：' + r.error);
    if (r.ok) { $i('pvKey').value = ''; await refresh(); }
  };
  $i('pvEmbTest').onclick = async () => {
    $i('pvEmbMsg').textContent = '探测中…';
    const r = await API.post('/api/agent/embedding/test', {});
    $i('pvEmbMsg').textContent = (r.ok && r.data.ok) ? ('可用，维度 ' + r.data.dim) : ('失败：' + ((r.data && r.data.error) || r.error));
  };
  $i('pvEmbReindex').onclick = async () => {
    $i('pvEmbMsg').textContent = '补向量中（可能要一会儿）…';
    const r = await API.post('/api/agent/embedding/reindex', {});
    $i('pvEmbMsg').textContent = (r.ok && r.data.ok) ? `完成 ${r.data.done} 条，剩 ${r.data.remaining}` : ('失败：' + ((r.data && r.data.error) || r.error));
  };

  await refresh();
}

/* ================= 5. MCP 服务 ================= */
/* 数据：GET /api/agent/mcp → {servers[], hint}
        ServerConfig 字段：name,transport(stdio|http),command,args[],env[],dir,url,headers{},enabled,autoApprove,safeTools[],prefix,timeoutSec
   动作：POST /api/agent/mcp {action:upsert|remove|reload|call, server, config, tool, args} */
async function renderMCP(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>MCP 服务</h3>
        <div class="sub">MCP 工具默认走人工审批；只有你显式信任（或列进 safeTools）的才免批。</div>
        <div id="mcHint" class="sub"></div>
        <div id="mcList"></div>
      </div>

      <div class="sect">
        <h3>加 / 改一条</h3>
        <div class="fields" style="grid-template-columns:130px 110px 1fr">
          <div><label>名字</label><input id="mcName" placeholder="filesystem"></div>
          <div><label>传输</label><select id="mcTr"><option value="stdio">stdio</option><option value="http">http</option></select></div>
          <div><label>命令（stdio）</label><input id="mcCmd" placeholder="npx -y @modelcontextprotocol/server-filesystem /data"></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr 140px">
          <div><label>URL（http）</label><input id="mcURL" placeholder="https://.../mcp"></div>
          <div><label>&nbsp;</label>
            <label class="sub" style="margin:0"><input type="checkbox" id="mcOn" checked style="margin-right:6px">启用</label></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="mcSave">保存</button>
          <span class="sub" id="mcMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>手动调一个工具</h3>
        <div class="fields" style="grid-template-columns:170px 200px 1fr">
          <div><label>服务</label><input id="mcCallServer" placeholder="filesystem"></div>
          <div><label>工具名</label><input id="mcCallTool" placeholder="list_directory"></div>
          <div><label>参数（JSON）</label><input id="mcCallArgs" class="mono" placeholder='{"path":"/data"}'></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn sm ghost" id="mcCall">调用</button>
          <span class="sub" id="mcCallMsg" style="margin:0"></span>
        </div>
        <div id="mcCallOut"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const refresh = async () => {
    const r = await API.get('/api/agent/mcp');
    if (!r.ok) { showErr($i('mcList'), r); return; }
    const d = r.data || {}, servers = d.servers || [];
    $i('mcHint').textContent = d.hint || '';
    $i('mcList').innerHTML = servers.length ? servers.map(s => `
      <div class="row">
        <div class="who"><b>${esc(s.name)}</b>
          <span class="mono">${esc(s.transport || 'stdio')} · ${esc(s.command || s.url || '')}</span></div>
        <div class="tags">
          <span class="tag ${s.enabled ? 'on' : 'off'}">${s.enabled ? '启用' : '停用'}</span>
          ${s.autoApprove ? '<span class="tag warn">免审批</span>' : '<span class="tag">需审批</span>'}
          <button class="btn sm ghost" data-reload="${esc(s.name)}">重连</button>
          <button class="btn sm ghost" data-rm="${esc(s.name)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有接 MCP 服务</div>';

    $i('mcList').querySelectorAll('[data-reload]').forEach(b => b.onclick = async () => {
      const rr = await API.post('/api/agent/mcp', { action: 'reload', server: b.dataset.reload });
      Shell.toast(rr.ok ? '已重连' : ('重连失败：' + rr.error), rr.ok ? 'ok' : 'err');
      await refresh();
    });
    $i('mcList').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
      if (!confirm('删除 MCP 服务「' + b.dataset.rm + '」？')) return;
      const rr = await API.post('/api/agent/mcp', { action: 'remove', server: b.dataset.rm });
      if (!rr.ok) Shell.toast('删除失败：' + rr.error, 'err');
      await refresh();
    });
  };

  $i('mcSave').onclick = async () => {
    const name = $i('mcName').value.trim();
    if (!name) { $i('mcMsg').textContent = '名字不能为空'; return; }
    const tr = $i('mcTr').value;
    const cfg = { name: name, transport: tr, enabled: $i('mcOn').checked };
    if (tr === 'stdio') {
      const parts = ($i('mcCmd').value || '').trim().split(/\s+/).filter(Boolean);
      if (!parts.length) { $i('mcMsg').textContent = 'stdio 需要命令'; return; }
      cfg.command = parts[0]; cfg.args = parts.slice(1);
    } else {
      const u = $i('mcURL').value.trim();
      if (!u) { $i('mcMsg').textContent = 'http 需要 URL'; return; }
      cfg.url = u;
    }
    $i('mcMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/mcp', { action: 'upsert', config: cfg });
    $i('mcMsg').textContent = r.ok ? '已保存' : ('失败：' + r.error);
    if (r.ok) await refresh();
  };

  $i('mcCall').onclick = async () => {
    const server = $i('mcCallServer').value.trim(), tool = $i('mcCallTool').value.trim();
    if (!server || !tool) { $i('mcCallMsg').textContent = '服务和工具名都要填'; return; }
    let args;
    const raw = ($i('mcCallArgs').value || '').trim();
    if (raw) { try { args = JSON.parse(raw); } catch (e) { $i('mcCallMsg').textContent = '参数不是合法 JSON'; return; } }
    $i('mcCallMsg').textContent = '调用中…';
    const r = await API.post('/api/agent/mcp', { action: 'call', server: server, tool: tool, args: args });
    $i('mcCallMsg').textContent = (r.ok && !r.data.error) ? '完成' : ('失败：' + ((r.data && r.data.error) || r.error));
    const out = (r.data && r.data.result) || {};
    $i('mcCallOut').innerHTML = `<div class="pre" style="margin-top:8px;max-height:220px;overflow:auto">${esc(out.text || JSON.stringify(out))}</div>`;
  };

  await refresh();
}

/* ================= 6. 技能 ================= */
/* 数据：GET /api/agent/state → skills[{name,description,path,body,triggers[],tokens}]
   说明：后端目前只提供「列出」。技能由 Agent 运行时的 skill_manage 工具创建/改写，
        没有对外的增删改接口 —— 这里如实只做只读展示。 */
async function renderSkills(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>技能库 <span class="sub" id="skN" style="margin:0"></span></h3>
        <div class="sub">技能是白泽自己攒的「操作手册」，由它在运行时用 skill_manage 创建/改写。<br>
          后端暂无对外的增删改接口 —— 所以这里只读；想加技能，直接跟白泽说。</div>
        <div id="skList"></div>
      </div>
      <div style="display:flex;gap:10px;align-items:center">
        <button class="btn ghost sm" id="skRefresh">刷新</button>
        <span class="sub" id="skMsg" style="margin:0"></span>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const refresh = async () => {
    const r = await API.get('/api/agent/state');
    if (!r.ok) { showErr($i('skList'), r); return; }
    const skills = (r.data && r.data.skills) || [];
    $i('skN').textContent = skills.length ? `（${skills.length}）` : '';
    $i('skList').innerHTML = skills.length ? skills.map(s => `
      <details class="row" style="display:block">
        <summary style="cursor:pointer"><b>${esc(s.name)}</b>
          <span class="mono">${esc(s.tokens || 0)} token${(s.triggers || []).length ? ' · 触发词 ' + esc((s.triggers || []).join(' / ')) : ''}</span></summary>
        <div class="sub" style="margin:6px 0 0">${esc(s.description || '')}</div>
        <div class="mono" style="color:var(--text-3);font-size:11px">${esc(s.path || '')}</div>
        <div class="pre" style="margin-top:6px;max-height:240px;overflow:auto">${esc((s.body || '').slice(0, 1200))}</div>
      </details>`).join('') : '<div class="empty">还没有技能</div>';
    $i('skMsg').textContent = '更新于 ' + new Date().toLocaleTimeString();
  };
  $i('skRefresh').onclick = refresh;
  await refresh();
}

/* ================= 7. 定时任务 ================= */
/* 数据：GET /api/agent/state → cron[{id,expr,goal,recipe,enabled}]（config.CronJob）
   动作：POST /api/agent/config {cronExpr, cronGoal} —— 只支持「新增」
   说明：后端目前没有「列出 / 删除 / 停用定时任务」的接口，列表来自状态快照；
        删除/停用需要后端补接口（已记进待办）。 */
async function renderCron(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>定时任务 <span class="sub" id="crN" style="margin:0"></span></h3>
        <div class="sub">到点让白泽自动干一件事。表达式是标准 5 段 cron（分 时 日 月 周），例如 <span class="mono">0 8 * * *</span>。</div>
        <div id="crList"></div>
      </div>

      <div class="sect">
        <h3>加一条</h3>
        <div class="fields" style="grid-template-columns:170px 1fr">
          <div><label>cron 表达式</label><input id="crExpr" class="mono" placeholder="0 8 * * *"></div>
          <div><label>要干什么</label><input id="crGoal" placeholder="总结昨天的工作记录并写成一条记忆"></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="crAdd">添加</button>
          <span class="sub" id="crMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const refresh = async () => {
    const r = await API.get('/api/agent/state');
    if (!r.ok) { showErr($i('crList'), r); return; }
    const jobs = (r.data && r.data.cron) || [];
    $i('crN').textContent = jobs.length ? `（${jobs.length}）` : '';
    $i('crList').innerHTML = jobs.length ? jobs.map(j => `
      <div class="row">
        <div class="who"><b class="mono">${esc(j.expr || '')}</b>
          <span>${esc(j.goal || '')}</span>
          <span class="mono">${esc(j.recipe || '')} · ${esc(j.id || '')}</span></div>
        <div class="tags"><span class="tag ${j.enabled ? 'on' : 'off'}">${j.enabled ? '启用' : '停用'}</span></div>
      </div>`).join('') : '<div class="empty">还没有定时任务</div>';
  };
  $i('crAdd').onclick = async () => {
    const expr = $i('crExpr').value.trim(), goal = $i('crGoal').value.trim();
    if (!expr || !goal) { $i('crMsg').textContent = '表达式和目标都要填'; return; }
    const r = await API.post('/api/agent/config', { cronExpr: expr, cronGoal: goal });
    $i('crMsg').textContent = r.ok ? '已添加' : ('失败：' + r.error);
    if (r.ok) { $i('crExpr').value = ''; $i('crGoal').value = ''; await refresh(); Shell.toast('定时任务已添加', 'ok'); }
  };
  await refresh();
}

/* ================= 8. 备份与恢复 ================= */
/* 数据：GET /api/agent/backups → {backups[{path,name,size,manifest{format,app,version,createdAt,
           dataDir,selection{config,hub,memory,runs,skills,checkpoints,workspace,vault},entries[],totalSize,note}}], dir, hint}
   动作：POST /api/agent/backups {selection,note}；/backups/verify {path}；
        /backups/restore {path,dryRun,only}；/backups/delete {path} */
async function renderBackup(root) {
  const SEL = [['config', '配置+令牌'], ['hub', '设备/任务/记录/记忆'], ['memory', '记忆树'],
    ['runs', '运行记录'], ['skills', '技能库'], ['checkpoints', '变更快照'],
    ['workspace', '工作目录'], ['vault', '密码本']];
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>新建备份</h3>
        <div class="sub">勾选要备份的内容；恢复前后端会自动做一次安全点。</div>
        <div class="fields" style="grid-template-columns:repeat(4,1fr)">
          ${SEL.map(([k, label]) => `<div style="display:flex;align-items:center;gap:6px">
            <input type="checkbox" data-sel="${k}" checked><span class="sub" style="margin:0">${esc(label)}</span></div>`).join('')}
        </div>
        <div class="fields" style="grid-template-columns:1fr 140px">
          <div><label>备注</label><input id="bkNote" placeholder="例如：换手机前"></div>
          <div><label>&nbsp;</label><button class="btn" id="bkNew">立刻备份</button></div>
        </div>
        <span class="sub" id="bkMsg" style="margin:0"></span>
      </div>

      <div class="sect">
        <h3>已有备份 <span class="sub" id="bkN" style="margin:0"></span></h3>
        <div id="bkDir" class="sub"></div>
        <div id="bkList"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const refresh = async () => {
    const r = await API.get('/api/agent/backups');
    if (!r.ok) { showErr($i('bkList'), r); return; }
    const d = r.data || {}, list = d.backups || [];
    $i('bkN').textContent = list.length ? `（${list.length}）` : '';
    $i('bkDir').textContent = '存放目录：' + (d.dir || '—') + (d.hint ? ' · ' + d.hint : '');
    $i('bkList').innerHTML = list.length ? list.map(b => {
      const m = b.manifest || {};
      const selKeys = Object.keys(m.selection || {}).filter(k => m.selection[k]);
      return `<div class="row">
        <div class="who"><b>${esc(b.name || b.path)}</b>
          <span class="mono">${bytes(b.size)} · ${m.entries ? m.entries.length + ' 个文件' : ''} · ${fmtTime(m.createdAt)}${m.note ? ' · ' + esc(m.note) : ''}</span>
          <span class="mono" style="color:var(--text-3)">含：${esc(selKeys.join(', ') || '（默认）')}</span></div>
        <div class="tags">
          <button class="btn sm ghost" data-verify="${esc(b.path)}">校验</button>
          <button class="btn sm ghost" data-dry="${esc(b.path)}">预演恢复</button>
          <button class="btn sm ghost" data-del="${esc(b.path)}">删除</button>
        </div>
      </div>`;
    }).join('') : '<div class="empty">还没有备份</div>';

    $i('bkList').querySelectorAll('[data-verify]').forEach(b => b.onclick = async () => {
      b.disabled = true;
      const rr = await API.post('/api/agent/backups/verify', { path: b.dataset.verify });
      b.disabled = false;
      Shell.toast(rr.ok ? '校验通过（清单与 sha256 一致）' : ('校验失败：' + rr.error), rr.ok ? 'ok' : 'err');
    });
    $i('bkList').querySelectorAll('[data-dry]').forEach(b => b.onclick = async () => {
      b.disabled = true; b.textContent = '预演中…';
      const rr = await API.post('/api/agent/backups/restore', { path: b.dataset.dry, dryRun: true });
      b.disabled = false; b.textContent = '预演恢复';
      const res = (rr.data && rr.data.result) || {};
      $i('bkMsg').textContent = rr.ok && !rr.data.error
        ? `预演完成：将恢复 ${res.restored || res.files || 0} 个文件（未改动任何数据）`
        : ('预演失败：' + ((rr.data && rr.data.error) || rr.error));
      if (res.items) $i('bkMsg').textContent += ' · ' + JSON.stringify(res.items).slice(0, 160);
    });
    $i('bkList').querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
      if (!confirm('删除这个备份文件？不可撤销。')) return;
      const rr = await API.post('/api/agent/backups/delete', { path: b.dataset.del });
      if (!rr.ok) Shell.toast('删除失败：' + rr.error, 'err');
      await refresh();
    });
  };

  $i('bkNew').onclick = async () => {
    const sel = {};
    root.querySelectorAll('[data-sel]').forEach(c => { sel[c.dataset.sel] = c.checked; });
    if (!Object.values(sel).some(Boolean)) { $i('bkMsg').textContent = '至少选一项'; return; }
    $i('bkMsg').textContent = '备份中…';
    $i('bkNew').disabled = true;
    const r = await API.post('/api/agent/backups', { selection: sel, note: $i('bkNote').value.trim() });
    $i('bkNew').disabled = false;
    if (r.ok) {
      const m = r.data.manifest || {};
      $i('bkMsg').textContent = `完成：${m.entries ? m.entries.length : 0} 个文件 / ${bytes(m.totalSize)}`;
      Shell.toast('备份完成', 'ok');
      await refresh();
    } else {
      $i('bkMsg').textContent = '失败：' + r.error;
    }
  };

  await refresh();
}

/* ================= 9. 活动追踪 ================= */
/* 数据：GET /api/agent/activity（AlwaysOn）
        → {level,active,stage,detail,updatedAtMs,sinceMs,runId,note,steps[{at,stage,detail,runId}],
           stats{runs,toolCalls,blocked,errors,startedAtMs,lastRunAtMs}} */
async function renderActivity(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="sect" style="margin-top:0">
        <h3>白泽现在在干什么</h3>
        <div class="sub">始终开启（AlwaysOn），3 秒刷新一次。</div>
        <div id="acNow" class="pre">读取中…</div>
        <div id="acTiles" class="sub" style="margin-top:8px"></div>
      </div>
      <div class="sect">
        <h3>轨迹</h3>
        <div class="sub">新 → 旧。</div>
        <div id="acSteps"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);

  const refresh = async () => {
    const r = await API.get('/api/agent/activity');
    if (!r.ok) { showErr($i('acNow'), r); return; }
    const d = r.data || {};
    const since = d.sinceMs ? Math.max(0, Math.round((Date.now() - d.sinceMs) / 1000)) : 0;
    $i('acNow').textContent =
      '状态：' + (d.active ? '正在干活' : '空闲') +
      '\n阶段：' + (d.stage || '—') +
      (d.detail ? '\n细节：' + d.detail : '') +
      (d.active && since ? '\n已跑：' + since + ' 秒' : '') +
      (d.runId ? '\n运行：' + d.runId : '') +
      (d.note ? '\n说明：' + d.note : '') +
      '\n更新：' + (d.updatedAtMs ? fmtTime(d.updatedAtMs) : '—');
    const s = d.stats || {};
    $i('acTiles').innerHTML =
      tile('档位', d.level || '-') + tile('运行', s.runs) + tile('工具调用', s.toolCalls) +
      tile('被拦', s.blocked, s.blocked ? 'warn' : '') + tile('错误', s.errors, s.errors ? 'warn' : '');

    const steps = d.steps || [];
    $i('acSteps').innerHTML = steps.length ? steps.map(st => `
      <div class="row">
        <div class="who"><b>${esc(st.stage || '')}</b>
          <span class="mono">${fmtTime(st.at)}${st.runId ? ' · ' + esc(st.runId) : ''}</span>
          ${st.detail ? `<span class="mono" style="color:var(--text-3)">${esc(String(st.detail).slice(0, 200))}</span>` : ''}</div>
      </div>`).join('') : '<div class="empty">还没有轨迹</div>';
  };

  await refresh();
  pollWhileMounted(root, refresh, 3000);
}

/* ================= 回填注册表 =================
   apps.js 里这 9 个只声明了 id/名字/图标/尺寸，render 在这里挂上去。
   为什么不直接在 apps.js 写 render: renderXxx？因为 apps.js 先加载，那一刻这些函数
   还没定义，对象字面量求值会直接 ReferenceError（整个 window.APPS 就废了）。 */
(function wireD2Apps() {
  const map = {
    tasks: renderTasks, kb: renderKB, memory: renderMemory, providers: renderProviders,
    mcp: renderMCP, skills: renderSkills, cron: renderCron, backup: renderBackup, activity: renderActivity,
  };
  Object.keys(map).forEach(id => {
    const app = window.APP_BY_ID && window.APP_BY_ID[id];
    if (app) app.render = map[id];
  });
})();
