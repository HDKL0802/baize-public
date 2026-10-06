# 白泽 · 跨端个人智能体

一个**全 Go** 的个人 AI 智能体：**NAS 上的后端是唯一大脑**，**桌面端 / 手机端注册成设备当手脚**。
数据正本全在后端，端侧只留缓存；危险动作强制人工审批。

> 个人项目，起因是想要一个「自己的、跨设备的、能记住事的」智能体。

## 架构

```
                    ┌──────────────────────────────────────┐
                    │   NAS 后端（唯一大脑，Docker）          │
                    │   设备中枢 / 任务与审批 / Agent / 记忆   │
                    │   知识库 / MCP / 技能 / 备份 / 定时任务  │
                    └──────┬────────────────────┬──────────┘
             WebSocket+HTTP          WebSocket+HTTP
                    ┌──────┴──────┐      ┌──────┴──────────┐
                    │ Windows 桌面端 │      │  Android 手机端   │
                    │ Go + WebView2 │      │ Go 内核 + WebView │
                    └───────────────┘      └───────────────────┘
```

## 仓库结构

| 目录 | 说明 |
|---|---|
| `agent/` | 后端 Agent（Go）：设备中枢、任务与审批、Agent 循环、记忆、知识库、MCP、技能、备份、定时任务 |
| `shared/` | 跨端共享契约：消息定义（`proto`）+ 设备客户端（`client`） |
| `core/` | 共享领域内核 + 手机端 Go 内核（`cmd/bzcore`） |
| `desktop/` | Windows 桌面端（Go + 纯 Go WebView2，无 cgo） |
| `android-app/` | 手机壳（Java，不打 Gradle）+ 打包脚本 |
| `todo-app/` | 手机端 WebView 前端（原生 HTML/CSS/JS） |
| `cli/` | 电脑端小工具 `bz` |
| `tools/` | 部署与运维（`deploy-nas.ps1`、`sshrun`） |
| `deploy/` | NAS 的 docker-compose |

## 快速开始

**后端（部署到 NAS）**
```powershell
# 1. 在仓库根建 deploy.local.env（模板见 deploy.local.env.example），填 NAS 地址/密码/令牌
# 2. 一条命令：构建 → 上传 → NAS 打镜像 → 起容器 → 健康检查（不通过自动回滚）
powershell -ExecutionPolicy Bypass -File tools\deploy-nas.ps1
```

**Windows 桌面端**
```powershell
powershell -ExecutionPolicy Bypass -File desktop\build.ps1
desktop\bin\baize-desktop.exe --server http://<NAS>:8787 --token <令牌>
```

**Android 手机端**
```powershell
# 先交叉编译内核（arm64），再打 APK，两步都在脚本里
powershell -ExecutionPolicy Bypass -File android-app\build.ps1
```

## 风险与免责声明（请务必先读）

白泽会按你的授权**在真实设备上执行操作**（读文件、跑命令、删除文件，最高档「完全访问」不限定范围）。
因此：

1. **高权限意味着高风险**。「桌面控制」里的「只读指定盘」与「完全访问」两档会显著扩大智能体可触达的范围；
   放开前请先备份重要数据，并建议从最低档（关闭 / 只读）开始，确认无误再逐步放开。
2. **智能体不可靠**。大模型可能误解指令、产生幻觉或误操作，从而造成误删、误改、数据泄露等不可预期后果。
3. **后果自负**。你自行选择放开权限、自行部署与使用本软件所产生的一切后果，**由你本人承担，与白泽作者及贡献者无关**。
4. **无担保**。本软件按「现状」提供，不附带任何明示或默示担保（详见 [LICENSE](LICENSE) 的 MIT 免责条款）。
5. 危险动作默认强制**人工审批**，但审批闸门只降低风险、**不能消除风险**；请始终保留备份并谨慎放权。

> 首次安装、以及第一次进入「设置 → 桌面控制」时，程序都会再次弹出这条提醒要求你确认。

## 文档

- **[白泽项目交接文档.md](白泽项目交接文档.md)** —— 架构、协议、部署、开发工作流、命令速查（**从这里开始**）
- **[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)** —— 第三方依赖与许可证

## 许可（双许可）

- **主体：MIT**（见 [LICENSE](LICENSE)）。
- **例外**：`agent/internal/memory/` 与 `agent/internal/voice/` 是基于 **GPL-3.0** 项目
  [OpenHuman](https://github.com/tinyhumansai/openhuman) 的实现用 Go 重写而来的衍生作品，
  这两处按 **GPL-3.0** 授权（原文见 [LICENSE-GPL-3.0.txt](LICENSE-GPL-3.0.txt)）。
