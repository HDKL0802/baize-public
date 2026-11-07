<!-- 外部 Agent 页：接 /api/agent/external-agents（配置 + 测试 + 派活 + 最近委托）。
     委托记录只在后端进程内存里，重启清零 —— 界面明确标注，不冒充持久历史。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';
import { fmtTime } from '../api.js';

const agents = ref([]);
const types = ref([]);
const allowRemote = ref(false);
const recent = ref([]);
const loading = ref(true);
const busy = ref('');
const toastMsg = ref('');
const toastTone = ref('');
let toastTimer = null;

function toast(text, tone) {
  toastMsg.value = text;
  toastTone.value = tone || '';
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastMsg.value = ''; }, 2800);
}

const enabledCount = computed(() => agents.value.filter(a => a.enabled).length);

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/external-agents');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取外部 Agent 失败', 'err'); return; }
  const d = r.data || {};
  agents.value = d.agents || [];
  types.value = d.types || [];
  allowRemote.value = !!d.allowRemote;
  recent.value = d.recent || [];
}

/* ---------- 表单 ---------- */
const form = ref(null);
const formBusy = ref(false);

function blankForm() {
  return { id: '', name: '', type: 'http', url: '', token: '', note: '', enabled: true, timeoutSec: 300, allowRemote: allowRemote.value, hasToken: false };
}

function edit(a) {
  form.value = {
    id: a.id, name: a.name || '', type: a.type || 'http', url: a.url || '', token: '',
    note: a.note || '', enabled: a.enabled !== false, timeoutSec: a.timeoutSec || 300,
    allowRemote: allowRemote.value, hasToken: !!a.hasToken,
  };
}

async function saveAgent() {
  const f = form.value;
  if (!f.id.trim()) { toast('id 不能为空（agent_call 的目标名）', 'warn'); return; }
  if (!f.url.trim()) { toast('要填端点地址', 'warn'); return; }
  formBusy.value = true;
  const r = await API.post('/api/agent/external-agents', {
    action: 'upsert', allowRemote: !!f.allowRemote,
    config: {
      id: f.id.trim(), name: f.name.trim() || f.id.trim(), type: f.type, url: f.url.trim(),
      token: f.token.trim(), note: f.note.trim(), enabled: !!f.enabled, timeoutSec: Number(f.timeoutSec) || 0,
    },
  });
  formBusy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  agents.value = (r.data && r.data.agents) || [];
  form.value = null;
  toast('已保存 ' + f.id, 'ok');
}

/* ---------- 单条操作 ---------- */
async function act(action, a, extra) {
  busy.value = action + ':' + (a ? a.id : '');
  const r = await API.post('/api/agent/external-agents', Object.assign({ action, id: a ? a.id : '' }, extra || {}));
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return null; }
  if (r.data && r.data.agents) agents.value = r.data.agents;
  return r.data || {};
}

async function removeAgent(a) {
  if (!window.confirm(`删除外部 Agent「${a.name || a.id}」？`)) return;
  const d = await act('remove', a);
  if (d) { toast('已删除 ' + a.id, 'ok'); load(); }
}

async function testAgent(a) {
  const d = await act('test', a);
  if (!d) return;
  if (d.error) { toast('测试失败：' + d.error, 'err'); return; }
  const res = d.result || {};
  toast('测试通过：' + (res.text || '端点有响应') + (res.latencyMs ? '（' + res.latencyMs + ' ms）' : ''), 'ok');
}

/* ---------- 派活 ---------- */
const call = ref(null);
const callGoal = ref('');
const callResult = ref('');
const callBusy = ref(false);

function openCall(a) {
  call.value = a;
  callGoal.value = '';
  callResult.value = '';
}

async function runCall() {
  if (!call.value || !callGoal.value.trim()) { toast('写一段要派过去的任务', 'warn'); return; }
  callBusy.value = true;
  callResult.value = '';
  const r = await API.post('/api/agent/external-agents', {
    action: 'call', id: call.value.id, goal: callGoal.value.trim(),
  });
  callBusy.value = false;
  const d = r.data || {};
  const res = d.result || {};
  callResult.value = res.text || res.preview || d.error || r.error || '没有返回内容';
  if (d.error) toast('委托失败：' + d.error, 'err');
  else toast('委托完成', 'ok');
  load();
}

onMounted(load);
</script>

<template>
  <div class="bz-page ext-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">外部 Agent</h1>
        <p class="bz-sub">把一段独立的活委托给另一个 Agent（另一个白泽实例，或任意 HTTP 端点）。外部执行体不可信，派活默认走人工审批。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" type="primary" @click="form = blankForm()">新增外部 Agent</NButton>
      </div>
    </header>

    <div class="ext-stats">
      <div class="ext-stat"><span class="ext-v">{{ agents.length }}</span><span class="ext-l">已配置</span></div>
      <div class="ext-stat"><span class="ext-v">{{ enabledCount }}</span><span class="ext-l">已启用</span></div>
      <div class="ext-stat"><span class="ext-v">{{ recent.length }}</span><span class="ext-l">本次启动的委托</span></div>
      <div class="ext-stat"><span class="ext-v">{{ allowRemote ? '已开' : '仅本机' }}</span><span class="ext-l">远端地址</span></div>
    </div>

    <section v-if="form" class="bz-card ext-form">
      <div class="ext-form-head">
        <h2 class="ext-form-title">{{ form.id ? '编辑：' + form.id : '新增外部 Agent' }}</h2>
        <button class="ext-x" @click="form = null">×</button>
      </div>
      <div class="ext-grid">
        <label class="ext-field"><span>ID（唯一，派活时用它）</span><NInput v-model:value="form.id" size="small" placeholder="partner-1" /></label>
        <label class="ext-field"><span>显示名</span><NInput v-model:value="form.name" size="small" placeholder="可空，默认同 ID" /></label>
      </div>
      <div class="ext-grid">
        <label class="ext-field"><span>类型</span>
          <select v-model="form.type" class="ext-select">
            <option v-for="t in types" :key="t" :value="t">{{ t === 'baize' ? 'baize（另一个白泽实例）' : 'http（任意 HTTP 端点）' }}</option>
          </select>
        </label>
        <label class="ext-field"><span>超时秒数</span><NInput v-model:value="form.timeoutSec" size="small" /></label>
      </div>
      <label class="ext-field"><span>端点地址</span><NInput v-model:value="form.url" size="small" placeholder="http://192.168.1.20:8787/api/agent/ext-call" /></label>
      <label class="ext-field"><span>令牌（编辑时留空 = 保留原值）</span>
        <NInput v-model:value="form.token" size="small" type="password" show-password-on="click" placeholder="可空" />
      </label>
      <label class="ext-field"><span>说明（会进系统提示，帮模型决定该派给谁）</span><NInput v-model:value="form.note" size="small" type="textarea" :autosize="{ minRows: 2, maxRows: 4 }" /></label>
      <div class="ext-switches">
        <span class="ext-switch"><NSwitch v-model:value="form.enabled" size="small" /> 启用</span>
        <span class="ext-switch"><NSwitch v-model:value="form.allowRemote" size="small" /> 允许远端地址</span>
      </div>
      <div class="ext-form-foot">
        <span class="ext-note">非本机地址必须打开「允许远端地址」。</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="saveAgent">保存</NButton>
      </div>
    </section>

    <section v-if="call" class="bz-card ext-form">
      <div class="ext-form-head">
        <h2 class="ext-form-title">派活给 · {{ call.name || call.id }}</h2>
        <button class="ext-x" @click="call = null">×</button>
      </div>
      <label class="ext-field"><span>任务内容</span>
        <NInput v-model:value="callGoal" type="textarea" size="small" :autosize="{ minRows: 4, maxRows: 10 }" placeholder="只写任务本身；白泽不会把本机文件 / 记忆 / 密钥带过去。" />
      </label>
      <div class="ext-form-foot">
        <span class="ext-note">不可信执行体：返回内容会当外部输入对待。</span>
        <NButton size="small" type="primary" :loading="callBusy" @click="runCall">派活</NButton>
      </div>
      <pre v-if="callResult" class="ext-result">{{ callResult }}</pre>
    </section>

    <section class="bz-card ext-list">
      <div v-if="!loading && !agents.length" class="bz-empty">
        <div class="bz-empty-title">还没有外部 Agent</div>
        <div class="bz-empty-desc">点「新增外部 Agent」，填一个端点与令牌即可。另一个白泽实例选类型 baize。</div>
      </div>
      <div v-for="a in agents" :key="a.id" class="ext-item">
        <div class="ext-item-main">
          <div class="ext-item-head">
            <b class="ext-item-name">{{ a.name || a.id }}</b>
            <span class="ext-badge">{{ a.type }}</span>
            <span class="ext-badge" :class="a.enabled ? 'ok' : ''">{{ a.enabled ? '已启用' : '已停用' }}</span>
            <span v-if="a.local" class="ext-badge">本机</span>
            <span v-if="!a.hasToken" class="ext-badge">无令牌</span>
          </div>
          <div class="ext-item-meta ext-mono">{{ a.url }}</div>
          <div v-if="a.note" class="ext-item-note">{{ a.note }}</div>
        </div>
        <div class="ext-item-actions">
          <NButton size="tiny" :loading="busy === 'test:' + a.id" @click="testAgent(a)">测试</NButton>
          <NButton size="tiny" type="primary" @click="openCall(a)">派活</NButton>
          <NButton size="tiny" @click="edit(a)">编辑</NButton>
          <button class="ext-del" @click="removeAgent(a)">删除</button>
        </div>
      </div>
    </section>

    <section class="bz-card ext-recent">
      <h2 class="ext-form-title">最近委托</h2>
      <p class="ext-note">只在后端进程内存里，重启清零；完整运行轨迹在「活动追踪」里。</p>
      <div v-if="!recent.length" class="ext-note">这段时间还没有委托记录。</div>
      <div v-for="(r, i) in recent" :key="i" class="ext-rec">
        <span class="ext-badge" :class="r.status === 'done' ? 'ok' : 'err'">{{ r.status === 'done' ? '完成' : '失败' }}</span>
        <b class="ext-rec-name">{{ r.name || r.target }}</b>
        <span class="ext-rec-meta">{{ fmtTime(r.at) }} · {{ r.latencyMs }} ms</span>
        <div class="ext-rec-body">
          <div class="ext-rec-goal">任务：{{ r.goal }}</div>
          <div v-if="r.preview" class="ext-rec-preview">结论：{{ r.preview }}</div>
          <div v-if="r.error" class="ext-item-err">错误：{{ r.error }}</div>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.ext-page { display: flex; flex-direction: column; }
.ext-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px; }
.ext-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.ext-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; }
.ext-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.ext-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.ext-form-head { display: flex; align-items: center; justify-content: space-between; }
.ext-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.ext-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.ext-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.ext-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.ext-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg); font-family: inherit; font-size: 13px; }
.ext-switches { display: flex; gap: 18px; flex-wrap: wrap; }
.ext-switch { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--fg-2); }
.ext-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.ext-note { font-size: 11px; color: var(--fg-3); }
.ext-result { margin: 0; padding: 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg-2); white-space: pre-wrap; word-break: break-word; font-family: var(--mono); font-size: 12px; max-height: 320px; overflow: auto; }
.ext-list { padding: 0; overflow: hidden; }
.ext-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.ext-item:last-child { border-bottom: 0; }
.ext-item-main { min-width: 0; flex: 1; }
.ext-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.ext-item-name { font-size: 13px; color: var(--fg); }
.ext-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.ext-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.ext-badge.err { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.ext-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.ext-item-note { margin-top: 6px; font-size: 11px; color: var(--fg-3); }
.ext-item-err { margin-top: 6px; font-size: 11px; color: var(--err); word-break: break-word; }
.ext-item-actions { display: flex; flex-direction: column; gap: 6px; flex: none; align-items: stretch; }
.ext-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.ext-del:hover { border-color: var(--err); color: var(--err); }
.ext-mono { font-family: var(--mono); }
.ext-recent { margin-top: 14px; display: flex; flex-direction: column; gap: 8px; }
.ext-rec { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; padding: 8px 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); }
.ext-rec-name { font-size: 12px; color: var(--fg); }
.ext-rec-meta { font-size: 11px; color: var(--fg-3); font-family: var(--mono); }
.ext-rec-body { flex-basis: 100%; display: flex; flex-direction: column; gap: 3px; }
.ext-rec-goal, .ext-rec-preview { font-size: 11px; color: var(--fg-2); word-break: break-word; }

@media (max-width: 900px) {
  .ext-stats { grid-template-columns: 1fr 1fr; }
  .ext-grid { grid-template-columns: 1fr; }
  .ext-item { flex-direction: column; }
  .ext-item-actions { flex-direction: row; flex-wrap: wrap; }
}
</style>
