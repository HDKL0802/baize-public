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

function hhmmss(d) { const p = n => String(n).padStart(2, '0'); return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()); }
function paintStat(text, cls) { stat.value = text; statCls.value = cls || ''; }
function paintDictate() {
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
    <div class="f-recbar">
      <button :class="['fbtn', recBtnPrimary ? 'primary' : 'stop']" @click="toggle">{{ recBtnText }}</button>
      <span :class="['f-recstat', statCls]">{{ stat }}</span>
    </div>
    <div class="f-dlist">
      <div v-for="(l, i) in lines" :key="i" class="f-dline"><span class="f-dts">{{ l.ts }}</span><span class="f-dtx">{{ l.text }}</span></div>
    </div>
    <div class="f-dbar">
      <button class="fbtn" @click="save">保存为笔记</button>
      <button class="fbtn" @click="copy">复制全部</button>
      <button class="fbtn" @click="clear">清空</button>
    </div>
    <div class="f-dtip">不做说话人分离：当前语音通道（dashscope paraformer-realtime / openai whisper）都不提供说话人分离，所以转写结果里不会有「说话人 A/B」。</div>
  </div>
</template>

<style scoped>
.f-dictate { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.f-recbar { display: flex; align-items: center; gap: 10px; padding: 12px 12px 8px; }
.f-recstat { color: var(--text-2); font-size: 12px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.f-recstat.err { color: var(--err); }
.fbtn {
  border: 1px solid var(--border-strong); background: var(--surface-2); color: var(--text);
  border-radius: 6px; padding: 5px 12px; font: inherit; font-size: 12.5px; cursor: pointer;
  white-space: nowrap;
}
.fbtn:hover { background: var(--surface-3, var(--surface-2)); }
.fbtn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
.fbtn.stop { background: var(--err); border-color: var(--err); color: var(--accent-ink); font-weight: 600; }
.f-dlist {
  flex: 1; min-height: 0; overflow: auto; margin: 0 12px; padding: 10px 12px; background: var(--surface);
  border: 1px solid var(--border); border-radius: 6px; font-size: 13px; line-height: 1.75; user-select: text;
}
.f-dlist:empty::before { content: '按上面的按钮开始录音，每 5 秒转一段，自动攒成一整篇。'; color: var(--text-3); }
.f-dline { word-break: break-word; }
.f-dts { color: var(--text-3); font-variant-numeric: tabular-nums; }
.f-dtx { color: var(--text); }
.f-dbar { display: flex; gap: 8px; padding: 10px 12px 6px; }
.f-dtip { padding: 0 12px 10px; color: var(--text-3); font-size: 11px; line-height: 1.5; }
</style>
