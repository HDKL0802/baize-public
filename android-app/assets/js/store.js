/* 白泽待办中心 - 存储层（本地优先，密码本 AES-GCM 加密） */
'use strict';
window.Store = (function () {
  const K = {
    todos: 'bz_todos',
    settings: 'bz_settings',
    vaultPlain: 'bz_vault',   // 未设置主密码时用
    vaultEnc: 'bz_vault_enc', // 设置主密码后：{iv, data} Base64
    pwdHash: 'bz_vault_pwd',  // 主密码的 PBKDF2 校验哈希 {salt, hash}
  };

  function read(key, fallback) {
    try {
      const raw = localStorage.getItem(key);
      if (raw === null || raw === undefined) return fallback;
      return JSON.parse(raw);
    } catch (e) { return fallback; }
  }
  function write(key, val) {
    localStorage.setItem(key, JSON.stringify(val));
  }

  /* ---------- 密码学（WebCrypto，Go 版将对应使用 Argon2 + AES-GCM） ---------- */
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
  async function encryptBytes(key, dataBuf) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, dataBuf);
    return { iv: bufToB64(iv), data: bufToB64(ct) };
  }
  async function decryptBytes(key, box) {
    const pt = await crypto.subtle.decrypt({ name: 'AES-GCM', iv: b64ToBuf(box.iv), }, key, b64ToBuf(box.data));
    return new TextDecoder().decode(pt);
  }

  /* ---------- 设置 ---------- */
  const defaultSettings = {
    model: { provider: 'deepseek', baseUrl: 'https://api.deepseek.com/v1', apiKey: '', model: 'deepseek-chat' },
    widget: '1x1',
    notify: false,
    remind: true,
    autoExecAgentTodo: false,
    planPolicy: 'balance',
  };
  function getSettings() { return Object.assign({}, defaultSettings, read(K.settings, {})); }
  function saveSettings(s) { write(K.settings, s); }

  /* ---------- 待办 ---------- */
  function getTodos() { return read(K.todos, []); }
  function saveTodos(list) { write(K.todos, list); }

  function sampleTodos() {
    const now = new Date(Date.now() + 3600e3);
    const pad = (n) => String(n).padStart(2, '0');
    const fmt = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const base = { weekly: false, estimate: 0, deps: [], owner: 'user', status: 'todo', remind: false };
    return [
      { ...base, id: uid(), title: '整理本周工作计划', category: '工作开发', priority: 'high', form: 'schedule', due: fmt(now), estimate: 60, note: '示例待办', remind: true, createdAt: Date.now() },
      { ...base, id: uid(), title: '复习 Go 基础', category: '工作开发', priority: 'mid', form: 'schedule', due: fmt(new Date(now.getTime() + 3600e3)), weekly: true, estimate: 45, createdAt: Date.now() },
      { ...base, id: uid(), title: 'AI 漫剧角色设定与主线大纲设计', category: '创作（AI漫剧等）', priority: 'mid', form: 'leisure', due: '', estimate: 90, note: '灵感项目，有空时推进', createdAt: Date.now() },
      { ...base, id: uid(), title: '给爸妈买牛奶', category: '日常生活', priority: 'low', form: 'leisure', due: '', estimate: 15, createdAt: Date.now() },
    ];
  }

  /* ---------- 密码本 ---------- */
  async function getVault() {
    if (masterKey) {
      const box = read(K.vaultEnc, null);
      if (!box) return [];
      try { return JSON.parse(await decryptBytes(masterKey, box)); }
      catch (e) { return []; }
    }
    return read(K.vaultPlain, []);
  }
  async function saveVault(list) {
    if (masterKey) {
      const box = await encryptBytes(masterKey, new TextEncoder().encode(JSON.stringify(list)));
      write(K.vaultEnc, box);
      localStorage.removeItem(K.vaultPlain);
    } else {
      write(K.vaultPlain, list);
    }
  }
  function isVaultLocked() { return !!localStorage.getItem(K.pwdHash) && !masterKey; }
  function hasMasterPwd() { return !!localStorage.getItem(K.pwdHash); }

  async function verifyMasterPwd(pwd) {
    const rec = read(K.pwdHash, null);
    if (!rec) return false;
    const key = await derive(pwd, b64ToBuf(rec.salt));
    const probe = new TextEncoder().encode('probe');
    const enc = await encryptBytes(key, probe);
    return enc.data === rec.probe;
  }
  async function setMasterPwd(pwd) {
    const salt = crypto.getRandomValues(new Uint8Array(16));
    const key = await derive(pwd, salt);
    masterKey = key; // 立即生效，后续写入均走加密
    const probe = await encryptBytes(key, new TextEncoder().encode('probe'));
    // 迁移现有明文凭据到加密库
    const current = read(K.vaultPlain, []);
    if (current.length) {
      const box = await encryptBytes(key, new TextEncoder().encode(JSON.stringify(current)));
      write(K.vaultEnc, box);
    }
    write(K.pwdHash, { salt: bufToB64(salt), probe: probe.data });
    localStorage.removeItem(K.vaultPlain);
  }
  async function unlock(pwd) {
    if (!(await verifyMasterPwd(pwd))) return false;
    const rec = read(K.pwdHash, null);
    masterKey = await derive(pwd, b64ToBuf(rec.salt));
    return true;
  }
  function lock() { masterKey = null; }
  function clearVaultPwd() { lock(); localStorage.removeItem(K.pwdHash); masterKey = null; }

  function uid() {
    return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
  }

  return {
    getSettings, saveSettings, getTodos, saveTodos, sampleTodos,
    getVault, saveVault, isVaultLocked, hasMasterPwd,
    setMasterPwd, unlock, verifyMasterPwd, lock, clearVaultPwd,
    uid,
  };
})();