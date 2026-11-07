<!-- 插件市场页：接 /api/agent/plugins（源管理 / 逛市场 / 安装 / 更新 / 启停 / 卸载 / 直装 URL）。
     安装会真的落技能目录并改 MCP 配置，后端不提供"试运行"，界面也不假装能预演。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API } from '../api.js';

const sources = ref([]);
const available = ref([]);
const installed = ref([]);
const dir = ref('');
const loading = ref(true);
const busy = ref('');
const channel = ref('all');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 3000);
}

const channels = computed(() => {
  const set = new Set();
  available.value.forEach(a => set.add(a.channel || 'community'));
  return [...set];
});

const shelf = computed(() => {
  if (channel.value === 'all') return available.value;
  return available.value.filter(a => (a.channel || 'community') === channel.value);
});

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/plugins');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取插件市场失败', 'err'); return; }
  const d = r.data || {};
  sources.value = d.sources || [];
  available.value = d.available || [];
  installed.value = d.installed || [];
  dir.value = d.dir || '';
}

/* ---------- 源管理 ---------- */
const showSource = ref(false);
const srcForm = ref({ name: '', url: '' });

async function addSource() {
  const f = srcForm.value;
  if (!f.url.trim()) { toast('要填索引地址', 'warn'); return; }
  busy.value = 'addSource';
  const r = await API.post('/api/agent/plugins', { action: 'addSource', name: f.name.trim(), url: f.url.trim(), enabled: true });
  busy.value = '';
  if (!r.ok) { toast(r.error || '添加源失败', 'err'); return; }
  srcForm.value = { name: '', url: '' };
  showSource.value = false;
  await load();
  toast('已添加源', 'ok');
}

async function removeSource(s) {
  if (!window.confirm(`移除源「${s.name}」？已安装的插件不会自动卸载。`)) return;
  busy.value = 'removeSource:' + s.url;
  const r = await API.post('/api/agent/plugins', { action: 'removeSource', url: s.url });
  busy.value = '';
  if (!r.ok) { toast(r.error || '移除失败', 'err'); return; }
  await load();
  toast('已移除源', 'ok');
}

/* ---------- 安装 / 启停 / 卸载 ---------- */
async function pluginAct(action, id) {
  busy.value = action + ':' + id;
  const r = await API.post('/api/agent/plugins', { action, id });
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return false; }
  if (r.data && r.data.installed) installed.value = r.data.installed;
  await load();
  return true;
}

async function install(a) {
  if (!window.confirm(`安装「${a.name}」${a.version ? ' v' + a.version : ''}？\n会往技能目录写文件，并可能注册 MCP 服务。`)) return;
  if (await pluginAct('install', a.id)) toast('已安装 ' + a.name, 'ok');
}

async function update(a) {
  if (!window.confirm(`把「${a.name}」升级到 ${a.version}？旧版本会先被拆掉。`)) return;
  if (await pluginAct('update', a.id)) toast('已更新 ' + a.name, 'ok');
}

async function toggleInstalled(p) {
  const action = p.enabled ? 'disable' : 'enable';
  if (await pluginAct(action, p.id)) toast((p.enabled ? '已停用 ' : '已启用 ') + p.name, 'ok');
}

async function uninstall(p) {
  if (!window.confirm(`卸载「${p.name}」？它带来的技能与 MCP 注册会被一起清掉。`)) return;
  if (await pluginAct('uninstall', p.id)) toast('已卸载 ' + p.name, 'ok');
}

/* ---------- 直装 URL / 本地包 ---------- */
const direct = ref(null);
const directBusy = ref(false);
async function installDirect() {
  const d = direct.value;
  if (!d.url.trim()) { toast('要填插件包地址或本地路径', 'warn'); return; }
  directBusy.value = true;
  const r = await API.post('/api/agent/plugins', { action: 'install', url: d.url.trim() });
  directBusy.value = false;
  if (!r.ok) { toast(r.error || '安装失败', 'err'); return; }
  direct.value = null;
  await load();
  toast('已安装', 'ok');
}

onMounted(load);
</script>

<template>
  <div class="bz-page pl-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">插件市场</h1>
        <p class="bz-sub">插件包是静态 zip：装进来会落技能目录、注册 MCP 服务。没有沙箱隔离，只装信得过的源。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" @click="showSource = !showSource">添加源</NButton>
        <NButton size="small" type="primary" @click="direct = { url: '' }">从地址直装</NButton>
      </div>
    </header>

    <div class="pl-stats">
      <div class="pl-stat"><span class="pl-v">{{ sources.length }}</span><span class="pl-l">插件源</span></div>
      <div class="pl-stat"><span class="pl-v">{{ available.length }}</span><span class="pl-l">货架条目</span></div>
      <div class="pl-stat"><span class="pl-v">{{ installed.length }}</span><span class="pl-l">已安装</span></div>
      <div class="pl-stat"><span class="pl-v">{{ installed.filter(p => p.enabled).length }}</span><span class="pl-l">已启用</span></div>
    </div>

    <section v-if="showSource" class="bz-card pl-form">
      <div class="pl-form-head"><h2 class="pl-form-title">添加插件源</h2><button class="pl-x" @click="showSource = false">×</button></div>
      <div class="pl-grid">
        <label class="pl-field"><span>源名称（可空）</span><NInput v-model:value="srcForm.name" size="small" placeholder="团队源" /></label>
        <label class="pl-field"><span>index.json 地址</span><NInput v-model:value="srcForm.url" size="small" placeholder="https://.../index.json" /></label>
      </div>
      <div class="pl-form-foot">
        <span class="pl-note">支持 http(s) 与本地文件路径。</span>
        <NButton size="small" type="primary" :loading="busy === 'addSource'" @click="addSource">添加</NButton>
      </div>
    </section>

    <section v-if="direct" class="bz-card pl-form">
      <div class="pl-form-head"><h2 class="pl-form-title">从地址直装</h2><button class="pl-x" @click="direct = null">×</button></div>
      <label class="pl-field"><span>插件包（.zip 地址或本地路径）</span><NInput v-model:value="direct.url" size="small" placeholder="https://.../plugin.zip 或 D:\pkg\plugin.zip" /></label>
      <div class="pl-form-foot">
        <span class="pl-note">直装不校验索引里的 sha256；能用源装就尽量用源。</span>
        <NButton size="small" type="primary" :loading="directBusy" @click="installDirect">安装</NButton>
      </div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head"><h2 class="pl-form-title">插件源</h2><span v-if="dir" class="pl-note pl-mono">安装目录 {{ dir }}</span></div>
      <div v-for="s in sources" :key="s.url" class="pl-src">
        <div class="pl-src-main">
          <div class="pl-src-head">
            <b class="pl-src-name">{{ s.name || s.url }}</b>
            <span v-if="s.builtin" class="pl-badge ok">内置官方源</span>
            <span class="pl-badge" :class="s.enabled ? '' : 'warn'">{{ s.enabled ? '启用' : '停用' }}</span>
            <span class="pl-badge">{{ s.count }} 条</span>
          </div>
          <div class="pl-src-meta pl-mono">{{ s.url }}</div>
          <div v-if="s.error" class="pl-err">{{ s.error }}</div>
        </div>
        <button v-if="!s.builtin" class="pl-del" @click="removeSource(s)">移除</button>
      </div>
      <div v-if="!sources.length" class="pl-note">没有配置任何源，可以添加一个 index.json 地址。</div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head">
        <h2 class="pl-form-title">货架</h2>
        <div class="pl-tabs">
          <button class="pl-tab" :class="{ on: channel === 'all' }" @click="channel = 'all'">全部</button>
          <button v-for="c in channels" :key="c" class="pl-tab" :class="{ on: channel === c }" @click="channel = c">{{ c === 'official' ? '官方' : '社区' }}</button>
        </div>
      </div>
      <div v-if="!loading && !shelf.length" class="pl-note">货架是空的。源拉取失败会在上面标出来，不会是"假装没有插件"。</div>
      <div class="pl-grid-cards">
        <div v-for="a in shelf" :key="a.source + ':' + a.id" class="pl-card-item">
          <div class="pl-card-item-head">
            <b class="pl-item-name">{{ a.name }}</b>
            <span class="pl-badge">{{ a.version }}</span>
            <span class="pl-badge" :class="a.channel === 'official' ? 'ok' : ''">{{ a.channel === 'official' ? '官方' : '社区' }}</span>
            <span v-if="a.stage === 'beta'" class="pl-badge warn">beta</span>
          </div>
          <div class="pl-item-desc">{{ a.description || '（没有描述）' }}</div>
          <div class="pl-item-meta">
            {{ a.id }}
            <template v-if="a.author"> · {{ a.author }}</template>
            <template v-if="a.category"> · {{ a.category }}</template>
            <template v-if="a.license"> · {{ a.license }}</template>
          </div>
          <div class="pl-item-foot">
            <span v-if="a.hasUpdate" class="pl-badge warn">可升级 {{ a.instVersion }} → {{ a.version }}</span>
            <span v-else-if="a.installed" class="pl-badge ok">已装 {{ a.instVersion || a.version }}</span>
            <NButton v-if="a.hasUpdate" size="tiny" :loading="busy === 'update:' + a.id" @click="update(a)">升级</NButton>
            <NButton v-else-if="!a.installed" size="tiny" :loading="busy === 'install:' + a.id" @click="install(a)">安装</NButton>
            <NButton v-else size="tiny" disabled>已安装</NButton>
          </div>
        </div>
      </div>
    </section>

    <section class="bz-card pl-card">
      <div class="pl-card-head"><h2 class="pl-form-title">已安装</h2></div>
      <div v-if="!installed.length" class="pl-note">还没装任何插件。</div>
      <div v-for="p in installed" :key="p.id" class="pl-inst">
        <div class="pl-inst-main">
          <div class="pl-inst-head">
            <b class="pl-item-name">{{ p.name }}</b>
            <span class="pl-badge">{{ p.version }}</span>
            <span class="pl-badge" :class="p.enabled ? 'ok' : ''">{{ p.enabled ? '已启用' : '已停用' }}</span>
          </div>
          <div class="pl-item-meta">
            来源 {{ p.source || '—' }}
            <template v-if="(p.skills || []).length"> · 技能 {{ p.skills.join(', ') }}</template>
            <template v-if="(p.mcp || []).length"> · MCP {{ p.mcp.join(', ') }}</template>
          </div>
          <div v-if="p.note" class="pl-note">{{ p.note }}</div>
        </div>
        <div class="pl-inst-actions">
          <NButton size="tiny" :loading="busy === 'enable:' + p.id || busy === 'disable:' + p.id" @click="toggleInstalled(p)">
            {{ p.enabled ? '停用' : '启用' }}
          </NButton>
          <button class="pl-del" @click="uninstall(p)">卸载</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.pl-page { display: flex; flex-direction: column; gap: 14px; }
.pl-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 0; }
.pl-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.pl-v { font-family: var(--mono); font-size: 16px; color: var(--fg); }
.pl-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.pl-form { display: flex; flex-direction: column; gap: 10px; }
.pl-form-head, .pl-card-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pl-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.pl-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.pl-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.pl-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.pl-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pl-note { font-size: 11px; color: var(--fg-3); }
.pl-mono { font-family: var(--mono); }
.pl-card { display: flex; flex-direction: column; gap: 10px; }
.pl-src { display: flex; gap: 12px; justify-content: space-between; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.pl-src-main { min-width: 0; flex: 1; }
.pl-src-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.pl-src-name { font-size: 12px; color: var(--fg); }
.pl-src-meta { margin-top: 4px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.pl-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.pl-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.pl-badge.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); }
.pl-err { margin-top: 5px; font-size: 11px; color: var(--err); word-break: break-word; }
.pl-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.pl-del:hover { border-color: var(--err); color: var(--err); }
.pl-tabs { display: flex; gap: 4px; }
.pl-tab { padding: 3px 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 11px; cursor: pointer; }
.pl-tab.on { border-color: var(--accent); color: var(--accent); }
.pl-grid-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 10px; }
.pl-card-item { display: flex; flex-direction: column; gap: 6px; padding: 12px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.pl-card-item-head { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; }
.pl-item-name { font-size: 13px; color: var(--fg); }
.pl-item-desc { font-size: 12px; color: var(--fg-2); line-height: 1.55; }
.pl-item-meta { font-size: 11px; color: var(--fg-3); word-break: break-all; }
.pl-item-foot { margin-top: auto; display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.pl-inst { display: flex; gap: 12px; justify-content: space-between; padding: 10px; border: 1px solid var(--line); border-radius: 2px; }
.pl-inst-main { min-width: 0; flex: 1; }
.pl-inst-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.pl-inst-actions { display: flex; gap: 6px; align-items: center; }

@media (max-width: 900px) {
  .pl-stats { grid-template-columns: 1fr 1fr; }
  .pl-grid { grid-template-columns: 1fr; }
  .pl-inst { flex-direction: column; }
}
</style>
