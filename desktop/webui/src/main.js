// 白泽桌面主控台 Vue3 入口。
// Naive UI 按需引入 + 暗色/亮色主题跟随系统（沿用旧 shell.js 的 bz_theme 本地键，
// 与手机端同名，保持跨端偏好一致）。
import { createApp } from 'vue';
import { createPinia } from 'pinia';
import router from './router';
import App from './App.vue';
import { darkTheme, lightTheme } from 'naive-ui';

// 主题：先按本地缓存同步应用（避免闪一下），再向原生 /api/local/theme 拉一次确认。
const THEME_KEY = 'bz_theme';
let themePref = 'system';
try { themePref = localStorage.getItem(THEME_KEY) || 'system'; } catch (e) { /* 隐私模式忽略 */ }

function currentTheme() {
  const sysLight = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
  const t = (themePref === 'light' || themePref === 'dark') ? themePref : (sysLight ? 'light' : 'dark');
  document.documentElement.setAttribute('data-theme', t);
  return t === 'dark' ? darkTheme : lightTheme;
}

// 把当前主题挂到 window，App.vue / 各页面通过 useTheme() 订阅变化
window.__bzTheme = {
  get() { return currentTheme(); },
  setPref(t) {
    themePref = (t === 'light' || t === 'dark') ? t : 'system';
    try { localStorage.setItem(THEME_KEY, themePref); } catch (e) { /* 忽略 */ }
    document.documentElement.setAttribute('data-theme', currentTheme());
  },
};
applyThemeInit();
function applyThemeInit() { currentTheme(); }
if (window.matchMedia) {
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', applyThemeInit);
}

const app = createApp(App);
app.use(createPinia());
app.use(router);
app.mount('#app');

// 启动时向原生拉一次主题（读不到就用本地缓存）
(async () => {
  try {
    const r = await (await fetch('/api/local/theme')).json();
    if (r && r.theme) window.__bzTheme.setPref(r.theme);
  } catch (e) { /* 忽略 */ }
})();
