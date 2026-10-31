<!-- 危险操作审批弹窗：置顶确认框（跨端任务 / 工具调用）。
     一次只显示一条（取队首），其余排队。超时由后端按「拒绝」处理，这里如实倒计时。 -->
<script setup>
import { computed, ref, onMounted, onBeforeUnmount, nextTick } from 'vue';
import { NButton, NCheckbox } from 'naive-ui';
import { useConnStore } from '../store/conn.js';
import { fmtTime } from '../api.js';

const conn = useConnStore();
const current = computed(() => conn.approvals[0] || null);
const isTask = computed(() => current.value && current.value.kind === 'task');
const rememberSession = ref(false);
const rememberAlways = ref(false);
const leftText = ref('不限时（等你确认）');
const queueCount = computed(() => Math.max(0, conn.approvals.length - 1));
let countdownTimer = null;
let lastKey = '';

function toast(msg, kind) {
  const t = document.createElement('div');
  t.className = 'bz-toast ' + (kind || '');
  t.textContent = msg;
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3800);
}

function startCountdown(expiresAt) {
  clearInterval(countdownTimer);
  if (!expiresAt) { leftText.value = '不限时（等你确认）'; return; }
  const tick = () => {
    const left = expiresAt - Date.now();
    if (left <= 0) { leftText.value = '已超时（后端按拒绝处理）'; return; }
    const s = Math.floor(left / 1000);
    leftText.value = s >= 60
      ? (Math.floor(s / 60) + ' 分 ' + String(s % 60).padStart(2, '0') + ' 秒')
      : s + ' 秒';
  };
  tick();
  countdownTimer = setInterval(tick, 1000);
}

async function decide(ok) {
  if (!current.value) return;
  const it = current.value;
  const key = it.kind + ':' + it.id;
  if (key === lastKey) return;
  lastKey = key;
  const remember = !ok ? '' : (rememberAlways.value ? 'always' : (rememberSession.value ? 'session' : ''));
  const r = await conn.decideApproval(it.kind, it.id, ok, remember);
  lastKey = '';
  if (!r.ok) toast('操作失败：' + r.error, 'err');
  else if (it.kind === 'agent' && ok && remember === 'always') toast('已批准，并永久放行该工具', 'ok');
  else toast(ok ? '已批准' : '已拒绝', ok ? 'ok' : 'warn');
  rememberSession.value = false;
  rememberAlways.value = false;
  nextTick(() => startCountdown(current.value && current.value.expiresAt));
}

// 回车放行 / Esc 拒绝（输入框内不拦截）
function onKey(e) {
  if (!current.value || e.isComposing) return;
  const tag = (e.target && e.target.tagName) || '';
  if (tag === 'TEXTAREA' || tag === 'INPUT') return;
  if (e.key === 'Enter') { e.preventDefault(); decide(true); }
  else if (e.key === 'Escape') { e.preventDefault(); decide(false); }
}

let obs = null;
onMounted(() => {
  obs = setInterval(() => {
    if (current.value && current.value.expiresAt) startCountdown(current.value.expiresAt);
  }, 1000);
  window.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
  clearInterval(obs);
  clearInterval(countdownTimer);
  window.removeEventListener('keydown', onKey);
});
</script>

<template>
  <Teleport to="body">
    <div v-if="current" class="am-overlay">
      <div class="am-panel" role="dialog" aria-modal="true">
        <div class="am-head">
          <svg class="am-warn" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M12 3.8l9 16.2h-18z" />
            <path d="M12 9.5v4.5" />
            <path d="M12 17.2h.01" />
          </svg>
          <span class="am-kicker">需要你确认</span>
          <span class="am-kind">{{ isTask ? '跨端任务' : '工具调用' }}</span>
          <span v-if="queueCount > 0" class="am-count">还有 {{ queueCount }} 条排队</span>
        </div>

        <h3 class="am-title">
          {{ isTask ? (current.action || '（无动作名）') : (current.tool || '（未知工具）') }}
        </h3>

        <p class="am-desc">
          <template v-if="isTask">
            来源 {{ current.origin || 'agent' }} · 目标设备 {{ current.deviceId || '?' }} · {{ fmtTime(current.createdAt) }}
          </template>
          <template v-else>
            白泽想调用这个工具<template v-if="current.runId"> · 运行 {{ current.runId }}</template>
          </template>
        </p>

        <pre class="am-args">{{ JSON.stringify(current.args || {}, null, 2) }}</pre>

        <div class="am-meta">
          <div class="am-meta-row">
            <span class="am-meta-label">影响范围</span>
            <span v-if="isTask">这条动作会真的下发到设备「{{ current.deviceId || '?' }}」执行</span>
            <span v-else>只影响这台 NAS 上的后端（执行工具「{{ current.tool }}」本身）</span>
          </div>
          <div class="am-meta-row">
            <span class="am-meta-label">建议</span>
            <span v-if="isTask">确认这台设备与这个动作是你派的，再放行；拿不准就拒绝</span>
            <span v-else>看清参数是不是你要的；拿不准就拒绝，白泽会当成「你不许这么做」</span>
          </div>
          <div class="am-meta-row">
            <span class="am-meta-label">剩余时间</span>
            <span class="am-left">{{ leftText }}</span>
          </div>
        </div>

        <div v-if="!isTask" class="am-remember">
          <NCheckbox v-model:checked="rememberSession">本会话内不再问这个工具</NCheckbox>
          <NCheckbox v-model:checked="rememberAlways">永久放行（写进配置，可撤）</NCheckbox>
        </div>

        <div class="am-actions">
          <NButton ghost type="error" @click="decide(false)">否，拒绝</NButton>
          <NButton type="primary" @click="decide(true)">是，放行</NButton>
        </div>
        <div class="am-hint">回车放行，Esc 拒绝；点遮罩不会关闭。</div>
      </div>
    </div>
  </Teleport>
</template>

<style>
.am-overlay {
  position: fixed; inset: 0; z-index: 9999;
  background: rgba(0,0,0,.55);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
  animation: am-fade 250ms cubic-bezier(.2,0,0,1);
}
.am-panel {
  width: 100%; max-width: 520px; max-height: 86vh; overflow-y: auto;
  padding: 20px; border-radius: 10px;
  background: var(--surface); border: 1px solid var(--line-strong);
  box-shadow: 0 8px 28px rgba(0,0,0,.35);
  animation: am-in 250ms cubic-bezier(.2,0,0,1);
}
@keyframes am-fade { from { opacity: 0; } to { opacity: 1; } }
@keyframes am-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }

.am-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; flex-wrap: wrap; }
.am-warn { width: 18px; height: 18px; flex: none; color: var(--warn); }
.am-kicker { font-size: 12px; font-weight: 600; color: var(--fg); letter-spacing: .02em; }
.am-kind {
  padding: 2px 7px; border-radius: 4px;
  background: color-mix(in srgb, var(--warn) 12%, transparent); color: var(--warn);
  font-size: 11px; letter-spacing: .04em;
}
.am-count { margin-left: auto; font-size: 11px; color: var(--fg-3); font-family: var(--mono); }
.am-title { margin: 2px 0 4px; font-size: 16px; font-weight: 600; color: var(--fg); word-break: break-word; }
.am-desc { margin: 4px 0 0; font-size: 12px; color: var(--fg-3); line-height: 1.55; }
.am-args {
  margin: 12px 0; padding: 12px; max-height: 160px; overflow: auto;
  background: var(--surface-2); border: 1px solid var(--line); border-radius: 4px;
  font-family: var(--mono); font-size: 12px; line-height: 1.55; color: var(--fg-2);
  white-space: pre-wrap; word-break: break-all;
}
.am-meta { display: flex; flex-direction: column; gap: 7px; font-size: 12px; color: var(--fg-2); line-height: 1.55; }
.am-meta-row { display: flex; gap: 10px; }
.am-meta-label { flex: none; width: 64px; font-size: 11px; color: var(--fg-3); letter-spacing: .04em; padding-top: 1px; }
.am-left { font-family: var(--mono); font-variant-numeric: tabular-nums; color: var(--fg); }
.am-left.err { color: var(--err); }
.am-remember { display: flex; flex-direction: column; gap: 8px; margin: 14px 0 0; padding-top: 12px; border-top: 1px solid var(--line); font-size: 13px; }
.am-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.am-hint { margin-top: 10px; font-size: 11px; color: var(--fg-3); text-align: right; }
</style>
