import { createApp } from 'vue';
import { createPinia } from 'pinia';
import App from './App.vue';
import { router } from './router.js';

const app = createApp(App);
app.use(createPinia());
app.use(router);

// 初始化原生桥（Android WebView 才有）
if (window.BzNative) {
  window.__bzNative = window.BzNative;
}

// 快捷方式启动意图（来自 Android 长按图标）
window.__bzLaunchAction = (action) => {
  if (!action) return;
  const a = String(action);
  if (a.includes('NEW_TODO')) {
    router.push('/todo');
    setTimeout(() => { window.__bzOpenTodoForm?.(); }, 300);
  } else if (a.includes('VOICE') || a.includes('BAIZE')) {
    router.push('/chat');
  } else if (a.includes('NEW_VAULT')) {
    router.push('/vault');
    setTimeout(() => { window.__bzOpenVaultForm?.(); }, 300);
  } else if (a.includes('VAULT')) {
    router.push('/vault');
  } else if (a.includes('KB') || a.includes('KB_EXT')) {
    router.push('/kb');
  }
};

app.mount('#app');
