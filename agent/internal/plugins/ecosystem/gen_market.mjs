// 一次性生成器：把公网 DSH 生态索引快照（dsh-index.snapshot.json，格式 1）
// 转成白泽插件市场的紧凑 index.json。运行：node gen_market.mjs（离线，不联网）。
//
// 快照是来源凭证，保留不动；生成的 dsh-market.index.json 才是被 //go:embed 内嵌进
// 二进制的那份。之所以不直接 embed 8.4MB 原始快照：白泽只认自己的 index.json 格式，
// 原始快照是 DSH 生态格式（11141 条、每条带 versions/sortKeys/labels 等一大堆用不到的东西），
// 转成紧凑格式能省掉大半体积，也避免把"看起来能装"的假象带进市场。
//
// 字段映射见各条注释。注意 DSH 插件本体是 npm / cordis 运行时形态，白泽装不了，
// 所以每条的 url 指向 builtin:dsh-market/<id>.zip —— readDshMarket 会对这个地址明确报错，
// 绝不伪造可安装包（用户最恨"看起来能用其实不能用"）。

import { readFileSync, writeFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// 用脚本自身所在目录定位文件：从仓库根 / 任意 cwd 运行都能找到，不依赖 cwd。
const HERE = dirname(fileURLToPath(import.meta.url));
const SNAPSHOT = join(HERE, "dsh-index.snapshot.json");
const OUT = join(HERE, "dsh-market.index.json");

// ecosystemID 与 ecology.EcosystemID 等价（去 @、斜杠换点、小写、非法字符换连字符、截 64）。
// 独立实现一份，避免 ecosystem 包反向依赖 ecology（build.go 已经依赖 plugins，会成环）。
function ecosystemID(name) {
  let s = String(name ?? "").trim();
  if (s.startsWith("@")) s = s.slice(1);
  s = s.replaceAll("/", ".").toLowerCase();
  let out = "";
  for (const ch of s) {
    out += /[a-z0-9._-]/.test(ch) ? ch : "-";
  }
  // 去掉首尾的 . - _（与 Go 版 strings.Trim(s, ".-_") 等价）。
  // 注意别写成 [.-_] —— 那是 0x2E..0x5F 的字符范围，会把数字、大写字母、/ 都吃掉，
  // 导致不同包名被误判成同一个 id。这里直接"去掉首尾所有非字母数字"，最不容易踩坑。
  out = out.replace(/^[^a-z0-9]+|[^a-z0-9]+$/g, "");
  if (!out) return "";
  if (out.length > 64) out = out.slice(0, 64);
  return out;
}

// localized 取一份可读文本：字符串直接用；对象按 zh -> en -> 第一个值。
// DSH 快照里 title/description 是 {en,zh} 对象（title 多数没有 zh，description 多数有 zh）。
// name 与 description 都按"优先中文"取，与需求一致。
function localized(v) {
  if (v == null) return "";
  if (typeof v === "string") return v.trim();
  if (typeof v === "object") {
    for (const k of ["zh", "zh-CN", "zh-Hans", "en", "en-US"]) {
      if (typeof v[k] === "string" && v[k].trim()) return v[k].trim();
    }
    for (const val of Object.values(v)) {
      if (typeof val === "string" && val.trim()) return val.trim();
    }
  }
  return "";
}

const raw = readFileSync(SNAPSHOT, "utf8");
const snap = JSON.parse(raw);
if (snap.format !== 1 || !Array.isArray(snap.plugins)) {
  throw new Error("快照不是格式 1 的 DSH 索引，拒绝生成");
}

const plugins = [];
const seen = new Set();
let skippedEmptyID = 0;
let skippedDup = 0;
for (const p of snap.plugins) {
  const id = ecosystemID(p.name);
  // 空 id / 重复 id 的条目直接丢，保证生成后的条数与 normalizeIndex 之后 Catalog 看到的条数一致
  // （否则磁盘里写着 11141 条、界面只显示 11140 条，就是"看起来不一致"）。
  if (!id) { skippedEmptyID++; continue; }
  if (seen.has(id)) { skippedDup++; continue; }
  seen.add(id);

  // name：title.zh -> title.en -> 原始 name
  const title = localized(p.title);
  const name = title || String(p.name ?? "").trim() || id;
  // description：优先 description.zh，其次 en / 任意语言
  const description = localized(p.description);
  // version：取 versions[0].version
  const version = (Array.isArray(p.versions) && p.versions[0] && p.versions[0].version)
    ? String(p.versions[0].version).trim()
    : "0";
  // author / homepage / tags / category
  const author = typeof p.author === "string" ? p.author.trim() : "";
  const homepage = (p.repository && typeof p.repository.url === "string") ? p.repository.url.trim() : "";
  const tags = (p.metadata && Array.isArray(p.metadata.keywords))
    ? p.metadata.keywords.filter(x => typeof x === "string" && x.trim()).map(x => x.trim())
    : [];
  const category = (p.metadata && typeof p.metadata.category === "string")
    ? p.metadata.category.trim() : "";

  const meta = {
    id,
    name,
    version,
    channel: "community",
    stage: "stable",
    url: "builtin:dsh-market/" + id + ".zip",
  };
  if (description) meta.description = description;
  if (author) meta.author = author;
  if (homepage) meta.homepage = homepage;
  if (tags.length) meta.tags = tags;
  if (category) meta.category = category;
  plugins.push(meta);
}

const out = {
  schema: 1,
  name: "DSH 生态市场（内置）",
  updated: process.env.DSH_GEN_UPDATED || new Date().toISOString(),
  plugins,
};

// 紧凑输出：单行 JSON（无缩进），把 8.4MB 快照压到 2~3MB。
writeFileSync(OUT, JSON.stringify(out), "utf8");
const bytes = statSync(OUT).size;
console.log(JSON.stringify({
  snapshotBytes: Buffer.byteLength(raw, "utf8"),
  sourceCount: snap.plugins.length,
  generatedCount: plugins.length,
  skippedEmptyID,
  skippedDup,
  output: OUT,
  outputBytes: bytes,
  updated: out.updated,
}, null, 2));
