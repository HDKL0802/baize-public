<!-- 对话页：向白泽发指令（替换旧 apps.js 的 renderChat）。
     后端 /api/agent/chat 接收 {text}，回 {reply}。支持多轮上下文（带 session）。 -->
<script setup>
import { ref, onMounted, nextTick } from 'vue';
import { NButton, NInput } from 'naive-ui';
import { API } from '../api.js';

const input = ref('');
const busy = ref(false);
const history = ref([]); // {role: 'user'|'assistant', text, at}
const sessionId = ref(null);
const boxRef = ref(null);

function nowText() {
  const d = new Date(), p = n => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes());
}

async function send() {
  const text = input.value.trim();
  if (!text || busy.value) return;
  input.value = '';
  history.value.push({ role: 'user', text, at: nowText() });
  busy.value = true;
  await nextTick(); scrollBottom();
  try {
    const r = await API.post('/api/agent/chat', { text, session: sessionId.value });
    if (r.ok) {
      history.value.push({ role: 'assistant', text: (r.data && r.data.reply) || r.data.raw || '（空回复）', at: nowText() });
      if (r.data && r.data.sessionId) sessionId.value = r.data.sessionId;
    } else {
      history.value.push({ role: 'assistant', text: '请求失败：' + (r.error || '未知错误'), error: true, at: nowText() });
    }
  } catch (e) {
    history.value.push({ role: 'assistant', text: '请求失败：' + String(e), error: true, at: nowText() });
  } finally {
    busy.value = false;
    await nextTick(); scrollBottom();
  }
}

function scrollBottom() {
  if (boxRef.value) boxRef.value.scrollTop = boxRef.value.scrollHeight;
}

function clear() {
  history.value = [];
  sessionId.value = null;
}

function fillSample() {
  input.value = '整理一下今天的待办';
}

onMounted(scrollBottom);
</script>

<template>
  <div class="bz-page chat-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">对话</h1>
        <p class="bz-sub">直接说要做的事；跨端任务会先停下来等你审批。</p>
      </div>
      <div class="bz-head-actions">
        <NButton size="small" :disabled="!history.length" @click="clear">清空对话</NButton>
      </div>
    </header>

    <div class="chat-box" ref="boxRef">
      <div v-if="!history.length" class="bz-empty chat-empty">
        <svg class="bz-empty-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v9A1.5 1.5 0 0 1 18.5 16H9l-4 4v-4A1.5 1.5 0 0 1 4 14.5Z" />
          <path d="M8.5 9h7" />
          <path d="M8.5 12.5h4" />
        </svg>
        <div class="bz-empty-title">开始和白泽对话</div>
        <div class="bz-empty-desc">可以试试「整理一下今天的待办」，或者问「这台设备在线的有哪些」。</div>
        <NButton size="small" @click="fillSample">填入示例指令</NButton>
      </div>

      <div
        v-for="(m, i) in history"
        :key="i"
        class="chat-msg"
        :class="['chat-' + m.role, { err: m.error }]"
      >
        <div class="chat-meta">
          <span>{{ m.role === 'user' ? '你' : '白泽' }}</span>
          <span class="chat-time">{{ m.at }}</span>
        </div>
        <div class="chat-text">{{ m.text }}</div>
      </div>

      <div v-if="busy" class="chat-msg chat-assistant">
        <div class="chat-meta"><span>白泽</span><span class="chat-time">思考中</span></div>
        <div class="chat-thinking"><i class="chat-spinner"></i>正在处理你的指令…</div>
      </div>
    </div>

    <div class="chat-compose">
      <NInput
        v-model:value="input"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 4 }"
        placeholder="输入指令，回车发送（Shift+Enter 换行）"
        @keyup.enter.exact.prevent="send"
      />
      <NButton type="primary" :loading="busy" @click="send">发送</NButton>
    </div>
  </div>
</template>

<style>
.chat-page { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.chat-box { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 14px; padding: 2px 2px 16px; }
.chat-empty { margin: auto 0; }

.chat-msg { max-width: 86%; padding: 10px 14px; border-radius: 6px; border-left: 2px solid transparent; }
.chat-user { align-self: flex-end; background: var(--accent-soft); border-left-color: var(--accent); }
.chat-assistant { align-self: flex-start; background: var(--surface-2); }
.chat-meta {
  display: flex; align-items: center; gap: 8px; margin-bottom: 4px;
  font-family: var(--mono); font-size: 11px; color: var(--fg-3); letter-spacing: .02em;
}
.chat-time { font-variant-numeric: tabular-nums; }
.chat-text { font-size: 14px; line-height: 1.6; color: var(--fg); white-space: pre-wrap; word-break: break-word; }
.chat-msg.err { border-left-color: var(--err); }
.chat-msg.err .chat-text { color: var(--err); }
.chat-thinking { font-size: 14px; color: var(--fg-2); }
.chat-spinner {
  display: inline-block; width: 12px; height: 12px; margin-right: 8px; vertical-align: -1px;
  border-radius: 50%; border: 2px solid var(--line-strong); border-top-color: var(--accent);
  animation: chspin .8s linear infinite;
}
@keyframes chspin { to { transform: rotate(360deg); } }

.chat-compose {
  flex: none; display: flex; align-items: flex-end; gap: 8px;
  padding-top: 12px; border-top: 1px solid var(--line);
}
.chat-compose .n-input { flex: 1; }
.chat-compose .n-input .n-input-wrapper { min-height: 36px; align-items: center; }
.chat-compose .n-input textarea { padding-top: 8px; padding-bottom: 8px; }
</style>
