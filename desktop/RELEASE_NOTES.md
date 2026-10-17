# 白泽桌面端 0.5.30

「电脑控制」的**浏览器双方案**：内置浏览器（白泽自带窗口，可把控制权移交给你）+ Chrome 扩展（控制你已经装好的浏览器）。
默认关闭，所有 `browser.*` 动作仍然**每次**走人工审批。

## 本版变化

### 1. 内置浏览器（白泽自带）

- 主界面新增应用 **「浏览器」**（导航「核心」组）：地址栏、打开、当前页面状态、**移交给我 / 交还给白泽**、关闭。
- 浏览器是**独立进程的 WebView2 窗口**（与主窗口互不影响；关掉不等同于关白泽）。
- AI 可执行的动作（`target=builtin`，默认）：
  - `browser.open`：打开/导航到 http(s) 地址；
  - `browser.read`：读当前页 URL / 标题 / 正文；
  - `browser.click`：按 CSS 选择器或**可见文字**点击；
  - `browser.type`：往输入框填字（可 `submit` 回车提交）；
  - `browser.key`：按键（`enter` / `ctrl+s` / `f5`…，多组用逗号串）；
  - `browser.screenshot`：截当前窗口，存本机临时 PNG，回执带路径与尺寸；
  - `browser.status`：是否打开 / 当前地址 / 是否已移交；
  - `browser.handoff`：`{handoff:true}` 把控制权交给你，`{handoff:false}` 收回。
- **移交是硬闸门**：移交期间除 `status` / `handoff` 外，AI 的动作一律被拒绝（页面顶部黄条上也有「交还给白泽」按钮）。
  典型用法：AI 说"需要你扫码登录" → 移交给你 → 你登录完点交还 → AI 接着干。

### 2. Chrome 扩展（控制你已装的浏览器）

- 设置页新增「允许控制浏览器」：总开关 + **允许 Chrome 扩展接入**（分开授权，默认都关）。
- 打开后设置页会显示本机接入地址（`ws://127.0.0.1:47121/ext/ws`）与**随机令牌**，可复制、可重置。
- 安装包自带扩展目录 `browser-ext/`：
  1. Chrome/Edge 打开 `chrome://extensions` → 打开「开发者模式」→「加载已解压的扩展程序」→ 选安装目录下的 `browser-ext`；
  2. 点扩展图标，填设置页里的地址与令牌 →「保存并连接」；
  3. 之后 AI 用 `browser.*` + `{"target":"chrome"}` 控制**当前活动标签页**（`chrome.debugger`）。
- 扩展支持：`open` / `read` / `click` / `type` / `key` / `screenshot` / `status`；断线自动重连（30 秒保活）。
- **外部 Chrome 没有「移交」动作**（你本来就能直接操作浏览器），调用会明确报错说明。

### 3. 安全与闸门

- 两道**本机**开关：① 允许控制浏览器；② 允许 Chrome 扩展接入（只在 ① 打开时可开）。
  开关关闭时**根本不上报** `browser.control` 能力，后端也就派不过来。
- `browser.*` 全部在危险动作前缀里 → **每次**派下来都要人工审批。
- 内置浏览器的控制口只监听 `127.0.0.1`，随机端口 + 随机令牌；**只接受白名单动作**，不执行任意 JS。
- 扩展接入口同样只监听 `127.0.0.1`，令牌不对直接 403；令牌可随时重置（旧连接立刻失效）。
- 扩展目录随安装包/自动更新一起更新（不会因为更新桌面端而丢扩展）。

## 验证

- 桌面端 `go build` / `go vet` / `go test -count=1` 全绿；后端 `go build` / `go vet` 与 `executor`/`tools` 测试全绿。
- 新增桌面端单测：
  - `TestBrowserPermGate`：总开关没开时 `browser.*` 全拒，开了才放行；
  - `TestSetBrowserControlValidation`：开启必须过风险确认、令牌自动生成、关总开关时扩展授权跟着关；
  - `TestExtToken` / `TestExtRoundTrip` / `TestExtRejectsBadToken`：扩展令牌不可重复、**WS 通道往返真实跑通**、错误令牌返回 403；
  - `TestExtCallNotConnected` / `TestExtHandoffRejected`：没连上 / 外部浏览器移交都如实报错，不假装成功；
  - `TestBrowserStatusWhenClosed` / `TestBuiltinActionWithoutWindow`：没打开时状态为 `open=false`，其他动作提示"先打开"；
  - `TestCaptureInvalidWindow`：截图入口对无效句柄报错不崩。
- **真机内置浏览器冒烟**（本机真跑）：打开 `https://example.com` → `read` 读到页面正文 →
  `click {text:"Learn more"}` 成功导航到 iana.org → 截图 `1200x820` / 69KB（像素抽样非黑）→
  `handoff:true` 后 `read` 被拒绝 → `handoff:false` 后恢复。
- **无头 Edge 真渲染**：导航里出现「浏览器」；设置页「允许控制浏览器」与浏览器面板的
  地址栏/打开按钮/状态卡片都在；**0 个 JS 错误**。

## 兼容与说明

- 后端同期升到 **0.9.37**（协议加 8 个 `browser.*` 动作 + `browser.control` 能力；无界面端如实拒绝）。
  手机端 **0.10.16 未改动**。
- 浏览器控制与文件权限档位、命令执行、GUI 控制**都不是一条轴**，各自独立开关。
- 扩展通过 `chrome.debugger` 工作，Chrome 会在页面顶部显示"正在调试此浏览器"的提示条，属正常现象。

## 安装

1. 下载并运行 `baize-desktop-setup-0.5.30.exe`（用户级安装，不需要管理员）。
2. 已装过旧版：直接运行即可，会自动按更新走，安装位置可改。
3. Chrome 扩展：安装目录下的 `browser-ext/`，按上面「Chrome 扩展」三步加载。
