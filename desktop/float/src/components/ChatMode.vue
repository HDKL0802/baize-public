<!-- 对话浮窗：聊天 + 进展页签 -->
<script setup>
import { ref, onMounted, onBeforeUnmount, inject, nextTick } from 'vue';
import { api, visionText, local } from '../api.js';

const bus = inject('bus');
const msgs = ref([{ cls: 'ai', text: '你好，我是白泽。说一句话就行。' }]);
const goal = ref('');
const busy = ref(false);
const dotCls = ref('');
const statText = ref('就绪');
const tab = ref('chat');   // chat | prog
const progList = ref([]);

async function send() {
  const text = goal.value.trim();
  if (!text || busy.value) return;
  msgs.value.push({ cls: 'me', text });
  goal.value = '';
  busy.value = true;
  dotCls.value = 'busy';
  statText.value = '已派发，跑着…';
  await nextTick();
  scrollBottom();
  const r = await api('/api/agent/run', 'POST', { goal: text, wait: false });
  if (!r.ok) {
    msgs.value.push({ cls: 'ai', text: '派发失败：' + (r.error || '未知') });
    dotCls.value = 'err'; statText.value = '失败';
    busy.value = false;
    return;
  }
  const d = r.data || {};
  if (d.command) {
    msgs.value.push({ cls: 'ai', text: d.reply || '（命令已执行）' });
    dotCls.value = 'ok'; statText.value = '魔法命令 /' + (d.name || '');
    busy.value = false;
    return;
  }
  const id = d.runId || d.id || '';
  msgs.value.push({ cls: 'ai', text: '已派发' + (id ? ' · ' + id : '') + '，进展页可以盯状态。' });
  busy.value = false;
  await refreshProg();
}

async function refreshProg() {
  const r = await api('/api/agent/runs?limit=8');
  if (!r.ok) return;
  const list = (r.data && (r.data.runs || r.data.items)) || (Array.isArray(r.data) ? r.data : []);
  progList.value = list.slice(0, 8);
  const anyRunning = list.some(x => x.status === 'running');
  dotCls.value = anyRunning ? 'busy' : (list[0] && list[0].status === 'done' ? 'ok' : (list[0] && list[0].status === 'failed' ? 'err' : ''));
  statText.value = anyRunning ? '运行中…' : (list.length ? '当前空闲 · 最近 ' + list[0].status : '就绪');
}

function scrollBottom() {
  const el = document.getElementById('chat-msgs');
  if (el) el.scrollTop = el.scrollHeight;
}

// 「问问白泽」：把截图里的文字读出来填进输入框
async function askPrefill() {
  const v = await visionText('extract');
  if (v && v.text) {
    goal.value = v.text;
    msgs.value.push({ cls: 'ai', text: '已把截图里的文字带进来了，接着问就行。' });
  }
}

onMounted(async () => {
  const m = await local('/float/mode');
  if (m && m.hasImage) await askPrefill();
  refreshProg();
  const t = setInterval(refreshProg, 3000);
  onBeforeUnmount(() => clearInterval(t));
});
</script>

<template>
  <div class="f-chat">
    <div class="f-ctabs">
      <button :class="['f-tab', { on: tab === 'chat' }]" @click="tab = 'chat'">对话</button>
      <button :class="['f-tab', { on: tab === 'prog' }]" @click="tab = 'prog'">进展</button>
    </div>

    <div v-show="tab === 'chat'" class="f-chat-body">
      <div id="chat-msgs" class="f-msgs">
        <div v-for="(m, i) in msgs" :key="i" :class="['f-msg', m.cls]">{{ m.text }}</div>
      </div>
      <div class="f-composer">
        <textarea v-model="goal" rows="1" placeholder="说一句要做什么…（Enter 发送，Shift+Enter 换行）"
          @keydown.enter.exact.prevent="send"></textarea>
        <button class="f-sendbtn" :disabled="busy || !goal.trim()" title="发送" @click="send">
          <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
            <path d="M22 2 11 13" />
            <path d="M22 2 15 22l-4-9-9-4Z" />
          </svg>
        </button>
      </div>
    </div>

    <div v-show="tab === 'prog'" class="f-prog">
      <div class="f-msg ai f-prog-stat">
        <span :class="['f-dot', dotCls]"></span>{{ statText }}
      </div>
      <div v-if="!progList.length" class="hint f-prog-empty">还没有运行记录，在「对话」页发一句话就会出现在这里。</div>
      <div v-else class="f-prog-list">
        <div v-for="p in progList" :key="p.id || p.runId" class="f-prog-line">
          <span class="g">{{ p.goal || '（无目标）' }}</span>
          <span :class="['s', 'mono', p.status === 'done' ? 'ok' : p.status === 'failed' ? 'err' : '']">
            {{ p.status || '?' }} · {{ p.steps ?? '?' }} 步 · {{ p.totalTokens ?? '?' }} token
          </span>
        </div>
      </div>
      <div class="f-sub">每 3 秒刷新一次；这里是「白泽在干什么」，聊天在「对话」页。</div>
    </div>
  </div>
</template>

<style scoped>
.f-chat { flex: 1; min-height: 0; display: flex; flex-direction: column; }

/* 页签：两段式，底 track + 选中 accent-soft */
.f-ctabs {
  flex: 0 0 auto; display: flex; gap: 2px; margin: 12px 12px 0;
  padding: 2px; background: var(--surface-2); border-radius: 4px; width: fit-content;
}
.f-tab {
  height: 26px; padding: 0 14px; border: 0; border-radius: 3px; background: transparent;
  color: var(--text-2); font: inherit; font-size: 12px; cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1);
}
.f-tab:hover { color: var(--text); }
.f-tab.on { background: var(--surface); color: var(--accent); font-weight: 600; }

.f-chat-body { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.f-msgs {
  flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden;
  padding: 12px; display: flex; flex-direction: column; gap: 8px;
}
/* 气泡：助手 surface-2，用户 accent-soft + 左侧 2px accent 条 */
.f-msg {
  max-width: 84%; border-radius: 6px; padding: 8px 10px;
  font-size: 13px; line-height: 1.55; word-break: break-word; user-select: text;
}
.f-msg.me {
  align-self: flex-end; background: var(--accent-soft);
  border-left: 2px solid var(--accent); color: var(--text);
}
.f-msg.ai { align-self: flex-start; background: var(--surface-2); color: var(--text); }

/* 输入区：36px 输入 + 36×36 图标发送 */
.f-composer {
  flex: 0 0 auto; display: flex; align-items: flex-end; gap: 8px;
  padding: 8px 12px 12px; background: var(--surface);
  border-top: 1px solid var(--line);
}
.f-composer textarea {
  flex: 1; min-width: 0; height: 36px; resize: none; overflow-y: auto;
  background: var(--bg); color: var(--text);
  border: 1px solid var(--line-strong); border-radius: 4px;
  padding: 8px 10px; font: inherit; font-size: 13px; line-height: 1.4; outline: none;
  transition: border-color 120ms cubic-bezier(.2,0,0,1);
}
.f-composer textarea::placeholder { color: var(--text-3); }
.f-composer textarea:focus { border-color: var(--accent); }
.f-sendbtn {
  flex: 0 0 auto; width: 36px; height: 36px; display: grid; place-items: center;
  padding: 0; border: 0; border-radius: 4px;
  background: var(--accent); color: var(--accent-ink); cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), opacity 120ms cubic-bezier(.2,0,0,1);
}
.f-sendbtn:hover { background: var(--accent-2); }
.f-sendbtn:disabled { background: var(--surface-3); color: var(--text-3); cursor: not-allowed; }
.f-sendbtn .ic { width: 16px; height: 16px; }

/* 进展页 */
.f-prog { flex: 1; min-height: 0; overflow-y: auto; padding: 12px; }
.f-prog-stat { display: flex; align-items: center; max-width: 100%; }
.f-prog-empty { margin-top: 10px; }
.f-prog-list { display: flex; flex-direction: column; gap: 8px; margin-top: 10px; }
.f-prog-line {
  background: var(--surface-2); border: 1px solid var(--line); border-radius: 6px;
  padding: 8px 10px; font-size: 12.5px; line-height: 1.5;
}
.f-prog-line .g { display: block; color: var(--text); word-break: break-word; }
.f-prog-line .s { display: block; margin-top: 2px; color: var(--text-3); font-size: 11px; }
.f-prog-line .s.ok { color: var(--ok); }
.f-prog-line .s.err { color: var(--err); }
.f-sub { color: var(--text-3); font-size: 11.5px; line-height: 1.5; margin-top: 10px; }

.f-dot {
  display: inline-block; width: 6px; height: 6px; border-radius: 999px;
  background: var(--text-3); margin-right: 6px; flex: 0 0 auto;
}
.f-dot.busy { background: var(--warn); }
.f-dot.ok { background: var(--ok); }
.f-dot.err { background: var(--err); }
</style>
