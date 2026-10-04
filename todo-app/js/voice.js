/* 白泽 · 语音（说话 / 听话）
   ─────────────────────────────────────────────
   说话（朗读）：页面由本机内核旁边的静态服务托管，而内核是另一个端口，
   所以这里用「媒体跨源不需要 CORS」这条规则，直接把音频交给 <audio>：
     http://127.0.0.1:<内核端口>/api/agent/voice/tts?text=…
   音频流由内核转发给后端合成，手机上不留文件、也不用把几 MB 塞进 JS 桥。

   听话（按住说话）：网页 getUserMedia 录音 → 重采样成 16k 单声道 WAV
   → base64 经 BzDevice 桥交给内核 → 后端（百炼 paraformer）转成文字。

   铁律：哪一步不通就说哪一步为什么不通（没内核 / 没连后端 / 没给麦克风 / 没配语音通道），
   绝不静默失败，也不假装"录到了"。 */
'use strict';
window.VzVoice = (function () {
  const $ = (id) => document.getElementById(id);

  const MAX_READ = 600;      // 一次最多念多少字（再长就分段念上几分钟，没人听得完）
  const TARGET_RATE = 16000; // 百炼实时听写只认 8k/16k

  function toast(m, ms) { try { UI.toast(m, ms); } catch (e) { /* 界面还没起来 */ } }
  function native() { return window.BzNative || null; }
  function dev() { return window.BzDevice || null; }

  /* ---------- 内核：在不在、端口是多少 ---------- */
  /** 返回 {ok, port, why}。why 是"为什么用不了"，直接能读给用户听。 */
  function kernel() {
    const nb = native();
    if (!nb || typeof nb.bzCoreStatus !== 'function') {
      return { ok: false, why: '当前入口不是手机 App（网页版没有本机内核，语音走不通）' };
    }
    let st = {};
    try { st = JSON.parse(nb.bzCoreStatus() || '{}'); } catch (e) { st = {}; }
    if (!st.supported) return { ok: false, why: '这个安装包里没有带本机内核' };
    if (!st.enabled) return { ok: false, why: '跨端没打开：设置 → 跨端，填上后端地址与配对令牌' };
    if (!st.running || !st.port) return { ok: false, why: '本机内核没起来：' + (st.error || '可以在设置 → 跨端里重启') };
    return { ok: true, port: st.port };
  }

  /** 问一次后端的语音状态（顺带验证"真的够得着"）。返回 {ok, voice, note, why} */
  function status() {
    const d = dev();
    if (!d || !d.available || !d.available()) return { ok: false, why: '当前入口不是手机 App' };
    const r = d.call('/api/agent/voice', 'GET', null);
    if (!r || r.ok === false) return { ok: false, why: (r && r.error) || '内核没有响应' };
    return { ok: true, data: r, voice: r.voice, autoSpeak: !!r.autoSpeak, note: r.note || '', enabled: !!r.enabled };
  }

  /* ================= 说话（朗读） ================= */

  let audio = null;
  let speakBtn = null;

  function isSpeaking() { return !!(audio && !audio.paused && !audio.ended); }
  function setBtn(btn, on) {
    if (!btn) return;
    if (!btn.dataset.idle) btn.dataset.idle = btn.textContent || '🔊 朗读';
    btn.textContent = on ? (btn.dataset.busy || '⏹ 停止') : btn.dataset.idle;
    btn.classList.toggle('speaking', !!on);
  }

  function stop() {
    if (audio) { try { audio.pause(); } catch (e) { /* 忽略 */ } audio = null; }
    setBtn(speakBtn, false);
    speakBtn = null;
  }

  /**
   * 念一段文字。
   * @param text 要念的内容
   * @param opts {voice, btn, onDone}
   * @returns Promise<{ok, why?}>
   */
  async function speak(text, opts) {
    opts = opts || {};
    const raw = String(text == null ? '' : text).trim();
    if (!raw) return { ok: false, why: '要念的内容是空的' };

    if (isSpeaking() || speakBtn) {
      const wasSame = speakBtn === (opts.btn || null);
      stop();
      if (wasSame) return { ok: true };   // 再点一次同一个就是"停"
    }

    const k = kernel();
    if (!k.ok) { toast(k.why, 4200); return { ok: false, why: k.why }; }
    const st = status();
    if (!st.ok) { toast('连不上白泽后端：' + st.why, 4200); return { ok: false, why: st.why }; }
    if (!st.enabled) { toast('后端还没配语音通道（控制台 → 语音）', 4200); return { ok: false, why: '语音通道未启用' }; }
    if (st.note) { toast('语音通道：' + st.note, 4200); return { ok: false, why: st.note }; }

    let text2 = raw;
    if (raw.length > MAX_READ) {
      text2 = raw.slice(0, MAX_READ);
      toast('这段有点长，先念前 ' + MAX_READ + ' 个字', 3200);
    }
    const voice = opts.voice || st.voice || '';
    let url = 'http://127.0.0.1:' + k.port + '/api/agent/voice/tts?text=' + encodeURIComponent(text2);
    if (voice) url += '&voice=' + encodeURIComponent(voice);

    // 媒体跨源加载不要求 CORS（所以不用给内核加跨源头），直接交给 <audio>
    speakBtn = opts.btn || null;
    const a = new Audio(url);
    audio = a;
    setBtn(speakBtn, true);
    a.addEventListener('ended', () => { if (audio === a) { audio = null; setBtn(speakBtn, false); speakBtn = null; } });
    a.addEventListener('error', () => {
      if (audio !== a) return;
      audio = null; setBtn(speakBtn, false); speakBtn = null;
      toast('没念出来：内核这一步没拿到音频（后端语音通道配好了吗？）', 4200);
    });
    try {
      await a.play();
    } catch (e) {
      audio = null; setBtn(speakBtn, false); speakBtn = null;
      const why = (e && e.name) === 'NotAllowedError' ? '被系统拦了自动播放，点一下屏幕再试' : ((e && e.message) || '播放失败');
      toast('没念出来：' + why, 4200);
      return { ok: false, why: why };
    }
    return { ok: true };
  }

  /* ================= 听话（按住说话） ================= */

  let recorder = null;
  let stream = null;
  let chunks = [];
  let holding = false;
  let holdStartAt = 0;

  function isHolding() { return holding; }

  /** 按住：开始录。失败返回 {ok:false, why}，并把原因念给用户 */
  async function holdStart() {
    if (holding) return { ok: true };
    const k = kernel();
    if (!k.ok) { toast(k.why, 4200); return { ok: false, why: k.why }; }
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      toast('这个壳不支持网页录音（Android 系统版本偏低？）', 4200);
      return { ok: false, why: '不支持 getUserMedia' };
    }
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
    } catch (e) {
      const n = (e && e.name) || '';
      let why;
      if (n === 'NotAllowedError') why = '没给麦克风权限（系统设置 → 应用 → 待办中心 → 权限 → 麦克风）';
      else if (n === 'NotFoundError') why = '这台设备没有可用的麦克风';
      else why = (e && e.message) || '打不开麦克风';
      toast(why, 4600);
      return { ok: false, why: why };
    }
    let mime = '';
    const cands = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg'];
    for (const c of cands) {
      try { if (window.MediaRecorder && MediaRecorder.isTypeSupported(c)) { mime = c; break; } } catch (e) { /* 试下一个 */ }
    }
    try {
      recorder = mime ? new MediaRecorder(stream, { mimeType: mime }) : new MediaRecorder(stream);
    } catch (e) {
      releaseStream();
      toast('这台设备的录音器起不来：' + ((e && e.message) || e), 4200);
      return { ok: false, why: String((e && e.message) || e) };
    }
    chunks = [];
    recorder.addEventListener('dataavailable', (e) => { if (e.data && e.data.size) chunks.push(e.data); });
    recorder.start();
    holding = true;
    holdStartAt = Date.now();
    return { ok: true };
  }

  function releaseStream() {
    if (stream) { try { stream.getTracks().forEach(t => t.stop()); } catch (e) { /* 忽略 */ } stream = null; }
  }

  /**
   * 松手：停止录音并去转写。
   * @returns Promise<{ok, text?, why?, millis?}>
   */
  function holdStop() {
    if (!holding || !recorder) { return Promise.resolve({ ok: false, why: '没有在录音' }); }
    const held = Date.now() - holdStartAt;
    holding = false;
    const rec = recorder;
    recorder = null;
    const done = new Promise((resolve) => {
      rec.addEventListener('stop', () => resolve(), { once: true });
    });
    try { rec.stop(); } catch (e) { /* 已经停了 */ }
    return done.then(() => {
      releaseStream();
      const blob = new Blob(chunks, { type: (chunks[0] && chunks[0].type) || 'audio/webm' });
      chunks = [];
      if (held < 500) return { ok: false, why: '按太短了（不到半秒），按住说一句完整的话' };
      if (!blob.size) return { ok: false, why: '没录到声音（麦克风被别的应用占着？）' };
      return transcribe(blob, held);
    });
  }

  async function transcribe(blob, held) {
    let wav;
    try {
      wav = await toWav16k(blob);
    } catch (e) {
      const why = '录音转换失败：' + ((e && e.message) || e);
      toast(why, 4200);
      return { ok: false, why: why };
    }
    let audioB64;
    try {
      audioB64 = await blobToBase64(wav);
    } catch (e) {
      const why = '录音读取失败：' + ((e && e.message) || e);
      toast(why, 4200);
      return { ok: false, why: why };
    }
    const d = dev();
    if (!d || !d.available || !d.available()) {
      const why = '当前入口不是手机 App，听写走不通';
      toast(why, 4200);
      return { ok: false, why: why };
    }
    const r = d.call('/api/agent/voice/stt', 'POST', JSON.stringify({ audio: audioB64, filename: 'phone.wav' }));
    if (!r || r.ok === false || !r.text) {
      const why = (r && r.error) || '后端没回听写结果';
      toast('没听清：' + why, 4600);
      return { ok: false, why: why };
    }
    return { ok: true, text: r.text, millis: r.millis || 0, held: held };
  }

  /* ---------- 音频工具 ---------- */

  /** 任意录音 → 16kHz 单声道 16 位 WAV（百炼实时听写只认这个） */
  async function toWav16k(blob) {
    const ab = await blob.arrayBuffer();
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) throw new Error('这个壳没有 AudioContext，解不了录音');
    const tmp = new AC();
    let decoded;
    try {
      decoded = await tmp.decodeAudioData(ab.slice(0));
    } finally {
      try { tmp.close(); } catch (e) { /* 忽略 */ }
    }
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

  /* ================= 聊天页接线 ================= */

  /** 在气泡的按钮行里加一个「朗读」（chat.js 渲染气泡时调用，传选择器结果） */
  function mountSpeakBtn(btn, text) {
    if (!btn) return;
    btn.textContent = '🔊 朗读';
    btn.dataset.busy = '⏹ 停止';
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      speak(text, { btn: btn });
    });
  }

  /** 回复自动朗读（读后端 voice.autoSpeak；写不进去就说原因） */
  async function autoSpeakOn() {
    const st = status();
    return !!(st.ok && st.autoSpeak);
  }

  async function setAutoSpeak(on) {
    const d = dev();
    if (!d || !d.available || !d.available()) return { ok: false, why: '当前入口不是手机 App' };
    const r = d.call('/api/agent/voice/config', 'POST', JSON.stringify({ autoSpeak: !!on }));
    if (!r || r.ok === false) return { ok: false, why: (r && r.error) || '保存失败' };
    return { ok: true };
  }

  function init() {
    const mic = $('btnChatMic');
    if (!mic) return;
    // 按住说话：pointerdown 开始录，松手（或滑走）就停下并转写
    const start = async (e) => {
      e.preventDefault();
      mic.classList.add('recording');
      const r = await holdStart();
      if (!r.ok) mic.classList.remove('recording');
    };
    const finish = async () => {
      if (!isHolding()) { mic.classList.remove('recording'); return; }
      mic.classList.remove('recording');
      mic.disabled = true;
      mic.title = '识别中…';
      const r = await holdStop();
      mic.disabled = false;
      mic.title = '按住说话';
      if (!r.ok) { if (r.why) toast(r.why, 4200); return; }
      const input = $('chatInput');
      if (!input) return;
      const cur = input.value.trim();
      input.value = cur ? (cur + ' ' + r.text) : r.text;
      input.dispatchEvent(new Event('input'));
      toast('听写好了，检查一下再点发送', 2600);
    };
    mic.addEventListener('pointerdown', start);
    mic.addEventListener('pointerup', finish);
    mic.addEventListener('pointercancel', finish);
    mic.addEventListener('pointerleave', () => { if (isHolding()) finish(); });
    // 长按选中/右键菜单会打断按住说话，直接挡掉
    mic.addEventListener('contextmenu', (e) => e.preventDefault());
  }

  return {
    init, speak, stop, isSpeaking,
    holdStart, holdStop, isHolding,
    mountSpeakBtn, autoSpeakOn, setAutoSpeak, status, kernel,
    toWav16k, encodeWav16,   // 自测用
  };
})();
