/* 白泽待办中心 - 入口 */
'use strict';
(function boot() {
  Todo.init();
  Vault.init();
  Voice.init();
  Plan.init();
  Settings.init();
  UI.startReminderLoop();

  // 离线优先：注册 Service Worker（HTTP 环境有效）
  if ('serviceWorker' in navigator && location.protocol.startsWith('http')) {
    navigator.serviceWorker.register('sw.js').then((reg) => {
      // 有新版本就绪且当前仍由旧版本控制 → 刷新一次，避免前端更新被缓存卡住
      reg.addEventListener('updatefound', () => {
        const sw = reg.installing;
        if (!sw) return;
        sw.addEventListener('statechange', () => {
          if (sw.state === 'installed' && navigator.serviceWorker.controller
            && !sessionStorage.getItem('bz-sw-reloaded')) {
            sessionStorage.setItem('bz-sw-reloaded', '1');
            location.reload();
          }
        });
      });
      reg.update().catch(() => {});
    }).catch(() => {});
  }
})();
