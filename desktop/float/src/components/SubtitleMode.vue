<!-- 实时字幕（subtitle）：贴屏幕底部的置顶横条 -->
<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue';
import { api, local } from '../api.js';
import { toWav16k, blobToBase64, startRec, stopRec } from '../voice.js';

function close() { local('/float/close', {}); }

const SUB_SEG_MS = 3800;
let on = false, busyCt = 0, err = '';
const cfg = ref({ to: 'zh', showSource: true, font: 'm' });

const stat = ref('');
const statCls = ref('');
const subItems = ref([]);
const toggleText = ref('开始');

function setStat(text, cls) { stat.value = text; statCls.value = cls || ''; }
function paintStat() {
  if (err) { setStat(err, 'err'); return; }
  const l = '译文：' + (cfg.value.to === 'zh' ? '中文' : 'English');
  setStat(l + (on ? (busyCt > 0 ? ' · 转写中…' : ' · 监听中') : ' · 已暂停'));
}
function paintCfg() {
  subItems.value.forEach(() => {}); // 占位
}
function saveCfg() {
  local('/subtitle', { to: cfg.value.to, showSource: cfg.value.showSource, font: cfg.value.font });
}
function addSubLine(src, dst, dstErr) {
  subItems.value.push({ src, dst, dstErr, key: Date.now() + Math.random() });
  while (subItems.value.length > 3) subItems.value.shift();
}
async function onSegment(blob, startAt, segErr) {
  if (segErr) { on = false; toggleText.value = '开始'; err = '录音器起不来：' + ((segErr && segErr.message) || segErr); paintStat(); return; }
  busyCt++;
  try {
    let b64;
    try { b64 = await blobToBase64(await toWav16k(blob)); }
    catch (e) { err = '录音转换失败：' + ((e && e.message) || e); return; }
    const r = await api('/api/agent/voice/stt', 'POST', { audio: b64, filename: 'seg.wav' });
    const d = (r && r.data) || {};
    if (!r.ok || d.ok === false) { err = '转写失败：' + ((d && d.error) || (r && r.error) || '后端没回结果'); return; }
    const src = String(d.text || '').trim();
    if (!src) { err = ''; return; }
    let dst = '', dstErr = '';
    const tr = await api('/api/agent/voice/translate', 'POST', { text: src, to: cfg.value.to });
    const td = (tr && tr.data) || {};
    if (tr.ok && td.ok !== false) dst = String(td.text || '').trim();
    else dstErr = (td && td.error) || (tr && tr.error) || '未知原因';
    addSubLine(src, dst, dstErr);
    err = '';
  } finally { busyCt--; paintStat(); }
}

async function toggle() {
  if (on) { stopRec(); on = false; toggleText.value = '开始'; paintStat(); return; }
  err = '';
  const r = await startRec(SUB_SEG_MS, onSegment);
  if (!r.ok) { err = r.why; paintStat(); return; }
  on = true; toggleText.value = '暂停'; paintStat();
}

function toggleLang() {
  cfg.value.to = cfg.value.to === 'zh' ? 'en' : 'zh';
  saveCfg(); paintStat();
}
function toggleSrc() {
  cfg.value.showSource = !cfg.value.showSource;
  saveCfg();
}
function setFont(f) {
  cfg.value.font = f;
  saveCfg();
}

onMounted(async () => {
  try {
    const c = await local('/subtitle');
    if (c && c.ok) {
      if (c.to === 'zh' || c.to === 'en') cfg.value.to = c.to;
      if (typeof c.showSource === 'boolean') cfg.value.showSource = c.showSource;
      if (c.font === 's' || c.font === 'm' || c.font === 'l') cfg.value.font = c.font;
    }
  } catch (e) { /* 用默认值 */ }
  paintStat();
});
</script>

<template>
  <div class="f-sub">
    <div class="f-subbar">
      <button class="fsbtn" @click="toggle">{{ toggleText }}</button>
      <button class="fsbtn" @click="toggleLang">{{ cfg.to === 'zh' ? '中' : 'EN' }}</button>
      <button :class="['fsbtn', { on: cfg.showSource }]" @click="toggleSrc">显示原文</button>
      <span class="fsfont">
        <button v-for="f in ['s','m','l']" :key="f" :class="['fsbtn', { on: cfg.font === f }]" @click="setFont(f)">
          {{ f === 's' ? '小' : f === 'm' ? '中' : '大' }}
        </button>
      </span>
      <span :class="['fsstat', statCls]">{{ stat }}</span>
      <button class="fsbtn" @click="close">关闭</button>
    </div>
    <div :class="['f-sublist', 'f-' + cfg.font, { 'hide-src': !cfg.showSource }]">
      <div v-for="(it, i) in subItems" :key="it.key" class="f-sub-item">
        <div class="f-src">{{ it.src }}</div>
        <div :class="['f-dst', { bad: it.dstErr }]">{{ it.dstErr ? '翻译失败：' + it.dstErr : it.dst }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.f-sub { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.f-subbar {
  display: flex; align-items: center; gap: 4px; padding: 2px 6px;
  background: var(--surface); border-bottom: 1px solid var(--border);
  opacity: .32; transition: opacity .15s; user-select: none;
}
.f-subbar:hover { opacity: 1; }
.fsbtn {
  border: 1px solid var(--border-strong); background: transparent; color: var(--text-2);
  border-radius: 4px; padding: 1px 7px; font: inherit; font-size: 11px; line-height: 1.6;
  cursor: pointer; white-space: nowrap;
}
.fsbtn:hover { background: var(--surface-2); color: var(--text); }
.fsbtn.on { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
.fsfont { display: inline-flex; gap: 2px; }
.fsstat {
  flex: 1; min-width: 0; margin-left: 6px; color: var(--text-3); font-size: 10.5px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: right;
}
.fsstat.err { color: var(--err); }
.f-sublist {
  flex: 1; min-height: 0; display: flex; flex-direction: column; justify-content: flex-end;
  gap: 10px; padding: 10px 18px 16px; overflow: hidden; text-align: center; --sub-size: 22px;
}
.f-sublist.f-s { --sub-size: 16px; }
.f-sublist.f-m { --sub-size: 22px; }
.f-sublist.f-l { --sub-size: 30px; }
.f-sub-item { transition: opacity .2s; }
.f-src { color: var(--text-2); font-size: var(--sub-size); line-height: 1.4; word-break: break-word; }
.f-dst { color: var(--accent); font-size: calc(var(--sub-size) * .92); font-weight: 600; line-height: 1.45; margin-top: 2px; word-break: break-word; }
.f-dst.bad { color: var(--warn); font-weight: 400; }
.f-sublist.hide-src .f-src { display: none; }
</style>
