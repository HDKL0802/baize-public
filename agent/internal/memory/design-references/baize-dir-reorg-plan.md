# 白泽 · 项目目录整理建议
> 2026-10-24 ｜ 基于当前仓库 `D:\xm\白泽智能体` 实际结构（已探查）
> 原则：**只改目录归属与命名，不改代码逻辑**；先出建议，确认后再动，避免影响 git/CI/go module。

---

## 一、现状诊断（为什么乱）
| 问题 | 具体表现 |
|---|---|
| 后端包颗粒过细 | `agent/internal/` 下 30 个平铺包（accounts/agentrt/agentsvc/channels/llm/memory/kb/skills/httpapi/hub/conflicts/...），无分组，难导航 |
| 前端散乱 | `desktop/ui/` 手写 `apps.js`~`apps9.js` 共 9 个文件 + shell.*，无构建体系（正好要换 Vue3） |
| 职责边界不清 | `core/` 只有 bin/cmd/testdata（名不副实）；`shared/` 又有 client/proto（和 core 重复） |
| 杂物混入 | `agent/` 下有 `.demo/.demo-ext/.sweep` 隐藏目录、`agent/ui` 空目录 |
| 发布工具混放 | `tools/` 混合了子工具(local-embed/sshrun)和发布脚本(ps1) |
| 文档无家 | 交接文档/开发文档在仓库外（聊天目录），仓库内无 docs/ |
| 多端目录分散 | `android-app/` 和 `todo-app/` 是两个端，未归类 |
| 前端=官网 | `website/`（静态官网）与桌面前端(desktop/ui)命名易混 |

---

## 二、建议目标结构
```
baize/  (D:\xm\白泽智能体)
├── backend/                 # ← 原 agent/（后端核心）
│   ├── cmd/
│   ├── data/
│   └── internal/
│       ├── api/             # httpapi, proto
│       ├── core/            # agentrt, agentsvc, executor, hooks, slash, cron
│       ├── ai/              # llm, memory, kb, skills, voice
│       ├── channel/         # channels
│       ├── domain/          # accounts, conflicts, plugins, calls
│       ├── infra/           # store, config, logx, observe, sysinfo, activity, backup
│       └── tools/           # tools, mcp
├── core/                    # 共享内核（真正放公共逻辑：协议/公共类型），与 backend 边界清晰
├── desktop/                 # Windows 桌面端
│   ├── app/                 # Go 壳（ball/tray/shot/gui/browser/shell... _windows.go）
│   └── webui/               # ← 新 Vue3 前端（替代 ui/ 9个js），Vite 构建产物放 dist/
├── mobile/                  # ← android-app/ + todo-app/ 合并
├── web/                     # ← 原 website/（官网）
├── deploy/                  # nas-docker-compose.yml + 部署脚本（从 tools 迁入）
├── tools/                   # 开发子工具（local-embed, sshrun）
├── scripts/                 # 发布脚本（release-*.ps1, deploy-nas.ps1）从 tools 拆出
├── third_party/             # vendored 依赖（hermes-agent/openhuman/picoclaw/qwenpaw）
├── docs/                    # 交接文档、开发文档、本清单都迁入
└── .github/  .trae/         # 保留
```

---

## 三、迁移动作清单（照做，分步、可回滚）
| # | 动作 | 影响/注意 |
|---|---|---|
| 1 | `agent/` → `backend/` | 改 go module 路径 + 所有 import（**大动作，建议用脚本批量替换 + 编译验证**） |
| 2 | `agent/internal` 30 包按上表分组到 6 个子目录 | 纯移动，无逻辑改动；逐组做、每组编译过再下一组 |
| 3 | `desktop/ui/` 9 个 js 作废，新建 `desktop/webui/`（Vue3 工程） | 与前端改造并行，前端重做时顺带完成 |
| 4 | `android-app/` + `todo-app/` → `mobile/` 下两个子目录 | 纯移动 |
| 5 | `website/` → `web/` | 纯移动（git mv） |
| 6 | `tools/` 拆：发布脚本 → `scripts/`，子工具留 `tools/` | 纯移动 |
| 7 | `deploy/` 并入部署脚本 | 从 tools/deploy-nas.ps1 迁入 |
| 8 | 新建 `docs/`，把交接/开发文档从聊天目录复制进来 | 建议 + 提交 |
| 9 | 清理：删 `agent/.demo/.demo-ext/.sweep`、空 `agent/ui` | 先确认无引用再删 |
| 10 | `core/` 与 `shared/` 合并职责 | 梳理后定：core 放公共逻辑，shared 拆掉并入 core |

---

## 四、注意（避免踩坑）
1. **用 `git mv` 移动**，保留历史；每步独立提交，可回滚。
2. **go module 路径改动影响大**：先改根 module 名（如 baize/backend），再全局替换 import 前缀，最后 `go build ./...` 全量编译验证。
3. **CI/发布脚本里的路径引用**（.github/workflows、build.ps1、installer.nsi）要同步改。
4. **目录整理与前端 Vue3 改造可以并行**：目录整理主要动 backend，前端重做主要动 desktop/ui → 互不阻塞。
5. 确认后再动手；**一步一提交，编译过了再继续**。

---

## 五、快速见效的低风险项（可以立刻做）
- [x] 新建 `docs/`，把交接文档/开发文档/待办清单/本建议放进仓库（✅ 2026-10-24 完成）
- [x] 清理 `agent/.demo`、`.sweep`、空 `agent/ui`（✅ 2026-10-24 完成，均未被 git 跟踪、无代码引用）
- [x] `website/` → `web/`（git mv，✅ 2026-10-24 完成；同步改 pages.yml）
- [x] `android-app/`+`todo-app/` → `mobile/`（git mv，✅ 2026-10-24 完成；同步改 .gitignore/.dockerignore/docker.yml/build.ps1/README/LICENSE/官网页）
> 高风险项（backend 重命名、internal 分组）放到前端改造完成后再做，避免两个大动作叠加出错。
