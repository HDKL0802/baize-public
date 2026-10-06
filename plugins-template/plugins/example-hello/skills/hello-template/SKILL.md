---
name: 技能模板示例
description: 示例技能：照着这个结构写你自己的。
---

# 技能模板（照抄这份）

这个文件就是**白泽技能的全部格式**。把它复制成你自己的技能，改掉上下的 front-matter 和正文即可。

## 1. 文件放在哪

```
plugins/<插件id>/              ← plugin.json 在这一级
├── plugin.json               ← 插件清单（见下节）
└── skills/
    └── <技能slug>/            ← 目录名只能小写字母/数字/连字符/点/下划线，且以字母或数字开头
        ├── SKILL.md           ← 就是本文件
        └── references/        ← 可选：参考/模板/脚本，跟着技能一起装到用户机器上
```

一个插件可以带**多个技能**（`skills/` 下多个目录）；`references/`、`templates/`、`scripts/`、`assets/` 这四个子目录是约定的存放处。

## 2. front-matter（必须有，且必须是文件开头）

```yaml
---
name: 展示名（可以中文，界面上和技能清单里显示的就是它）
description: 一句话说清"什么时候该用我"。整行不超过 60 个字，超了会被截断。
---
```

- `name` 是**展示名**，随便起（中文也行）；目录名（slug）才是落盘位置，只能 ASCII。
- `description` 会进系统提示的技能清单，**只展示前 60 个字**——所以先把"触发场景"写进去，别写成目录。

## 3. 正文写什么

正文是给**模型**看的操作手册，不是给人看的介绍。最有用的写法：

1. **前提**：做这件事之前要先确认什么（缺什么就明确说缺，别硬来）。
2. **步骤**：编号写清，每步都落到**白泽真实存在的工具名**上（`web_fetch` / `file_search` / `note` / `cron` …）。
3. **别做**：明确列出容易做错的地方（往哪写会踩线、什么不能伪造）。

## 4. 一条硬规矩：工具名必须是真的

技能是给白泽照着做的，**工具名写错就等于这份技能是坏的**。只写白泽真实注册的工具，例如：

| 工具 | 干什么 |
|---|---|
| `web_fetch` / `web_search` | 抓网页正文 / 联网搜索（搜索要先在配置里指定通道） |
| `browser` | 真浏览器：动态页取渲染后正文、跑 JS、截图 |
| `fs_read` / `fs_write` / `fs_list` / `file_search` | 工作目录里的文件读写与搜索 |
| `note` / `memory` | 成篇的笔记（记忆星图）/ 零散长期记忆 |
| `cron` / `cron_remove` | 定时任务 |
| `device_list` / `device_run` | 派活到用户的电脑/手机 |
| `skill_manage` | 让白泽自己沉淀技能 |

（完整清单看白泽仓库 `agent/internal/tools/` 与 `agent/internal/kb/tools.go` 里的 `Name()`。）

## 5. 写完怎么发布

回到模板仓根目录，跑一次发布脚本：

```powershell
powershell -ExecutionPolicy Bypass -File tools\build.ps1
```

它会把 `plugins/<id>/` 打成 `packages/<id>-<版本>.zip`、算好 sha256，并写好 `index.json`。
然后把这个仓推到 GitHub、在「插件市场 → 插件源」里填它 `index.json` 的直链（raw）就行。
细节见仓库根目录的 `README.md`。

> 现在把本文件里讲的东西删掉，换成你真正的技能吧。
