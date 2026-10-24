<!-- 对话页：向白泽发指令（替换旧 apps.js 的 renderChat）。
     后端 /api/agent/chat 接收 {text}，回 {reply}。支持多轮上下文（带 session）。 -->
<script setup>
import { ref, onMounted, nextTick } from 'vue';
import { NButton, NSpace, NInput } from 'naive-ui';
import { API } from '../api.js';

function toast(msg, kind) {
  const t = document.createElement('div');
  t.className = 'bz-toast ' + (kind || '');
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3800);
}

const input = ref('');
const busy = ref(false);
const history = ref([]); // {role: 'user'|'assistant', text}
const sessionId = ref(null);
const boxRef = ref(null);

async function send() {
  const text = input.value.trim();
  if (!text || busy.value) return;
  input.value = '';
  history.value.push({ role: 'user', text });
  busy.value = true;
  await nextTick(); scrollBottom();
  try {
    const r = await API.post('/api/agent/chat', { text, session: sessionId.value });
    if (r.ok) {
      history.value.push({ role: 'assistant', text: (r.data && r.data.reply) || r.data.raw || '（空回复）' });
      if (r.data && r.data.sessionId) sessionId.value = r.data.sessionId;
    } else {
      history.value.push({ role: 'assistant', text: '⚠ ' + (r.error || '请求失败'), error: true });
    }
  } catch (e) {
    history.value.push({ role: 'assistant', text: '⚠ ' + String(e), error: true });
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

onMounted(scrollBottom);
</script>

<template>
  <div class="chat">
    <div class="chat-box" ref="boxRef">
      <div v-if="!history.length" class="chat-empty">
        跟白泽说句话试试，比如「整理一下今天的待办」/「查一下这台设备在线的有哪些」。
      </div>
      <div
        v-for="(m, i) in history"
        :key="i"
        class="chat-msg"
        :class="['chat-' + m.role, { err: m.error }]"
      >
        <span class="chat-role">{{ m.role === 'user' ? '你' : '白泽' }}</span>
        <div class="chat-text">{{ m.text }}</div>
      </div>
      <div v-if="busy" class="chat-msg chat-loading"><i class="chat-spinner"></i> 白泽正在思考…</div>
    </div>

    <div class="chat-input">
      <n-input
        v-model:value="input"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 4 }"
        placeholder="输入指令，回车发送（Shift+Enter 换行）"
        @keyup.enter.exact.prevent="send"
      />
      <n-space vertical size="8">
        <n-button type="primary" :loading="busy" @click="send" style="width:100%">发送</n-button>
        <n-button ghost size="small" @click="clear" :disabled="!history.length">清空对话</n-button>
      </n-space>
    </div>
  </div>
</template>

<style>
.chat { display: flex; flex-direction: column; height: 100%; max-width: 860px; margin: 0 auto; }
.chat-box { flex: 1; overflow-y: auto; padding: 6px 2px; display: flex; flex-direction: column; gap: 12px; }
.chat-empty { color: var(--fg3); font-size: 13px; text-align: center; margin-top: 40px; }
.chat-msg { display: flex; gap: 8px; align-items: flex-start; }
.chat-role { font-size: 12px; color: var(--fg3); min-width: 34px; }
.chat-text { white-space: pre-wrap; word-break: break-word; }
.chat-user .chat-role { color: var(--accent); font-weight: 600; }
.chat-msg.err .chat-text { color: var(--err); }
.chat-loading { color: var(--fg3); font-size: 13px; }
.chat-spinner {
  display: inline-block; width: 12px; height: 12px; border-radius: 50%;
  border: 2px solid var(--line); border-top-color: var(--accent);
  animation: chspin .8s linear infinite; margin-right: 6px;
}
@keyframes chspin { to { transform: rotate(360deg); } }
.chat-input { padding: 10px 0 4px; display: flex; flex-direction: column; gap: 6px; }
</style>
