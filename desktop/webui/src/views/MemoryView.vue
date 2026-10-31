<!-- 记忆星图页：笔记列表 + 关系星图（数据源 /api/agent/notes*）
     笔记正本是磁盘上的 .md 文件，这里只做展示与编辑；星图由索引现场算出来。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const notes = ref([]);
const tags = ref([]);
const stats = ref({});
const total = ref(0);
const loading = ref(true);
const busy = ref(false);
const q = ref('');
const activeTag = ref('');
const tab = ref('graph');

const detail = ref(null);
const detailLoading = ref(false);
const outlinks = ref([]);
const backlinks = ref([]);

const showForm = ref(false);
const form = ref({ path: '', title: '', tags: '', content: '' });

const autoOn = ref(true);
const showTags = ref(true);

const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;
function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2600);
}

async function loadList() {
  loading.value = true;
  const params = new URLSearchParams({ limit: '200' });
  if (q.value.trim()) params.set('q', q.value.trim());
  if (activeTag.value) params.set('tag', activeTag.value);
  const r = await API.get('/api/agent/notes?' + params.toString());
  if (r.ok) {
    const d = r.data || {};
    notes.value = d.notes || [];
    tags.value = d.tags || [];
    stats.value = d.stats || {};
    total.value = d.total || notes.value.length;
    // 当前选中的笔记如果还在，刷新它的详情
    if (detail.value && notes.value.some(n => n.key === detail.value.key)) loadDetail(detail.value.key);
  } else {
    toast(r.error || '读取笔记失败', 'err');
  }
  loading.value = false;
}

function searchNotes() { loadList(); }

async function openTag(t) {
  activeTag.value = activeTag.value === t ? '' : t;
  await loadList();
}

/* ---------- 星图 ---------- */

const canvasRef = ref(null);
const graphMeta = ref({ noteCount: 0, shown: 0, explicitEdges: 0, autoEdges: 0, orphans: 0 });
const graphLoading = ref(false);
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

async function loadGraph() {
  if (tab.value !== 'graph') return;
  graphLoading.value = true;
  const r = await API.get('/api/agent/notes/graph?limit=240&tags=' + (showTags.value ? '1' : '0') + '&auto=' + (autoOn.value ? '1' : '0'));
  graphLoading.value = false;
  if (!r.ok) {
    toast(r.error || '读取星图失败', 'err');
    return;
  }
  const d = r.data || {};
  graphMeta.value = {
    noteCount: d.noteCount || 0, shown: d.shown || 0,
    explicitEdges: d.explicitEdges || 0, autoEdges: d.autoEdges || 0,
    orphans: d.orphans || 0,
  };
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
  // 注意：后端的边记的是节点的 key（不是 id），笔记 key 是路径、标签是 "t:<名字>"、悬空是 "u:<目标>"
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
  const selectedId = detail.value ? 'n:' + detail.value.key : '';
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
  // #RRGGBB → rgba；主题变量偶尔是 rgba()，直接原样返回
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
    openTag(n.title || '');
    switchTab('notes');
    return;
  }
  if (n.kind === 'note' && n.key) {
    openNote(n.key);
  }
}

/* ---------- 详情 ---------- */

async function loadDetail(key) {
  detailLoading.value = true;
  const enc = encodeURIComponent(key).replace(/%2F/g, '/');
  const r = await API.get('/api/agent/notes/' + enc);
  detailLoading.value = false;
  if (r.ok) {
    const d = r.data || {};
    detail.value = d.note || null;
    outlinks.value = d.outlinks || [];
    backlinks.value = d.backlinks || [];
  } else {
    toast(r.error || '读取笔记失败', 'err');
  }
}

async function openNote(key) {
  tab.value = 'notes';
  await loadDetail(key);
  await nextTick();
  document.querySelector('.mem-detail')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

async function removeNote() {
  if (!detail.value) return;
  if (!window.confirm('删除这篇笔记？磁盘上的 .md 文件会一并删掉。')) return;
  const r = await API.post('/api/agent/notes/forget', { key: detail.value.key });
  if (r.ok) {
    toast('已删除', 'ok');
    detail.value = null;
    outlinks.value = [];
    backlinks.value = [];
    await loadList();
    loadGraph();
  } else toast(r.error || '删除失败', 'err');
}

/* ---------- 写 / 维护 ---------- */

async function saveNote() {
  const f = form.value;
  if (!f.content.trim()) { toast('正文不能为空', 'warn'); return; }
  busy.value = true;
  const body = {
    path: f.path.trim(), title: f.title.trim(), content: f.content,
    tags: f.tags.split(/[,，]/).map(s => s.trim()).filter(Boolean),
  };
  const r = await API.post('/api/agent/notes', body);
  busy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  toast('已保存', 'ok');
  showForm.value = false;
  form.value = { path: '', title: '', tags: '', content: '' };
  await loadList();
  loadGraph();
  if (r.data?.note?.key) openNote(r.data.note.key);
}

async function scan() {
  busy.value = true;
  const r = await API.post('/api/agent/notes/scan', {});
  busy.value = false;
  if (r.ok) {
    const s = (r.data && r.data.scan) || {};
    toast(`扫描完成：新增 ${s.indexed || 0}，移除 ${s.removed || 0}`, 'ok');
    await loadList();
    loadGraph();
  } else toast(r.error || '扫描失败', 'err');
}

async function autolink() {
  busy.value = true;
  const r = await API.post('/api/agent/notes/autolink', {});
  busy.value = false;
  if (r.ok) {
    toast(`已按共引关系补边：${r.data.edges || 0} 条`, 'ok');
    loadGraph();
  } else toast(r.error || '自动连边失败', 'err');
}

function toggleAuto() {
  autoOn.value = !autoOn.value;
  loadGraph();
}

function switchTab(t) {
  tab.value = t;
  if (t === 'graph') {
    nextTick(() => {
      resizeCanvas();
      if (!sim) loadGraph();
      else if (sim.nodes.length) { sim.alpha = Math.max(sim.alpha, 0.35); sim.running = true; startLoop(); }
    });
  }
}

function reload() {
  loadList();
  if (tab.value === 'graph') loadGraph();
}

const statCards = computed(() => [
  { label: '笔记', value: stats.value.notes ?? notes.value.length },
  { label: '已解析链接', value: stats.value.links ?? 0 },
  { label: '悬空链接', value: stats.value.missing ?? 0 },
  { label: '标签', value: stats.value.tags ?? tags.value.length },
  { label: '孤立笔记', value: stats.value.orphans ?? 0 },
]);

watch(showTags, () => loadGraph());
watch(autoOn, () => loadGraph());

onMounted(() => {
  readColors();
  loadList();
  themeWatch = new MutationObserver(() => { readColors(); draw(); });
  themeWatch.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  window.addEventListener('resize', resizeCanvas);
  nextTick(() => { resizeCanvas(); loadGraph(); startLoop(); });
});
onBeforeUnmount(() => {
  cancelAnimationFrame(raf);
  if (themeWatch) themeWatch.disconnect();
  window.removeEventListener('resize', resizeCanvas);
  sim = null;
});
</script>

<template>
  <div class="bz-page mem-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">记忆星图</h1>
        <p class="bz-sub">笔记正本是磁盘上的 .md（vault/，Obsidian 可打开）；星图与检索由索引实时算出。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="busy" @click="reload">刷新</NButton>
        <NButton size="small" :loading="busy" @click="scan">重建索引</NButton>
        <NButton size="small" type="primary" @click="showForm = !showForm">
          {{ showForm ? '收起' : '新建笔记' }}
        </NButton>
      </div>
    </header>

    <div class="mem-stats">
      <div v-for="s in statCards" :key="s.label" class="mem-stat">
        <span class="mem-stat-v">{{ s.value }}</span>
        <span class="mem-stat-l">{{ s.label }}</span>
      </div>
      <div class="mem-stat mem-stat-grow">
        <span class="mem-stat-l">最后扫描</span>
        <span class="mem-stat-v mem-mono">{{ fmtTime(stats.lastScanAt) }}</span>
      </div>
    </div>

    <section v-if="showForm" class="bz-card mem-form">
      <div class="mem-form-grid">
        <NInput v-model:value="form.path" size="small" placeholder="路径（可空，如 项目/周报.md）" />
        <NInput v-model:value="form.title" size="small" placeholder="标题（可空，取正文首行）" />
      </div>
      <NInput v-model:value="form.tags" size="small" placeholder="标签，逗号分隔（可空）" />
      <NInput
        v-model:value="form.content"
        type="textarea"
        size="small"
        placeholder="正文，支持 [[双向链接]] 与 #标签"
        :autosize="{ minRows: 5, maxRows: 14 }"
      />
      <div class="mem-form-foot">
        <span class="mem-hint">保存后立刻落盘成 .md，并重新解析链接与标签。</span>
        <NButton size="small" type="primary" :loading="busy" @click="saveNote">保存</NButton>
      </div>
    </section>

    <section class="bz-card mem-main">
      <div class="mem-tabs">
        <button :class="['mem-tab', { on: tab === 'graph' }]" @click="switchTab('graph')">星图</button>
        <button :class="['mem-tab', { on: tab === 'notes' }]" @click="switchTab('notes')">笔记</button>
        <span class="mem-tabs-gap"></span>
        <NInput v-model:value="q" size="small" placeholder="搜索（回车）" clearable style="max-width: 220px"
                @keyup.enter="searchNotes" @clear="searchNotes" />
        <button class="mem-mini" :class="{ on: autoOn }" @click="toggleAuto">自动边{{ autoOn ? '开' : '关' }}</button>
        <button class="mem-mini" :class="{ on: showTags }" @click="showTags = !showTags">标签节点</button>
        <button class="mem-mini" @click="autolink">重新连边</button>
      </div>

      <!-- 星图 -->
      <div v-if="tab === 'graph'" class="mem-graph">
        <div class="mem-graph-wrap">
          <canvas ref="canvasRef"
                  @pointerdown="onPointerDown"
                  @pointermove="onPointerMove"
                  @pointerup="onPointerUp"
                  @pointercancel="onPointerUp"></canvas>
          <div v-if="graphLoading" class="mem-graph-hint">正在算星图…</div>
          <div v-else-if="!graphMeta.shown" class="mem-graph-hint">
            <div class="bz-empty-title">还没有笔记</div>
            <div class="bz-empty-desc">点右上角「新建笔记」，或在 vault/ 里放几个 .md 再点「重建索引」。</div>
          </div>
        </div>
        <div class="mem-legend">
          <span class="mem-legend-item"><i class="mem-dot note"></i>笔记</span>
          <span class="mem-legend-item"><i class="mem-dot tag"></i>标签</span>
          <span class="mem-legend-item"><i class="mem-dot missing"></i>悬空链接</span>
          <span class="mem-legend-item"><i class="mem-line solid"></i>显式链接</span>
          <span class="mem-legend-item"><i class="mem-line dashed"></i>自动相关</span>
          <span class="mem-legend-meta">
            {{ graphMeta.noteCount }} 篇 · 显式 {{ graphMeta.explicitEdges }} · 自动 {{ graphMeta.autoEdges }}
            · 孤立 {{ graphMeta.orphans }}
          </span>
        </div>
      </div>

      <!-- 笔记 -->
      <div v-else class="mem-notes">
        <aside class="mem-list">
          <div class="mem-list-head">
            <span v-if="activeTag" class="mem-filter">
              #{{ activeTag }}
              <button class="mem-filter-x" @click="openTag(activeTag)">×</button>
            </span>
            <span class="mem-list-count">{{ loading ? '读取中…' : total + ' 篇' }}</span>
          </div>
          <div class="mem-list-body">
            <button v-for="n in notes" :key="n.key"
                    :class="['mem-item', { on: detail && detail.key === n.key }]"
                    @click="openNote(n.key)">
              <span class="mem-item-title">{{ n.title || n.key }}</span>
              <span class="mem-item-path mem-mono">{{ n.path }}</span>
              <span v-if="n.tags && n.tags.length" class="mem-item-tags">
                <button v-for="t in n.tags" :key="t" class="mem-tag" @click.stop="openTag(t)">#{{ t }}</button>
              </span>
              <span class="mem-item-ex">{{ n.excerpt || '（空）' }}</span>
            </button>
            <div v-if="!loading && !notes.length" class="mem-list-empty">
              {{ q || activeTag ? '没有匹配的笔记' : '还没有笔记' }}
            </div>
          </div>
          <div v-if="tags.length" class="mem-cloud">
            <button v-for="t in tags.slice(0, 18)" :key="t.tag"
                    :class="['mem-tag', { on: activeTag === t.tag }]"
                    @click="openTag(t.tag)">#{{ t.tag }}<b>{{ t.count }}</b></button>
          </div>
        </aside>

        <div class="mem-detail">
          <div v-if="detailLoading" class="mem-detail-empty">读取中…</div>
          <div v-else-if="!detail" class="mem-detail-empty">
            <div class="bz-empty-title">选一篇笔记</div>
            <div class="bz-empty-desc">左侧点标题查看正文、出链与反向链接；也可以在星图里点节点。</div>
          </div>
          <template v-else>
            <div class="mem-detail-head">
              <div>
                <div class="mem-detail-title">{{ detail.title }}</div>
                <div class="mem-detail-meta mem-mono">
                  {{ detail.path }} · {{ detail.tokens }} tokens · 更新于 {{ fmtTime(detail.updatedAt) }}
                </div>
              </div>
              <NButton size="tiny" @click="removeNote">删除</NButton>
            </div>
            <div v-if="detail.tags && detail.tags.length" class="mem-detail-tags">
              <button v-for="t in detail.tags" :key="t" class="mem-tag" @click="openTag(t)">#{{ t }}</button>
            </div>

            <pre class="mem-content">{{ detail.content }}</pre>

            <div class="mem-rel">
              <div class="mem-rel-col">
                <div class="mem-rel-title">出链 {{ outlinks.length }}</div>
                <button v-for="l in outlinks" :key="l.target + (l.anchor || '')"
                        :class="['mem-rel-item', 'mem-mono', { missing: l.missing }]"
                        @click="l.noteKey && openNote(l.noteKey)">
                  → {{ l.target }}<span v-if="l.missing">（未创建）</span>
                </button>
                <div v-if="!outlinks.length" class="mem-rel-none">没有指向别的笔记</div>
              </div>
              <div class="mem-rel-col">
                <div class="mem-rel-title">反向链接 {{ backlinks.length }}</div>
                <button v-for="b in backlinks" :key="b.noteKey" class="mem-rel-item"
                        @click="openNote(b.noteKey)">
                  ← {{ b.title }}<span v-if="b.alias">（{{ b.alias }}）</span>
                </button>
                <div v-if="!backlinks.length" class="mem-rel-none">还没有笔记链接到它</div>
              </div>
            </div>
          </template>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.mem-page { display: flex; flex-direction: column; }
.mem-stats {
  display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)) 1.4fr;
  gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px;
}
.mem-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.mem-stat-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; }
.mem-stat-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.mem-stat-grow { flex-direction: row; align-items: center; justify-content: space-between; gap: 10px; }

.mem-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.mem-form-grid { display: grid; grid-template-columns: 1.2fr 1fr; gap: 10px; }
.mem-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.mem-hint { font-size: 11px; color: var(--fg-3); }

.mem-main { padding: 0; overflow: hidden; display: flex; flex-direction: column; min-height: 0; }
.mem-tabs {
  display: flex; align-items: center; gap: 8px; padding: 10px 14px;
  border-bottom: 1px solid var(--line); flex-wrap: wrap;
}
.mem-tabs-gap { flex: 1; }
.mem-tab {
  height: 30px; padding: 0 14px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-2); font-family: inherit; font-size: 12px; cursor: pointer;
}
.mem-tab.on { background: var(--accent-soft); border-color: var(--accent); color: var(--fg); }
.mem-mini {
  height: 30px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer;
}
.mem-mini:hover { border-color: var(--accent); color: var(--fg); }
.mem-mini.on { color: var(--accent); border-color: var(--accent); }

.mem-graph { display: flex; flex-direction: column; min-height: 0; }
.mem-graph-wrap { position: relative; height: calc(100vh - 330px); min-height: 380px; }
.mem-graph-wrap canvas { display: block; cursor: grab; touch-action: none; }
.mem-graph-wrap canvas:active { cursor: grabbing; }
.mem-graph-hint {
  position: absolute; inset: 0; display: flex; flex-direction: column;
  align-items: center; justify-content: center; gap: 6px; font-size: 12px; color: var(--fg-3);
  pointer-events: none;
}
.mem-legend {
  display: flex; align-items: center; gap: 14px; flex-wrap: wrap;
  padding: 8px 14px; border-top: 1px solid var(--line); font-size: 11px; color: var(--fg-3);
}
.mem-legend-item { display: inline-flex; align-items: center; gap: 5px; }
.mem-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); }
.mem-dot.tag { background: var(--warn); }
.mem-dot.missing { background: var(--err); opacity: .6; }
.mem-line { display: inline-block; width: 18px; height: 0; border-top: 1px solid var(--fg-3); }
.mem-line.dashed { border-top-style: dashed; }
.mem-legend-meta { margin-left: auto; font-family: var(--mono); }

.mem-notes { display: flex; min-height: 0; height: calc(100vh - 330px); }
.mem-list {
  width: 320px; min-width: 320px; border-right: 1px solid var(--line);
  display: flex; flex-direction: column; min-height: 0;
}
.mem-list-head {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 8px 12px; border-bottom: 1px solid var(--line); min-height: 34px;
}
.mem-list-count { font-size: 11px; color: var(--fg-3); margin-left: auto; }
.mem-filter {
  display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--accent);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.mem-filter-x { border: 0; background: transparent; color: var(--fg-3); cursor: pointer; font-size: 13px; }
.mem-list-body { flex: 1; overflow-y: auto; min-height: 0; }
.mem-item {
  display: flex; flex-direction: column; gap: 3px; width: 100%; text-align: left;
  padding: 9px 12px; border: 0; border-bottom: 1px solid var(--line);
  background: transparent; color: inherit; font-family: inherit; cursor: pointer;
}
.mem-item:hover { background: var(--surface-2); }
.mem-item.on { background: var(--accent-soft); box-shadow: inset 2px 0 0 var(--accent); }
.mem-item-title { font-size: 13px; font-weight: 600; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mem-item-path { font-size: 10px; color: var(--fg-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mem-item-ex { font-size: 11px; color: var(--fg-2); line-height: 1.5; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.mem-item-tags, .mem-detail-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.mem-tag {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-3); font-family: var(--mono); font-size: 10px; cursor: pointer;
}
.mem-tag:hover, .mem-tag.on { border-color: var(--accent); color: var(--accent); }
.mem-tag b { font-weight: 500; opacity: .7; }
.mem-cloud {
  display: flex; flex-wrap: wrap; gap: 5px; padding: 10px 12px;
  border-top: 1px solid var(--line); max-height: 132px; overflow-y: auto;
}
.mem-list-empty { padding: 22px 14px; font-size: 12px; color: var(--fg-3); }

.mem-detail { flex: 1; min-width: 0; overflow-y: auto; padding: 16px 18px; }
.mem-detail-empty { padding: 60px 20px; text-align: center; }
.mem-detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.mem-detail-title { font-family: Georgia, "Songti SC", serif; font-size: 18px; color: var(--fg); line-height: 1.4; }
.mem-detail-meta { margin-top: 4px; font-size: 10px; color: var(--fg-3); }
.mem-detail-tags { margin-top: 10px; }
.mem-content {
  margin: 14px 0 0; padding: 12px; border: 1px solid var(--line); border-radius: 2px;
  background: var(--surface-2); color: var(--fg-2);
  white-space: pre-wrap; word-break: break-word;
  font-family: var(--mono); font-size: 12px; line-height: 1.7;
}
.mem-rel { margin-top: 14px; display: flex; gap: 24px; flex-wrap: wrap; }
.mem-rel-col { min-width: 220px; flex: 1; }
.mem-rel-title { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; margin-bottom: 6px; }
.mem-rel-item {
  display: block; width: 100%; text-align: left; padding: 5px 8px; margin-bottom: 4px;
  border: 1px solid var(--line); border-radius: 2px; background: transparent;
  color: var(--fg-2); font-size: 11px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.mem-rel-item:hover { border-color: var(--accent); color: var(--fg); }
.mem-rel-item.missing { color: var(--fg-3); border-style: dashed; cursor: default; }
.mem-rel-item.missing span { opacity: .7; }
.mem-rel-item.missing:hover { border-color: var(--line); color: var(--fg-3); }
.mem-rel-none { font-size: 11px; color: var(--fg-3); }
.mem-mono { font-family: var(--mono); font-variant-numeric: tabular-nums; }

@media (max-width: 900px) {
  .mem-stats { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .mem-notes { flex-direction: column; height: auto; }
  .mem-list { width: 100%; min-width: 0; border-right: 0; border-bottom: 1px solid var(--line); max-height: 320px; }
  .mem-graph-wrap { height: 60vh; }
}
</style>
