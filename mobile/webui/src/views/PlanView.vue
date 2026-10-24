<template>
  <div class="plan-view">
    <header class="m-page-head">
      <div>
        <h1 class="m-page-title">AI 排期</h1>
        <p class="m-page-sub">让白泽按策略生成执行顺序</p>
      </div>
    </header>

    <!-- 策略选择 -->
    <div class="m-card">
      <div class="m-card-title">排期策略</div>
      <div class="policy-row">
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
    <button class="m-btn plan-go" :disabled="running" @click="onRun">
      {{ running ? '白泽排期中…' : '开始排期' }}
    </button>

    <!-- 结果 -->
    <div v-if="result" class="m-card plan-result">
      <div class="m-card-title">建议执行顺序</div>
      <ol class="plan-steps">
        <li v-for="(s, i) in result.steps" :key="i">{{ s }}</li>
      </ol>
      <div v-if="result.summary" class="plan-summary">{{ result.summary }}</div>
    </div>
    <div v-if="err" class="m-card err-card">{{ err }}</div>
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

.policy-row {
  display: grid; grid-template-columns: repeat(2, 1fr); gap: 8px;
}
.policy-item {
  display: flex; flex-direction: column; gap: 2px;
  padding: 10px 12px; border-radius: 8px;
  background: var(--surface-2); border: 1px solid var(--line);
  color: var(--fg-2); text-align: left; cursor: pointer;
}
.policy-item.on { border-color: var(--accent); }
.policy-name { font-size: 13px; font-weight: 600; color: var(--fg); }
.policy-item.on .policy-name { color: var(--accent); }
.policy-desc { font-size: 11px; color: var(--fg-3); }

.plan-extra { resize: none; }
.plan-go {
  justify-content: center; margin: 6px 0 14px;
  min-height: 44px; font-size: 14px;
}
.plan-go:disabled { opacity: .6; }

.plan-steps { margin: 0; padding-left: 20px; }
.plan-steps li {
  font-size: 13px; line-height: 1.8;
  padding: 4px 0; border-bottom: 1px dashed var(--line);
}
.plan-steps li:last-child { border-bottom: none; }
.plan-summary { font-size: 11px; color: var(--fg-3); margin-top: 8px; }

.err-card { color: var(--err); border-color: var(--err); font-size: 13px; }
</style>
