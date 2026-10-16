/* 白泽官网 · 交互脚本 v3.2
   1. 快速开始 4 卡片展开切换
   2. 代码块复制按钮
   3. 下载按钮：从 GitHub API 动态拉 latest release，自动填充三端直链（1h 缓存）
   4. 移动端导航菜单
   5. 滚动渐入动画
*/

(function () {
  "use strict";

  // ===================== 配置 =====================
  const GITHUB_OWNER = "HDKL0802";
  const GITHUB_REPO  = "baize";
  const RELEASE_API  = `https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/releases/latest`;
  const CACHE_KEY    = "baize-latest-release";
  const CACHE_TTL_MS = 60 * 60 * 1000;  // 1 小时

  // 匹配规则：从 release assets 里按文件名找到对应文件
  const ASSET_MATCHERS = [
    {
      key: "desktop",
      title: "Windows 桌面端",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="13" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /^baize-desktop-setup-\d[\d.]+\.exe$/.test(name),
    },
    {
      key: "android",
      title: "Android 手机端",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="2" width="14" height="20" rx="2"/><line x1="12" y1="18" x2="12.01" y2="18"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /^bz-todo-\d[\d.]+\.apk$/.test(name),
    },
    {
      key: "compose",
      title: "NAS · docker-compose",
      icon: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" y1="21" x2="16" y2="21"/><line x1="12" y1="17" x2="12" y2="21"/></svg>`,
      arrow: `<svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>`,
      match: (name) => /nas-docker-compose\.yml$/.test(name),
    },
  ];

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
  const releasesUrl   = `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases`;

  function setLoading() {
    if (!downloadMenu) return;
    downloadMenu.innerHTML = `<div class="ndm-loading">正在从 GitHub 获取最新版本…</div>`;
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
    // API 拿不到时的兜底：给个 GitHub Releases 链接
    if (!downloadMenu) return;
    downloadMenu.innerHTML = `
      <a class="ndm-item ndm-more" href="${releasesUrl}" target="_blank" rel="noopener">
        <div class="ndm-ic ndm-ic-more">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
        </div>
        <div class="ndm-body">
          <div class="ndm-title">GitHub Releases</div>
          <div class="ndm-sub">手动查看所有下载资源</div>
        </div>
        <svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
      </a>`;
  }
  function setMenuHtml(items) {
    if (!downloadMenu) return;
    let html = "";
    items.forEach((it) => {
      if (!it) return;
      const sizeMb = (it.asset.size / 1024 / 1024).toFixed(1);
      html += `
        <a class="ndm-item" href="${it.asset.browser_download_url}" target="_blank" rel="noopener" download>
          <div class="ndm-ic">${it.meta.icon}</div>
          <div class="ndm-body">
            <div class="ndm-title">${it.meta.title}</div>
            <div class="ndm-sub">${it.asset.name} · ${sizeMb} MB</div>
          </div>
          ${it.meta.arrow}
        </a>`;
    });
    html += `
      <div class="ndm-divider"></div>
      <a class="ndm-item ndm-more" href="${releasesUrl}" target="_blank" rel="noopener">
        <div class="ndm-ic ndm-ic-more">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
        </div>
        <div class="ndm-body">
          <div class="ndm-title">查看全部 Releases</div>
          <div class="ndm-sub">GitHub 版本页 · 含历史版本</div>
        </div>
        <svg class="ndm-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
      </a>`;
    downloadMenu.innerHTML = html;
  }

  function parseRelease(json) {
    const items = ASSET_MATCHERS.map((meta) => {
      const asset = (json.assets || []).find((a) => meta.match(a.name));
      return asset ? { asset, meta } : null;
    });
    return {
      tag: json.tag_name || "",
      name: json.name || "",
      published_at: json.published_at || "",
      items,
    };
  }

  async function fetchRelease() {
    // 读缓存
    try {
      const raw = localStorage.getItem(CACHE_KEY);
      if (raw) {
        const cached = JSON.parse(raw);
        if (Date.now() - cached.ts < CACHE_TTL_MS) {
          setMenuHtml(cached.data.items);
          return;
        }
      }
    } catch {}

    setLoading();
    try {
      const res = await fetch(RELEASE_API, {
        headers: { "Accept": "application/vnd.github+json" },
        cache: "no-store",
      });
      if (!res.ok) {
        // 匿名 API rate limit 通常是 403 / 429，404 是没 release
        throw new Error(`GitHub API ${res.status}`);
      }
      const json = await res.json();
      const data = parseRelease(json);
      // 缓存
      try {
        localStorage.setItem(CACHE_KEY, JSON.stringify({ ts: Date.now(), data }));
      } catch {}
      setMenuHtml(data.items);
    } catch (err) {
      console.warn("[baize] release fetch failed:", err.message);
      // 静默降级：给个兜底的 GitHub Releases 链接
      setFallback();
    }
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
    downloadWrap.querySelectorAll(".ndm-item").forEach((item) => {
      item.addEventListener("click", () => closeMenu());
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
