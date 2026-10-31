/* 白泽桌面端 · D2：控制台其余 9 个应用
   任务与审批 / 知识库 / 记忆 / 模型通道 / MCP 服务 / 技能 / 定时任务 / 备份与恢复 / 活动追踪

   约定：
   - 每个 render(root) 自己画进内容区；数据一律走 /api/be/*（Go 侧代理，令牌不进网页）。
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
  return `<span class="stat ${cls || ''}"><i>${esc(label)}</i><b>${esc(value)}</b></span>`;
}

function stateTag(s) {
  const map = {
    done: 'on', ok: 'on', todo: '', open: '', running: 'warn', dispatched: 'warn',
    pending_approval: 'warn', failed: 'off', rejected: 'off', canceled: 'off', blocked: 'warn',
  };
  return map[s] !== undefined ? map[s] : '';
}

/* 切走/换应用就停止轮询：新外壳会摘掉旧容器并发 shell:closed，这里再补一层 DOM 断链兜底 */
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

/* 复制文本：优先 navigator.clipboard，不支持/失败时回退 execCommand + 临时 textarea */
function copyText(text) {
  text = String(text == null ? '' : text);
  if (navigator.clipboard && navigator.clipboard.writeText) {
    return navigator.clipboard.writeText(text).then(() => true).catch(() => copyTextFallback(text));
  }
  return Promise.resolve(copyTextFallback(text));
}
function copyTextFallback(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.position = 'fixed';
  ta.style.top = '-1000px';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
  document.body.removeChild(ta);
  return ok;
}

/* ================= 1. 任务与审批 ================= */
/* 数据：GET /api/state → tasks[{id,deviceId,action,args,status,needApproval,origin,createdAt,
   dispatchedAt,finishedAt,result}] / devices[] / actions[]（动作白名单）
   动作：POST /api/tasks {deviceId,action,args,origin}；POST /api/tasks/{id}/approve|reject */
async function renderTasks(root) {
  root.innerHTML = `
    <div class="page">
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
    $i('tkPending').innerHTML = '<div class="row-head"><span>动作 · 目标设备</span><span>操作</span></div>' + pending.map(t => `
      <div class="row">
        <div class="who"><b>${esc(t.action)}</b>
          <span class="mono">${esc(t.deviceId)} · ${esc(JSON.stringify(t.args || {}))}</span></div>
        <div class="tags">
          <button class="btn sm" data-ok="${esc(t.id)}">批准</button>
          <button class="btn ghost sm" data-no="${esc(t.id)}">驳回</button>
        </div>
      </div>`).join('');
    $i('tkPending').querySelectorAll('[data-ok]').forEach(b => b.onclick = () => act('/api/tasks/' + b.dataset.ok + '/approve'));
    $i('tkPending').querySelectorAll('[data-no]').forEach(b => b.onclick = () => act('/api/tasks/' + b.dataset.no + '/reject', { reason: '控制台驳回' }));

    const recent = tasks.slice().sort((a, b) => (b.createdAt || 0) - (a.createdAt || 0)).slice(0, 40);
    $i('tkList').innerHTML = recent.length ? '<div class="row-head"><span>动作 · 目标设备 · 来源</span><span>状态</span></div>' + recent.map(t => {
      const res = t.result == null ? '' : (typeof t.result === 'string' ? t.result : JSON.stringify(t.result));
      return `<div class="row">
        <div class="who"><b>${esc(t.action)}</b>
          <span class="mono">${esc(t.deviceId)} · ${esc(t.origin || '')} · ${fmtTime(t.createdAt)}${t.finishedAt ? ' → ' + fmtTime(t.finishedAt) : ''}</span>
          ${res ? `<span class="mono" style="color:var(--text-3)">${esc(res.slice(0, 180))}</span>` : ''}
        </div>
        <div class="tags"><span class="tag ${stateTag(t.status)}">${esc(t.status)}</span></div>
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
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>概览</h3>
        <div id="kbTiles" class="sub" style="margin-bottom:6px">读取中…</div>
      </div>

      <div class="sect">
        <h3>待办 <span class="count" id="kbTodoN"></span></h3>
        <div class="sub">正本在 NAS；手机上只是缓存。勾选 = 完成，✕ = 删除。</div>
        <div id="kbTodos"></div>
      </div>

      <div class="sect">
        <h3>附件 <span class="count" id="kbFileN"></span></h3>
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
          <button class="btn ghost sm" data-del title="删除">✕</button>
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
          <button class="btn ghost sm" data-get="${esc(f.id)}">下载</button>
          <button class="btn ghost sm danger" data-rm="${esc(f.id)}">删除</button>
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
    <div class="page">
      <div class="tabs" id="mmTabs">
        <button data-tab="search" class="on">检索</button>
        <button data-tab="graph">星图</button>
        <button data-tab="notes">笔记</button>
        <button data-tab="import">导入</button>
      </div>

      <div data-panel="search">
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
      </div>

      <div data-panel="graph" hidden>
        <div class="sect" style="margin-top:0">
          <h3>记忆星图</h3>
          <div class="sub" id="gpStats">读取中…</div>
          <div class="fields" style="grid-template-columns:repeat(4,1fr)">
            <div><label>最多显示</label><input id="gpLimit" value="200"></div>
            <div><label>自动边相似度下限</label><input id="gpMin" value="0.55"></div>
            <div><label>每篇最多连几个</label><input id="gpTopK" value="5"></div>
            <div><label>局部图跳数</label><input id="gpHops" value="1"></div>
          </div>
          <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">
            <label style="display:flex;gap:5px;align-items:center;margin:0;color:var(--text-2)">
              <input type="checkbox" id="gpAuto" checked> 显示自动边</label>
            <button class="btn ghost" id="gpReload">刷新</button>
            <button class="btn" id="gpAutolink">自动连边</button>
            <button class="btn ghost" id="gpResolve">重解析链接</button>
            <button class="btn ghost" id="gpFocus">只看选中</button>
            <button class="btn ghost" id="gpAll">看全图</button>
            <span class="sub" id="gpMsg" style="margin:0"></span>
          </div>
          <div class="graph-wrap" style="margin-top:10px">
            <canvas id="gpCanvas"></canvas>
            <div class="graph-hint">滚轮缩放 · 拖动节点 · 单击看笔记 · 双击以它为中心</div>
          </div>
          <div class="legend"><span><i></i>手工双链</span><span><i class="auto"></i>自动连边</span>
            <span id="gpSel" style="margin-left:auto"></span></div>
        </div>
      </div>

      <div data-panel="notes" hidden>
        <div class="split">
          <div>
            <div class="fields" style="grid-template-columns:1fr;margin-top:0">
              <div><label>找笔记</label><input id="ntQ" placeholder="标题 / 正文关键词"></div>
            </div>
            <div style="display:flex;gap:8px;margin-bottom:8px">
              <button class="btn ghost sm" id="ntReload">刷新</button>
              <button class="btn sm" id="ntNew">新建</button>
            </div>
            <div class="pane-list" id="ntList"></div>
          </div>
          <div>
            <div class="fields" style="grid-template-columns:1fr 1fr;margin-top:0">
              <div><label>路径（同一路径 = 覆盖保存）</label><input id="ntPath" placeholder="例如 项目/白泽.md"></div>
              <div><label>标题（可空，默认取正文首个一级标题）</label><input id="ntTitle"></div>
            </div>
            <div style="margin-bottom:8px"><label>正文（[[双向链接]] 会自动解析，写 #标签 也能被识别）</label>
              <textarea id="ntContent" style="min-height:220px"></textarea></div>
            <div style="display:flex;gap:10px;align-items:center">
              <button class="btn" id="ntSave">保存</button>
              <button class="btn ghost danger" id="ntDelete">删除</button>
              <span class="sub" id="ntMsg" style="margin:0"></span>
            </div>
            <div class="sect"><h3>出链（指向别人）</h3><div id="ntOut"></div></div>
            <div class="sect"><h3>反向链接（谁指向它）</h3><div id="ntBack"></div></div>
          </div>
        </div>
      </div>

      <div data-panel="import" hidden>
        <div class="sect" style="margin-top:0">
          <h3>从本机文件夹导入笔记（Obsidian 库）</h3>
          <div class="sub">后端在 NAS 上读不到你电脑的磁盘，所以由桌面端扫描本机文件夹，把 .md 内容交给后端。
            正文里的 [[双向链接]] 与 #标签 会自动解析，导入完自动把链接接上；.obsidian / .git 等目录会跳过。</div>
          <div class="fields" style="grid-template-columns:1fr 180px">
            <div><label>库目录（本机绝对路径）</label><input id="imDir" placeholder="例如 D:\\我的笔记\\Obsidian"></div>
            <div><label>导入到分区</label><input id="imNs" placeholder="留空 = 默认分区"></div>
          </div>
          <button class="btn" id="imGo">开始导入</button>
          <span class="sub" id="imMsg" style="margin-left:10px"></span>
        </div>
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
    if (!hits.length) { $i('mmOut').innerHTML = head + '<div class="empty">还没有命中</div>'; return; }
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
          ${h.why ? `<span class="tag wrap">${esc(h.why)}</span>` : ''}
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

  /* ---------- 页签切换 ---------- */
  const tabs = $i('mmTabs');
  const showTab = (name) => {
    tabs.querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.tab === name));
    root.querySelectorAll('[data-panel]').forEach(p => { p.hidden = p.dataset.panel !== name; });
    if (name === 'graph') loadGraph();
    if (name === 'notes') loadNotes();
  };
  tabs.querySelectorAll('button').forEach(b => { b.onclick = () => showTab(b.dataset.tab); });

  /* ---------- 记忆星图 ---------- */
  let graphData = { nodes: [], edges: [] };
  let selKey = '';        // 当前选中的笔记键（星图里高亮）
  let focusRoot = '';     // 局部图的中心
  let sim = { raf: 0, stopped: false };

  const loadGraph = async () => {
    const limit = $i('gpLimit').value.trim() || '200';
    let q = `/api/agent/notes/graph?limit=${encodeURIComponent(limit)}&auto=${$i('gpAuto').checked ? 1 : 0}`;
    q += `&minWeight=${encodeURIComponent($i('gpMin').value.trim() || '0')}`;
    if (focusRoot) q += `&root=${encodeURIComponent(focusRoot)}&hops=${encodeURIComponent($i('gpHops').value.trim() || '1')}`;
    const r = await API.get(q);
    if (!r.ok) { $i('gpStats').innerHTML = `<span class="err">读取失败：${esc(r.error)}</span>`; return; }
    graphData = r.data || { nodes: [], edges: [] };
    const d = graphData;
    $i('gpStats').innerHTML =
      `笔记 <b>${d.noteCount || 0}</b> 篇 · 显示 <b>${d.shown || 0}</b> 个点 · ` +
      `手工链 <b>${d.explicitEdges || 0}</b> · 自动链 <b>${d.autoEdges || 0}</b> · 孤立 <b>${d.orphans || 0}</b>` +
      (focusRoot ? ' · <span class="sub">局部图</span>' : '') +
      (d.note ? `<br><span class="sub">${esc(d.note)}</span>` : '');
    startGraph();
  };

  // 力导向布局：斥力（点之间）+ 弹簧（有边相连的）+ 向心力（别飘走）。
  // 纯 canvas，无外部依赖；几百个点用 O(n²) 也够快。
  const startGraph = () => {
    if (sim.raf) cancelAnimationFrame(sim.raf);
    sim.stopped = true;
    sim = { raf: 0, stopped: false };
    const canvas = $i('gpCanvas');
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    const dpr = window.devicePixelRatio || 1;
    const W = canvas.clientWidth || 640, H = canvas.clientHeight || 430;
    canvas.width = Math.round(W * dpr);
    canvas.height = Math.round(H * dpr);

    const nodes = (graphData.nodes || []).map(n => ({
      ...n,
      x: W / 2 + (Math.random() - 0.5) * W * 0.72,
      y: H / 2 + (Math.random() - 0.5) * H * 0.72,
      vx: 0, vy: 0,
    }));
    const idx = {};
    nodes.forEach((n, i) => { idx[n.key] = i; });
    const edges = (graphData.edges || [])
      .filter(e => idx[e.from] != null && idx[e.to] != null)
      .map(e => ({ s: idx[e.from], t: idx[e.to], kind: e.kind, weight: Number(e.weight) || 0 }));
    const N = nodes.length;
    const view = { k: 1, x: 0, y: 0 };
    const st = { drag: null, sel: selKey };

    const font = '11px "Segoe UI","Microsoft YaHei",sans-serif';
    const shorten = (s, n) => { s = String(s || ''); return s.length > n ? s.slice(0, n) + '…' : s; };

    const draw = () => {
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, W, H);
      ctx.save();
      ctx.translate(view.x, view.y);
      ctx.scale(view.k, view.k);
      for (const e of edges) {
        const a = nodes[e.s], b = nodes[e.t];
        const near = !st.sel || a.key === st.sel || b.key === st.sel;
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.lineTo(b.x, b.y);
        if (e.kind === 'auto') {
          ctx.setLineDash([4, 4]);
          ctx.strokeStyle = near
            ? 'rgba(56,189,248,' + (0.18 + Math.min(0.5, e.weight * 0.6)).toFixed(2) + ')'
            : 'rgba(100,116,139,.14)';
        } else {
          ctx.setLineDash([]);
          ctx.strokeStyle = near ? 'rgba(56,189,248,.62)' : 'rgba(100,116,139,.22)';
        }
        ctx.lineWidth = 1;
        ctx.stroke();
      }
      ctx.setLineDash([]);
      for (const n of nodes) {
        const r = 4 + (Number(n.size) || 1) * 1.5;
        const on = n.key === st.sel;
        ctx.beginPath();
        ctx.arc(n.x, n.y, on ? r + 2 : r, 0, Math.PI * 2);
        ctx.fillStyle = on ? '#38bdf8' : (n.explicitDegree ? '#7dd3fc' : (n.autoDegree ? '#a5b4c4' : '#64748b'));
        ctx.fill();
        if (on || view.k > 1.2 || nodes.length <= 30) {
          ctx.fillStyle = on ? 'rgba(219,228,238,.95)' : 'rgba(219,228,238,.6)';
          ctx.font = font;
          ctx.fillText(shorten(n.title || n.key, 16), n.x + r + 4, n.y + 3);
        }
      }
      ctx.restore();
    };

    const toWorld = (mx, my) => ({ x: (mx - view.x) / view.k, y: (my - view.y) / view.k });
    const localXY = (ev) => {
      const rect = canvas.getBoundingClientRect();
      return { x: ev.clientX - rect.left, y: ev.clientY - rect.top };
    };
    const pick = (mx, my) => {
      const p = toWorld(mx, my);
      let best = -1, bestD = 20 * 20;
      nodes.forEach((n, i) => {
        const dx = n.x - p.x, dy = n.y - p.y, d2 = dx * dx + dy * dy;
        if (d2 < bestD) { bestD = d2; best = i; }
      });
      return best;
    };

    const step = () => {
      if (st.stopped) return;
      const rep = 5600, spring = 0.012, springLen = 88, center = 0.0024, damp = 0.85;
      for (let i = 0; i < N; i++) {
        const a = nodes[i];
        for (let j = i + 1; j < N; j++) {
          const b = nodes[j];
          let dx = a.x - b.x, dy = a.y - b.y;
          let d2 = dx * dx + dy * dy;
          if (d2 < 1) { d2 = 1; dx = Math.random() - 0.5; dy = Math.random() - 0.5; }
          const d = Math.sqrt(d2);
          const f = rep / d2;
          const fx = (dx / d) * f, fy = (dy / d) * f;
          a.vx += fx; a.vy += fy; b.vx -= fx; b.vy -= fy;
        }
      }
      for (const e of edges) {
        const a = nodes[e.s], b = nodes[e.t];
        const dx = b.x - a.x, dy = b.y - a.y;
        const d = Math.sqrt(dx * dx + dy * dy) || 1;
        const f = (d - springLen) * spring;
        const fx = (dx / d) * f, fy = (dy / d) * f;
        a.vx += fx; a.vy += fy; b.vx -= fx; b.vy -= fy;
      }
      for (const n of nodes) {
        n.vx += (W / 2 - n.x) * center;
        n.vy += (H / 2 - n.y) * center;
        n.vx *= damp; n.vy *= damp;
        if (st.drag !== n) { n.x += n.vx; n.y += n.vy; }
        n.x = Math.max(16, Math.min(W - 16, n.x));
        n.y = Math.max(16, Math.min(H - 16, n.y));
      }
      draw();
      sim.raf = requestAnimationFrame(step);
    };

    let panning = null;
    canvas.onmousedown = (ev) => {
      const p = localXY(ev);
      const i = pick(p.x, p.y);
      if (i >= 0) { st.drag = nodes[i]; canvas.classList.add('grabbing'); }
      else panning = { cx: ev.clientX, cy: ev.clientY, vx: view.x, vy: view.y };
    };
    canvas.onmousemove = (ev) => {
      const p = localXY(ev);
      if (st.drag) {
        const w = toWorld(p.x, p.y);
        st.drag.x = w.x; st.drag.y = w.y; st.drag.vx = 0; st.drag.vy = 0;
      } else if (panning) {
        view.x = panning.vx + (ev.clientX - panning.cx);
        view.y = panning.vy + (ev.clientY - panning.cy);
      } else {
        canvas.style.cursor = pick(p.x, p.y) >= 0 ? 'pointer' : 'grab';
      }
    };
    const endDrag = () => { st.drag = null; panning = null; canvas.classList.remove('grabbing'); };
    canvas.onmouseup = endDrag;
    canvas.onmouseleave = endDrag;
    canvas.onclick = (ev) => {
      const p = localXY(ev);
      const i = pick(p.x, p.y);
      if (i < 0) { st.sel = ''; selKey = ''; $i('gpSel').textContent = ''; return; }
      st.sel = nodes[i].key;
      selKey = nodes[i].key;
      $i('gpSel').innerHTML = `已选：<b>${esc(nodes[i].title || nodes[i].key)}</b> · 连接 ${nodes[i].degree}（手工 ${nodes[i].explicitDegree} / 自动 ${nodes[i].autoDegree}）`;
    };
    canvas.ondblclick = async (ev) => {
      const p = localXY(ev);
      const i = pick(p.x, p.y);
      if (i < 0) return;
      showTab('notes');
      await loadNotes();
      await openNote(nodes[i].key);
    };
    canvas.onwheel = (ev) => {
      ev.preventDefault();
      const p = localXY(ev);
      const w = toWorld(p.x, p.y);
      const nk = Math.max(0.25, Math.min(3, view.k * (ev.deltaY < 0 ? 1.12 : 0.89)));
      view.k = nk;
      view.x = p.x - w.x * nk;
      view.y = p.y - w.y * nk;
    };

    sim.stopped = false;
    sim.raf = requestAnimationFrame(step);
  };

  $i('gpReload').onclick = loadGraph;
  $i('gpAuto').onchange = loadGraph;
  $i('gpAll').onclick = () => { focusRoot = ''; loadGraph(); };
  $i('gpFocus').onclick = () => {
    if (!selKey) { $i('gpMsg').textContent = '先在图上点一个点'; return; }
    focusRoot = selKey; loadGraph();
  };
  $i('gpAutolink').onclick = async () => {
    $i('gpMsg').textContent = '按语义连边中…';
    const r = await API.post('/api/agent/notes/autolink', {
      minSim: Number($i('gpMin').value) || 0.55,
      topK: Number($i('gpTopK').value) || 5,
    });
    if (!r.ok) { $i('gpMsg').innerHTML = `<span class="err">失败：${esc(r.error)}</span>`; return; }
    const d = r.data || {};
    $i('gpMsg').textContent = d.vectorUsed
      ? `已连 ${d.pairs || 0} 对（${d.edges || 0} 条边）`
      : (d.note || '没能连边');
    await loadGraph();
  };
  $i('gpResolve').onclick = async () => {
    const r = await API.post('/api/agent/notes/resolve', {});
    $i('gpMsg').textContent = r.ok ? `解析到 ${r.data.resolved || 0} 条链接` : ('失败：' + r.error);
    await loadGraph();
  };

  /* ---------- 笔记编辑 ---------- */
  let curNote = null;

  const loadNotes = async () => {
    const q = $i('ntQ').value.trim();
    const r = await API.get('/api/agent/notes?limit=200' + (q ? '&q=' + encodeURIComponent(q) : ''));
    if (!r.ok) { $i('ntList').innerHTML = `<div class="empty err" style="padding:12px">${esc(r.error)}</div>`; return; }
    const notes = (r.data && r.data.notes) || [];
    $i('ntList').innerHTML = notes.length ? notes.map(n =>
      `<div class="note-item${curNote && curNote.docKey === n.docKey ? ' on' : ''}" data-key="${esc(n.docKey)}">
        <b>${esc(n.title || n.path)}</b>
        <span>${esc(n.path)} · ${n.links ? n.links.length : 0} 出链${n.tags && n.tags.length ? ' · #' + n.tags.map(esc).join(' #') : ''}</span>
      </div>`).join('')
      : '<div class="empty" style="padding:12px">还没有笔记；点「新建」，或到「导入」里导入 Obsidian 库</div>';
    $i('ntList').querySelectorAll('[data-key]').forEach(el => { el.onclick = () => openNote(el.dataset.key); });
    if (notes.length && (!curNote || !notes.some(n => n.docKey === curNote.docKey))) {
      await openNote(notes[0].docKey);
    }
  };

  const renderRel = (el, items, kind) => {
    $i(el).innerHTML = items.length ? items.map(o => {
      const missing = !!o.missing;
      const label = kind === 'out'
        ? (missing ? `${o.target}（目标还不存在）` : (o.title || o.target))
        : (o.title || o.path) + (o.kind === 'auto' ? `（自动，相似度 ${(Number(o.weight) || 0).toFixed(2)}）` : '');
      const mark = kind === 'out' ? (missing ? '悬空' : '→') : (o.kind === 'auto' ? '≈' : '←');
      return `<div class="link-item"${missing ? '' : ` data-key="${esc(o.noteKey)}"`}>
        <span class="mono">${mark}</span><span class="ln">${esc(label)}</span></div>`;
    }).join('') : `<div class="sub" style="margin:0">${kind === 'out' ? '没有出链' : '还没有反向链接'}</div>`;
    $i(el).querySelectorAll('[data-key]').forEach(x => { x.onclick = () => openNote(x.dataset.key); });
  };

  const openNote = async (key) => {
    const r = await API.get('/api/agent/notes/' + encodeURIComponent(key));
    if (!r.ok) { $i('ntMsg').innerHTML = `<span class="err">打开失败：${esc(r.error)}</span>`; return; }
    const d = r.data || {};
    curNote = d.note || null;
    if (!curNote) return;
    $i('ntPath').value = curNote.path || '';
    $i('ntTitle').value = curNote.title || '';
    $i('ntContent').value = curNote.content || '';
    renderRel('ntOut', d.outlinks || [], 'out');
    renderRel('ntBack', d.backlinks || [], 'back');
    $i('ntMsg').textContent = '';
    $i('ntList').querySelectorAll('[data-key]').forEach(el => {
      el.classList.toggle('on', el.dataset.key === key);
    });
  };

  $i('ntReload').onclick = loadNotes;
  $i('ntNew').onclick = () => {
    curNote = null;
    $i('ntPath').value = '';
    $i('ntTitle').value = '';
    $i('ntContent').value = '';
    $i('ntOut').innerHTML = '';
    $i('ntBack').innerHTML = '';
    $i('ntMsg').textContent = '填好内容点「保存」';
    $i('ntPath').focus();
  };
  $i('ntQ').onkeydown = (ev) => { if (ev.key === 'Enter') loadNotes(); };
  $i('ntSave').onclick = async () => {
    const content = $i('ntContent').value.trim();
    if (!content) { $i('ntMsg').textContent = '正文不能为空'; return; }
    const r = await API.post('/api/agent/notes', {
      path: $i('ntPath').value.trim(),
      title: $i('ntTitle').value.trim(),
      content: content,
    });
    if (!r.ok) { $i('ntMsg').innerHTML = `<span class="err">保存失败：${esc(r.error)}</span>`; return; }
    const n = (r.data && r.data.note) || {};
    curNote = n;
    $i('ntPath').value = n.path || $i('ntPath').value;
    $i('ntMsg').innerHTML = `<span class="ok">已保存${n.links && n.links.length ? `，解析到 ${n.links.length} 条链接` : ''}${n.hasVector ? '' : '（没落向量：语义连边要先配 embedding 通道）'}</span>`;
    await loadNotes();
    await openNote(n.docKey);
  };
  $i('ntDelete').onclick = async () => {
    if (!curNote) { $i('ntMsg').textContent = '先选一篇笔记'; return; }
    if (!confirm(`确定删除「${curNote.title}」？连同它的双向链接一起删掉，删了就真没了。`)) return;
    const r = await API.post('/api/agent/notes/' + encodeURIComponent(curNote.docKey) + '/forget', {});
    if (!r.ok) { $i('ntMsg').innerHTML = `<span class="err">删除失败：${esc(r.error)}</span>`; return; }
    curNote = null;
    $i('ntPath').value = ''; $i('ntTitle').value = ''; $i('ntContent').value = '';
    $i('ntOut').innerHTML = ''; $i('ntBack').innerHTML = '';
    $i('ntMsg').innerHTML = '<span class="ok">已删除</span>';
    await loadNotes();
  };

  /* ---------- 导入（走桌面端本机接口，能读你电脑的磁盘） ---------- */
  $i('imGo').onclick = async () => {
    const dir = $i('imDir').value.trim();
    if (!dir) { $i('imMsg').textContent = '请填写要导入的文件夹'; return; }
    $i('imMsg').textContent = '扫描并导入中，笔记多时可能要一会儿…';
    let r;
    try {
      const resp = await fetch('/api/local/notes/import', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: dir, namespace: $i('imNs').value.trim() }),
      });
      r = await resp.json();
    } catch (e) {
      $i('imMsg').innerHTML = `<span class="err">失败：${esc(e && e.message)}</span>`;
      return;
    }
    if (!r || !r.ok) {
      $i('imMsg').innerHTML = `<span class="err">失败：${esc((r && r.error) || '未知错误')}</span>`;
      return;
    }
    $i('imMsg').innerHTML = `<span class="ok">导入完成：扫描 ${r.scanned || 0} 个文件，写入 ${r.notes || 0} 篇，解析链接 ${r.links || 0} 条`
      + (r.skipped ? `，跳过 ${r.skipped}` : '') + '</span>';
    await loadNotes();
  };

  await loadTiles();
  await search();
  return () => { sim.stopped = true; if (sim.raf) cancelAnimationFrame(sim.raf); };
}

/* ================= 4. 模型通道 ================= */
/* 数据：GET /api/agent/providers → {allowRemote, providers[{name,protocol,baseUrl,model,fallback,hasApiKey,usable,local}]}
        GET /api/agent/config → {embedding{baseUrl,model,dim,hasApiKey}, ...}
   动作：POST /api/agent/providers {action:upsert|remove|test, name, config{name,protocol,baseUrl,model,apiKey}, allowRemote}
        POST /api/agent/embedding/test、/api/agent/embedding/reindex */
async function renderProviders(root) {
  root.innerHTML = `
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>对话模型通道</h3>
        <div class="sub">密钥只存在后端，这里只显示「配没配」。</div>
        <div id="pvList"></div>
      </div>

      <div class="sect">
        <h3>加 / 改一条</h3>
        <label>从模板新建（选一个自动填地址与示例模型）</label>
        <div class="bar tight">
          <select id="pvPreset" style="flex:1;min-width:180px"><option value="">（自己填）</option></select>
          <button class="btn ghost sm" id="pvDiscover">模型发现</button>
        </div>
        <div id="pvPresetNote" class="sub" style="margin:6px 0 0"></div>
        <div class="fields" style="grid-template-columns:1fr 140px">
          <div><label>名字</label><input id="pvName" placeholder="deepseek"></div>
          <div><label>协议</label><select id="pvProto"><option value="openai">openai</option><option value="anthropic">anthropic</option></select></div>
        </div>
        <div class="fields">
          <div><label>Base URL</label><input id="pvBase" placeholder="https://api.deepseek.com/v1"></div>
          <div><label>模型</label><input id="pvModel" placeholder="deepseek-chat" list="pvModelList"><datalist id="pvModelList"></datalist></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>API Key（留空 = 保留原 key）</label><input id="pvKey" type="password"></div>
        </div>
        <div class="bar tight">
          <label class="sub" style="margin:0"><input type="checkbox" id="pvRemote" style="margin-right:6px">允许远程</label>
        </div>
        <div class="fields" style="grid-template-columns:1fr 1fr 1fr">
          <div><label>任务类型（逗号分隔，空 = 通吃）</label><input id="pvKinds" placeholder="chat, summarize"></div>
          <div><label>成本标注（人话）</label><input id="pvCost" placeholder="免费（本地）/ 按量计费"></div>
          <div><label>隐私标注</label><input id="pvPrivacy" placeholder="本地，不出机器 / 公网云端"></div>
        </div>
        <div class="bar tight">
          <label class="sub" style="margin:0"><input type="checkbox" id="pvFallback" style="margin-right:6px">作为回退通道（主通道失败时按顺序兜底）</label>
        </div>
        <div class="bar">
          <button class="btn" id="pvSave">保存</button>
          <button class="btn ghost" id="pvClear">清空</button>
          <span class="sub" id="pvMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>向量化通道（记忆语义检索用）</h3>
        <div id="pvEmb" class="pre">读取中…</div>
        <div style="display:flex;gap:10px;align-items:center;margin-top:8px">
          <button class="btn ghost sm" id="pvEmbTest">探活</button>
          <button class="btn ghost sm" id="pvEmbReindex">给旧记忆补向量</button>
          <span class="sub" id="pvEmbMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let recharges = {};   // 通道充值流水（/api/agent/providers/recharge），按通道名分组

  const refresh = async () => {
    const r = await API.get('/api/agent/providers');
    if (!r.ok) { showErr($i('pvList'), r); return; }
    const ps = (r.data && r.data.providers) || [];
    const presets = (r.data && r.data.presets) || [];
    const rcResp = await API.get('/api/agent/providers/recharge');
    recharges = (rcResp.ok && rcResp.data && rcResp.data.recharges) || {};
    const selP = $i('pvPreset');
    if (selP && selP.options.length <= 1) {
      selP.innerHTML = '<option value="">（自己填）</option>' +
        presets.map(x => `<option value="${esc(x.id)}">${esc(x.name)}</option>`).join('');
      selP.dataset.presets = JSON.stringify(presets);
    }
    $i('pvList').innerHTML = ps.length ? ps.map(p => {
      const rc = (recharges[p.name] || []).reduce((a, e) => a + (e.amount || 0), 0);
      return `
      <div class="row">
        <div class="who"><b>${esc(p.name)}</b>
          <span class="mono">${esc(p.protocol)} · ${esc(p.model || '')} · ${esc(p.baseUrl || '')}</span></div>
        <div class="tags">
          ${p.hasApiKey ? '<span class="tag on">有 key</span>' : '<span class="tag warn">无 key</span>'}
          ${p.usable ? '<span class="tag on">可用</span>' : '<span class="tag off">不可用</span>'}
          ${p.local ? '<span class="tag">本地</span>' : ''}
          ${p.privacy ? `<span class="tag wrap">${esc(p.privacy)}</span>` : ''}
          ${p.cost ? `<span class="tag wrap">${esc(p.cost)}</span>` : ''}
          ${(p.kinds || []).length ? `<span class="tag wrap">管 ${esc(p.kinds.join('/'))}</span>` : ''}
          ${p.fallback ? '<span class="tag">兜底</span>' : ''}
          ${rc > 0 ? `<span class="tag">累计 ¥${esc(String(rc))}</span>` : ''}
          <button class="btn ghost sm" data-test="${esc(p.name)}">探活</button>
          <button class="btn ghost sm" data-copykey="${esc(p.name)}" ${p.hasApiKey ? '' : 'disabled'}>复制 Key</button>
          <button class="btn ghost sm" data-copyurl="${esc(p.name)}">复制 URL</button>
          <button class="btn ghost sm" data-recharge="${esc(p.name)}">充值</button>
          <button class="btn ghost sm danger" data-rm="${esc(p.name)}">删除</button>
        </div>
      </div>`; }).join('') : '<div class="empty">还没有配置模型通道</div>';

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
    $i('pvList').querySelectorAll('[data-copykey]').forEach(b => b.onclick = async () => {
      const rr = await API.post('/api/agent/providers/secret', { name: b.dataset.copykey, field: 'apiKey' });
      if (!rr.ok) { Shell.toast('读取失败：' + rr.error, 'err'); return; }
      const ok = await copyText(rr.data.value);
      Shell.toast(ok ? 'Key 已复制' : '复制失败：浏览器不允许复制', ok ? 'ok' : 'err');
    });
    $i('pvList').querySelectorAll('[data-copyurl]').forEach(b => b.onclick = async () => {
      const rr = await API.post('/api/agent/providers/secret', { name: b.dataset.copyurl, field: 'baseUrl' });
      if (!rr.ok) { Shell.toast('读取失败：' + rr.error, 'err'); return; }
      const ok = await copyText(rr.data.value);
      Shell.toast(ok ? 'Base URL 已复制' : '复制失败：浏览器不允许复制', ok ? 'ok' : 'err');
    });
    $i('pvList').querySelectorAll('[data-recharge]').forEach(b => b.onclick = async () => {
      const name = b.dataset.recharge;
      const raw = prompt('给「' + name + '」记一笔充值，金额（元）：');
      if (raw === null) return;
      const amount = parseFloat(String(raw).trim());
      if (!isFinite(amount) || amount <= 0) { Shell.toast('金额要填一个大于 0 的数字', 'err'); return; }
      const note = prompt('备注（可留空）：') || '';
      const rr = await API.post('/api/agent/providers/recharge', { action: 'add', name: name, amount: amount, note: note });
      if (!rr.ok) { Shell.toast('记账失败：' + rr.error, 'err'); return; }
      Shell.toast('已记一笔充值', 'ok');
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

  $i('pvPreset').onchange = () => {
    const list = JSON.parse($i('pvPreset').dataset.presets || '[]');
    const p = list.find(x => x.id === $i('pvPreset').value);
    const note = $i('pvPresetNote');
    if (!p) { note.textContent = ''; return; }
    $i('pvName').value = p.id;
    $i('pvProto').value = p.protocol;
    $i('pvBase').value = p.baseUrl || '';
    $i('pvModel').value = p.model || '';
    $i('pvCost').value = p.cost || '';
    $i('pvPrivacy').value = p.privacy || '';
    $i('pvRemote').checked = p.region !== 'local';
    $i('pvMsg').textContent = '';
    note.innerHTML = (p.note ? esc(p.note) : '')
      + (p.docUrl ? ` <a href="${esc(p.docUrl)}" target="_blank" rel="noreferrer">文档</a>` : '');
  };

  $i('pvDiscover').onclick = async () => {
    const base = $i('pvBase').value.trim();
    if (!base) { $i('pvMsg').textContent = '先填 Base URL'; return; }
    $i('pvMsg').textContent = '发现模型中…';
    const r = await API.post('/api/agent/providers', {
      action: 'discover', protocol: $i('pvProto').value, baseUrl: base,
      apiKey: $i('pvKey').value.trim(), allowRemote: $i('pvRemote').checked,
    });
    if (!r.ok) { $i('pvMsg').textContent = '发现失败：' + r.error; return; }
    if (r.data && r.data.error) { $i('pvMsg').textContent = '发现失败：' + r.data.error; return; }
    const ms = (r.data && r.data.models) || [];
    $i('pvModelList').innerHTML = ms.map(m => `<option value="${esc(m)}"></option>`).join('');
    if (!$i('pvModel').value && ms.length) $i('pvModel').value = ms[0];
    $i('pvMsg').textContent = `发现 ${ms.length} 个模型（${(r.data && r.data.source) || ''}）—— 点「模型」输入框挑一个`;
  };

  $i('pvSave').onclick = async () => {
    const name = $i('pvName').value.trim();
    if (!name) { $i('pvMsg').textContent = '名字不能为空'; return; }
    const key = $i('pvKey').value.trim();
    const kinds = $i('pvKinds').value.split(',').map(s => s.trim()).filter(Boolean);
    const cfg = {
      name: name, protocol: $i('pvProto').value,
      baseUrl: $i('pvBase').value.trim(), model: $i('pvModel').value.trim(),
      kinds: kinds, fallback: $i('pvFallback').checked,
      cost: $i('pvCost').value.trim(), privacy: $i('pvPrivacy').value.trim(),
    };
    if (key) cfg.apiKey = key;
    $i('pvMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/providers', { action: 'upsert', config: cfg, allowRemote: $i('pvRemote').checked });
    $i('pvMsg').textContent = r.ok ? '已保存' : ('失败：' + r.error);
    if (r.ok) { $i('pvKey').value = ''; await refresh(); }
  };
  $i('pvClear').onclick = () => {
    ['pvName', 'pvBase', 'pvModel', 'pvKey', 'pvKinds', 'pvCost', 'pvPrivacy'].forEach(k => { $i(k).value = ''; });
    $i('pvFallback').checked = false; $i('pvRemote').checked = false;
    $i('pvPreset').value = ''; $i('pvPresetNote').textContent = ''; $i('pvMsg').textContent = '';
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
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>MCP 服务</h3>
        <div class="sub">MCP 工具默认走人工审批；只有你显式信任（或列进 safeTools）的才免批。</div>
        <div id="mcHint" class="sub"></div>
        <div id="mcList"></div>
      </div>

      <div class="sect">
        <h3>加 / 改一条</h3>
        <div class="fields" style="grid-template-columns:1fr 140px">
          <div><label>名字</label><input id="mcName" placeholder="filesystem"></div>
          <div><label>传输</label><select id="mcTr"><option value="stdio">stdio</option><option value="http">http</option></select></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>命令（stdio）</label><input id="mcCmd" class="mono" placeholder="npx -y @modelcontextprotocol/server-filesystem /data"></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>URL（http）</label><input id="mcURL" class="mono" placeholder="https://.../mcp"></div>
        </div>
        <div class="bar tight">
          <label class="sub" style="margin:0"><input type="checkbox" id="mcOn" checked style="margin-right:6px">启用</label>
        </div>
        <div class="bar">
          <button class="btn" id="mcSave">保存</button>
          <span class="sub" id="mcMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>手动调一个工具</h3>
        <div class="fields" style="grid-template-columns:1fr 1fr">
          <div><label>服务</label><input id="mcCallServer" placeholder="filesystem"></div>
          <div><label>工具名</label><input id="mcCallTool" placeholder="list_directory"></div>
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>参数（JSON）</label><input id="mcCallArgs" class="mono" placeholder='{"path":"/data"}'></div>
        </div>
        <div class="bar tight">
          <button class="btn ghost sm" id="mcCall">调用</button>
          <span class="sub" id="mcCallMsg" style="margin:0"></span>
        </div>
        <div id="mcCallOut"><div class="empty">还没调用过</div></div>
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
          <button class="btn ghost sm" data-reload="${esc(s.name)}">重连</button>
          <button class="btn ghost sm danger" data-rm="${esc(s.name)}">删除</button>
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
/* 数据：GET /api/agent/skills → {skills[{...Skill, slug}]}
        Skill：name(展示名，可中文)、description、body、triggers[]、tokens；slug = 技能目录名（ASCII）
   动作：POST /api/agent/skills {action: save|patch|writeFile|delete, name(slug 或展示名都行), category, content}
   说明：与白泽自己的 skill_manage 是同一套实现，所以这里建的技能它立刻能用；删除是归档不是硬删。 */
async function renderSkills(root) {
  root.innerHTML = `
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>技能库 <span class="count" id="skN"></span></h3>
        <div class="sub">技能是白泽自己攒的「操作手册」。这里也能建 / 改 / 删 —— 用的是和它自己同一套实现。</div>
        <div id="skList"></div>
      </div>

      <div class="sect">
        <h3 id="skFormTitle">新建技能</h3>
        <div class="sub">目录名（slug）与分类都只能是 ASCII 小写字母 / 数字 / <span class="mono">-._</span>，且以字母或数字开头（决定落盘目录）；
          展示名写在正文的 <span class="mono">name:</span> 里，可以中文。</div>
        <div class="fields" style="grid-template-columns:180px 150px 1fr">
          <div><label>目录名（slug）</label><input id="skSlug" placeholder="daily-report"></div>
          <div><label>分类（可空，如 report）</label><input id="skCat" placeholder="report"></div>
          <div><label>提示</label><span class="sub" style="margin:0">删除会归档到 .archive，不硬删</span></div>
        </div>
        <label>SKILL.md 全文（必须带 front-matter，末尾要有正文）</label>
        <textarea id="skBody" class="mono" style="min-height:160px" placeholder="---&#10;name: 每日汇报&#10;description: 把昨天的运行记录归纳成三点&#10;---&#10;&#10;1. 先读昨天的运行记录&#10;2. 归纳成三点"></textarea>
        <div style="display:flex;gap:10px;align-items:center;margin-top:8px">
          <button class="btn" id="skSave">保存</button>
          <button class="btn ghost" id="skImport">导入 SKILL.md（粘贴）</button>
          <button class="btn ghost" id="skCancel" hidden>取消编辑</button>
          <span class="sub" id="skMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let skills = [];

  const resetForm = () => {
    $i('skSlug').value = ''; $i('skSlug').readOnly = false;
    $i('skCat').value = ''; $i('skBody').value = '';
    $i('skFormTitle').textContent = '新建技能';
    $i('skSave').textContent = '保存';
    $i('skCancel').hidden = true;
  };

  const refresh = async () => {
    const r = await API.get('/api/agent/skills');
    if (!r.ok) { showErr($i('skList'), r); return; }
    skills = (r.data && r.data.skills) || [];
    $i('skN').textContent = skills.length ? `（${skills.length}）` : '';
    // 按分类分组：没有分类（category 为空）的归「未分类」，排在最后
    const groups = new Map();
    skills.forEach(s => {
      const c = (s.category || '').trim() || '未分类';
      if (!groups.has(c)) groups.set(c, []);
      groups.get(c).push(s);
    });
    const cats = Array.from(groups.keys()).sort((a, b) => (a === '未分类' ? 1 : b === '未分类' ? -1 : a.localeCompare(b)));
    const rowOf = s => `
      <div class="row" style="align-items:flex-start">
        <div class="who">
          <b>${esc(s.name || s.slug)}</b>
          <span class="mono">${esc(s.slug)} · ${esc(s.tokens || 0)} token${(s.triggers || []).length ? ' · 触发词 ' + esc((s.triggers || []).join(' / ')) : ''}</span>
          <div class="sub" style="margin:4px 0 0">${esc(s.description || '')}</div>
          <details><summary class="sub" style="cursor:pointer;margin:4px 0 0">看正文</summary>
            <div class="pre" style="max-height:220px;overflow:auto">${esc(s.body || '')}</div></details>
        </div>
        <div class="tags">
          ${s.category ? `<span class="tag">${esc(s.category)}</span>` : ''}
          <button class="btn ghost sm" data-edit="${esc(s.slug)}">编辑</button>
          <button class="btn ghost sm danger" data-del="${esc(s.slug)}">删除</button>
        </div>
      </div>`;
    $i('skList').innerHTML = skills.length ? cats.map(c =>
      `<div class="shelf-head">${esc(c)} <span class="tag">${groups.get(c).length}</span></div>` +
      groups.get(c).map(rowOf).join('')
    ).join('') : '<div class="empty">还没有技能（可以让白泽自己攒，也可以在这里建）</div>';

    $i('skList').querySelectorAll('[data-edit]').forEach(b => b.onclick = () => {
      const s = skills.find(x => x.slug === b.dataset.edit);
      if (!s) return;
      // 后端只存了 front-matter 解析后的字段，这里按它拼回一份 SKILL.md 供编辑
      const fm = ['---', 'name: ' + (s.name || ''), 'description: ' + (s.description || '')];
      if ((s.triggers || []).length) fm.push('triggers: ' + s.triggers.join(', '));
      fm.push('---', '', s.body || '');
      $i('skSlug').value = s.slug; $i('skSlug').readOnly = true;
      $i('skBody').value = fm.join('\n');
      $i('skFormTitle').textContent = '编辑技能：' + s.slug;
      $i('skSave').textContent = '保存修改';
      $i('skCancel').hidden = false;
      $i('skMsg').textContent = '';
      $i('skBody').focus();
    });
    $i('skList').querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
      if (!confirm('删除技能「' + b.dataset.del + '」？（归档到 .archive，不是硬删）')) return;
      const rr = await API.post('/api/agent/skills', { action: 'delete', name: b.dataset.del });
      if (!rr.ok) { Shell.toast('删除失败：' + rr.error, 'err'); return; }
      Shell.toast('已归档', 'ok');
      await refresh();
    });
  };

  $i('skCancel').onclick = resetForm;
  $i('skSave').onclick = async () => {
    const slug = $i('skSlug').value.trim();
    const content = $i('skBody').value;
    if (!slug) { $i('skMsg').textContent = '目录名不能为空'; return; }
    if (!content.trim()) { $i('skMsg').textContent = '正文不能为空'; return; }
    $i('skMsg').textContent = '保存中…';
    const body = { action: 'save', name: slug, content: content };
    const cat = $i('skCat').value.trim();
    if (cat) body.category = cat;
    const r = await API.post('/api/agent/skills', body);
    if (!r.ok) { $i('skMsg').textContent = '失败：' + r.error; return; }
    $i('skMsg').textContent = '已保存（白泽立刻能用）';
    Shell.toast('技能已保存', 'ok');
    resetForm();
    await refresh();
  };

  // 导入 SKILL.md：与「保存」共用表单里的 slug / 分类 / 正文，只是走 action=import
  $i('skImport').onclick = async () => {
    const slug = $i('skSlug').value.trim();
    const content = $i('skBody').value;
    if (!slug) { $i('skMsg').textContent = '目录名不能为空'; return; }
    if (!content.trim()) { $i('skMsg').textContent = '正文不能为空'; return; }
    $i('skMsg').textContent = '导入中…';
    const body = { action: 'import', name: slug, files: [{ path: 'SKILL.md', content: content }] };
    const cat = $i('skCat').value.trim();
    if (cat) body.category = cat;
    const r = await API.post('/api/agent/skills', body);
    if (!r.ok) { $i('skMsg').textContent = '失败：' + r.error; return; }
    $i('skMsg').textContent = '已导入（白泽立刻能用）';
    Shell.toast('技能已导入', 'ok');
    resetForm();
    await refresh();
  };

  await refresh();
}

/* ================= 6.5 人设 ================= */
/* 数据：GET /api/agent/persona → {enabled, dir, files[{name,enabled,exists,tokens,preview,builtin}],
        builtin[], tokens, promptPreview}
        读单文件：GET /api/agent/persona/file?name=xx.md
   动作：POST /api/agent/persona {action: save|enable|disable|order|archive|reset|master, ...}
   说明：人设每次运行现读（热重载），保存后下一句话就生效，不用重启。 */
async function renderPersona(root) {
  root.innerHTML = `
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>人设 <span class="sub" id="peMaster" style="margin:0"></span></h3>
        <div class="sub">决定白泽「是谁、按什么规矩办事」的一组 Markdown 文件，按顺序整篇拼进系统提示（顺序 = 由上到下）。
          改完保存立即生效，不用重启。</div>
        <div class="sub mono" id="peDir" style="margin:0 0 8px"></div>
        <div id="peList"></div>
        <div class="fields" style="grid-template-columns:240px 1fr">
          <div><label>新建文件</label><input id="peNewName" class="mono" placeholder="RULES.md"></div>
          <div style="display:flex;align-items:flex-end;gap:8px">
            <button class="btn ghost" id="peNew">新建（默认不启用）</button>
            <button class="btn ghost" id="peMasterBtn">人设总开关</button>
          </div>
        </div>
      </div>

      <div class="sect">
        <h3 id="peEdTitle">编辑</h3>
        <div class="sub" id="peEdHint">点上面任意文件的「编辑」，或新建一个。</div>
        <textarea id="peBody" class="mono" style="min-height:220px;width:100%" placeholder="Markdown 正文…" hidden></textarea>
        <div style="display:flex;gap:10px;align-items:center;margin-top:8px">
          <button class="btn" id="peSave" hidden>保存</button>
          <button class="btn ghost" id="peCancel" hidden>取消</button>
          <span class="sub" id="peMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>拼装预览 <span class="sub" id="peTokens" style="margin:0"></span></h3>
        <div class="sub">白泽实际读到的开头（截断显示）。没启用的文件不会出现在这里。</div>
        <div class="pre" id="pePreview" style="max-height:260px;overflow:auto"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let files = [];
  let edit = null; // 正在编辑的文件 {name, content}

  const paintEdit = () => {
    const box = $i('peBody'), save = $i('peSave'), cancel = $i('peCancel');
    if (!edit) {
      $i('peEdTitle').textContent = '编辑';
      $i('peEdHint').textContent = '点上面任意文件的「编辑」，或新建一个。';
      $i('peEdHint').hidden = false;
      box.hidden = true; save.hidden = true; cancel.hidden = true;
      return;
    }
    $i('peEdTitle').textContent = '编辑：' + edit.name;
    $i('peEdHint').textContent = '支持 front-matter（会被自动去掉）；心跳段用 <!-- heartbeat:start/end --> 包起来，心跳没启用时整段不会进提示。';
    $i('peEdHint').hidden = false;
    box.hidden = false; save.hidden = false; cancel.hidden = false;
    box.value = edit.content;
  };

  const refresh = async () => {
    const r = await API.get('/api/agent/persona');
    if (!r.ok) { showErr($i('peList'), r); return; }
    const d = r.data || {};
    files = d.files || [];
    $i('peMaster').innerHTML = d.enabled ? '<span class="tag on">已启用</span>' : '<span class="tag off">已停用</span>';
    $i('peDir').textContent = '目录：' + (d.dir || '');
    $i('peTokens').textContent = d.tokens ? `（约 ${d.tokens} token）` : '';
    $i('pePreview').textContent = d.promptPreview || (d.enabled ? '（没有任何启用的文件，人设不注入）' : '（人设已停用，不注入）');
    const enabledOrder = files.filter(f => f.enabled).map(f => f.name);
    $i('peList').innerHTML = files.length ? files.map(f => `
      <div class="row" style="align-items:flex-start">
        <div class="who">
          <b class="mono">${esc(f.name)} ${f.builtin ? '<span class="tag">模板</span>' : ''}
            ${f.enabled ? '<span class="tag on">加载中</span>' : '<span class="tag off">未启用</span>'}
            ${f.exists ? '' : '<span class="tag warn">文件缺失</span>'}</b>
          <span class="mono">${f.tokens || 0} token${f.enabled ? ' · 第 ' + (enabledOrder.indexOf(f.name) + 1) + ' 个拼进去' : ''}</span>
          <div class="sub" style="margin:4px 0 0">${esc(f.preview || '')}</div>
        </div>
        <div class="tags">
          <button class="btn ghost sm" data-edit="${esc(f.name)}">编辑</button>
          <button class="btn ghost sm" data-toggle="${esc(f.name)}">${f.enabled ? '停用' : '启用'}</button>
          <button class="btn ghost sm" data-up="${esc(f.name)}" ${f.enabled ? '' : 'disabled'}>↑</button>
          <button class="btn ghost sm" data-down="${esc(f.name)}" ${f.enabled ? '' : 'disabled'}>↓</button>
          ${f.builtin ? `<button class="btn ghost sm" data-reset="${esc(f.name)}">恢复默认</button>` : ''}
          <button class="btn ghost sm" data-arch="${esc(f.name)}">归档</button>
        </div>
      </div>`).join('') + '<div class="sub" style="margin-top:8px">未启用的文件不会进系统提示；「归档」会移进 .archive，不硬删。</div>'
      : '<div class="empty">还没有人设文件</div>';

    $i('peList').querySelectorAll('[data-edit]').forEach(b => b.onclick = async () => {
      const rr = await API.get('/api/agent/persona/file?name=' + encodeURIComponent(b.dataset.edit));
      if (!rr.ok) { Shell.toast('读取失败：' + rr.error, 'err'); return; }
      edit = { name: rr.data.name, content: rr.data.content || '' };
      paintEdit();
      $i('peMsg').textContent = '';
    });
    $i('peList').querySelectorAll('[data-toggle]').forEach(b => b.onclick = async () => {
      const f = files.find(x => x.name === b.dataset.toggle);
      const r2 = await API.post('/api/agent/persona', { action: f && f.enabled ? 'disable' : 'enable', name: b.dataset.toggle });
      if (!r2.ok) { Shell.toast('失败：' + r2.error, 'err'); return; }
      await refresh();
    });
    $i('peList').querySelectorAll('[data-up],[data-down]').forEach(b => b.onclick = async () => {
      const name = b.dataset.up || b.dataset.down;
      const order = enabledOrder.slice();
      const i = order.indexOf(name);
      const j = b.dataset.up ? i - 1 : i + 1;
      if (i < 0 || j < 0 || j >= order.length) return;
      order[i] = order[j]; order[j] = name;
      const r2 = await API.post('/api/agent/persona', { action: 'order', files: order });
      if (!r2.ok) { Shell.toast('失败：' + r2.error, 'err'); return; }
      await refresh();
    });
    $i('peList').querySelectorAll('[data-reset]').forEach(b => b.onclick = async () => {
      if (!confirm('把「' + b.dataset.reset + '」恢复成内置模板？你的修改会丢。')) return;
      const r2 = await API.post('/api/agent/persona', { action: 'reset', name: b.dataset.reset });
      if (!r2.ok) { Shell.toast('失败：' + r2.error, 'err'); return; }
      Shell.toast('已恢复默认', 'ok');
      await refresh();
    });
    $i('peList').querySelectorAll('[data-arch]').forEach(b => b.onclick = async () => {
      if (!confirm('归档「' + b.dataset.arch + '」？（移进 .archive，不硬删）')) return;
      const r2 = await API.post('/api/agent/persona', { action: 'archive', name: b.dataset.arch });
      if (!r2.ok) { Shell.toast('失败：' + r2.error, 'err'); return; }
      if (edit && edit.name === b.dataset.arch) { edit = null; paintEdit(); }
      Shell.toast('已归档', 'ok');
      await refresh();
    });
  };

  $i('peNew').onclick = () => {
    const name = $i('peNewName').value.trim();
    if (!name) { Shell.toast('先填文件名（如 RULES.md）', 'err'); return; }
    edit = { name: name, content: '# ' + name + '\n\n' };
    paintEdit();
    $i('peMsg').textContent = '新文件默认不启用：保存后回到列表里点「启用」才生效';
  };
  $i('peMasterBtn').onclick = async () => {
    const cur = $i('peMaster').textContent.indexOf('已启用') >= 0;
    const r = await API.post('/api/agent/persona', { action: 'master', enabled: !cur });
    if (!r.ok) { Shell.toast('失败：' + r.error, 'err'); return; }
    Shell.toast(!cur ? '人设已启用' : '人设已停用', 'ok');
    await refresh();
  };
  $i('peCancel').onclick = () => { edit = null; paintEdit(); $i('peMsg').textContent = ''; };
  $i('peSave').onclick = async () => {
    if (!edit) return;
    const content = $i('peBody').value;
    $i('peMsg').textContent = '保存中…';
    const r = await API.post('/api/agent/persona', { action: 'save', name: edit.name, content: content });
    if (!r.ok) { $i('peMsg').textContent = '失败：' + r.error; return; }
    $i('peMsg').textContent = '';
    Shell.toast('人设已保存（立即生效）', 'ok');
    edit = null; paintEdit();
    await refresh();
  };

  paintEdit();
  await refresh();
}

/* ================= 7. 定时任务 ================= */
/* 数据：GET /api/agent/cron → {jobs[{...CronJob, nextAt, parseError}]}
        CronJob：id、expr、goal、recipe、enabled（外加 nextAt / parseError / runs / lastStatus / lastError）
   动作：POST /api/agent/cron {action: add|update|toggle|remove, id, expr, goal, recipe, enabled}
   说明：调度器每 20 秒重读一次配置，所以改完最多 20 秒生效。 */
async function renderCron(root) {
  root.innerHTML = `
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>定时任务 <span class="count" id="crN"></span></h3>
        <div class="sub">到点让白泽自动干一件事。表达式是标准 5 段 cron（分 时 日 月 周），例如 <span class="mono">0 8 * * *</span>；改完最多 20 秒生效。</div>
        <div id="crList"></div>
      </div>

      <div class="sect">
        <h3 id="crFormTitle">加一条</h3>
        <div class="fields" style="grid-template-columns:170px 1fr 120px">
          <div><label>cron 表达式</label><input id="crExpr" class="mono" placeholder="0 8 * * *"></div>
          <div><label>要干什么</label><input id="crGoal" placeholder="总结昨天的工作记录并写成一条记忆"></div>
          <div><label>配方</label><input id="crRecipe" placeholder="chat"></div>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="btn" id="crSave">添加</button>
          <button class="btn ghost" id="crCancel" hidden>取消编辑</button>
          <span class="sub" id="crMsg" style="margin:0"></span>
        </div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let jobs = [];

  const resetForm = () => {
    $i('crExpr').value = ''; $i('crGoal').value = ''; $i('crRecipe').value = '';
    $i('crFormTitle').textContent = '加一条';
    $i('crSave').textContent = '添加';
    $i('crCancel').hidden = true;
    delete $i('crSave').dataset.id;
  };

  const refresh = async () => {
    const r = await API.get('/api/agent/cron');
    if (!r.ok) { showErr($i('crList'), r); return; }
    jobs = (r.data && r.data.jobs) || [];
    $i('crN').textContent = jobs.length ? `（${jobs.length}）` : '';
    $i('crList').innerHTML = jobs.length ? jobs.map(j => `
      <div class="row">
        <div class="who"><b class="mono">${esc(j.expr || '')}</b>
          <span>${esc(j.goal || '')}</span>
          <span class="mono">${esc(j.recipe || '')} · ${esc(j.id || '')}${j.nextAt ? ' · 下次 ' + fmtTime(j.nextAt) : ''}${j.runs ? ' · 已跑 ' + esc(j.runs) + ' 次' : ''}${j.lastStatus ? ' · 上次 ' + esc(j.lastStatus) : ''}</span>
          ${j.parseError ? `<span class="mono" style="color:var(--danger)">表达式解析失败：${esc(j.parseError)}</span>` : ''}
          ${j.lastError ? `<span class="mono" style="color:var(--danger)">上次错误：${esc(j.lastError)}</span>` : ''}</div>
        <div class="tags">
          <span class="tag ${j.enabled ? 'on' : 'off'}">${j.enabled ? '启用中' : '已停用'}</span>
          <button class="btn ghost sm" data-toggle="${esc(j.id)}" data-on="${j.enabled ? '1' : '0'}">${j.enabled ? '停用' : '启用'}</button>
          <button class="btn ghost sm" data-edit="${esc(j.id)}">编辑</button>
          <button class="btn ghost sm danger" data-del="${esc(j.id)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有定时任务</div>';

    $i('crList').querySelectorAll('[data-toggle]').forEach(b => b.onclick = async () => {
      const rr = await API.post('/api/agent/cron', { action: 'toggle', id: b.dataset.toggle, enabled: b.dataset.on !== '1' });
      if (!rr.ok) { Shell.toast('操作失败：' + rr.error, 'err'); return; }
      await refresh();
    });
    $i('crList').querySelectorAll('[data-edit]').forEach(b => b.onclick = () => {
      const j = jobs.find(x => x.id === b.dataset.edit) || {};
      $i('crExpr').value = j.expr || ''; $i('crGoal').value = j.goal || ''; $i('crRecipe').value = j.recipe || '';
      $i('crFormTitle').textContent = '编辑定时任务：' + (j.id || '');
      $i('crSave').textContent = '保存修改';
      $i('crSave').dataset.id = j.id || '';
      $i('crCancel').hidden = false;
      $i('crMsg').textContent = '';
    });
    $i('crList').querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
      if (!confirm('删除这条定时任务？')) return;
      const rr = await API.post('/api/agent/cron', { action: 'remove', id: b.dataset.del });
      if (!rr.ok) { Shell.toast('删除失败：' + rr.error, 'err'); return; }
      Shell.toast('已删除', 'ok');
      await refresh();
    });
  };

  $i('crCancel').onclick = resetForm;
  $i('crSave').onclick = async () => {
    const expr = $i('crExpr').value.trim(), goal = $i('crGoal').value.trim();
    if (!expr || !goal) { $i('crMsg').textContent = '表达式和目标都要填'; return; }
    const id = $i('crSave').dataset.id;
    const body = { action: id ? 'update' : 'add', expr: expr, goal: goal };
    if (id) body.id = id;
    const rc = $i('crRecipe').value.trim();
    if (rc) body.recipe = rc;
    $i('crMsg').textContent = '提交中…';
    const r = await API.post('/api/agent/cron', body);
    if (!r.ok) { $i('crMsg').textContent = '失败：' + r.error; return; }
    $i('crMsg').textContent = id ? '已更新' : '已添加';
    Shell.toast(id ? '定时任务已更新' : '定时任务已添加（最多 20 秒生效）', 'ok');
    resetForm();
    await refresh();
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
    <div class="page">
      <div class="sect" style="margin-top:0">
        <h3>新建备份</h3>
        <div class="sub">勾选要备份的内容；恢复前后端会自动做一次安全点。</div>
        <div class="fields" style="grid-template-columns:repeat(auto-fit,minmax(170px,1fr))">
          ${SEL.map(([k, label]) => `<div style="display:flex;align-items:center;gap:6px">
            <input type="checkbox" data-sel="${k}" checked><span class="sub" style="margin:0">${esc(label)}</span></div>`).join('')}
        </div>
        <div class="fields" style="grid-template-columns:1fr">
          <div><label>备注</label><input id="bkNote" placeholder="例如：换手机前"></div>
        </div>
        <div class="bar">
          <button class="btn" id="bkNew">立刻备份</button>
          <span class="sub" id="bkMsg" style="margin:0"></span>
        </div>
      </div>

      <div class="sect">
        <h3>已有备份 <span class="count" id="bkN"></span></h3>
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
          <button class="btn ghost sm" data-verify="${esc(b.path)}">校验</button>
          <button class="btn ghost sm" data-dry="${esc(b.path)}">预演恢复</button>
          <button class="btn ghost sm danger" data-del="${esc(b.path)}">删除</button>
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
    <div class="page">
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
    $i('acSteps').innerHTML = steps.length ? '<div class="row-head"><span>阶段 · 时间 · 详情</span></div>' + steps.map(st => `
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
   apps.js 里这 10 个只声明了 id/名字/图标/尺寸，render 在这里挂上去。
   为什么不直接在 apps.js 写 render: renderXxx？因为 apps.js 先加载，那一刻这些函数
   还没定义，对象字面量求值会直接 ReferenceError（整个 window.APPS 就废了）。 */
(function wireD2Apps() {
  const map = {
    tasks: renderTasks, kb: renderKB, memory: renderMemory, providers: renderProviders,
    mcp: renderMCP, skills: renderSkills, persona: renderPersona, cron: renderCron,
    backup: renderBackup, activity: renderActivity,
  };
  Object.keys(map).forEach(id => {
    const app = window.APP_BY_ID && window.APP_BY_ID[id];
    if (app) app.render = map[id];
  });
})();
