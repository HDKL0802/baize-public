// 路由：hash 模式（与旧 desktop/ui 的 #appid 深链一致，Go 壳不依赖 history API）。
// 导航分组沿用旧 shell.js 的 NAV；页面组件按需引入（路由懒加载，省首屏）。
import { createRouter, createWebHashHistory } from 'vue-router';

export const NAV = [
  { title: '核心', ids: ['chat', 'devices', 'tasks', 'files', 'browser'] },
  { title: '知识', ids: ['kb', 'memory', 'activity'] },
  { title: '协作', ids: ['accounts', 'conflicts'] },
  { title: '能力', ids: ['providers', 'mcp', 'channels', 'external', 'skills', 'plugins', 'persona', 'cron'] },
  { title: '系统', ids: ['observe', 'backup', 'settings'] },
];

// 各页面的标题（与旧 window.APPS 对齐）
export const VIEW_META = {
  chat: '对话', devices: '设备', files: '电脑文件', browser: '浏览器',
  settings: '设置', tasks: '任务与审批', kb: '知识库', memory: '记忆星图',
  providers: '模型通道', mcp: 'MCP 服务', external: '外部 Agent',
  channels: '频道', skills: '技能', plugins: '插件市场', persona: '人设',
  cron: '定时任务', backup: '备份与恢复', activity: '活动追踪', observe: '可观测性',
  accounts: '账号与共享', conflicts: '冲突协商',
};

// 已实装的页面 → 组件（其余先走占位，避免整页空白）
import ChatView from './views/ChatView.vue';
import DevicesView from './views/DevicesView.vue';
import SettingsView from './views/SettingsView.vue';
import TasksView from './views/TasksView.vue';
import PlaceholderView from './views/PlaceholderView.vue';

function lazyView(name) {
  // 已实装的用组件，未实装的回退占位（懒加载不报错）
  const map = {
    chat: ChatView, devices: DevicesView, settings: SettingsView, tasks: TasksView,
  };
  return map[name] || PlaceholderView;
}

const routes = Object.keys(VIEW_META).map(id => ({
  path: '/' + id,
  name: id,
  component: lazyView(id),
  meta: { title: VIEW_META[id] },
}));

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    ...routes,
    { path: '/', redirect: '/devices' }, // 默认开「设备」（与旧 shell 一致）
    { path: '/:id(.*)', component: PlaceholderView, meta: { title: '未找到' } },
  ],
});

router.afterEach((to) => {
  document.title = '白泽 · ' + (to.meta && to.meta.title ? to.meta.title : '');
});

export default router;
