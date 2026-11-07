<!-- 占位页：尚未实装的页面统一显示此（与旧 renderSoon 对齐）。 -->
<script setup>
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NButton } from 'naive-ui';

const route = useRoute();
const router = useRouter();
const title = computed(() => (route.meta && route.meta.title) || route.name || '未命名');

// 已接线的页面不会走到这里；这个兜底只给未登记的 id 用
const desc = computed(() => '这个页面还没有登记实现；如果是导航里的功能，说明接线漏了。');
</script>

<template>
  <div class="bz-page ph-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">{{ title }}</h1>
        <p class="bz-sub">功能尚未实现</p>
      </div>
    </header>

    <section class="bz-card">
      <div class="bz-empty">
        <svg class="bz-empty-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor"
             stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M4 4.5h6.5v6.5H4z" />
          <path d="M13.5 4.5H20v6.5h-6.5z" />
          <path d="M4 13h6.5v6.5H4z" />
          <path d="M13.5 13H20v6.5h-6.5z" />
        </svg>
        <div class="bz-empty-title">「{{ title }}」尚未实现</div>
        <div class="bz-empty-desc">{{ desc }}</div>
        <div class="ph-note">这一块确实还没做，不做占位伪装。</div>
        <NButton size="small" @click="router.push('/chat')">返回对话</NButton>
      </div>
    </section>
  </div>
</template>

<style>
.ph-page { max-width: 720px; }
.ph-note { margin-top: 10px; font-family: var(--mono); font-size: 11px; color: var(--fg-3); }
</style>
