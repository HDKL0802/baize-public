# 白泽插件源 · 模板仓

这是给 [**白泽（Baize）**](https://github.com/HDKL0802/baize-public) 做**插件源**的模板仓。

白泽的插件源就是**一堆静态文件**：一个 `index.json` 加若干 `.zip`。
所以它**不需要任何服务器**——推到 GitHub 用 raw 直链就能当源，或者丢到任何静态托管 / 你自己的网盘目录都行。

```
你写插件目录  →  tools\build.ps1 打 zip + 算 sha256 + 写 index.json  →  推到 GitHub  →  白泽里填 raw 直链
```

- 插件里装什么？**技能**（SKILL.md）和 **MCP 服务**（在 `plugin.json` 里声明）。
- 装上以后：技能落进白泽的技能目录、MCP 服务登记进配置，白泽立刻就能用。
- **不会覆盖用户已有的东西**：与用户现有同名的技能 / MCP 服务一律跳过（插件的优先级永远低于用户自己那摊）。

---

## 快速开始（三步）

### 1. 写一个插件

复制 `plugins/example-hello/` 改成你自己的（**发布前把它删掉**）：

```
plugins/my-plugin/                  ← 目录名 = 插件 id（小写字母/数字/-/./_，字母或数字开头）
├── plugin.json                     ← 插件清单
└── skills/
    └── my-skill/                   ← 技能目录名 = slug（落盘位置，只能 ASCII）
        ├── SKILL.md                ← 技能本体（必须！）
        └── references/             ← 可选：参考文件，会跟着技能一起装到用户机器上
            └── checklist.md
```

一个插件可以带**多个技能**；`references/`、`templates/`、`scripts/`、`assets/` 是约定的支持文件目录。

### 2. 跑发布脚本

```powershell
powershell -ExecutionPolicy Bypass -File tools\build.ps1
```

它会：校验清单和技能 → 把每个 `plugins/<id>/` 打成 `packages/<id>-<版本>.zip` → 算 sha256 与大小
→ 汇总写 `index.json`。任何一处不对（清单缺字段、SKILL.md 没有 front-matter、id 与目录名对不上、
zip 根目录没有 `plugin.json`）都会**直接报错停下**，不会产出一个坏源。

### 3. 推到 GitHub，然后在白泽里加源

把 `packages/` 和 `index.json` **一起提交**（两者必须来自同一次构建），推到 GitHub 后，在
**白泽 → 插件市场 → 插件源** 里填 `index.json` 的 raw 直链：

```
https://raw.githubusercontent.com/<你的账号>/<你的仓>/main/index.json
```

也可以用接口加：

```bash
curl -X POST http://<白泽后端>/api/agent/plugins -H 'Content-Type: application/json' \
  -d '{"action":"addSource","name":"我的插件源","url":"https://raw.githubusercontent.com/…/index.json"}'
```

> 离线/内网也能用：地址填**本机路径**（如 `D:\my-plugins\index.json`）同样有效。

---

## `plugin.json` 字段

```json
{
  "schema": 1,
  "id": "my-plugin",
  "name": "我的插件",
  "version": "1.0.0",
  "description": "一句话说清这个插件是干什么的。",
  "author": "你的名字",
  "homepage": "https://github.com/你的账号/你的仓",
  "license": "MIT",
  "tags": ["工具", "写作"],
  "minBaize": "0.9.21",
  "mcp": [
    { "name": "my-mcp", "transport": "http", "url": "http://127.0.0.1:3000/mcp", "enabled": true }
  ]
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `schema` | 是 | 格式版本，现在是 `1` |
| `id` | 是 | 插件 id，**必须与目录名一致** |
| `name` | 是 | 展示名（可中文） |
| `version` | 是 | 版本号。改了内容就**递增它**，白泽那边才会显示"有更新" |
| `description` | 建议 | 货架上一句话说明 |
| `author` / `homepage` / `license` / `tags` / `minBaize` | 否 | 都只是展示信息；`minBaize` 用来提示需要的最低白泽版本 |
| `mcp` | 否 | 要登记进白泽配置的 MCP 服务（**同名不覆盖**，用户已配过的会被跳过） |

## `SKILL.md` 格式

```markdown
---
name: 展示名（可中文，界面与技能清单显示的就是它）
description: 一句话说清"什么时候该用我"，整行不超过 60 个字。
---

正文：照着做的步骤。每步都写到白泽**真实存在**的工具名上。
```

**两条硬规矩**（发布脚本会帮你挡一部分，但自己要清楚）：

1. **`description` 控制在 60 个字以内。** 它进的是白泽系统提示里的技能清单，超了会被截断——
   所以先写"触发场景"，别写成目录。
2. **工具名必须是真的。** 技能是给白泽照着做的，写错工具名等于这份技能是坏的。
   真实工具清单看白泽仓库的 `agent/internal/tools/`（`Name()` 方法）与 `agent/internal/kb/tools.go`。
   例：`web_fetch` / `web_search` / `browser` / `fs_read` / `fs_write` / `file_search` / `note` /
   `memory` / `cron` / `cron_remove` / `device_list` / `device_run` / `skill_manage`。

还有一条**容易踩的坑**：技能 slug **不要和内置技能撞名**（`make-skill`、`file-reader`、
`office-files`、`cron`、`note-taking`）。撞名的话装上去会被"同名已存在"整个跳过，插件看起来装了却什么都没多。

---

## 关于安全（白泽这边的口径）

- 源里给了 `sha256`（`build.ps1` 会写）就**逐字节校验**；没给就会在安装说明里**明说"没做完整性校验"**。
- 解包会挡 **zip slip**（`../`、绝对路径）、**符号链接**和**解压炸弹**。
- 包里声明的 `id` 与索引里登记的不一致 → **直接拒装**（防"货不对板"）。
- 与用户现有同名的技能 / MCP 服务 → **跳过**，并如实写进安装说明。
- 卸载是**归档**（技能与原始包一起挪进 `plugins/_removed/`，能捞回来）；停用是把技能挪进
  `plugins/store/<id>/.off`（保留用户改动），启用再挪回来。

## 维护自己的源

- **改内容** → 递增 `plugin.json` 的 `version` → 重跑 `tools\build.ps1` → 提交 `packages/` + `index.json`。
  白泽那边的"检查"会看到货架上的版本比装的高，标「有更新」。
- **别手改 `index.json`**：里面的 `sha256` / `size` 必须和 `packages/` 里的 zip 对得上。
- 想上 GitHub Pages 也行（把 raw 地址换成 Pages 地址即可）；`plugins[].url` 写的是**相对路径**，
  所以整个仓可以整体搬家到别的域名/目录，不用改内容。

## 许可证

本模板仓以 **MIT** 发布（见 `LICENSE`）——你可以随便改、随便拿去建自己的源。
你源里的**插件内容**用什么许可，由你自己定（在各自 `plugin.json` 的 `license` 里写清）。
