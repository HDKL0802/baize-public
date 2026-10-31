<!-- 两人通话（call）：登录 → 选组/开 → WebRTC → AI 文档判定 -->
<script setup>
import { ref, reactive, onMounted, onBeforeUnmount, inject, computed } from 'vue';
import { api, local, session, SESSION_KEY } from '../api.js';

const bus = inject('bus');

/* ---------- 状态 ---------- */
const view = ref('pick');          // pick | in | login
const loginName = ref('');
const loginPass = ref('');
const loginNote = ref('');

const groups = ref([]);            // [{id, name}]
const meId = ref('');
const groupSel = ref('');
const pickList = ref([]);          // 混合成员 + 进行中的通话
const refreshTick = ref(0);        // 强制刷新 pick 列表

const call = reactive({
  group: '', callId: '', conflict: '', peerId: '', peerName: '',
  me: '', status: '', updatedAt: '',
});
const msgs = ref([]);              // 通话消息
const callStateText = ref('');
const cmInput = ref('');
const aiBoxHidden = ref(true);
const aiState = ref('');
const aiBtnDisabled = ref(false);

const avHidden = ref(true);
const avStartHidden = ref(false);
const avHangHidden = ref(true);
const avMuteHidden = ref(true);
const avMuteOn = ref(true);        // true = 声音开着（按钮文本「静音」）
const avCamHidden = ref(true);
const avCamOn = ref(true);

/* ---------- 视觉状态：状态徽标 / 通话时长（纯展示，不参与信令） ---------- */
const badge = computed(() => {
  if (call.status === 'ended') return { cls: 'err', text: '已挂断' };
  if (call.conflict) return { cls: 'warn', text: '待确认' };
  if (call.status === 'live') return { cls: 'ok', text: '通话中' };
  return { cls: 'warn', text: '未接通' };
});
const dur = ref('00:00');
let durTick = 0, durStart = 0;
function fmtDur(ms) {
  const s = Math.floor(ms / 1000), p = n => String(n).padStart(2, '0');
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  return h ? p(h) + ':' + p(m) + ':' + p(s % 60) : p(m) + ':' + p(s % 60);
}
function startDur() {
  if (durTick) return;
  durStart = Date.now();
  durTick = setInterval(() => { dur.value = fmtDur(Date.now() - durStart); }, 1000);
}
function stopDur() {
  if (durTick) { clearInterval(durTick); durTick = 0; }
  dur.value = '00:00';
}

/* ---------- WebRTC ---------- */
let rtc = null;       // { pc, stream, peer }
let iceQ = [];
let callPoll = 0;
let msgAfter = 0, sigAfter = 0;

/* ---------- 工具 ---------- */
function hhmm(ts) {
  if (!ts) return '';
  const d = new Date(ts), p = n => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes());
}

/* ---------- 登录 ---------- */
async function doLogin() {
  const name = loginName.value.trim(), pw = loginPass.value;
  if (!name || !pw) { loginNote.value = '用户名和密码都要填'; return; }
  loginNote.value = '登录中…';
  const r = await api('/api/agent/auth/login', 'POST', { name, password: pw });
  if (!r.ok) { loginNote.value = '登录失败：' + (r.error || '未知'); return; }
  try { localStorage.setItem(SESSION_KEY, String((r.data && r.data.token) || '')); } catch {}
  loginPass.value = '';
  view.value = 'pick';
  await renderPick();
}

/* ---------- 选组 / 开通话 ---------- */
async function renderPick() {
  const r = await api('/api/agent/auth/whoami', 'GET');
  if (!r.ok) { loginNote.value = '取登录态失败：' + (r.error || '未知'); view.value = 'login'; return; }
  groups.value = (r.data && r.data.groups) || [];
  meId.value = (r.data && r.data.user && r.data.user.id) || '';
  if (!groups.value.length) {
    pickList.value = [{ empty: '你还没加入任何用户组。先在「账号与共享 → 用户组」建一个、拉上对方。' }];
    return;
  }
  if (!groupSel.value || !groups.value.some(g => g.id === groupSel.value)) groupSel.value = groups.value[0].id;
  await loadGroup();
}
async function loadGroup() {
  const gid = groupSel.value;
  if (!gid) return;
  call.group = gid;
  const gr = await api('/api/agent/groups/' + encodeURIComponent(gid), 'GET');
  const cl = await api('/api/agent/groups/' + encodeURIComponent(gid) + '/calls', 'GET');
  const members = ((gr.ok && gr.data && gr.data.members) || []).filter(m => m.userId !== meId.value);
  const live = ((cl.ok && cl.data && cl.data.calls) || []).filter(c => c.status === 'live');
  const rows = [];
  live.forEach(c => rows.push({ kind: 'live', id: c.id, name: c.peerName || c.peerId }));
  members.forEach(m => rows.push({ kind: 'member', peer: m.userId, name: m.userName, role: m.role }));
  if (!rows.length) pickList.value = [{ empty: '这个组里还没有别的成员，先去「用户组」把对方拉进来。' }];
  else pickList.value = rows;
}
async function openCallWith(p) {
  const gid = call.group;
  const r = await api('/api/agent/groups/' + encodeURIComponent(gid) + '/calls', 'POST', { peer: p.peer });
  if (!r.ok) { alert('开通话失败：' + (r.error || '未知')); return; }
  call.callId = (r.data && r.data.call && r.data.call.id) || '';
  call.peerId = p.peer; call.peerName = p.name; call.conflict = '';
  await openInView();
}
async function resumeCallWith(p) {
  call.callId = p.id; call.conflict = '';
  await openInView();
}
async function refreshPick() { await renderPick(); }

/* ---------- 通话中视图 ---------- */
async function openInView() {
  view.value = 'in';
  startDur();
  msgs.value = [];
  msgAfter = 0; sigAfter = 0;
  await tickDetail(true);
  if (callPoll) clearInterval(callPoll);
  callPoll = setInterval(() => tickDetail(false), 2000);
}
async function tickDetail(full) {
  if (!call.callId) return;
  const gid = encodeURIComponent(call.group), cid = encodeURIComponent(call.callId);
  const r = await api('/api/agent/groups/' + gid + '/calls/' + cid +
    '?afterMsg=' + (full ? 0 : msgAfter) + '&afterSignal=' + (full ? 0 : sigAfter), 'GET');
  if (!r.ok) { callStateText.value = '读通话失败：' + r.error; return; }
  const d = r.data || {};
  const c = d.call || {};
  const me = d.me || '';
  call.me = me;
  call.peerId = me && c.aId === me ? c.bId : c.aId;
  call.peerName = me && c.aId === me ? (c.bName || c.bId) : (c.aName || c.aId);
  call.conflict = c.conflictSid || call.conflict;
  call.status = c.status || '';
  call.updatedAt = c.updatedAt || '';
  callStateText.value = c.status === 'ended' ? '已挂断（消息还在）' : '进行中 · ' + hhmm(c.updatedAt);

  (d.messages || []).forEach(m => { msgAfter = Math.max(msgAfter, m.id | 0); addMsg(m); });
  (d.signals  || []).forEach(s => { sigAfter = Math.max(sigAfter, s.id | 0); handleSignal(s); });

  // AI 文档冲突判定
  const conflict = d.conflict, confirmers = d.aiConfirmers || [];
  if (conflict) {
    aiBoxHidden.value = false;
    if (conflict.aiStatus === 'running') aiState.value = 'AI 正在通读你们的记录并判定…';
    else if (conflict.aiStatus === 'done') { aiState.value = 'AI 已判定：' + (conflict.aiNote || '完成'); aiBtnDisabled.value = true; }
    else if (conflict.aiStatus === 'failed') { aiState.value = 'AI 判定失败：' + (conflict.aiNote || '原因未知'); aiBtnDisabled.value = false; }
    else {
      const mine = confirmers.indexOf(me) >= 0;
      aiState.value = '等两方都点「回复AI」，AI 才通读记录并自动判定/合并文档。' +
        (mine ? '（我已确认，还差对方）' : '（未确认）');
      aiBtnDisabled.value = false;
    }
  } else { aiBoxHidden.value = true; }
}
function addMsg(m) {
  const isMe = !!call.me && m.fromId === call.me;
  msgs.value.push({
    key: m.id || Math.random(),
    cls: m.fromId === 'ai' ? 'ai' : (isMe ? 'me' : 'ai'),
    text: m.text || '',
    who: m.fromName || m.fromId || '',
    isAI: m.fromId === 'ai',
    isMe,
  });
  // 滚到底
  setTimeout(() => {
    const el = document.getElementById('call-msgs');
    if (el) el.scrollTop = el.scrollHeight;
  }, 0);
}
async function sendMsg() {
  const text = cmInput.value.trim();
  if (!text || !call.callId) return;
  cmInput.value = '';
  const r = await api('/api/agent/groups/' + encodeURIComponent(call.group) + '/calls/' + encodeURIComponent(call.callId) +
    '/messages', 'POST', { text });
  if (!r.ok) addMsg({ fromId: 'local', fromName: '我', text: '（没发出去：' + (r.error || '未知') + '）' });
}

/* ---------- WebRTC ---------- */
function postSignal(to, kind, payload) {
  return api('/api/agent/groups/' + encodeURIComponent(call.group) + '/calls/' + encodeURIComponent(call.callId) +
    '/signal', 'POST', { to, kind, payload });
}
// 只起本地媒体 + RTCPeerConnection，不发 offer（被 startAV / startAVForPeer 复用）
async function startAVMedia() {
  if (rtc) return true;
  if (!navigator.mediaDevices || !window.RTCPeerConnection) {
    callStateText.value = '这个壳不支持 WebRTC（没有 RTCPeerConnection）'; return false;
  }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true }); }
  catch {
    try { stream = await navigator.mediaDevices.getUserMedia({ audio: true }); }
    catch (e2) {
      callStateText.value = '拿不到麦克风/摄像头：' + ((e2 && e2.name) === 'NotAllowedError' ? '权限被拒了' : ((e2 && e2.message) || '未知'));
      return false;
    }
  }
  const pc = new RTCPeerConnection();
  stream.getTracks().forEach(t => pc.addTrack(t, stream));
  const cvLocal = document.getElementById('cv-local');
  if (cvLocal) {
    cvLocal.srcObject = stream;
    cvLocal.play().catch(() => {}); // 显式 play，防 autoplay 不触发
  }
  avHidden.value = false; avStartHidden.value = true; avHangHidden.value = false;
  avMuteHidden.value = false; avCamHidden.value = !stream.getVideoTracks().length;
  rtc = { pc, stream, peer: call.peerId };
  iceQ = [];
  // 修复：ev.streams 可能为空，手动构建 MediaStream；显式 play
  pc.ontrack = ev => {
    let remoteStream;
    if (ev.streams && ev.streams.length > 0) remoteStream = ev.streams[0];
    else remoteStream = new MediaStream([ev.track]);
    const cvRemote = document.getElementById('cv-remote');
    if (cvRemote) {
      cvRemote.srcObject = remoteStream;
      cvRemote.play().catch(() => {});
    }
  };
  pc.onicecandidate = ev => { if (ev.candidate) postSignal(call.peerId, 'ice', JSON.stringify(ev.candidate)); };
  return true;
}
// 用户主动发起：起媒体 → createOffer → 发信令
async function startAV() {
  if (rtc) return;
  const ok = await startAVMedia();
  if (!ok) return;
  const offer = await rtc.pc.createOffer();
  await rtc.pc.setLocalDescription(offer);
  await postSignal(call.peerId, 'offer', JSON.stringify(rtc.pc.localDescription));
}
async function handleSignal(s) {
  if (!rtc) {
    if (s.kind === 'offer') await startAVForPeer(s);
    return;
  }
  let payload;
  try { payload = JSON.parse(s.payload || '{}'); } catch { return; }
  if (s.kind === 'answer') {
    rtc.pc.setRemoteDescription(new RTCSessionDescription(payload)).catch(() => {});
  } else if (s.kind === 'ice') {
    if (rtc.pc.remoteDescription) rtc.pc.addIceCandidate(new RTCIceCandidate(payload)).catch(() => {});
    else iceQ.push(payload);
  } else if (s.kind === 'bye') { stopAV(); callStateText.value = '对方挂断了音视频'; }
}
async function startAVForPeer(sig) {
  if (rtc) return;
  const ok = await startAVMedia(); // 起本地媒体，不发 offer
  if (!ok) return;
  try {
    await rtc.pc.setRemoteDescription(new RTCSessionDescription(JSON.parse(sig.payload)));
    const ans = await rtc.pc.createAnswer();
    await rtc.pc.setLocalDescription(ans);
    await postSignal(sig.from, 'answer', JSON.stringify(rtc.pc.localDescription));
    iceQ.forEach(c => rtc.pc.addIceCandidate(new RTCIceCandidate(c)).catch(() => {}));
    iceQ = [];
  } catch (e) { callStateText.value = '接通话失败：' + ((e && e.message) || e); }
}
function stopAV() {
  if (!rtc) return;
  try { rtc.pc.close(); } catch {}
  try { rtc.stream.getTracks().forEach(t => t.stop()); } catch {}
  try { const cl = document.getElementById('cv-local'), cr = document.getElementById('cv-remote'); if (cl) cl.srcObject = null; if (cr) cr.srcObject = null; } catch {}
  avHidden.value = true; avStartHidden.value = false; avHangHidden.value = true; avMuteHidden.value = true; avCamHidden.value = true;
  rtc = null; iceQ = [];
}
async function endCall() {
  stopAV();
  await postSignal(call.peerId, 'bye', '{}');
  await api('/api/agent/groups/' + encodeURIComponent(call.group) + '/calls/' + encodeURIComponent(call.callId) + '/end', 'POST', {});
  call.callId = '';
  view.value = 'pick';
  stopDur();
  if (callPoll) { clearInterval(callPoll); callPoll = 0; }
  await renderPick();
}
function backToPick() {
  stopAV();
  call.callId = '';
  view.value = 'pick';
  stopDur();
  if (callPoll) { clearInterval(callPoll); callPoll = 0; }
  renderPick();
}
async function hangAV() { stopAV(); await postSignal(call.peerId, 'bye', '{}'); }
function toggleMute() {
  if (!rtc) return;
  const on = !avMuteOn.value;
  rtc.stream.getAudioTracks().forEach(t => { t.enabled = on; });
  avMuteOn.value = on;
}
function toggleCam() {
  if (!rtc) return;
  const on = !avCamOn.value;
  rtc.stream.getVideoTracks().forEach(t => { t.enabled = on; });
  avCamOn.value = on;
}
async function onAIConfirm() {
  if (!call.conflict) return;
  aiBtnDisabled.value = true;
  const r = await api('/api/agent/groups/' + encodeURIComponent(call.group) + '/conflicts/' + encodeURIComponent(call.conflict) + '/ai-confirm', 'POST', {});
  if (!r.ok) { aiState.value = '确认失败：' + (r.error || '未知'); aiBtnDisabled.value = false; return; }
  const d = r.data || {};
  aiBtnDisabled.value = !!(d.allConfirmed && d.triggered);
  aiState.value = d.allConfirmed
    ? 'AI 开始通读了（判定结果会出现在协商记录和这里）'
    : ('我已确认，还差 ' + ((d.missing || []).length) + ' 方');
}

/* ---------- ctx 轮询（父组件 FloatApp 派发 float-event 'ctx'） ---------- */
let ctxRev = 0;
async function applyCtx(ctx) {
  if (!ctx || typeof ctx !== 'object') return;
  if (ctx.session) { try { localStorage.setItem(SESSION_KEY, String(ctx.session)); } catch {} }
  if (ctx.group) call.group = ctx.group;
  if (ctx.call) call.callId = ctx.call;
  if (ctx.conflict) call.conflict = ctx.conflict;
  if (session() && call.callId) await openInView();
  else if (session() && call.group) { view.value = 'pick'; await renderPick(); }
}

/* ---------- 关窗发 bye（keepalive fetch） ---------- */
function onPageHide() {
  if (!rtc) return;
  try {
    fetch('/api/be/api/agent/groups/' + encodeURIComponent(call.group) + '/calls/' + encodeURIComponent(call.callId) + '/signal', {
      method: 'POST', keepalive: true,
      headers: { 'Content-Type': 'application/json', 'X-Baize-Session': session() },
      body: JSON.stringify({ to: rtc.peer, kind: 'bye', payload: '{}' }),
    });
  } catch {}
  stopAV();
}

/* ---------- 生命周期 ---------- */
let ctxHandler = null;
onMounted(async () => {
  // ctx 轮询：父组件 FloatApp 每隔 1500ms 检查 ctx rev 变了就派发
  ctxHandler = (e) => {
    if (e.detail && e.detail.type === 'ctx') applyCtx(e.detail.ctx);
  };
  window.addEventListener('float-event', ctxHandler);

  const c0 = await local('/float/ctx');
  ctxRev = (c0 && c0.rev) | 0;
  await applyCtx(c0 && c0.ctx);

  if (!call.callId) {
    if (!session()) view.value = 'login';
    else { view.value = 'pick'; await renderPick(); }
  }
  window.addEventListener('pagehide', onPageHide);
});
onBeforeUnmount(() => {
  if (ctxHandler) window.removeEventListener('float-event', ctxHandler);
  if (callPoll) clearInterval(callPoll);
  stopDur();
  stopAV();
  window.removeEventListener('pagehide', onPageHide);
});
</script>

<template>
  <!-- 登录 -->
  <div v-if="view === 'login'" class="calllogin">
    <h4>登录</h4>
    <input v-model="loginName" placeholder="用户名" autocomplete="username" />
    <input v-model="loginPass" type="password" placeholder="密码" autocomplete="current-password"
           @keydown.enter.prevent="doLogin" />
    <div class="call-row">
      <button class="btn primary" @click="doLogin">登录</button>
      <span class="login-note">{{ loginNote }}</span>
    </div>
  </div>

  <!-- 选组 / 开通话 -->
  <div v-else-if="view === 'pick'" class="callpick">
    <div class="call-row">
      <label>用户组</label>
      <select v-model="groupSel" @change="loadGroup">
        <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
      </select>
      <button class="btn" @click="refreshPick">刷新</button>
    </div>
    <div class="clist">
      <template v-if="pickList.length && pickList[0].empty">
        <div class="hint cempty">{{ pickList[0].empty }}</div>
      </template>
      <template v-else>
        <div v-for="(p, i) in pickList" :key="i" class="crow"
             @click="p.kind === 'live' ? resumeCallWith(p) : openCallWith(p)">
          <span class="nm">
            <template v-if="p.kind === 'live'">正在通话 · {{ p.name }}</template>
            <template v-else>{{ p.name }}{{ p.role === 'owner' ? '（组长）' : '' }}</template>
          </span>
          <span :class="['st', p.kind === 'live' ? 'live' : '']">{{ p.kind === 'live' ? '进行中' : '' }}</span>
          <button class="btn">{{ p.kind === 'live' ? '继续' : '开通话' }}</button>
        </div>
      </template>
    </div>
  </div>

  <!-- 通话中 -->
  <div v-else class="callin">
    <div class="calltop">
      <span class="nm">{{ call.peerName || call.peerId || '—' }}</span>
      <span :class="['badge', badge.cls]"><i class="bdot"></i>{{ badge.text }}</span>
      <span class="dur mono">{{ dur }}</span>
      <button class="btn" @click="backToPick">切换</button>
      <button class="btn danger" @click="endCall">挂断</button>
    </div>
    <div class="callstat">{{ callStateText }}</div>

    <!-- WebRTC 视频区 -->
    <div v-show="!avHidden" class="callvid">
      <video id="cv-local" autoplay muted playsinline class="mine"></video>
      <video id="cv-remote" autoplay playsinline></video>
    </div>
    <div v-show="!avHidden" class="avbar">
      <button v-show="!avMuteHidden" :class="['abtn', { off: !avMuteOn }]"
              :title="avMuteOn ? '静音' : '取消静音'" @click="toggleMute">
        <svg v-if="avMuteOn" class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 2a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z" />
          <path d="M19 10v1a7 7 0 0 1-14 0v-1" />
          <path d="M12 18v3" />
        </svg>
        <svg v-else class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M15 9.34V5a3 3 0 0 0-5.94-.6" />
          <path d="M9 9v3a3 3 0 0 0 5.12 2.12" />
          <path d="M19 10v1a7 7 0 0 1-11.4 5.5" />
          <path d="M12 18v3" />
          <path d="m2 2 20 20" />
        </svg>
      </button>
      <button v-show="!avCamHidden" :class="['abtn', { off: !avCamOn }]"
              :title="avCamOn ? '关摄像' : '开摄像'" @click="toggleCam">
        <svg v-if="avCamOn" class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="m16 13 5.2 3.5a.5.5 0 0 0 .8-.4V7.9a.5.5 0 0 0-.8-.4L16 10.5" />
          <rect x="2" y="6" width="14" height="12" rx="2" />
        </svg>
        <svg v-else class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M10.66 6H14a2 2 0 0 1 2 2v2.5l5.2 3.5a.5.5 0 0 0 .8-.4V7.9a.5.5 0 0 0-.8-.4L16 10.5" />
          <path d="M16 16a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h2" />
          <path d="m2 2 20 20" />
        </svg>
      </button>
      <button v-show="!avHangHidden" class="abtn hang" title="挂断音视频" @click="hangAV">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M10.1 13.9a14 14 0 0 0 3.7 2.7 1 1 0 0 0 1.2-.3l.4-.5a2 2 0 0 1 1.6-.8h3a2 2 0 0 1 2 2v3a2 2 0 0 1-2 2A18 18 0 0 1 2 4a2 2 0 0 1 2-2h3a2 2 0 0 1 2 2v3a2 2 0 0 1-.8 1.6l-.5.4a1 1 0 0 0-.3 1.2 14 14 0 0 0 2.7 3.7" />
        </svg>
      </button>
    </div>
    <div v-show="avHidden" class="callvid-empty">
      <button v-show="!avStartHidden" class="btn primary wide" @click="startAV">开始音视频通话</button>
      <span v-show="avStartHidden" class="hint">音视频进行中</span>
    </div>

    <!-- 消息区 -->
    <div id="call-msgs" class="callmsgs">
      <div v-if="!msgs.length" class="hint">还没有消息，先打个招呼，或在上面发起音视频。</div>
      <div v-for="m in msgs" :key="m.key" :class="['msg', m.cls]">
        <span v-if="m.isAI" class="who">白泽AI</span>
        {{ m.text }}
      </div>
    </div>
    <div class="callbar">
      <input v-model="cmInput" placeholder="发个消息…（Enter 发，Shift+Enter 换行）"
             @keydown.enter.exact.prevent="sendMsg" />
      <button class="sendbtn" :disabled="!cmInput.trim()" title="发送" @click="sendMsg">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M22 2 11 13" />
          <path d="M22 2 15 22l-4-9-9-4Z" />
        </svg>
      </button>
    </div>

    <!-- AI 文档冲突判定 -->
    <div v-show="!aiBoxHidden" class="ai-box">
      <div class="ai-state">{{ aiState }}</div>
      <button class="btn" :disabled="aiBtnDisabled" @click="onAIConfirm">回复AI</button>
    </div>
  </div>
</template>

<style scoped>
.callpick, .callin { flex: 1; min-height: 0; display: flex; flex-direction: column; padding: 12px; gap: 8px; }
.calllogin { flex: 1; min-height: 0; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.calllogin h4 { margin: 0; font-size: 12.5px; font-weight: 600; color: var(--text); }
.calllogin input {
  width: 100%; height: 36px; background: var(--bg); color: var(--text);
  border: 1px solid var(--line-strong); border-radius: 4px; padding: 0 10px;
  font: inherit; font-size: 13px; outline: none;
  transition: border-color 120ms cubic-bezier(.2,0,0,1);
}
.calllogin input::placeholder { color: var(--text-3); }
.calllogin input:focus { border-color: var(--accent); }
.call-row { display: flex; align-items: center; gap: 8px; }
.call-row label { font-size: 12px; color: var(--text-3); flex: 0 0 auto; }
.call-row select {
  flex: 1; min-width: 0; height: 32px; padding: 0 8px;
  background: var(--bg); color: var(--text); border: 1px solid var(--line-strong);
  border-radius: 4px; font: inherit; font-size: 12.5px; outline: none;
}
.call-row select:focus { border-color: var(--accent); }
.login-note { font-size: 11.5px; color: var(--text-3); min-width: 0; }

/* 基础按钮：4 圆角 + hairline */
.btn {
  flex: 0 0 auto; height: 32px; padding: 0 10px;
  display: inline-flex; align-items: center; justify-content: center; gap: 5px;
  border: 1px solid var(--line-strong); border-radius: 4px;
  background: var(--surface-2); color: var(--text-2);
  font: inherit; font-size: 11.5px; cursor: pointer; white-space: nowrap;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1),
              border-color 120ms cubic-bezier(.2,0,0,1);
}
.btn:hover { background: var(--surface-3); color: var(--text); }
.btn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
.btn.primary:hover { background: var(--accent-2); }
.btn.danger { color: var(--err); }
.btn.danger:hover { background: var(--surface-3); color: var(--err); }
.btn.wide { height: 40px; padding: 0 18px; font-size: 12.5px; }
.btn:disabled { opacity: .45; cursor: not-allowed; }

/* 顶部：名字 + 状态徽标 + 时长 + 切换/挂断 */
.calltop { flex: 0 0 auto; display: flex; align-items: center; gap: 8px; min-width: 0; }
.calltop .nm {
  flex: 1; min-width: 0; font-size: 12.5px; font-weight: 600; color: var(--text);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.badge {
  flex: 0 0 auto; display: inline-flex; align-items: center; gap: 5px;
  height: 20px; padding: 0 7px; border: 1px solid var(--line-strong); border-radius: 4px;
  font-size: 11px; color: var(--text-2); white-space: nowrap;
}
.badge .bdot { width: 6px; height: 6px; border-radius: 999px; background: var(--text-3); }
.badge.ok { color: var(--ok); }
.badge.ok .bdot { background: var(--ok); }
.badge.warn { color: var(--warn); }
.badge.warn .bdot { background: var(--warn); }
.badge.err { color: var(--err); }
.badge.err .bdot { background: var(--err); }
.dur { flex: 0 0 auto; font-size: 11.5px; color: var(--text-2); }
.callstat {
  flex: 0 0 auto; font-size: 11.5px; color: var(--text-3); margin-top: -4px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}

/* 列表 */
.clist { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; }
.cempty { padding: 4px 2px; }
.crow {
  display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  background: var(--surface-2); border: 1px solid var(--line); border-radius: 6px;
  cursor: pointer;
  transition: border-color 120ms cubic-bezier(.2,0,0,1);
}
.crow:hover { border-color: var(--accent); }
.crow .nm { flex: 1; min-width: 0; font-size: 13px; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.crow .st { flex: 0 0 auto; font-size: 11px; color: var(--text-3); }
.crow .st.live { color: var(--ok); font-weight: 600; }

/* 视频区 */
.callvid { flex: 0 0 auto; display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.callvid video {
  width: 100%; aspect-ratio: 4/3; object-fit: cover; display: block;
  background: #000; border-radius: 4px; border: 1px solid var(--line);
}
.callvid-empty { flex: 0 0 auto; display: flex; justify-content: center; align-items: center; padding: 2px 0; }
/* 音视频控制：40×40 方形，挂断 44 圆形 err 底 */
.avbar { flex: 0 0 auto; display: flex; align-items: center; justify-content: center; gap: 8px; }
.abtn {
  width: 40px; height: 40px; display: grid; place-items: center;
  padding: 0; border: 1px solid var(--line-strong); border-radius: 4px;
  background: var(--surface-2); color: var(--text); cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1),
              border-color 120ms cubic-bezier(.2,0,0,1), opacity 120ms cubic-bezier(.2,0,0,1);
}
.abtn:hover { background: var(--surface-3); }
.abtn.off { border-color: var(--err); color: var(--err); }
.abtn:disabled { opacity: .45; cursor: not-allowed; }
.abtn .ic { width: 20px; height: 20px; }
.abtn.hang {
  width: 44px; height: 44px; border-radius: 999px;
  background: var(--err); border-color: var(--err); color: var(--accent-ink);
}
.abtn.hang:hover { background: var(--err); opacity: .88; }

/* 消息 */
.callmsgs {
  flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden;
  display: flex; flex-direction: column; gap: 8px; padding: 2px 0;
}
.msg {
  max-width: 84%; border-radius: 6px; padding: 8px 10px;
  font-size: 13px; line-height: 1.55; word-break: break-word; user-select: text;
}
.msg.me { align-self: flex-end; background: var(--accent-soft); border-left: 2px solid var(--accent); color: var(--text); }
.msg.ai { align-self: flex-start; background: var(--surface-2); color: var(--text); }
.msg.ai .who { display: block; color: var(--accent); font-size: 11px; margin-bottom: 2px; }

/* 发送条 */
.callbar { flex: 0 0 auto; display: flex; align-items: center; gap: 8px; }
.callbar input {
  flex: 1; min-width: 0; height: 36px; background: var(--bg); color: var(--text);
  border: 1px solid var(--line-strong); border-radius: 4px; padding: 0 10px;
  font: inherit; font-size: 13px; outline: none;
  transition: border-color 120ms cubic-bezier(.2,0,0,1);
}
.callbar input::placeholder { color: var(--text-3); }
.callbar input:focus { border-color: var(--accent); }
.sendbtn {
  flex: 0 0 auto; width: 36px; height: 36px; display: grid; place-items: center;
  padding: 0; border: 0; border-radius: 4px;
  background: var(--accent); color: var(--accent-ink); cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1);
}
.sendbtn:hover { background: var(--accent-2); }
.sendbtn:disabled { background: var(--surface-3); color: var(--text-3); cursor: not-allowed; }

/* AI 判定 */
.ai-box {
  flex: 0 0 auto; display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  background: var(--surface-2); border: 1px solid var(--accent); border-radius: 4px;
}
.ai-state { flex: 1; font-size: 12px; color: var(--text-2); line-height: 1.5; }
</style>
