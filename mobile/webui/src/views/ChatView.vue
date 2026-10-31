<template>
  <div class="chat-view">
    <!-- 顶部 -->
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 class="m-page-title">白泽智能体</h1>
        <p class="m-page-sub">{{ agentLabel }} · 本地会话</p>
      </div>
      <button class="m-icon-btn accent" title="新会话" @click="onNewChat">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4.5H6.5A2.5 2.5 0 0 0 4 7v9.5A2.5 2.5 0 0 0 6.5 19H16a2.5 2.5 0 0 0 2.5-2.5V11"/><path d="M16.2 4.3l3.5 3.5L12.5 15l-3.8.6.6-3.8 6.9-7.5z"/></svg>
      </button>
    </header>

    <!-- 消息区 -->
    <div ref="listEl" class="chat-list">
      <div v-if="!msgs.length" class="m-empty">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5a8.4 8.4 0 0 1-9 8.4 9.3 9.3 0 0 1-3.9-.8L3 21l1.9-5.1A8.3 8.3 0 0 1 3 11.5 8.4 8.4 0 0 1 11.5 3a8.5 8.5 0 0 1 9.5 8.5z"/></svg>
        <p>还没有对话，在下方说第一句话吧</p>
      </div>
      <div v-for="(m, i) in msgs" :key="i" :class="['msg-row', m.role === 'user' ? 'mine' : 'agent']">
        <!-- 卡片消息 -->
        <div v-if="m.kind === 'card' && m.card" class="msg-card">
          <div class="m-card-title">{{ m.card.title || '确认' }}</div>
          <div class="msg-card-body">{{ m.card.text || m.card.detail || '' }}</div>
          <div v-if="m.pending" class="msg-card-actions">
            <button class="m-btn sm" @click="onCardOk(m, i)">确认</button>
            <button class="m-btn ghost" @click="onCardCancel(m, i)">取消</button>
          </div>
          <div v-else-if="m.cardResult" class="msg-card-result">{{ m.cardResult }}</div>
        </div>
        <!-- 普通气泡 -->
        <div v-else :class="['msg-bubble', m.role === 'user' ? 'bubble-user' : 'bubble-agent']">
          {{ m.text }}
          <span class="msg-time">{{ m.ts ? fmtTime(m.ts) : '' }}</span>
        </div>
      </div>
      <div v-if="sending" class="msg-row agent">
        <div class="msg-bubble bubble-agent typing">白泽思考中…</div>
      </div>
    </div>

    <!-- 输入区 -->
    <div class="chat-input">
      <!-- 附件菜单 -->
      <div v-if="attachMenu" class="chat-attach-menu">
        <button class="attach-item" @click="onAttach('todo')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3.5" y="3.5" width="17" height="17" rx="3"/><path d="M8 12l2.8 2.8L16.5 9"/></svg>
          添加待办卡片
        </button>
        <button class="attach-item" @click="onAttach('password')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="14" r="4"/><path d="M11 11l8-8M16 4l3 3M14 6l2.5 2.5"/></svg>
          添加密码卡片
        </button>
        <button class="attach-item" @click="onAttach('file')">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>
          添加文件（暂不支持）
        </button>
      </div>
      <div class="chat-input-row">
        <button class="chat-tool" title="附件" @click="attachMenu = !attachMenu">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
        </button>
        <input
          v-model="draft"
          class="m-input chat-text"
          type="text"
          placeholder="给白泽说点什么…"
          @keyup.enter="onSend"
        />
        <button class="chat-send" title="发送" :disabled="sending || !draft.trim()" @click="onSend">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5.5 11.5L12 5l6.5 6.5"/></svg>
        </button>
      </div>
      <div class="chat-foot">
        <select class="chat-agent" v-model="agentName">
          <option value="">白泽（默认）</option>
          <option v-for="a in agents" :key="a" :value="a">{{ a }}</option>
        </select>
        <span class="chat-hint">Enter 发送</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, nextTick } from 'vue';
import { useStore } from '../store.js';
import { agentRun, uid, fmtTime } from '../api.js';

const store = useStore();

const msgs = computed(() => store.getChat());
const agents = computed(() => store.settings.chatAgents || []);
const agentName = ref('');
const draft = ref('');
const sending = ref(false);
const attachMenu = ref(false);
const listEl = ref(null);

const agentLabel = computed(() => agentName.value || '白泽');

function scrollBottom() {
  nextTick(() => {
    if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight;
  });
}

watch(msgs, scrollBottom, { deep: true, immediate: true });

function pushMsg(m) {
  store.saveChat(msgs.value.concat(m));
}

async function onSend() {
  const text = draft.value.trim();
  if (!text || sending.value) return;
  draft.value = '';
  attachMenu.value = false;
  pushMsg({ id: uid(), role: 'user', text, ts: Date.now() });
  await runAgent(text);
}

async function runAgent(goal) {
  sending.value = true;
  try {
    const r = await agentRun(goal, true, agentName.value || undefined);
    if (r.ok) {
      const reply = (r.data && (r.data.reply || r.data.text || r.data.result)) || '（白泽已处理，没有返回文字）';
      pushMsg({ id: uid(), role: 'agent', text: String(reply), ts: Date.now() });
    } else {
      pushMsg({ id: uid(), role: 'agent', text: '出错了：' + (r.error || '未知'), ts: Date.now(), err: true });
    }
  } finally {
    sending.value = false;
  }
}

function onNewChat() {
  store.newChat();
}

/* ---------- 卡片消息 ---------- */
function onAttach(kind) {
  attachMenu.value = false;
  if (kind === 'todo') {
    pushMsg({
      id: uid(), role: 'agent', kind: 'card', ts: Date.now(), pending: true,
      card: { type: 'todo', title: '要加一条待办', text: '「' + (draft.value || '新待办') + '」', detail: '' },
    });
    draft.value = '';
  } else if (kind === 'password') {
    pushMsg({
      id: uid(), role: 'agent', kind: 'card', ts: Date.now(), pending: true,
      card: { type: 'password', title: '要存一条密码', text: '「' + (draft.value || '新密码条目') + '」', detail: '' },
    });
    draft.value = '';
  } else {
    pushMsg({ id: uid(), role: 'agent', text: '文件附件功能开发中，敬请期待。', ts: Date.now() });
  }
}

function onCardOk(m, i) {
  m.pending = false;
  m.cardResult = '已确认（演示）';
  store.saveChat(msgs.value);
  if (m.card.type === 'todo') {
    // 顺带把内容落到待办里
    const list = store.getTodos();
    list.push({ id: uid(), title: (m.card.text || '').replace(/^「|」$/g, '') || '新待办', done: false, ts: Date.now(), owner: '我' });
    store.saveTodos(list);
  }
}

function onCardCancel(m, i) {
  m.pending = false;
  m.cardResult = '已取消';
  store.saveChat(msgs.value);
}

onMounted(() => {
  store.loadChats();
});
</script>

<style scoped>
.chat-view {
  display: flex;
  flex-direction: column;
  height: calc(100vh - var(--nav-h) - env(safe-area-inset-bottom) - 22px);
  height: calc(100dvh - var(--nav-h) - env(safe-area-inset-bottom) - 22px);
}
.chat-list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 2px 0 8px;
}
.msg-row { display: flex; }
.msg-row.mine { justify-content: flex-end; }
.msg-row.agent { justify-content: flex-start; }
.msg-bubble {
  max-width: 82%;
  padding: 10px 12px;
  border-radius: 6px;
  font-size: 14px;
  line-height: 1.55;
  word-break: break-word;
}
.bubble-user {
  background: var(--accent-soft);
  border-left: 2px solid var(--accent);
  color: var(--fg);
}
.bubble-user .msg-time { text-align: right; }
.bubble-agent {
  background: var(--surface-2);
  border: 1px solid var(--line);
  color: var(--fg);
}
.msg-time {
  display: block;
  margin-top: 4px;
  font-size: 10px;
  color: var(--fg-3);
}
.typing { color: var(--fg-2); }

/* 卡片消息 */
.msg-card {
  max-width: 82%;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 6px;
  padding: 14px;
}
.msg-card .m-card-title { margin-bottom: 6px; }
.msg-card-body {
  font-size: 13px;
  color: var(--fg-2);
  margin-bottom: 10px;
  word-break: break-word;
}
.msg-card-actions { display: flex; gap: 8px; }
.msg-card-result { font-size: 11px; color: var(--fg-3); }

/* 输入区 */
.chat-input { flex: none; padding-top: 8px; }
.chat-input-row { display: flex; align-items: center; gap: 8px; }
.chat-tool {
  flex: none;
  width: 44px; height: 44px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border-radius: 4px;
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--fg-2);
  cursor: pointer;
  transition: background 120ms var(--ease-in), color 120ms var(--ease-in);
}
.chat-tool:active { background: var(--surface-3); color: var(--fg); }
.chat-tool svg { width: 18px; height: 18px; }
.chat-text { flex: 1; min-width: 0; }
.chat-send {
  flex: none;
  width: 44px; height: 44px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: none;
  border-radius: 4px;
  background: var(--accent);
  color: var(--accent-ink);
  cursor: pointer;
  transition: background 120ms var(--ease-in), opacity 120ms var(--ease-in);
}
.chat-send:active { background: var(--accent-2); }
.chat-send:disabled { opacity: .45; cursor: default; }
.chat-send svg { width: 20px; height: 20px; }
.chat-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 6px;
}
.chat-agent {
  height: 44px;
  max-width: 150px;
  padding: 0 8px;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: 4px;
  color: var(--fg-2);
  font: inherit;
  font-size: 12px;
  outline: none;
}
.chat-agent:focus { border-color: var(--accent); }
.chat-hint { font-size: 11px; letter-spacing: .04em; color: var(--fg-3); }

/* 附件菜单 */
.chat-attach-menu {
  display: flex;
  flex-direction: column;
  margin-bottom: 8px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: 6px;
  overflow: hidden;
}
.attach-item {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 52px;
  padding: 0 14px;
  background: none;
  border: none;
  border-bottom: 1px solid var(--line);
  color: var(--fg);
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.attach-item:last-child { border-bottom: none; }
.attach-item svg { width: 18px; height: 18px; color: var(--fg-3); flex: none; }
.attach-item:active { background: var(--surface-2); }
</style>
