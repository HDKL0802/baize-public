import { defineStore } from 'pinia';
import { call, remoteState, remoteOp, probeRemote, bzCoreStatus, uid } from './api.js';

/* 白泽手机端 Pinia store
   移植自 mobile/todo/js/store.js，两种模式：
   ① 本机模式：localStorage（密码本 AES-GCM 加密）
   ② 知识库模式：正本在 NAS，本机只留待办缓存 */

const K = {
  todos: 'bz_todos',
  outbox: 'bz_outbox',
  remote: 'bz_remote_snapshot',
  settings: 'bz_settings',
  vaultPlain: 'bz_vault',
  vaultEnc: 'bz_vault_enc',
  pwdHash: 'bz_vault_pwd',
  chat: 'bz_chat',
  chats: 'bz_chats',
  theme: 'bz_theme',
};

function read(key, fallback) {
  try {
    const raw = localStorage.getItem(key);
    if (raw === null || raw === undefined) return fallback;
    return JSON.parse(raw);
  } catch { return fallback; }
}
function write(key, val) {
  try { localStorage.setItem(key, JSON.stringify(val)); }
  catch (e) { throw new Error('本机存储写不进去了（多半是附件缩略图占满了），删掉一些再试'); }
}

export const useStore = defineStore('baize', {
  state: () => ({
    todos: read(K.todos, []),
    chats: null,           // 延迟加载
    settings: Object.assign(
      { ai: { protocol: 'openai', baseUrl: '', apiKey: '', model: '' }, chatAgents: [], notify: false, remind: true, planPolicy: 'balance' },
      read(K.settings, {})
    ),
    theme: read(K.theme, 'dark'),
    kbMode: false,         // 是否知识库模式（配了后端）
    kbOnline: false,       // 后端此刻通不通
    kbInfo: null,          // 后端知识库信息
    outbox: read(K.outbox, []),
    pendingCount: 0,       // 待审批数量
    toastMsg: '',
    toastKind: '',
    toastTimer: null,
  }),

  getters: {
    settings: (s) => s.settings,
    todosList: (s) => s.todos,
    outboxCount: (s) => s.outbox.length,
    currentTheme: (s) => s.theme,
  },

  actions: {
    // ---------- toast ----------
    toast(msg, kind = '') {
      this.toastMsg = msg;
      this.toastKind = kind;
      clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => { this.toastMsg = ''; }, 3600);
    },

    // ---------- 设置 ----------
    getSettings() { return this.settings; },
    saveSettings(s) {
      this.settings = Object.assign({}, this.settings, s);
      write(K.settings, this.settings);
      window.dispatchEvent(new CustomEvent('bz:aichanged'));
    },

    // ---------- 待办 ----------
    getTodos() { return this.todos; },
    saveTodos(list) {
      this.todos = Array.isArray(list) ? list : [];
      write(K.todos, this.todos);
      // 知识库模式：diff 推给后端
      if (this.kbMode) {
        const prev = read(K.remote, this.todos);
        const ops = this.diffTodos(prev, this.todos);
        if (ops.length) {
          this.outbox = this.outbox.concat(ops);
          this.flushOutbox();
        }
      }
    },
    diffTodos(prev, next) {
      const ops = [];
      const prevById = {};
      (prev || []).forEach(t => { if (t && t.id) prevById[t.id] = t; });
      const seen = {};
      (next || []).forEach(t => {
        if (!t || !t.id) return;
        seen[t.id] = true;
        const old = prevById[t.id];
        if (!old || JSON.stringify(old) !== JSON.stringify(t)) ops.push({ op: 'todo.upsert', args: t });
      });
      (prev || []).forEach(t => {
        if (t && t.id && !seen[t.id]) ops.push({ op: 'todo.remove', args: { id: t.id } });
      });
      return ops;
    },
    flushOutbox() {
      if (!this.outbox.length) return;
      let list = this.outbox;
      // 对账
      const have = {};
      this.todos.forEach(t => { if (t && t.id) have[t.id] = true; });
      list = list.filter(it => {
        if (!it || !it.op) return false;
        const a = it.args || {};
        if (it.op === 'todo.upsert') return !!(a.id && have[a.id]);
        if (it.op === 'todo.remove') return !have[a.id];
        return true;
      });
      this.outbox = list;
      write(K.outbox, list.length ? list : null);
      for (const item of list) {
        const r = remoteOp(item.op, item.args);
        if (!r.ok) {
          write(K.outbox, this.outbox);
          this.outbox = read(K.outbox, []);
          return;
        }
        this.outbox.shift();
        write(K.outbox, this.outbox.length ? this.outbox : null);
      }
      this.toast('已同步到 NAS', 'ok');
    },

    // ---------- 知识库同步 ----------
    async refreshRemote() {
      const r = await probeRemote();
      if (r.ok && r.data) {
        this.kbInfo = r.data;
        this.kbMode = !!r.data.enabled;
        this.kbOnline = !!r.data.enabled && !r.data.stale;
      }
      window.dispatchEvent(new CustomEvent('bz:syncdone'));
    },
    async syncFromRemote() {
      if (!this.kbMode) return { ok: false, error: '没连上白泽后端' };
      const f = this.flushOutbox();
      const r = await remoteState();
      if (!r.ok) return { ok: false, error: r.error || '取知识库失败' };
      const snap = r.data || {};
      const remoteTodos = Array.isArray(snap.todos) ? snap.todos : [];
      this.todos = remoteTodos;
      write(K.todos, remoteTodos);
      write(K.remote, remoteTodos);
      write(K.outbox, null);
      this.outbox = [];
      window.dispatchEvent(new CustomEvent('bz:syncdone'));
      return { ok: true, todos: remoteTodos.length };
    },

    // ---------- 对话（多会话） ----------
    loadChats() {
      if (this.chats) return this.chats;
      let all = read(K.chats, null);
      if (!all || !Array.isArray(all.list) || !all.list.length) {
        const old = read(K.chat, []);
        all = { active: '', list: [] };
        if (Array.isArray(old) && old.length) {
          all.list.push({ id: uid(), title: this.titleOf(old), ts: Date.now(), msgs: old });
        }
        if (!all.list.length) all.list.push({ id: uid(), title: '', ts: Date.now(), msgs: [] });
        all.active = all.list[0].id;
        write(K.chats, all);
      }
      if (!all.list.some(c => c.id === all.active)) all.active = all.list[0].id;
      this.chats = all;
      return all;
    },
    titleOf(list) {
      const first = (list || []).find(m => m && m.role === 'user' && m.text);
      if (!first) return '';
      const t = String(first.text).replace(/\s+/g, ' ').trim();
      return t.length > 16 ? t.slice(0, 16) + '…' : t;
    },
    activeChat() {
      const all = this.loadChats();
      return all.list.find(c => c.id === all.active) || all.list[0];
    },
    getChat() { return (this.activeChat() || { msgs: [] }).msgs || []; },
    saveChat(list) {
      const all = this.loadChats();
      const c = all.list.find(x => x.id === all.active);
      if (!c) return;
      c.msgs = list || [];
      c.ts = Date.now();
      if (!c.title) c.title = this.titleOf(c.msgs);
      this.chats = all;
      write(K.chats, all);
    },
    chatList() {
      const all = this.loadChats();
      return all.list.map(c => ({
        id: c.id, active: c.id === all.active,
        title: c.title || this.titleOf(c.msgs) || '新会话',
        ts: c.ts || 0, count: (c.msgs || []).length,
      }));
    },
    newChat() {
      const all = this.loadChats();
      const c = { id: uid(), title: '', ts: Date.now(), msgs: [] };
      all.list.push(c);
      all.active = c.id;
      this.chats = all;
      write(K.chats, all);
      return c.id;
    },
    switchChat(id) {
      const all = this.loadChats();
      if (!all.list.some(c => c.id === id)) return false;
      all.active = id;
      this.chats = all;
      write(K.chats, all);
      return true;
    },
    delChat(id) {
      const all = this.loadChats();
      all.list = all.list.filter(c => c.id !== id);
      if (!all.list.length) all.list.push({ id: uid(), title: '', ts: Date.now(), msgs: [] });
      if (!all.list.some(c => c.id === all.active)) all.active = all.list[0].id;
      this.chats = all;
      write(K.chats, all);
      return all.active;
    },
    clearAllChats() {
      const all = this.loadChats();
      all.list = [{ id: uid(), title: '', ts: Date.now(), msgs: [] }];
      all.active = all.list[0].id;
      this.chats = all;
      write(K.chats, all);
    },

    // ---------- 主题 ----------
    setTheme(t) {
      this.theme = t;
      write(K.theme, t);
      document.documentElement.setAttribute('data-theme', t === 'system' ? 'dark' : t);
    },

    // ---------- 密码本（知识库模式走后端，本机模式走 localStorage） ----------
    async getVault() {
      if (this.kbMode) {
        const r = await remoteState();
        if (!r.ok) return { ok: false, error: r.error };
        return { ok: true, list: (r.data && r.data.vault) || [] };
      }
      return { ok: true, list: read(K.vaultPlain, []) };
    },
    async saveVault(list) {
      if (this.kbMode) {
        // 简化：整表推（TODO: diff 推）
        const r = await remoteOp('vault.replaceAll', { vault: list });
        if (!r.ok) throw new Error(r.error || '同步失败');
        return;
      }
      write(K.vaultPlain, list);
    },
    isVaultLocked() {
      if (this.kbMode) return !!read('bz_vault_flags', {}).locked;
      return !!localStorage.getItem(K.pwdHash);
    },
  },
});
