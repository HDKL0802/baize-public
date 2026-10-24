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
     - 密码: xxx
     - 备注: xxx
  ---------- */
  function parseMarkdown(text) {
    const items = [];
    let cur = null;
    text.split(/\r?\n/).forEach((raw) => {
      const line = raw.trim();
      if (!line) return;
      if (line.startsWith('#')) {
        if (cur && cur.title) items.push(cur);
        cur = { title: line.replace(/^#+\s*/, ''), account: '', password: '', url: '', note: '' };
      } else if (cur) {
        const m1 = line.match(/^[-\*]\s*账号[:：]\s*(.+)/);
        const m2 = line.match(/^[-\*]\s*密码[:：]\s*(.+)/);
        const m3 = line.match(/^[-\*]\s*URL[:：]\s*(.+)/i);
        const m4 = line.match(/^[-\*]\s*备注[:：]\s*(.+)/);
        if (m1) cur.account = m1[1].trim();
        else if (m2) cur.password = m2[1].trim();
        else if (m3) cur.url = m3[1].trim();
        else if (m4) cur.note = m4[1].trim();
      }
    });
    if (cur && cur.title) items.push(cur);
    return items.filter(x => x.title);
  }

  async function importMarkdown(text) {
    const parsed = parseMarkdown(text);
    let added = 0, merged = 0;
    for (const p of parsed) {
      const key = p.title + '|' + p.account;
      const dup = list.find(x => (x.title + '|' + x.account) === key);
      if (dup) {
        if (p.password && dup.password !== p.password) dup.password = p.password;
        merged++;
      } else {
        list.push({ id: Store.uid(), ...p, source: 'md', createdAt: Date.now() });
        added++;
      }
    }
    await Store.saveVault(list);
    renderAll();
    return { added, skipped: merged };
  }

  async function reload() { list = await Store.getVault(); renderAll(); }

  function renderAll() {
    const q = $('vaultSearch').value.trim().toLowerCase();
    const items = list.filter(v =>
      !q || (v.title + ' ' + v.account + ' ' + (v.url || '') + ' ' + (v.note || '')).toLowerCase().includes(q)
    );
    const box = $('vaultList');
    box.innerHTML = '';
    items.forEach(v => box.appendChild(card(v)));
    $('vaultEmpty').hidden = items.length > 0;
    $('vaultCount').textContent = `${items.length} 组密码`;
    renderLockTip();
  }

  function card(v) {
    const el = document.createElement('div');
    el.className = 'vault-card';
    const srcLabel = v.source === 'md' ? 'MD 导入' : v.source === 'voice' ? '语音录入' : v.source === 'agent' ? 'Agent 捕获' : '手动录入';
    el.innerHTML = `
      <div class="vc-row">
        <div class="vc-site">
          <div class="vc-icon">${esc((v.title || '?').charAt(0).toUpperCase())}</div>
          <div style="min-width:0">
            <div class="vc-title">${esc(v.title)}</div>
            <div class="vc-sub">${v.url ? esc(v.url) : srcLabel}</div>
          </div>
        </div>
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
          <div class="vc-field-label">密码凭据</div>
          <div class="vc-field-value" data-reveal="${v.id}">${'•'.repeat(Math.max(6, Math.min(14, (v.password || '').length || 8)))}</div>
        </div>
        <button class="btn ghost sm" data-eye="${v.id}">显示</button>
        <button class="btn primary sm" data-copy="${v.id}">复制密码</button>
      </div>
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
    return el;
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
    $('vUrl').value = v ? (v.url || '') : '';
    $('vNote').value = v ? (v.note || '') : '';
    UI_.openPage('vaultPage');
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
      password: $('vPassword').value,
      url: $('vUrl').value.trim(),
      note: $('vNote').value,
    };
    if (editingId) Object.assign(list.find(x => x.id === editingId), data);
    else list.push({ id: Store.uid(), ...data, source: 'manual', createdAt: Date.now() });
    await Store.saveVault(list);
    UI_.closePage('vaultPage');
    renderAll();
    UI_.toast(editingId ? '已保存' : '已存入自用密码库');
  }

  function del(v) {
    UI_.actionSheet([
      { label: `删除「${v.title}」`, danger: true, onTap: async () => { list = list.filter(x => x.id !== v.id); await Store.saveVault(list); renderAll(); UI_.toast('已删除'); } },
      { label: '取消', cancel: true },
    ]);
  }

  /* ---------- 主密码 ---------- */
  function renderLockTip() {
    const tip = $('vaultLockTip');
    tip.onclick = null;
    if (!Store.hasMasterPwd()) {
      tip.className = 'vault-lock-tip';
      tip.innerHTML = '🔓 未设置主密码，凭据将以明文存储。点此设为 AES-GCM 加密。';
      tip.style.display = 'block';
      tip.onclick = () => window.dispatchEvent(new CustomEvent('bz:toggleMasterPwd'));
    } else if (Store.isVaultLocked()) {
      tip.className = 'vault-lock-tip';
      tip.innerHTML = '🔒 密码本已锁定。点此输入主密码解锁。';
      tip.style.display = 'block';
      tip.onclick = () => requireUnlock();
    } else {
      tip.className = 'vault-lock-tip safe';
      tip.innerHTML = '🔐 已解锁 · 凭据以 AES-GCM 加密存储于本机';
      tip.style.display = 'block';
    }
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
    UI_.toast('已清除主密码');
    refreshLockBtn(); renderLockTip();
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
      if (!(await Store.unlock(pwd))) { UI_.toast('密码错误'); $('pwdInput').value = ''; return; }
      UI_.closeModal('pwdModal'); renderAll(); refreshLockBtn();
      UI_.toast('已解锁');
    }
  }

  function refreshLockBtn() {
    const btn = $('btnLock');
    btn.hidden = !Store.hasMasterPwd();
    btn.classList.toggle('warn', !Store.isVaultLocked());
  }

  function init() {
    $('vaultSearch').addEventListener('input', renderAll);
    $('btnVaultAdd').addEventListener('click', () => openEdit(null));
    $('btnVaultImport').addEventListener('click', () => {
      if (Store.isVaultLocked()) { requireUnlock(); return; }
      $('fileImport').click();
    });
    $('fileImport').addEventListener('change', async (e) => {
      const file = e.target.files[0];
      if (!file) return;
      e.target.value = '';
      if (Store.isVaultLocked()) { requireUnlock(); return; }
      const r = await importMarkdown(await file.text());
      UI_.toast(`导入完成：新增 ${r.added} 条${r.skipped ? '，合并 ' + r.skipped + ' 条' : ''}`);
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
      if (Store.isVaultLocked()) { requireUnlock(); return; }
      Store.lock(); refreshLockBtn(); renderLockTip(); UI_.toast('密码本已锁定');
    });

    window.addEventListener('bz:lockstate', () => { refreshLockBtn(); renderLockTip(); });
    window.addEventListener('bz:requestUnlock', requireUnlock);
    window.addEventListener('bz:toggleMasterPwd', () => {
      if (Store.hasMasterPwd()) {
        UI_.actionSheet([
          { label: '重置主密码（凭据保留）', onTap: () => resetPwd() },
          { label: '取消', cancel: true },
        ]);
      } else setupPwd();
    });

    refreshLockBtn();
    reload();
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  async function exportMarkdown() {
    const vault = await Store.getVault();
    const todos = Store.getTodos();
    const now = new Date();
    let md = `# 待办中心导出（${now.toISOString().slice(0, 10)}）\n\n## 待办\n`;
    todos.forEach(t => {
      const parts = [`- [${t.status === 'done' ? 'x' : ' '}] ${t.title}${t.owner === 'agent' ? '（Agent）' : ''}`];
      if (t.category) parts.push(`类别: ${t.category}`);
      if (t.priority) parts.push(`优先级: ${{ high: '高优', mid: '中', low: '低' }[t.priority] || t.priority}`);
      if (t.due) parts.push(`截止: ${t.due.replace('T', ' ')}`);
      if (t.note) parts.push(`备注: ${t.note}`);
      if ((t.deps || []).length) parts.push(`依赖: ${t.deps.length}项`);
      md += parts.join(' ｜ ') + '\n';
    });
    md += `\n## 密码\n`;
    vault.forEach(v => {
      md += `### ${v.title}\n`;
      if (v.account) md += `- 账号: ${v.account}\n`;
      if (v.password) md += `- 密码: ${v.password}\n`;
      if (v.url) md += `- URL: ${v.url}\n`;
      if (v.note) md += `- 备注: ${v.note}\n`;
      md += '\n';
    });
    return md;
  }

  return { init, reload, parseMarkdown, importMarkdown, exportMarkdown, setupPwd, resetPwd, refreshLockBtn, generateStrongPassword };
})();
