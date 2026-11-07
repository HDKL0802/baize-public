<!-- 独立知识库（笔记型）：Obsidian 兼容的 .md 正本 + 双链 + 知识星图。
     数据源全部是既有后端 /api/agent/notes*（notebook 包），这里只做界面：
     目录树、预览/编辑、反链、搜索、标签、星图、从本机导入 .md。
     星图用共用的 NoteGraph 组件（与「记忆星图」同一套画法）。 -->
<script setup>
import { ref, computed, onMounted, nextTick } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime } from '../api.js';
import NoteGraph from '../components/NoteGraph.vue';

const notes = ref([]);
const tags = ref([]);
const stats = ref({});
const total = ref(0);
const loading = ref(true);
const busy = ref(false);
const q = ref('');
const activeTag = ref('');
const tab = ref('notes');

const detail = ref(null);
const detailLoading = ref(false);
const outlinks = ref([]);
const backlinks = ref([]);

const showEditor = ref(false);
const editor = ref({ key: '', path: '', content: '' });
const viewMode = ref('preview');
const collapsedDirs = ref([]);
const searchHits = ref(null);

const graphRef = ref(null);
const importOpen = ref(false);
const importPath = ref('');
const importBusy = ref(false);
const importMsg = ref('');

const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;
function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

/* ---------- 列表 / 标签 / 搜索 ---------- */

async function loadList() {
  loading.value = true;
  searchHits.value = null;
  const params = new URLSearchParams({ limit: '300' });
  if (q.value.trim()) params.set('q', q.value.trim());
  if (activeTag.value) params.set('tag', activeTag.value);
  const r = await API.get('/api/agent/notes?' + params.toString());
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取笔记失败', 'err'); return; }
  const d = r.data || {};
  notes.value = d.notes || [];
  tags.value = d.tags || [];
  stats.value = d.stats || {};
  total.value = d.total || notes.value.length;
}

async function doSearch() {
  const kw = q.value.trim();
  if (!kw) { loadList(); return; }
  loading.value = true;
  const r = await API.get('/api/agent/notes/search?q=' + encodeURIComponent(kw) + '&limit=30');
  loading.value = false;
  if (!r.ok) { toast(r.error || '搜索失败', 'err'); return; }
  searchHits.value = (r.data && r.data.hits) || [];
}

function clearSearch() {
  q.value = '';
  searchHits.value = null;
  loadList();
}

function openTag(t) {
  activeTag.value = activeTag.value === t ? '' : t;
  q.value = '';
  loadList();
}

function onGraphTag(t) {
  tab.value = 'notes';
  openTag(t);
}

const groups = computed(() => {
  const map = new Map();
  for (const n of notes.value) {
    const p = n.path || '';
    const i = p.lastIndexOf('/');
    const dir = i >= 0 ? p.slice(0, i) : '';
    if (!map.has(dir)) map.set(dir, []);
    map.get(dir).push(n);
  }
  return Array.from(map.entries())
    .sort((a, b) => a[0].localeCompare(b[0], 'zh'))
    .map(([dir, list]) => ({ dir, list }));
});

function toggleDir(dir) {
  const i = collapsedDirs.value.indexOf(dir);
  if (i >= 0) collapsedDirs.value.splice(i, 1);
  else collapsedDirs.value.push(dir);
}

/* ---------- 详情 / 打开 ---------- */

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
  searchHits.value = null;
  document.getElementById('kbNote')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  await loadDetail(key);
  viewMode.value = 'preview';
}

/* 双链/标签跳转：笔记标题、文件名、路径都能对上 */
const targetIndex = computed(() => {
  const idx = new Map();
  const add = (k, v) => { const s = (k || '').trim().toLowerCase(); if (s && !idx.has(s)) idx.set(s, v); };
  for (const n of notes.value) {
    add(n.key, n.key);
    add(n.title, n.key);
    add(baseName(n.path), n.key);
    (n.aliases || []).forEach(a => add(a, n.key));
  }
  return idx;
});

function baseName(p) {
  const s = (p || '').split('/').pop();
  return (s || '').replace(/\.md$/i, '');
}

function openTarget(target) {
  const t = (target || '').split('#')[0].trim();
  if (!t) return;
  const hit = targetIndex.value.get(t.toLowerCase());
  if (hit) openNote(hit);
  else toast('「' + t + '」还没创建；这条链接会保持悬空，建好后自动连上', 'warn');
}

function onContentClick(e) {
  const wl = e.target.closest('[data-wl]');
  if (wl) { openTarget(wl.getAttribute('data-wl')); return; }
  const tg = e.target.closest('[data-tag]');
  if (tg) openTag(tg.getAttribute('data-tag'));
}

/* ---------- 极简 Markdown 预览（只渲染常见语法，全部先转义） ---------- */

function escHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function inlineMd(s) {
  return s
    .replace(/\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g,
      (m, t, label) => '<a class="kb-wl" data-wl="' + t + '">' + (label || t) + '</a>')
    .replace(/`([^`]+)`/g, '<code class="kb-inline">$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|\s)#([\p{L}\p{N}_-]+)/gu, '$1<span class="kb-htag" data-tag="$2">#$2</span>');
}

function renderMd(text) {
  const lines = escHtml(text || '').split('\n');
  let out = '';
  let inCode = false;
  const inline = inlineMd; // 简写，下面循环里用
  for (const line of lines) {
    if (line.startsWith('```')) {
      if (inCode) { out += '</code></pre>'; inCode = false; }
      else { out += '<pre class="kb-code"><code>'; inCode = true; }
      continue;
    }
    if (inCode) { out += line + '\n'; continue; }
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      const lv = Math.min(6, h[1].length);
      out += '<h' + lv + ' class="kb-h kb-h' + lv + '">' + inline(h[2]) + '</h' + lv + '>';
      continue;
    }
    if (!line.trim()) { out += '<div class="kb-gap"></div>'; continue; }
    const li = line.match(/^[-*]\s+(.*)$/);
    if (li) { out += '<div class="kb-li">• ' + inline(li[1]) + '</div>'; continue; }
    const ol = line.match(/^\d+\.\s+(.*)$/);
    if (ol) { out += '<div class="kb-li kb-li-num">' + inline(ol[1]) + '</div>'; continue; }
    out += '<p class="kb-p">' + inline(line) + '</p>';
  }
  if (inCode) out += '</code></pre>';
  return out;
}

/* ---------- 编辑 / 新建 / 删除 ---------- */

function newNote() {
  tab.value = 'notes';
  detail.value = null;
  editor.value = { key: '', path: '', content: '# 新笔记\n\n' };
  showEditor.value = true;
  nextTick(() => document.getElementById('kbEditor')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
}

function editNote() {
  if (!detail.value) return;
  editor.value = { key: detail.value.key, path: detail.value.path, content: detail.value.content };
  showEditor.value = true;
  nextTick(() => document.getElementById('kbEditor')?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }));
}

async function saveEditor() {
  const e = editor.value;
  if (!e.content.trim()) { toast('正文不能为空', 'warn'); return; }
  busy.value = true;
  // 路径改了就走 rename；路径留空则由后端用正文首行标题命名
  const targetPath = e.path.trim() || (detail.value ? detail.value.path : '');
  if (e.key && targetPath && targetPath !== detail.value?.path) {
    const rr = await API.post('/api/agent/notes/rename', { key: e.key, path: targetPath });
    if (!rr.ok) { busy.value = false; toast(rr.error || '重命名失败', 'err'); return; }
  }
  const r = await API.post('/api/agent/notes', { path: targetPath, content: e.content });
  busy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  toast('已保存', 'ok');
  showEditor.value = false;
  await loadList();
  if (r.data?.note?.key) await openNote(r.data.note.key);
}

async function removeNote() {
  if (!detail.value) return;
  if (!window.confirm('删除这篇笔记？磁盘上的 .md 文件会一并删掉。')) return;
  const r = await API.post('/api/agent/notes/forget', { key: detail.value.key });
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  toast('已删除', 'ok');
  detail.value = null;
  outlinks.value = [];
  backlinks.value = [];
  await loadList();
  graphRef.value?.reload();
}

/* ---------- 维护 ---------- */

async function scan() {
  busy.value = true;
  const r = await API.post('/api/agent/notes/scan', {});
  busy.value = false;
  if (!r.ok) { toast(r.error || '重建索引失败', 'err'); return; }
  const s = (r.data && r.data.scan) || {};
  toast('扫描完成：索引 ' + (s.indexed || 0) + '，移除 ' + (s.removed || 0), 'ok');
  await loadList();
  graphRef.value?.reload();
}

async function autolink() {
  busy.value = true;
  const r = await API.post('/api/agent/notes/autolink', {});
  busy.value = false;
  if (!r.ok) { toast(r.error || '自动连边失败', 'err'); return; }
  toast('已按共引关系补边：' + (r.data?.edges || 0) + ' 条', 'ok');
  graphRef.value?.reload();
}

async function resolveLinks() {
  busy.value = true;
  const r = await API.post('/api/agent/notes/resolve', {});
  busy.value = false;
  if (!r.ok) { toast(r.error || '解析失败', 'err'); return; }
  toast('悬空链接重新解析：' + (r.data?.resolved || 0) + ' 条连上', 'ok');
  await loadList();
  graphRef.value?.reload();
}

/* ---------- 从本机导入 Obsidian 库（复用桌面端受权限约束的文件接口） ---------- */

function b64ToBytes(b64) {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

async function doImport() {
  const dir = importPath.value.trim();
  if (!dir) { importMsg.value = '先填一个本机目录，例如 D:\\Notes'; return; }
  importBusy.value = true;
  importMsg.value = '正在按文件名找 .md …';
  const sr = await API.localGet('/files/search?path=' + encodeURIComponent(dir) + '&q=.md');
  if (!sr.ok) {
    importBusy.value = false;
    importMsg.value = (sr.error || '读不到这个目录') +
      '（本机文件接口受「设置 → 桌面控制」的允许范围限制）';
    return;
  }
  const items = (sr.results || []).filter(x => !x.isDir && /\.md$/i.test(x.name));
  if (!items.length) {
    importBusy.value = false;
    importMsg.value = '这个目录（含子目录）里没找到 .md 文件';
    return;
  }
  const files = [];
  let readErr = 0, tooBig = 0;
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    importMsg.value = '读取 ' + (i + 1) + '/' + items.length + '：' + it.name;
    if ((it.size || 0) > 4 * 1024 * 1024) { tooBig++; continue; }
    const fr = await API.localGet('/files/read?path=' + encodeURIComponent(it.path));
    if (!fr.ok) { readErr++; continue; }
    files.push({ path: String(it.rel || it.name).replace(/\\/g, '/'), content: new TextDecoder('utf-8').decode(b64ToBytes(fr.dataBase64)) });
  }
  if (!files.length) {
    importBusy.value = false;
    importMsg.value = '没有可导入的文本笔记（读取失败 ' + readErr + '，过大跳过 ' + tooBig + '）';
    return;
  }
  importMsg.value = '写入笔记库：' + files.length + ' 篇…';
  const r = await API.post('/api/agent/notes/import', { files });
  importBusy.value = false;
  if (!r.ok) { importMsg.value = r.error || '导入失败'; return; }
  const d = r.data || {};
  importMsg.value = '导入完成：新增 ' + (d.imported || 0) + '，跳过 ' + (d.skipped || 0) +
    (tooBig ? '，过大跳过 ' + tooBig : '') +
    (readErr ? '，读取失败 ' + readErr : '') +
    (sr.truncated ? '（本机搜索有上限，只导入了找到的前 ' + items.length + ' 个）' : '');
  toast('已导入 ' + (d.imported || 0) + ' 篇', 'ok');
  await loadList();
  graphRef.value?.reload();
}

const statCards = computed(() => [
  { label: '笔记', value: stats.value.notes ?? notes.value.length },
  { label: '已解析链接', value: stats.value.links ?? 0 },
  { label: '悬空链接', value: stats.value.missing ?? 0 },
  { label: '标签', value: stats.value.tags ?? tags.value.length },
  { label: '孤立笔记', value: stats.value.orphans ?? 0 },
]);

onMounted(loadList);
</script>

<template>
  <div class="bz-page kb-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">独立知识库</h1>
        <p class="bz-sub">
          笔记型知识库：正本是磁盘上的 .md（vault/，Obsidian 可直接打开），双链、反链与星图由索引实时算出。
        </p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="busy" @click="loadList">刷新</NButton>
        <NButton size="small" :loading="busy" @click="scan">重建索引</NButton>
        <NButton size="small" @click="importOpen = !importOpen">从本机导入</NButton>
        <NButton size="small" type="primary" @click="newNote">新建笔记</NButton>
      </div>
    </header>

    <div class="kb-stats">
      <div v-for="s in statCards" :key="s.label" class="kb-stat">
        <span class="kb-stat-v">{{ s.value }}</span>
        <span class="kb-stat-l">{{ s.label }}</span>
      </div>
      <div class="kb-stat kb-stat-grow">
        <span class="kb-stat-l">最后扫描</span>
        <span class="kb-stat-v kb-mono">{{ fmtTime(stats.lastScanAt) }}</span>
      </div>
    </div>

    <section v-if="importOpen" class="bz-card kb-import">
      <div class="kb-import-row">
        <NInput v-model:value="importPath" size="small"
                placeholder="本机目录，例如 D:\Notes（会递归找 .md，最多 300 个 / 8 层）" />
        <NButton size="small" type="primary" :loading="importBusy" @click="doImport">开始导入</NButton>
        <NButton size="small" :disabled="importBusy" @click="importOpen = false">收起</NButton>
      </div>
      <div class="kb-import-note">
        走桌面端的本机文件接口，目录必须落在「设置 → 桌面控制」允许的范围内；只读取
        <b>.md</b>，不读其他文件。导入只写笔记库（vault/），不动你原来的目录。
      </div>
      <div v-if="importMsg" class="kb-import-msg kb-mono">{{ importMsg }}</div>
    </section>

    <section v-if="showEditor" id="kbEditor" class="bz-card kb-editor">
      <div class="kb-editor-grid">
        <NInput v-model:value="editor.path" size="small"
                placeholder="路径（可空，用正文首行命名；如 项目/周报.md）" />
      </div>
      <NInput v-model:value="editor.content" type="textarea" size="small"
              placeholder="正文：支持 [[双向链接]]、#标签、Markdown"
              :autosize="{ minRows: 8, maxRows: 22 }" />
      <div class="kb-editor-foot">
        <span class="kb-hint">
          {{ editor.key ? '改路径 = 重命名/移动；正文按原文覆盖。' : '保存后立刻落盘成 .md，并重新解析链接与标签。' }}
        </span>
        <div class="kb-editor-actions">
          <NButton size="small" :disabled="busy" @click="showEditor = false">取消</NButton>
          <NButton size="small" type="primary" :loading="busy" @click="saveEditor">保存</NButton>
        </div>
      </div>
    </section>

    <section class="bz-card kb-main">
      <div class="kb-tabs">
        <button :class="['kb-tab', { on: tab === 'notes' }]" @click="tab = 'notes'">笔记</button>
        <button :class="['kb-tab', { on: tab === 'graph' }]" @click="tab = 'graph'">知识星图</button>
        <span class="kb-tabs-gap"></span>
        <NInput v-model:value="q" size="small" placeholder="搜索标题/正文（回车）" clearable style="max-width: 220px"
                @keyup.enter="doSearch" @clear="clearSearch" />
        <NButton size="small" @click="doSearch">搜索</NButton>
        <NButton size="small" :loading="busy" @click="autolink">自动连边</NButton>
        <NButton size="small" :loading="busy" @click="resolveLinks">重解悬空</NButton>
      </div>

      <!-- 星图 -->
      <div v-if="tab === 'graph'" class="kb-graph">
        <NoteGraph ref="graphRef" :selected-key="detail ? detail.key : ''"
                   @open-note="openNote"
                   @open-tag="onGraphTag" />
      </div>

      <!-- 笔记 -->
      <div v-else class="kb-notes">
        <aside class="kb-side">
          <div class="kb-side-head">
            <span v-if="activeTag" class="kb-filter">
              #{{ activeTag }}
              <button class="kb-filter-x" @click="openTag(activeTag)">×</button>
            </span>
            <span v-if="searchHits" class="kb-filter">搜索：{{ q }}</span>
            <span class="kb-side-count">{{ loading ? '读取中…' : (searchHits ? searchHits.length + ' 条命中' : total + ' 篇') }}</span>
          </div>

          <div class="kb-side-body">
            <template v-if="searchHits">
              <button v-for="h in searchHits" :key="h.key" class="kb-hit" @click="openNote(h.key)">
                <span class="kb-hit-title">{{ h.title || h.key }}</span>
                <span class="kb-hit-path kb-mono">{{ h.path }}</span>
                <span class="kb-hit-ex">{{ h.excerpt || '（空）' }}</span>
              </button>
              <div v-if="!searchHits.length" class="kb-side-empty">没有匹配的笔记</div>
            </template>

            <template v-else>
              <div v-for="g in groups" :key="g.dir || '(root)'" class="kb-group">
                <button v-if="g.dir" class="kb-group-head" @click="toggleDir(g.dir)">
                  <span class="kb-caret">{{ collapsedDirs.includes(g.dir) ? '▸' : '▾' }}</span>
                  <span class="kb-group-name">{{ g.dir }}</span>
                  <span class="kb-group-count">{{ g.list.length }}</span>
                </button>
                <div v-show="!g.dir || !collapsedDirs.includes(g.dir)">
                  <button v-for="n in g.list" :key="n.key"
                          :class="['kb-item', { on: detail && detail.key === n.key }]"
                          @click="openNote(n.key)">
                    <span class="kb-item-title">{{ n.title || n.key }}</span>
                    <span class="kb-item-path kb-mono">{{ n.path }}</span>
                    <span v-if="n.tags && n.tags.length" class="kb-item-tags">
                      <span v-for="t in n.tags" :key="t" class="kb-tag" @click.stop="openTag(t)">#{{ t }}</span>
                    </span>
                    <span class="kb-item-ex">{{ n.excerpt || '（空）' }}</span>
                  </button>
                </div>
              </div>
              <div v-if="!loading && !notes.length" class="kb-side-empty">
                {{ q || activeTag ? '没有匹配的笔记' : '还没有笔记：新建一篇，或「从本机导入」一个 .md 目录' }}
              </div>
            </template>
          </div>

          <div v-if="tags.length" class="kb-cloud">
            <button v-for="t in tags.slice(0, 20)" :key="t.tag"
                    :class="['kb-tag', { on: activeTag === t.tag }]"
                    @click="openTag(t.tag)">#{{ t.tag }}<b>{{ t.count }}</b></button>
          </div>
        </aside>

        <div id="kbNote" class="kb-detail">
          <div v-if="detailLoading" class="kb-detail-empty">读取中…</div>
          <div v-else-if="!detail" class="kb-detail-empty">
            <div class="bz-empty-title">选一篇笔记</div>
            <div class="bz-empty-desc">左侧点标题查看正文、双链与反向链接；也可以在星图里点节点。</div>
          </div>
          <template v-else>
            <div class="kb-detail-head">
              <div>
                <div class="kb-detail-title">{{ detail.title }}</div>
                <div class="kb-detail-meta kb-mono">
                  {{ detail.path }} · {{ detail.tokens }} tokens · 更新于 {{ fmtTime(detail.updatedAt) }}
                </div>
              </div>
              <div class="kb-detail-actions">
                <button :class="['kb-mini', { on: viewMode === 'preview' }]" @click="viewMode = 'preview'">预览</button>
                <button :class="['kb-mini', { on: viewMode === 'source' }]" @click="viewMode = 'source'">源码</button>
                <NButton size="tiny" type="primary" @click="editNote">编辑</NButton>
                <NButton size="tiny" @click="removeNote">删除</NButton>
              </div>
            </div>

            <div v-if="detail.tags && detail.tags.length" class="kb-detail-tags">
              <button v-for="t in detail.tags" :key="t" class="kb-tag" @click="openTag(t)">#{{ t }}</button>
            </div>

            <div v-if="viewMode === 'preview'" class="kb-content" v-html="renderMd(detail.content)" @click="onContentClick"></div>
            <pre v-else class="kb-content kb-source">{{ detail.content }}</pre>

            <div class="kb-rel">
              <div class="kb-rel-col">
                <div class="kb-rel-title">出链 {{ outlinks.length }}</div>
                <button v-for="l in outlinks" :key="l.target + (l.anchor || '')"
                        :class="['kb-rel-item', 'kb-mono', { missing: l.missing }]"
                        @click="l.noteKey && openNote(l.noteKey)">
                  → {{ l.target }}<span v-if="l.missing">（未创建）</span>
                </button>
                <div v-if="!outlinks.length" class="kb-rel-none">没有指向别的笔记</div>
              </div>
              <div class="kb-rel-col">
                <div class="kb-rel-title">反向链接 {{ backlinks.length }}</div>
                <button v-for="b in backlinks" :key="b.noteKey" class="kb-rel-item" @click="openNote(b.noteKey)">
                  ← {{ b.title }}<span v-if="b.alias">（{{ b.alias }}）</span>
                </button>
                <div v-if="!backlinks.length" class="kb-rel-none">还没有笔记链接到它</div>
              </div>
            </div>

            <div class="kb-vault-note">
              vault/ 整个目录就是一个 Obsidian 库；笔记库按登录身份分区（用户/用户组各自一份），
              界面不挂载任意外部目录。
            </div>
          </template>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.kb-page { display: flex; flex-direction: column; }
.kb-stats {
  display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)) 1.4fr;
  gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px;
}
.kb-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.kb-stat-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; }
.kb-stat-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.kb-stat-grow { flex-direction: row; align-items: center; justify-content: space-between; gap: 10px; }

.kb-import, .kb-editor { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.kb-import-row { display: flex; gap: 10px; align-items: center; }
.kb-import-row .n-input { flex: 1; }
.kb-import-note { font-size: 11px; color: var(--fg-3); line-height: 1.7; }
.kb-import-msg { font-size: 11px; color: var(--fg-2); }
.kb-editor-grid { display: grid; grid-template-columns: 1fr; gap: 10px; }
.kb-editor-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.kb-editor-actions { display: flex; gap: 8px; }
.kb-hint { font-size: 11px; color: var(--fg-3); }

.kb-main { padding: 0; overflow: hidden; display: flex; flex-direction: column; min-height: 0; }
.kb-tabs { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border-bottom: 1px solid var(--line); flex-wrap: wrap; }
.kb-tabs-gap { flex: 1; }
.kb-tab {
  height: 30px; padding: 0 14px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-2); font-family: inherit; font-size: 12px; cursor: pointer;
}
.kb-tab.on { background: var(--accent-soft); border-color: var(--accent); color: var(--fg); }

.kb-graph { display: flex; flex-direction: column; min-height: 0; }

.kb-notes { display: flex; min-height: 0; height: calc(100vh - 330px); }
.kb-side { width: 300px; min-width: 300px; border-right: 1px solid var(--line); display: flex; flex-direction: column; min-height: 0; }
.kb-side-head { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-bottom: 1px solid var(--line); min-height: 34px; }
.kb-side-count { font-size: 11px; color: var(--fg-3); margin-left: auto; white-space: nowrap; }
.kb-filter {
  display: inline-flex; align-items: center; gap: 4px; font-size: 11px; color: var(--accent);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 160px;
}
.kb-filter-x { border: 0; background: transparent; color: var(--fg-3); cursor: pointer; font-size: 13px; }
.kb-side-body { flex: 1; overflow-y: auto; min-height: 0; }
.kb-group-head {
  display: flex; align-items: center; gap: 6px; width: 100%; text-align: left;
  padding: 6px 10px; border: 0; border-bottom: 1px solid var(--line);
  background: var(--surface-2); color: var(--fg-2); font-family: var(--mono); font-size: 11px; cursor: pointer;
}
.kb-caret { width: 12px; color: var(--fg-3); }
.kb-group-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.kb-group-count { color: var(--fg-3); }
.kb-item, .kb-hit {
  display: flex; flex-direction: column; gap: 3px; width: 100%; text-align: left;
  padding: 9px 12px; border: 0; border-bottom: 1px solid var(--line);
  background: transparent; color: inherit; font-family: inherit; cursor: pointer;
}
.kb-item:hover, .kb-hit:hover { background: var(--surface-2); }
.kb-item.on { background: var(--accent-soft); box-shadow: inset 2px 0 0 var(--accent); }
.kb-item-title, .kb-hit-title { font-size: 13px; font-weight: 600; color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.kb-item-path, .kb-hit-path { font-size: 10px; color: var(--fg-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.kb-item-ex, .kb-hit-ex {
  font-size: 11px; color: var(--fg-2); line-height: 1.5;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.kb-item-tags, .kb-detail-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.kb-tag {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-3); font-family: var(--mono); font-size: 10px; cursor: pointer;
}
.kb-tag:hover, .kb-tag.on { border-color: var(--accent); color: var(--accent); }
.kb-tag b { font-weight: 500; opacity: .7; }
.kb-cloud { display: flex; flex-wrap: wrap; gap: 5px; padding: 10px 12px; border-top: 1px solid var(--line); max-height: 132px; overflow-y: auto; }
.kb-side-empty { padding: 22px 14px; font-size: 12px; color: var(--fg-3); line-height: 1.7; }

.kb-detail { flex: 1; min-width: 0; overflow-y: auto; padding: 16px 18px; }
.kb-detail-empty { padding: 60px 20px; text-align: center; }
.kb-detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.kb-detail-title { font-family: Georgia, "Songti SC", serif; font-size: 18px; color: var(--fg); line-height: 1.4; }
.kb-detail-meta { margin-top: 4px; font-size: 10px; color: var(--fg-3); }
.kb-detail-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.kb-mini {
  height: 24px; padding: 0 8px; border: 1px solid var(--line); border-radius: 2px;
  background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer;
}
.kb-mini.on { color: var(--accent); border-color: var(--accent); }
.kb-detail-tags { margin-top: 10px; }

.kb-content {
  margin: 14px 0 0; padding: 14px 16px; border: 1px solid var(--line); border-radius: 2px;
  background: var(--surface-2); color: var(--fg-2);
  font-size: 13px; line-height: 1.75; word-break: break-word;
}
.kb-source { white-space: pre-wrap; font-family: var(--mono); font-size: 12px; line-height: 1.7; }
.kb-h { color: var(--fg); font-family: Georgia, "Songti SC", serif; margin: 12px 0 6px; font-weight: 600; }
.kb-h1 { font-size: 18px; } .kb-h2 { font-size: 16px; } .kb-h3 { font-size: 14px; }
.kb-h4, .kb-h5, .kb-h6 { font-size: 13px; }
.kb-p { margin: 0 0 6px; }
.kb-gap { height: 8px; }
.kb-li { margin: 1px 0 1px 8px; }
.kb-li-num { color: var(--fg-2); }
.kb-code {
  margin: 8px 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: 2px;
  background: var(--surface); color: var(--fg-2); overflow-x: auto;
  font-family: var(--mono); font-size: 12px; line-height: 1.6; white-space: pre;
}
.kb-inline { padding: 1px 4px; border-radius: 2px; background: var(--surface); font-family: var(--mono); font-size: 12px; }
.kb-wl { color: var(--accent); text-decoration: none; cursor: pointer; border-bottom: 1px dotted var(--accent); }
.kb-wl:hover { background: var(--accent-soft); }
.kb-htag { color: var(--warn); cursor: pointer; font-family: var(--mono); font-size: 12px; }

.kb-rel { margin-top: 14px; display: flex; gap: 24px; flex-wrap: wrap; }
.kb-rel-col { min-width: 220px; flex: 1; }
.kb-rel-title { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; margin-bottom: 6px; }
.kb-rel-item {
  display: block; width: 100%; text-align: left; padding: 5px 8px; margin-bottom: 4px;
  border: 1px solid var(--line); border-radius: 2px; background: transparent;
  color: var(--fg-2); font-size: 11px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.kb-rel-item:hover { border-color: var(--accent); color: var(--fg); }
.kb-rel-item.missing { color: var(--fg-3); border-style: dashed; cursor: default; }
.kb-rel-item.missing:hover { border-color: var(--line); color: var(--fg-3); }
.kb-rel-none { font-size: 11px; color: var(--fg-3); }
.kb-vault-note { margin-top: 18px; font-size: 11px; color: var(--fg-3); line-height: 1.7; }
.kb-mono { font-family: var(--mono); font-variant-numeric: tabular-nums; }

@media (max-width: 900px) {
  .kb-stats { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .kb-notes { flex-direction: column; height: auto; }
  .kb-side { width: 100%; min-width: 0; border-right: 0; border-bottom: 1px solid var(--line); max-height: 320px; }
}
</style>
