<!-- 占位页：尚未实装的页面统一显示此（与旧 renderSoon 对齐）。 -->
<script setup>
import { useRoute } from 'vue-router';
import { VIEW_META } from '../router';
const route = useRoute();
const title = (route.meta && route.meta.title) || route.name || '未命名';
</script>

<template>
  <div class="ph">
    <h3>{{ title }}</h3>
    <div class="ph-sub">这一块排在 D2：先跑通外壳 + 对话/设备/设置/任务，再逐个接上控制台的功能。</div>
    <div class="ph-empty">接口已经就绪（Go 侧统一代理 /api/be/*），接上去只是画界面的事。</div>
  </div>
</template>

<style>
.ph { max-width: 640px; margin: 20px auto; }
.ph h3 { margin: 0 0 6px; }
.ph-sub { color: var(--fg2); font-size: 13px; margin-bottom: 10px; }
.ph-empty {
  padding: 20px; border: 1px dashed var(--line); border-radius: 10px;
  color: var(--fg3); font-size: 13px; text-align: center;
}
</style>
