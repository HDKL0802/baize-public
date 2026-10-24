/* 白泽 · 外观（亮色 / 暗色 / 跟随系统）
   ─────────────────────────────────────────────
   这个文件在 <head> 里、样式表之后**最先执行**：先把 data-theme 打到 <html> 上，
   再让后面的内容渲染 —— 亮色用户才不会先闪一下暗色（跟桌面端 shell.js 同一个套路）。

   主题是「每台设备各管各的」的本机偏好，所以只存 localStorage（键 `bz_theme`，与桌面端同名），
   不进后端设置：手机看暗色、平板看亮色是完全合理的。

   设置页那个「外观」分组也由这里**运行时注入**（renderGroup），
   这样 todo-app/ 与后端控制台那份手机版副本（agent/internal/httpapi/webui/）不用各写一份一样的 HTML。 */
'use strict';
(function () {
  var KEY = 'bz_theme';
  var PREF = 'system'; // light | dark | system
  try { PREF = localStorage.getItem(KEY) || 'system'; } catch (e) { /* 隐私模式：读不到就当跟随系统 */ }
  if (PREF !== 'light' && PREF !== 'dark') PREF = 'system';

  var LABEL = { light: '亮色', dark: '暗色', system: '跟随系统' };

  function sysLight() {
    return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches);
  }
  /** 当前实际生效的那一套：light | dark */
  function effective() {
    return PREF === 'system' ? (sysLight() ? 'light' : 'dark') : PREF;
  }
  function apply() {
    var t = effective();
    document.documentElement.setAttribute('data-theme', t);
    // 顺手把浏览器地址栏/状态栏的颜色也对上（就是 index.html 里那个 meta）
    var m = document.querySelector('meta[name="theme-color"]');
    if (m) m.setAttribute('content', t === 'light' ? '#f7f6f2' : '#0b0e13');
  }
  apply(); // 尽早定调：这一步在 <head> 里、body 之前

  // 跟随系统时，系统换主题要即时跟（老 WebView 只有 addListener）
  if (window.matchMedia) {
    var mq = window.matchMedia('(prefers-color-scheme: light)');
    var onSysChange = function () { if (PREF === 'system') apply(); };
    if (mq.addEventListener) mq.addEventListener('change', onSysChange);
    else if (mq.addListener) mq.addListener(onSysChange);
  }

  function pref() { return PREF; }
  function setPref(p) {
    PREF = (p === 'light' || p === 'dark') ? p : 'system';
    try { localStorage.setItem(KEY, PREF); } catch (e) { /* 忽略：写不进去就只对本次会话生效 */ }
    apply();
  }

  /* ================= 设置页的「外观」分组（运行时注入） ================= */

  function renderGroup() {
    var host = document.getElementById('view-settings');
    if (!host || document.getElementById('themeGroup')) return;
    var group = document.createElement('div');
    group.className = 'settings-group';
    group.innerHTML =
      '<h3 class="group-title">外观</h3>' +
      '<p class="group-desc">「跟随系统」会随手机的浅色 / 深色设置自动切换。这个偏好只存在这台设备上，不上传。</p>' +
      '<div class="chip-group" id="themeGroup">' +
      '<button class="chip-opt" data-theme-set="light">亮色</button>' +
      '<button class="chip-opt" data-theme-set="dark">暗色</button>' +
      '<button class="chip-opt" data-theme-set="system">跟随系统</button>' +
      '</div>';
    // 插在最后一个分组（「关于」）之前，别孤零零挂在页尾
    var last = host.lastElementChild;
    if (last && last.classList && last.classList.contains('settings-group')) host.insertBefore(group, last);
    else host.appendChild(group);

    document.getElementById('themeGroup').addEventListener('click', function (e) {
      var b = e.target.closest('[data-theme-set]');
      if (!b) return;
      setPref(b.dataset.themeSet);
      paint();
      try { if (window.UI && UI.toast) UI.toast('外观：' + (LABEL[PREF] || PREF)); } catch (err) { /* 界面还没起来就算了 */ }
    });
    paint();
  }

  /** 把选中态刷到按钮上（与别处 .chip-opt.on 同一套样式） */
  function paint() {
    var grp = document.getElementById('themeGroup');
    if (!grp) return;
    grp.querySelectorAll('[data-theme-set]').forEach(function (b) {
      b.classList.toggle('on', b.dataset.themeSet === PREF);
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', renderGroup);
  else renderGroup();

  window.BzTheme = { pref: pref, setPref: setPref, effective: effective };
})();
