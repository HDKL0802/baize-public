/* 白泽桌面端 · 多用户与协作（10. 账号与共享 / 11. 冲突协商）
   这两块对应后端的 accounts / conflicts 两个包。render 在文件末尾回填到 window.APPS。

   要点：
   - 登录会话存在 localStorage，经本机代理（/api/be）以 X-Baize-Session 头透传给后端；
     后端按它把你的记忆库/知识库路由到你自己的分区。
   - 组共享文档是"内容寻址"的：**同名不同内容 = 两份并存（分叉）**，列表按名字归组并标出来。
   - 冲突协商里，两版**都只能看**：这里只做展示，没有任何"改对方那版"的操作。 */

/* base64 → 文本（UTF-8） */
function b64ToText(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  try { return new TextDecoder('utf-8').decode(bytes); } catch (e) { return bin; }
}

/* ================= 10. 账号与共享 ================= */
async function renderAccounts(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <div class="tabs">
        <button data-tab="me" class="on">身份</button>
        <button data-tab="users">用户</button>
        <button data-tab="groups">用户组</button>
        <button data-tab="docs">共享文档</button>
      </div>
      <div id="uaBody"></div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  const body = $i('uaBody');
  let tab = 'me';

  const draw = async () => {
    try {
      if (tab === 'me') await drawMe();
      else if (tab === 'users') await drawUsers();
      else if (tab === 'groups') await drawGroups();
      else await drawDocs();
    } catch (e) {
      body.innerHTML = `<div class="empty err">渲染失败：${esc(e && e.message ? e.message : e)}</div>`;
    }
  };
  root.querySelectorAll('.tabs button').forEach(b => b.onclick = () => {
    tab = b.dataset.tab;
    root.querySelectorAll('.tabs button').forEach(x => x.classList.toggle('on', x === b));
    draw();
  });

  /* ---------- 身份 ---------- */
  const drawMe = async () => {
    body.innerHTML = '<div class="empty">读取中…</div>';
    const r = await API.whoami();
    if (!r.ok) { showErr(body, r); return; }
    const d = r.data || {};

    if (!d.loggedIn) {
      body.innerHTML = `
        <h3>登录</h3>
        <div class="sub">每个账号的数据（记忆库、笔记、知识库）都是<b>各自独立</b>的一份；用户组再加一份组内共享的空间。
          没登录时用的是本机默认库 —— 单人用法完全不变。</div>
        <div class="fields" style="grid-template-columns:1fr 1fr">
          <div><label>用户名</label><input id="lgName" autocomplete="username"></div>
          <div><label>口令</label><input id="lgPwd" type="password" autocomplete="current-password"></div>
        </div>
        <div style="display:flex;gap:8px;align-items:center">
          <button class="btn" id="lgGo">登录</button>
          <span class="sub" id="lgMsg" style="margin:0"></span>
        </div>
        <div class="sect">
          <h3>首次使用</h3>
          <div class="sub">后端第一次启动会自动建一个 <b>admin</b> 账号，随机口令写在 NAS 数据目录的
            <span class="mono">admin-password.txt</span> 里。登录后请尽快去「用户」页签改掉。</div>
        </div>`;
      const go = async () => {
        const name = $i('lgName').value.trim(), pwd = $i('lgPwd').value;
        if (!name || !pwd) { $i('lgMsg').innerHTML = '<span class="err">用户名与口令都要填</span>'; return; }
        $i('lgMsg').textContent = '登录中…';
        const rr = await API.login(name, pwd);
        if (!rr.ok) { $i('lgMsg').innerHTML = `<span class="err">${esc(rr.error || '登录失败')}</span>`; return; }
        const nm = ((rr.data || {}).user || {}).name || name;
        Shell.toast('已登录：' + nm, 'ok');
        await draw();
      };
      $i('lgGo').onclick = go;
      $i('lgPwd').onkeydown = e => { if (e.key === 'Enter') go(); };
      return;
    }

    const u = d.user || {};
    const gs = d.groups || [];
    body.innerHTML = `
      <h3>当前身份</h3>
      <div class="sub">你看到、写入的记忆 / 笔记 / 知识库都是<b>你这一份</b>；组内共享的东西在「共享文档」页签。</div>
      <div class="pre">用户：${esc(u.name || '')}${u.admin ? '（管理员）' : ''}
用户 id：${esc(u.id || '')}
数据分区：${esc(d.principal || '')}
用户组：${gs.length ? esc(gs.map(x => x.name).join('、')) : '（还没加入任何用户组）'}</div>
      <div style="display:flex;gap:8px;margin-top:10px">
        <button class="btn ghost" id="meOut">退出登录</button>
        <span class="sub" id="meMsg" style="margin:0"></span>
      </div>`;
    $i('meOut').onclick = async () => {
      if (!confirm('退出登录？退出后回到本机默认库，看不到你的私人数据（数据还在，登录回来就有）。')) return;
      await API.logout();
      Shell.toast('已退出登录', 'ok');
      await draw();
    };
  };

  /* ---------- 用户 ---------- */
  const drawUsers = async () => {
    body.innerHTML = '<div class="empty">读取中…</div>';
    const r = await API.get('/api/agent/users');
    if (!r.ok) { showErr(body, r); return; }
    const list = (r.data && r.data.users) || [];
    body.innerHTML = `
      <h3>用户</h3>
      <div class="sub">每个用户一份独立数据（记忆库 + 知识库）。删除用户<b>不会删掉他的数据目录</b> ——
        删数据不可逆，留给你在文件层面自己决定。</div>
      <div id="usList"></div>
      <div class="sect">
        <h3>新建用户</h3>
        <div class="fields" style="grid-template-columns:1fr 1fr 140px">
          <div><label>用户名</label><input id="usName" placeholder="例如 阿离"></div>
          <div><label>口令（至少 6 位）</label><input id="usPwd" type="password"></div>
          <div><label>管理员</label><label class="sub" style="margin:0"><input type="checkbox" id="usAdmin"> 是</label></div>
        </div>
        <div style="display:flex;gap:8px;align-items:center">
          <button class="btn" id="usAdd">创建</button>
          <span class="sub" id="usMsg" style="margin:0"></span>
        </div>
      </div>`;
    $i('usList').innerHTML = list.length ? list.map(u => `
      <div class="row">
        <div class="who"><b>${esc(u.name)}${u.admin ? ' <span class="tag on">管理员</span>' : ''}</b>
          <span class="mono">${esc(u.id)} · 建于 ${fmtTime(u.createdAt)}</span></div>
        <div class="tags">
          <button class="btn ghost sm" data-pwd="${esc(u.id)}">改口令</button>
          <button class="btn ghost sm" data-del="${esc(u.id)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有用户</div>';

    $i('usList').querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
      if (!confirm('删除这个用户？（它的数据目录会保留，需要你自己去文件里清）')) return;
      const rr = await API.call('/api/agent/users/' + encodeURIComponent(b.dataset.del), 'DELETE');
      Shell.toast(rr.ok ? '已删除' : ('删除失败：' + rr.error), rr.ok ? 'ok' : 'err');
      draw();
    });
    $i('usList').querySelectorAll('[data-pwd]').forEach(b => b.onclick = async () => {
      const p = prompt('输入新口令（至少 6 位）：');
      if (!p) return;
      const rr = await API.post('/api/agent/users/' + encodeURIComponent(b.dataset.pwd) + '/password', { password: p });
      Shell.toast(rr.ok ? '已改口令（该用户的旧登录已作废）' : ('改口令失败：' + rr.error), rr.ok ? 'ok' : 'err');
    });
    $i('usAdd').onclick = async () => {
      const name = $i('usName').value.trim(), pwd = $i('usPwd').value, admin = $i('usAdmin').checked;
      if (!name || !pwd) { $i('usMsg').innerHTML = '<span class="err">用户名与口令都要填</span>'; return; }
      const rr = await API.post('/api/agent/users', { name: name, password: pwd, admin: admin });
      if (!rr.ok) { $i('usMsg').innerHTML = `<span class="err">${esc(rr.error || '创建失败')}</span>`; return; }
      $i('usMsg').innerHTML = '<span class="ok">已创建（数据分区也建好了）</span>';
      draw();
    };
  };

  /* ---------- 用户组 ---------- */
  const drawGroups = async () => {
    body.innerHTML = '<div class="empty">读取中…</div>';
    const [rg, ru, me] = await Promise.all([
      API.get('/api/agent/groups'), API.get('/api/agent/users'), API.whoami(),
    ]);
    if (!rg.ok) { showErr(body, rg); return; }
    const groups = (rg.data && rg.data.groups) || [];
    const users = (ru.ok && ru.data && ru.data.users) || [];
    const my = (me.ok && me.data && me.data.groups) || [];
    const myIds = my.map(g => g.id);

    body.innerHTML = `
      <h3>用户组</h3>
      <div class="sub">组内成员共享一份「共享库」：谁上传的文档，别的成员刷新就能看到并下载。
        同名不同内容会各存一份（这就是"分叉"），到「冲突协商」页签里收场。</div>
      <div class="fields" style="grid-template-columns:1fr 1fr 180px">
        <div><label>组名</label><input id="grName" placeholder="例如 一家人"></div>
        <div><label>创建者（owner，可留空）</label>
          <select id="grOwner"><option value="">（不设）</option>
          ${users.map(u => `<option value="${esc(u.id)}">${esc(u.name)}</option>`).join('')}</select></div>
        <div style="display:flex;align-items:flex-end"><button class="btn" id="grAdd">新建用户组</button></div>
      </div>
      <div class="sub" id="grMsg" style="margin:0 0 8px"></div>
      <div id="grList"></div>
      <div class="sect" id="grMembersWrap" hidden>
        <h3>成员 · <span id="grMName" style="color:var(--accent)"></span></h3>
        <div id="grMembers"></div>
        <div class="fields" style="grid-template-columns:1fr 160px">
          <div><label>加入成员</label><select id="grAddUser">
            ${users.map(u => `<option value="${esc(u.id)}">${esc(u.name)}</option>`).join('')}</select></div>
          <div style="display:flex;align-items:flex-end"><button class="btn ghost" id="grAddGo">加入</button></div>
        </div>
      </div>`;

    $i('grList').innerHTML = groups.length ? groups.map(g => `
      <div class="row">
        <div class="who"><b>${esc(g.name)}${myIds.indexOf(g.id) >= 0 ? ' <span class="tag on">我在组里</span>' : ''}</b>
          <span class="mono">${esc(g.id)} · ${g.memberCount || 0} 人 · 建于 ${fmtTime(g.createdAt)}</span></div>
        <div class="tags">
          <button class="btn ghost sm" data-mem="${esc(g.id)}" data-name="${esc(g.name)}">成员</button>
          <button class="btn ghost sm" data-del="${esc(g.id)}">删除</button>
        </div>
      </div>`).join('') : '<div class="empty">还没有用户组</div>';

    const showMembers = async (gid, gname) => {
      $i('grMembersWrap').hidden = false;
      $i('grMName').textContent = gname;
      $i('grMembers').innerHTML = '<div class="empty">读取中…</div>';
      const rr = await API.get('/api/agent/groups/' + encodeURIComponent(gid));
      if (!rr.ok) { showErr($i('grMembers'), rr); return; }
      const mem = (rr.data && rr.data.members) || [];
      $i('grMembers').innerHTML = mem.length ? mem.map(m => `
        <div class="row">
          <div class="who"><b>${esc(m.userName || m.userId)}</b>
            <span class="mono">${esc(m.userId)} · ${esc(m.role)} · 加入 ${fmtTime(m.joinedAt)}</span></div>
          <div class="tags"><button class="btn ghost sm" data-rm="${esc(m.userId)}">移出</button></div>
        </div>`).join('') : '<div class="empty">组里还没有人</div>';
      $i('grMembers').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
        const rr2 = await API.post('/api/agent/groups/' + encodeURIComponent(gid) + '/members',
          { action: 'remove', userId: b.dataset.rm });
        Shell.toast(rr2.ok ? '已移出' : ('移出失败：' + rr2.error), rr2.ok ? 'ok' : 'err');
        showMembers(gid, gname);
      });
      $i('grAddGo').onclick = async () => {
        const uid = $i('grAddUser').value;
        if (!uid) return;
        const rr2 = await API.post('/api/agent/groups/' + encodeURIComponent(gid) + '/members',
          { action: 'add', userId: uid });
        Shell.toast(rr2.ok ? '已加入' : ('加入失败：' + rr2.error), rr2.ok ? 'ok' : 'err');
        showMembers(gid, gname);
      };
    };

    $i('grList').querySelectorAll('[data-mem]').forEach(b => b.onclick = () => showMembers(b.dataset.mem, b.dataset.name));
    $i('grList').querySelectorAll('[data-del]').forEach(b => b.onclick = async () => {
      if (!confirm('删除这个用户组？（组内共享文档会保留在磁盘上）')) return;
      const rr = await API.call('/api/agent/groups/' + encodeURIComponent(b.dataset.del), 'DELETE');
      Shell.toast(rr.ok ? '已删除' : ('删除失败：' + rr.error), rr.ok ? 'ok' : 'err');
      draw();
    });
    $i('grAdd').onclick = async () => {
      const name = $i('grName').value.trim();
      if (!name) { $i('grMsg').innerHTML = '<span class="err">先填组名</span>'; return; }
      const rr = await API.post('/api/agent/groups', { name: name, ownerId: $i('grOwner').value });
      if (!rr.ok) { $i('grMsg').innerHTML = `<span class="err">${esc(rr.error || '创建失败')}</span>`; return; }
      $i('grMsg').innerHTML = '<span class="ok">已创建</span>';
      draw();
    };
  };

  /* ---------- 共享文档 ---------- */
  const drawDocs = async () => {
    body.innerHTML = '<div class="empty">读取中…</div>';
    const me = await API.whoami();
    if (!me.ok || !me.data || !me.data.loggedIn) {
      body.innerHTML = '<div class="empty">共享文档要<b>先登录</b>才能看（是组内隐私）。到「身份」页签登录。</div>';
      return;
    }
    const mine = (me.data.groups || []);
    if (!mine.length) {
      body.innerHTML = '<div class="empty">你还没加入任何用户组。到「用户组」页签建一个，或让别人把你加进去。</div>';
      return;
    }
    body.innerHTML = `
      <h3>共享文档</h3>
      <div class="sub">组内成员互相可见：谁上传，别人刷新就能看到并下载。
        <b>同名不同内容 = 两版并存（分叉）</b>，会被标出来，去「冲突协商」里定稿。</div>
      <div class="fields" style="grid-template-columns:240px 1fr 160px">
        <div><label>用户组</label><select id="dcGroup">
          ${mine.map(g => `<option value="${esc(g.id)}">${esc(g.name)}</option>`).join('')}</select></div>
        <div><label>上传（选中文件后点右边按钮）</label><input type="file" id="dcFile"></div>
        <div style="display:flex;align-items:flex-end"><button class="btn" id="dcUp">上传到该组</button></div>
      </div>
      <div class="sub" id="dcMsg" style="margin:0 0 10px"></div>
      <div id="dcList"></div>
      <div class="sect"><button class="btn ghost" id="dcReload">刷新</button></div>`;

    const gid = () => $i('dcGroup').value;
    const load = async () => {
      $i('dcList').innerHTML = '<div class="empty">读取中…</div>';
      const rr = await API.get('/api/agent/groups/' + encodeURIComponent(gid()) + '/docs');
      if (!rr.ok) { showErr($i('dcList'), rr); return; }
      const d = rr.data || {};
      const groups = d.versions || [];
      $i('dcList').innerHTML = groups.length ? groups.map(grp => `
        <div class="sect" style="margin-top:14px">
          <h3 style="font-size:13px">${esc(grp.name)}
            ${grp.forked ? '<span class="tag warn">分叉 · ' + grp.versions.length + ' 版</span>' : ''}</h3>
          ${(grp.versions || []).map(v => `
            <div class="row">
              <div class="who"><b>${esc((v.by || '未署名') + ' 的一版')}</b>
                <span class="mono">${bytes(v.size)} · ${fmtTime(v.at)} · ${esc(v.id)}${v.mime ? ' · ' + esc(v.mime) : ''}</span></div>
              <div class="tags">
                <button class="btn ghost sm" data-get="${esc(v.id)}" data-nm="${esc(grp.name)}">下载</button>
                <button class="btn ghost sm" data-rm="${esc(v.id)}">删除</button>
              </div>
            </div>`).join('')}
        </div>`).join('')
        : '<div class="empty">这个组还没有共享文档</div>';

      $i('dcList').querySelectorAll('[data-get]').forEach(b => b.onclick = async () => {
        const rr2 = await API.get('/api/agent/groups/' + encodeURIComponent(gid()) + '/docs/' + encodeURIComponent(b.dataset.get));
        if (!rr2.ok) { Shell.toast('下载失败：' + rr2.error, 'err'); return; }
        downloadBase64((rr2.data && rr2.data.doc && rr2.data.doc.name) || b.dataset.nm, rr2.data.dataBase64);
      });
      $i('dcList').querySelectorAll('[data-rm]').forEach(b => b.onclick = async () => {
        if (!confirm('删掉这一版？')) return;
        const rr2 = await API.call('/api/agent/groups/' + encodeURIComponent(gid()) + '/docs/' + encodeURIComponent(b.dataset.rm), 'DELETE');
        Shell.toast(rr2.ok ? '已删除' : ('删除失败：' + rr2.error), rr2.ok ? 'ok' : 'err');
        load();
      });
    };

    $i('dcGroup').onchange = load;
    $i('dcReload').onclick = load;
    $i('dcUp').onclick = async () => {
      const f = $i('dcFile').files && $i('dcFile').files[0];
      if (!f) { $i('dcMsg').innerHTML = '<span class="err">先选一个文件</span>'; return; }
      $i('dcMsg').textContent = '读取文件…';
      let b64;
      try { b64 = await fileToBase64(f); }
      catch (e) { $i('dcMsg').innerHTML = `<span class="err">${esc(e.message)}</span>`; return; }
      $i('dcMsg').textContent = '上传中…';
      const rr = await API.post('/api/agent/groups/' + encodeURIComponent(gid()) + '/docs',
        { name: f.name, kind: 'file', mime: f.type || '', dataBase64: b64 });
      if (!rr.ok) { $i('dcMsg').innerHTML = `<span class="err">${esc(rr.error || '上传失败')}</span>`; return; }
      const conflict = (rr.data || {}).conflict;
      $i('dcMsg').innerHTML = conflict
        ? `<span class="warn">已上传 —— 这个文件名现在有 ${conflict.versionCount} 版（分叉），自动开了协商会话。去「冲突协商」页签收场。</span>`
        : '<span class="ok">已上传（组内成员刷新即可见）</span>';
      load();
    };
    await load();
  };

  await draw();
}

/* ================= 11. 冲突协商 ================= */
/* 数据：GET /api/agent/conflicts（跨组聚合，只列 open）
        GET /api/agent/groups/{gid}/conflicts/{sid}[?afterMsg=][&afterSignal=]
        POST .../messages | .../resolve {docId} | .../giveup | .../signal {to,kind,payload}
   两版内容各自 GET /api/agent/groups/{gid}/docs/{docId}（只读）。 */
async function renderConflicts(root) {
  root.innerHTML = `
    <div style="padding:14px 16px">
      <h3>冲突协商</h3>
      <div class="sub">同一份文档被两个人各改了一版 → 系统自动开一次协商。
        两版<b>左右对照、都只能看</b>（这里没有任何"改对方那版"的操作）；用聊天把话说清，
        最后保留一版，或一方放弃自己的那版。</div>
      <div id="cfList"></div>
      <div class="sect" id="cfDetailWrap" hidden>
        <h3>协商 · <span id="cfName" style="color:var(--accent)"></span>
          <span class="tag" id="cfStatus"></span></h3>
        <div id="cfBody"></div>
      </div>
    </div>`;

  const $i = id => root.querySelector('#' + id);
  let cur = null;          // {group, sid}
  let msgAfter = 0, rtcLastSig = 0;
  let curMe = '', curOther = '';   // 我 / 对方的用户 id（音视频要发给对方）
  let rtc = null;          // WebRTC 会话（可选）

  /* ---------- 列表 ---------- */
  const loadList = async () => {
    const r = await API.get('/api/agent/conflicts');
    if (!r.ok) {
      $i('cfList').innerHTML = `<div class="empty err">读不到：${esc(r.error || '')}
        ${/登录/.test(r.error || '') ? '（先到「账号与共享 → 身份」登录）' : ''}</div>`;
      return;
    }
    const list = (r.data && r.data.conflicts) || [];
    $i('cfList').innerHTML = list.length ? list.map(x => {
      const c = x.conflict || {};
      return `<div class="row">
        <div class="who"><b>${esc(c.name || '')}${c.versionCount > 2 ? ' <span class="tag warn">' + c.versionCount + ' 版</span>' : ''}</b>
          <span class="mono">${esc(x.groupName || x.group)} · ${c.messageCount || 0} 条消息 · ${fmtTime(c.updatedAt)}</span></div>
        <div class="tags"><button class="btn sm" data-open="1" data-g="${esc(x.group)}" data-s="${esc(c.id)}">去协商</button></div>
      </div>`;
    }).join('') : '<div class="empty">现在没有待处理的冲突</div>';
    $i('cfList').querySelectorAll('[data-open]').forEach(b => b.onclick = () => openDetail(b.dataset.g, b.dataset.s));
  };

  /* ---------- 详情 ---------- */
  const openDetail = async (gid, sid) => {
    cur = { group: gid, sid: sid };
    msgAfter = 0; rtcLastSig = 0;
    lastMessages = [];
    stopCall();
    $i('cfDetailWrap').hidden = false;
    $i('cfBody').innerHTML = '<div class="empty">读取中…</div>';
    await refreshDetail(true);
  };

  let lastMessages = [];
  const refreshDetail = async (full) => {
    if (!cur) return;
    const r = await API.get('/api/agent/groups/' + encodeURIComponent(cur.group) +
      '/conflicts/' + encodeURIComponent(cur.sid) +
      '?afterMsg=' + (full ? 0 : msgAfter) + '&afterSignal=' + (full ? 0 : rtcLastSig));
    if (!r.ok) { showErr($i('cfBody'), r); return; }
    const d = r.data || {};
    const c = d.conflict || {};
    const vers = d.versions || [];
    const me = d.me || '';
    $i('cfName').textContent = c.name || '';
    $i('cfStatus').innerHTML = c.status === 'open'
      ? '<span class="tag warn">协商中</span>'
      : (c.status === 'resolved' ? '<span class="tag on">已定稿</span>' : '<span class="tag off">已放弃</span>');

    if (full) {
      $i('cfBody').innerHTML = detailHTML(vers, me, c);
      await wireDetail(vers, me, c);
    }

    // 增量消息
    const delta = d.messages || [];
    if (delta.length) {
      delta.forEach(m => lastMessages.push(m));
      msgAfter = delta[delta.length - 1].id;
      renderMessages();
    }
    // 信令（音视频）：只处理新到的
    const sigs = d.signals || [];
    if (sigs.length) {
      rtcLastSig = Math.max(rtcLastSig, sigs[sigs.length - 1].id);
      await handleSignals(sigs);
    }
  };

  const detailHTML = (vers, me, c) => {
    const mine = vers.find(v => v.ownerId === me) || vers[0] || {};
    const other = vers.find(v => v.docId !== mine.docId) || vers[1] || {};
    return `
      <div class="sub">会话 <span class="mono">${esc(c.id || '')}</span>
        · 作者：${vers.map(v => esc(v.ownerName || v.ownerId || '?')).join(' / ')}
        ${c.resolvedDoc ? '· 已保留 <span class="mono">' + esc(c.resolvedDoc) + '</span>' : ''}</div>
      <div class="vs2">
        <div class="vsCol">
          <div class="vsHead"><b>${esc(mine.ownerName || '我')} 的一版</b>
            ${mine.ownerId === me ? '<span class="tag on">我的</span>' : '<span class="tag">只读</span>'}</div>
          <pre class="vsText" id="cfLeft"></pre>
        </div>
        <div class="vsCol">
          <div class="vsHead"><b>${esc(other.ownerName || '对方')} 的一版</b>
            <span class="tag">只读</span></div>
          <pre class="vsText" id="cfRight"></pre>
        </div>
      </div>
      <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin:10px 0">
        ${c.status === 'open' ? `
          <button class="btn" data-keep="${esc(mine.docId || '')}">保留左版（${esc(mine.ownerName || '我')}）</button>
          <button class="btn" data-keep="${esc(other.docId || '')}">保留右版（${esc(other.ownerName || '对方')}）</button>
          <button class="btn ghost" data-giveup="1">我放弃我这一版</button>
        ` : '<span class="sub" style="margin:0">这次协商已经收场，下面留着聊天记录。</span>'}
        <span class="sub" id="cfAct" style="margin:0"></span>
      </div>
      <div class="sect">
        <h3 style="font-size:13px">聊天</h3>
        <div class="chat-box" id="cfMsgs"></div>
        <div style="display:flex;gap:8px;margin-top:8px">
          <input id="cfInput" placeholder="说点什么（例如：我用的是新预算，你在旧表上改的）">
          <button class="btn" id="cfSend">发送</button>
        </div>
      </div>
      <div class="sect">
        <h3 style="font-size:13px">音视频</h3>
        <div id="cfRtc" class="sub">检测设备中…</div>
      </div>`;
  };

  const renderMessages = () => {
    const box = $i('cfMsgs');
    if (!box) return;
    box.innerHTML = lastMessages.length ? lastMessages.map(m => `
      <div class="chat-line"><b>${esc(m.fromName || m.fromId || '')}</b>
        <span class="mono">${fmtTime(m.at)}</span>
        <div class="chat-text">${esc(m.text)}</div></div>`).join('')
      : '<div class="empty">还没有聊天</div>';
    box.scrollTop = box.scrollHeight;
  };

  const loadDocText = async (docId) => {
    if (!docId) return '（这一版读不到）';
    const r = await API.get('/api/agent/groups/' + encodeURIComponent(cur.group) + '/docs/' + encodeURIComponent(docId));
    if (!r.ok) return '（读这一版失败：' + (r.error || '') + '）';
    return b64ToText((r.data || {}).dataBase64 || '');
  };

  const wireDetail = async (vers, me, c) => {
    const [$l, $r] = [$i('cfLeft'), $i('cfRight')];
    const mine = vers.find(v => v.ownerId === me) || vers[0] || {};
    const other = vers.find(v => v.docId !== mine.docId) || vers[1] || {};
    curMe = me;
    curOther = other.ownerId || '';
    if ($l) $l.textContent = await loadDocText(mine.docId);
    if ($r) $r.textContent = await loadDocText(other.docId);

    $i('cfBody').querySelectorAll('[data-keep]').forEach(b => b.onclick = async () => {
      const docId = b.dataset.keep;
      if (!docId) return;
      if (!confirm('保留这一版？同名其余版本会被删掉，协商随之结束。')) return;
      $i('cfAct').textContent = '定稿中…';
      const rr = await API.post('/api/agent/groups/' + encodeURIComponent(cur.group) +
        '/conflicts/' + encodeURIComponent(cur.sid) + '/resolve', { docId: docId });
      if (!rr.ok) { $i('cfAct').innerHTML = `<span class="err">${esc(rr.error || '失败')}</span>`; return; }
      Shell.toast('已定稿，只保留你选的那一版', 'ok');
      openDetail(cur.group, cur.sid);
      loadList();
    });
    $i('cfBody').querySelectorAll('[data-giveup]').forEach(b => b.onclick = async () => {
      if (!confirm('放弃你自己这一版？对方的版本会成为正本（你的那份会被删掉）。')) return;
      $i('cfAct').textContent = '提交中…';
      const rr = await API.post('/api/agent/groups/' + encodeURIComponent(cur.group) +
        '/conflicts/' + encodeURIComponent(cur.sid) + '/giveup', {});
      if (!rr.ok) { $i('cfAct').innerHTML = `<span class="err">${esc(rr.error || '失败')}</span>`; return; }
      Shell.toast('已放弃自己那一版', 'ok');
      openDetail(cur.group, cur.sid);
      loadList();
    });
    $i('cfSend').onclick = sendMsg;
    $i('cfInput').onkeydown = e => { if (e.key === 'Enter') sendMsg(); };
    await setupRTC();
  };

  const sendMsg = async () => {
    const t = $i('cfInput').value.trim();
    if (!t) return;
    $i('cfInput').value = '';
    const rr = await API.post('/api/agent/groups/' + encodeURIComponent(cur.group) +
      '/conflicts/' + encodeURIComponent(cur.sid) + '/messages', { text: t });
    if (!rr.ok) { Shell.toast('发送失败：' + rr.error, 'err'); return; }
    await refreshDetail(false);
  };

  /* ---------- 音视频（WebRTC + 后端信令中转） ----------
     设备有麦克风/摄像头就用 WebRTC；没有就只留文字——后端两种都只是中转，不做区分。
     ⚠️ 设备枚举在某些环境会卡住/被拦，所以**不把它当成闸门**：能枚举就提示一下，
     枚举不到照样给按钮（点下去才知道到底行不行），绝不把界面卡在"检测中"。 */
  const withTimeout = (p, ms) => Promise.race([
    p, new Promise(resolve => setTimeout(() => resolve(null), ms)),
  ]);

  const setupRTC = async () => {
    const box = $i('cfRtc');
    if (!box) return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      box.innerHTML = '这个环境不支持音视频（没有 getUserMedia），用上面的文字聊天即可。';
      return;
    }
    let devs = [];
    try {
      const got = await withTimeout(navigator.mediaDevices.enumerateDevices(), 2500);
      devs = got || [];
    } catch (e) { devs = []; }
    const hasCam = devs.some(d => d.kind === 'videoinput');
    const hasMic = devs.some(d => d.kind === 'audioinput');
    const hint = (hasMic || hasCam)
      ? '检测到：' + (hasMic ? '麦克风' : '') + (hasCam ? (hasMic ? ' + ' : '') + '摄像头' : '') + '。'
      : '没读到设备列表（也可能是还没授权）——直接点一下试试，不行就用上面的打字框。';
    box.innerHTML = `
      <div>${esc(hint)} 点下面发起通话，另一方在同一个协商页签会收到（需要双方都开着这个页面）。</div>
      <div style="display:flex;gap:8px;align-items:center;margin-top:8px">
        <button class="btn" id="cfCall">发起音视频</button>
        <button class="btn ghost" id="cfHang" hidden>挂断</button>
        <span class="sub" id="cfRtcMsg" style="margin:0"></span>
      </div>
      <div class="vs2" style="margin-top:8px">
        <video id="cfLocal" autoplay muted playsinline></video>
        <video id="cfRemote" autoplay playsinline></video>
      </div>`;
    $i('cfCall').onclick = () => startCall(true);
    $i('cfHang').onclick = () => stopCall();
  };

  const startCall = async (initiator) => {
    const msg = id => $i(id);
    try {
      const otherId = curOther;
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true }).catch(
        () => navigator.mediaDevices.getUserMedia({ audio: true }).catch(() => null));
      if (!stream) { msg('cfRtcMsg').innerHTML = '<span class="err">拿不到麦克风/摄像头权限</span>'; return; }
      const pc = new RTCPeerConnection();           // 同一局域网内靠 host 候选即可，不引第三方 STUN/TURN
      stream.getTracks().forEach(t => pc.addTrack(t, stream));
      const local = msg('cfLocal');
      if (local) local.srcObject = stream;
      pc.ontrack = ev => { const r = msg('cfRemote'); if (r && ev.streams[0]) r.srcObject = ev.streams[0]; };
      pc.onicecandidate = ev => {
        if (ev.candidate) postSignal(otherId, 'ice', JSON.stringify(ev.candidate));
      };
      rtc = { pc: pc, stream: stream, otherId: otherId };
      msg('cfRtcMsg').textContent = '连接中…';
      msg('cfHang').hidden = false;
      if (initiator) {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        postSignal(otherId, 'offer', JSON.stringify(pc.localDescription));
      }
    } catch (e) {
      msg('cfRtcMsg').innerHTML = `<span class="err">发起失败：${esc(e.message || e)}</span>`;
    }
  };

  const stopCall = () => {
    if (rtc) {
      try { rtc.pc.close(); } catch (e) { /* 忽略 */ }
      try { (rtc.stream.getTracks() || []).forEach(t => t.stop()); } catch (e) { /* 忽略 */ }
      rtc = null;
    }
    const l = $i('cfLocal'), r = $i('cfRemote');
    if (l) l.srcObject = null;
    if (r) r.srcObject = null;
    const h = $i('cfHang'); if (h) h.hidden = true;
    const m = $i('cfRtcMsg'); if (m) m.textContent = '';
  };

  const postSignal = async (to, kind, payload) => {
    if (!cur) return;
    await API.post('/api/agent/groups/' + encodeURIComponent(cur.group) +
      '/conflicts/' + encodeURIComponent(cur.sid) + '/signal', { to: to, kind: kind, payload: payload });
  };

  const handleSignals = async (sigs) => {
    for (const s of sigs) {
      try {
        if (s.kind === 'offer' && !rtc) {
          await startCall(false);
          if (!rtc) continue;
        }
        if (!rtc) continue;
        if (s.kind === 'offer') {
          await rtc.pc.setRemoteDescription(JSON.parse(s.payload));
          const ans = await rtc.pc.createAnswer();
          await rtc.pc.setLocalDescription(ans);
          postSignal(rtc.otherId, 'answer', JSON.stringify(rtc.pc.localDescription));
        } else if (s.kind === 'answer') {
          await rtc.pc.setRemoteDescription(JSON.parse(s.payload));
        } else if (s.kind === 'ice') {
          await rtc.pc.addIceCandidate(JSON.parse(s.payload));
        } else if (s.kind === 'bye') {
          stopCall();
        }
      } catch (e) { /* 单条信令坏了不打断 */ }
    }
  };

  /* ---------- 启动 ---------- */
  await loadList();
  pollWhileMounted(root, async () => {
    await loadList();
    if (cur) await refreshDetail(false);
  }, 3000);
}

/* ================= 回填注册表（同 apps2.js 末尾的说明） ================= */
(function wireMultiUserApps() {
  const map = { accounts: renderAccounts, conflicts: renderConflicts };
  Object.keys(map).forEach(id => {
    const app = window.APP_BY_ID && window.APP_BY_ID[id];
    if (app) app.render = map[id];
  });
})();
