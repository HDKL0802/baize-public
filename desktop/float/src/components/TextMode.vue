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
onMounted(async () => {
  lang.value = bus?.lang || 'zh';
  const m = await local('/float/mode');
  if (m && m.hasImage) await run();
});

// 监听 bus 事件（rev 变化 / lang 变化）
onMounted(() => {
  const onEvent = (e) => {
    if (e.detail.type === 'rev') {
      if (e.detail.rev !== lastRev) { lastRev = e.detail.rev; run(); }
    }
    if (e.detail.type === 'lang') {
      lang.value = e.detail.lang;
      if (props.mode === 'translate') run();
    }
  };
  window.addEventListener('float-event', onEvent);
  return () => window.removeEventListener('float-event', onEvent);
});

const ph = () => result.value ? '' : placeholder.value;
</script>

<template>
  <div class="f-pane">
    <div class="f-result" :data-ph="ph()">{{ result }}</div>
  </div>
</template>

<style scoped>
.f-pane { padding: 12px; flex: 1; min-height: 0; display: flex; }
.f-result {
  flex: 1; white-space: pre-wrap; word-break: break-word;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 6px; padding: 12px 13px;
  font-size: 13.5px; line-height: 1.8; overflow: auto; user-select: text;
}
.f-result:empty::before { content: attr(data-ph); color: var(--text-3); }
</style>
