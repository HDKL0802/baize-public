/* 白泽 · WebUI 手机版：把「本机内核桥」换成「后端直连」
   ─────────────────────────────────────────────────────
   手机 App 里 window.BzDevice 的链路是：BzNative(Java) → 本机 Go 内核(libbzcore) → 后端；
   浏览器里没有本机内核，所以这里实现**同一套接口**，直接打后端。
   能这么干，是因为后端的路由跟内核那套是对齐的（见 kb_api.go 的头注释）：
     内核 /api/op      → 后端 /api/kb/op
     内核 /api/state   → 后端 /api/kb/state（返回体要多包一层 {ok,data}，见 remoteState）
     内核 /api/kb/*    → 后端 /api/kb/*（同名同形）
     内核 /api/agent/* → 后端 /api/agent/*（同名同形）

   两条必须守住的约定：
   1. **调用是同步的**。Store.flush / kb.js 都拿返回值直接读 r.ok、r.files；
      内核桥本身就是同步调用，所以这里用**同步 XMLHttpRequest** 对齐同一套语义
      （改成 async 会连带把 store.js / kb.js / chat.js 一大片都改掉）。
   2. **「配了后端」必须同步就绪**。页面本来就是后端给的 → 这件事在加载时就成立；
      只有「此刻通不通」才去探（探失败标 stale，界面显示离线、改动进队列等补推）。

   令牌由后端注入在 index.html 里（__BAIZE_TOKEN__ 占位符），网页里不用手填。 */
'use strict';

/** 给被移植过来的界面代码认的标志：这是网页版（没有本机内核） */
window.__bzWeb = true;

window.BzDevice = (function () {
  /** 配对令牌由后端注入在 <head> 的 meta 里（这一页本身就是令牌闸门之后才发的） */
  function tokenFromHead() {
    var m = document.querySelector('meta[name="baize-token"]');
    return (m && m.getAttribute('content')) || '';
  }
  var TOKEN = tokenFromHead();
  var ORIGIN = location.origin;

  /* ---- 内核路径 → 后端路径 ----
     · 两条本机接口跟后端不同名（见下）；
     · /api/be/ 是内核的「通用透传」前缀（core/cmd/bzcore：/api/be/api/x → 后端 /api/x），照抄；
     · 其余（/api/kb/*、/api/agent/*）同名同形，原样转发。 */
  var PATH_MAP = {
    '/api/op': '/api/kb/op',
    '/api/state': '/api/kb/state',
  };
  var BE_PREFIX = '/api/be';

  function mapPath(p) {
    var q = p.indexOf('?');
    var path = q < 0 ? p : p.slice(0, q);
    var query = q < 0 ? '' : p.slice(q);
    var mapped;
    if (path === BE_PREFIX || path.indexOf(BE_PREFIX + '/') === 0) {
      mapped = path.slice(BE_PREFIX.length) || '/';
    } else if (Object.prototype.hasOwnProperty.call(PATH_MAP, path)) {
      mapped = PATH_MAP[path];
    } else {
      mapped = path;
    }
    return mapped + query;
  }

  /* ---- 同步 HTTP：成功回后端原始 JSON（不另包信封），失败回 {ok:false,error} ----
     为什么不统一包一层 ok：kb.js 直接读 r.files、chat.js 直接读 r.messages，
     它们的 errOf()/unpack() 都按「后端原始形状」写。只有 /api/state 是例外。 */
  function xhr(path, method, body) {
    var x = new XMLHttpRequest();
    try {
      x.open(method || 'GET', ORIGIN + mapPath(path), false); // false = 同步，跟内核桥对齐
    } catch (e) {
      return { ok: false, error: '请求发不出去：' + ((e && e.message) || e) };
    }
    try { x.setRequestHeader('X-Baize-Token', TOKEN); } catch (e) { /* 忽略 */ }
    if (body != null) {
      try { x.setRequestHeader('Content-Type', 'application/json; charset=utf-8'); } catch (e) { /* 忽略 */ }
    }
    try {
      x.send(body == null ? null : (typeof body === 'string' ? body : JSON.stringify(body)));
    } catch (e) {
      return { ok: false, error: '连不上后端（' + ((e && e.message) || '网络不通') + '）' };
    }
    var text = '';
    try { text = x.responseText || ''; } catch (e) { text = ''; }
    var data = null;
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
    if (x.status < 200 || x.status >= 300) {
      var msg = (data && (data.error || data.message)) || ('HTTP ' + x.status);
      return { ok: false, error: msg, status: x.status };
    }
    if (data == null) return { ok: true };
    return data;
  }

  /* ================= 后端知识库状态 ================= */

  var remoteStatus = null;   // 最后一次探测结果
  var remoteError = '';      // 最近一次探测失败的原因

  /** 探一次「后端此刻通不通」。先**同步**把「配了后端」立住，再去核连通性。 */
  function refreshRemote() {
    remoteStatus = { at: Date.now(), enabled: true, remote: true, stale: false, server: ORIGIN };
    var h = xhr('/api/health', 'GET');
    if (h.ok === false) {
      remoteStatus.stale = true;
      remoteStatus.reason = h.error || '后端此刻不通';
      remoteStatus.error = remoteStatus.reason;
      remoteError = remoteStatus.reason;
    } else {
      remoteError = '';
    }
    return remoteStatus;
  }
  function remoteInfo() { if (!remoteStatus) refreshRemote(); return remoteStatus; }
  function remoteLastError() { return remoteError; }
  /** 页面由后端给 → 必然是「配了后端」，且同步就绪（Store 依赖这一点） */
  function remoteConfigured() { return true; }
  function remoteReady() { if (!remoteStatus) refreshRemote(); return !!remoteStatus && !remoteStatus.stale; }

  /** 往后端知识库发一条操作。返回 {ok:true,data} 或 {ok:false,error} */
  function remoteOp(op, args) {
    var r = xhr('/api/op', 'POST', JSON.stringify({ op: op, args: args || {} }));
    if (r.ok === false) {
      remoteError = r.error || '调后端失败';
      remoteStatus = Object.assign({ at: Date.now() }, remoteStatus || {}, { stale: true, reason: remoteError });
    }
    return r;
  }

  /** 取后端知识库快照：内核那侧回的是 {ok,data} 信封，所以这里补上这一层 */
  function remoteState() {
    var r = xhr('/api/state', 'GET');
    if (r.ok === false) return { ok: false, error: r.error || '取知识库失败' };
    return { ok: true, data: r };
  }

  /* ================= 内核进程状态（网页版没有内核：如实说没有） ================= */

  function processStatus() {
    return { web: true, native: false, supported: false, enabled: false, running: false, port: 0, server: ORIGIN };
  }
  function linkStatus() {
    return { connected: true, web: true, server: ORIGIN, deviceId: 'web-browser' };
  }
  function configure() {
    return { web: true, native: false, supported: false, error: '网页版不用配跨端：数据就直接在这个后端上' };
  }

  /* ================= 跟白泽智能体说话（同名路径原样转发） ================= */

  /** 内核那套本机接口的通用入口（kb.js 就是拿它打 /api/kb/files、/api/agent/memory 等） */
  function call(path, method, body) { return xhr(path, method, body); }

  function agentRun(goal, wait) { return xhr('/api/agent/run', 'POST', JSON.stringify({ goal: goal, wait: !!wait })); }
  function agentState() { return xhr('/api/agent/state', 'GET'); }
  function agentRunDetail(id) { return xhr('/api/agent/runs/' + encodeURIComponent(id), 'GET'); }
  function agentCommands() { return xhr('/api/agent/commands', 'GET'); }
  function agentApprovals() { return xhr('/api/agent/approvals', 'GET'); }
  function agentApprove(id) { return xhr('/api/agent/approvals/' + encodeURIComponent(id) + '/approve', 'POST', JSON.stringify({ by: '网页' })); }
  function agentReject(id, reason) { return xhr('/api/agent/approvals/' + encodeURIComponent(id) + '/reject', 'POST', JSON.stringify({ by: '网页', reason: reason || '' })); }

  /* ---- 待办镜像：那是「本机内核」时代的路子；网页版正本就在后端，什么都不用做 ---- */
  function pushTodos() { /* 网页版：正本已在后端，不镜像 */ }
  function mirrorWhenReady() { /* 同上 */ }
  function lastSyncInfo() { return null; }

  return {
    web: true,
    available: function () { return true; },
    processStatus: processStatus, linkStatus: linkStatus, configure: configure, call: call,
    refreshRemote: refreshRemote, remoteInfo: remoteInfo, remoteLastError: remoteLastError,
    remoteConfigured: remoteConfigured, remoteReady: remoteReady, remoteOp: remoteOp, remoteState: remoteState,
    agentRun: agentRun, agentState: agentState, agentRunDetail: agentRunDetail, agentCommands: agentCommands,
    agentApprovals: agentApprovals, agentApprove: agentApprove, agentReject: agentReject,
    pushTodos: pushTodos, mirrorWhenReady: mirrorWhenReady, lastSyncInfo: lastSyncInfo,
  };
})();
