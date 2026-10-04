/* 白泽 - 存储层
   两种模式，看「跨端」开没开：

   ① 本机模式（没连后端）：一切都在 localStorage，密码本用 WebCrypto（AES-GCM）加密。
   ② 知识库模式（连上后端）：正本在 NAS 上的白泽知识库，本机只留一份待办缓存
      （断网还能看见列表）。写操作拆成最小操作集推给后端，推不动就进「待办同步队列」，
      联网后自动补推 —— 绝不静默丢弃，也绝不在没推成功时就当它存好了。
      密码本不落本机：每次从后端读。 */
'use strict';
window.Store = (function () {
  const K = {
    todos: 'bz_todos',
    outbox: 'bz_outbox',      // 还没推给后端的待办改动（知识库模式）
    remote: 'bz_remote_snapshot', // 最后一次跟后端核对过的待办快照（saveTodos 做 diff 的基线）
    settings: 'bz_settings',
    vaultPlain: 'bz_vault',   // 未设置主密码时用（本机模式）
    vaultEnc: 'bz_vault_enc', // 设置主密码后：{iv, data} Base64（本机模式）
    pwdHash: 'bz_vault_pwd',  // 主密码的 PBKDF2 校验哈希 {salt, hash}（本机模式）
    chat: 'bz_chat',          // 老的单会话（只用于首次迁移）
    chats: 'bz_chats',        // 多会话：{active, list:[{id,title,ts,msgs}]}
  };

  function read(key, fallback) {
    try {
      const raw = localStorage.getItem(key);
      if (raw === null || raw === undefined) return fallback;
      return JSON.parse(raw);
    } catch (e) { return fallback; }
  }
  function write(key, val) {
    try {
      localStorage.setItem(key, JSON.stringify(val));
    } catch (e) {
      // 配额满了必须明确报出来：静默失败会让人以为"记进去了"
      throw new Error('本机存储写不进去了（多半是图片/附件缩略图占满了），删掉一些再试');
    }
  }

  /* ---------- 跨端：正本是不是在后端知识库 ---------- */

  function dev() { return window.BzDevice || null; }
  /** 知识库模式：配了后端就走它（连不上是另一回事——改动会进队列等补推，不回退成本机模式）。
      第一次用到时先探一次，免得启动早期把改动写错地方。 */
  function kbMode() {
    const d = dev();
    if (!d || !d.remoteConfigured) return false;
    if (!d.remoteInfo() && d.refreshRemote) d.refreshRemote();
    return !!d.remoteConfigured();
  }
  function kbInfo() { const d = dev(); return d && d.remoteInfo ? d.remoteInfo() : null; }
  /** 后端此刻通不通（通了才能读写最新数据） */
  function kbOnline() { const d = dev(); return !!(d && d.remoteReady && d.remoteReady()); }

  /* ---------- 密码学（本机模式专用） ---------- */
  let masterKey = null; // CryptoKey

  function bufToB64(buf) { return btoa(String.fromCharCode.apply(null, new Uint8Array(buf))); }
  function b64ToBuf(b64) {
    const bin = atob(b64);
    const u = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u.buffer;
  }
  async function derive(pwd, salt) {
    const enc = new TextEncoder();
    const base = await crypto.subtle.importKey('raw', enc.encode('baize-todo-vault:' + pwd), 'PBKDF2', false, ['deriveBits', 'deriveKey']);
    const key = await crypto.subtle.deriveKey(
      { name: 'PBKDF2', salt, iterations: 150000, hash: 'SHA-256' },
      base, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']
    );
    return key;
  }
  /** 主密码的确定性校验值（PBKDF2 出来的比特，同一个密码永远得到同一个值） */
  async function hashOf(pwd, salt) {
    const enc = new TextEncoder();
    const base = await crypto.subtle.importKey('raw', enc.encode('baize-todo-vault:' + pwd), 'PBKDF2', false, ['deriveBits']);
    const bits = await crypto.subtle.deriveBits(
      { name: 'PBKDF2', salt, iterations: 150000, hash: 'SHA-256' }, base, 256
    );
    return bufToB64(bits);
  }
  async function encryptBytes(key, dataBuf) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, dataBuf);
    return { iv: bufToB64(iv), data: bufToB64(ct) };
  }
  async function decryptBytes(key, box) {
    const pt = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: b64ToBuf(box.iv), }, key, b64ToBuf(box.data));
    return new TextDecoder().decode(pt);
  }

  /* ---------- 设置（永远存本机：通知开关/提醒策略是每台设备各管各的） ---------- */
  const defaultSettings = {
    ai: { protocol: 'openai', baseUrl: 'https://api.deepseek.com/v1', apiKey: '', model: 'deepseek-chat' },
    chatAgents: [],     // 白泽页选中的智能体（多个 = 群发）
    notify: false,
    remind: true,
    planPolicy: 'balance',
  };
  function getSettings() { return Object.assign({}, defaultSettings, read(K.settings, {})); }
  function saveSettings(s) { write(K.settings, s); }

  /* ---------- 待办 ---------- */

  function getTodos() { return read(K.todos, []); }

  /** 把新旧两份待办表比出最小操作集（后端只认 todo.upsert / todo.remove） */
  function diffTodos(prev, next) {
    const ops = [];
    const prevById = {};
    (prev || []).forEach(t => { if (t && t.id) prevById[t.id] = t; });
    const seen = {};
    (next || []).forEach(t => {
      if (!t || !t.id) return;
      seen[t.id] = true;
      const old = prevById[t.id];
      if (!old || JSON.stringify(old) !== JSON.stringify(t)) {
        ops.push({ op: 'todo.upsert', args: t });
      }
    });
    (prev || []).forEach(t => {
      if (t && t.id && !seen[t.id]) ops.push({ op: 'todo.remove', args: { id: t.id } });
    });
    return ops;
  }

  /* ---- 待办同步队列：推不动就存着，联网后补推（不许静默丢） ---- */

  function outbox() { return read(K.outbox, []); }
  function setOutbox(list) {
    if (list && list.length) write(K.outbox, list);
    else localStorage.removeItem(K.outbox);
  }
  function outboxCount() { return outbox().length; }

  /* ---- 后端快照基线 + 队列对账（防"后端删掉的条目被手机推回来"） ----
     背景：待办的正本在后端，但界面模块（todo.js）各自捏着一份内存列表，而 Store 缓存
     会被每次同步刷新。以前 saveTodos 拿「本机缓存」当 diff 基线，一旦缓存被刷过、
     模块手里的旧列表就会把"早就删掉的东西"当新增推回去（真出现过：后端删掉的待办
     带着同样的 id/createdAt 又冒出来）。
     两道互补的闸门：
       ① saveTodos 的 diff 基线改成「最后一次跟后端核对过的快照」——没真改就一个 op 都不产生；
       ② flush 推之前再跟本机缓存对一次账，过期的 op 直接丢——错过一次同步也不会复活。 */

  /** 最后一次成功跟后端核对过的待办快照（没有就返回 null，调用方退回本机缓存） */
  function remoteSnapshot() { return read(K.remote, null); }
  function setRemoteSnapshot(list) {
    if (Array.isArray(list)) write(K.remote, list);
    else localStorage.removeItem(K.remote);
  }

  /** 队列对账：upsert 的对象必须还在本机缓存里、remove 的对象必须已经不在。
      离线改动是「先写缓存再入队」的，所以这里不会误伤真正的离线意图；
      被丢掉的只会是"本机已经不要它、但它还赖在队列里"的那种 op。 */
  function reconcileOutbox(list) {
    const have = {};
    (getTodos() || []).forEach(t => { if (t && t.id) have[t.id] = true; });
    return (list || []).filter((it) => {
      if (!it || !it.op) return false;
      const a = it.args || {};
      if (it.op === 'todo.upsert') return !!(a.id && have[a.id]);
      if (it.op === 'todo.remove') return !have[a.id];
      return true;   // 其它 op（目前没有）原样保留
    });
  }

  function lastSyncError() { return localStorage.getItem('bz_sync_error') || ''; }
  function setSyncError(msg) {
    if (msg) localStorage.setItem('bz_sync_error', msg);
    else localStorage.removeItem('bz_sync_error');
  }

  /** 把队列里的改动按顺序推给后端；成功的出队，失败停下并如实报错 */
  function flush() {
    const d = dev();
    if (!d || !d.remoteOp) return { ok: false, error: '当前入口没有白泽后端通道' };
    let list = outbox();
    if (!list.length) { setSyncError(''); return { ok: true, left: 0 }; }
    // 推之前先对账：把已经跟本机意愿不符的过期 op 丢掉（别盲目重放）
    const kept = reconcileOutbox(list);
    if (kept.length !== list.length) { list = kept; setOutbox(list); }
    if (!list.length) { setSyncError(''); return { ok: true, left: 0, dropped: true }; }
    while (list.length) {
      const item = list[0];
      const r = d.remoteOp(item.op, item.args);
      if (!r.ok) {
        setSyncError(r.error || '同步失败');
        return { ok: false, error: r.error || '同步失败', left: list.length };
      }
      list = list.slice(1);
      setOutbox(list);
    }
    setSyncError('');
    return { ok: true, left: 0 };
  }

  /** 存待办：先落本机缓存（界面马上能看到），知识库模式下再推给后端 */
  function saveTodos(list) {
    const prev = getTodos();
    write(K.todos, list);
    const d = dev();
    if (!kbMode()) {
      // 本机模式：老路子，把整表镜像给内核，后端的 todo.list 才读得到
      if (d && d.pushTodos) d.pushTodos(list);
      return;
    }
    // 基线优先用「最后一次跟后端核对过的快照」：本机缓存可能刚被同步刷过，
    // 而调用方（如 todo.js 的 list）手里还捏着旧列表 —— 拿缓存当基线会产生多余的 upsert
    const base = remoteSnapshot();
    const ops = diffTodos(Array.isArray(base) ? base : prev, list);
    if (!ops.length) return;
    setOutbox(outbox().concat(ops));
    const res = flush();
    if (!res.ok) {
      notify('这次改动没同步到 NAS：' + res.error + '（已排队，联网后自动补推）');
    } else if (window.UI && UI.toast) {
      UI.toast('已同步到 NAS');
    }
  }

  function notify(msg) {
    try { if (window.UI && UI.toast) UI.toast(msg, 3600); } catch (e) { /* 界面还没起来就算了 */ }
    try { window.dispatchEvent(new CustomEvent('bz:sync', { detail: { error: msg } })); } catch (e) { /* 忽略 */ }
  }

  /** 从后端知识库拉一份最新状态，覆盖本机缓存（待办）。有排队改动就先补推，别把本地改动冲掉。 */
  function syncFromRemote() {
    const d = dev();
    if (!kbMode()) return { ok: false, error: '没连上白泽后端' };
    const f = flush();
    if (!f.ok) return { ok: false, error: f.error, pending: f.left };
    const r = d.remoteState();
    if (!r.ok) { setSyncError(r.error || '取知识库失败'); return { ok: false, error: r.error }; }
    const snap = r.data || {};
    const remoteTodos = Array.isArray(snap.todos) ? snap.todos : [];
    write(K.todos, remoteTodos);
    setRemoteSnapshot(remoteTodos);   // 记下基线，下次 diff 拿它比
    vaultMem = Array.isArray(snap.vault) ? snap.vault : [];
    vaultFlags = { locked: !!snap.vaultLocked, hasMasterPwd: !!snap.hasMasterPwd };
    vaultError = '';
    setSyncError('');
    // 拉取成功 = 本机缓存已是最新正本。通知各界面模块把自己的内存列表重新装载一遍，
    // 免得它们手里那份旧列表被下一次 saveTodos 当成「用户意图」推回去
    //（todo.js 的 list 就是这种"意图来源"，它一滞后，后端删掉的待办就会复活）。
    try { window.dispatchEvent(new CustomEvent('bz:syncdone')); } catch (e) { /* 忽略 */ }
    return { ok: true, todos: remoteTodos.length };
  }

  /* ---------- 与白泽智能体的对话（多会话） ----------
     bz_chats = { active: '<会话id>', list: [ {id,title,ts,msgs} ] }
     老的单会话（bz_chat）第一次打开时自动搬进第一个会话，历史不丢。
     getChat / saveChat 保持原来的语义：都指「当前会话」，
     所以其它模块（白泽页、测试）不用改。 */
  function titleOf(list) {
    const first = (list || []).find(m => m && m.role === 'user' && m.text);
    if (!first) return '';
    const t = String(first.text).replace(/\s+/g, ' ').trim();
    return t.length > 16 ? t.slice(0, 16) + '…' : t;
  }

  function readChats() {
    let all = read(K.chats, null);
    if (!all || !Array.isArray(all.list) || !all.list.length) {
      const old = read(K.chat, []);
      all = { active: '', list: [] };
      if (Array.isArray(old) && old.length) {
        all.list.push({ id: uid(), title: titleOf(old), ts: Date.now(), msgs: old });
      }
      if (!all.list.length) all.list.push({ id: uid(), title: '', ts: Date.now(), msgs: [] });
      all.active = all.list[0].id;
      write(K.chats, all);
    }
    if (!all.list.some(c => c.id === all.active)) all.active = all.list[0].id;
    return all;
  }
  function writeChats(all) { write(K.chats, all); }
  function activeChat() {
    const all = readChats();
    return all.list.find(c => c.id === all.active) || all.list[0];
  }

  function getChat() { return (activeChat() || { msgs: [] }).msgs || []; }
  function saveChat(list) {
    const all = readChats();
    const c = all.list.find(x => x.id === all.active);
    if (!c) return;
    c.msgs = list || [];
    c.ts = Date.now();
    if (!c.title) c.title = titleOf(c.msgs);
    writeChats(all);
  }

  /** 会话清单（当前那个排最前，其余按最近使用） */
  function chatList() {
    const all = readChats();
    return all.list.map(c => ({
      id: c.id, active: c.id === all.active,
      title: c.title || titleOf(c.msgs) || '新会话',
      ts: c.ts || 0, count: (c.msgs || []).length,
    }));
  }
  function newChat() {
    const all = readChats();
    const c = { id: uid(), title: '', ts: Date.now(), msgs: [] };
    all.list.push(c);
    all.active = c.id;
    writeChats(all);
    return c.id;
  }
  function switchChat(id) {
    const all = readChats();
    if (!all.list.some(c => c.id === id)) return false;
    all.active = id;
    writeChats(all);
    return true;
  }
  /** 删掉一个会话；返回删完之后当前该用哪个。最后一个不允许删空（会补一个新的） */
  function delChat(id) {
    const all = readChats();
    all.list = all.list.filter(c => c.id !== id);
    if (!all.list.length) all.list.push({ id: uid(), title: '', ts: Date.now(), msgs: [] });
    if (!all.list.some(c => c.id === all.active)) all.active = all.list[0].id;
    writeChats(all);
    return all.active;
  }
  function clearAllChats() {
    const all = readChats();
    all.list = [{ id: uid(), title: '', ts: Date.now(), msgs: [] }];
    all.active = all.list[0].id;
    writeChats(all);
  }

  function sampleTodos() {
    const now = new Date(Date.now() + 3600e3);
    const pad = (n) => String(n).padStart(2, '0');
    const fmt = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const base = { weekly: false, estimate: 0, deps: [], owner: 'user', status: 'todo', remind: false };
    return [
      { ...base, id: uid(), title: '整理本周工作计划', category: '工作开发', priority: 'high', form: 'schedule', due: fmt(now), estimate: 60, note: '示例待办', remind: true, createdAt: Date.now() },
      { ...base, id: uid(), title: '复习 Go 基础', category: '工作开发', priority: 'mid', form: 'schedule', due: fmt(new Date(now.getTime() + 3600e3)), weekly: true, estimate: 45, createdAt: Date.now() },
      { ...base, id: uid(), title: 'AI 漫剧角色设定与主线大纲设计', category: '创作', priority: 'mid', form: 'leisure', due: '', estimate: 90, note: '灵感项目，有空时推进', createdAt: Date.now() },
      { ...base, id: uid(), title: '给爸妈买牛奶', category: '日常生活', priority: 'low', form: 'leisure', due: '', estimate: 15, createdAt: Date.now() },
    ];
  }

  /* ---------- 密码本 ---------- */

  // 知识库模式下的内存缓存：本机不落盘（用户要求"手机上不留文件"），
  // 每次打开密码本从后端读一次，之后同一会话里复用，避免列表每渲染一次就多一次请求。
  let vaultMem = null;      // null = 还没读过
  let vaultFlags = { locked: false, hasMasterPwd: false };
  let vaultError = '';      // 最近一次读不到的原因（界面要如实说出来）

  /** 密码记录统一成 { password: 当前密码, history: [更早的…], group: 归属 }
      旧数据只有 password；这里补齐并顺手清掉和历史重复的当前密码 */
  function normalizeVault(list) {
    let changed = false;
    list.forEach(v => {
      if (!Array.isArray(v.history)) { v.history = []; changed = true; }
      if (typeof v.group !== 'string') { v.group = ''; changed = true; }
      if (!v.password && v.history.length) {           // 当前密码被清空过 → 把最近一条提上来
        const h = v.history.shift();
        v.password = h && h.pwd ? h.pwd : '';
        changed = true;
      }
      const before = v.history.length;
      v.history = v.history.filter(h => h && h.pwd && h.pwd !== v.password);
      if (v.history.length !== before) changed = true;
    });
    return changed;
  }

  async function getVault() {
    if (kbMode()) {
      if (vaultMem === null) {
        const r = syncFromRemote();
        if (!r.ok) {
          vaultError = r.error || '连不上后端知识库';
          return [];
        }
        vaultError = '';
      }
      const list = Array.isArray(vaultMem) ? vaultMem.slice() : [];
      normalizeVault(list);
      return list;
    }
    let list;
    if (masterKey) {
      const box = read(K.vaultEnc, null);
      list = box ? await decryptBytes(masterKey, box).then(JSON.parse).catch(() => []) : [];
    } else {
      list = read(K.vaultPlain, []);
    }
    if (!Array.isArray(list)) list = [];
    if (normalizeVault(list)) {
      try { await saveVault(list); } catch (e) { /* 锁定时写不进去，读到的仍是整理后的 */ }
    }
    return list;
  }

  /** 新旧两份密码本比出最小操作集 */
  function diffVault(prev, next) {
    const ops = [];
    const prevById = {};
    (prev || []).forEach(p => { if (p && p.id) prevById[p.id] = p; });
    const seen = {};
    (next || []).forEach(p => {
      if (!p || !p.id || !p.title || !p.account) return;
      seen[p.id] = true;
      const old = prevById[p.id];
      if (!old || JSON.stringify(old) !== JSON.stringify(p)) ops.push({ op: 'vault.upsert', args: p });
    });
    (prev || []).forEach(p => {
      if (p && p.id && !seen[p.id]) ops.push({ op: 'vault.remove', args: { id: p.id } });
    });
    return ops;
  }

  async function saveVault(list) {
    if (kbMode()) {
      const prev = Array.isArray(vaultMem) ? vaultMem : [];
      const ops = diffVault(prev, list);
      const d = dev();
      let failed = '';
      for (const o of ops) {
        const r = d.remoteOp(o.op, o.args);
        if (!r.ok) { failed = r.error || '同步失败'; break; }
      }
      if (failed) throw new Error('没能写进 NAS 知识库：' + failed);
      vaultMem = list.slice();
      vaultFlags = { locked: false, hasMasterPwd: vaultFlags.hasMasterPwd };
      return;
    }
    // 本机模式：有主密码但没解锁时绝不能往明文槽写，否则会多出一份「看不见的密码本」
    // （用户看到的就是「记进去了，但密码本里没有」）
    if (!masterKey && hasMasterPwd()) throw new Error('密码本已锁定，先在密码本页解锁再写入');
    if (masterKey) {
      const box = await encryptBytes(masterKey, new TextEncoder().encode(JSON.stringify(list)));
      write(K.vaultEnc, box);
      localStorage.removeItem(K.vaultPlain);
    } else {
      write(K.vaultPlain, list);
    }
  }

  /** 密码本读不到的原因（离线时界面要能把话说清楚，而不是显示成"没有密码"） */
  function vaultOfflineReason() {
    if (!kbMode()) return '';
    if (vaultMem !== null) return '';
    return vaultError || '连不上后端知识库';
  }

  function isVaultLocked() {
    if (kbMode()) return !!vaultFlags.locked;
    return !!localStorage.getItem(K.pwdHash) && !masterKey;
  }
  function hasMasterPwd() {
    if (kbMode()) return !!vaultFlags.hasMasterPwd;
    return !!localStorage.getItem(K.pwdHash);
  }

  /** 旧版遗留的失效主密码记录（只在本机模式下存在） */
  function isBrokenPwdRecord() {
    const rec = read(K.pwdHash, null);
    if (!rec || rec.hash) return false;
    return !read(K.vaultEnc, null);
  }

  async function verifyMasterPwd(pwd) {
    if (kbMode()) {
      if (!vaultFlags.hasMasterPwd) return false;
      const wasLocked = vaultFlags.locked;
      const r = dev().remoteOp('vault.unlock', { password: pwd });
      if (r.ok) {
        vaultFlags.locked = false;
        vaultMem = null;
      } else if (wasLocked) {
        // 只是试密码，验证失败不该改变原来的锁定状态
        vaultFlags.locked = true;
      }
      return !!r.ok;
    }
    const rec = read(K.pwdHash, null);
    if (!rec) return false;
    const salt = b64ToBuf(rec.salt);
    if (rec.hash) return (await hashOf(pwd, salt)) === rec.hash;
    // 兼容旧记录：旧版只存了 AES-GCM 密文（每次 IV 不同，无法直接比对），
    // 改成「用这个密码去解一下密文库」——AES-GCM 解不开就说明密码不对。
    const box = read(K.vaultEnc, null);
    if (!box) return false;
    try { await decryptBytes(await derive(pwd, salt), box); return true; } catch (e) { return false; }
  }

  async function setMasterPwd(pwd, confirm) {
    if (kbMode()) {
      const r = dev().remoteOp('vault.setMaster', { password: pwd, confirm: confirm || pwd });
      if (!r.ok) throw new Error(r.error || '设置主密码失败');
      vaultFlags = { locked: false, hasMasterPwd: true };
      vaultMem = null;
      return;
    }
    const salt = crypto.getRandomValues(new Uint8Array(16));
    const key = await derive(pwd, salt);
    masterKey = key; // 立即生效，后续写入均走加密
    // 迁移现有明文凭据到加密库
    const current = read(K.vaultPlain, []);
    if (current.length) {
      const box = await encryptBytes(key, new TextEncoder().encode(JSON.stringify(current)));
      write(K.vaultEnc, box);
    }
    write(K.pwdHash, { salt: bufToB64(salt), hash: await hashOf(pwd, salt) });
    localStorage.removeItem(K.vaultPlain);
  }

  async function unlock(pwd) {
    if (kbMode()) {
      const r = dev().remoteOp('vault.unlock', { password: pwd });
      if (!r.ok) return false;
      vaultFlags.locked = false;
      vaultMem = null;
      return true;
    }
    if (!(await verifyMasterPwd(pwd))) return false;
    const rec = read(K.pwdHash, null);
    masterKey = await derive(pwd, b64ToBuf(rec.salt));
    if (!rec.hash) {   // 旧记录顺手升级成带哈希的新格式
      rec.hash = await hashOf(pwd, b64ToBuf(rec.salt));
      write(K.pwdHash, rec);
    }
    return true;
  }

  function lock() {
    if (kbMode()) { dev().remoteOp('vault.lock', {}); vaultFlags.locked = true; vaultMem = null; return; }
    masterKey = null;
  }

  /** 清除主密码：加密密钥一旦丢弃，旧的密文库就打不开了，一并清掉避免留下死数据 */
  function clearVaultPwd() {
    if (kbMode()) {
      const r = dev().remoteOp('vault.clearMaster', {});
      if (!r.ok) throw new Error(r.error || '清除主密码失败');
      vaultFlags = { locked: false, hasMasterPwd: false };
      vaultMem = null;
      return;
    }
    lock();
    localStorage.removeItem(K.pwdHash);
    localStorage.removeItem(K.vaultEnc);
    masterKey = null;
  }

  function uid() {
    return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
  }

  return {
    getSettings, saveSettings, getTodos, saveTodos, sampleTodos, getChat, saveChat,
    chatList, newChat, switchChat, delChat, clearAllChats,
    getVault, saveVault, isVaultLocked, hasMasterPwd, isBrokenPwdRecord,
    setMasterPwd, unlock, verifyMasterPwd, lock, clearVaultPwd,
    uid,
    // 知识库模式相关
    kbMode, kbOnline, kbInfo, syncFromRemote, flush, outboxCount, lastSyncError, vaultOfflineReason,
  };
})();
