<!-- 占位页：尚未实装的页面统一显示此（与旧 renderSoon 对齐）。 -->
<script setup>
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NButton } from 'naive-ui';
import { VIEW_META } from '../router';

const route = useRoute();
const router = useRouter();
const id = computed(() => (route.name ? String(route.name) : ''));
const title = computed(() => (route.meta && route.meta.title) || route.name || '未命名');

// 各模块规划中的用途说明
const DESC = {
  files: '浏览、上传与整理这台电脑上的文件，把常用路径固化成快捷入口。',
  browser: '在受控浏览器里打开网页、截图并执行页面操作，结果回传到对话里。',
  kb: '沉淀文档与知识条目，检索后再交给白泽引用作答。',
  memory: '把重要的结论与偏好存成记忆，跨设备保持一致。',
  activity: '按时间回看白泽做过的每一步，含任务、工具调用与审批记录。',
  accounts: '多用户与共享空间的账号管理，控制谁能看到哪些数据。',
  conflicts: '同一份数据被多端同时修改时，在这里看差异并选择保留版本。',
  providers: '配置模型通道与密钥，给不同任务分配不同的模型。',
  mcp: '接入 MCP 服务，把外部工具能力挂到白泽身上。',
  external: '登记外部 Agent 与回调地址，让第三方也能接进调度。',
  channels: '把白泽接到消息渠道，在对话之外也能派活。',
  skills: '管理可复用的技能脚本，按需启用或停用。',
  plugins: '浏览与安装插件，扩展白泽的能力边界。',
  persona: '编辑白泽的人设、语气与默认行为。',
  cron: '按时间规则自动执行任务，失败会自动重试或转人工。',
  backup: '备份与恢复配置、记忆与任务历史。',
  observe: '查看健康状态、延迟与错误分布，定位问题更快。',
};
const desc = computed(() => DESC[id.value] || '这一块还没接上界面；先把对话、设备、任务与设置跑顺，再逐个补齐。');
</script>

<template>
  <div class="bz-page ph-page">
    <header class="bz-head">
      <div class="bz-head-main">
        <h1 class="bz-title">{{ title }}</h1>
        <p class="bz-sub">{{ VIEW_META[id] ? '模块规划中' : '页面规划中' }}</p>
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
        <div class="bz-empty-title">「{{ title }}」规划中</div>
        <div class="bz-empty-desc">{{ desc }}</div>
        <div class="ph-note">接口已经就绪（Go 壳统一代理 /api/be/*），接上界面即可使用。</div>
        <NButton size="small" @click="router.push('/chat')">返回对话</NButton>
      </div>
    </section>
  </div>
</template>

<style>
.ph-page { max-width: 720px; }
.ph-note { margin-top: 10px; font-family: var(--mono); font-size: 11px; color: var(--fg-3); }
</style>
