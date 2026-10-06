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

## 文档

- **[白泽项目交接文档.md](白泽项目交接文档.md)** —— 架构、协议、部署、开发工作流、命令速查（**从这里开始**）
- **[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)** —— 第三方依赖与许可证

## 许可（双许可）

- **主体：MIT**（见 [LICENSE](LICENSE)）。
- **例外**：`agent/internal/memory/` 与 `agent/internal/voice/` 是基于 **GPL-3.0** 项目
  [OpenHuman](https://github.com/tinyhumansai/openhuman) 的实现用 Go 重写而来的衍生作品，
  这两处按 **GPL-3.0** 授权（原文见 [LICENSE-GPL-3.0.txt](LICENSE-GPL-3.0.txt)）。
