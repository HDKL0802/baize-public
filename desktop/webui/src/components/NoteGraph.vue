<!-- 知识星图（共用组件）：力导向画布 + 拖拽 + 点选。
     数据源 GET /api/agent/notes/graph。原来写在 MemoryView.vue 里，
     独立知识库要同一张星图，就抽成组件复用，避免两套画法。 -->
<script setup>
import { ref, onMounted, onBeforeUnmount, nextTick, watch } from 'vue';
import { API } from '../api.js';

const props = defineProps({
  limit: { type: Number, default: 240 },
  auto: { type: Boolean, default: true },
  showTags: { type: Boolean, default: true },
  selectedKey: { type: String, default: '' },
  height: { type: String, default: 'calc(100vh - 330px)' },
});
const emit = defineEmits(['open-note', 'open-tag', 'meta']);

const canvasRef = ref(null);
const loading = ref(false);
const meta = ref({ noteCount: 0, shown: 0, explicitEdges: 0, autoEdges: 0, orphans: 0 });
const emptyText = ref('');

let sim = null;
let raf = 0;
let ctx = null;
let cssW = 0, cssH = 0;
let dragNode = null;
let themeWatch = null;
const COLORS = {};

function readColors() {
  const cs = getComputedStyle(document.documentElement);
  COLORS.note = cs.getPropertyValue('--accent').trim() || '#3BE0C4';
  COLORS.tag = cs.getPropertyValue('--warn').trim() || '#D9A45B';
  COLORS.missing = cs.getPropertyValue('--err').trim() || '#E5765B';
  COLORS.mute = cs.getPropertyValue('--fg-3').trim() || '#6F6C62';
  COLORS.ink = cs.getPropertyValue('--fg').trim() || '#E6E1D6';
  COLORS.line = cs.getPropertyValue('--line-strong').trim() || '#2A2F28';
}

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/notes/graph?limit=' + props.limit +
    '&tags=' + (props.showTags ? '1' : '0') + '&auto=' + (props.auto ? '1' : '0'));
  loading.value = false;
  if (!r.ok) {
    emptyText.value = r.error || '读取星图失败';
    return;
  }
  const d = r.data || {};
  meta.value = {
    noteCount: d.noteCount || 0, shown: d.shown || 0,
    explicitEdges: d.explicitEdges || 0, autoEdges: d.autoEdges || 0,
    orphans: d.orphans || 0,
  };
  emptyText.value = d.note || '';
  emit('meta', meta.value);
  await nextTick();
  buildSim(d.nodes || [], d.edges || []);
}

function radiusOf(n) {
  const deg = n.degree || 0;
  if (n.kind === 'tag') return 4 + Math.min(6, Math.sqrt(deg) * 1.6);
  if (n.kind === 'unresolved') return 3.5;
  return 4.5 + Math.min(9, Math.sqrt(deg) * 2.2);
}

function buildSim(nodes, edges) {
  resizeCanvas();
  const w = cssW || 900, h = cssH || 500;
  const list = nodes.map((n, i) => {
    const a = (i / Math.max(1, nodes.length)) * Math.PI * 2;
    const rad = Math.min(w, h) * 0.34 * (0.25 + (i % 7) / 8);
    return {
      ...n, r: radiusOf(n),
      x: w / 2 + Math.cos(a) * rad + (Math.random() - 0.5) * 30,
      y: h / 2 + Math.sin(a) * rad + (Math.random() - 0.5) * 30,
      vx: 0, vy: 0,
    };
  });
  const byKey = new Map(list.map(n => [n.key, n]));
  // 后端的边记的是节点 key：笔记 key 是路径、标签是 "t:<名字>"、悬空是 "u:<目标>"
  const links = edges
    .filter(e => byKey.has(e.from) && byKey.has(e.to) && e.from !== e.to)
    .map(e => ({ kind: e.kind, detail: e.detail, weight: e.weight || 1, s: byKey.get(e.from), t: byKey.get(e.to) }));
  sim = { nodes: list, edges: links, alpha: 1, ticks: 0, running: true };
  startLoop();
}

function tick() {
  if (!sim) return;
  const { nodes, edges, alpha } = sim;
  const w = cssW, h = cssH;
  const k = 900;
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i];
    for (let j = i + 1; j < nodes.length; j++) {
      const b = nodes[j];
      let dx = b.x - a.x, dy = b.y - a.y;
      let d2 = dx * dx + dy * dy;
      if (d2 < 1) { dx = (Math.random() - 0.5) * 2; dy = (Math.random() - 0.5) * 2; d2 = 1; }
      const d = Math.sqrt(d2);
      const f = (alpha * k) / Math.max(d2, 40);
      const fx = (dx / d) * f, fy = (dy / d) * f;
      a.vx -= fx; a.vy -= fy;
      b.vx += fx; b.vy += fy;
    }
  }
  for (const e of edges) {
    const dx = e.t.x - e.s.x, dy = e.t.y - e.s.y;
    const d = Math.max(1, Math.sqrt(dx * dx + dy * dy));
    const rest = e.kind === 'auto' ? 120 : 84;
    const f = alpha * (d - rest) * 0.055;
    const fx = (dx / d) * f, fy = (dy / d) * f;
    e.s.vx += fx; e.s.vy += fy;
    e.t.vx -= fx; e.t.vy -= fy;
  }
  const pad = 24;
  for (const n of nodes) {
    n.vx += (w / 2 - n.x) * 0.006 * alpha;
    n.vy += (h / 2 - n.y) * 0.006 * alpha;
    n.x += n.vx; n.y += n.vy;
    n.vx *= 0.86; n.vy *= 0.86;
    n.x = Math.max(pad, Math.min(w - pad, n.x));
    n.y = Math.max(pad, Math.min(h - pad, n.y));
  }
  sim.alpha *= 0.985;
  sim.ticks++;
  if (sim.ticks > 520 || sim.alpha < 0.02) sim.running = false;
}

function draw() {
  if (!ctx) return;
  ctx.clearRect(0, 0, cssW, cssH);
  if (!sim) return;
  const selectedId = props.selectedKey ? 'n:' + props.selectedKey : '';
  for (const e of sim.edges) {
    const hot = selectedId && (e.s.id === selectedId || e.t.id === selectedId);
    if (e.kind === 'auto') {
      ctx.strokeStyle = hot ? hexA(COLORS.note, 0.5) : hexA(COLORS.mute, 0.35);
      ctx.setLineDash([4, 4]);
    } else if (e.detail === 'tag') {
      ctx.strokeStyle = hexA(COLORS.tag, hot ? 0.6 : 0.22);
      ctx.setLineDash([]);
    } else if (e.detail === 'missing') {
      ctx.strokeStyle = hexA(COLORS.missing, hot ? 0.6 : 0.25);
      ctx.setLineDash([2, 3]);
    } else {
      ctx.strokeStyle = hot ? hexA(COLORS.note, 0.75) : hexA(COLORS.ink, 0.22);
      ctx.setLineDash([]);
    }
    ctx.lineWidth = hot ? 1.6 : 0.8;
    ctx.beginPath();
    ctx.moveTo(e.s.x, e.s.y);
    ctx.lineTo(e.t.x, e.t.y);
    ctx.stroke();
  }
  ctx.setLineDash([]);
  for (const n of sim.nodes) {
    const base = n.kind === 'tag' ? COLORS.tag : n.kind === 'unresolved' ? COLORS.missing : COLORS.note;
    ctx.beginPath();
    ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
    if (n.kind === 'unresolved') {
      ctx.fillStyle = hexA(base, 0.15);
      ctx.strokeStyle = hexA(base, 0.5);
      ctx.lineWidth = 1;
      ctx.fill(); ctx.stroke();
    } else {
      ctx.fillStyle = n.id === selectedId ? base : hexA(base, 0.75);
      ctx.fill();
      if (n.id === selectedId) {
        ctx.strokeStyle = COLORS.ink;
        ctx.lineWidth = 1.5;
        ctx.stroke();
      }
    }
    const label = n.title || n.key || '';
    if (label && (n.kind === 'tag' || (n.degree || 0) >= 2 || n.id === selectedId)) {
      ctx.font = '10px ui-monospace, Consolas, monospace';
      ctx.fillStyle = n.kind === 'tag' ? hexA(COLORS.tag, 0.9) : hexA(COLORS.ink, 0.72);
      ctx.textAlign = 'center';
      ctx.fillText(clip(label, 16), n.x, n.y + n.r + 11);
    }
  }
}

function clip(s, n) { return s.length > n ? s.slice(0, n) + '…' : s; }
function hexA(hex, a) {
  if (!hex || hex.startsWith('rgb')) return hex || `rgba(160,160,150,${a})`;
  const v = hex.replace('#', '');
  const full = v.length === 3 ? v.split('').map(c => c + c).join('') : v;
  const r = parseInt(full.slice(0, 2), 16), g = parseInt(full.slice(2, 4), 16), b = parseInt(full.slice(4, 6), 16);
  return `rgba(${r},${g},${b},${a})`;
}

function startLoop() {
  cancelAnimationFrame(raf);
  const loop = () => {
    if (sim && (sim.running || dragNode)) tick();
    draw();
    raf = requestAnimationFrame(loop);
  };
  raf = requestAnimationFrame(loop);
}

function resizeCanvas() {
  const el = canvasRef.value;
  if (!el) return;
  const box = el.parentElement.getBoundingClientRect();
  cssW = Math.max(320, Math.floor(box.width));
  cssH = Math.max(320, Math.floor(box.height));
  const dpr = window.devicePixelRatio || 1;
  el.width = Math.floor(cssW * dpr);
  el.height = Math.floor(cssH * dpr);
  el.style.width = cssW + 'px';
  el.style.height = cssH + 'px';
  ctx = el.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  if (sim) {
    for (const n of sim.nodes) {
      n.x = Math.max(24, Math.min(cssW - 24, n.x));
      n.y = Math.max(24, Math.min(cssH - 24, n.y));
    }
  }
}

function hitTest(x, y) {
  if (!sim) return null;
  let best = null, bestD = Infinity;
  for (const n of sim.nodes) {
    const d = (n.x - x) ** 2 + (n.y - y) ** 2;
    if (d < (n.r + 6) ** 2 && d < bestD) { best = n; bestD = d; }
  }
  return best;
}

function onPointerDown(e) {
  const rect = canvasRef.value.getBoundingClientRect();
  const x = e.clientX - rect.left, y = e.clientY - rect.top;
  const n = hitTest(x, y);
  if (!n) return;
  dragNode = { node: n, moved: false, sx: x, sy: y };
  canvasRef.value.setPointerCapture(e.pointerId);
}
function onPointerMove(e) {
  if (!dragNode) return;
  const rect = canvasRef.value.getBoundingClientRect();
  const x = e.clientX - rect.left, y = e.clientY - rect.top;
  if (Math.abs(x - dragNode.sx) + Math.abs(y - dragNode.sy) > 4) dragNode.moved = true;
  dragNode.node.x = x;
  dragNode.node.y = y;
  dragNode.node.vx = 0;
  dragNode.node.vy = 0;
  e.preventDefault();
}
function onPointerUp(e) {
  if (!dragNode) return;
  const n = dragNode.node, moved = dragNode.moved;
  dragNode = null;
  try { canvasRef.value.releasePointerCapture(e.pointerId); } catch (err) { /* 忽略 */ }
  if (moved) return;
  if (n.kind === 'tag') {
    emit('open-tag', n.title || '');
    return;
  }
  if (n.kind === 'note' && n.key) emit('open-note', n.key);
}

watch(() => props.auto, load);
watch(() => props.showTags, load);
watch(() => props.selectedKey, draw);

defineExpose({ reload: load });

onMounted(() => {
  readColors();
  themeWatch = new MutationObserver(() => { readColors(); draw(); });
  themeWatch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  window.addEventListener('resize', resizeCanvas);
  nextTick(() => { resizeCanvas(); load(); startLoop(); });
});
onBeforeUnmount(() => {
  cancelAnimationFrame(raf);
  if (themeWatch) themeWatch.disconnect();
  window.removeEventListener('resize', resizeCanvas);
  sim = null;
});
</script>

<template>
  <div class="ng-root">
    <div class="ng-wrap" :style="{ height }">
      <canvas ref="canvasRef"
              @pointerdown="onPointerDown"
              @pointermove="onPointerMove"
              @pointerup="onPointerUp"
              @pointercancel="onPointerUp"></canvas>
      <div v-if="loading" class="ng-hint">正在算星图…</div>
      <div v-else-if="emptyText && !meta.shown" class="ng-hint">
        <div class="ng-hint-title">{{ meta.noteCount ? '这张图暂时是空的' : '还没有笔记' }}</div>
        <div class="ng-hint-desc">{{ emptyText }}</div>
      </div>
    </div>
    <div class="ng-legend">
      <span class="ng-legend-item"><i class="ng-dot note"></i>笔记</span>
      <span class="ng-legend-item"><i class="ng-dot tag"></i>标签</span>
      <span class="ng-legend-item"><i class="ng-dot missing"></i>悬空链接</span>
      <span class="ng-legend-item"><i class="ng-line solid"></i>显式链接</span>
      <span class="ng-legend-item"><i class="ng-line dashed"></i>自动相关</span>
      <span class="ng-legend-meta">
        {{ meta.noteCount }} 篇 · 显式 {{ meta.explicitEdges }} · 自动 {{ meta.autoEdges }}
        · 孤立 {{ meta.orphans }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.ng-wrap { position: relative; width: 100%; min-height: 380px; }
.ng-wrap canvas { display: block; cursor: grab; touch-action: none; }
.ng-wrap canvas:active { cursor: grabbing; }
.ng-hint {
  position: absolute; inset: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: 6px; padding: 0 24px;
  font-size: 12px; color: var(--fg-3); text-align: center; pointer-events: none;
}
.ng-hint-title { font-size: 13px; color: var(--fg-2); }
.ng-hint-desc { max-width: 420px; line-height: 1.6; }
.ng-legend {
  display: flex; align-items: center; gap: 14px; flex-wrap: wrap;
  padding: 8px 14px; border-top: 1px solid var(--line); font-size: 11px; color: var(--fg-3);
}
.ng-legend-item { display: inline-flex; align-items: center; gap: 5px; }
.ng-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); }
.ng-dot.tag { background: var(--warn); }
.ng-dot.missing { background: var(--err); opacity: .6; }
.ng-line { display: inline-block; width: 18px; height: 0; border-top: 1px solid var(--fg-3); }
.ng-line.dashed { border-top-style: dashed; }
.ng-legend-meta { margin-left: auto; font-family: var(--mono); }
</style>
