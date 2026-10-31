/* 白泽官网 · 交互脚本 v3.3
   1. 快速开始 4 卡片展开切换
   2. 代码块复制按钮
   3. 下载按钮：双源（国内 Gitee / 国外 GitHub）拉 latest release，自动填充三端直链（1h 缓存）
   4. 移动端导航菜单
   5. 滚动渐入动画
*/

(function () {
  "use strict";

  // ===================== 配置 =====================
  // 双源发布：国内走 Gitee（szk0037/baize-public），国外走 GitHub（HDKL0802/baize-public）。
  // 默认自动判定（GitHub API 2.5 秒内不通就用 Gitee），也可以在下载菜单里手动切换。
  const MIRRORS = {
    github: {
      label: "GitHub",
      api: "https://api.github.com/repos/HDKL0802/baize-public",
      releases: "https://github.com/HDKL0802/baize-public/releases",
      raw: "https://raw.githubusercontent.com/HDKL0802/baize-public/main",
      headers: { Accept: "application/vnd.github+json" },
    },
    gitee: {
      label: "Gitee",
      api: "https://gitee.com/api/v5/repos/szk0037/baize-public",
      releases: "https://gitee.com/szk0037/baize-public/releases",
      raw: "https://gitee.com/szk0037/baize-public/raw/main",
      headers: {},
    },
  };
  const MIRROR_KEY   = "baize-mirror";           // auto | gitee | github
  const CACHE_PREFIX = "baize-latest-release-v4-";
  const CACHE_TTL_MS = 60 * 60 * 1000;           // 1 小时
  let mirrorName = "github";
  let mirrorInfo = MIRRORS.github;

  // 匹配规则：从 release assets 里按文件名找到对应文件
  const ASSET_MATCHERS = {
    desktop: {
      title: "Windows 桌面端",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="13" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /^baize-desktop-setup-\d[\d.]+\.exe$/.test(name),
    },
    android: {
      title: "Android 手机端",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="2" width="14" height="20" rx="2"/><line x1="12" y1="18" x2="12.01" y2="18"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /^bz-todo-v?\d[\d.]+\.apk$/.test(name),
    },
    compose: {
      title: "NAS · docker-compose",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /nas-docker-compose\.yml$/.test(name),
    },
  };

  // ===================== 快速开始 4 卡片 =====================
  const cards = document.querySelectorAll(".deploy-card");
  const panes = document.querySelectorAll(".deploy-pane");
  function activate(card) {
    const name = card.dataset.card;
    cards.forEach((c) => c.classList.toggle("on", c === card));
    panes.forEach((p) => p.classList.toggle("on", p.dataset.pane === name));
  }
  const firstCard = document.querySelector(".deploy-card.on") || cards[0];
  if (firstCard) activate(firstCard);
  cards.forEach((card) => card.addEventListener("click", () => activate(card)));

  // ===================== 代码块复制 =====================
  document.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      e.stopPropagation();
      const block = btn.parentElement;
      const text = block.innerText.replace(/复制|已复制/g, "").trim();
      try {
        await navigator.clipboard.writeText(text);
        btn.textContent = "已复制"; btn.classList.add("copied");
        setTimeout(() => { btn.textContent = "复制"; btn.classList.remove("copied"); }, 1600);
      } catch {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed"; ta.style.opacity = "0";
        document.body.appendChild(ta); ta.select();
        document.execCommand("copy"); document.body.removeChild(ta);
        btn.textContent = "已复制"; btn.classList.add("copied");
        setTimeout(() => { btn.textContent = "复制"; btn.classList.remove("copied"); }, 1600);
      }
    });
  });

  // ===================== 下载菜单 =====================
  const downloadWrap  = document.querySelector(".nav-download");
  const downloadBtn   = document.querySelector(".nav-download-btn");
  const downloadMenu  = document.querySelector(".nav-download-menu");

  // ---------- 下载源：自动探测 / 手动切换 ----------
  function readMirrorPref() {
    try { return localStorage.getItem(MIRROR_KEY) || "auto"; } catch { return "auto"; }
  }
  function saveMirrorPref(v) {
    try {
      if (v === "auto") localStorage.removeItem(MIRROR_KEY);
      else localStorage.setItem(MIRROR_KEY, v);
    } catch {}
  }
  function setMirror(name) {
    mirrorName = MIRRORS[name] ? name : "github";
    mirrorInfo = MIRRORS[mirrorName];
    applySiteLinks();
  }
  // 站点里的仓库 / 文档 / Issue 链接跟着下载源走（国内 Gitee，国外 GitHub）
  function applySiteLinks() {
    document.querySelectorAll("[data-github][data-gitee]").forEach((el) => {
      const url = mirrorName === "gitee" ? el.dataset.gitee : el.dataset.github;
      if (url) el.href = url;
      const label = mirrorName === "gitee" ? el.dataset.giteeLabel : el.dataset.githubLabel;
      if (label) el.textContent = label;
    });
  }
  // 2.5 秒内能连上 api.github.com 就用 GitHub，否则认为在国内网络环境，改用 Gitee。
  function probeGitHub(ms) {
    return new Promise((resolve) => {
      const ac = new AbortController();
      const timer = setTimeout(() => { ac.abort(); resolve(false); }, ms);
      fetch(`${MIRRORS.github.api}/releases/latest`, {
        headers: MIRRORS.github.headers, cache: "no-store", signal: ac.signal,
      })
        .then((res) => { clearTimeout(timer); resolve(res.ok); })
        .catch(() => { clearTimeout(timer); resolve(false); });
    });
  }
  async function resolveMirror() {
    const pref = readMirrorPref();
    if (MIRRORS[pref]) { setMirror(pref); return; }
    const ghOK = await probeGitHub(2500);
    setMirror(ghOK ? "github" : "gitee");
  }

  function setLoading() {
    if (!downloadMenu) return;
    downloadMenu.innerHTML = `<div class="ndm-loading">正在从 ${mirrorInfo.label} 获取最新版本…</div>`;
  }
  function setError(msg, onRetry) {
    if (!downloadMenu) return;
    const btnId = "ndm-retry-btn";
    downloadMenu.innerHTML = `
      <div class="ndm-error">${msg || "获取失败"}
        <br><button class="ndm-retry" id="${btnId}" type="button">重试</button>
      </div>`;
    const b = document.getElementById(btnId);
    if (b && onRetry) b.addEventListener("click", onRetry);
  }
  function setFallback() {
    // API 拿不到时的兜底：直接给当前下载源的 Releases 页
    if (!downloadMenu) return;
    downloadMenu.innerHTML = `
      <a class="ndm-item ndm-more" href="${mirrorInfo.releases}" target="_blank" rel="noopener">
        <div class="ndm-ic ndm-ic-more">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
        </div>
        <div class="ndm-body">
          <div class="ndm-title">${mirrorInfo.label} Releases</div>
          <div class="ndm-sub">手动查看所有下载资源</div>
        </div>
        <svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
      </a>
      ${mirrorSwitchHtml()}`;
  }

  // 下载源切换：自动 / 国内 Gitee / 国外 GitHub（选择记在 localStorage）
  function mirrorSwitchHtml() {
    const pref = readMirrorPref();
    const btn = (v, label) =>
      `<button type="button" class="ndm-mirror-btn${pref === v ? " on" : ""}" data-mirror="${v}">${label}</button>`;
    return `
      <div class="ndm-divider"></div>
      <div class="ndm-mirror">
        <span class="ndm-mirror-label">下载源</span>
        <div class="ndm-mirror-btns">
          ${btn("auto", "自动")}${btn("gitee", "国内 Gitee")}${btn("github", "国外 GitHub")}
        </div>
      </div>`;
  }

  function setMenuHtml(items) {
    if (!downloadMenu) return;
    let html = "";
    items.forEach((it) => {
      if (!it) return;
      const sub = it.asset.size > 0
        ? `${it.asset.name} · ${(it.asset.size / 1024 / 1024).toFixed(1)} MB`
        : `${it.asset.name} · 跟随 main 分支`;
      html += `
        <a class="ndm-item" href="${it.asset.browser_download_url}" target="_blank" rel="noopener" download>
          <div class="ndm-ic">${it.meta.icon}</div>
          <div class="ndm-body">
            <div class="ndm-title">${it.meta.title}</div>
            <div class="ndm-sub">${sub}</div>
          </div>
          ${it.meta.arrow}
        </a>`;
    });
    html += `
      <div class="ndm-divider"></div>
      <a class="ndm-item ndm-more" href="${mirrorInfo.releases}" target="_blank" rel="noopener">
        <div class="ndm-ic ndm-ic-more">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
        </div>
        <div class="ndm-body">
          <div class="ndm-title">查看全部 Releases</div>
          <div class="ndm-sub">${mirrorInfo.label} 版本页 · 含历史版本</div>
        </div>
        <svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
      </a>
      ${mirrorSwitchHtml()}`;
    downloadMenu.innerHTML = html;
  }

  // 桌面端和手机端是两条独立的 release 线（desktop-v* / mobile-v*），
  // 所以要拿「最新 release」+「release 列表」两份数据。
  async function loadItems() {
    const headers = mirrorInfo.headers || {};
    const [latestRes, listRes] = await Promise.all([
      fetch(`${mirrorInfo.api}/releases/latest`, { headers, cache: "no-store" }),
      fetch(`${mirrorInfo.api}/releases?per_page=30`, { headers, cache: "no-store" }),
    ]);
    if (!latestRes.ok) throw new Error(`${mirrorInfo.label} API ${latestRes.status}`);

    const latest = await latestRes.json();
    const list = listRes.ok ? await listRes.json() : [];

    const items = [];

    // 桌面端：latest release 里的 .exe
    const deskAsset = (latest.assets || []).find((a) => ASSET_MATCHERS.desktop.match(a.name));
    if (deskAsset) items.push({ asset: deskAsset, meta: ASSET_MATCHERS.desktop });

    // 手机端：在 release 列表里找最新的 bz-todo-*.apk
    if (Array.isArray(list)) {
      for (const rel of list) {
        const apk = (rel.assets || []).find((a) => ASSET_MATCHERS.android.match(a.name));
        if (apk) { items.push({ asset: apk, meta: ASSET_MATCHERS.android }); break; }
      }
    }

    // NAS compose：随 main 分支走，始终最新
    items.push({
      asset: {
        name: "nas-docker-compose.yml",
        size: 0,
        browser_download_url: `${mirrorInfo.raw}/deploy/nas-docker-compose.yml`,
      },
      meta: ASSET_MATCHERS.compose,
    });

    return { tag: latest.tag_name || "", items };
  }

  async function fetchRelease(force) {
    await resolveMirror();
    const cacheKey = CACHE_PREFIX + mirrorName;

    // 读缓存（手动切换下载源时跳过）
    if (!force) {
      try {
        const raw = localStorage.getItem(cacheKey);
        if (raw) {
          const cached = JSON.parse(raw);
          if (Date.now() - cached.ts < CACHE_TTL_MS && cached.data && cached.data.items.length) {
            setMenuHtml(cached.data.items);
            return;
          }
        }
      } catch {}
    }

    setLoading();

    // 自动模式下，首选源失败就换另一个源再试
    const auto = !MIRRORS[readMirrorPref()];
    const tries = [mirrorName];
    if (auto) tries.push(mirrorName === "github" ? "gitee" : "github");

    for (const name of tries) {
      setMirror(name);
      try {
        const data = await loadItems();
        try {
          localStorage.setItem(cacheKey, JSON.stringify({ ts: Date.now(), data }));
        } catch {}
        setMenuHtml(data.items);
        return;
      } catch (err) {
        console.warn(`[baize] ${mirrorInfo.label} release fetch failed:`, err.message);
      }
    }
    setFallback();
  }

  // 页面加载即取
  if (downloadMenu) fetchRelease();

  // ---------- 下拉菜单交互 ----------
  function openMenu() {
    downloadWrap?.classList.add("open");
    downloadBtn?.setAttribute("aria-expanded", "true");
  }
  function closeMenu() {
    downloadWrap?.classList.remove("open");
    downloadBtn?.setAttribute("aria-expanded", "false");
  }
  if (downloadWrap && downloadBtn) {
    downloadBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      downloadWrap.classList.toggle("open");
      const open = downloadWrap.classList.contains("open");
      downloadBtn.setAttribute("aria-expanded", String(open));
    });
    downloadWrap.addEventListener("mouseenter", openMenu);
    downloadWrap.addEventListener("mouseleave", closeMenu);
    document.addEventListener("click", (e) => {
      if (!downloadWrap.contains(e.target)) closeMenu();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeMenu();
    });
    // 菜单内容会被整体重绘，所以用事件委托：点条目就收起，点下载源按钮就切换
    downloadMenu?.addEventListener("click", (e) => {
      const mb = e.target.closest("[data-mirror]");
      if (mb) {
        e.preventDefault();
        e.stopPropagation();
        saveMirrorPref(mb.dataset.mirror);
        fetchRelease(true);
        return;
      }
      if (e.target.closest(".ndm-item")) closeMenu();
    });
  }

  // ===================== 移动端菜单 =====================
  const toggle = document.querySelector(".nav-toggle");
  const links  = document.querySelector(".nav-links");
  if (toggle && links) {
    toggle.addEventListener("click", () => links.classList.toggle("open"));
    links.querySelectorAll("a").forEach((a) => {
      a.addEventListener("click", () => {
        links.classList.remove("open");
        closeMenu();
      });
    });
  }

  // ===================== 滚动渐入 =====================
  const io = new IntersectionObserver(
    (entries) => {
      entries.forEach((e) => {
        if (e.isIntersecting) {
          e.target.classList.add("visible");
          io.unobserve(e.target);
        }
      });
    },
    { threshold: 0.12, rootMargin: "0px 0px -40px 0px" }
  );
  document.querySelectorAll(".fade-in").forEach((el) => io.observe(el));
})();
