<!-- 两人通话（call）：登录 → 选组/开 → WebRTC → AI 文档判定 -->
<script setup>
import { ref, reactive, onMounted, onBeforeUnmount, inject } from 'vue';
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
async function startAV() {
  if (rtc) return;
  if (!navigator.mediaDevices || !window.RTCPeerConnection) {
    callStateText.value = '这个壳不支持 WebRTC（没有 RTCPeerConnection）'; return;
  }
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true }); }
  catch {
    try { stream = await navigator.mediaDevices.getUserMedia({ audio: true }); }
    catch (e2) {
      callStateText.value = '拿不到麦克风/摄像头：' + ((e2 && e2.name) === 'NotAllowedError' ? '权限被拒了' : ((e2 && e2.message) || '未知'));
      return;
    }
  }
  const pc = new RTCPeerConnection();
  stream.getTracks().forEach(t => pc.addTrack(t, stream));
  const cvLocal = document.getElementById('cv-local');
  if (cvLocal) cvLocal.srcObject = stream;
  avHidden.value = false; avStartHidden.value = true; avHangHidden.value = false;
  avMuteHidden.value = false; avCamHidden.value = !stream.getVideoTracks().length;
  rtc = { pc, stream, peer: call.peerId };
  iceQ = [];
  pc.ontrack = ev => { const cvRemote = document.getElementById('cv-remote'); if (cvRemote) cvRemote.srcObject = ev.streams[0]; };
  pc.onicecandidate = ev => { if (ev.candidate) postSignal(call.peerId, 'ice', JSON.stringify(ev.candidate)); };
  const offer = await pc.createOffer();
  await pc.setLocalDescription(offer);
  await postSignal(call.peerId, 'offer', JSON.stringify(pc.localDescription));
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
  await startAV(); // 起本地媒体
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
  if (callPoll) { clearInterval(callPoll); callPoll = 0; }
  await renderPick();
}
function backToPick() {
  stopAV();
  call.callId = '';
  view.value = 'pick';
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
      <label>用户组：</label>
      <select v-model="groupSel" @change="loadGroup">
        <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
      </select>
      <button class="btn" @click="refreshPick">刷新</button>
    </div>
    <div class="clist">
      <template v-if="pickList.length && pickList[0].empty">
        <div class="cempty">{{ pickList[0].empty }}</div>
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
      <b>{{ call.peerName || call.peerId || '—' }}</b>
      <span class="sub">{{ callStateText }}</span>
      <button class="btn" @click="backToPick">切换</button>
      <button class="btn stop" @click="endCall">挂断</button>
    </div>

    <!-- WebRTC 视频区 -->
    <div v-show="!avHidden" class="callvid">
      <video id="cv-local" autoplay muted playsinline class="mine"></video>
      <video id="cv-remote" autoplay playsinline></video>
      <div class="avbar">
        <button v-show="!avStartHidden" class="btn" @click="startAV">音视频</button>
        <button v-show="!avHangHidden" class="btn stop" @click="hangAV">挂断音视频</button>
        <button v-show="!avMuteHidden" class="btn" @click="toggleMute">{{ avMuteOn ? '静音' : '取消静音' }}</button>
        <button v-show="!avCamHidden" class="btn" @click="toggleCam">{{ avCamOn ? '关摄像' : '开摄像' }}</button>
      </div>
    </div>
    <div v-show="avHidden" class="callvid-empty">
      <button v-show="!avStartHidden" class="btn primary" @click="startAV">开始音视频通话</button>
    </div>

    <!-- 消息区 -->
    <div id="call-msgs" class="callmsgs">
      <div v-for="m in msgs" :key="m.key" :class="['msg', m.cls]">
        <span v-if="m.isAI" class="who">白泽AI</span>
        {{ m.text }}
      </div>
    </div>
    <div class="callbar">
      <input v-model="cmInput" placeholder="发个消息…（Enter 发，Shift+Enter 换行）"
             @keydown.enter.exact.prevent="sendMsg" />
      <button class="btn primary" @click="sendMsg">发送</button>
    </div>

    <!-- AI 文档冲突判定 -->
    <div v-show="!aiBoxHidden" class="ai-box">
      <div class="ai-state">{{ aiState }}</div>
      <button class="btn" :disabled="aiBtnDisabled" @click="onAIConfirm">回复AI</button>
    </div>
  </div>
</template>

<style scoped>
.callpick, .callin { flex: 1; min-height: 0; display: flex; flex-direction: column; padding: 10px 12px; gap: 8px; }
.calllogin { flex: 1; min-height: 0; padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.calllogin h4 { margin: 0; font-size: 13px; }
.calllogin input {
  width: 100%; background: var(--bg); color: var(--text); border: 1px solid var(--border-strong);
  border-radius: 6px; padding: 7px 10px; font: inherit; font-size: 13px; outline: none;
}
.calllogin input:focus { border-color: var(--accent); }
.call-row { display: flex; align-items: center; gap: 8px; }
.login-note { font-size: 11.5px; color: var(--text-3); }
.btn {
  border: 1px solid var(--border-strong); background: var(--surface-2); color: var(--text);
  border-radius: 5px; padding: 4px 10px; font: inherit; font-size: 12px; cursor: pointer;
}
.btn:hover { background: var(--surface-3, var(--surface-2)); }
.btn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
.btn.stop { background: var(--err); border-color: var(--err); color: var(--accent-ink); font-weight: 600; }
.btn:disabled { opacity: .45; cursor: not-allowed; }

.calltop { display: flex; align-items: center; gap: 8px; flex: 0 0 auto; }
.calltop b { font-size: 13.5px; }
.sub { color: var(--text-3); font-size: 11.5px; }

.clist {
  flex: 1; min-height: 0; overflow: auto; display: flex; flex-direction: column; gap: 5px;
}
.cempty { color: var(--text-3); font-size: 12px; padding: 12px; }
.crow {
  display: flex; align-items: center; gap: 10px; padding: 8px 10px;
  background: var(--surface); border: 1px solid var(--border); border-radius: 6px; cursor: pointer;
}
.crow:hover { border-color: var(--accent); }
.crow .nm { flex: 1; font-size: 13px; color: var(--text); }
.crow .st { font-size: 11px; color: var(--text-3); }
.crow .st.live { color: var(--ok); font-weight: 600; }

.callvid { flex: 0 0 auto; display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
.callvid video {
  width: 100%; aspect-ratio: 4/3; object-fit: cover;
  background: #000; border-radius: 6px; border: 1px solid var(--border);
}
.callvid video.mine { position: relative; }
.avbar { grid-column: 1 / -1; display: flex; gap: 6px; flex-wrap: wrap; }
.callvid-empty { flex: 0 0 auto; display: flex; justify-content: center; padding: 4px 0 2px; }

.callmsgs { flex: 1; min-height: 0; overflow: auto; display: flex; flex-direction: column; gap: 6px; padding: 2px 1px; }
.msg { max-width: 80%; border-radius: 8px; padding: 6px 10px; font-size: 13px; line-height: 1.65; word-break: break-word; }
.msg.me { align-self: flex-end; background: var(--accent-soft); border: 1px solid var(--border-strong); }
.msg.ai { align-self: flex-start; background: var(--surface); border: 1px solid var(--border); }
.msg.ai .who { display: block; color: var(--accent); font-size: 11px; margin-bottom: 2px; }

.callbar { display: flex; gap: 6px; }
.callbar input {
  flex: 1; background: var(--bg); color: var(--text); border: 1px solid var(--border-strong);
  border-radius: 6px; padding: 7px 10px; font: inherit; font-size: 13px; outline: none;
}
.callbar input:focus { border-color: var(--accent); }

.ai-box {
  display: flex; align-items: center; gap: 8px; padding: 8px 10px;
  background: var(--surface); border: 1px dashed var(--accent); border-radius: 6px;
}
.ai-state { flex: 1; font-size: 12px; color: var(--text-2); }
</style>
