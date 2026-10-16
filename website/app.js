/* 白泽官网 · 交互脚本 v3.1
   1. 快速开始 4 卡片展开切换
   2. 代码块复制按钮
   3. 下载按钮下拉菜单（hover + click + 外部点击 + ESC）
   4. 移动端导航菜单
   5. 滚动渐入动画
*/

(function () {
  "use strict";

  // ---------- 快速开始：4 卡片展开切换 ----------
  const cards = document.querySelectorAll(".deploy-card");
  const panes = document.querySelectorAll(".deploy-pane");

  function activate(card) {
    const name = card.dataset.card;
    cards.forEach((c) => c.classList.toggle("on", c === card));
    panes.forEach((p) => p.classList.toggle("on", p.dataset.pane === name));
  }

  // 默认激活第一个（后端）
  const firstCard = document.querySelector(".deploy-card.on") || cards[0];
  if (firstCard) activate(firstCard);

  cards.forEach((card) => {
    card.addEventListener("click", () => activate(card));
  });

  // ---------- 代码块复制 ----------
  document.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      e.stopPropagation();
      const block = btn.parentElement;
      const text = block.innerText.replace(/复制|已复制/g, "").trim();
      try {
        await navigator.clipboard.writeText(text);
        btn.textContent = "已复制";
        btn.classList.add("copied");
        setTimeout(() => { btn.textContent = "复制"; btn.classList.remove("copied"); }, 1600);
      } catch (err) {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        btn.textContent = "已复制";
        btn.classList.add("copied");
        setTimeout(() => { btn.textContent = "复制"; btn.classList.remove("copied"); }, 1600);
      }
    });
  });

  // ---------- 下载按钮：下拉菜单 ----------
  const downloadWrap = document.querySelector(".nav-download");
  const downloadBtn = document.querySelector(".nav-download-btn");

  if (downloadWrap && downloadBtn) {
    function openMenu() {
      downloadWrap.classList.add("open");
      downloadBtn.setAttribute("aria-expanded", "true");
    }
    function closeMenu() {
      downloadWrap.classList.remove("open");
      downloadBtn.setAttribute("aria-expanded", "false");
    }

    // 点击 toggle
    downloadBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      downloadWrap.classList.toggle("open");
      const open = downloadWrap.classList.contains("open");
      downloadBtn.setAttribute("aria-expanded", String(open));
    });

    // hover 也展开（桌面端更顺手）
    downloadWrap.addEventListener("mouseenter", openMenu);
    downloadWrap.addEventListener("mouseleave", closeMenu);

    // 点击外部关闭
    document.addEventListener("click", (e) => {
      if (!downloadWrap.contains(e.target)) closeMenu();
    });

    // ESC 关闭
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") closeMenu();
    });

    // 菜单项点击后（如果在移动端菜单里）
    downloadWrap.querySelectorAll(".ndm-item").forEach((item) => {
      item.addEventListener("click", () => closeMenu());
    });
  }

  // ---------- 移动端菜单 ----------
  const toggle = document.querySelector(".nav-toggle");
  const links = document.querySelector(".nav-links");
  if (toggle && links) {
    toggle.addEventListener("click", () => links.classList.toggle("open"));
    links.querySelectorAll("a").forEach((a) => {
      a.addEventListener("click", () => {
        links.classList.remove("open");
        closeDownloadIfOpen();
      });
    });
  }
  // 移动端菜单打开时也关闭下载菜单
  function closeDownloadIfOpen() {
    if (downloadWrap) {
      downloadWrap.classList.remove("open");
      downloadBtn?.setAttribute("aria-expanded", "false");
    }
  }

  // ---------- 滚动渐入 ----------
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
