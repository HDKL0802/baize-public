/* 白泽 · 其他功能（对话与知识库之外的全部功能）
   ─────────────────────────────────────────────
   按后端控制台同一套分组收口：主要 / 资源 / 配置（运行记录归配置、定时任务归资源）。
   能点进去的都是真去后端取数的页面；取不到就把原因写在页面上，绝不装作"空数据"。

   注意：/api/be/api/xxx 是手机内核的通用透传口（转到后端 /api/xxx），
   因为本机内核自己占着 /api/state，所以设备 / 任务 / 日志这几条必须绕这个前缀走。 */
'use strict';
window.More = (function () {
  const $ = (id) => document.getElementById(id);

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    }[c]));
  }
  function toast(m, ms) { try { UI.toast(m, ms); } catch (e) { /* 忽略 */ } }

  function dev() { return window.BzDevice || null; }
  function ready() { const d = dev(); return !!(d && d.available && d.available()); }
  function call(path, method, body) {
    if (!ready()) return { error: '当前入口不是手机 App（网页版没有本机内核），这些数据都在 NAS 后端上' };
    return dev().call(path, method || 'GET', body == null ? null : body) || { error: '内核没有响应' };
  }
  function errOf(r) {
    if (!r) return '没有响应';
    if (r.ok === false) return r.error || '未知错误';
    if (r.ok === undefined && r.error) return r.error;
    return '';
  }
  function fmtTime(ts) {
    if (!ts) return '—';
    const d = new Date(ts), p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  function fmtSize(n) {
    if (!n) return '0KB';
    if (n > 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + 'MB';
    if (n > 1024) return Math.round(n / 1024) + 'KB';
    return n + 'B';
  }
  function card(html) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    el.innerHTML = html;
    return el;
  }
  function empty(box, msg) {
    box.innerHTML = '<div class="empty-tip">' + esc(msg) + '</div>';
  }
  function fail(box, why) {
    box.innerHTML = '<div class="empty-tip">读不到：' + esc(why) + '</div>';
  }

  /* ================= 功能目录 ================= */

  const GROUPS = [
    {
      name: '主要', items: [
        { id: 'chat', icon: '💬', name: '对话', desc: '跟白泽智能体说话，一句话派活', go: () => UI.switchTab('view-baize') },
        { id: 'kb', icon: '📚', name: '知识库', desc: '待办 / 密码本 / 附件 / 记忆的正本', go: () => UI.switchTab('view-kb') },
      ],
    },
    {
      name: '资源', items: [
        { id: 'devices', icon: '🖥️', name: '设备', desc: '电脑 / 手机 / NAS 注册上来的执行端' },
        { id: 'tasks', icon: '📋', name: '任务与审批', desc: '危险动作要人工放行后才真的执行' },
        { id: 'memory', icon: '🧠', name: '长期记忆', desc: '任务结论自动落盘，动手前先检索', go: () => UI.switchTab('view-memory') },
        { id: 'skills', icon: '📦', name: '技能', desc: '工作目录里的可复用技能包' },
        { id: 'cron', icon: '⏰', name: '定时任务', desc: 'cron 表达式 + 一句目标' },
      ],
    },
    {
      name: '配置', items: [
        { id: 'approve', icon: '🛡️', name: '模型审批', desc: '危险动作人工放行 + 审批超时', go: () => UI.switchTab('view-approval') },
        { id: 'apikeys', icon: '🔑', name: 'API 服务', desc: '各模型站的 API Key 与 baseURL', go: () => UI.switchTab('view-keys') },
        { id: 'voice', icon: '🎙️', name: '语音', desc: '说得出话（朗读）+ 听得懂话（按住说话）' },
        { id: 'mcp', icon: '🔌', name: 'MCP 服务', desc: '热插拔，加删重载都不用重启' },
        { id: 'backups', icon: '🗄️', name: '备份与恢复', desc: 'zip + sha256 清单，恢复前自动打安全点' },
        { id: 'runs', icon: '🧾', name: '运行记录', desc: '每次派活的步骤、工具链路与用量' },
        { id: 'logs', icon: '📜', name: '日志', desc: '后端运行日志（倒序）' },
        { id: 'settings', icon: '⚙️', name: '设置', desc: '跨端 / 通知 / 运行参数', go: () => UI.switchTab('view-settings') },
      ],
    },
  ];

  function render() {
    const box = $('moreBody');
    box.innerHTML = '';
    GROUPS.forEach((g) => {
      const title = document.createElement('h3');
      title.className = 'group-title';
      title.style.margin = '16px 0 10px';
      title.textContent = g.name;
      box.appendChild(title);
      g.items.forEach((it) => {
        const b = document.createElement('button');
        b.className = 'link-item';
        b.innerHTML = `
          <div class="li-main">
            <span class="li-icon">${it.icon}</span>
            <div style="min-width:0">
              <div class="li-name">${esc(it.name)}</div>
              <div class="li-desc">${esc(it.desc)}</div>
            </div>
          </div>
          <span class="li-go">›</span>`;
        b.addEventListener('click', () => {
          if (it.go) { it.go(); return; }
          open(it.id, it.name);
        });
        box.appendChild(b);
      });
    });
  }

  /* ================= 各个子页 ================= */

  function open(id, name) {
    const fn = PAGES[id];
    if (!fn) { toast('这一项还没接上'); return; }
    UI.openTool(name, '<div class="desc">读取中…</div>', (body) => {
      fn(body).catch((e) => { fail(body, e && e.message ? e.message : String(e)); });
    });
  }

  const PAGES = {
    /* ---- 资源 · 设备 ---- */
    devices: async (body) => {
      const r = call('/api/be/api/state?tasks=1&records=1&memories=1', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const devs = Array.isArray(r.devices) ? r.devices : [];
      const main = r.self || {};
      const kb = r.kb || {};
      const head = document.createElement('div');
      head.className = 'desc';
      head.textContent = `后端跑在 ${main.hostname || '未知主机'}（${main.os || ''} ${main.arch || ''} · v${main.version || '?'}）`
        + ` · 已注册 ${devs.length} 台设备 · 知识库 ${kb.todos || 0} 条待办 / ${kb.passwords || 0} 条密码 / ${kb.files || 0} 个附件`;
      body.appendChild(head);
      if (!devs.length) { empty(body, '还没有设备注册上来'); return; }
      devs.forEach((d) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(d.name || d.hostname || d.id)}</div>
            <span class="kb-chip ${d.online ? 'ok' : ''}">${d.online ? '在线' : '离线'}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${esc(d.os || '')} ${esc(d.arch || '')} · ${esc(d.id)}</div>
          <div class="vc-line">最后在线 ${fmtTime(d.lastSeen)}${d.remoteAddr ? ' · ' + esc(d.remoteAddr) : ''}</div>
          <div class="vc-line">能力 ${esc((d.caps || []).join(' / ') || '（未上报）')} · 版本 ${esc(d.version || '—')}</div>`));
      });
    },

    /* ---- 资源 · 任务与审批 ---- */
    tasks: async (body) => {
      const r = call('/api/be/api/state?tasks=50', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const tasks = Array.isArray(r.tasks) ? r.tasks : [];
      const pending = tasks.filter(t => t.needApproval && t.status === 'pending_approval');
      const t1 = document.createElement('h3');
      t1.className = 'group-title';
      t1.style.margin = '0 0 10px';
      t1.textContent = `待审批（${pending.length}）`;
      body.appendChild(t1);
      if (!pending.length) {
        body.appendChild(card('<div class="vc-sub">没有在等人工放行的跨端任务</div>'));
      } else {
        pending.forEach((t) => {
          const el = card(`
            <div class="kb-head-row">
              <div class="vc-title">${esc(t.action)}</div>
              <span class="kb-chip warn">待审批</span>
            </div>
            <div class="vc-sub" style="margin-top:4px">${esc(t.deviceId || '')} · ${fmtTime(t.createdAt)} · 来自 ${esc(t.origin || 'agent')}</div>
            <div class="kb-code" style="margin-top:6px">${esc(JSON.stringify(t.args || {}).slice(0, 400))}</div>
            <div class="vc-actions">
              <button class="btn primary sm" data-ok style="flex:1">放行</button>
              <button class="btn danger-ghost sm" data-no style="flex:1">驳回</button>
            </div>`);
          el.querySelector('[data-ok]').addEventListener('click', (e) => {
            e.currentTarget.disabled = true;
            const rr = call('/api/be/api/tasks/' + encodeURIComponent(t.id) + '/approve?by=' + encodeURIComponent('手机'), 'POST', null);
            const w = errOf(rr);
            if (w) { toast('没放行成功：' + w, 3600); e.currentTarget.disabled = false; return; }
            toast('已放行');
            open('tasks', '任务与审批');
          });
          el.querySelector('[data-no]').addEventListener('click', (e) => {
            e.currentTarget.disabled = true;
            const rr = call('/api/be/api/tasks/' + encodeURIComponent(t.id) + '/reject?by=' + encodeURIComponent('手机'), 'POST', null);
            const w = errOf(rr);
            if (w) { toast('没驳回成功：' + w, 3600); e.currentTarget.disabled = false; return; }
            toast('已驳回');
            open('tasks', '任务与审批');
          });
          body.appendChild(el);
        });
      }
      const t2 = document.createElement('h3');
      t2.className = 'group-title';
      t2.style.margin = '18px 0 10px';
      t2.textContent = `最近任务（${tasks.length}）`;
      body.appendChild(t2);
      if (!tasks.length) {
        body.appendChild(card('<div class="vc-sub">还没有跨端任务</div>'));
        return;
      }
      const statusName = {
        pending_approval: '待审批', pending: '待派发', dispatched: '已派发',
        done: '完成', failed: '失败', canceled: '已取消',
      };
      tasks.forEach((t) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(t.action)}</div>
            <span class="kb-chip ${t.status === 'done' ? 'ok' : t.status === 'failed' ? 'danger' : ''}">${esc(statusName[t.status] || t.status)}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${esc(t.deviceId || '')} · ${fmtTime(t.createdAt)}</div>
          ${t.error ? `<div class="vc-line" style="color:var(--rose)">${esc(t.error)}</div>` : ''}`));
      });
    },

    /* ---- 配置 · 运行记录 ---- */
    runs: async (body) => {
      const r = call('/api/agent/runs?limit=30', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const runs = Array.isArray(r.runs) ? r.runs : [];
      if (!runs.length) { empty(body, '还没有运行记录'); return; }
      runs.forEach((x) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(x.goal || '(无目标)')}</div>
            <span class="kb-chip ${x.status === 'done' ? 'ok' : 'danger'}">${x.status === 'done' ? '完成' : '失败'}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${fmtTime(x.startedAt)} · ${x.steps || 0} 步 · 工具 ${x.toolCalls || 0} 次 · token ${(x.promptTokens || 0) + (x.outTokens || 0)}</div>
          ${x.text ? `<div class="kb-body-text">${esc(String(x.text).slice(0, 400))}</div>` : ''}
          ${x.error ? `<div class="vc-line" style="color:var(--rose)">${esc(x.error)}</div>` : ''}`));
      });
    },

    /* ---- 资源 · 技能 ---- */
    skills: async (body) => {
      const r = call('/api/agent/state', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const list = Array.isArray(r.skills) ? r.skills : [];
      if (!list.length) { empty(body, '技能库是空的（把技能放进后端工作目录的 skills 里）'); return; }
      list.forEach((s) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(s.name)}</div>
            <span class="kb-chip">${s.tokens ? s.tokens + ' token' : '技能'}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${esc(s.description || '（没有描述）')}</div>
          ${(s.triggers || []).length ? `<div class="vc-line">触发词：${esc(s.triggers.join(' / '))}</div>` : ''}
          <div class="kb-code" style="margin-top:6px">${esc(s.path || '')}</div>`));
      });
    },

    /* ---- 资源 · 定时任务 ---- */
    cron: async (body) => {
      const r = call('/api/agent/state', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const jobs = Array.isArray(r.cron) ? r.cron : [];
      if (!jobs.length) {
        body.appendChild(card('<div class="vc-sub">还没有定时任务</div>'));
      }
      jobs.forEach((c) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(c.goal || '(无目标)')}</div>
            <span class="kb-chip ${c.enabled ? 'ok' : ''}">${c.enabled ? '启用' : '停用'}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px"><code>${esc(c.expr || '')}</code> · 上次 ${c.lastRunAt ? fmtTime(c.lastRunAt) : '—'} · 下次 ${c.nextAt ? fmtTime(c.nextAt) : '—'}</div>
          ${c.parseError ? `<div class="vc-line" style="color:var(--rose)">cron 表达式有问题：${esc(c.parseError)}</div>` : ''}`));
      });
      const add = document.createElement('button');
      add.className = 'btn primary';
      add.style.width = '100%';
      add.style.marginTop = '14px';
      add.textContent = '＋ 新增定时任务';
      add.addEventListener('click', () => openToolForm());
      body.appendChild(add);
    },

    /* ---- 配置 · MCP 服务 ---- */
    mcp: async (body) => {
      const r = call('/api/agent/mcp', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const list = Array.isArray(r.servers) ? r.servers : [];
      if (!list.length) { empty(body, '还没有配 MCP 服务（在后端控制台的「MCP 服务」里加）'); return; }
      const stName = { connected: '已连接', connecting: '连接中', error: '出错', disabled: '已停用' };
      list.forEach((s) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(s.name)}</div>
            <span class="kb-chip ${s.status === 'connected' ? 'ok' : s.status === 'error' ? 'danger' : ''}">${esc(stName[s.status] || s.status)}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${esc(s.transport || '')} · 工具 ${s.toolCount || 0} 个${s.latencyMs ? ' · ' + s.latencyMs + 'ms' : ''}</div>
          ${s.error ? `<div class="vc-line" style="color:var(--rose)">${esc(s.error)}</div>` : ''}
          ${(s.tools || []).length ? `<div class="vc-line">${esc(s.tools.slice(0, 20).join(' / '))}</div>` : ''}`));
      });
    },

    /* ---- 配置 · 备份与恢复 ---- */
    backups: async (body) => {
      const r = call('/api/agent/backups', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';
      const list = Array.isArray(r.backups) ? r.backups : [];
      const btn = document.createElement('button');
      btn.className = 'btn primary';
      btn.style.width = '100%';
      btn.textContent = '立即备份一次';
      btn.addEventListener('click', () => {
        btn.disabled = true;
        const rr = call('/api/agent/backups', 'POST', JSON.stringify({ note: '手机端手动备份' }));
        btn.disabled = false;
        const w = errOf(rr);
        if (w) { toast('备份失败：' + w, 4000); return; }
        toast('备份完成');
        open('backups', '备份与恢复');
      });
      body.appendChild(btn);
      if (!list.length) {
        const p = document.createElement('div');
        p.className = 'empty-tip';
        p.textContent = '还没有备份';
        body.appendChild(p);
        return;
      }
      list.forEach((b) => {
        body.appendChild(card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(b.name || b.path)}</div>
            <span class="kb-chip">${fmtSize(b.size)}</span>
          </div>
          ${b.error ? `<div class="vc-line" style="color:var(--rose)">${esc(b.error)}</div>` : ''}
          ${b.manifest && b.manifest.createdAt ? `<div class="vc-sub" style="margin-top:4px">${fmtTime(b.manifest.createdAt)}</div>` : ''}`));
      });
    },

    /* ---- 配置 · 语音 ---- */
    voice: async (body) => {
      const r = call('/api/agent/voice', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      body.innerHTML = '';

      body.appendChild(card(`
        <div class="kb-head-row">
          <div class="vc-title">语音通道</div>
          <span class="kb-chip ${r.ready ? 'ok' : ''}">${r.ready ? '已就绪' : (r.enabled ? '缺配置' : '未启用')}</span>
        </div>
        <div class="vc-sub" style="margin-top:4px">协议 ${esc(r.protocol || '—')} · 合成 ${esc(r.ttsModel || '—')} · 听写 ${esc(r.sttModel || '—')}</div>
        <div class="vc-line">音色 ${esc(r.voice || '—')} · API Key ${r.hasApiKey ? '已配置' : '未配置'}</div>
        ${r.note ? `<div class="vc-line" style="color:var(--rose)">${esc(r.note)}</div>` : ''}`));

      // 音色：点一下试听（真的合成一句念出来）
      const vs = document.createElement('h3');
      vs.className = 'group-title';
      vs.style.margin = '16px 0 10px';
      vs.textContent = `音色（点一下试听并设为默认）`;
      body.appendChild(vs);
      const list = Array.isArray(r.voices) ? r.voices : [];
      if (!list.length) {
        body.appendChild(card('<div class="vc-sub">后端没给音色清单（协议 ' + esc(r.protocol || '?') + '）</div>'));
      }
      list.forEach((v) => {
        const el = card(`
          <div class="kb-head-row">
            <div class="vc-title">${esc(v.name)}　<span class="kb-chip">${esc(v.id)}</span></div>
            <span class="kb-chip ${v.id === r.voice ? 'ok' : ''}">${v.id === r.voice ? '当前默认' : esc(v.gender || '')}</span>
          </div>
          <div class="vc-sub" style="margin-top:4px">${esc(v.desc || '')}</div>
          <div class="vc-actions">
            <button class="btn ghost sm" data-listen style="flex:1">试听</button>
            <button class="btn primary sm" data-set style="flex:1">设为默认音色</button>
          </div>`);
        el.querySelector('[data-listen]').addEventListener('click', (e) => {
          const b = e.currentTarget;
          window.VzVoice.speak('你好，我是白泽，这是音色 ' + v.name, { voice: v.id, btn: b });
        });
        el.querySelector('[data-set]').addEventListener('click', (e) => {
          const b = e.currentTarget;
          b.disabled = true;
          const rr = call('/api/agent/voice/config', 'POST', JSON.stringify({ voice: v.id }));
          b.disabled = false;
          const w = errOf(rr);
          if (w) { toast('没改成：' + w, 4000); return; }
          toast('默认音色已设为 ' + v.name);
          open('voice', '语音');
        });
        body.appendChild(el);
      });

      // 自动朗读开关
      const auto = card(`
        <div class="kb-head-row">
          <div class="vc-title">回复自动朗读</div>
          <span class="kb-chip ${r.autoSpeak ? 'ok' : ''}">${r.autoSpeak ? '已开启' : '已关闭'}</span>
        </div>
        <div class="vc-sub" style="margin-top:4px">打开后，白泽每次回完话都会念出来（这台手机/所有端都按这个开关走，开关存在后端）</div>
        <div class="vc-actions">
          <button class="btn ${r.autoSpeak ? 'ghost' : 'primary'} sm" data-auto style="flex:1">${r.autoSpeak ? '关掉自动朗读' : '打开自动朗读'}</button>
        </div>`);
      auto.querySelector('[data-auto]').addEventListener('click', (e) => {
        const b = e.currentTarget;
        b.disabled = true;
        const rr = call('/api/agent/voice/config', 'POST', JSON.stringify({ autoSpeak: !r.autoSpeak }));
        b.disabled = false;
        const w = errOf(rr);
        if (w) { toast('没改成：' + w, 4000); return; }
        toast(!r.autoSpeak ? '自动朗读已打开' : '自动朗读已关掉');
        open('voice', '语音');
      });
      body.appendChild(auto);

      // 自检：真念一遍再听回来
      const t = document.createElement('h3');
      t.className = 'group-title';
      t.style.margin = '16px 0 10px';
      t.textContent = '自检';
      body.appendChild(t);
      const test = card(`
        <div class="vc-sub">先让后端念一句「白泽语音自检，一二三四五六七」，再把这段音频送回去听写。
        两步都过才说明「说得出话 + 听得懂话」。会真发两次请求，消耗一点点额度。</div>
        <div class="vc-actions"><button class="btn primary sm" data-test style="flex:1">跑一次自检</button></div>
        <div id="vTestOut"></div>`);
      test.querySelector('[data-test]').addEventListener('click', (e) => {
        const b = e.currentTarget;
        b.disabled = true; b.textContent = '自检中…（要真念一遍）';
        const rr = call('/api/agent/voice/test', 'POST', JSON.stringify({}));
        b.disabled = false; b.textContent = '再跑一次自检';
        const out = test.querySelector('#vTestOut');
        const w = errOf(rr);
        if (w) { out.innerHTML = '<div class="vc-line" style="color:var(--rose)">' + esc(w) + '</div>'; return; }
        const tts = rr.tts || {}, stt = rr.stt || {};
        out.innerHTML = '<div class="vc-line">说话：' + (tts.ok ? 'OK（' + (tts.bytes || 0) + ' 字节 / 约 ' + (tts.millis || 0) + ' 毫秒）' : esc(tts.note || '失败')) + '</div>'
          + '<div class="vc-line">听话：' + (stt.ok ? 'OK（听回「' + esc(stt.text || '') + '」）' : esc(stt.note || '失败')) + '</div>';
      });
      body.appendChild(test);
    },

    /* ---- 配置 · 日志 ---- */
    logs: async (body) => {
      const r = call('/api/be/api/logs', 'GET');
      const why = errOf(r);
      if (why) { fail(body, why); return; }
      const lines = (Array.isArray(r.lines) ? r.lines : []).map(l => l.line || l.text || JSON.stringify(l));
      body.innerHTML = '';
      const pre = document.createElement('pre');
      pre.className = 'logbox';
      pre.style.cssText = 'white-space:pre-wrap;word-break:break-all;font-size:11px;line-height:1.6;'
        + 'background:var(--surface-2);border:1px solid var(--line);border-radius:10px;padding:10px;margin:0';
      pre.textContent = lines.slice(-300).join('\n') || '（还没有日志）';
      body.appendChild(pre);
    },
  };

  /** 定时任务的新增表单 */
  function openToolForm() {
    const html = `
      <div class="f-block">
        <label class="f-label">cron 表达式<span class="req">*</span></label>
        <input class="f-input" id="mc-expr" placeholder="0 8 * * *">
        <div class="f-note">五位：分 时 日 月 周。例：0 8 * * * = 每天 08:00。</div>
      </div>
      <div class="f-block">
        <label class="f-label">到点要做什么<span class="req">*</span></label>
        <input class="f-input" id="mc-goal" placeholder="例如：汇总今天的新增待办">
      </div>
      <button class="btn-block" id="mc-save">添加</button>`;
    UI.openTool('新增定时任务', html, () => {
      const save = () => {
        const expr = document.getElementById('mc-expr').value.trim();
        const goal = document.getElementById('mc-goal').value.trim();
        if (!expr || !goal) { toast('表达式和目标都要填'); return; }
        const r = call('/api/agent/config', 'POST', JSON.stringify({ cronExpr: expr, cronGoal: goal }));
        const why = errOf(r);
        if (why) { toast('没加上：' + why, 4000); return; }
        UI.closePage('toolPage');
        toast('已添加定时任务');
        open('cron', '定时任务');
      };
      document.getElementById('mc-save').addEventListener('click', save);
      UI.setToolAction('添加', save);
    });
  }

  function init() {
    render();
    $('btnMoreRefresh').addEventListener('click', () => {
      render();
      toast('已刷新功能列表');
    });
  }

  return { init, render, open };
})();
