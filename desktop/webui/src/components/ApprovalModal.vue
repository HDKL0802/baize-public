<!-- 危险操作审批弹窗：置顶确认框（跨端任务 / 工具调用）。
     一次只显示一条（取队首），其余排队。超时由后端按「拒绝」处理，这里如实倒计时。 -->
<script setup>
import { computed, ref, onMounted, onBeforeUnmount, nextTick } from 'vue';
import { NButton, NCheckbox, NTag } from 'naive-ui';
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

onMounted(() => {
  const obs = setInterval(() => {
    if (current.value && current.value.expiresAt) startCountdown(current.value.expiresAt);
  }, 1000);
  onBeforeUnmount(() => { clearInterval(obs); clearInterval(countdownTimer); });
});
</script>

<template>
  <Teleport to="body">
    <div v-if="current" class="am-overlay">
      <div class="am-panel">
        <div class="am-head">
          <NTag :type="isTask ? 'warning' : 'error'" size="small">{{ isTask ? '跨端任务' : '工具调用' }}</NTag>
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
          <div>
            <b>影响范围</b>
            <span v-if="isTask">这条动作会真的下发到设备「{{ current.deviceId || '?' }}」执行</span>
            <span v-else>只影响这台 NAS 上的后端（执行工具「{{ current.tool }}」本身）</span>
          </div>
          <div>
            <b>建议</b>
            <span v-if="isTask">确认这台设备与这个动作是你派的，再放行；拿不准就拒绝</span>
            <span v-else>看清参数是不是你要的；拿不准就拒绝，白泽会当成「你不许这么做」</span>
          </div>
          <div><b>剩余时间</b><span class="am-left">{{ leftText }}</span></div>
        </div>

        <div v-if="!isTask" class="am-remember">
          <NCheckbox v-model:checked="rememberSession">本会话内不再问这个工具</NCheckbox>
          <NCheckbox v-model:checked="rememberAlways">永久放行（写进配置，可撤）</NCheckbox>
        </div>

        <div class="am-actions">
          <NButton @click="decide(false)">拒绝</NButton>
          <NButton type="primary" @click="decide(true)">批准并执行</NButton>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style>
.am-overlay {
  position: fixed; inset: 0; z-index: 9999;
  background: rgba(0,0,0,0.35);
  display: flex; align-items: center; justify-content: center;
}
.am-panel {
  background: var(--card); border-radius: 12px;
  box-shadow: 0 8px 32px rgba(0,0,0,0.25);
  width: 460px; max-width: 92vw; max-height: 86vh; overflow-y: auto;
  padding: 20px;
}
.am-head { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.am-count { font-size: 12px; color: var(--warn); }
.am-title { font-size: 16px; margin: 4px 0 8px; }
.am-desc { color: var(--fg2); font-size: 13px; margin: 4px 0; line-height: 1.5; }
.am-args {
  background: color-mix(in srgb, var(--fg) 4%, transparent);
  border-radius: 8px; padding: 10px; font-family: var(--mono);
  font-size: 12px; color: var(--fg2); max-height: 160px; overflow: auto;
  white-space: pre-wrap; word-break: break-all; margin: 8px 0;
}
.am-meta { display: grid; gap: 6px; font-size: 13px; }
.am-meta b { display: inline-block; min-width: 60px; color: var(--fg2); font-weight: 600; }
.am-left.err { color: var(--err); }
.am-remember { display: flex; flex-direction: column; gap: 6px; margin: 10px 0; }
.am-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
</style>
