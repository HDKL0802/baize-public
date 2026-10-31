<!-- 语音转写（dictate）：分段录音 → STT → 攒成一整篇 -->
<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { toWav16k, blobToBase64, startRec, stopRec } from '../voice.js';

const DICTATE_SEG_MS = 5000;
let on = false, count = 0, t0 = 0, tick = 0, busyCt = 0, err = '';
const stat = ref('');
const statCls = ref('');
const lines = ref([]);
const recBtnText = ref('开始录音');
const recBtnPrimary = ref(true);

/* 波形示意：录音在 voice.js 内部跑，没有音量数据源，用录音状态 + 计时驱动条高 */
const waves = ref(Array.from({ length: 16 }, () => 0.2));
let waveTick = 0;
function paintWave() {
  const t = waveTick++;
  const running = !recBtnPrimary.value;
  waves.value = Array.from({ length: 16 }, (_, i) =>
    running ? 0.26 + 0.62 * Math.abs(Math.sin(i * 1.9 + t * 0.9)) : 0.16);
}

function hhmmss(d) { const p = n => String(n).padStart(2, '0'); return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()); }
function paintStat(text, cls) { stat.value = text; statCls.value = cls || ''; }
function paintDictate() {
  paintWave();
  if (err) { paintStat(err, 'err'); return; }
  if (busyCt > 0) { paintStat('正在转写…'); return; }
  const sec = on ? Math.floor((Date.now() - t0) / 1000) : 0;
  const mmss = String(Math.floor(sec / 60)).padStart(2, '0') + ':' + String(sec % 60).padStart(2, '0');
  paintStat(on ? ('录音中 ' + mmss + ' · 已转写 ' + count + ' 段') : ('已停止 · 已转写 ' + count + ' 段'));
}
function setBtn(onState) {
  recBtnText.value = onState ? '停止录音' : '开始录音';
  recBtnPrimary.value = !onState;
}
function addLine(at, text) {
  lines.value.push({ ts: '[' + hhmmss(at) + '] ', text });
}
function text() { return lines.value.map(l => l.text).join('\n'); }

async function onSegment(blob, startAt, segErr) {
  if (segErr) {
    on = false; setBtn(false);
    err = '录音器起不来：' + ((segErr && segErr.message) || segErr);
    paintDictate(); return;
  }
  busyCt++;
  try {
    let b64;
    try { b64 = await blobToBase64(await toWav16k(blob)); }
    catch (e) { err = '录音转换失败：' + ((e && e.message) || e); return; }
    const r = await api('/api/agent/voice/stt', 'POST', { audio: b64, filename: 'seg.wav' });
    const d = (r && r.data) || {};
    if (!r.ok || d.ok === false) { err = '转写失败：' + ((d && d.error) || (r && r.error) || '后端没回结果'); return; }
    const txt = String(d.text || '').trim();
    if (txt) { addLine(new Date(startAt || Date.now()), txt); count++; }
    err = '';
  } finally { busyCt--; paintDictate(); }
}

async function toggle() {
  if (on) {
    stopRec(); on = false;
    if (tick) { clearInterval(tick); tick = 0; }
    setBtn(false); paintDictate(); return;
  }
  err = '';
  const r = await startRec(DICTATE_SEG_MS, onSegment);
  if (!r.ok) { paintStat(r.why, 'err'); return; }
  on = true; t0 = Date.now(); setBtn(true); paintDictate();
  tick = setInterval(paintDictate, 1000);
}

async function save() {
  const content = text().trim();
  if (!content) { paintStat('还没有转写内容，先录一段', 'err'); return; }
  const d = new Date(), p = n => String(n).padStart(2, '0');
  const title = '语音转写 ' + d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  paintStat('正在保存…');
  const r = await api('/api/agent/notes', 'POST', { title, content, tags: ['语音转写'] });
  const dd = (r && r.data) || {};
  if (!r.ok || dd.ok === false) { paintStat('保存失败：' + ((dd && dd.error) || (r && r.error) || '未知'), 'err'); return; }
  paintStat('已保存为笔记：' + title);
}
async function copy() {
  const content = text();
  if (!content.trim()) { paintStat('还没有可复制的内容', 'err'); return; }
  try { await navigator.clipboard.writeText(content); paintStat('已复制到剪贴板'); }
  catch (e) { paintStat('复制失败：' + ((e && e.message) || e), 'err'); }
}
function clear() { lines.value = []; count = 0; paintDictate(); }

onMounted(() => paintStat('就绪 · 按「开始录音」'));
</script>

<template>
  <div class="f-dictate">
    <div class="f-rec">
      <button :class="['f-recbtn', { rec: !recBtnPrimary }]" :title="recBtnText" @click="toggle">
        <span class="f-ring" aria-hidden="true"></span>
        <!-- 录音中显示停止方块，空闲显示麦克风 -->
        <svg v-if="recBtnPrimary" class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 2a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z" />
          <path d="M19 10v1a7 7 0 0 1-14 0v-1" />
          <path d="M12 18v3" />
        </svg>
        <svg v-else class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <rect x="7" y="7" width="10" height="10" rx="2" />
        </svg>
      </button>
      <div :class="['f-recstat', statCls]">{{ stat }}</div>
      <div class="f-wave">
        <i v-for="(l, i) in waves" :key="i" :style="{ height: (4 + l * 24) + 'px' }"></i>
      </div>
    </div>

    <div class="f-dlist">
      <div v-for="(l, i) in lines" :key="i" class="f-dline">
        <span class="f-dts mono">{{ l.ts }}</span><span class="f-dtx">{{ l.text }}</span>
      </div>
    </div>

    <div class="f-dbar">
      <button class="fbtn" @click="save">保存为笔记</button>
      <button class="fbtn" @click="copy">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <rect x="8" y="8" width="13" height="13" rx="2" />
          <path d="M4 16a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2" />
        </svg>
        复制全部
      </button>
      <button class="fbtn" @click="clear">清空</button>
    </div>
    <div class="f-dtip">不做说话人分离：当前语音通道（dashscope paraformer-realtime / openai whisper）都不提供说话人分离，所以转写结果里不会有「说话人 A/B」。</div>
  </div>
</template>

<style scoped>
.f-dictate { flex: 1; min-height: 0; display: flex; flex-direction: column; }

/* 录音区：64 圆形按钮 + 状态 + 波形 */
.f-rec {
  flex: 0 0 auto; display: flex; flex-direction: column; align-items: center; gap: 8px;
  padding: 16px 12px 10px;
}
.f-recbtn {
  position: relative; width: 64px; height: 64px; display: grid; place-items: center;
  border: 0; border-radius: 999px; background: var(--accent); color: var(--accent-ink);
  cursor: pointer; transition: background 120ms cubic-bezier(.2,0,0,1);
}
.f-recbtn:hover { background: var(--accent-2); }
.f-recbtn .ic { width: 26px; height: 26px; stroke-width: 1.6; }
.f-recbtn:active { transform: scale(.98); }
.f-ring {
  position: absolute; inset: -4px; border: 2px solid var(--accent); border-radius: 999px;
  opacity: 0; pointer-events: none;
}
/* 录音中：2px accent 外环 200ms 呼吸（全窗唯一循环动效） */
.f-recbtn.rec .f-ring { animation: fring 200ms cubic-bezier(.2,0,0,1) infinite alternate; }
@keyframes fring {
  from { opacity: .25; }
  to { opacity: .9; }
}
.f-recstat {
  font-size: 12px; color: var(--text-2); text-align: center; line-height: 1.5;
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.f-recstat.err { color: var(--err); }
.f-wave { display: flex; align-items: center; justify-content: center; gap: 3px; height: 30px; }
.f-wave i {
  display: block; width: 3px; border-radius: 2px; background: var(--line-strong);
  transition: height 200ms cubic-bezier(.2,0,0,1), background 120ms cubic-bezier(.2,0,0,1);
}
.f-recbtn.rec ~ .f-wave i { background: var(--accent); opacity: .85; }

/* 转写结果 */
.f-dlist {
  flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden; margin: 0 12px;
  padding: 10px 12px; background: var(--surface-2); border: 1px solid var(--line);
  border-radius: 6px; font-size: 13px; line-height: 1.7; user-select: text;
}
.f-dlist:empty::before {
  content: '按上面的按钮开始录音，每 5 秒转一段，自动攒成一整篇。';
  color: var(--text-3); font-size: 12px;
}
.f-dline { word-break: break-word; }
.f-dts { color: var(--text-3); font-size: 11px; margin-right: 6px; }
.f-dtx { color: var(--text); }

/* 底部操作 */
.f-dbar { flex: 0 0 auto; display: flex; gap: 8px; padding: 10px 12px 6px; }
.fbtn {
  flex: 1; min-width: 0; height: 32px; padding: 0 8px;
  display: inline-flex; align-items: center; justify-content: center; gap: 5px;
  border: 1px solid var(--line-strong); border-radius: 4px;
  background: var(--surface-2); color: var(--text);
  font: inherit; font-size: 12px; cursor: pointer; white-space: nowrap;
  transition: background 120ms cubic-bezier(.2,0,0,1), border-color 120ms cubic-bezier(.2,0,0,1);
}
.fbtn:hover { background: var(--surface-3); border-color: var(--line-strong); }
.fbtn .ic { width: 14px; height: 14px; }
.f-dtip { flex: 0 0 auto; padding: 0 12px 10px; color: var(--text-3); font-size: 11px; line-height: 1.5; }
</style>
