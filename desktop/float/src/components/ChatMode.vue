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
      <button :class="{ on: tab === 'chat' }" @click="tab = 'chat'">对话</button>
      <button :class="{ on: tab === 'prog' }" @click="tab = 'prog'">进展</button>
    </div>

    <div v-show="tab === 'chat'" class="f-chat-body">
      <div id="chat-msgs" class="f-msgs">
        <div v-for="(m, i) in msgs" :key="i" :class="['f-msg', m.cls]">{{ m.text }}</div>
      </div>
      <div class="f-composer">
        <div class="f-send" style="margin:0 0 8px">
          <button class="fbtn primary" :disabled="busy" @click="send">发送</button>
        </div>
        <textarea v-model="goal" placeholder="说一句要做什么…（Enter 发送，Shift+Enter 换行）"
          @keydown.enter.exact.prevent="send"></textarea>
      </div>
    </div>

    <div v-show="tab === 'prog'" class="f-prog">
      <div class="f-msg ai">
        <span :class="['f-dot', dotCls]"></span>{{ statText }}
      </div>
      <div class="f-prog-list">
        <div v-for="p in progList" :key="p.id || p.runId" class="f-prog-line">
          <span class="g">{{ p.goal || '（无目标）' }}</span>
          <span :class="['s', p.status === 'done' ? 'ok' : p.status === 'failed' ? 'err' : '']">
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
.f-ctabs { display: flex; gap: 4px; padding: 8px 12px 0; }
.f-ctabs button {
  border: 1px solid var(--border); background: var(--surface); color: var(--text-2);
  border-radius: 5px; padding: 4px 12px; font: inherit; font-size: 12px; cursor: pointer;
}
.f-ctabs button.on { background: var(--accent-soft); border-color: var(--accent); color: var(--accent); font-weight: 600; }
.f-chat-body { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.f-msgs { flex: 1; min-height: 0; overflow: auto; padding: 12px; display: flex; flex-direction: column; gap: 9px; }
.f-msg {
  max-width: 88%; border-radius: 8px; padding: 8px 11px; font-size: 13px; line-height: 1.65;
  word-break: break-word; user-select: text;
}
.f-msg.me { align-self: flex-end; background: var(--accent-soft); border: 1px solid var(--border-strong); }
.f-msg.ai { align-self: flex-start; background: var(--surface); border: 1px solid var(--border); }
.f-composer { padding: 10px 12px 12px; border-top: 1px solid var(--border); background: var(--surface); }
.f-composer textarea {
  width: 100%; resize: none; height: 58px; background: var(--bg); color: var(--text);
  border: 1px solid var(--border-strong); border-radius: 6px; padding: 9px 10px; font: inherit;
  font-size: 13px; outline: none; line-height: 1.55;
}
.f-composer textarea:focus { border-color: var(--accent); }
.f-send { display: flex; justify-content: flex-end; margin-top: 8px; }
.fbtn {
  border: 1px solid var(--border-strong); background: var(--surface-2); color: var(--text);
  border-radius: 6px; padding: 5px 12px; font: inherit; font-size: 12.5px; cursor: pointer;
  white-space: nowrap;
}
.fbtn:hover { background: var(--surface-3, var(--surface-2)); }
.fbtn:disabled { opacity: .45; cursor: not-allowed; }
.fbtn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
.f-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--text-3); margin-right: 5px; vertical-align: 1px; }
.f-dot.busy { background: var(--warn); animation: fpulse 1s infinite; }
.f-dot.ok { background: var(--ok); }
.f-dot.err { background: var(--err); }
@keyframes fpulse { 0%,100% { opacity: 1 } 50% { opacity: .3 } }
.f-prog { flex: 1; min-height: 0; overflow: auto; padding: 12px; }
.f-prog-list { display: flex; flex-direction: column; gap: 8px; margin-top: 10px; }
.f-prog-line { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; font-size: 12.5px; line-height: 1.5; }
.f-prog-line .g { color: var(--text); display: block; word-break: break-word; }
.f-prog-line .s { color: var(--text-3); font-size: 11px; }
.f-prog-line .s.ok { color: var(--ok); }
.f-prog-line .s.err { color: var(--err); }
.f-sub { color: var(--text-3); font-size: 11.5px; line-height: 1.5; margin-top: 10px; }
</style>
