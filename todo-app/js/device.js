/* 白泽 · 跨端（把这台手机接进白泽后端）
   只有 APK 里才有这一层：壳会把 Go 内核（bzcore）拉起来，
   内核再以「设备」身份连后端，同时把数据读写代理到后端的知识库。

   两条约定：
   1. 正本在后端知识库（NAS）。手机这边只留一份待办缓存，断网还能看见列表；
      密码本不缓存，一律联网读。
   2. 内核的本机接口由原生层代调用（BzNative.bzCoreCall）：既绕开 WebView 的跨源限制，
      配对令牌也只留在原生层。任何一步失败都要能把原因显示出来，不许静默失败。 */
'use strict';
window.BzDevice = (function () {
  function nb() {
    return (window.BzNative && typeof window.BzNative.bzCoreStatus === 'function')
      ? window.BzNative : null;
  }
  function available() { return !!nb(); }

  function parse(s) {
    try { return JSON.parse(s || '{}'); } catch (e) { return {}; }
  }

  /** 内核进程本身的状态（壳给的）：supported(安装包里有没有内核)/enabled/running/port/error/server/pairTokenSet */
  function processStatus() {
    const b = nb();
    // native = 这一层能不能用（在不在手机 App 里）。supported 由壳如实报，别覆盖它
    if (!b) return { native: false, supported: false };
    const st = parse(b.bzCoreStatus());
    st.native = true;
    return st;
  }

  /** 代调内核接口，返回 {ok, data} 或 {ok:false, error} */
  function call(path, method, body) {
    const b = nb();
    if (!b) return { ok: false, error: '当前入口不是手机 App，没有本机内核' };
    let raw = '';
    try {
      raw = b.bzCoreCall(path, method || 'GET', body == null ? null : body);
    } catch (e) {
      return { ok: false, error: '调内核失败：' + (e.message || e) };
    }
    if (!raw) return { ok: false, error: '内核没有响应（可能还没起来）' };
    const out = parse(raw);
    if (out.ok === undefined && out.error) return { ok: false, error: out.error };
    return out;
  }

  /** 内核自己报的跨端连接状态：connected/deviceId/server/lastError/actions */
  function linkStatus() {
    const r = call('/api/device', 'GET');
    return r.ok && r.data ? r.data : { error: r.error || '内核没回状态' };
  }

  /** 保存后端地址与配对令牌并立即重启内核（令牌留空 = 不改已存的那串） */
  function configure(server, pairToken, enabled) {
    const b = nb();
    if (!b) return { native: false, supported: false, error: '当前入口不是手机 App' };
    const st = parse(b.bzCoreConfigure(server || '', pairToken || '', !!enabled));
    st.native = true;
    remoteStatus = null;   // 换了后端，之前那份状态作废
    return st;
  }

  /* ---- 后端知识库状态（正本在不在这边） ---- */

  let remoteStatus = null;   // 最后一次成功探到的状态
  let remoteError = '';      // 最近一次探测失败的原因

  /** 问内核「后端通不通、我读到的是不是正本」。失败也记下来，不吞。 */
  function refreshRemote() {
    if (!available()) {
      remoteStatus = { at: Date.now(), enabled: false, remote: false, stale: false };
      remoteError = '不在手机 App 里';
      return remoteStatus;
    }
    const r = call('/api/remote', 'GET');
    if (r.ok && r.data) {
      remoteStatus = Object.assign({ at: Date.now() }, r.data);
      remoteError = '';
      return remoteStatus;
    }
    // 探测失败：**保留上一次「有没有配后端」的结论**，只把原因记下来。
    // 不能因为一次探不到就把模式判成「本机模式」——那样接下来的改动会静默留在手机上，
    // 用户会以为已经存进 NAS 了。
    remoteError = r.error || '内核没回状态';
    if (remoteStatus) {
      remoteStatus = Object.assign({}, remoteStatus, { stale: true, reason: remoteError });
    } else {
      const st = processStatus();
      const configured = !!(st.enabled && st.server);
      remoteStatus = {
        at: Date.now(), enabled: configured, remote: configured,
        stale: true, reason: remoteError, server: st.server || '',
      };
    }
    return remoteStatus;
  }

  /** 上一次探测的结果（同步，供界面直接判断） */
  function remoteInfo() { return remoteStatus; }
  function remoteLastError() { return remoteError; }

  /** 有没有配后端（配了就该走知识库模式，连不上是另一回事：改动进队列等补推） */
  function remoteConfigured() { return !!(remoteStatus && remoteStatus.enabled); }

  /** 现在能不能读写后端知识库（配了 + 这一次探到了） */
  function remoteReady() { return remoteConfigured() && !remoteStatus.stale; }

  /** 往后端知识库发一条操作。返回 {ok, data} 或 {ok:false, error} */
  function remoteOp(op, args) {
    const r = call('/api/op', 'POST', JSON.stringify({ op: op, args: args || {} }));
    if (!r.ok) {
      // 连不上就把状态标脏，界面能立刻看到「离线」
      remoteError = r.error || '调后端失败';
      remoteStatus = Object.assign({ at: Date.now() }, remoteStatus || {}, { stale: true, reason: remoteError });
    }
    return r;
  }

  /** 取后端知识库快照（待办 + 密码本） */
  function remoteState() {
    const r = call('/api/state', 'GET');
    if (!r.ok) return { ok: false, error: r.error || '取知识库失败' };
    return { ok: true, data: r.data || {} };
  }

  /** 跟白泽智能体说话：转给后端 Agent（走内核代理，令牌不出原生层） */
  function agentRun(goal, wait) {
    return call('/api/agent/run', 'POST', JSON.stringify({ goal: goal, wait: !!wait }));
  }
  function agentState() { return call('/api/agent/state', 'GET'); }
  function agentRunDetail(id) { return call('/api/agent/runs/' + encodeURIComponent(id), 'GET'); }
  function agentCommands() { return call('/api/agent/commands', 'GET'); }
  function agentApprovals() { return call('/api/agent/approvals', 'GET'); }
  function agentApprove(id) { return call('/api/agent/approvals/' + encodeURIComponent(id) + '/approve', 'POST', JSON.stringify({ by: '手机' })); }
  function agentReject(id, reason) { return call('/api/agent/approvals/' + encodeURIComponent(id) + '/reject', 'POST', JSON.stringify({ by: '手机', reason: reason || '' })); }

  /* ---- 待办镜像（纯本机模式下的老路子：把整表推给内核，让后端查得到） ---- */

  let lastSync = null;  // {at, count, error}

  function pushTodos(list) {
    const st = processStatus();
    if (!st.enabled || !st.running || !st.port) return;
    // 跨端模式下正本在后端，本机这份只是缓存 —— 绝不能把缓存当"本机数据"镜像给内核：
    // 内核本机库平时就存着后端待办缓存，一旦后端被清空，首次迁移会把这份缓存迁回去（条目复活）。
    // 注意判断用 remoteConfigured()（"配了后端"）而不是 remoteReady()（"配了且此刻通"）：
    // 后者在后端暂时不可用时为假，正好会把缓存写进去，正是踩过的坑。
    if (remoteConfigured()) return;
    const todos = list || Store.getTodos();
    const r = call('/api/op', 'POST', JSON.stringify({
      op: 'todo.replaceAll', args: { todos: todos },
    }));
    lastSync = {
      at: Date.now(), count: todos.length,
      error: r.ok ? '' : (r.error || '推送失败'),
    };
  }

  /** 内核起来要花一两秒，等它准备好了再推第一份（之后每次改动都会推） */
  function mirrorWhenReady(tries) {
    const st = processStatus();
    if (!st.enabled) return;
    if (st.running && st.port) { pushTodos(); return; }
    const left = typeof tries === 'number' ? tries : 8;
    if (left <= 0) return;
    setTimeout(() => mirrorWhenReady(left - 1), 1500);
  }

  function lastSyncInfo() { return lastSync; }

  return {
    available, processStatus, linkStatus, call, configure,
    refreshRemote, remoteInfo, remoteLastError, remoteConfigured, remoteReady, remoteOp, remoteState,
    agentRun, agentState, agentRunDetail, agentCommands, agentApprovals, agentApprove, agentReject,
    pushTodos, mirrorWhenReady, lastSyncInfo,
  };
})();
