/*
 * 白泽官网 · 更新日志页
 * ------------------------------------------------------------------
 * 数据源：GitHub Releases（首选）→ Gitee Releases（镜像）→ 本地兜底
 * 约束：零构建、零第三方脚本、零 CDN；所有正文先 HTML 转义再解析。
 * 缓存键使用独立前缀 baize-changelog-v1-，与 app.js 下载菜单的缓存完全隔离。
 */
(function () {
  "use strict";

  var GH_API = "https://api.github.com/repos/HDKL0802/baize-public/releases?per_page=20";
  var GITEE_API = "https://gitee.com/api/v5/repos/HDKL0037/baize-public/releases";
  var GH_RELEASES_URL = "https://github.com/HDKL0802/baize-public/releases";
  var GITEE_RELEASES_URL = "https://gitee.com/HDKL0037/baize-public/releases";
  var FALLBACK_URL = "CHANGELOG.md";
  var CACHE_KEY = "baize-changelog-v1-releases";
  var CACHE_TTL_MS = 60 * 60 * 1000; // 1 小时
  var REQUEST_TIMEOUT_MS = 9000;
  var BT = String.fromCharCode(96); // 反引号字符，源码里不写裸反引号
  var NUL = String.fromCharCode(0);

  // 两个在线接口都不可用且 CHANGELOG.md 也取不到时的最后兜底：0.8.3 / 0.8.2 / 0.8.1 / 0.8.0 摘要
  // 正文里的 § 会在下面替换成真正的反引号（Markdown 行内代码）
  var EMBEDDED_FALLBACK = [
    "@@@ tag: desktop-v0.8.3",
    "@@@ name: 白泽桌面端 0.8.3",
    "@@@ date: 2026-11-14T22:30:00Z",
    "# 白泽 0.8.3",
    "",
    "> 补丁版本（+0.0.1）。0.8.2「多生态插件接入」的补齐版，把上一版如实挂着的六个缺口逐一收口。",
    "",
    "## 这一版做了什么",
    "",
    "- 修登记接口语义缺口：register 登记前校验白泽格式 index.json，DSH 原始格式 1 明确 400 并指引先 sync。",
    "- 探测改为有界并发（8 worker）+ 同仓库缓存（含负缓存）+ 单条 / 整轮双超时。",
    "- 桌面端内置「DSH 生态市场（内置）」只读源（离线快照 11141 条，含 143 个桌宠类插件），npm / cordis 形态明确禁用安装。",
    "- Trae 生态适配器可选开关（需自备白泽格式索引地址；Trae 自身格式本轮未逆向）。",
    "- 官网新增更新日志页，数据同步 GitHub Releases，失败回退 Gitee 再回退本地兜底。",
    "- 补三接口 handler 级测试、探测池测试、内置市场测试；WSL 跑 Go 全量测试；Docker 构建并起容器实测接口。",
    "",
    "## 不含（如实说明）",
    "",
    "- 未逆向 Trae，Trae 适配器只是自带索引地址的挂载钩子。",
    "- 内置 DSH 市场是离线快照，不会联网自动刷新。",
    "- 手机端沿用 bz-todo-v0.10.17（versionCode 1017）。",
    "",
    "@@@ tag: desktop-v0.8.2",
    "@@@ name: 白泽桌面端 0.8.2",
    "@@@ date: 2026-11-14T09:01:33Z",
    "# 白泽 0.8.2",
    "",
    "> 多生态插件接入：把别的 Agent 生态的插件市场接进白泽（当前支持 DSH 插件市场的索引格式 1）。",
    "",
    "## 这一版做了什么",
    "",
    "- 新增「生态插件源」能力：能读 DSH 插件市场的索引，按内容形态把插件分成技能型（带 SKILL.md）与工具型（带 MCP 配置）转译成白泽插件包；纯 cordis 运行时插件只进目录、不假装能装。",
    "- 新增接口：§GET /api/agent/ecology/dsh§（拉目录）、§POST /api/agent/ecology/dsh/sync§（转译）、§POST /api/agent/ecology/dsh/register§（登记插件源）。",
    "- 生态插件一律标为社区来源，逐条记录来源仓库与许可证；探测不到内容形态时如实说明「现在还说不准」，不与「确认不能装」混为一谈。",
    "- 全程只读第三方公开内容，不执行第三方代码、不自动安装。",
    "",
    "@@@ tag: desktop-v0.8.1",
    "@@@ name: 白泽桌面端 0.8.1",
    "@@@ date: 2026-11-14T04:27:27Z",
    "# 白泽 0.8.1",
    "",
    "> 补丁版本（+0.0.1）。把「技能」页的编辑改成读磁盘上的 SKILL.md 原文再改，不再用 name / description / 正文重组整篇。",
    "",
    "## 变更",
    "",
    "- 后端新增 §GET /api/agent/skills/raw?name=<技能名>§：返回磁盘原文 §{name, slug, content, bytes}§；缺参数 400、不存在 404、读盘失败 500，都是明确报错，不静默返回空内容。",
    "- 桌面端点「编辑」先拉原文；读取失败直接报错并停止编辑，不再用重组内容假装成功。保存时按原文整篇写回，front-matter 原样保留。",
    "- 新增后端测试 §TestSkillRawRead§，断言带 triggers / version 字段的 SKILL.md 能被原样读出。",
    "",
    "## 不含",
    "",
    "- 手机端沿用 bz-todo-v0.10.17（versionCode 1017），本版不动手机端。",
    "- 模型通道 / 外部 Agent / 频道 / 插件市场 / 人设 / 备份 / 可观测性 / 文件浏览器 / 独立知识库 / 内置浏览器在 0.8.0 已接线，本版无改动。",
    "",
    "@@@ tag: desktop-v0.8.0",
    "@@@ name: 白泽桌面端 0.8.0",
    "@@@ date: 2026-11-07T09:05:55Z",
    "# 白泽 0.8.0",
    "",
    "把剩下「后端已就绪、只差界面」的八个模块全部接上，并新增文件浏览器、独立知识库（含知识星图）与内置浏览器控制台。",
    "",
    "## 本版变化",
    "",
    "- 八个模块接上界面：模型通道、外部 Agent、频道、技能、插件市场、人设、备份、可观测。",
    "- 新增文件浏览器、独立知识库（含知识星图）与内置浏览器控制台。",
    "- 界面不编造后端没有的数据：无「剩余额度」查询接口就不显示额度，没有原文读取接口就明确警告。",
    "",
    "> 注意：这份是页面内嵌的离线摘要，只覆盖最近四个桌面版本，并非完整 Release 列表。联网恢复后请点「手动刷新」。",
    ""
  ].join("\n").replace(/§/g, BT);

  // ------------------------- DOM -------------------------
  var elList = document.getElementById("cl-list");
  var elSource = document.getElementById("cl-source");
  var elDot = document.getElementById("cl-dot");
  var elSync = document.getElementById("cl-sync-time");
  var elStatus = document.getElementById("cl-status");
  var elRefresh = document.getElementById("cl-refresh");
  var elError = document.getElementById("cl-error");
  var elErrorDetail = document.getElementById("cl-error-detail");
  var elFallbackNote = document.getElementById("cl-fallback-note");
  var elCount = document.getElementById("cl-count");

  var busy = false;

  // --------------------- 基础工具 ---------------------
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function stripBom(t) {
    return String(t == null ? "" : t).replace(/^\uFEFF/, "");
  }

  function normText(s) {
    return String(s == null ? "" : s)
      .replace(/\uFEFF/g, "")
      .replace(/\r\n/g, "\n")
      .replace(/\r/g, "\n");
  }

  function pad2(n) { return (n < 10 ? "0" : "") + n; }

  function fmtDate(raw, ts) {
    if (!raw && !ts) return "时间未知";
    var d = new Date(ts || raw);
    if (isNaN(d.getTime())) return esc(raw || "时间未知");
    return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) +
      " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes());
  }

  function fmtSync(ts) {
    if (!ts) return "—";
    var d = new Date(ts);
    if (isNaN(d.getTime())) return "—";
    return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) +
      " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds()) +
      "（浏览器本地时间）";
  }

  // URL 白名单：只允许 http(s) / mailto / 站内相对路径 / 锚点，挡住 javascript: data: 等
  function safeHref(escapedHref) {
    var raw = escapedHref.replace(/&amp;/g, "&");
    if (!/^(https?:\/\/|mailto:|\/|#)/i.test(raw)) return "";
    if (new RegExp("[\\s\\u0000-\\u001f]").test(raw)) return "";
    return escapedHref; // 已经过 HTML 转义，放进 href="..." 不会越界
  }

  // 行内 Markdown：入参必须是「已经 HTML 转义」的文本
  // 顺序：行内代码 → Markdown 链接 → 加粗 → 裸 URL 自动链接 → 还原占位符
  function inline(s) {
    var stash = [];
    function keep(html) {
      stash.push(html);
      return NUL + (stash.length - 1) + NUL;
    }

    // 1) 行内代码（先占位，避免代码里的 ** 被当加粗）
    var codeRe = new RegExp(BT + "([^" + BT + "\\n]+)" + BT, "g");
    s = s.replace(codeRe, function (_, code) {
      return keep("<code>" + code + "</code>");
    });

    // 2) [文字](链接)
    s = s.replace(/\[([^\]\n]*)\]\(([^)\s]+)\)/g, function (_, label, href) {
      var safe = safeHref(href);
      if (!safe) return "[" + label + "](" + href + ")";
      return keep('<a href="' + safe + '" target="_blank" rel="noopener noreferrer">' + label + "</a>");
    });

    // 3) 加粗
    s = s.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");

    // 4) 裸 URL（正文里确实存在，如 https://baize.wydhd.fun/nas/install.sh）
    s = s.replace(/(https?:\/\/[^\s<>"']+)/g, function (u) {
      var clean = u.replace(/[.,;:!?)\]]+$/, "");
      var safe = safeHref(clean);
      if (!safe) return u;
      return keep('<a href="' + safe + '" target="_blank" rel="noopener noreferrer">' + clean + "</a>") +
        u.slice(clean.length);
    });

    // 5) 还原占位符
    var stashRe = new RegExp(NUL + "(\\d+)" + NUL, "g");
    s = s.replace(stashRe, function (_, i) {
      return stash[Number(i)] || "";
    });
    return s;
  }

  function escInline(t) { return inline(esc(t)); }

  // --------------------- 轻量 Markdown 块级渲染 ---------------------
  var LIST_RE = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  var FENCE_RE = new RegExp("^\\s*(" + BT + BT + BT + "|~~~)\\s*([^\\s]*)\\s*$");
  var HEAD_RE = /^(#{1,6})\s+(.*)$/;
  var HR_RE = /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/;
  var FENCE_START = new RegExp("^\\s*(" + BT + BT + BT + "|~~~)");

  function renderList(lines, start) {
    var i = start;
    var roots = [];
    var stack = [];

    function pushCtx(tag, indent) {
      var ctx = { tag: tag, indent: indent, items: [] };
      if (stack.length) {
        var parent = stack[stack.length - 1];
        var pItem = parent.items[parent.items.length - 1];
        if (pItem) pItem.children.push(ctx);
      } else {
        roots.push(ctx);
      }
      stack.push(ctx);
    }

    while (i < lines.length) {
      var L = lines[i];
      var m = LIST_RE.exec(L);
      if (m) {
        var indent = m[1].replace(/\t/g, "  ").length;
        var tag = /^\d/.test(m[2]) ? "ol" : "ul";
        while (stack.length && stack[stack.length - 1].indent > indent) stack.pop();
        var top = stack[stack.length - 1];
        if (top && top.indent === indent && top.tag !== tag) stack.pop();
        if (!top || top.tag !== tag || top.indent < indent) pushCtx(tag, indent);
        stack[stack.length - 1].items.push({ text: m[3], children: [] });
        i++;
        continue;
      }
      // 缩进续行：并入上一个列表项（RELEASE_NOTES 里大量存在）
      if (/^\s{2,}\S/.test(L) && stack.length) {
        var ctx = stack[stack.length - 1];
        var it = ctx.items[ctx.items.length - 1];
        if (it) { it.text += " " + L.trim(); i++; continue; }
      }
      // 空行：后面还是列表 / 续行就吃掉，否则结束列表
      if (!L.trim()) {
        var j = i + 1;
        if (j < lines.length && (LIST_RE.test(lines[j]) || /^\s{2,}\S/.test(lines[j]))) { i++; continue; }
        break;
      }
      break;
    }

    function renderCtx(ctx) {
      var h = "<" + ctx.tag + ">";
      for (var k = 0; k < ctx.items.length; k++) {
        var item = ctx.items[k];
        h += "<li>" + escInline(item.text);
        for (var c = 0; c < item.children.length; c++) h += renderCtx(item.children[c]);
        h += "</li>";
      }
      return h + "</" + ctx.tag + ">";
    }
    var html = "";
    for (var r = 0; r < roots.length; r++) html += renderCtx(roots[r]);
    return { html: html, next: i };
  }

  function renderMarkdown(md) {
    var lines = normText(md).split("\n");
    var out = [];
    var para = [];
    var quote = [];
    var i = 0;

    function flushPara() {
      if (para.length) { out.push("<p>" + escInline(para.join(" ")) + "</p>"); para = []; }
    }
    function flushQuote() {
      if (quote.length) {
        out.push("<blockquote>" + renderMarkdown(quote.join("\n")) + "</blockquote>");
        quote = [];
      }
    }

    while (i < lines.length) {
      var L = lines[i];

      var fm = FENCE_RE.exec(L);
      if (fm) {
        flushPara(); flushQuote();
        var fence = fm[1];
        var code = [];
        i++;
        // 闭合围栏用纯字符串比较，避免正则转义
        while (i < lines.length && lines[i].trim() !== fence) { code.push(lines[i]); i++; }
        if (i < lines.length) i++;
        // 代码块内容只做转义，不做任何行内解析
        out.push("<pre><code>" + esc(code.join("\n")) + "</code></pre>");
        continue;
      }

      if (HR_RE.test(L)) { flushPara(); flushQuote(); out.push("<hr>"); i++; continue; }

      var hm = HEAD_RE.exec(L);
      if (hm) {
        flushPara(); flushQuote();
        var lv = Math.min(hm[1].length, 3);
        out.push("<h" + lv + ">" + escInline(hm[2].trim()) + "</h" + lv + ">");
        i++;
        continue;
      }

      if (/^\s*>\s?/.test(L)) {
        flushPara();
        quote.push(L.replace(/^\s*>\s?/, ""));
        i++;
        continue;
      }

      if (LIST_RE.test(L)) {
        flushPara(); flushQuote();
        var lr = renderList(lines, i);
        if (lr.html) out.push(lr.html);
        i = lr.next;
        continue;
      }

      if (!L.trim()) { flushPara(); flushQuote(); i++; continue; }
      para.push(L.trim());
      i++;
    }
    flushPara(); flushQuote();
    return out.join("\n");
  }

  /*
   * RELEASE_NOTES.md 是「累积全文」：每条 release 的 body 都含自身及之前所有版本的段落。
   * 直接渲染 20 条会把同一段内容重复 20 次，所以按版本抽出「本版新增段落」：
   * 遇到第二个 H1，或遇到后面紧跟 H1 的 --- 分隔线时截断（代码块内的不算）。
   * 完整的 body 仍在每条记录的「查看完整 Release 说明」里原样保留。
   */
  function extractDelta(body) {
    var lines = normText(body).split("\n");
    var inFence = false;
    var seenH1 = false;
    for (var i = 0; i < lines.length; i++) {
      var L = lines[i];
      if (FENCE_START.test(L)) { inFence = !inFence; continue; }
      if (inFence) continue;
      if (i > 0 && HR_RE.test(L)) {
        var j = i + 1;
        while (j < lines.length && !/\S/.test(lines[j])) j++;
        if (j < lines.length && /^#\s+\S/.test(lines[j])) {
          return lines.slice(0, i).join("\n").replace(/\s+$/, "");
        }
      }
      if (/^#\s+\S/.test(L)) {
        if (seenH1) return lines.slice(0, i).join("\n").replace(/\s+$/, "");
        seenH1 = true;
      }
    }
    return lines.join("\n").replace(/\s+$/, "");
  }

  // --------------------- 数据规范化 ---------------------
  function normalizeReleases(raw, source) {
    if (!Array.isArray(raw)) throw new Error("返回数据不是数组（可能被接口改写或代理拦截）");
    var out = [];
    for (var k = 0; k < raw.length; k++) {
      var r = raw[k];
      if (!r || typeof r !== "object") continue;
      var tag = typeof r.tag_name === "string" ? r.tag_name.trim() : "";
      if (!tag) continue;
      if (r.draft === true || r.prerelease === true) continue;
      var name = (typeof r.name === "string" && r.name.trim()) ? r.name.trim() : tag;
      var dateRaw = (typeof r.published_at === "string" && r.published_at) ||
        (typeof r.created_at === "string" && r.created_at) || "";
      var body = typeof r.body === "string" ? r.body : "";
      var url = (typeof r.html_url === "string" && /^https?:\/\//.test(r.html_url)) ? r.html_url :
        (source === "gitee" ? GITEE_RELEASES_URL : GH_RELEASES_URL);
      out.push({
        tag: tag,
        name: name,
        dateRaw: dateRaw,
        ts: Date.parse(dateRaw) || 0,
        body: body,
        url: url,
        source: source
      });
    }
    if (!out.length) {
      throw new Error("接口返回 " + raw.length + " 条记录，但没有可展示的有效发布（缺 tag_name 或全是草稿）");
    }
    out.sort(function (a, b) { return b.ts - a.ts; }); // 按发布时间倒序
    return out;
  }

  function parseFallback(md) {
    var lines = normText(md).split("\n");
    var out = [];
    var cur = null;
    for (var i = 0; i < lines.length; i++) {
      var m = /^@@@\s*(\w+)\s*:\s*(.*)$/.exec(lines[i]);
      if (m) {
        if (m[1] === "tag") {
          cur = { tag: m[2].trim(), name: "", dateRaw: "", url: "", body: [] };
          out.push(cur);
        } else if (cur) {
          var v = m[2].trim();
          if (m[1] === "name") cur.name = v;
          else if (m[1] === "date") cur.dateRaw = v;
          else if (m[1] === "url") cur.url = v;
        }
        continue;
      }
      if (cur) cur.body.push(lines[i]);
    }
    return out.filter(function (r) { return r.tag && r.body.length; }).map(function (r) {
      return {
        tag: r.tag,
        name: r.name || r.tag,
        dateRaw: r.dateRaw,
        ts: Date.parse(r.dateRaw) || 0,
        body: r.body.join("\n").trim(),
        url: r.url || "",
        source: "local"
      };
    });
  }

  // --------------------- 网络 ---------------------
  function fetchText(url, opts) {
    return new Promise(function (resolve, reject) {
      var ctrl = typeof AbortController !== "undefined" ? new AbortController() : null;
      var timer = setTimeout(function () {
        if (ctrl) ctrl.abort();
        reject(new Error("请求超时（" + REQUEST_TIMEOUT_MS / 1000 + " 秒）"));
      }, REQUEST_TIMEOUT_MS);
      var init = { cache: "no-store" };
      if (ctrl) init.signal = ctrl.signal;
      if (opts && opts.headers) init.headers = opts.headers;
      fetch(url, init).then(function (res) {
        if (!res.ok) { throw new Error("HTTP " + res.status + " " + (res.statusText || "")); }
        return res.text();
      }).then(function (t) {
        clearTimeout(timer);
        resolve(t);
      }).catch(function (e) {
        clearTimeout(timer);
        reject(e);
      });
    });
  }

  function fetchReleases(source) {
    var url = source === "gitee" ? GITEE_API : GH_API;
    var opts = source === "github" ? { headers: { "Accept": "application/vnd.github+json" } } : null;
    return fetchText(url, opts).then(function (t) {
      var data;
      try { data = JSON.parse(stripBom(t)); }
      catch (e) { throw new Error("返回内容不是合法 JSON（" + e.message + "）"); }
      return normalizeReleases(data, source);
    });
  }

  function fetchFallbackFile() {
    return fetchText(FALLBACK_URL).then(function (t) {
      var list = parseFallback(t);
      if (!list.length) throw new Error("CHANGELOG.md 里没有解析出任何记录");
      return list;
    });
  }

  // --------------------- 缓存 ---------------------
  function readCache() {
    try {
      var raw = localStorage.getItem(CACHE_KEY);
      if (!raw) return null;
      var c = JSON.parse(raw);
      if (!c || typeof c.ts !== "number" || !Array.isArray(c.data) || !c.data.length) return null;
      return {
        data: c.data,
        ts: c.ts,
        source: c.source === "gitee" ? "gitee" : "github",
        fresh: (Date.now() - c.ts) < CACHE_TTL_MS
      };
    } catch (e) { return null; }
  }

  function saveCache(data, source) {
    try {
      localStorage.setItem(CACHE_KEY, JSON.stringify({ ts: Date.now(), source: source, data: data }));
    } catch (e) { /* 隐私模式 / 配额满：不影响页面展示 */ }
  }

  // --------------------- 视图 ---------------------
  function setBusy(on) {
    busy = on;
    if (elRefresh) {
      elRefresh.disabled = on;
      elRefresh.classList.toggle("spin", on);
    }
    if (elDot && on) elDot.className = "cl-dot busy";
  }

  function setStatus(text, tone) {
    if (!elStatus) return;
    elStatus.textContent = "";
    var span = document.createElement("span");
    if (tone === "ok") span.className = "cl-ok";
    else if (tone === "warn") span.className = "cl-warn";
    span.textContent = text;
    elStatus.appendChild(span);
  }

  function setMeta(sourceLabelText, syncedAt, fromCache, ok) {
    if (elSource) elSource.textContent = sourceLabelText;
    if (elDot) elDot.className = "cl-dot " + (ok ? "ok" : "warn");
    if (elSync) elSync.textContent = fmtSync(syncedAt) + (fromCache ? "（缓存）" : "");
  }

  function hideError() { if (elError) elError.classList.remove("show"); }
  function showError(reasons) {
    if (!elError) return;
    if (elErrorDetail) elErrorDetail.textContent = reasons;
    elError.classList.add("show");
  }
  function showFallbackNote(text) {
    if (!elFallbackNote) return;
    elFallbackNote.textContent = text;
    elFallbackNote.classList.add("show");
  }
  function hideFallbackNote() { if (elFallbackNote) elFallbackNote.classList.remove("show"); }

  function sourceLabel(src, extra) {
    if (src === "gitee") return "数据来自 Gitee Releases（GitHub 不可用）" + (extra || "");
    if (src === "local") return "数据来自本地兜底 · 非在线最新数据" + (extra || "");
    return "数据来自 GitHub Releases" + (extra || "");
  }

  function renderReleases(list, meta) {
    if (!elList) return;
    elList.textContent = "";
    var frag = document.createDocumentFragment();

    for (var k = 0; k < list.length; k++) {
      var rel = list[k];
      var entry = document.createElement("article");
      entry.className = "cl-entry " + (k === 0 ? "cl-latest" : "cl-not-latest");

      var head = document.createElement("div");
      head.className = "cl-entry-head";

      var tagEl = document.createElement("span");
      tagEl.className = "cl-tag";
      tagEl.textContent = rel.tag;

      var nameEl = document.createElement("span");
      nameEl.className = "cl-entry-name";
      nameEl.textContent = rel.name;

      var dateEl = document.createElement("time");
      dateEl.className = "cl-date";
      dateEl.textContent = fmtDate(rel.dateRaw, rel.ts);

      head.appendChild(tagEl);
      head.appendChild(nameEl);
      head.appendChild(dateEl);

      if (rel.url && /^https?:\/\//.test(rel.url)) {
        var link = document.createElement("a");
        link.className = "cl-gh-link";
        link.href = rel.url;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = (meta.source === "gitee" ? "在 Gitee 查看" : "在 GitHub 查看") + " ↗";
        head.appendChild(link);
      }

      var body = document.createElement("div");
      body.className = "cl-body cl-md";
      // 兜底数据已是单版本摘要；在线数据做累积全文去重
      var delta = meta.fallback ? rel.body : extractDelta(rel.body);
      body.innerHTML = renderMarkdown(delta); // 渲染器内部已对原始文本做 HTML 转义

      entry.appendChild(head);
      entry.appendChild(body);

      // 在线数据额外提供「完整 Release 说明」，原样保留 body，不做去重
      if (!meta.fallback && rel.body && rel.body !== delta) {
        var details = document.createElement("details");
        details.className = "cl-full";
        var summary = document.createElement("summary");
        summary.textContent = "查看完整 Release 说明（累积全文）";
        var full = document.createElement("div");
        full.className = "cl-md";
        full.innerHTML = renderMarkdown(rel.body);
        details.appendChild(summary);
        details.appendChild(full);
        entry.appendChild(details);
      }

      frag.appendChild(entry);
    }
    elList.appendChild(frag);
    if (elCount) elCount.textContent = String(list.length);
  }

  function showLoading(text) {
    if (!elList) return;
    elList.textContent = "";
    var d = document.createElement("div");
    d.className = "cl-loading";
    d.textContent = text;
    elList.appendChild(d);
  }

  // --------------------- 主流程 ---------------------
  function load(force) {
    if (busy) return;
    hideError();
    hideFallbackNote();
    setBusy(true);

    var cache = readCache();

    // 1) 非强制刷新且缓存新鲜：直接用缓存，明确标注「（缓存）」
    if (!force && cache && cache.fresh) {
      renderReleases(cache.data, { source: cache.source, fallback: false });
      setMeta(sourceLabel(cache.source), cache.ts, true, true);
      setStatus("已加载本地缓存（1 小时内有效）。点「手动刷新」可强制重新拉取在线数据。", "ok");
      setBusy(false);
      return;
    }

    setStatus(force ? "正在重新拉取发布记录…" : "正在从 GitHub Releases 拉取发布记录…", "");
    // 加载中：明确显示「连接中」，绝不提前亮绿灯假装成功
    if (elSource) elSource.textContent = "正在连接数据源…";
    if (elDot) elDot.className = "cl-dot busy";
    if (elSync) elSync.textContent = cache ? fmtSync(cache.ts) + "（上次缓存）" : "—";
    showLoading("正在加载发布记录…");

    var reasons = [];
    var online = null;

    // 2) GitHub 首选
    fetchReleases("github").then(function (list) {
      online = { list: list, source: "github" };
    }).catch(function (e) {
      reasons.push("GitHub：" + (e && e.message ? e.message : String(e)));
      setStatus("GitHub 接口不可用，正在尝试 Gitee 镜像…", "warn");
      // 3) Gitee 回退
      return fetchReleases("gitee").then(function (list) {
        online = { list: list, source: "gitee" };
      }).catch(function (e2) {
        reasons.push("Gitee：" + (e2 && e2.message ? e2.message : String(e2)));
      });
    }).then(function () {
      var now = Date.now();
      if (online) {
        saveCache(online.list, online.source);
        renderReleases(online.list, { source: online.source, fallback: false });
        setMeta(sourceLabel(online.source), now, false, true);
        setStatus("同步成功，共 " + online.list.length + " 条发布记录，按发布时间倒序排列。", "ok");
        setBusy(false);
        return;
      }

      // 4) 两个在线接口都失败：绝不静默，先显式报错
      showError(reasons.join("；") || "未知错误");

      // 4a) 有过期缓存：显示缓存，但明确说明「不是最新数据」
      if (cache) {
        renderReleases(cache.data, { source: cache.source, fallback: false });
        setMeta(sourceLabel(cache.source, " · 缓存已过期"), cache.ts, true, false);
        showFallbackNote("在线接口均不可用，下面显示的是 " + fmtSync(cache.ts) +
          " 保存的本地缓存，可能不是最新数据。恢复联网后请点「手动刷新」。");
        setStatus("在线接口均不可用，已显示过期缓存（不假装是最新数据）。", "warn");
        setBusy(false);
        return;
      }

      // 4b) 本地兜底文件
      return fetchFallbackFile().then(function (list) {
        useFallback(list, "web/CHANGELOG.md");
      }).catch(function (e3) {
        reasons.push("CHANGELOG.md：" + (e3 && e3.message ? e3.message : String(e3)));
        // 4c) 最后的内嵌摘要
        showError(reasons.join("；"));
        useFallback(parseFallback(EMBEDDED_FALLBACK), "页面内嵌摘要");
      });
    }).catch(function (fatal) {
      // 理论上的兜底：任何未预料异常也不能让页面空白
      showError(reasons.concat(["内部错误：" + (fatal && fatal.message ? fatal.message : String(fatal))]).join("；"));
      useFallback(parseFallback(EMBEDDED_FALLBACK), "页面内嵌摘要");
    });
  }

  function useFallback(list, label) {
    renderReleases(list, { source: "local", fallback: true });
    setMeta(sourceLabel("local", " · " + label), 0, false, false);
    showFallbackNote("当前显示的是 " + label +
      "，只覆盖最近几个版本，不代表 GitHub Releases 的完整列表；联网恢复后请点「手动刷新」。");
    setStatus("在线接口均不可用，已显示 " + label + "（" + list.length + " 条，非在线最新数据）。", "warn");
    setBusy(false);
  }

  if (elRefresh) {
    elRefresh.addEventListener("click", function () { load(true); });
  }

  load(false);
})();
