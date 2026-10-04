/* 白泽 · 入口 */
'use strict';

/* 出错别静默：打到控制台（adb logcat / Chrome DevTools 能看到现场） */
window.addEventListener('error', (e) => {
  console.error('JS错误: ' + (e.message || e.error) + ' @' + (e.filename || '') + ':' + (e.lineno || 0));
});
window.addEventListener('unhandledrejection', (e) => {
  const r = e.reason || {};
  console.error('Promise未处理: ' + (r.message || r));
});

/** 诊断快照：存储里都有哪些 key、AI 与密码本是什么状态
    （排查「加了却看不到 / 加错地方」这类问题用，返回 JSON 字符串） */
window.__bzDiag = function () {
  try {
    const keys = {};
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      keys[k] = (localStorage.getItem(k) || '').length;
    }
    return JSON.stringify({
      keys,
      kbMode: Store.kbMode(),
      kbOnline: Store.kbOnline(),
      kb: Store.kbInfo(),
      outbox: Store.outboxCount(),
      syncError: Store.lastSyncError(),
      models: (AI.list() || []).map(m => ({ name: m.name, model: m.model, keyLen: (m.apiKey || '').length, ready: AI.isReady(m) })),
      activeId: (AI.active() || {}).id,
      vaultLocked: Store.isVaultLocked(),
      hasMasterPwd: Store.hasMasterPwd(),
      chatCount: Chat.getMessages().length,
    });
  } catch (e) { return 'diag失败: ' + (e.message || e); }
};

/* 处理来自 Android 快捷方式（长按图标）的启动意图 */
function handleLaunchAction(action) {
  if (!action) return;
  const a = String(action);
  try {
    if (a.indexOf('NEW_TODO') >= 0) {
      UI.switchTab('view-todo');
      Todo.openForm(null);
    } else if (a.indexOf('VOICE') >= 0) {
      // 语音模块已移除：切到白泽页，用输入法自己的麦克风说话
      UI.switchTab('view-baize');
    } else if (a.indexOf('NEW_VAULT') >= 0) {
      UI.switchTab('view-vault');
      setTimeout(() => { try { Vault.openEdit(null); } catch (e) { } }, 350);
    } else if (a.indexOf('VAULT') >= 0) {
      UI.switchTab('view-vault');
    }
  } catch (e) { /* 忽略 */ }
}
window.__bzLaunchAction = handleLaunchAction;

/** 没连上后端知识库时，白泽页给一条去配置的提示（本地规则仍然可用） */
function refreshAiHints() {
  const warn = document.getElementById('voiceCfgWarn');
  if (!warn) return;
  warn.hidden = Store.kbOnline() || AI.ready();
}

/** 后端通了就把最新知识库拉下来，覆盖本机缓存（有排队改动会先补推，不会冲掉本地改动） */
function syncKB(quiet) {
  if (!Store.kbMode()) return;
  const r = Store.syncFromRemote();
  if (!r.ok) {
    if (!quiet) console.warn('知识库同步失败：' + r.error);
    return;
  }
  try { Todo.reload(); } catch (e) { /* 页面还没起来就算了 */ }
  try { Vault.reload(); } catch (e) { /* 忽略 */ }
}

(function boot() {
  // 先探一次「后端知识库通不通」：后面的读写要靠它决定走 NAS 还是本机
  if (window.BzDevice && BzDevice.available()) BzDevice.refreshRemote();
  syncKB(true);

  Todo.init();
  Vault.init();
  Chat.init();
  if (window.VzVoice) VzVoice.init();   // 语音：按住说话 + 气泡朗读
  Plan.init();
  Settings.init();
  KBExt.init();   // 知识库扩展页：附件 / 记忆 / API 服务 / 模型审批
  More.init();    // 其他功能目录
  UI.startReminderLoop();
  refreshAiHints();
  window.addEventListener('bz:aichanged', refreshAiHints);
  window.addEventListener('bz:syncdone', refreshAiHints);

  // 回到前台再对一次账：断网期间排队的改动补推，顺便拉一次最新数据
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    if (window.BzDevice && BzDevice.available()) BzDevice.refreshRemote();
    syncKB(true);
    refreshAiHints();
  });

  // 跨端：没走知识库模式时，等内核起来把本机待办镜像一份（之后每次改动都会自动推）
  if (window.BzDevice && BzDevice.available()) BzDevice.mirrorWhenReady();

  // 离线优先：注册 Service Worker（HTTP 环境有效；APK 里资源是内置的，不需要）
  if ('serviceWorker' in navigator && location.protocol.startsWith('http')
    && !window.BzNative) {
    navigator.serviceWorker.register('sw.js').then((reg) => {
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
