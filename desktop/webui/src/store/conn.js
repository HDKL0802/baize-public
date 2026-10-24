// 连接状态 / 待审批 / 时钟 / 迷你小窗 的轮询状态（Pinia store）。
// 逻辑沿用旧 shell.js：8s 连一次 /api/health，12s 轮询一次 /api/state（待审批数），
// 4s 轮询一次审批弹窗队列，2.5s 轮询迷你小窗状态。
import { defineStore } from 'pinia';
import { API } from '../api';

export const useConnStore = defineStore('conn', {
  state: () => ({
    connected: false,
    statusText: '连接中…',
    serverText: '—',
    pendingCount: 0,
    mini: false,
    clock: '--:--',
    // 审批弹窗队列（kind: 'agent'|'task', it, total）
    approvals: [],
  }),
  actions: {
    start() {
      this.tickClock();
      this._tClock = setInterval(() => this.tickClock(), 20000);
      this._tConn = setInterval(() => this.pollConn(), 8000);
      this._tState = setInterval(() => this.pollState(), 12000);
      this._tApprove = setInterval(() => this.pollApprovals(), 4000);
      this._tMini = setInterval(() => this.pollMini(), 2500);
      this.pollConn();
      this.pollState();
      this.pollApprovals();
      this.pollMini();
    },
    stop() {
      for (const k of ['_tClock', '_tConn', '_tState', '_tApprove', '_tMini']) {
        if (this[k]) clearInterval(this[k]);
        this[k] = null;
      }
    },
    tickClock() {
      const d = new Date(), p = n => String(n).padStart(2, '0');
      this.clock = p(d.getHours()) + ':' + p(d.getMinutes());
    },
    async pollConn() {
      let r;
      try { r = await API.get('/api/health'); }
      catch (e) { r = { ok: false, error: String(e) }; }
      const d = (r && r.data) || {};
      if (r && r.ok) {
        this.connected = true;
        this.statusText = 'NAS 已连 · ' + (d.devicesOnline ?? '?') + ' 台在线';
      } else {
        this.connected = false;
      }
      const c = await API.localConfig();
      this.serverText = c.server || '未配置后端';
    },
    async pollState() {
      const r = await API.get('/api/state');
      if (!r.ok) { this.pendingCount = 0; return; }
      const tasks = (r.data && r.data.tasks) || [];
      this.pendingCount = tasks.filter(t => t.status === 'pending_approval').length;
    },
    async pollApprovals() {
      const [r1, r2] = await Promise.all([API.get('/api/agent/approvals'), API.get('/api/state')]);
      const queue = [];
      if (r1.ok) {
        ((r1.data && r1.data.approvals) || [])
          .filter(a => a.status === 'pending')
          .forEach(a => queue.push({ kind: 'agent', it: a }));
      }
      if (r2.ok) {
        ((r2.data && r2.data.tasks) || [])
          .filter(t => t.status === 'pending_approval')
          .forEach(t => queue.push({ kind: 'task', it: t }));
      }
      queue.sort((a, b) => ((a.it.at || a.it.createdAt || 0) - (b.it.at || b.it.createdAt || 0)));
      this.approvals = queue;
    },
    async pollMini() {
      let r = {};
      try { r = await (await fetch('/api/local/ball/mini')).json(); } catch (e) { r = {}; }
      this.mini = !!(r && r.mini);
    },
    // 决策审批（ApprovalModal 调用）
    async decideApproval(kind, id, ok, extra) {
      let r;
      if (kind === 'task') {
        r = ok
          ? await API.post('/api/tasks/' + encodeURIComponent(id) + '/approve', { by: '桌面端' })
          : await API.post('/api/tasks/' + encodeURIComponent(id) + '/reject', { by: '桌面端', reason: '桌面端驳回' });
      } else {
        r = ok
          ? await API.post('/api/agent/approvals/' + encodeURIComponent(id) + '/approve', { by: '桌面端', remember: extra || '' })
          : await API.post('/api/agent/approvals/' + encodeURIComponent(id) + '/reject', { by: '桌面端', reason: '桌面端拒绝' });
      }
      this.pollApprovals();
      this.pollState();
      return r;
    },
  },
});
