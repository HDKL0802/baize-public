import { createRouter, createWebHashHistory } from 'vue-router';
import ChatView from './views/ChatView.vue';
import TodoView from './views/TodoView.vue';
import VaultView from './views/VaultView.vue';
import KBView from './views/KBView.vue';
import SettingsView from './views/SettingsView.vue';
import MoreView from './views/MoreView.vue';
import PlanView from './views/PlanView.vue';

export const NAV = [
  { id: 'chat', label: '对话', icon: 'chat' },
  { id: 'kb', label: '知识库', icon: 'book' },
  { id: 'more', label: '其他功能', icon: 'grid' },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', redirect: '/chat' },
    { path: '/chat', name: 'chat', component: ChatView, meta: { title: '白泽智能体' } },
    { path: '/todo', name: 'todo', component: TodoView, meta: { title: '待办' } },
    { path: '/vault', name: 'vault', component: VaultView, meta: { title: '密码本' } },
    { path: '/kb', name: 'kb', component: KBView, meta: { title: '知识库' } },
    { path: '/settings', name: 'settings', component: SettingsView, meta: { title: '设置' } },
    { path: '/more', name: 'more', component: MoreView, meta: { title: '其他功能' } },
    { path: '/plan', name: 'plan', component: PlanView, meta: { title: 'AI 排期' } },
  ],
});
