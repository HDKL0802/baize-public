<!-- MCP 服务页：接后端 /api/agent/mcp（列表 + 增改删 + 重连 + 试调用）。
     后端早就实现了连接管理（stdio / http），这里只是把界面接上，不再显示"规划中"。 -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const servers = ref([]);
const hint = ref('');
const loading = ref(true);
const busy = ref('');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2600);
}

const STATUS = {
  connected: { text: '已连接', tone: 'ok' },
  connecting: { text: '连接中', tone: 'warn' },
  disabled: { text: '已禁用', tone: '' },
  error: { text: '连接失败', tone: 'err' },
};

function statusOf(s) { return STATUS[s.status] || { text: s.status || '未知', tone: '' }; }
const connectedCount = computed(() => servers.value.filter(s => s.status === 'connected').length);
const toolTotal = computed(() => servers.value.reduce((n, s) => n + (s.toolCount || 0), 0));

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/mcp');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取 MCP 服务失败', 'err'); return; }
  servers.value = (r.data && r.data.servers) || [];
  hint.value = (r.data && r.data.hint) || '';
}

/* ---------- 新增 / 编辑 ---------- */

const form = ref(null);
const formBusy = ref(false);

function blankForm() {
  return {
    name: '', transport: 'stdio', command: '', argsText: '', envText: '', dir: '',
    url: '', headersText: '', enabled: true, autoApprove: false, safeToolsText: '',
    prefix: '', timeoutSec: 0,
  };
}

function edit(s) {
  const c = s.config || {};
  form.value = {
    name: c.name || s.name,
    transport: c.transport || s.transport || 'stdio',
    command: c.command || '',
    argsText: (c.args || []).join('\n'),
    envText: (c.env || []).join('\n'),
    dir: c.dir || '',
    url: c.url || '',
    headersText: Object.entries(c.headers || {}).map(([k, v]) => k + '=' + v).join('\n'),
    enabled: c.enabled !== false,
    autoApprove: !!c.autoApprove,
    safeToolsText: (c.safeTools || []).join('\n'),
    prefix: c.prefix || '',
    timeoutSec: c.timeoutSec || 0,
  };
}

function lines(s) { return String(s || '').split(/\r?\n/).map(x => x.trim()).filter(Boolean); }

function formConfig(f) {
  const c = {
    name: f.name.trim(), transport: f.transport, enabled: !!f.enabled,
    autoApprove: !!f.autoApprove,
    prefix: f.prefix.trim(), timeoutSec: Number(f.timeoutSec) || 0,
  };
  if (f.transport === 'stdio') {
    c.command = f.command.trim();
    c.args = lines(f.argsText);
    c.env = lines(f.envText);
    c.dir = f.dir.trim();
    if (!c.command) throw new Error('stdio 模式必须填启动命令');
  } else {
    c.url = f.url.trim();
    c.headers = {};
    for (const row of lines(f.headersText)) {
      const i = row.indexOf('=');
      if (i > 0) c.headers[row.slice(0, i).trim()] = row.slice(i + 1).trim();
    }
    if (!c.url) throw new Error('http 模式必须填服务地址');
  }
  c.safeTools = lines(f.safeToolsText);
  return c;
}

async function save() {
  if (!form.value) return;
  let cfg;
  try { cfg = formConfig(form.value); } catch (e) { toast(e.message, 'warn'); return; }
  if (!cfg.name) { toast('服务名不能为空', 'warn'); return; }
  formBusy.value = true;
  const r = await API.post('/api/agent/mcp', { action: 'upsert', config: cfg });
  formBusy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  servers.value = (r.data && r.data.servers) || [];
  form.value = null;
  toast('已保存并尝试连接', 'ok');
}

/* ---------- 单服务操作 ---------- */

async function act(action, server, extra) {
  busy.value = server;
  const r = await API.post('/api/agent/mcp', Object.assign({ action, server }, extra || {}));
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return null; }
  if (r.data && r.data.servers) servers.value = r.data.servers;
  return r.data || {};
}

async function toggle(s) {
  const enabled = !(s.config && s.config.enabled !== false);
  const cfg = Object.assign({}, s.config || {}, { name: s.name, enabled });
  const r = await act('upsert', s.name, { config: cfg });
  if (r) toast(enabled ? '已启用并连接' : '已禁用', 'ok');
}

async function reload(s) {
  const r = await act('reload', s.name);
  if (r) toast('已重连 ' + s.name, 'ok');
}

async function remove(s) {
  if (!window.confirm(`删除 MCP 服务「${s.name}」？配置会一并移除。`)) return;
  const r = await act('remove', s.name);
  if (r) toast('已删除 ' + s.name, 'ok');
}

/* ---------- 试调用 ---------- */

const call = ref(null);
const callTool = ref('');
const callArgs = ref('{}');
const callResult = ref('');
const callBusy = ref(false);

function openCall(s) {
  call.value = s.name;
  callTool.value = (s.tools && s.tools[0]) || '';
  callArgs.value = '{}';
  callResult.value = '';
}

async function runCall() {
  if (!call.value || !callTool.value) return;
  let args;
  try { args = JSON.parse(callArgs.value || '{}'); } catch (e) { toast('参数不是合法 JSON', 'err'); return; }
  callBusy.value = true;
  callResult.value = '';
  const r = await API.post('/api/agent/mcp', { action: 'call', server: call.value, tool: callTool.value, args });
  callBusy.value = false;
  const d = (r.data && r.data.result) || {};
  callResult.value = d.text || (r.data && r.data.error) || (r.error || '没有返回内容');
  if (d.isError) toast('工具返回错误', 'err');
}

let timer = null;
onMounted(() => { load(); timer = setInterval(load, 15000); });
onBeforeUnmount(() => clearInterval(timer));
</script>

<template>
  <div class="bz-page mcp-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">MCP 服务</h1>
        <p class="bz-sub">把外部 MCP 工具挂到白泽身上。连接、重连、禁用都即时生效，不用重启后端。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" type="primary" @click="form = blankForm()">新增服务</NButton>
      </div>
    </header>

    <div class="mcp-stats">
      <div class="mcp-stat"><span class="mcp-stat-v">{{ servers.length }}</span><span class="mcp-stat-l">已配置服务</span></div>
      <div class="mcp-stat"><span class="mcp-stat-v">{{ connectedCount }}</span><span class="mcp-stat-l">已连接</span></div>
      <div class="mcp-stat"><span class="mcp-stat-v">{{ toolTotal }}</span><span class="mcp-stat-l">可用工具</span></div>
      <div class="mcp-stat mcp-stat-grow"><span class="mcp-stat-l">状态</span><span class="mcp-stat-v mcp-hint">{{ hint || (servers.length ? '—' : '还没有配置服务') }}</span></div>
    </div>

    <section v-if="form" class="bz-card mcp-form">
      <div class="mcp-form-head">
        <h2 class="mcp-form-title">{{ form.name ? '编辑：' + form.name : '新增 MCP 服务' }}</h2>
        <button class="mcp-x" @click="form = null">×</button>
      </div>
      <div class="mcp-grid">
        <label class="mcp-field"><span>服务名</span><NInput v-model:value="form.name" size="small" placeholder="例如 filesystem" /></label>
        <label class="mcp-field"><span>传输方式</span>
          <select v-model="form.transport" class="mcp-select">
            <option value="stdio">stdio（本机子进程）</option>
            <option value="http">http（远端/本机地址）</option>
          </select>
        </label>
      </div>

      <template v-if="form.transport === 'stdio'">
        <label class="mcp-field"><span>启动命令</span><NInput v-model:value="form.command" size="small" placeholder="npx / uvx / 绝对路径" /></label>
        <div class="mcp-grid">
          <label class="mcp-field"><span>参数（每行一个）</span><NInput v-model:value="form.argsText" type="textarea" size="small" :autosize="{ minRows: 2, maxRows: 6 }" /></label>
          <label class="mcp-field"><span>环境变量（每行 K=V）</span><NInput v-model:value="form.envText" type="textarea" size="small" :autosize="{ minRows: 2, maxRows: 6 }" /></label>
        </div>
        <label class="mcp-field"><span>工作目录（可空）</span><NInput v-model:value="form.dir" size="small" /></label>
      </template>

      <template v-else>
        <label class="mcp-field"><span>服务地址</span><NInput v-model:value="form.url" size="small" placeholder="http://127.0.0.1:9000/mcp" /></label>
        <label class="mcp-field"><span>请求头（每行 K=V，可空）</span><NInput v-model:value="form.headersText" type="textarea" size="small" :autosize="{ minRows: 2, maxRows: 5 }" /></label>
      </template>

      <div class="mcp-grid">
        <label class="mcp-field"><span>工具名前缀（可空，默认 mcp.服务名）</span><NInput v-model:value="form.prefix" size="small" /></label>
        <label class="mcp-field"><span>超时秒数（0 = 默认）</span><NInput v-model:value="form.timeoutSec" size="small" /></label>
      </div>
      <label class="mcp-field"><span>安全工具白名单（每行一个，免人工审批）</span><NInput v-model:value="form.safeToolsText" type="textarea" size="small" :autosize="{ minRows: 2, maxRows: 5 }" /></label>

      <div class="mcp-switches">
        <span class="mcp-switch"><NSwitch v-model:value="form.enabled" size="small" /> 启用</span>
        <span class="mcp-switch"><NSwitch v-model:value="form.autoApprove" size="small" /> 信任该服务（工具默认免审批）</span>
      </div>

      <div class="mcp-form-foot">
        <span class="mcp-note">保存后会立即尝试连接；连不上会以错误状态显示在列表里，不吞配置。</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="save">保存</NButton>
      </div>
    </section>

    <section v-if="call" class="bz-card mcp-form">
      <div class="mcp-form-head">
        <h2 class="mcp-form-title">试调用 · {{ call }}</h2>
        <button class="mcp-x" @click="call = null">×</button>
      </div>
      <label class="mcp-field"><span>工具名</span><NInput v-model:value="callTool" size="small" /></label>
      <label class="mcp-field"><span>参数 JSON</span><NInput v-model:value="callArgs" type="textarea" size="small" :autosize="{ minRows: 3, maxRows: 8 }" /></label>
      <div class="mcp-form-foot">
        <span class="mcp-note">直接调用远端工具；危险操作仍受审批闸门约束。</span>
        <NButton size="small" type="primary" :loading="callBusy" @click="runCall">调用</NButton>
      </div>
      <pre v-if="callResult" class="mcp-result">{{ callResult }}</pre>
    </section>

    <section class="bz-card mcp-list">
      <div v-if="!loading && !servers.length" class="bz-empty">
        <div class="bz-empty-title">还没有 MCP 服务</div>
        <div class="bz-empty-desc">点右上角「新增服务」，填 stdio 命令或 http 地址即可。白泽会自动接管连接与工具注册。</div>
      </div>
      <div v-for="s in servers" :key="s.name" class="mcp-item">
        <div class="mcp-item-main">
          <div class="mcp-item-head">
            <b class="mcp-item-name">{{ s.name }}</b>
            <span class="mcp-badge" :class="statusOf(s).tone">{{ statusOf(s).text }}</span>
            <span class="mcp-badge">{{ s.transport }}</span>
            <span v-if="s.toolCount" class="mcp-badge">{{ s.toolCount }} 工具</span>
            <span v-if="s.latencyMs" class="mcp-badge">{{ s.latencyMs }} ms</span>
          </div>
          <div class="mcp-item-meta mcp-mono">
            {{ s.transport === 'http' ? (s.config.url || '—') : ((s.config.command || '—') + ' ' + ((s.config.args || []).join(' '))) }}
          </div>
          <div v-if="s.error" class="mcp-item-err">{{ s.error }}</div>
          <div v-if="s.note" class="mcp-item-note">{{ s.note }}</div>
          <div v-if="s.tools && s.tools.length" class="mcp-tools">
            <button v-for="t in s.tools" :key="t" class="mcp-tool" @click="openCall(s); callTool = t">{{ t }}</button>
          </div>
        </div>
        <div class="mcp-item-actions">
          <NButton size="tiny" :loading="busy === s.name" @click="reload(s)">重连</NButton>
          <NButton size="tiny" @click="toggle(s)">{{ (s.config && s.config.enabled !== false) ? '禁用' : '启用' }}</NButton>
          <NButton size="tiny" @click="edit(s)">编辑</NButton>
          <NButton size="tiny" type="primary" @click="openCall(s)">试调用</NButton>
          <button class="mcp-del" title="删除" @click="remove(s)">删除</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.mcp-page { display: flex; flex-direction: column; }
.mcp-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)) 2fr; gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px; }
.mcp-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.mcp-stat-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; }
.mcp-stat-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.mcp-stat-grow { flex-direction: row; align-items: center; justify-content: space-between; gap: 10px; }
.mcp-hint { font-size: 12px; color: var(--fg-2); text-align: right; }
.mcp-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.mcp-form-head { display: flex; align-items: center; justify-content: space-between; }
.mcp-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.mcp-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.mcp-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.mcp-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.mcp-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg); font-family: inherit; font-size: 13px; }
.mcp-switches { display: flex; gap: 18px; flex-wrap: wrap; }
.mcp-switch { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--fg-2); }
.mcp-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.mcp-note { font-size: 11px; color: var(--fg-3); }
.mcp-result { margin: 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; max-height: 260px; overflow: auto; }
.mcp-list { padding: 0; overflow: hidden; }
.mcp-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.mcp-item:last-child { border-bottom: 0; }
.mcp-item-main { min-width: 0; flex: 1; }
.mcp-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.mcp-item-name { font-size: 13px; color: var(--fg); }
.mcp-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.mcp-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.mcp-badge.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); }
.mcp-badge.err { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.mcp-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.mcp-item-err { margin-top: 6px; font-size: 11px; color: var(--err); word-break: break-word; }
.mcp-item-note { margin-top: 6px; font-size: 11px; color: var(--fg-3); }
.mcp-tools { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 9px; }
.mcp-tool { padding: 2px 6px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-2); font-family: var(--mono); font-size: 10px; cursor: pointer; }
.mcp-tool:hover { border-color: var(--accent); color: var(--accent); }
.mcp-item-actions { display: flex; flex-direction: column; gap: 6px; flex: none; align-items: stretch; }
.mcp-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.mcp-del:hover { border-color: var(--err); color: var(--err); }
.mcp-mono { font-family: var(--mono); }

@media (max-width: 900px) {
  .mcp-stats { grid-template-columns: 1fr 1fr; }
  .mcp-grid { grid-template-columns: 1fr; }
  .mcp-item { flex-direction: column; }
  .mcp-item-actions { flex-direction: row; flex-wrap: wrap; }
}
</style>
