/* 白泽待办中心 - 密码本模块（本地加密 / MD 导入导出 / 搜索 / 复制） */
'use strict';
window.Vault = (function () {
  const UI_ = window.UI;
  const $ = UI_.$;
  let list = [];
  let editingId = null;

  /* ---------- MD 解析：兼容
     # 站点名
     - 账号: xxx
     - 归属: Trae            （同一服务的多个站点填同一个归属，列表里会分到一组）
     - 密码: xxx             （当前密码）
     - 历史密码: xxx ｜ 2026-09-20 10:00
     - 备注: xxx
  ---------- */
  function parseMarkdown(text) {
    const items = [];
    let cur = null;
    const flush = () => { if (cur && cur.title) items.push(cur); };
    const atOf = (s) => {
      const t = String(s || '').trim().replace(' ', 'T');
      if (!t) return 0;
      const d = new Date(t);
      return isNaN(d.getTime()) ? 0 : d.getTime();
    };
    text.split(/\r?\n/).forEach((raw) => {
      const line = raw.trim();
      if (!line) return;
      if (line.startsWith('#')) {
        flush();
        cur = { title: line.replace(/^#+\s*/, ''), account: '', password: '', url: '', note: '', group: '', history: [] };
      } else if (cur) {
        const m1 = line.match(/^[-\*]\s*账号[:：]\s*(.+)/);
        const m2 = line.match(/^[-\*]\s*密码[:：]\s*(.+)/);
        const m3 = line.match(/^[-\*]\s*URL[:：]\s*(.+)/i);
        const m4 = line.match(/^[-\*]\s*备注[:：]\s*(.+)/);
        const m5 = line.match(/^[-\*]\s*归属[:：]\s*(.+)/);
        const m6 = line.match(/^[-\*]\s*历史密码[:：]\s*(.+)/);
        if (m1) cur.account = m1[1].trim();
        else if (m2) cur.password = m2[1].trim();
        else if (m3) cur.url = m3[1].trim();
        else if (m4) cur.note = m4[1].trim();
        else if (m5) cur.group = m5[1].trim();
        else if (m6) {
          const parts = m6[1].split(/｜|\|/).map(s => s.trim());
          if (parts[0]) cur.history.push({ pwd: parts[0], at: atOf(parts[1]) });
        }
      }
    });
    flush();
    // 章节标题（## 待办 / ## 密码 / 文档大标题）会被当成空条目，这里丢掉没有账号也没有密码的空壳
    return items.filter(x => x.title && (x.account || x.password || (x.history || []).length));
  }

  /** 往库里放一条密码：同「平台 + 账号」已存在就并进它的历史（新的当当前，旧的沉到历史里，按先后排序） */
  function mergePassword(target, item) {
    const key = (x) => (x.title || '') + '|' + (x.account || '');
    const pwd = item.password || '';
    const dup = target.find(x => key(x) === key(item));
    if (!dup) {
      target.push({
        id: Store.uid(), title: item.title, account: item.account || '',
        group: item.group || '', password: pwd,
        history: (item.history || []).filter(h => h && h.pwd && h.pwd !== pwd),
        url: item.url || '', note: item.note || '',
        source: item.source || 'manual', createdAt: item.createdAt || Date.now(), updatedAt: Date.now(),
      });
      return 'new';
    }
    if (!Array.isArray(dup.history)) dup.history = [];
    if (item.group && !dup.group) dup.group = item.group;      // 归属只补不覆盖
    if (item.url && !dup.url) dup.url = item.url;
    if (item.note && !dup.note) dup.note = item.note;
    (item.history || []).forEach(h => {
      if (h && h.pwd && h.pwd !== dup.password && !dup.history.some(y => y.pwd === h.pwd)) {
        dup.history.push({ pwd: h.pwd, at: h.at || Date.now() });
      }
    });
    let result = 'same';
    if (pwd && pwd !== dup.password) {
      if (dup.password) dup.history.unshift({ pwd: dup.password, at: Date.now() });
      dup.password = pwd;
      result = 'updated';
      dup.updatedAt = Date.now();
    }
    dup.history = dup.history.filter(h => h && h.pwd && h.pwd !== dup.password)
      .sort((a, b) => (b.at || 0) - (a.at || 0));
    return result;
  }

  /* ---------- MD 解析：待办行
     - [ ] 标题 ｜ 类别: 工作开发 ｜ 优先级: 高优 ｜ 截止: 2026-09-25 14:00 ｜ 备注: xxx
  ---------- */
  const PRI_IN = { '高优': 'high', '高': 'high', 'high': 'high', '中': 'mid', 'mid': 'mid', '低': 'low', 'low': 'low' };

  function parseTodos(text) {
    const out = [];
    let inTodo = false;
    text.split(/\r?\n/).forEach((raw) => {
      const line = raw.trim();
      if (/^##\s*待办/.test(line)) { inTodo = true; return; }
      if (/^##\s/.test(line)) { inTodo = false; return; }
      if (!inTodo && !/^-\s*\[[ xX]\]/.test(line)) return;
      const m = line.match(/^-\s*\[([ xX])\]\s*(.+)$/);
      if (!m) return;
      const parts = m[2].split(/｜|\|/).map(s => s.trim()).filter(Boolean);
      if (!parts.length) return;
      const t = {
        title: parts[0].replace(/（Agent）$/, ''),
        status: m[1].toLowerCase() === 'x' ? 'done' : 'todo',
        category: '日常生活', priority: 'mid', due: '', note: '', deps: [],
      };
      parts.slice(1).forEach((seg) => {
        const kv = seg.match(/^([^:：]+)[:：]\s*(.*)$/);
        if (!kv) return;
        const k = kv[1].trim(), v = kv[2].trim();
        if (k === '类别') t.category = v;
        else if (k === '优先级') t.priority = PRI_IN[v] || 'mid';
        else if (k === '截止') t.due = v.replace(' ', 'T');
        else if (k === '备注') t.note = v;
      });
      if (t.title) out.push(t);
    });
    return out;
  }

  async function importMarkdown(text) {
    // 先拉最新的库：内存里的 list 可能已经过期（比如 AI 刚录进去一条），
    // 直接写回会把别人的改动覆盖掉
    list = await Store.getVault();
    // 1) 密码（同账号的旧密码自动沉到历史里，不会互相覆盖）
    const parsed = parseMarkdown(text);
    let added = 0, merged = 0;
    parsed.forEach(p => {
      p.source = p.source || 'md';
      const r = mergePassword(list, p);
      if (r === 'new') added++; else merged++;
    });
    await Store.saveVault(list);

    // 2) 待办（同标题 + 同截止视为同一条，重复导入不会翻倍）
    const todos = Store.getTodos();
    let tAdded = 0, tMerged = 0;
    parseTodos(text).forEach((t) => {
      const dup = todos.find(x => x.title === t.title && (x.due || '') === (t.due || ''));
      if (dup) { tMerged++; return; }
      todos.unshift({
        id: Store.uid(), title: t.title, category: t.category, priority: t.priority,
        form: t.due ? 'schedule' : 'leisure', due: t.due, weekly: false, estimate: 0,
        note: t.note, deps: [], owner: 'user', status: t.status, remind: false, createdAt: Date.now(),
      });
      tAdded++;
    });
    Store.saveTodos(todos);

    renderAll();
    return { todos: { added: tAdded, merged: tMerged }, vault: { added, merged } };
  }

  async function reload() {
    list = await Store.getVault();
    // 读不到的时候必须把原因说出来：正本在 NAS，断网时显示成「暂无凭据」会让人以为密码丢了
    const off = Store.vaultOfflineReason ? Store.vaultOfflineReason() : '';
    if (off) {
      list = [];
      try { UI_.toast('密码本读不到：' + off, 3600); } catch (e) { /* 忽略 */ }
    }
    renderAll();
    const tip = document.getElementById('vaultLockTip');
    if (tip && off) {
      tip.textContent = '密码本正本在 NAS 上的白泽知识库，现在读不到：' + off
        + '（联网后自动恢复，密码不会丢）';
    }
  }

  function getList() { return list.slice(); }

  // 每次跟后端同步完，把本模块的内存列表重新装载一遍。
  // 这份 list 是 saveVault 的「用户意图来源」：它一旦滞后于后端正本，就会把
  // 「后端已经删掉的记录」当成用户改动又推回去。待办那边犯过同样的错；
  // 密码本没有 outbox，所以是「当场推回」而不是「补推时复活」——一样得防。
  // 注：Store 的拉取是「先写 vaultMem 再派发本事件」，所以这里的 reload 不会再触发一次同步。
  window.addEventListener('bz:syncdone', () => { reload(); });

  function dots(pwd) {
    return '•'.repeat(Math.max(6, Math.min(14, (pwd || '').length || 8)));
  }

  /** 历史密码的时间（存的是毫秒时间戳） */
  function fmtAt(ts) {
    if (!ts) return '时间未知';
    const d = new Date(ts);
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  function renderAll() {
    const q = $('vaultSearch').value.trim().toLowerCase();
    const items = list.filter(v =>
      !q || (v.title + ' ' + v.account + ' ' + (v.group || '') + ' ' + (v.url || '') + ' ' + (v.note || ''))
        .toLowerCase().includes(q)
    );
    const box = $('vaultList');
    box.innerHTML = '';
    // 按「归属」分组：同一服务的国际站/国内站填同一个归属就排在一起
    const groups = new Map();
    items.forEach(v => {
      const g = (v.group || '').trim();
      if (!groups.has(g)) groups.set(g, []);
      groups.get(g).push(v);
    });
    [...groups.keys()]
      .sort((a, b) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b, 'zh')))
      .forEach(g => {
        const arr = groups.get(g);
        const head = document.createElement('div');
        head.className = 'vault-group-head' + (g ? '' : ' none');
        head.innerHTML = g
          ? `<span>${esc(g)}</span><span>${arr.length} 个账号</span>`
          : `<span>未分组</span><span>${arr.length} 个账号</span>`;
        box.appendChild(head);
        arr.forEach(v => box.appendChild(card(v)));
      });
    $('vaultEmpty').hidden = items.length > 0;
    $('vaultCount').textContent = `${items.length} 条记录`;
    renderLockTip();
  }

  function card(v) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    const srcLabel = v.source === 'md' ? 'MD 导入' : v.source === 'agent' ? 'Agent 捕获' : '手动录入';
    const his = Array.isArray(v.history) ? v.history : [];
    el.innerHTML = `
      <div class="vc-row">
        <div class="vc-site">
          <div class="vc-icon">${esc((v.title || '?').charAt(0).toUpperCase())}</div>
          <div style="min-width:0">
            <div class="vc-title">${esc(v.title)}</div>
            <div class="vc-sub">${v.url ? esc(v.url) : srcLabel}</div>
          </div>
        </div>
        ${v.group ? `<span class="vc-group">${esc(v.group)}</span>` : ''}
      </div>
      <div class="vc-field">
        <div class="vc-field-main">
          <div class="vc-field-label">账号 / 用户名</div>
          <div class="vc-field-value">${esc(v.account || '—')}</div>
        </div>
        <button class="btn ghost sm" data-copyacc="${v.id}">复制</button>
      </div>
      <div class="vc-field">
        <div class="vc-field-main">
          <div class="vc-field-label">当前密码${his.length ? `（另有 ${his.length} 个历史密码）` : ''}</div>
          <div class="vc-field-value" data-reveal="${v.id}">${dots(v.password)}</div>
        </div>
        <button class="btn ghost sm" data-eye="${v.id}">显示</button>
        <button class="btn primary sm" data-copy="${v.id}">复制密码</button>
      </div>
      ${his.length ? `
      <div class="vc-history">
        <button class="vc-his-toggle" data-his="${v.id}">历史密码 ${his.length} 个 ▾</button>
        <div class="vc-his-list" hidden>
          ${his.map((h, i) => `
            <div class="vc-his-row">
              <span class="vc-his-pwd" data-hshow="${v.id}:${i}">${dots(h.pwd)}</span>
              <span class="vc-his-time">${fmtAt(h.at)}</span>
              <button class="btn ghost sm" data-hcopy="${v.id}:${i}">复制</button>
              <button class="btn ghost sm" data-huse="${v.id}:${i}">换回这条</button>
            </div>`).join('')}
          <div class="vc-his-tip">点密码可显示原值；「换回这条」会把当前密码沉到历史里，并把这条恢复成当前。</div>
        </div>
      </div>` : ''}
      ${v.note ? `<div class="vc-line">备注：${esc(v.note)}</div>` : ''}
      <div class="vc-actions">
        <button class="btn ghost sm" data-edit="${v.id}" style="flex:1">编辑</button>
        <button class="btn danger-ghost sm" data-del="${v.id}" style="flex:1">删除</button>
      </div>`;

    el.querySelector('[data-copyacc]').addEventListener('click', () => copyText(v.account, '已复制账号'));
    el.querySelector('[data-copy]').addEventListener('click', () => copyText(v.password, '已复制密码'));
    el.querySelector('[data-eye]').addEventListener('click', () => toggleReveal(v));
    el.querySelector('[data-edit]').addEventListener('click', () => openEdit(v.id));
    el.querySelector('[data-del]').addEventListener('click', () => del(v));
    const tog = el.querySelector('[data-his]');
    if (tog) tog.addEventListener('click', () => {
      const box = el.querySelector('.vc-his-list');
      box.hidden = !box.hidden;
      tog.textContent = `历史密码 ${his.length} 个 ` + (box.hidden ? '▾' : '▴');
    });
    el.querySelectorAll('[data-hshow]').forEach(sp => sp.addEventListener('click', () => {
      const h = his[Number(sp.dataset.hshow.split(':')[1])];
      if (!h) return;
      sp.dataset.open = sp.dataset.open === '1' ? '' : '1';
      sp.textContent = sp.dataset.open === '1' ? (h.pwd || '(空)') : dots(h.pwd);
    }));
    el.querySelectorAll('[data-hcopy]').forEach(b => b.addEventListener('click', () => {
      const h = his[Number(b.dataset.hcopy.split(':')[1])];
      if (h) copyText(h.pwd, '已复制历史密码');
    }));
    el.querySelectorAll('[data-huse]').forEach(b => b.addEventListener('click', () => {
      useHistory(v.id, Number(b.dataset.huse.split(':')[1]));
    }));
    return el;
  }

  /** 把某条历史密码换回当前（当前那条沉到历史最前） */
  async function useHistory(id, idx) {
    list = await Store.getVault();
    const v = list.find(x => x.id === id);
    if (!v || !Array.isArray(v.history) || !v.history[idx]) return;
    const picked = v.history[idx];
    const old = v.password;
    const rest = v.history.filter((h, i) => i !== idx);
    v.password = picked.pwd;
    v.history = [{ pwd: old, at: Date.now() }].concat(rest)
      .filter(h => h && h.pwd && h.pwd !== v.password)
      .sort((a, b) => (b.at || 0) - (a.at || 0));
    v.updatedAt = Date.now();
    await Store.saveVault(list);
    renderAll();
    UI_.toast('已把这条换回当前密码');
  }

  function toggleReveal(v) {
    const target = document.querySelector(`[data-reveal="${v.id}"]`);
    const btn = document.querySelector(`[data-eye="${v.id}"]`);
    if (!target) return;
    if (target.dataset.open === '1') {
      target.dataset.open = '';
      target.textContent = '•'.repeat(Math.max(6, Math.min(14, (v.password || '').length || 8)));
      if (btn) btn.textContent = '显示';
    } else {
      target.dataset.open = '1';
      target.textContent = v.password || '(空)';
      if (btn) btn.textContent = '隐藏';
    }
  }

  async function copyText(text, okMsg) {
    try {
      await navigator.clipboard.writeText(text || '');
      UI_.toast(okMsg);
    } catch (e) {
      const ta = document.createElement('textarea');
      ta.value = text || ''; document.body.appendChild(ta); ta.select();
      document.execCommand('copy'); ta.remove(); UI_.toast(okMsg);
    }
  }

  function openEdit(id) {
    if (Store.isVaultLocked()) { requireUnlock(); return; }
    editingId = id;
    const v = id ? list.find(x => x.id === id) : null;
    $('vpTitle').textContent = v ? '编辑密码记录' : '新建密码记录';
    $('vTitle').value = v ? v.title : '';
    $('vAccount').value = v ? v.account : '';
    $('vPassword').value = v ? v.password : '';
    $('vGroup').value = v ? (v.group || '') : '';
    $('vUrl').value = v ? (v.url || '') : '';
    $('vNote').value = v ? (v.note || '') : '';
    const his = v && Array.isArray(v.history) ? v.history : [];
    $('vHisNote').hidden = !his.length;
    $('vHisNote').textContent = his.length
      ? `这个账号还有 ${his.length} 个历史密码：换密码直接改上面的「密码凭据」再保存，老密码会自动进历史。`
      : '';
    renderGroupChips();
    UI_.openPage('vaultPage');
  }

  /** 归属的快捷候选：填过的归属点一下就填上（多个站点共用同一个归属） */
  function renderGroupChips() {
    const box = $('vGroupChips');
    const names = [...new Set(list.map(v => (v.group || '').trim()).filter(Boolean))];
    box.innerHTML = '';
    box.hidden = names.length === 0;
    names.forEach(n => {
      const b = document.createElement('button');
      b.className = 'chip-opt';
      b.textContent = n;
      b.addEventListener('click', () => {
        $('vGroup').value = n;
        box.querySelectorAll('.chip-opt').forEach(x => x.classList.toggle('on', x === b));
      });
      if ($('vGroup').value.trim() === n) b.classList.add('on');
      box.appendChild(b);
    });
  }

  /* 生成强密码：大小写 + 数字 + 符号，避开易混字符 */
  function generateStrongPassword(len = 16) {
    const up = 'ABCDEFGHJKLMNPQRSTUVWXYZ', low = 'abcdefghijkmnpqrstuvwxyz';
    const num = '23456789', sym = '!#$%+-_=@';
    const all = up + low + num + sym;
    const pick = (set) => set[Math.floor(Math.random() * set.length)];
    const arr = [pick(up), pick(low), pick(num), pick(sym)];
    while (arr.length < len) arr.push(pick(all));
    for (let i = arr.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [arr[i], arr[j]] = [arr[j], arr[i]];
    }
    return arr.join('');
  }

  async function saveEdit() {
    const title = $('vTitle').value.trim();
    const account = $('vAccount').value.trim();
    if (!title || !account) { UI_.toast('平台名称与账号为必填'); return; }
    const data = {
      title, account,
      group: $('vGroup').value.trim(),
      password: $('vPassword').value,
      url: $('vUrl').value.trim(),
      note: $('vNote').value,
    };
    list = await Store.getVault();   // 以库里最新的为准，别用可能过期的内存副本覆盖
    if (editingId) {
      const rec = list.find(x => x.id === editingId);
      if (rec) {
        const changed = data.password && data.password !== rec.password;
        if (changed) {
          if (!Array.isArray(rec.history)) rec.history = [];
          if (rec.password) rec.history.unshift({ pwd: rec.password, at: Date.now() });
          rec.history = rec.history.filter(h => h && h.pwd && h.pwd !== data.password)
            .sort((a, b) => (b.at || 0) - (a.at || 0));
        }
        Object.assign(rec, { title: data.title, account: data.account, group: data.group,
          password: data.password, url: data.url, note: data.note, updatedAt: Date.now() });
      }
    } else {
      // 同一个「平台 + 账号」已经存在 → 新密码记进它的历史，而不是多出一条重复记录
      const r = mergePassword(list, Object.assign({}, data, { source: 'manual' }));
      if (r === 'updated') UI_.toast('这个账号原来就有，新密码已记进它的历史');
      else if (r === 'same') UI_.toast('这个账号已有记录，密码一样，没重复添加');
    }
    await Store.saveVault(list);
    UI_.closePage('vaultPage');
    renderAll();
    UI_.toast(editingId ? '已保存' : '已存入自用密码库');
  }

  function del(v) {
    UI_.actionSheet([
      { label: `删除「${v.title}」`, danger: true, onTap: async () => { list = await Store.getVault(); list = list.filter(x => x.id !== v.id); await Store.saveVault(list); renderAll(); UI_.toast('已删除'); } },
      { label: '取消', cancel: true },
    ]);
  }

  /* ---------- 主密码 ---------- */
  function renderLockTip() {
    const tip = $('vaultLockTip');
    tip.onclick = null;
    if (!Store.hasMasterPwd()) {
      tip.className = 'vault-lock-tip';
      tip.innerHTML = '未设置主密码，凭据将以明文存储。点此设为 AES-GCM 加密。';
      tip.style.display = 'block';
      tip.onclick = () => window.dispatchEvent(new CustomEvent('bz:toggleMasterPwd'));
    } else if (Store.isVaultLocked()) {
      tip.className = 'vault-lock-tip';
      tip.innerHTML = '密码本已锁定（点这里输入主密码解锁）';
      tip.style.display = 'block';
      tip.onclick = () => requireUnlock();
    } else {
      tip.className = 'vault-lock-tip safe';
      tip.innerHTML = '已解锁 · 凭据以 AES-GCM 加密存储于本机（点此可重新锁定）';
      tip.style.display = 'block';
      tip.onclick = () => lockVault();
    }
  }

  function lockVault() {
    Store.lock();
    refreshLockBtn(); renderLockTip();
    UI_.toast('密码本已锁定');
  }

  function requireUnlock() {
    $('pwdTitle').textContent = '密码本解锁';
    $('pwdHintWrap').hidden = true;
    $('pwdDesc').textContent = '输入主密码解锁密码本';
    $('pwdInput').value = '';
    UI_.openModal('pwdModal');
    setTimeout(() => $('pwdInput').focus(), 100);
  }

  function setupPwd() {
    $('pwdTitle').textContent = '设置主密码';
    $('pwdHintWrap').hidden = false;
    $('pwdDesc').textContent = '主密码用于 AES-GCM 加密密码本，仅存本机。忘记后无法找回（可重置清空）。';
    $('pwdInput').value = ''; $('pwdInput2').value = '';
    UI_.openModal('pwdModal');
  }

  function resetPwd() {
    Store.clearVaultPwd();
    reload();
    refreshLockBtn(); renderLockTip();
    UI_.toast('已清除主密码');
  }

  async function onPwdOk() {
    const pwd = $('pwdInput').value;
    if ($('pwdTitle').textContent === '设置主密码') {
      if (pwd.length < 6) { UI_.toast('主密码至少 6 位'); return; }
      if (pwd !== $('pwdInput2').value) { UI_.toast('两次输入不一致'); return; }
      await Store.setMasterPwd(pwd);
      UI_.closeModal('pwdModal'); renderAll(); refreshLockBtn();
      UI_.toast('主密码已设置，密码本已加密');
    } else {
      const btn = $('pwdOk');
      btn.textContent = '解锁中…';
      const ok = await Store.unlock(pwd);
      btn.textContent = '确定';
      if (!ok) { UI_.toast('密码错误'); $('pwdInput').value = ''; return; }
      UI_.closeModal('pwdModal');
      await reload();
      refreshLockBtn();
      UI_.toast('已解锁');
    }
  }

  function refreshLockBtn() {
    const btn = $('btnLock');
    const locked = Store.isVaultLocked();
    btn.hidden = !Store.hasMasterPwd();
    btn.classList.toggle('warn', !locked);
    btn.title = locked ? '密码本已锁定，点此解锁' : '密码本已解锁，点此锁定';
  }

  function init() {
    $('vaultSearch').addEventListener('input', renderAll);
    $('btnVaultAdd').addEventListener('click', () => openEdit(null));

    // 导入：APK 里走原生文件选择器，浏览器回退到隐藏 input
    $('btnVaultImport').addEventListener('click', () => {
      if (Store.isVaultLocked()) { requireUnlock(); return; }
      UI_.pickText(async (text, name) => {
        if (Store.isVaultLocked()) { requireUnlock(); return; }
        const r = await importMarkdown(text);
        Todo.reload();
        UI_.toast(`${name || '文件'} 导入完成：密码 +${r.vault.added}/合并 ${r.vault.merged}，待办 +${r.todos.added}`);
      });
    });

    // 导出：只导出密码本这一段
    $('btnVaultExport').addEventListener('click', async () => {
      if (Store.isVaultLocked()) { requireUnlock(); return; }
      UI_.saveText('密码本_导出_' + new Date().toISOString().slice(0, 10) + '.md', await exportVaultMarkdown());
    });

    $('vpClose').addEventListener('click', () => UI_.closePage('vaultPage'));
    $('vpSave').addEventListener('click', saveEdit);
    $('vSaveBlock').addEventListener('click', saveEdit);
    $('vGen').addEventListener('click', () => {
      $('vPassword').value = generateStrongPassword(16);
      UI_.toast('已生成强密码，记得保存');
    });
    UI_.closeOnMask('pwdModal');

    $('pwdOk').addEventListener('click', onPwdOk);
    $('pwdCancel').addEventListener('click', () => UI_.closeModal('pwdModal'));

    const btnLock = $('btnLock');
    btnLock.addEventListener('click', () => {
      if (Store.isVaultLocked()) requireUnlock();
      else lockVault();
    });

    window.addEventListener('bz:lockstate', () => { refreshLockBtn(); renderLockTip(); });
    window.addEventListener('bz:requestUnlock', requireUnlock);
    window.addEventListener('bz:toggleMasterPwd', () => {
      if (Store.hasMasterPwd()) {
        UI_.actionSheet([
          { label: '重置主密码（已加密的凭据将无法再读取）', danger: true, onTap: () => resetPwd() },
          { label: '取消', cancel: true },
        ]);
      } else setupPwd();
    });

    // 旧版（0.3 以前）写下的主密码记录是坏的、永远解不开，且没有加密任何数据 → 自愈清掉
    if (Store.isBrokenPwdRecord()) {
      Store.clearVaultPwd();
      UI_.toast('检测到旧版失效的主密码记录，已自动清除，可重新设置');
    }

    refreshLockBtn();
    reload();
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  function buildTodosMd() {
    const todos = Store.getTodos();
    let md = '## 待办\n';
    todos.forEach(t => {
      const parts = [`- [${t.status === 'done' ? 'x' : ' '}] ${t.title}${t.owner === 'agent' ? '（Agent）' : ''}`];
      if (t.category) parts.push(`类别: ${t.category}`);
      if (t.priority) parts.push(`优先级: ${{ high: '高优', mid: '中', low: '低' }[t.priority] || t.priority}`);
      if (t.due) parts.push(`截止: ${t.due.replace('T', ' ')}`);
      if (t.note) parts.push(`备注: ${t.note}`);
      if ((t.deps || []).length) parts.push(`依赖: ${t.deps.length}项`);
      md += parts.join(' ｜ ') + '\n';
    });
    return md;
  }

  function buildVaultMd() {
    let md = '## 密码\n';
    list.forEach(v => {
      md += `### ${v.title}\n`;
      if (v.account) md += `- 账号: ${v.account}\n`;
      if (v.group) md += `- 归属: ${v.group}\n`;
      if (v.password) md += `- 密码: ${v.password}\n`;
      (Array.isArray(v.history) ? v.history : []).forEach(h => {
        if (h && h.pwd) md += `- 历史密码: ${h.pwd}${h.at ? ' ｜ ' + fmtAt(h.at) : ''}\n`;
      });
      if (v.url) md += `- URL: ${v.url}\n`;
      if (v.note) md += `- 备注: ${v.note}\n`;
      md += '\n';
    });
    return md;
  }

  async function exportMarkdown() {
    await reload();
    return `# 待办中心导出（${new Date().toISOString().slice(0, 10)}）\n\n`
      + buildTodosMd() + '\n' + buildVaultMd();
  }

  /** 只导出密码本（供密码本页右上角导出用），段落结构与完整导出一致，可被同一个导入器读回 */
  async function exportVaultMarkdown() {
    await reload();
    return `# 密码本导出（${new Date().toISOString().slice(0, 10)}）\n\n` + buildVaultMd();
  }

  /** 给外部（AI 助手 / 导入）用：往库里放一条密码，同账号自动并进历史 */
  async function addPassword(item) {
    list = await Store.getVault();
    const r = mergePassword(list, item);
    await Store.saveVault(list);
    renderAll();
    return r;
  }

  return {
    init, reload, getList, parseMarkdown, parseTodos, importMarkdown, addPassword, mergePassword,
    exportMarkdown, exportVaultMarkdown,
    setupPwd, resetPwd, lockVault, refreshLockBtn, generateStrongPassword, openEdit,
  };
})();
