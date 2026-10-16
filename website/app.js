/* 白泽官网 · 交互脚本 v3
   1. 快速开始 4 卡片展开切换（点击卡片显示对应代码块）
   2. 代码块复制按钮
   3. 移动端导航菜单
   4. 滚动渐入动画
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
      // 防止冒泡到 deploy-card
      e.stopPropagation();

      const block = btn.parentElement;
      const text = block.innerText.replace(/复制|已复制/g, "").trim();
      try {
        await navigator.clipboard.writeText(text);
        btn.textContent = "已复制";
        btn.classList.add("copied");
        setTimeout(() => {
          btn.textContent = "复制";
          btn.classList.remove("copied");
        }, 1600);
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
        setTimeout(() => {
          btn.textContent = "复制";
          btn.classList.remove("copied");
        }, 1600);
      }
    });
  });

  // ---------- 移动端菜单 ----------
  const toggle = document.querySelector(".nav-toggle");
  const links = document.querySelector(".nav-links");
  if (toggle && links) {
    toggle.addEventListener("click", () => links.classList.toggle("open"));
    links.querySelectorAll("a").forEach((a) => {
      a.addEventListener("click", () => links.classList.remove("open"));
    });
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
