<!-- 电脑文件：走本机 Go 壳的 /api/local/files/*（列表 / 读取 / 目录内搜索）。
     权限不在这里判断：所有请求都先过 permGate，越界会被后端直接拒绝；
     界面只把 permTitle / scopes 原样展示，不假装能读范围外的目录。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API, fmtTime } from '../api.js';

const cur = ref('');            // 当前目录，空 = 此电脑（盘符清单）
const roots = ref([]);
const entries = ref([]);
const parent = ref('');
const truncated = ref(false);
const perm = ref(null);
const permTitle = ref('');
const scopes = ref([]);
const loading = ref(true);
const errMsg = ref('');
const hint = ref('');

const keyword = ref('');
const searching = ref(false);
const searchInfo = ref(null);
const searchResults = ref([]);

const preview = ref(null);
const previewBusy = ref(false);
const previewErr = ref('');

const TEXT_EXT = new Set(['txt', 'log', 'md', 'markdown', 'json', 'csv', 'tsv', 'yaml', 'yml', 'xml',
  'ini', 'conf', 'cfg', 'toml', 'go', 'js', 'mjs', 'ts', 'tsx', 'jsx', 'vue', 'css', 'html', 'htm',
  'sh', 'ps1', 'bat', 'py', 'rs', 'java', 'c', 'h', 'cpp', 'sql', 'env']);

function extOf(name) {
  const i = String(name || '').lastIndexOf('.');
  return i >= 0 ? name.slice(i + 1).toLowerCase() : '';
}

function isTextLike(f) {
  if (!f) return false;
  if ((f.mime || '').startsWith('text/')) return true;
  if (['application/json', 'text/csv'].includes(f.mime)) return true;
  return TEXT_EXT.has(extOf(f.name));
}

function sizeText(n) {
  if (n == null) return '—';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB';
}

const crumbs = computed(() => {
  const p = cur.value;
  if (!p) return [];
  const parts = p.split(/[\\/]+/).filter(Boolean);
  const out = [];
  let acc = '';
  parts.forEach((seg, i) => {
    acc = i === 0 ? seg + '\\' : acc.replace(/\\+$/, '') + '\\' + seg;
    out.push({ name: seg, path: acc });
  });
  return out;
});

const totalFiles = computed(() => entries.value.filter(e => !e.isDir).length);

function applyPerm(d) {
  if (d && d.perm != null) perm.value = d.perm;
  if (d && d.permTitle) permTitle.value = d.permTitle;
  if (d && Array.isArray(d.scopes)) scopes.value = d.scopes;
}

async function load(path) {
  loading.value = true;
  errMsg.value = '';
  hint.value = '';
  searchInfo.value = null;
  searchResults.value = [];
  keyword.value = '';
  const r = await API.localGet('/files/list?path=' + encodeURIComponent(path || ''));
  loading.value = false;
  applyPerm(r);
  if (!r || r.ok === false) {
    // 出错时保留上一次的列表位置，不把界面清空成"假目录"
    errMsg.value = (r && r.error) || '读不到这个目录';
    hint.value = (r && r.hint) || '';
    if (r && r.path) { cur.value = r.path; parent.value = r.parent || ''; }
    entries.value = [];
    roots.value = [];
    return;
  }
  cur.value = r.path || '';
  parent.value = r.parent || '';
  truncated.value = !!r.truncated;
  if (!cur.value) {
    roots.value = r.roots || [];
    entries.value = [];
  } else {
    roots.value = [];
    entries.value = (r.entries || []).map(e => ({
      ...e,
      path: cur.value.replace(/[\\/]+$/, '') + '\\' + e.name,
    }));
  }
}

function enter(e) {
  if (e.isDir) load(e.path);
  else openPreview(e);
}

function up() {
  if (!cur.value) return;
  load(parent.value || '');
}

function home() { load(''); }

async function runSearch() {
  const q = keyword.value.trim();
  if (!q) { searchInfo.value = null; searchResults.value = []; return; }
  if (!cur.value) { errMsg.value = '先进入一个目录再搜索（「此电脑」不递归）'; return; }
  searching.value = true;
  errMsg.value = '';
  const r = await API.localGet('/files/search?path=' + encodeURIComponent(cur.value) + '&q=' + encodeURIComponent(q));
  searching.value = false;
  if (!r || r.ok === false) {
    errMsg.value = (r && r.error) || '搜索失败';
    searchInfo.value = null;
    searchResults.value = [];
    return;
  }
  applyPerm(r);
  searchResults.value = r.results || [];
  searchInfo.value = { q: r.q, visited: r.visited, truncated: r.truncated };
}

function clearSearch() {
  keyword.value = '';
  searchInfo.value = null;
  searchResults.value = [];
}

function b64ToText(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder('utf-8').decode(bytes);
}

async function openPreview(e) {
  previewBusy.value = true;
  previewErr.value = '';
  preview.value = { name: e.name, path: e.path, size: e.size, mime: '', mode: 'info', text: '', dataUrl: null };
  const r = await API.localGet('/files/read?path=' + encodeURIComponent(e.path));
  previewBusy.value = false;
  if (!r || r.ok === false) {
    previewErr.value = (r && r.error) || '读不到这个文件';
    return;
  }
  const f = {
    name: r.name, path: e.path, size: r.size, mime: r.mime || '', kind: r.kind || 'file',
    mode: 'info', text: '', dataUrl: null,
  };
  const big = (r.size || 0) > 4 * 1024 * 1024;
  if (r.kind === 'image') {
    if (r.size > 12 * 1024 * 1024) {
      f.mode = 'info';
      f.note = '图片超过 12MB，不内联预览（避免把窗口撑爆）。';
    } else {
      f.mode = 'image';
      f.dataUrl = 'data:' + (r.mime || 'image/png') + ';base64,' + r.dataBase64;
    }
  } else if ((r.mime === 'application/pdf')) {
    if (r.size > 12 * 1024 * 1024) {
      f.mode = 'info';
      f.note = 'PDF 超过 12MB，不内联预览。';
    } else {
      f.mode = 'pdf';
      f.dataUrl = 'data:application/pdf;base64,' + r.dataBase64;
    }
  } else if (isTextLike(r)) {
    if (big) {
      f.mode = 'info';
      f.note = '文本文件超过 4MB，不整篇载入（可用系统编辑器打开）。';
    } else {
      f.mode = 'text';
      f.text = b64ToText(r.dataBase64);
    }
  } else {
    f.mode = 'info';
    f.note = '这类格式没有内联预览，只显示文件信息。';
  }
  preview.value = f;
}

function previewFromSearch(e) {
  if (e.isDir) { load(e.path); clearSearch(); }
  else openPreview(e);
}

onMounted(() => load(''));
</script>

<template>
  <div class="bz-page fm-page" :class="{ 'with-preview': !!preview }">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">电脑文件</h1>
        <p class="bz-sub">浏览这台电脑的目录、预览常见文件、在当前目录里按文件名搜索。能读到哪儿由「设置 → 桌面控制」的权限决定。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :disabled="!cur" @click="up">上一层</NButton>
        <NButton size="small" @click="home">此电脑</NButton>
        <NButton size="small" :loading="loading" @click="load(cur)">刷新</NButton>
      </div>
    </header>

    <div class="fm-bar">
      <div class="fm-crumbs">
        <button class="fm-crumb" :class="{ on: !cur }" @click="home">此电脑</button>
        <template v-for="(c, i) in crumbs" :key="c.path">
          <span class="fm-sep">›</span>
          <button class="fm-crumb" :class="{ on: i === crumbs.length - 1 }" @click="load(c.path)">{{ c.name }}</button>
        </template>
        <span v-if="perm != null" class="fm-perm" :class="{ ok: perm >= 2 }">
          {{ permTitle || ('权限档位 ' + perm) }}<template v-if="scopes.length">：{{ scopes.join('、') }}</template>
        </span>
      </div>
      <div class="fm-search">
        <NInput v-model:value="keyword" size="small" clearable placeholder="在当前目录里按文件名搜索（含子目录）"
                @keyup.enter="runSearch" @clear="clearSearch" />
        <NButton size="small" :loading="searching" :disabled="!cur" @click="runSearch">搜索</NButton>
      </div>
    </div>

    <div v-if="errMsg" class="fm-err">
      {{ errMsg }}
      <div v-if="hint" class="fm-hint">{{ hint }}</div>
    </div>

    <div class="fm-body">
      <section class="bz-card fm-list">
        <div v-if="loading" class="fm-empty">读取中…</div>

        <template v-else-if="!cur">
          <div class="fm-list-head">可访问的盘符 / 根</div>
          <div v-if="!roots.length" class="fm-empty">权限没给你任何可进的起点，去「设置 → 桌面控制」放开范围。</div>
          <div v-for="v in roots" :key="v.path" class="fm-row fm-row-dir" @click="load(v.path)">
            <span class="fm-ico">▣</span>
            <span class="fm-name">{{ v.name }}</span>
            <span class="fm-meta fm-mono">{{ v.path }}</span>
            <span class="fm-open">进入</span>
          </div>
        </template>

        <template v-else>
          <div class="fm-list-head">
            <span>{{ truncated ? '条目太多，只显示前 500 项' : '目录内容' }}</span>
            <span class="fm-meta">{{ entries.length }} 项 · {{ totalFiles }} 个文件</span>
          </div>
          <div v-if="!entries.length" class="fm-empty">这个目录是空的。</div>
          <div v-for="e in entries" :key="e.path" class="fm-row" :class="{ 'fm-row-dir': e.isDir }" @click="enter(e)">
            <span class="fm-ico">{{ e.isDir ? '▸' : '·' }}</span>
            <span class="fm-name" :title="e.name">{{ e.name }}</span>
            <span class="fm-meta fm-mono">{{ e.isDir ? '' : sizeText(e.size) }}<template v-if="e.modTime"> · {{ fmtTime(e.modTime) }}</template></span>
            <span class="fm-open">{{ e.isDir ? '进入' : '预览' }}</span>
          </div>
        </template>
      </section>

      <section v-if="preview" class="bz-card fm-preview">
        <div class="fm-preview-head">
          <div class="fm-preview-title">
            <b>{{ preview.name }}</b>
            <span class="fm-meta fm-mono">{{ preview.path }}</span>
            <span class="fm-meta">{{ preview.size != null ? sizeText(preview.size) : '' }}{{ preview.mime ? ' · ' + preview.mime : '' }}</span>
          </div>
          <button class="fm-x" @click="preview = null">×</button>
        </div>
        <div v-if="previewBusy" class="fm-empty">读取中…</div>
        <div v-else-if="previewErr" class="fm-err">{{ previewErr }}</div>
        <template v-else>
          <img v-if="preview.mode === 'image'" class="fm-img" :src="preview.dataUrl" :alt="preview.name" />
          <iframe v-else-if="preview.mode === 'pdf'" class="fm-pdf" :src="preview.dataUrl" title="PDF 预览"></iframe>
          <pre v-else-if="preview.mode === 'text'" class="fm-text">{{ preview.text }}</pre>
          <div v-else class="fm-info">
            <div>{{ preview.note || '没有可用的内联预览。' }}</div>
            <div class="fm-hint">这里只读；不提供"用系统程序打开"，避免把本机任意文件当命令去执行。</div>
          </div>
        </template>
      </section>

      <section v-if="searchInfo" class="bz-card fm-list">
        <div class="fm-list-head">
          <span>搜索「{{ searchInfo.q }}」· {{ searchResults.length }} 个命中</span>
          <span class="fm-meta">{{ searchInfo.truncated ? '结果被截断（上限 300 / 2 万条目）' : '看了 ' + searchInfo.visited + ' 个条目' }}</span>
        </div>
        <div v-if="!searchResults.length" class="fm-empty">当前目录下没有名字匹配的文件。</div>
        <div v-for="e in searchResults" :key="e.path" class="fm-row" :class="{ 'fm-row-dir': e.isDir }" @click="previewFromSearch(e)">
          <span class="fm-ico">{{ e.isDir ? '▸' : '·' }}</span>
          <span class="fm-name" :title="e.name">{{ e.name }}</span>
          <span class="fm-meta fm-mono fm-rel">{{ e.rel }}</span>
          <span class="fm-open">{{ e.isDir ? '进入' : '预览' }}</span>
        </div>
      </section>
    </div>
  </div>
</template>

<style>
.fm-page { display: flex; flex-direction: column; }
.fm-bar { display: flex; flex-direction: column; gap: 8px; margin-bottom: 12px; }
.fm-crumbs { display: flex; align-items: center; gap: 2px; flex-wrap: wrap; min-width: 0; }
.fm-crumb { border: 0; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; padding: 2px 4px; cursor: pointer; }
.fm-crumb:hover { color: var(--accent); }
.fm-crumb.on { color: var(--fg); }
.fm-sep { color: var(--fg-3); font-size: 11px; }
.fm-perm { margin-left: 10px; padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--warn); }
.fm-perm.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.fm-search { display: flex; gap: 8px; align-items: center; max-width: 520px; }
.fm-body { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; align-items: start; }
.fm-body:has(.fm-preview) { grid-template-columns: minmax(0, 1fr) minmax(320px, 42%); }
.fm-list { padding: 0; overflow: hidden; }
.fm-list-head { display: flex; justify-content: space-between; gap: 12px; padding: 9px 14px; border-bottom: 1px solid var(--line); font-size: 11px; color: var(--fg-3); }
.fm-row { display: flex; align-items: center; gap: 10px; padding: 7px 14px; border-bottom: 1px solid var(--line); font-size: 12px; cursor: pointer; }
.fm-row:last-child { border-bottom: 0; }
.fm-row:hover { background: var(--surface-2); }
.fm-row-dir .fm-name { color: var(--fg); font-weight: 600; }
.fm-ico { color: var(--fg-3); width: 12px; flex: none; text-align: center; }
.fm-name { color: var(--fg-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0; }
.fm-meta { font-size: 11px; color: var(--fg-3); flex: none; }
.fm-mono { font-family: var(--mono); }
.fm-rel { max-width: 45%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fm-open { flex: none; font-size: 11px; color: var(--fg-3); opacity: 0; }
.fm-row:hover .fm-open { opacity: 1; color: var(--accent); }
.fm-empty { padding: 20px 14px; font-size: 12px; color: var(--fg-3); }
.fm-err { margin-bottom: 10px; padding: 9px 12px; border: 1px solid color-mix(in srgb, var(--err) 35%, var(--line)); border-radius: 2px; background: var(--surface); color: var(--err); font-size: 12px; word-break: break-word; }
.fm-hint { margin-top: 5px; font-size: 11px; color: var(--fg-3); }
.fm-preview { display: flex; flex-direction: column; gap: 8px; position: sticky; top: 0; }
.fm-preview-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
.fm-preview-title { display: flex; flex-direction: column; gap: 3px; min-width: 0; font-size: 13px; color: var(--fg); }
.fm-preview-title b { word-break: break-word; }
.fm-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.fm-img { max-width: 100%; max-height: 60vh; object-fit: contain; background: var(--surface-2); border: 1px solid var(--line); border-radius: 2px; }
.fm-pdf { width: 100%; height: 60vh; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.fm-text { margin: 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; max-height: 62vh; overflow: auto; }
.fm-info { padding: 12px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); font-size: 12px; color: var(--fg-2); }

@media (max-width: 1000px) {
  .fm-body:has(.fm-preview) { grid-template-columns: 1fr; }
  .fm-preview { position: static; }
  .fm-search { max-width: none; }
}
</style>
