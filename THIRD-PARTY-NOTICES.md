# 第三方开源与许可（THIRD-PARTY NOTICES）

> 生成：2026-10-04。原则：**不猜**——许可证名称均据各项目/模块内置的 `LICENSE` 原文判定（扫描方法见文末）。
> 本项目本体拟以 **MIT** 发布；**但下面的「参考项目」里有一个是 GPL-3.0，见 §3，需先决策**。

---

## 1. 直接依赖（编译进产物）

### 1.1 Go 模块（后端 / 内核 / 桌面端）
| 模块 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `github.com/gorilla/websocket` | v1.5.3 | **BSD-2-Clause** | 设备 WebSocket 传输 |
| `modernc.org/sqlite` | v1.59.0 | **BSD-3-Clause** | 纯 Go SQLite（后端 DB） |
| `modernc.org/libc` | v1.75.7 | BSD-3-Clause | 上者的间接依赖 |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause | 同上 |
| `modernc.org/memory` | v1.12.1 | BSD-3-Clause | 同上 |
| `github.com/dustin/go-humanize` | v1.0.1 | **MIT** | 人类可读数字/大小 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | UUID |
| `github.com/mattn/go-isatty` | v0.0.24 | **MIT** | 是否 TTY |
| `github.com/ncruces/go-strftime` | v1.0.0 | **MIT** | 时间格式化 |
| `github.com/remyoudompheng/bigfft` | v0.0.0-2023… | BSD-3-Clause | 大数运算（间接） |
| `golang.org/x/sys` | v0.47.0 | BSD-3-Clause | 系统调用 |

### 1.2 Go 模块（仅桌面端）
| 模块 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `github.com/jchv/go-webview2` | v0.0.0-20260205173254-56598839c808 | **MIT** | 纯 Go 的 WebView2 绑定（无 cgo） |
| `github.com/jchv/go-winloader` | v0.0.0-20250406163304-c1995be93bd1 | **模块内未找到 LICENSE 文件** ⚠️ | 上者的间接依赖；**发布前需向上游确认** |

### 1.3 前端 / 其它
- 手机端 WebView 前端为本仓库自写（`todo-app/`），未引入第三方 JS 库。
- Android 侧仅用系统 API，未引入第三方 AAR。

---

## 2. 借鉴 / 参考的开源项目（`third_party/`，**不进版本库**）

| 项目（版本） | 许可证 | 我们怎么用 |
|---|---|---|
| **QwenPaw** `2.2.2b4`（agentscope-ai） | **Apache-2.0** | 参考其「Web 控制台 + 桌面端外壳」的交互与功能划分；桌面端外壳为本项目自写（Go + 原生 HTML/CSS/JS）。<br>**功能移植**（2026-10-05 起）：persona（人设）/ heartbeat（心跳）/ 魔法命令 / channels（IM 渠道）等能力参照其设计与文档，**用 Go 重写**；Apache-2.0 允许此类衍用，本文件保留其署名与许可。 |
| **Hermes-Agent** `0.0.0`（pyproject 占位版本；Nous Research） | **MIT** | 「创造技能」能力参考其设计，已**用 Go 重写**为 `agent/internal/skills` |
| **PicoClaw** `dev`（构建期由 ldflags 注入，源码里无固定版本号） | **MIT** | 渠道/工具设计参考 |
| **OpenHuman** `0.63.33` ⚠️ | **GPL-3.0** | 记忆术 / 语音路由参考其设计，并**用 Go 重写**为 `agent/internal/memory`、`agent/internal/voice` —— **见 §3** |

> ⚠️ **版本对齐说明（2026-10-05 核对）**：`third_party/openhuman` 已到 `0.63.33`，其记忆引擎已抽成独立 crate `tinymemory-core`（含 conversations / goals / sources / guard / 6 种检索原语等）。本项目 `agent/internal/memory` 对齐的是**抽取之前的旧代实现**（混合检索 + 记忆树），并未跟进 `tinymemory-core` 的新形态；两者「对不上」属预期，后续按需再补。

---

## 3. OpenHuman 是 GPL-3.0 —— 已决策采用**方案 B（双许可）**

**问题**：`third_party/openhuman/LICENSE` 是 **GNU GPL v3**。本项目最初的口径是「把 OpenHuman 的记忆术与实时语音**用 Go 重写**」。
对 GPL-3 代码做翻译/改写，法律上通常构成"衍生作品" → 衍生部分必须同样以 GPL-3 授权；这与「公开仓用 MIT」冲突。

**✅ 决定（2026-10-04）：方案 B —— 双许可拆分**
- 仓库**主体 → MIT**（根目录 `LICENSE`）：后端中枢、共享契约、手机内核与领域、桌面端、手机壳、WebView 前端、CLI、部署工具。
- 仅以下两个目录 → **GPL-3.0**（各自目录内放 `LICENSE` 说明；许可证原文见根目录 `LICENSE-GPL-3.0.txt`）：
  - `agent/internal/memory/`（记忆术：混合检索 / 自动召回 / 记忆树）
  - `agent/internal/voice/`（语音通道：TTS / STT）
- 私有仓与公开仓**许可口径一致**（同一份代码），README 需写明这个边界。

> 决策时评估过的其它路线（备查）：**A** 整体改 GPL-3（最省事但放弃 MIT）；**C** 公开仓剔除这两个包（功能不全）；**D** 独立重实现（唯一能保持全 MIT，但要重写且难以事后举证）。

| 方案 | 说明 | 代价 |
|---|---|---|
| **A. 公开仓整体改 GPL-3** | 最省事、最稳妥 | 放弃 MIT；他人二次开发也必须开源 |
| **B. 双许可（拆分）** | 其余部分 MIT，OpenHuman 衍生部分标 GPL-3 | 仓库内混许可，边界要写清楚，稍麻烦 |
| **C. 剔除 GPL 部分** | 公开仓**不含** `internal/memory`、`internal/voice`（留私有仓） | 公开版本功能不全 |
| **D. 独立重实现（clean-room）** | 只按公开的技术思路（混合检索加权、TTS/STT 路由等通用做法）重新设计，不参照其代码 | 需要重写 + 留存"未参照源码"的说明，事后举证困难 |

> 说明：**通用算法与设计思想本身不受版权保护**，受保护的是"具体表达"。
> 若现有实现是逐段照抄式翻译，属衍生作品；若是独立设计，则不算。**这需要你确认当初重写的程度**。
> `agent/internal/memory`（混合检索四档权重、MMR、三闸门）与 `internal/voice`（OpenAI/DashScope 两实现）里的**具体权重数值**是白泽自己标定的，但整体结构是照着 OpenHuman 对齐的。

---

## 4. 发布前检查清单（公开仓）

- [ ] 根目录放 `LICENSE`（MIT，含双许可范围说明）与 `LICENSE-GPL-3.0.txt`
- [ ] `agent/internal/memory/` 与 `agent/internal/voice/` 目录内各有 `LICENSE`（标注 GPL-3.0）
- [ ] 带上本文件 `THIRD-PARTY-NOTICES.md`
- [ ] `github.com/jchv/go-winloader` 的许可证向上游确认并补录
- [ ] 用工具生成**完整**依赖清单与许可（Go：`go-licenses`；正式发布前跑一次，避免只覆盖直接依赖）
- [ ] 确认公开仓无密钥/无 NAS 地址口令（`.gitignore` 生效：`agent/data/`、`deploy.local.env`）
- [ ] 若采用方案 B/C/D，务必在 README 写明许可范围与理由

---

## 附：本文件的许可证是怎么判定的

对每个 Go 模块用 `go list -m -f '{{.Path}}|{{.Dir}}' all` 拿到模块目录，读取其中的 `LICENSE` 原文并按关键字判定：
`Apache License` → Apache-2.0；`GNU GENERAL PUBLIC` → GPL-3.0；`Permission is hereby granted` → MIT；
`Neither the name` → BSD-3-Clause；`Redistribution and use` → BSD-2-Clause。
（`third_party/` 的项目直接读其根 `LICENSE`。）
