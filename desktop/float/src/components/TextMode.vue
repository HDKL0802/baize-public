<!-- 提取文字 / 翻译：共用一个组件，靠 mode 区分 -->
<script setup>
import { ref, onMounted, onBeforeUnmount, inject } from 'vue';
import { api, visionText, local } from '../api.js';

const props = defineProps({
  mode: { type: String, required: true },   // 'extract' | 'translate'
});
const bus = inject('bus');                    // { rev, lang, setLang }

const result = ref('');
const placeholder = ref('正在等截图…');
const lang = ref('zh');

async function run() {
  placeholder.value = props.mode === 'translate'
    ? (lang.value === 'zh' ? '翻译成中文…（可能几秒）' : 'Translating…')
    : '识别中…（可能几秒）';
  result.value = '';
  const v = await visionText(props.mode === 'translate' ? 'translate' : 'extract', lang.value);
  if (v == null) { placeholder.value = '没拿到截图'; return; }
  if (v.error) { placeholder.value = '失败：' + v.error; return; }
  placeholder.value = '（没有识别到内容）';
  result.value = v.text;
}

// rev 变化（主进程推来新图）→ 重跑
let lastRev = -1;
let onEvent = null;
onMounted(async () => {
  lang.value = bus?.lang?.value || bus?.lang || 'zh';  // bus.lang 是 ref，兜底兼容旧写法
  const m = await local('/float/mode');
  if (m && m.hasImage) await run();

  // 监听 bus 事件（rev 变化 / lang 变化）—— 别在 onMounted 里 return cleanup（Vue3 不认）
  onEvent = (e) => {
    if (e.detail.type === 'rev') {
      if (e.detail.rev !== lastRev) { lastRev = e.detail.rev; run(); }
    }
    if (e.detail.type === 'lang') {
      lang.value = e.detail.lang;
      if (props.mode === 'translate') run();
    }
  };
  window.addEventListener('float-event', onEvent);
});

onBeforeUnmount(() => {
  if (onEvent) window.removeEventListener('float-event', onEvent);
});

const ph = () => result.value ? '' : placeholder.value;

// 复制结果（仅界面侧动作）
const copied = ref(false);
let copyTimer = 0;
async function copyText() {
  if (!result.value) return;
  try {
    await navigator.clipboard.writeText(result.value);
    copied.value = true;
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => { copied.value = false; }, 1200);
  } catch {
    placeholder.value = '复制失败，请手动选中文本';
  }
}
onBeforeUnmount(() => clearTimeout(copyTimer));
</script>

<template>
  <div class="f-pane">
    <div class="f-card">
      <div class="f-cardhead">
        <span class="f-label">{{ props.mode === 'translate' ? '译文 · ' + (lang === 'zh' ? '中文' : 'English') : '识别结果' }}</span>
        <button class="f-cbtn" :disabled="!result" :title="copied ? '已复制' : '复制结果'" @click="copyText">
          <svg class="ic" viewBox="0 0 24 24" aria-hidden="true">
            <rect x="8" y="8" width="13" height="13" rx="2" />
            <path d="M4 16a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2" />
          </svg>
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </button>
      </div>
      <div class="f-result" :data-ph="ph()">{{ result }}</div>
    </div>
  </div>
</template>

<style scoped>
.f-pane { padding: 12px; flex: 1; min-height: 0; display: flex; }
.f-card {
  flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column;
  background: var(--surface-2); border: 1px solid var(--line); border-radius: 6px;
}
.f-cardhead {
  flex: 0 0 auto; display: flex; align-items: center; gap: 8px;
  padding: 6px 8px 6px 12px; border-bottom: 1px solid var(--line);
}
.f-label {
  flex: 1; min-width: 0; font-size: 11px; color: var(--text-3);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.f-cbtn {
  flex: 0 0 auto; height: 32px; min-width: 32px; padding: 0 8px;
  display: inline-flex; align-items: center; gap: 5px;
  border: 0; border-radius: 4px; background: transparent;
  color: var(--text-2); font: inherit; font-size: 11.5px; cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1);
}
.f-cbtn:hover { background: var(--surface-3); color: var(--text); }
.f-cbtn:disabled { color: var(--text-3); opacity: .5; cursor: not-allowed; }
.f-cbtn:disabled:hover { background: transparent; }
.f-cbtn .ic { width: 16px; height: 16px; }
.f-result {
  flex: 1; min-height: 0; overflow-y: auto; overflow-x: hidden;
  white-space: pre-wrap; word-break: break-word; user-select: text;
  padding: 10px 12px; font-size: 13px; line-height: 1.7;
  color: var(--text);
}
.f-result:empty::before { content: attr(data-ph); color: var(--text-3); font-size: 12px; }
</style>
