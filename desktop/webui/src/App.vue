<!-- 白泽桌面主控台外壳：左侧导航 + 顶栏 + 内容区（Naive UI 实现，替换旧 shell.html） -->
<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { NConfigProvider, darkTheme, lightTheme } from 'naive-ui';
import { NAV, VIEW_META } from './router';
import { useConnStore } from './store/conn.js';
import ApprovalModal from './components/ApprovalModal.vue';

const route = useRoute();
const router = useRouter();
const conn = useConnStore();
const currentTitle = computed(() => (route.meta && route.meta.title) || '—');
const currentId = computed(() => route.name ? String(route.name) : '');

function openApp(id) { router.push('/' + id); }

// 待审批角标：跟随 conn 轮询的 pendingCount
const pendingCount = computed(() => conn.pendingCount);

// 主题：main.js 把当前主题写到 data-theme，这里监听属性变化同步 Naive 主题
const themeRef = ref(document.documentElement.getAttribute('data-theme') === 'light' ? lightTheme : darkTheme);
let themeObserver = null;
function syncTheme() {
  themeRef.value = document.documentElement.getAttribute('data-theme') === 'light' ? lightTheme : darkTheme;
}

onMounted(() => {
  syncTheme();
  themeObserver = new MutationObserver(syncTheme);
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  conn.start();
});
onBeforeUnmount(() => {
  if (themeObserver) themeObserver.disconnect();
  conn.stop();
});

// 与 CSS 变量同源的一套 token，供 Naive 组件计算 themeOverrides
const DARK = {
  bg: '#070807', bgElev: '#0E100E', surface: '#0E100E', surface2: '#131614', surface3: '#181C18',
  line: '#1F231F', lineStrong: '#2A2F28',
  fg: '#E6E1D6', fg2: '#A6A296', fg3: '#6F6C62',
  accent: '#3BE0C4', accent2: '#7CEBD8', accentSoft: 'rgba(59,224,196,.10)', accentInk: '#05221D',
  ok: '#52C07A', warn: '#D9A45B', err: '#E5765B',
};
const LIGHT = {
  bg: '#F2ECE0', bgElev: '#FAF6EC', surface: '#FAF6EC', surface2: '#EFE8DA', surface3: '#E4DCCB',
  line: '#E0D7C5', lineStrong: '#D1C7B3',
  fg: '#17150F', fg2: '#55524A', fg3: '#7B776B',
  accent: '#0B6E63', accent2: '#0F8A7E', accentSoft: 'rgba(11,110,99,.10)', accentInk: '#FAF6EC',
  ok: '#2F7A45', warn: '#96601F', err: '#B23B27',
};
const FONT = '-apple-system, "Segoe UI Variable", "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif';

function buildOverrides(t) {
  return {
    common: {
      primaryColor: t.accent, primaryColorHover: t.accent, primaryColorPressed: t.accent2, primaryColorSuppl: t.accent2,
      infoColor: t.accent, successColor: t.ok, warningColor: t.warn, errorColor: t.err,
      bodyColor: t.bg, cardColor: t.surface, modalColor: t.surface, popoverColor: t.surface2, tableColor: t.surface,
      borderColor: t.line, dividerColor: t.line,
      textColorBase: t.fg, textColor1: t.fg, textColor2: t.fg2, textColor3: t.fg3,
      textColorDisabled: t.fg3, placeholderColor: t.fg3,
      fontFamily: FONT, fontSize: '14px', fontSizeSmall: '12px',
      borderRadius: '2px', borderRadiusSmall: '2px',
      heightSmall: '32px', heightMedium: '36px', heightLarge: '40px',
    },
    Button: {
      borderRadiusTiny: '2px', borderRadiusSmall: '2px', borderRadiusMedium: '2px', borderRadiusLarge: '2px',
      heightSmall: '32px', heightMedium: '36px', heightLarge: '40px',
      fontWeight: '500', fontWeightStrong: '600',
      colorPrimary: t.accent, colorHoverPrimary: t.accent, colorPressedPrimary: t.accent2, colorFocusPrimary: t.accent,
      textColorPrimary: t.accentInk, textColorHoverPrimary: t.accentInk, textColorPressedPrimary: t.accentInk, textColorFocusPrimary: t.accentInk,
      borderPrimary: '1px solid ' + t.accent, borderHoverPrimary: '1px solid ' + t.accent, borderPressedPrimary: '1px solid ' + t.accent2,
      textColor: t.fg2, textColorHover: t.fg, textColorPressed: t.fg,
      /* 默认按钮走「透明底 + 1px 描边」，墨底上不会出现白色块 */
      color: 'transparent', colorHover: t.surface2, colorPressed: t.surface3,
      border: '1px solid ' + t.line, borderHover: '1px solid ' + t.accent, borderPressed: '1px solid ' + t.accent2,
    },
    Input: {
      borderRadius: '2px', heightSmall: '32px', heightMedium: '34px', heightLarge: '36px',
      color: t.surface2, colorFocus: t.surface2,
      border: '1px solid ' + t.line, borderHover: '1px solid ' + t.lineStrong, borderFocus: '1px solid ' + t.accent,
      borderDisabled: '1px solid ' + t.line,
      boxShadowFocus: '0 0 0 3px ' + t.accentSoft,
      textColor: t.fg, caretColor: t.accent, placeholderColor: t.fg3,
    },
    Card: {
      borderRadius: '2px', color: t.surface, borderColor: t.line,
      titleFontSizeMedium: '13px', titleFontWeight: '600', paddingMedium: '16px',
    },
    Table: {
      borderRadius: '2px', borderColor: t.line,
      thColor: t.surface2, tdColor: t.surface, tdColorHover: t.surface2,
      thTextColor: t.fg3, tdTextColor: t.fg, thFontWeight: '600',
      thHeightMedium: '40px', tdHeightMedium: '44px', fontSizeMedium: '13px',
    },
    Tag: { borderRadius: '2px', color: t.surface2, textColor: t.fg2, border: '1px solid ' + t.line },
    Checkbox: { borderRadius: '2px', textColor: t.fg2 },
    Radio: { buttonBorderRadius: '2px' },
  };
}
const themeOverrides = computed(() => buildOverrides(themeRef.value === darkTheme ? DARK : LIGHT));

// 导航线性图标（24×24，stroke 1.6，统一在模板里渲染）
const ICON_PATHS = {
  chat: ['M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v9A1.5 1.5 0 0 1 18.5 16H9l-4 4v-4A1.5 1.5 0 0 1 4 14.5Z'],
  devices: ['M3.5 5.5h17v10h-17z', 'M8.5 19.5h7', 'M12 15.5v4'],
  tasks: ['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z', 'M8.5 12l2.4 2.4 4.6-4.8'],
  files: ['M3.5 6.5h5.5l2 2.5H20v9.5h-16.5z'],
  browser: ['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z', 'M3.5 12h17', 'M12 3.5c2.6 2.6 3.9 5.4 3.9 8.5s-1.3 5.9-3.9 8.5c-2.6-2.6-3.9-5.4-3.9-8.5s1.3-5.9 3.9-8.5Z'],
  kb: ['M5 4.5h14v15H5z', 'M9 4.5v15'],
  memory: ['M12 4l1.9 5.1L19 11l-5.1 1.9L12 18l-1.9-5.1L5 11l5.1-1.9z'],
  activity: ['M3 12h3.5L9 7l3.5 10L15 12h6'],
  accounts: ['M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z', 'M2.5 20a6.5 6.5 0 0 1 13 0', 'M16 4.6a3.5 3.5 0 0 1 0 6.8', 'M17.5 14.2a6.5 6.5 0 0 1 4 5.8'],
  conflicts: ['M4 5h4l8 14h4', 'M4 19h4l8-14h4', 'M17 2.5l3 2.5-3 2.5', 'M17 16.5l3 2.5-3 2.5'],
  providers: ['M4 5.5h16v5H4z', 'M4 13.5h16v5H4z', 'M7 8h2', 'M7 16h2'],
  mcp: ['M8.5 3.5v4', 'M15.5 3.5v4', 'M6 7.5h12v3a6 6 0 0 1-12 0Z', 'M12 16.5v4'],
  channels: ['M4 9a11.3 11.3 0 0 1 16 0', 'M7.5 12.5a7 7 0 0 1 9 0', 'M11 16a2.5 2.5 0 0 1 2 0', 'M12 18.5a1 1 0 1 0 0 2 1 1 0 0 0 0-2Z'],
  external: ['M14 4.5h5.5V10', 'M19.5 4.5l-7 7', 'M10 5.5H6a1.5 1.5 0 0 0-1.5 1.5v11A1.5 1.5 0 0 0 6 19.5h11a1.5 1.5 0 0 0 1.5-1.5v-4'],
  skills: ['M13 3.5L6 12.5h5.5L10.5 20.5 17.5 11.5H12z'],
  plugins: ['M4 4.5h6.5v6.5H4z', 'M13.5 4.5H20v6.5h-6.5z', 'M4 13h6.5v6.5H4z', 'M13.5 13H20v6.5h-6.5z'],
  persona: ['M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M4 20a8 8 0 0 1 16 0'],
  cron: ['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z', 'M12 7.5V12l3.2 2'],
  observe: ['M4 19.5V10', 'M9.5 19.5V5', 'M15 19.5v-6.5', 'M20.5 19.5V8', 'M3.5 20.5h17'],
  backup: ['M4 4.5h16v4H4z', 'M5.5 8.5v10h13v-10', 'M9.5 12h5'],
  settings: ['M4 7.5h16', 'M4 16.5h16', 'M8 4.5v6', 'M16 13.5v6'],
};
function iconPaths(id) { return ICON_PATHS[id] || ['M12 4.5a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15Z']; }
</script>

<template>
  <NConfigProvider :theme="themeRef" :theme-overrides="themeOverrides" class="bz-provider">
    <div class="bz-layout" :class="{ mini: conn.mini }">
      <aside class="bz-sidebar">
        <div class="bz-brand">
          <div class="bz-mark" aria-hidden="true">
            <!-- 白泽：独角瑞兽侧脸线描（与安装包 / 官网 / NAS 面板同一枚图标） -->
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"
                 stroke-linecap="round" stroke-linejoin="round">
              <path d="M4.6 13C4.8 11.4 5.8 10.2 7.2 9.7C7.5 8.4 8.3 7.3 9.5 6.9C10.4 6.6 11.4 6.6 12.3 6.9C14.5 7.4 16.2 9.2 16.5 11.5C16.7 13.1 16.1 14.7 15 15.9C13.2 17.4 10.9 17.9 8.9 17.3C7 16.7 5.4 15.2 4.6 13Z" />
              <path d="M5.2 14.6C6.6 15.1 8.2 15.2 9.4 14.8" />
              <path d="M9.2 9C10.1 8.6 11.1 8.8" />
              <path d="M12.9 7.2C13.4 5.4 14.5 4.5 15.9 4.7C15.8 6.1 15 7.2 13.9 7.9" />
              <path d="M16.4 9.4C17.9 9.7 19 10.7 19.4 12" />
              <path d="M16.4 11.6C17.9 12.3 18.8 13.6 19 15.1" />
              <path d="M10.6 6.9C10.9 5 11.6 3.2 12.9 2C13 4.2 12.6 5.8 11.8 6.9Z" />
              <path d="M9.3 10.5C9.9 9.8 10.9 9.8 11.5 10.4C10.9 11 9.9 11 9.3 10.5Z" fill="currentColor" stroke="none" />
            </svg>
          </div>
          <div class="bz-brand-text"><b>白泽</b><span>个人智能体</span></div>
        </div>

        <nav class="bz-nav">
          <template v-for="g in NAV" :key="g.title">
            <div class="bz-nav-grp">{{ g.title }}</div>
            <a
              v-for="id in g.ids"
              :key="id"
              :class="['bz-nav-item', { on: currentId === id }]"
              title="切换到 {{ VIEW_META[id] || id }}"
              @click.prevent="openApp(id)"
            >
              <svg class="bz-nav-ico" viewBox="0 0 24 24" aria-hidden="true" fill="none"
                   stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
                <path v-for="(d, i) in iconPaths(id)" :key="i" :d="d" />
              </svg>
              <span class="bz-nav-n">{{ VIEW_META[id] || id }}</span>
              <span v-if="id === 'tasks' && pendingCount > 0" class="bz-nav-badge">{{ pendingCount }}</span>
            </a>
          </template>
        </nav>

        <div class="bz-sidefoot">
          <div class="bz-status">
            <i class="bz-dot" :class="{ on: conn.connected, err: !conn.connected }"></i>
            <span>{{ conn.connected ? conn.statusText : '连不上后端' }}</span>
          </div>
          <div class="bz-server">{{ conn.serverText }}</div>
        </div>
      </aside>

      <main class="bz-main">
        <header class="bz-topbar">
          <div class="bz-crumb">
            <span class="bz-crumb-brand">白泽</span>
            <span class="bz-crumb-sep">/</span>
            <b>{{ currentTitle }}</b>
          </div>
          <div class="bz-topbar-right">
            <button
              v-if="pendingCount > 0"
              class="bz-chip"
              title="待人工放行的跨端任务"
              @click="openApp('tasks')"
            >
              <i class="bz-chip-dot"></i>{{ pendingCount }} 待审批
            </button>
            <span class="bz-clock">{{ conn.clock }}</span>
          </div>
        </header>
        <div class="bz-view">
          <router-view />
        </div>
      </main>

      <!-- 危险操作审批弹窗（置顶，全局可用） -->
      <ApprovalModal />
    </div>
  </NConfigProvider>
</template>

<style>
/* 设计系统「墨玉 · 东方蓝图」：深色默认，浅色按 data-theme 覆盖 */
:root {
  --bg: #070807; --bg-elev: #0E100E; --surface: #0E100E; --surface-2: #131614; --surface-3: #181C18;
  --line: #1F231F; --line-strong: #2A2F28;
  --fg: #E6E1D6; --fg-2: #A6A296; --fg-3: #6F6C62;
  --accent: #3BE0C4; --accent-2: #7CEBD8; --accent-soft: rgba(59,224,196,.10); --accent-ink: #05221D;
  --ok: #52C07A; --warn: #D9A45B; --err: #E5765B;
  --mono: ui-monospace, "SFMono-Regular", Consolas, monospace;
  /* 旧变量名保留为别名，避免遗漏的引用变成无效色 */
  --fg2: var(--fg-2); --fg3: var(--fg-3); --panel: var(--surface); --card: var(--surface);
}
[data-theme="light"] {
  --bg: #F2ECE0; --bg-elev: #FAF6EC; --surface: #FAF6EC; --surface-2: #EFE8DA; --surface-3: #E4DCCB;
  --line: #E0D7C5; --line-strong: #D1C7B3;
  --fg: #17150F; --fg-2: #55524A; --fg-3: #7B776B;
  --accent: #0B6E63; --accent-2: #0F8A7E; --accent-soft: rgba(11,110,99,.10); --accent-ink: #FAF6EC;
  --ok: #2F7A45; --warn: #96601F; --err: #B23B27;
}

* { box-sizing: border-box; }
html, body, #app { height: 100%; margin: 0; }
body {
  background: var(--bg); color: var(--fg);
  font: 400 14px/1.55 -apple-system, "Segoe UI Variable", "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
  -webkit-font-smoothing: antialiased;
}
.bz-provider { height: 100%; }
.n-button { min-width: 0; }
::selection { background: var(--accent-soft); color: var(--fg); }

.bz-layout { display: flex; height: 100vh; background: var(--bg); }
.bz-main { flex: 1; display: flex; flex-direction: column; min-width: 0; }

/* 侧栏 */
.bz-sidebar {
  width: 236px; min-width: 236px; display: flex; flex-direction: column;
  background: var(--bg-elev); border-right: 1px solid var(--line);
}
.bz-brand { height: 64px; display: flex; align-items: center; gap: 10px; padding: 0 16px; }
.bz-mark {
  width: 30px; height: 30px; flex: none; border-radius: 2px;
  display: grid; place-items: center;
  background: var(--accent-soft); color: var(--accent);
}
.bz-mark svg { width: 23px; height: 23px; display: block; }
.bz-brand-text b { display: block; font-size: 14px; font-weight: 600; line-height: 1.35; }
.bz-brand-text span { font-size: 11px; color: var(--fg-3); letter-spacing: .08em; }

/* 导航 */
.bz-nav { flex: 1; overflow-y: auto; padding: 0 10px 12px; }
.bz-nav-grp {
  margin: 18px 6px 6px; font-size: 11px; color: var(--fg-3);
  letter-spacing: .1em; line-height: 1.4;
}
.bz-nav-item {
  position: relative; display: flex; align-items: center; gap: 10px;
  height: 38px; padding: 0 10px; border-radius: 4px;
  color: var(--fg-2); font-size: 13px; text-decoration: none; cursor: pointer;
  transition: background 120ms cubic-bezier(.2,0,0,1), color 120ms cubic-bezier(.2,0,0,1);
}
.bz-nav-item:hover { background: var(--surface-2); color: var(--fg); }
.bz-nav-item.on { background: var(--accent-soft); color: var(--fg); font-weight: 500; }
.bz-nav-item.on::before {
  content: ""; position: absolute; left: 0; top: 0; bottom: 0; width: 2px;
  background: var(--accent); border-radius: 2px 0 0 2px;
}
.bz-nav-ico { width: 18px; height: 18px; flex: none; opacity: .85; }
.bz-nav-item.on .bz-nav-ico { color: var(--accent); opacity: 1; }
.bz-nav-n { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.bz-nav-badge {
  min-width: 20px; height: 18px; padding: 0 6px; border-radius: 2px;
  display: inline-flex; align-items: center; justify-content: center;
  background: color-mix(in srgb, var(--warn) 14%, transparent); color: var(--warn);
  font-family: var(--mono); font-size: 11px; font-variant-numeric: tabular-nums;
}

/* 侧栏底部 */
.bz-sidefoot { border-top: 1px solid var(--line); padding: 14px 16px 16px; display: flex; flex-direction: column; gap: 6px; }
.bz-status { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--fg-2); line-height: 1.4; }
.bz-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--fg-3); flex: none; }
.bz-dot.on { background: var(--ok); }
.bz-dot.err { background: var(--err); }
.bz-server { font-family: var(--mono); font-size: 11px; color: var(--fg-3); line-height: 1.45; word-break: break-all; }

/* 顶栏 */
.bz-topbar {
  height: 52px; flex: none; display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 0 24px; background: var(--bg-elev); border-bottom: 1px solid var(--line);
}
.bz-crumb { display: flex; align-items: center; gap: 8px; min-width: 0; }
.bz-crumb-brand { font-size: 12px; color: var(--fg-3); flex: none; }
.bz-crumb-sep { font-size: 12px; color: var(--fg-3); opacity: .7; flex: none; }
.bz-crumb b { font-size: 15px; font-weight: 600; color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.bz-topbar-right { display: flex; align-items: center; gap: 12px; flex: none; }
.bz-chip {
  display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px;
  border: 1px solid var(--line); border-radius: 2px; cursor: pointer;
  background: color-mix(in srgb, var(--warn) 12%, transparent); color: var(--warn);
  font-family: inherit; font-size: 11px; font-weight: 600; letter-spacing: .06em;
  transition: background 120ms cubic-bezier(.2,0,0,1);
}
.bz-chip:hover { background: color-mix(in srgb, var(--warn) 20%, transparent); }
.bz-chip-dot { width: 5px; height: 5px; border-radius: 50%; background: var(--warn); }
.bz-clock {
  font-family: var(--mono); font-size: 12px; color: var(--fg-2);
  font-variant-numeric: tabular-nums; min-width: 44px; text-align: right;
}

/* 内容区 */
.bz-view { flex: 1; overflow: auto; padding: 24px; background: var(--bg); min-height: 0; }

/* 页面通用骨架：标题区 / 卡片 / 空状态 */
.bz-page { max-width: 1120px; }
.bz-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.bz-head-main { min-width: 0; }
.bz-title { margin: 0; font-family: Georgia, "Songti SC", "Noto Serif SC", "Source Han Serif SC", serif; font-size: 21px; font-weight: 500; line-height: 1.35; letter-spacing: .02em; color: var(--fg); }
.bz-sub { margin: 4px 0 0; font-size: 12px; color: var(--fg-3); line-height: 1.5; }
.bz-head-actions { display: flex; align-items: center; gap: 8px; flex: none; padding-bottom: 2px; }
.bz-card { background: var(--surface); border: 1px solid var(--line); border-radius: 2px; padding: 16px; }
.bz-card + .bz-card { margin-top: 14px; }
.bz-empty { display: flex; flex-direction: column; align-items: center; text-align: center; padding: 56px 24px; }
.bz-empty-ico { width: 40px; height: 40px; color: var(--fg-3); margin-bottom: 14px; }
.bz-empty-title { font-size: 14px; font-weight: 600; color: var(--fg); }
.bz-empty-desc { margin-top: 6px; max-width: 380px; font-size: 12px; color: var(--fg-3); line-height: 1.65; }
.bz-empty .n-button { margin-top: 18px; }

/* 全局轻提示（各页面 toast() 使用） */
.bz-toast {
  position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); z-index: 10000;
  padding: 10px 16px; border-radius: 2px;
  background: var(--surface-3); border: 1px solid var(--line); color: var(--fg); font-size: 12px;
  box-shadow: 0 8px 28px rgba(0,0,0,.35);
  animation: bz-toast-in 200ms cubic-bezier(.2,0,0,1);
}
.bz-toast.ok { color: var(--ok); }
.bz-toast.warn { color: var(--warn); }
.bz-toast.err { color: var(--err); }
@keyframes bz-toast-in { from { opacity: 0; transform: translate(-50%, 6px); } to { opacity: 1; transform: translate(-50%, 0); } }

::-webkit-scrollbar { width: 10px; height: 10px; }
::-webkit-scrollbar-thumb { background: var(--surface-3); border: 3px solid transparent; background-clip: content-box; border-radius: 999px; }
::-webkit-scrollbar-track { background: transparent; }

/* 迷你小窗（原生侧把主窗口切成置顶小窗时，藏掉侧栏与顶栏） */
.bz-layout.mini .bz-sidebar, .bz-layout.mini .bz-topbar { display: none; }
</style>
