# 手机端远程连接参考调研：ds-harness-remote 与豆包语音（2026-11-22）

> 任务来源：用户 m02126「按原进度继续干」+ 给参考「DSH go 靠 ds harness 插件连 GitHub 登录、P2P 协议」，随后 m02162 补充详细要求。
> 本轮只做调研与文档落盘，**未改任何代码**，不触发版本号纪律（后端保持 0.8.x、手机端 bz-todo-v0.10.17 / versionCode 1017）。

---

## 0. 一句话结论

用户想参考的东西找对了：真正能借鉴的远程连接方案是 **`liguobao/ds-harness-remote`（MIT）**，不是 DSH Go 本体（AGPL-3.0）。
11-07 旧调研把两者混成一个项目，才得出「代码不能合」的错误结论——那句对 DSH Go 本体仍成立，但**不适用于 ds-harness-remote**。
其 LAN→P2P→TURN→Relay 路径协商、账号 OAuth 身份认证、端到端加密、多设备身份与界面布局，都符合用户对白泽手机端的设想；**可借鉴思路、参考界面、按 Go 重写（或搬 MIT 代码）**。缺的三块是：国内账号（GitCode/Gitee）、更好用的全双工语音/电话、中继策略取舍。

---

## 1. 对象厘清（关键更正）

| 项目 | 许可证 | 技术栈 | 与用户描述的关系 |
|---|---|---|---|
| `ccch1mneyyy/dsh-Go`（DSH Go 本体） | **AGPL-3.0**（`sdk/`、`examples/` 为 MIT；名称/标识/Deepy/鲸娘形象不在授权内） | Flutter/Dart，Android 8+，手机本地跑引擎约 1GB，无障碍/Shizuku 手机操控 | 用户看到的「界面+连接入口」宿主；**只可参考界面与思路，不并代码** |
| **`liguobao/ds-harness-remote`（Remote 插件）** | **MIT License (c) 2026 DSH Remote contributors** | TypeScript，Node>=24.9，pnpm；DSH 插件 + Android/iOS/Web/PC 客户端 | **用户真正想参考的对象**：GitHub/Google/知乎 OAuth 登录 + P2P 打洞、主机无需公网端口；协议/界面/代码都可参考（MIT 与白泽兼容） |

- DSH Go README 原文（line 177）：「远程连接：通过 SSH 或李国宝老师开发的 DSHarness Remote」；line 356：「远程连接协议与 ds-harness-remote（MIT）兼容」。即 DSH Go 只是 ds-harness-remote 的一个客户端，远程那套 MIT 与它本体 AGPL 是分开的。
- 旧文档 `docs/接入调研-手机端Agent-dsh-Go与抖音样例.md` 已于 2026-11-22 加更正前言与三问答复状态（原文历史保全，未删）。

---

## 2. ds-harness-remote 核实事实（GitHub API + README 原文，2026-11-22 抓取）

- 仓库：`https://github.com/liguobao/ds-harness-remote`，**MIT**，TypeScript，Star **3504**，最近推送 2026-11-22；package.json 0.6.7。
- 定位：基于 DSH 插件机制的远程访问，PC/移动/Web 访问本地 DSH、CodeX、Cursor、Claude Code 工作区；端到端加密 P2P，主机**只出站连接，不需公网监听端口/路由器端口转发**。
- 连接方式（README line 194）：`LAN -> P2P -> TURN -> Relay` 顺序协商；WebRTC 不可用/连不通时回退加密 WebSocket Relay；每条路径都是 Noise 密文。
- 安全：LAN/P2P 数据绕开 Server（仅连接协调经过）；同账号设备免二次确认，设备身份密钥本地固定；移出设备即撤销凭据、成员与活动连接；服务端只转发密文，不存会话明文/设备私钥。
- 登录：Zhihu / GitHub / Google 授权，或账号+密码（注册需邀请码）；可自托管最小 Server（含 WebRTC LAN/P2P/TURN 协调 + 加密 Relay；TURN 需自建 coturn）。
- 多客户端：同一 Host 多客户端、隔离 RPC/流；DSH Go 内置兼容客户端 `packages/dsh_remote/`（Dart）。
- 客户端截图章节：Desktop 与 Android 两套界面（README line 115-141），即用户说「界面不错」的来源。
- 边界（原文 self-limits）：只传原生 Harness 会话所需消息；**不开放通用工具 RPC 与远程桌面控制**；文件预览只读；远程终端走 Host 本地开关。→ 白泽要把手机当「执行器」（能派活/批审批/落回执），需要在自己的协议层补这部分，不能直接套它的最小能力面。

---

## 3. 白泽现状与契合点

- 后端全 Go，NAS 后端是唯一大脑；设备注册到 `agent/internal/hub/hub.go`（设备注册表 + 任务库 + 审批闸门 + 下发 + 回执）。
- `shared/client/client.go`：`normalizeURL` 把 http(s) 转成 `ws(s)://.../ws/device`，设备 WebSocket 直连 NAS:8787。
- 痛点：直连要求**公网 IP/端口或同一局域网**；外出且无公网入口时手机端连不上大脑。这正是 ds-harness-remote 的 P2P/中继要补的洞。
- 契合做法（思路层）：保留白泽 `/ws/device` 上层语义（注册/任务/审批/回执不变），在**传输/接入层**增加「LAN 直连 → P2P → TURN/Relay」的路径协商与账号身份认证；设备身份密钥本地固定，沿用 hub 的审批闸门。

---

## 4. 用户明确决定（m02126 / m02162，三问已全部拍板）

1. **手机端不跑本地引擎**：本地引擎内存需降到 **≤100MB** 才考虑，否则不做（低配手机前台跑不动）。→ 定位维持「大脑在 NAS/后端，手机是手脚」，AGPL 本体不碰。
2. **真机**：无 Android 12+；**有 Android 10 真机，但只能测局域网**。用户同意开机连电脑，ADB 以前已开过，需要时连上即可由助手控制 → 只做 **LAN 段**验证，P2P 打洞/TURN 需不同公网环境，Android 10 这台测不了（如实记录，不假报）。
3. **AGPL 目录问题作废**：只取 MIT 部分。
4. **账号**：国外 **GitHub + Google** 够用；**Google 登录国内基本用不到**；国内希望支持 **GitCode（CSDN 旗下）/ Gitee（码云）**——用户口述「gitcode 或者是 git」，按 GitCode + Gitee 理解（待用户纠正）。
5. **语音/电话**：ds-harness-remote 的语音/电话「能用但不好用」，**要仿豆包**——用户明确说豆包电话语音做得最好。界面与连接思路整体符合设想，可照着补功能。
6. **时机说明**：用户原话「如果他早些出现，我估计会直接让你把手机版用它来改，但它出现得有点晚」→ 现在按白泽自己的节奏借鉴，不整建制替换。

---

## 5. 账号认证可行性（补充调研）

- **国外**：GitHub / Google OAuth 是 ds-harness-remote 现成支持的，直接沿用思路即可。
- **国内**：
  - **Gitee**：标准 OAuth2 授权码模式（跳转授权页 → code 回调 → 换 token → 拉用户信息），有成熟接入资料与 Vert.x GiteeAuth 等现成实现；SDK/文档完备。
  - **GitCode（CSDN 旗下）**：同样有 OAuth2（`response_type=code`、`redirect_uri` 回调、`client_secret` 放请求体），已有鸿蒙应用接入实战案例；注意其 `/oauth/token` 对 Content-Type 敏感（form-data）。
  - 两者均为国内可达，适合替代国内不可用的 Google 登录。
  - 落地要点：ds-harness-remote 现只内置 Zhihu/GitHub/Google + 账密；**GitCode/Gitee 需自行扩展认证 provider**，自托管 Server（MIT）可改，或白泽自己写认证层。
  - 来源（外部资料，二手，仅作可行性旁证）：腾讯云开发者社区《在网站中集成 Gitee 第三方登录的完整指南》、CSDN《从零实现 GitCode OAuth 登录——鸿蒙 6 应用实战》、Vert.x `GiteeAuth` 文档。

---

## 6. 语音 / 电话：以豆包为目标（补充调研）

- 豆包「打电话」已全量升级**全双工语音大模型 Seeduplex**（字节跳动）：「边听边说」框架，听与说同步；官方多维度评测称对话流畅度、节奏感、判停与抗干扰显著优于传统半双工，用户反馈「抢话/响应慢/误打断」下降；IT之家/凤凰科技/DoNews 均有报道（外部二手来源）。
- 白泽现状：后端有 `internal/voice`（TTS/STT，DashScope / OpenAI 两种实现）；桌面端 0.5.24 有「两人通话」浮窗，WebRTC 音视频媒体 P2P、信令走后端 `calls` 模块中转，**同内网 P2P，无 STUN/TURN**（见 `docs/白泽项目交接文档.md` §语音通话快捷键）；本地 STT 候选 CapsWriter-Offline（Go 常驻轻）在待办清单第 23 项。
- 差距与做法：半双工「按住说/轮次说」是难用根源 → 目标改**全双工流式**：边听边说、可打断、VAD 判停、回声消除；媒体走已验证的 WebRTC P2P，超局域网时复用第 3 节的 P2P/TURN 通道；模型侧接全双工语音模型（API 调用，符合「所有模型走 API、不本地部署」铁律），STT 保留 DashScope/OpenAI 与 CapsWriter-Offline 候选。

---

## 7. 落地建议（借鉴清单，供后续排期，不立即实施）

| 项 | 借鉴/新增 | 说明 |
|---|---|---|
| 传输路径协商 | 借鉴 LAN→P2P→TURN→Relay | 上层 `/ws/device` 语义不动，传输层可插拔 |
| 账号认证 | 借鉴 OAuth 抽象 + 本地设备身份密钥 | GitHub/Google 现成；新增 GitCode/Gitee provider（自托管/自写） |
| 端到端加密 | 借鉴 Noise 密文 + 设备撤销 | hub 已有审批闸门，配合身份固定 |
| 界面 | 参考 Android 客户端布局与会话交互 | 前端形态保持白泽 WebView |
| **补功能①国内账号** | GitCode/Gitee OAuth | 用户明确要 |
| **补功能②全双工语音/电话** | 仿豆包 Seeduplex 体验 | 用户明确要，模型走 API |
| **补功能③中继策略** | 暂不定（用户「你看吧」） | 建议先自托管最小 Relay，TURN 按需自建 coturn；避免依赖第三方服务 |

---

## 8. 真机测试计划（Android 10，待用户开机连线）

- 用户动作：开机 Android 10 真机、USB 连电脑、确认 ADB 授权（以前开过 USB 调试）。
- 助手动作：`adb devices` 确认 → 仅做 **LAN 段**连接与 `/ws/device` 注册/任务/审批/回执回归；不装、不卸载、不改账号数据。
- 能验：局域网内手机↔NAS 连接、WebSocket 注册、审批闸门、语音通话同内网 P2P。
- 不能验：P2P 打洞（需两端不同 NAT/公网）、TURN/Relay 回退（需公网服务端）、Google/GitHub OAuth（需账号与网络）。→ 如实标注，不假报通过。
- 若后续提供 Android 12+ 真机与双网环境，再补 P2P/TURN 段。

---

## 9. 出处

- 用户提供线索：m02126、m02162（本会话原文）。
- 抓取原文（存 `D:\xm\白泽智能体\.memsearch\ref-*.txt`）：ds-harness-remote LICENSE / README / package.json / GitHub API repo 元数据；DSH Go README / ASSETS.md。
- 外部二手（web_search）：凤凰科技/IT之家/DoNews 关于豆包 Seeduplex 全双工语音；腾讯云开发者社区 Gitee OAuth 指南、CSDN GitCode OAuth 实战、Vert.x GiteeAuth 文档。
- 仓库内：`docs/接入调研-手机端Agent-dsh-Go与抖音样例.md`（旧调研，已加更正）、`README.md`、`agent/internal/hub/hub.go`、`shared/client/client.go`、`docs/白泽项目交接文档.md`（语音通话）、`docs/白泽下一步待办清单.md`（本地 STT 第 23 项）。
- 版权与素材：DSH Go 名称、标识、Deepy/鲸娘不在开源授权内；只借鉴，不搬运品牌素材。
