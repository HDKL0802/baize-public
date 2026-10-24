<template>
  <div class="chat-view">
    <!-- 顶部 -->
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">白泽智能体</h1>
        <p class="m-page-sub">{{ agentLabel }} · 本地会话</p>
      </div>
      <button class="m-btn ghost" @click="onNewChat">新会话</button>
    </header>

    <!-- 消息区 -->
    <div ref="listEl" class="chat-list">
      <div v-if="!msgs.length" class="m-empty">暂无消息，下方输入一条试试吧</div>
      <div v-for="(m, i) in msgs" :key="i" :class="['msg-row', m.role === 'user' ? 'mine' : 'agent']">
        <!-- 卡片消息 -->
        <div v-if="m.kind === 'card' && m.card" class="msg-card">
          <div class="m-card-title">{{ m.card.title || '确认' }}</div>
          <div class="msg-card-body">{{ m.card.text || m.card.detail || '' }}</div>
          <div v-if="m.pending" class="msg-card-actions">
            <button class="m-btn" @click="onCardOk(m, i)">确认</button>
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
      <div class="chat-input-row">
        <!-- Agent 选择器 -->
        <select class="chat-agent" v-model="agentName">
          <option value="">白泽（默认）</option>
          <option v-for="a in agents" :key="a" :value="a">{{ a }}</option>
        </select>
        <!-- 附件菜单 -->
        <button class="chat-plus" @click="attachMenu = !attachMenu" title="附件">＋</button>
        <!-- 文本输入 -->
        <input
          v-model="draft"
          class="m-input chat-text"
          type="text"
          placeholder="给白泽说点什么…"
          @keyup.enter="onSend"
        />
        <button class="m-btn" :disabled="sending || !draft.trim()" @click="onSend">发送</button>
      </div>
      <!-- 附件菜单弹层 -->
      <div v-if="attachMenu" class="chat-attach-menu">
        <button class="attach-item" @click="onAttach('todo')">添加待办卡片</button>
        <button class="attach-item" @click="onAttach('password')">添加密码卡片</button>
        <button class="attach-item" @click="onAttach('file')">添加文件（暂不支持）</button>
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
  display: flex; flex-direction: column;
  height: calc(100vh - var(--nav-h) - 20px);
}
.chat-list {
  flex: 1; overflow-y: auto;
  display: flex; flex-direction: column; gap: 8px;
  padding: 4px 0 10px;
}
.msg-row { display: flex; }
.msg-row.mine { justify-content: flex-end; }
.msg-row.agent { justify-content: flex-start; }
.msg-bubble {
  max-width: 78%; padding: 8px 12px;
  border-radius: 12px; font-size: 14px; line-height: 1.55;
  position: relative; word-break: break-word;
}
.bubble-user {
  background: var(--accent); color: #fff;
  border-bottom-right-radius: 4px;
}
.bubble-agent {
  background: var(--surface-2); color: var(--fg);
  border: 1px solid var(--line);
  border-bottom-left-radius: 4px;
}
.msg-time { display: block; font-size: 10px; opacity: .6; margin-top: 2px; }
.typing { color: var(--fg-2); }

/* 卡片 */
.msg-card {
  max-width: 85%;
  background: var(--surface); border: 1px solid var(--accent);
  border-radius: 12px; padding: 10px 12px;
}
.msg-card-body { font-size: 13px; color: var(--fg-2); margin-bottom: 8px; word-break: break-word; }
.msg-card-actions { display: flex; gap: 8px; }
.msg-card-result { font-size: 12px; color: var(--fg-3); }

/* 输入区 */
.chat-input {
  position: sticky; bottom: 0;
  background: var(--bg); padding: 8px 0 4px;
}
.chat-input-row { display: flex; align-items: center; gap: 6px; }
.chat-agent {
  max-width: 90px; padding: 8px 6px;
  background: var(--surface-2); border: 1px solid var(--line);
  border-radius: 8px; color: var(--fg); font-size: 12px;
}
.chat-plus {
  width: 34px; height: 38px; flex: none;
  border-radius: 8px; border: 1px solid var(--line-2);
  background: var(--surface-2); color: var(--fg-2);
  font-size: 18px; line-height: 1; cursor: pointer;
}
.chat-text { flex: 1; }
.chat-attach-menu {
  display: flex; flex-direction: column; margin-bottom: 8px;
  background: var(--surface); border: 1px solid var(--line);
  border-radius: 10px; overflow: hidden;
}
.attach-item {
  padding: 10px 14px; font-size: 13px; text-align: left;
  background: none; border: none; color: var(--fg-2); cursor: pointer;
  border-bottom: 1px solid var(--line);
}
.attach-item:last-child { border-bottom: none; }
.attach-item:active { background: var(--surface-2); }
</style>
