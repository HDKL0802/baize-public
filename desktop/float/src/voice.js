/* 语音工具：录音转 WAV16k + 分段录音
   从旧 float.html 移植。MediaRecorder 出的是 webm/opus，听写只认 16k 单声道 WAV，
   所以先解码再用 OfflineAudioContext 离线重采样到 16k。 */
'use strict';

const TARGET_RATE = 16000;

/** blob（任意格式）→ 16k 单声道 WAV */
async function toWav16k(blob) {
  const ab = await blob.arrayBuffer();
  const AC = window.AudioContext || window.webkitAudioContext;
  if (!AC) throw new Error('这个壳没有 AudioContext，解不了录音');
  const tmp = new AC();
  let decoded;
  try { decoded = await tmp.decodeAudioData(ab.slice(0)); }
  finally { try { tmp.close(); } catch (e) { /* 忽略 */ } }
  const Offline = window.OfflineAudioContext || window.webkitOfflineAudioContext;
  if (!Offline) throw new Error('这个壳没有 OfflineAudioContext，降不了采样率');
  const frames = Math.max(1, Math.ceil(decoded.duration * TARGET_RATE));
  const off = new Offline(1, frames, TARGET_RATE);
  const src = off.createBufferSource();
  src.buffer = decoded;
  src.connect(off.destination);
  src.start(0);
  const rendered = await off.startRendering();
  return encodeWav16(rendered.getChannelData(0), TARGET_RATE);
}

function encodeWav16(f32, rate) {
  const n = f32.length;
  const buf = new ArrayBuffer(44 + n * 2);
  const dv = new DataView(buf);
  const str = (off, s) => { for (let i = 0; i < s.length; i++) dv.setUint8(off + i, s.charCodeAt(i)); };
  str(0, 'RIFF'); dv.setUint32(4, 36 + n * 2, true); str(8, 'WAVE');
  str(12, 'fmt '); dv.setUint32(16, 16, true); dv.setUint16(20, 1, true); dv.setUint16(22, 1, true);
  dv.setUint32(24, rate, true); dv.setUint32(28, rate * 2, true);
  dv.setUint16(32, 2, true); dv.setUint16(34, 16, true);
  str(36, 'data'); dv.setUint32(40, n * 2, true);
  let off = 44;
  for (let i = 0; i < n; i++) {
    let s = f32[i];
    if (s > 1) s = 1; else if (s < -1) s = -1;
    dv.setInt16(off, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    off += 2;
  }
  return new Blob([buf], { type: 'audio/wav' });
}

function blobToBase64(blob) {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => {
      const s = String(fr.result || '');
      const i = s.indexOf(',');
      if (i < 0) { reject(new Error('读出来的不是 dataURL')); return; }
      resolve(s.slice(i + 1));
    };
    fr.onerror = () => reject(new Error('FileReader 读取失败'));
    fr.readAsDataURL(blob);
  });
}

/* 分段录音：走完整周期，绝不用 timeslice。
   为什么不用 timeslice：切出来的 webm 碎片是一条流被拦腰截断的块，单块缺容器头解不开。
   所以每段都跑一个完整周期：new MediaRecorder → start → 到点 stop → onstop 拿到完整 blob，
   onstop 里立刻再起一轮，避免换挡漏音。 */
let micStream = null, recRunning = false, segTimer = 0, segStartAt = 0, curRec = null, curChunks = [], micMime = '';

function pickMicMime() {
  const cands = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg'];
  for (const c of cands) { try { if (window.MediaRecorder && MediaRecorder.isTypeSupported(c)) return c; } catch (e) { /* 试下一个 */ } }
  return '';
}
function releaseMic() {
  if (micStream) { try { micStream.getTracks().forEach(t => t.stop()); } catch (e) { /* 忽略 */ } micStream = null; }
}
function recSegment(segMs, onBlob) {
  if (!recRunning) return;
  segStartAt = Date.now();
  curChunks = [];
  let rec;
  try { rec = micMime ? new MediaRecorder(micStream, { mimeType: micMime }) : new MediaRecorder(micStream); }
  catch (e) { recRunning = false; if (segTimer) { clearTimeout(segTimer); segTimer = 0; } releaseMic(); onBlob(null, 0, e); return; }
  curRec = rec;
  rec.addEventListener('dataavailable', e => { if (e.data && e.data.size) curChunks.push(e.data); });
  rec.addEventListener('stop', async () => {
    const startAt = segStartAt;
    const blob = new Blob(curChunks, { type: (curChunks[0] && curChunks[0].type) || 'audio/webm' });
    curChunks = [];
    if (recRunning) recSegment(segMs, onBlob); // 立刻接下一轮，无缝
    else releaseMic();                          // 不再录了，主动把麦克风放掉
    if (blob.size) await onBlob(blob, startAt, null);
  }, { once: true });
  rec.start();
  segTimer = setTimeout(() => { try { rec.stop(); } catch (e) { /* 已经停了 */ } }, segMs);
}
async function startRec(segMs, onBlob) {
  if (recRunning) return { ok: true };
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) return { ok: false, why: '这个壳不支持网页录音（没有 getUserMedia）' };
  if (!window.MediaRecorder) return { ok: false, why: '这个壳没有 MediaRecorder，录不了音' };
  if (!(window.AudioContext || window.webkitAudioContext)) return { ok: false, why: '这个壳没有 AudioContext，解不了录音' };
  try {
    micStream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
  } catch (e) {
    const n = (e && e.name) || ''; let why;
    if (n === 'NotAllowedError') why = '麦克风权限被拒了（系统设置 → 隐私 → 麦克风）';
    else if (n === 'NotFoundError') why = '这台设备没有可用的麦克风';
    else why = (e && e.message) || '打不开麦克风';
    return { ok: false, why };
  }
  micMime = pickMicMime();
  recRunning = true;
  recSegment(segMs, onBlob);
  return { ok: true };
}
function stopRec() {
  if (!recRunning) return;
  recRunning = false;
  if (segTimer) { clearTimeout(segTimer); segTimer = 0; }
  const rec = curRec;
  if (rec && rec.state !== 'inactive') { try { rec.stop(); } catch (e) { /* 已经停了 */ } }
  else releaseMic();
}

export { toWav16k, blobToBase64, startRec, stopRec };
