<template>
  <div class="plan-view">
    <header class="m-page-head">
      <div class="m-head-text">
        <h1 class="m-page-title">AI 排期</h1>
        <p class="m-page-sub">让白泽按策略生成执行顺序</p>
      </div>
      <button class="m-icon-btn accent" title="开始排期" :disabled="running" @click="onRun">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M8 5.5l10 6.5L8 18.5v-13z"/></svg>
      </button>
    </header>

    <!-- 策略选择 -->
    <div class="m-card">
      <div class="m-card-title">排期策略</div>
      <div class="policy-grid">
        <button
          v-for="p in policies"
          :key="p.key"
          :class="['policy-item', policy === p.key ? 'on' : '']"
          @click="policy = p.key"
        >
          <span class="policy-name">{{ p.name }}</span>
          <span class="policy-desc">{{ p.desc }}</span>
        </button>
      </div>
    </div>

    <!-- 附加要求（可选） -->
    <div class="m-card">
      <div class="m-card-title">附加要求（可选）</div>
      <textarea
        v-model="extra"
        class="m-input plan-extra"
        rows="2"
        placeholder="例如：今晚只处理重要事项"
      ></textarea>
    </div>

    <!-- 开始排期 -->
    <button class="m-btn block plan-go" :disabled="running" @click="onRun">
      {{ running ? '白泽排期中…' : '开始排期' }}
    </button>

    <!-- 空状态 -->
    <div v-if="!result && !err" class="m-empty plan-empty">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="18" r="2"/><circle cx="18" cy="6" r="2"/><path d="M8 18h5a4 4 0 0 0 4-4V8"/></svg>
      <p>选好策略和附加要求后，点上方开始排期</p>
    </div>

    <!-- 结果：竖向时间线 -->
    <div v-if="result" class="m-card plan-result">
      <div class="m-card-title">建议执行顺序</div>
      <div class="timeline">
        <div v-for="(s, i) in result.steps" :key="i" :class="['tl-step', { current: i === 0 }]">
          <span class="tl-dot"></span>
          <div class="tl-body">
            <span class="tl-index mono">{{ stepNo(i) }}</span>
            <span class="tl-text">{{ s }}</span>
          </div>
        </div>
      </div>
      <div v-if="result.summary" class="plan-summary">{{ result.summary }}</div>
    </div>

    <div v-if="err" class="m-card err-card">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5M12 16.2v.3"/></svg>
      {{ err }}
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue';
import { useStore } from '../store.js';
import { agentRun } from '../api.js';

const store = useStore();

const policies = [
  { key: 'balance', name: '均衡', desc: '工作与休息均衡' },
  { key: 'work', name: '工作优先', desc: '尽量压缩工作块' },
  { key: 'create', name: '创作时段', desc: '为深度创作留整块时间' },
  { key: 'life', name: '生活优先', desc: '保证睡眠与运动' },
];

const policy = ref('balance');
const extra = ref('');
const running = ref(false);
const result = ref(null);
const err = ref('');

onMounted(() => {
  const s = store.getSettings();
  if (s.planPolicy) policy.value = s.planPolicy;
});

function stepNo(i) {
  const n = i + 1;
  return n < 10 ? '0' + n : String(n);
}

function parseResult(text) {
  // 简单解析：把 agent 回复按行拆成步骤
  const lines = String(text || '').split('\n').map(s => s.replace(/^\s*[-•*\d.、)\]]+\s*/, '').trim()).filter(Boolean);
  if (!lines.length) return null;
  return {
    steps: lines.slice(0, 12),
    summary: lines.length > 12 ? '（已截取前 12 步）' : '',
  };
}

async function onRun() {
  running.value = true;
  err.value = '';
  result.value = null;
  store.saveSettings({ planPolicy: policy.value });

  const todos = store.getTodos().filter(t => t && !t.done && t.title);
  const parts = ['请按「' + policy.value + '」策略，为我的待办排一个执行顺序：'];
  if (todos.length) {
    parts.push('待办列表：');
    todos.slice(0, 30).forEach(t => parts.push('- ' + t.title + (t.due ? '（截止 ' + t.due + '）' : '')));
  } else {
    parts.push('（暂无未完成的待办，请给一个通用的日程建议）');
  }
  if (extra.value.trim()) parts.push('附加要求：' + extra.value.trim());

  try {
    const r = await agentRun(parts.join('\n'), true);
    if (r.ok) {
      const text = (r.data && (r.data.reply || r.data.text || r.data.result)) || '';
      result.value = parseResult(text) || { steps: [String(text || '（无内容）')], summary: '' };
    } else {
      err.value = '排期失败：' + (r.error || '未知错误');
    }
  } finally {
    running.value = false;
  }
}
</script>

<style scoped>
.plan-view { display: flex; flex-direction: column; }
.m-icon-btn:disabled { opacity: .45; cursor: default; }

.policy-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
}
.policy-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-height: 60px;
  padding: 10px 12px;
  border-radius: 6px;
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--fg-2);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background 120ms var(--ease-in), border-color 120ms var(--ease-in);
}
.policy-item.on {
  background: var(--accent-soft);
  border-color: var(--accent);
}
.policy-name { font-size: 13px; font-weight: 600; color: var(--fg); }
.policy-item.on .policy-name { color: var(--accent); }
.policy-desc { font-size: 11px; color: var(--fg-3); }

.plan-extra { resize: none; }
.plan-go { margin: 2px 0 4px; }
.plan-empty { padding: 28px 0 40px; }

/* 时间线 */
.timeline {
  position: relative;
  padding-left: 24px;
  margin-top: 4px;
}
.timeline::before {
  content: '';
  position: absolute;
  left: 5px; top: 8px; bottom: 14px;
  width: 2px;
  background: var(--line-strong);
}
.tl-step {
  position: relative;
  padding-bottom: 14px;
}
.tl-step:last-child { padding-bottom: 0; }
.tl-dot {
  position: absolute;
  left: -24px; top: 3px;
  width: 12px; height: 12px;
  border-radius: 999px;
  border: 2px solid var(--line-strong);
  background: var(--surface);
}
.tl-step.current .tl-dot {
  border-color: var(--accent);
  background: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-soft);
}
.tl-body {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.tl-index {
  flex: none;
  width: 18px;
  font-size: 11px;
  color: var(--fg-3);
}
.tl-text {
  font-size: 14px;
  line-height: 1.55;
  word-break: break-word;
}
.tl-step.current .tl-index { color: var(--accent); }
.plan-summary { margin-top: 12px; font-size: 11px; color: var(--fg-3); }

.err-card {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  border-color: rgba(240, 115, 106, .45);
  color: var(--err);
  font-size: 13px;
  line-height: 1.55;
}
.err-card svg { flex: none; width: 18px; height: 18px; margin-top: 2px; }
</style>
