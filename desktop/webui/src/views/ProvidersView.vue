<!-- 模型通道页：接 /api/agent/providers（增改删 / 探活 / 发现模型 / 设默认 / 充值记账）。
     后端能力齐全，这里只做界面；不做"额度轮换"之类后端没有的能力。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const providers = ref([]);
const presets = ref([]);
const allowRemote = ref(false);
const activeProvider = ref('');
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

const usableCount = computed(() => providers.value.filter(p => p.usable).length);

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/providers');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取模型通道失败', 'err'); return; }
  const d = r.data || {};
  providers.value = d.providers || [];
  presets.value = d.presets || [];
  allowRemote.value = !!d.allowRemote;
  activeProvider.value = d.activeProvider || '';
}

/* ---------- 表单 ---------- */
const form = ref(null);

function blankForm() {
  return {
    name: '', protocol: 'openai', baseUrl: '', model: '', apiKey: '', kindsText: '',
    cost: '', privacy: '', timeoutSec: 0, fallback: false, default: false, allowRemote: allowRemote.value,
  };
}

function edit(p) {
  form.value = {
    name: p.name, protocol: p.protocol || 'openai', baseUrl: p.baseUrl || '', model: p.model || '',
    apiKey: '', kindsText: (p.kinds || []).join(','), cost: p.cost || '', privacy: p.privacy || '',
    timeoutSec: 0, fallback: !!p.fallback, default: !!p.default, allowRemote: allowRemote.value,
    hasApiKey: !!p.hasApiKey,
  };
}

function applyPreset(id) {
  const p = presets.value.find(x => x.id === id);
  if (!p || !form.value) return;
  form.value.name = p.name;
  form.value.protocol = p.protocol || 'openai';
  form.value.baseUrl = p.baseUrl || '';
  form.value.model = p.model || '';
  form.value.cost = p.cost || '';
  form.value.privacy = p.privacy || '';
}

const discovering = ref(false);
const discovered = ref([]);

async function discoverModels() {
  if (!form.value) return;
  discovering.value = true;
  discovered.value = [];
  const r = await API.post('/api/agent/providers', {
    action: 'discover',
    protocol: form.value.protocol,
    baseUrl: form.value.baseUrl,
    apiKey: form.value.apiKey,
    allowRemote: form.value.allowRemote,
  });
  discovering.value = false;
  if (!r.ok) { toast(r.error || '拉取模型列表失败', 'err'); return; }
  if (r.data && r.data.error) { toast(r.data.error, 'err'); return; }
  discovered.value = (r.data && r.data.models) || [];
  if (!discovered.value.length) toast('这个端点没有返回可用模型', 'warn');
  else toast('拉到 ' + discovered.value.length + ' 个模型', 'ok');
}

const formBusy = ref(false);

async function saveProvider() {
  if (!form.value) return;
  const f = form.value;
  const cfg = {
    name: f.name.trim(), protocol: f.protocol, baseUrl: f.baseUrl.trim(), model: f.model.trim(),
    apiKey: f.apiKey.trim(), kinds: String(f.kindsText || '').split(',').map(s => s.trim()).filter(Boolean),
    cost: f.cost.trim(), privacy: f.privacy.trim(), timeoutSec: Number(f.timeoutSec) || 0,
    fallback: !!f.fallback, default: !!f.default,
  };
  if (!cfg.name) { toast('通道名不能为空', 'warn'); return; }
  if (!cfg.baseUrl) { toast('要填 Base URL', 'warn'); return; }
  if (!cfg.model) { toast('要填模型名', 'warn'); return; }
  formBusy.value = true;
  const r = await API.post('/api/agent/providers', { action: 'upsert', config: cfg, allowRemote: !!f.allowRemote });
  formBusy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  providers.value = (r.data && r.data.providers) || [];
  form.value = null;
  discovered.value = [];
  toast('已保存 ' + cfg.name, 'ok');
  load();
}

/* ---------- 单通道操作 ---------- */
async function act(action, name, extra) {
  busy.value = action + ':' + (name || '');
  const r = await API.post('/api/agent/providers', Object.assign({ action, name }, extra || {}));
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return null; }
  if (r.data && r.data.providers) providers.value = r.data.providers;
  return r.data || {};
}

async function testProvider(p) {
  const d = await act('test', p.name);
  if (!d) return;
  const res = d.result || {};
  if (d.error) toast('探活失败：' + d.error, 'err');
  else toast('探活成功：' + (res.reply || '有回复') + (res.latencyMs ? '（' + res.latencyMs + ' ms）' : ''), 'ok');
}

async function removeProvider(p) {
  if (!window.confirm(`删除模型通道「${p.name}」？`)) return;
  const d = await act('remove', p.name);
  if (d) { toast('已删除 ' + p.name, 'ok'); load(); }
}

async function setActive(p) {
  const d = await act('active', p.name);
  if (d) { activeProvider.value = d.activeProvider || ''; toast(p.name + ' 已设为默认首选', 'ok'); }
}

async function clearActive() {
  const d = await act('active', '');
  if (d) { activeProvider.value = ''; toast('已恢复自动路由', 'ok'); }
}

async function copySecret(p, field) {
  const r = await API.post('/api/agent/providers/secret', { name: p.name, field });
  if (!r.ok) { toast(r.error || '读取失败', 'err'); return; }
  const v = (r.data && r.data.value) || '';
  if (!v) { toast('这个字段是空的', 'warn'); return; }
  try {
    await navigator.clipboard.writeText(v);
    toast('已复制到剪贴板', 'ok');
  } catch (e) {
    toast('浏览器不让写剪贴板，请手动复制：' + v, 'warn');
  }
}

/* ---------- 充值记账 ---------- */
const recharges = ref({});
const rechargeForm = ref(null);

async function loadRecharges() {
  const r = await API.get('/api/agent/providers/recharge');
  if (r.ok) recharges.value = (r.data && r.data.recharges) || {};
}

async function addRecharge() {
  const f = rechargeForm.value;
  if (!f || !f.name) { toast('选一条通道', 'warn'); return; }
  const r = await API.post('/api/agent/providers/recharge', {
    action: 'add', name: f.name, amount: Number(f.amount) || 0, note: f.note,
  });
  if (!r.ok) { toast(r.error || '记账失败', 'err'); return; }
  rechargeForm.value = null;
  await loadRecharges();
  toast('已记一笔充值', 'ok');
}

async function removeRecharge(name, id) {
  const r = await API.post('/api/agent/providers/recharge', { action: 'remove', name, id });
  if (!r.ok) { toast(r.error || '删除失败', 'err'); return; }
  await loadRecharges();
}

function rechargeTotal(name) {
  return (recharges.value[name] || []).reduce((n, x) => n + (x.amount || 0), 0);
}

onMounted(() => { load(); loadRecharges(); });
</script>

<template>
  <div class="bz-page prov-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">模型通道</h1>
        <p class="bz-sub">白泽用哪家模型、走什么协议、默认用哪条。保存即写回 config.json，运行时立即生效。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" @click="clearActive" :disabled="!activeProvider">恢复自动路由</NButton>
        <NButton size="small" type="primary" @click="form = blankForm()">新增通道</NButton>
      </div>
    </header>

    <div class="prov-stats">
      <div class="prov-stat"><span class="prov-v">{{ providers.length }}</span><span class="prov-l">已配置通道</span></div>
      <div class="prov-stat"><span class="prov-v">{{ usableCount }}</span><span class="prov-l">当前可用</span></div>
      <div class="prov-stat"><span class="prov-v">{{ activeProvider || '自动' }}</span><span class="prov-l">默认首选</span></div>
      <div class="prov-stat"><span class="prov-v">{{ allowRemote ? '已开' : '仅本机' }}</span><span class="prov-l">远端地址</span></div>
    </div>

    <section v-if="form" class="bz-card prov-form">
      <div class="prov-form-head">
        <h2 class="prov-form-title">{{ form.name ? '编辑：' + form.name : '新增模型通道' }}</h2>
        <button class="prov-x" @click="form = null">×</button>
      </div>

      <label v-if="presets.length" class="prov-field"><span>从模板填充（可选）</span>
        <select class="prov-select" @change="applyPreset($event.target.value); $event.target.value = ''">
          <option value="">选一个模板…</option>
          <option v-for="p in presets" :key="p.id" :value="p.id">{{ p.name }}{{ p.region === 'local' ? ' · 本地' : '' }}</option>
        </select>
      </label>

      <div class="prov-grid">
        <label class="prov-field"><span>通道名（唯一）</span><NInput v-model:value="form.name" size="small" placeholder="例如 dmx-intl-1" /></label>
        <label class="prov-field"><span>协议</span>
          <select v-model="form.protocol" class="prov-select">
            <option value="openai">OpenAI 兼容</option>
            <option value="anthropic">Anthropic</option>
          </select>
        </label>
      </div>
      <label class="prov-field"><span>Base URL</span><NInput v-model:value="form.baseUrl" size="small" placeholder="https://api.deepseek.com/v1" /></label>
      <div class="prov-grid">
        <label class="prov-field"><span>模型名</span><NInput v-model:value="form.model" size="small" placeholder="deepseek-v4-flash" /></label>
        <label class="prov-field"><span>API Key（编辑时留空 = 保留原值）</span>
          <NInput v-model:value="form.apiKey" size="small" type="password" show-password-on="click" placeholder="sk-..." />
        </label>
      </div>

      <div class="prov-form-foot">
        <span class="prov-note">不知道模型名？先填 Base URL 与 Key，点「拉取模型列表」。</span>
        <NButton size="small" :loading="discovering" @click="discoverModels">拉取模型列表</NButton>
      </div>
      <div v-if="discovered.length" class="prov-chips">
        <button v-for="m in discovered" :key="m" class="prov-chip" @click="form.model = m">{{ m }}</button>
      </div>

      <div class="prov-grid">
        <label class="prov-field"><span>任务类型（逗号分隔，可空 = 通吃）</span><NInput v-model:value="form.kindsText" size="small" placeholder="chat,code" /></label>
        <label class="prov-field"><span>超时秒数（0 = 默认）</span><NInput v-model:value="form.timeoutSec" size="small" /></label>
      </div>
      <div class="prov-grid">
        <label class="prov-field"><span>成本标注（人话）</span><NInput v-model:value="form.cost" size="small" placeholder="按量计费（便宜）" /></label>
        <label class="prov-field"><span>隐私标注</span><NInput v-model:value="form.privacy" size="small" placeholder="公网云端 / 本地，不出机器" /></label>
      </div>

      <div class="prov-switches">
        <span class="prov-switch"><NSwitch v-model:value="form.default" size="small" /> 标记为默认首选</span>
        <span class="prov-switch"><NSwitch v-model:value="form.fallback" size="small" /> 作为回退通道</span>
        <span class="prov-switch"><NSwitch v-model:value="form.allowRemote" size="small" /> 允许远端地址</span>
      </div>
      <div class="prov-form-foot">
        <span class="prov-note">非本机地址必须打开「允许远端地址」，否则保存会被后端拒绝。</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="saveProvider">保存</NButton>
      </div>
    </section>

    <section class="bz-card prov-list">
      <div v-if="!loading && !providers.length" class="bz-empty">
        <div class="bz-empty-title">还没有模型通道</div>
        <div class="bz-empty-desc">点右上角「新增通道」。本地模型（Ollama 等）不需要 Key，云端通道填 Key 即可。</div>
      </div>
      <div v-for="p in providers" :key="p.name" class="prov-item">
        <div class="prov-item-main">
          <div class="prov-item-head">
            <b class="prov-item-name">{{ p.name }}</b>
            <span class="prov-badge" :class="p.usable ? 'ok' : 'err'">{{ p.usable ? '可用' : '不可用' }}</span>
            <span class="prov-badge">{{ p.protocol }}</span>
            <span v-if="p.local" class="prov-badge">本机</span>
            <span v-if="p.default" class="prov-badge warn">默认</span>
            <span v-if="p.isActive" class="prov-badge ok">当前首选</span>
            <span v-if="!p.hasApiKey" class="prov-badge">无 Key</span>
          </div>
          <div class="prov-item-meta prov-mono">{{ p.baseUrl }} · {{ p.model }}</div>
          <div v-if="p.error" class="prov-item-err">{{ p.error }}</div>
          <div v-if="p.cost || p.privacy" class="prov-item-note">{{ [p.cost, p.privacy].filter(Boolean).join(' · ') }}</div>
        </div>
        <div class="prov-item-actions">
          <NButton size="tiny" :loading="busy === 'test:' + p.name" @click="testProvider(p)">探活</NButton>
          <NButton size="tiny" :disabled="p.isActive" @click="setActive(p)">设为默认</NButton>
          <NButton size="tiny" @click="edit(p)">编辑</NButton>
          <NButton v-if="p.hasApiKey" size="tiny" @click="copySecret(p, 'apiKey')">复制 Key</NButton>
          <button class="prov-del" @click="removeProvider(p)">删除</button>
        </div>
      </div>
    </section>

    <section class="bz-card prov-recharge">
      <div class="prov-form-head">
        <h2 class="prov-form-title">充值记账</h2>
        <NButton size="tiny" @click="rechargeForm = { name: providers[0] ? providers[0].name : '', amount: 0, note: '' }">记一笔</NButton>
      </div>
      <p class="prov-note">只记金额 / 时间 / 备注，不做汇率与单价换算。</p>
      <div v-if="rechargeForm" class="prov-grid prov-recharge-form">
        <label class="prov-field"><span>通道</span>
          <select v-model="rechargeForm.name" class="prov-select">
            <option v-for="p in providers" :key="p.name" :value="p.name">{{ p.name }}</option>
          </select>
        </label>
        <label class="prov-field"><span>金额</span><NInput v-model:value="rechargeForm.amount" size="small" /></label>
        <label class="prov-field"><span>备注</span><NInput v-model:value="rechargeForm.note" size="small" placeholder="可空" /></label>
        <div class="prov-recharge-add"><NButton size="small" type="primary" @click="addRecharge">保存</NButton></div>
      </div>
      <div v-for="p in providers" :key="'rc-' + p.name" class="prov-rc-row">
        <template v-if="(recharges[p.name] || []).length">
          <span class="prov-rc-name">{{ p.name }}</span>
          <span class="prov-rc-total prov-mono">合计 {{ rechargeTotal(p.name) }}</span>
          <div class="prov-rc-items">
            <span v-for="rc in recharges[p.name]" :key="rc.id" class="prov-rc-item">
              {{ rc.amount }} · {{ rc.note || '—' }}
              <button class="prov-rc-x" title="删除这笔" @click="removeRecharge(p.name, rc.id)">×</button>
            </span>
          </div>
        </template>
      </div>
      <div v-if="!Object.keys(recharges).length" class="prov-note">还没有充值记录。</div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.prov-page { display: flex; flex-direction: column; }
.prov-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px; }
.prov-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.prov-v { font-family: var(--mono); font-size: 16px; color: var(--fg); font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.prov-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.prov-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.prov-form-head { display: flex; align-items: center; justify-content: space-between; }
.prov-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.prov-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.prov-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.prov-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.prov-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg); font-family: inherit; font-size: 13px; }
.prov-switches { display: flex; gap: 18px; flex-wrap: wrap; }
.prov-switch { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--fg-2); }
.prov-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.prov-note { font-size: 11px; color: var(--fg-3); }
.prov-chips { display: flex; flex-wrap: wrap; gap: 5px; }
.prov-chip { padding: 2px 7px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-2); font-family: var(--mono); font-size: 11px; cursor: pointer; }
.prov-chip:hover { border-color: var(--accent); color: var(--accent); }
.prov-list { padding: 0; overflow: hidden; }
.prov-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.prov-item:last-child { border-bottom: 0; }
.prov-item-main { min-width: 0; flex: 1; }
.prov-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.prov-item-name { font-size: 13px; color: var(--fg); }
.prov-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.prov-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.prov-badge.warn { color: var(--warn); border-color: color-mix(in srgb, var(--warn) 40%, transparent); }
.prov-badge.err { color: var(--err); border-color: color-mix(in srgb, var(--err) 40%, transparent); }
.prov-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.prov-item-err { margin-top: 6px; font-size: 11px; color: var(--err); word-break: break-word; }
.prov-item-note { margin-top: 6px; font-size: 11px; color: var(--fg-3); }
.prov-item-actions { display: flex; flex-direction: column; gap: 6px; flex: none; align-items: stretch; }
.prov-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.prov-del:hover { border-color: var(--err); color: var(--err); }
.prov-mono { font-family: var(--mono); }
.prov-recharge { margin-top: 14px; display: flex; flex-direction: column; gap: 10px; }
.prov-recharge-form { align-items: end; }
.prov-recharge-add { display: flex; align-items: center; }
.prov-rc-row { display: flex; gap: 12px; align-items: baseline; padding-top: 8px; border-top: 1px dashed var(--line); font-size: 12px; }
.prov-rc-name { color: var(--fg); font-weight: 600; }
.prov-rc-total { color: var(--fg-2); }
.prov-rc-items { display: flex; flex-direction: column; gap: 3px; color: var(--fg-3); font-size: 11px; }
.prov-rc-item { display: flex; align-items: center; gap: 6px; }
.prov-rc-x { border: 0; background: transparent; color: var(--fg-3); cursor: pointer; }
.prov-rc-x:hover { color: var(--err); }

@media (max-width: 900px) {
  .prov-stats { grid-template-columns: 1fr 1fr; }
  .prov-grid { grid-template-columns: 1fr; }
  .prov-item { flex-direction: column; }
  .prov-item-actions { flex-direction: row; flex-wrap: wrap; }
}
</style>
