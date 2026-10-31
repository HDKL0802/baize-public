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
  while (subItems.value.length > 2) subItems.value.shift();   // 最多两条，字幕不占太多屏
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
      <button class="fsbtn" :title="toggleText" @click="toggle">
        <svg v-if="toggleText === '暂停'" class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <rect x="7" y="7" width="10" height="10" rx="2" />
        </svg>
        <svg v-else class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 2a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3Z" />
          <path d="M19 10v1a7 7 0 0 1-14 0v-1" />
          <path d="M12 18v3" />
        </svg>
        {{ toggleText }}
      </button>
      <button class="fsbtn mono" @click="toggleLang">{{ cfg.to === 'zh' ? '中' : 'EN' }}</button>
      <button :class="['fsbtn', { on: cfg.showSource }]" @click="toggleSrc">原文</button>
      <span class="fsfont">
        <button v-for="f in ['s','m','l']" :key="f" :class="['fsbtn', { on: cfg.font === f }]" @click="setFont(f)">
          {{ f === 's' ? '小' : f === 'm' ? '中' : '大' }}
        </button>
      </span>
      <span :class="['fsstat', statCls]">{{ stat }}</span>
      <button class="fsbtn icon" title="关闭浮窗" @click="close">
        <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
          <path d="M18 6 6 18" /><path d="m6 6 12 12" />
        </svg>
      </button>
    </div>
    <div :class="['f-sublist', 'f-' + cfg.font, { 'hide-src': !cfg.showSource }]">
      <div v-if="!subItems.length" class="f-empty">点「开始」后，这里会滚动显示实时字幕。</div>
      <div v-for="(it, i) in subItems" :key="it.key" :class="['f-sub-item', { older: i < subItems.length - 1 }]">
        <div class="f-src">{{ it.src }}</div>
        <div :class="['f-dst', { bad: it.dstErr }]">{{ it.dstErr ? '翻译失败：' + it.dstErr : it.dst }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 字幕条始终压在深色半透明底上，所以这里不走主题变量，固定高对比配色 */
.f-sub {
  flex: 1; min-height: 0; display: flex; flex-direction: column;
  background: rgba(7,8,7,.82); border: 1px solid var(--line); border-radius: 2px;
  overflow: hidden;
  --sd-line: rgba(230,225,214,.16);
  --sd-text: #E6E1D6;
  --sd-dim: #A6A296;
  --sd-faint: #6F6C62;
}
.f-subbar {
  flex: 0 0 auto; display: flex; align-items: center; gap: 6px;
  padding: 4px 8px; border-bottom: 1px solid var(--sd-line);
  opacity: .38; transition: opacity 120ms cubic-bezier(.2,0,0,1); user-select: none;
}
.f-subbar:hover { opacity: 1; }
.fsbtn {
  height: 24px; padding: 0 7px; display: inline-flex; align-items: center; gap: 4px;
  border: 1px solid var(--sd-line); border-radius: 4px; background: transparent;
  color: var(--sd-dim); font: inherit; font-size: 11px; line-height: 1; cursor: pointer;
  white-space: nowrap;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1),
              border-color 120ms cubic-bezier(.2,0,0,1);
}
.fsbtn:hover { background: rgba(255,255,255,.07); color: var(--sd-text); }
.fsbtn.on {
  background: rgba(59,224,196,.12); border-color: var(--accent); color: var(--accent); font-weight: 600;
}
/* 图标按钮：24×24 命中区 */
.fsbtn.icon { width: 24px; padding: 0; justify-content: center; border-color: transparent; }
.fsbtn .ic { width: 13px; height: 13px; stroke-width: 1.7; }
.fsfont { display: inline-flex; gap: 2px; }
.fsstat {
  flex: 1; min-width: 0; color: var(--sd-faint); font-size: 10.5px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: right;
}
.fsstat.err { color: var(--err); }

/* 字幕正文：15px/1.5，最多两行 */
.f-sublist {
  flex: 1; min-height: 0; display: flex; flex-direction: column; justify-content: flex-end;
  gap: 8px; padding: 10px 14px 12px; overflow: hidden; text-align: center;
  --sub-size: 15px;
}
.f-sublist.f-s { --sub-size: 13px; }
.f-sublist.f-m { --sub-size: 15px; }
.f-sublist.f-l { --sub-size: 18px; }
.f-empty { color: var(--sd-faint); font-size: 12px; line-height: 1.55; }
.f-sub-item { min-height: 0; transition: opacity 200ms cubic-bezier(.2,0,0,1); }
.f-sub-item.older { opacity: .38; }
.f-src {
  color: var(--sd-dim); font-size: calc(var(--sub-size) - 3px); line-height: 1.4;
  word-break: break-word; overflow: hidden;
  display: -webkit-box; -webkit-line-clamp: 1; -webkit-box-orient: vertical;
}
.f-dst {
  color: var(--accent); font-size: var(--sub-size); font-weight: 600; line-height: 1.5;
  margin-top: 2px; word-break: break-word; overflow: hidden;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
.f-dst.bad { color: var(--warn); font-weight: 400; }
.f-sublist.hide-src .f-src { display: none; }
</style>
