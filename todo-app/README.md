# 白泽 · 待办中心（Todo App）

秒哒(Miaoda) 待办 App 的 Go 重写前身 —— **Web 形态（PWA）**，本地优先、可离线、可加装到手机桌面。
后续将作为**插件模块**内嵌进白泽智能体 Agent，或打包为**独立 APK**（共用同一套 UI/领域逻辑/存储核心，仅换外壳）。

## 快速使用

```bash
# 方式一：直接双击 index.html
# 方式二：本地起 HTTP 服务（推荐）
python -m http.server 8080 --directory .
# 手机同局域网访问 http://<电脑IP>:8080，Chrome 菜单「添加到主屏幕」即可当 App 用
```

## 目录与模块边界（便于日后融入 Agent）

| 文件 | 职责 | Agent 融合策略 |
|------|------|----------------|
| `index.html` | 四 tab 应用壳 | 保留，作为插件 UI |
| `css/app.css` | 移动端样式（深浅色自适应） | 保留 |
| `js/store.js` | 存储层：localStorage + WebCrypto(AES-GCM/PBKDF2)；**Go 版对位 Argon2+AES-GCM** | 换为 Go 内核（gobridge 提供同名接口） |
| `js/reminders.js` | 提醒计划（哪些待办到点该响）纯逻辑 | 迁入 Go |
| `js/nlparse.js` | 自然语言解析（时间/领域/优先级/密码），无 DOM 依赖 | 意图解析换成后端 Agent |
| `js/todo.js` | 待办领域（双源 user/agent、日程/闲暇、依赖前置） | 迁入 Go `todo` 包 |
| `js/vault.js` | 密码本（加密、MD 导入导出、主密码锁） | 迁入 Go 凭据库（本地加密） |
| `js/chat.js` | **AI 助手对话页**：输入栏、群发、附件、入库卡片、`adapters` 智能体适配层（白泽智能体接入点） | 保留 |
| `js/app.js` | UI 公共层（tab/弹层/toast/提醒调度/文件桥） | 保留 |
| `js/settings.js` | AI 智能体（多模型）/ MD 导入导出 / 通知 / 数据管理 | 模型路由移交后端 |
| `sw.js` / `manifest.webmanifest` | PWA 离线壳 | 打包 APK 时替换为原生 WebView 壳 |
| `../cli/` | Go 版命令行（`bz.exe`），独立 JSON 库，MD 与 App 互通 | 提供给 Agent 的 CLI 入口 |

## 已实现能力（按秒哒参考应用 1:1 复刻）

**主题**：暗色 slate 深灰底（`#0B0F19`）+ emerald 翠绿主色（`#059669`），色值取自参考应用实机计算样式。

- **待办**：页头（标题 + 「日程 N 项 · 闲暇 M 项」+ 密码本锁定 / AI排期 / 排序）；我的待办 · Agent 待办双源；「今日 / 日程排期 (N)」⇄「闲暇待办 (M)」双视图（带计数、可左右滑动切换）；快速回车添加 + 「＋ 新建」；任务卡片（所属领域、优先级、截止、每周提醒、预计耗时、依赖、状态 + 「⋯」更多菜单）；长按卡片同菜单。
- **新建待办任务**（全屏页）：任务标题* / 详细备注步骤 / **所属领域**（工作开发·创作·日常生活）/ **优先级**（高·中·低）/ **任务形式**（日程排期 ⇄ 闲暇待办，带说明）/ 日期时间 / **每周定时提醒** / **前置依赖任务**（多选列表，含防循环校验）/ **预计耗时**（15/30/45/60/90/120 分钟）/ 到点提醒。
- **AI 智能排期**（全屏页）：AI 智能体卡（当前模型 · 待排期 N 项（含闲暇 M 项）· 去设置）；**四种排期策略偏好**（各项兼顾 / 专注于工作 / 专注于创作 / 专注于生活，持久化）；「开始自动智能排期」→ 请求模型给出建议顺序，失败回落本地规则（**依赖拓扑 + 截止时间 + 优先级 + 领域权重**，含依赖闭环告警）。未配置 AI 时点排期会被拦下并跳到设置。
- **自用密码本**：副标题「取代手机自带密码库 · 支持 MD 智能提取与导入」；搜索 + 「N 组密码」计数；凭据卡片（图标 / 名称 / URL / 账号行 + 复制 / 密码行 + 显示 + 复制密码 / 备注 / 编辑 / 删除）；**新建密码记录全屏页**含「⚡ 生成强密码」（大小写+数字+符号，避开易混字符）；主密码可设（PBKDF2 + AES-GCM，明文不落盘），**顶栏 🔒 可锁定/解锁来回切**；右上角导入 / 导出 Markdown。
- **AI 助手（对话页）**：底部输入栏 —— 左边=切换/多选智能体，右边依次 **＋（拍照/相册/文件）→ 录音 → 发送**（发送在最右边）。按住录音键说话，松开即上屏发出；**可勾选多个智能体群发**，同一条消息并行发给它们、各自的回复按到达顺序分别成气泡。**能干活**：话里说要记东西会立刻给出「待办/密码入库确认」卡片，点一下才写进库（提问、打招呼不会乱建待办）；也能问「我今天有什么安排」，模型会读到你的待办上下文。对话记录存本机。
  - **＋ 键三件事**：拍照 / 从相册选图 / 选文件。图片在原生侧降采样成 JPEG（最长边 1600）再交前端，前端再压到 1280 发出去、340 存历史；文本类文件（md/txt/json/csv… ≤200KB）直接内联正文给模型，其它类型只带文件名。**多模态请求**：OpenAI 协议用 `image_url: {url: dataURL}`，Anthropic 协议自动转成 `{type:'image', source:{type:'base64', media_type, data}}`；历史里只保留最近 6 张图的缩略图，不会撑爆 localStorage。
  - **★ 白泽智能体接入位 ★**：所有对外沟通都走 `js/chat.js` 里的 `adapters`，今天只有 `llm`（用户自配的 OpenAI 兼容 / Anthropic 模型）。以后接入白泽智能体，只需加一个 `kind`（例如把消息发到本机 Agent 的接口），左侧选择器会自动列出它，UI 一行都不用改；列表里现在已有一个「白泽智能体」占位行。
- **系统与配置**：**AI 智能体**（协议只走 OpenAI 兼容 / Anthropic，BaseURL·API Key·模型 ID 自填；**最多 5 个模型可增删切换**，内置 DeepSeek 预设，改动即时保存即时回显，带「测试连通性」）；**Markdown 格式数据中心（兼容 Obsidian）** 导出 / 导入（走原生文件桥，导出到系统「下载」目录、导入用系统文件选择器）；通知与到点提醒开关（原生精确闹钟 + 系统通知）；数据管理（恢复示例 / 清空）；关于。

## 验证方式

```bash
# 推荐：用带 no-cache 头的开发服务器（避免手机浏览器缓存旧前端资源）
python ../.trae/qa/serve.py 8080
# 或最简方式
python -m http.server 8080 --directory .
```

## 回归自测（无头浏览器，零依赖）

```bash
node _selftest.js        # 启动 Edge 无头模式，跑 113 项功能断言（需本机 Edge）
```

## Android 壳（`../android-app/`）

```powershell
powershell -ExecutionPolicy Bypass -File ..\android-app\build.ps1   # 产物 bz-todo-v0.10.0.apk
```

- **本地服务端口是固定且持久的**（首次运行写入 SharedPreferences）：WebView 的 localStorage 按 origin（含端口）隔离，端口一变数据全部看不见。同一进程内所有页面共用同一个端口 → 同一个 origin → 同一份数据。
- **语音（0.10.0 起）**：说话和听写都走后端那条独立的**语音通道**（`/api/agent/voice/*`），不再依赖手机系统的识别服务。
  - **朗读**：`new Audio('http://127.0.0.1:<内核端口>/api/agent/voice/tts?text=…')`。页面由壳里的静态服务托管、内核是另一个端口，这里靠"媒体跨源不需要 CORS"这条规则直接放音，几 MB 的音频不进 JS 桥、手机上也不留文件。
  - **按住说话**：`getUserMedia` 录音 → `decodeAudioData` + `OfflineAudioContext` 重采样成 16k 单声道 WAV → base64 经 `BzDevice` 桥交给内核 → 后端（百炼 paraformer-realtime-v2）转文字。壳侧只需 `RECORD_AUDIO` 权限 + `onPermissionRequest` 放行音频采集（见 `MainActivity`）。
- **跨端设备注册**：设置页「跨端」开启后，壳（`CoreBridge`）会把 Go 内核 `libbzcore.so` 从 `nativeLibraryDir` 拉起来（Android 10+ 只允许执行安装包 lib/<abi>/ 里解出来的文件，所以内核以 .so 名义打进 `lib/arm64-v8a/`），带上 `--server/--pair-token/--platform android` 注册成后端的设备。界面通过 `BzNative.bzCoreCall()` 让原生层代调内核接口（绕开 WebView 跨源限制，配对令牌不出原生层）；内核自己通过 `GET /api/device` 如实上报「连没连上」。
- **数据是镜像关系**：待办正本仍在 localStorage，`Store.saveTodos` 每次改动都把整表推给内核的 `todo.replaceAll`，后端的 `todo.list` 才读得到真数据；内核这侧只有只读动作，密码本相关动作一律不提供。
- **文件导入导出走原生桥**：`BzNative.saveTextFile()` 经 MediaStore 写系统「下载」目录，`BzNative.pickTextFile()` 调系统文件选择器（WebView 里的 `<a download>` / `<input type=file>` 在壳内不可用）。
- **对话附件走原生桥**：BzNative.pickAttachment(source)（camera / album / file）→ 相机拍进 MediaStore（无需存储权限）→ 原生降采样成 JPEG / 读文本正文 → 回调 window.__bzAttachment(JSON)。<input type=file> 在 WebView 里不工作，所以浏览器才用它兜底。
- 资源由 `LocalServer` 以 `http://127.0.0.1:<固定端口>` 提供（file:// 不是安全上下文，crypto.subtle 会失效）。

## 命令行版（`../cli/`）

```powershell
& "d:\xm\白泽智能体\cli\bz.exe" list
& "d:\xm\白泽智能体\cli\bz.exe" add "整理本周工作计划" --due 2026-09-26T14:00 --cat 工作开发 --pri high
& "d:\xm\白泽智能体\cli\bz.exe" export --out d:\todo.md
```

数据在 `%USERPROFILE%\.baize-todo\data.json`，与 App 端是两套独立数据库，互通只通过 `export` / `import` 的 Markdown（格式已由回归断言覆盖）。
Agent 使用说明见技能 `C:\Users\sun08\.agents\skills\bz-todo\SKILL.md`。

## 目录内的 QA 辅助文件

| 文件 | 用途 |
|------|------|
| `_selftest.js` | Node + CDP 功能回归（113 项断言） |
| `_devicecheck.html` | 真机自检页：同源 iframe 跑断言并把结果画在页面上，截图即可取证 |
| `_reset.html` | 清空本地数据 + 注销 Service Worker + 清缓存，便于拿到最新前端 |
| `../.trae/qa/serve.py` | 带 no-cache 头的开发服务器 |
| `../.trae/qa/shots.js` | 批量出图（9 个页面）用于视觉核对 |

> 前端更新排查提示：Service Worker 现为 network-first（在线取最新、离线回退缓存），并在检测到新版本时自动刷新一次；本地调试若仍看到旧样式，先打开 `_reset.html` 再进应用。