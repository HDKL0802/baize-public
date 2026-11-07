<!-- 消息渠道页：接 /api/agent/channels（清单 / 保存 / 启停 / 删除 / 测试发送）。
     入站回调与 WebSocket 是后端已有接口，界面只展示地址与令牌要求，不伪造"已连通"。 -->
<script setup>
import { ref, computed, onMounted } from 'vue';
import { NButton, NInput, NSwitch } from 'naive-ui';
import { API } from '../api.js';

const channels = ref([]);
const kinds = ref([]);
const formats = ref([]);
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

const kindLabel = {
  webhook: 'Webhook（通用出站 / 入站回调）',
  onebot: 'OneBot（QQ 等，反向 WS）',
  feishu: '飞书',
  dingtalk: '钉钉',
  yuanbao: '腾讯元宝',
  wechat: '个人微信（iLink）',
  xiaoyi: '小艺',
};
function kindName(k) { return kindLabel[k] || k; }

const enabledCount = computed(() => channels.value.filter(c => c.enabled).length);

async function load() {
  loading.value = true;
  const r = await API.get('/api/agent/channels');
  loading.value = false;
  if (!r.ok) { toast(r.error || '读取频道失败', 'err'); return; }
  const d = r.data || {};
  channels.value = d.channels || [];
  kinds.value = d.kinds || [];
  formats.value = d.formats || [];
}

/* ---------- 表单 ---------- */
const form = ref(null);
const formBusy = ref(false);

function blankForm() {
  return {
    id: '', kind: 'webhook', enabled: true, token: '', outboundUrl: '', format: '',
    botPrefix: '', appId: '', appSecret: '', agentId: '', encryptKey: '', domain: '',
    chatIdsText: '', pollSec: 0, hasToken: false,
  };
}

function edit(c) {
  form.value = {
    id: c.id, kind: c.kind || 'webhook', enabled: !!c.enabled, token: '',
    outboundUrl: c.outboundUrl || '', format: c.format || '', botPrefix: c.botPrefix || '',
    appId: c.appId || '', appSecret: '', agentId: c.agentId || '', encryptKey: '',
    domain: c.domain || '', chatIdsText: (c.chatIds || []).join(', '), pollSec: c.pollSec || 0,
    hasToken: !!c.hasToken,
  };
}

function kindNeeds(k) { return {
  outbound: ['webhook', 'yuanbao'].includes(k),
  token: ['webhook', 'onebot', 'feishu', 'xiaoyi'].includes(k),
  app: ['feishu', 'dingtalk', 'yuanbao', 'xiaoyi'].includes(k),
  domain: ['feishu', 'dingtalk', 'wechat', 'yuanbao', 'xiaoyi'].includes(k),
  chats: ['feishu', 'dingtalk'].includes(k),
  encrypt: ['feishu'].includes(k),
}; }

async function saveChannel() {
  const f = form.value;
  if (!f.id.trim()) { toast('频道 id 不能为空', 'warn'); return; }
  const body = {
    action: 'save', id: f.id.trim(), kind: f.kind, enabled: !!f.enabled,
    outboundUrl: f.outboundUrl.trim(), format: f.format, botPrefix: f.botPrefix,
    appId: f.appId.trim(), agentId: f.agentId.trim(), domain: f.domain.trim(),
    chatIds: String(f.chatIdsText || '').split(/[,，\s]+/).map(s => s.trim()).filter(Boolean),
    pollSec: Number(f.pollSec) || 0,
  };
  // 敏感字段留空 = 不改动（后端只在字段出现时才覆盖）
  if (f.token.trim()) body.token = f.token.trim();
  if (f.appSecret.trim()) body.appSecret = f.appSecret.trim();
  if (f.encryptKey.trim()) body.encryptKey = f.encryptKey.trim();
  formBusy.value = true;
  const r = await API.post('/api/agent/channels', body);
  formBusy.value = false;
  if (!r.ok) { toast(r.error || '保存失败', 'err'); return; }
  channels.value = (r.data && r.data.channels) || [];
  form.value = null;
  toast('已保存频道 ' + f.id, 'ok');
}

/* ---------- 单条操作 ---------- */
async function act(action, c, extra) {
  busy.value = action + ':' + (c ? c.id : '');
  const r = await API.post('/api/agent/channels', Object.assign({ action, id: c ? c.id : '' }, extra || {}));
  busy.value = '';
  if (!r.ok) { toast(r.error || '操作失败', 'err'); return null; }
  return r.data || {};
}

async function toggle(c) {
  const d = await act('toggle', c);
  if (d) { channels.value = d.channels || []; toast((c.enabled ? '已停用 ' : '已启用 ') + c.id, 'ok'); }
}

async function removeChannel(c) {
  if (!window.confirm(`删除频道「${c.id}」？凭据也会一起删掉。`)) return;
  const d = await act('remove', c);
  if (d) { channels.value = d.channels || []; toast('已删除 ' + c.id, 'ok'); }
}

async function testChannel(c) {
  const d = await act('test', c, { text: '白泽频道连通性测试。' });
  if (d) toast('测试消息已发出：' + (d.sent || c.id), 'ok');
}

function inboundHint(c) {
  const base = location.origin;
  if (c.kind === 'onebot') return `WS：${base}/api/channels/${c.id}/ws（连入时带频道令牌）`;
  if (c.kind === 'feishu' && (c.chatIds || []).length) return `轮询模式：白泽主动拉取会话 ${c.chatIds.join(', ')}`;
  return `回调：${base}/api/channels/${c.id}/inbound（请求头 X-Baize-Channel-Token）`;
}

onMounted(load);
</script>

<template>
  <div class="bz-page ch-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">消息渠道</h1>
        <p class="bz-sub">把白泽接到你已经在用的聊天软件。出站回复与入站回调都由后端处理，这里管配置、凭据和连通性。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :loading="loading" @click="load">刷新</NButton>
        <NButton size="small" type="primary" @click="form = blankForm()">新增频道</NButton>
      </div>
    </header>

    <div class="ch-stats">
      <div class="ch-stat"><span class="ch-v">{{ channels.length }}</span><span class="ch-l">已配置频道</span></div>
      <div class="ch-stat"><span class="ch-v">{{ enabledCount }}</span><span class="ch-l">已启用</span></div>
      <div class="ch-stat"><span class="ch-v">{{ kinds.length }}</span><span class="ch-l">支持类型</span></div>
    </div>

    <section v-if="form" class="bz-card ch-form">
      <div class="ch-form-head">
        <h2 class="ch-form-title">{{ form.id ? '编辑：' + form.id : '新增频道' }}</h2>
        <button class="ch-x" @click="form = null">×</button>
      </div>
      <div class="ch-grid">
        <label class="ch-field"><span>频道 ID（唯一）</span><NInput v-model:value="form.id" size="small" placeholder="例如 feishu-work" /></label>
        <label class="ch-field"><span>类型</span>
          <select v-model="form.kind" class="ch-select">
            <option v-for="k in kinds" :key="k" :value="k">{{ kindName(k) }}</option>
          </select>
        </label>
      </div>

      <label v-if="kindNeeds(form.kind).outbound" class="ch-field">
        <span>{{ form.kind === 'yuanbao' ? '接入点（ws/wss，可空用官方默认）' : '出站地址（回复发到这里）' }}</span>
        <NInput v-model:value="form.outboundUrl" size="small" :placeholder="form.kind === 'yuanbao' ? 'wss://...' : 'https://...'" />
      </label>

      <div class="ch-grid">
        <label v-if="kindNeeds(form.kind).token" class="ch-field">
          <span>入站令牌{{ form.hasToken ? '（已设置，留空不改）' : '' }}</span>
          <NInput v-model:value="form.token" size="small" type="password" show-password-on="click" placeholder="可空" />
        </label>
        <label v-if="form.kind === 'webhook' || form.kind === 'onebot'" class="ch-field"><span>出站格式</span>
          <select v-model="form.format" class="ch-select">
            <option value="">默认</option>
            <option v-for="f in formats" :key="f" :value="f">{{ f }}</option>
          </select>
        </label>
      </div>

      <div v-if="kindNeeds(form.kind).app" class="ch-grid">
        <label class="ch-field"><span>{{ form.kind === 'dingtalk' ? 'Client ID（AppKey）' : 'App ID' }}</span><NInput v-model:value="form.appId" size="small" /></label>
        <label class="ch-field"><span>App Secret（留空不改）</span><NInput v-model:value="form.appSecret" size="small" type="password" show-password-on="click" /></label>
      </div>
      <div v-if="form.kind === 'xiaoyi'" class="ch-grid">
        <label class="ch-field"><span>Agent ID</span><NInput v-model:value="form.agentId" size="small" /></label>
      </div>

      <div v-if="kindNeeds(form.kind).chats" class="ch-grid">
        <label class="ch-field"><span>轮询会话 ID（逗号分隔，免公网）</span><NInput v-model:value="form.chatIdsText" size="small" placeholder="oc_xxx, oc_yyy" /></label>
        <label class="ch-field"><span>轮询间隔秒数（0 = 默认）</span><NInput v-model:value="form.pollSec" size="small" /></label>
      </div>

      <div v-if="kindNeeds(form.kind).domain" class="ch-grid">
        <label class="ch-field"><span>自定义域名（可空 = 官方默认）</span><NInput v-model:value="form.domain" size="small" placeholder="https://open.feishu.cn" /></label>
        <label v-if="kindNeeds(form.kind).encrypt" class="ch-field"><span>事件加密 Key（可空）</span><NInput v-model:value="form.encryptKey" size="small" /></label>
        <label v-if="form.kind === 'webhook'" class="ch-field"><span>消息前缀（可空）</span><NInput v-model:value="form.botPrefix" size="small" /></label>
      </div>

      <div class="ch-switches">
        <span class="ch-switch"><NSwitch v-model:value="form.enabled" size="small" /> 保存后启用</span>
      </div>
      <div class="ch-form-foot">
        <span class="ch-note">各类型必填项由后端校验，缺了会被直接拒绝。</span>
        <NButton size="small" type="primary" :loading="formBusy" @click="saveChannel">保存</NButton>
      </div>
    </section>

    <section class="bz-card ch-list">
      <div v-if="!loading && !channels.length" class="bz-empty">
        <div class="bz-empty-title">还没有消息频道</div>
        <div class="bz-empty-desc">飞书可以走轮询会话（不需要公网）；Webhook / OneBot 适合自建桥。点右上角新建。</div>
      </div>
      <div v-for="c in channels" :key="c.id" class="ch-item">
        <div class="ch-item-main">
          <div class="ch-item-head">
            <b class="ch-item-name">{{ c.id }}</b>
            <span class="ch-badge">{{ kindName(c.kind) }}</span>
            <span class="ch-badge" :class="c.enabled ? 'ok' : ''">{{ c.enabled ? '已启用' : '已停用' }}</span>
            <span v-if="!c.hasToken" class="ch-badge">无令牌</span>
          </div>
          <div v-if="c.outboundUrl" class="ch-item-meta ch-mono">{{ c.outboundUrl }}</div>
          <div class="ch-item-hint">{{ inboundHint(c) }}</div>
          <div v-if="c.appId" class="ch-item-meta">AppID {{ c.appId }}{{ c.agentId ? ' · AgentID ' + c.agentId : '' }}</div>
        </div>
        <div class="ch-item-actions">
          <NButton size="tiny" :loading="busy === 'test:' + c.id" @click="testChannel(c)">发测试消息</NButton>
          <NButton size="tiny" @click="toggle(c)">{{ c.enabled ? '停用' : '启用' }}</NButton>
          <NButton size="tiny" @click="edit(c)">编辑</NButton>
          <button class="ch-del" @click="removeChannel(c)">删除</button>
        </div>
      </div>
    </section>

    <div v-if="toastMsg" class="bz-toast" :class="toastTone">{{ toastMsg }}</div>
  </div>
</template>

<style>
.ch-page { display: flex; flex-direction: column; }
.ch-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1px; background: var(--line); border: 1px solid var(--line); margin-bottom: 14px; }
.ch-stat { background: var(--surface); padding: 10px 14px; display: flex; flex-direction: column; gap: 2px; }
.ch-v { font-family: var(--mono); font-size: 16px; color: var(--fg); }
.ch-l { font-size: 11px; color: var(--fg-3); letter-spacing: .06em; }
.ch-form { display: flex; flex-direction: column; gap: 10px; margin-bottom: 14px; }
.ch-form-head { display: flex; align-items: center; justify-content: space-between; }
.ch-form-title { margin: 0; font-size: 13px; font-weight: 600; }
.ch-x { border: 0; background: transparent; color: var(--fg-3); font-size: 18px; cursor: pointer; line-height: 1; }
.ch-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.ch-field { display: flex; flex-direction: column; gap: 5px; font-size: 11px; color: var(--fg-3); }
.ch-select { height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: var(--surface-2); color: var(--fg); font-family: inherit; font-size: 13px; }
.ch-switches { display: flex; gap: 18px; }
.ch-switch { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--fg-2); }
.ch-form-foot { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.ch-note { font-size: 11px; color: var(--fg-3); }
.ch-list { padding: 0; overflow: hidden; }
.ch-item { display: flex; gap: 12px; justify-content: space-between; padding: 14px; border-bottom: 1px solid var(--line); }
.ch-item:last-child { border-bottom: 0; }
.ch-item-main { min-width: 0; flex: 1; }
.ch-item-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.ch-item-name { font-size: 13px; color: var(--fg); }
.ch-badge { padding: 1px 6px; border: 1px solid var(--line); border-radius: 2px; font-family: var(--mono); font-size: 10px; color: var(--fg-3); }
.ch-badge.ok { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 40%, transparent); }
.ch-item-meta { margin-top: 5px; font-size: 11px; color: var(--fg-3); word-break: break-all; }
.ch-item-hint { margin-top: 6px; font-size: 11px; color: var(--fg-2); font-family: var(--mono); word-break: break-all; }
.ch-item-actions { display: flex; flex-direction: column; gap: 6px; flex: none; align-items: stretch; }
.ch-del { height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 2px; background: transparent; color: var(--fg-3); font-family: inherit; font-size: 12px; cursor: pointer; }
.ch-del:hover { border-color: var(--err); color: var(--err); }
.ch-mono { font-family: var(--mono); }

@media (max-width: 900px) {
  .ch-grid { grid-template-columns: 1fr; }
  .ch-item { flex-direction: column; }
  .ch-item-actions { flex-direction: row; flex-wrap: wrap; }
}
</style>
