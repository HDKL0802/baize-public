# SteamCommunity 302 v2 能否内置到白泽后端 · 调研结论

> 调研日期：2026-11-06 ｜ 结论：**不能整建制内置**（不是"不做 UI"，而是技术上会破坏白泽的无特权、单端口运行形态；且再分发许可无法核实）。
> 本文只给结论与证据，不包含未核实的仓库或许可证。

## 1. 工具与作者（已核实部分）

| 项目 | 结论 | 出处 |
|---|---|---|
| 官方名称 | Steamcommunity 302（用户所称 "v2/V2" 不是官方版本命名；官方按 `Ver.15.0.x` 发布，当前页面为 **Ver.15.0.9**） | [官方博客页](https://www.dogfight360.com/blog/18682/) |
| 作者 | 羽翼城 / Dogfight360 | 同上作者署名 |
| 官方发布渠道 | `dogfight360.com` 博客正文附件（Windows / macOS / Linux 安装包） | 同上 |
| 同作者其他工具 | UsbEAm Hosts Editor（多平台 hosts 修改）；用户口中的 "USBEAM launcher" 未找到官方同名项目，疑指 UsbEAm Hosts Editor | [UsbEAm Hosts Editor](https://www.dogfight360.com/blog/475/)、[4.X 新版页](https://www.dogfight360.com/blog/18627/) |
| GitHub 仓库 | **未核实**：公开搜索未找到官方源码仓库；搜索结果里的 GitHub 链接均为第三方教程/其他项目，不能作为官方来源 | 2026-11-06 检索 |
| 许可证 | **未核实 / 未见开源许可声明**：官方只提供编译后的下载包，未找到 LICENSE 或源码授权条款 | 2026-11-06 检索 |

> 结论：在"许可证未核实"的情况下，把安装包打进白泽安装包或 Docker 镜像里再分发，风险不可控。这一条本身就足以判定"不内置"。

## 2. 运行形态（已核实部分）

来自官方更新日志与 Linux 教程：

- **多模式**：hosts 模式、本地反代（代理）模式、DNS 重定向模式。
- **Linux 支持**：13.X 起支持直接在 Linux/macOS 运行界面与安装；旧教程说明后端基于 **Caddy**，生成配置后可迁移到 Linux/macOS（[Linux/macOS 教程](https://www.dogfight360.com/blog/2319/)）。
- **需要系统级权限/系统级改动**：
  - 安装/启动服务需要 root（Linux 教程与更新日志都提到"安装服务"）。
  - 需要导入/信任 `steamcommunityCA.pem` 这类本地 CA 证书（官方页面要求导入证书文件）。
  - 默认会监听 **80 / 443**（更新日志 Ver.12.1.41 修的就是"错误监听 0.0.0.0:80"；Ver.15.0.7 增加"端口占用检测及排查提示"）。
  - hosts 模式会写系统 hosts；Ver.15.0.5+ 提到"hosts 权限提示""hosts 恢复失败"。
  - DNS 重定向模式在 Windows 上需要驱动服务（Ver.15.0.7 引入 NetFilterSDK；Ver.15.0.8 会与拳头 Vanguard 驱动冲突并自动规避）。
- **形态**：带界面（托盘/设置页）的桌面程序，不是无头常驻服务；没有发现官方提供"纯 headless + 可编程 API"的运行模式。

## 3. 与白泽后端的冲突点

白泽后端的运行前提（现状）：

1. 桌面端：普通用户权限启动 Go 壳 + 后端子进程，监听 `127.0.0.1:8787`。
2. NAS/Docker：容器内非特权运行，只暴露 8787；不接管宿主 80/443，不写宿主 hosts，不装系统 CA。

302 要内置就必须同时满足：

| 需求 | 与白泽的冲突 |
|---|---|
| root / 系统服务安装 | 桌面端安装器是用户级安装（不需管理员），NAS 是容器；无法静默拿到 root |
| 80/443 特权端口 | 与宿主/容器端口、以及用户其他 Web 服务冲突；302 的常见投诉就是端口占用 |
| 系统 CA 信任 | 需要改系统证书库（Windows 证书存储 / Linux ca-certificates），属于全局副作用，卸载不干净会留隐患 |
| 写 hosts / 装驱动 | 需要系统级写权限，且 DNS 重定向驱动与反作弊/其他网络软件有已知冲突 |
| 再分发 | 许可证未核实，不能随白泽安装包/镜像分发 |

因此：**不可行**。若强行以"子进程托管"方式内置，最轻的做法也只能做到"用户自己已安装 302 时，白泽帮忙启动/停止它"，但这仍然要求用户先手动装好并授予 root，且没有稳定的无头接口可托管——这已是另一个产品，不属于本次"内置到后端"的范围。

## 4. 建议的替代路径（仅建议，未实施）

- **不做内置**；在文档/设置页写明"如需该能力，请自行从官方页面安装 Steamcommunity 302"，白泽不代管、不分发。
- 若未来确实要"一键加速"，应基于**可再分发的开源组件**、在**用户态**实现最小能力（例如仅通过系统代理/hosts 做域名可达性修复），并单独做权限申请与回退，而不是打包 302。
- 用户态替代品可调研 **Watt Toolkit（Steam++）**：开源、跨平台、用户态本地反代；但其许可证与白泽的分发方式仍需单独核实后再决定。

## 5. 出处

- [Steamcommunity 302 Ver.15.0.9 官方页](https://www.dogfight360.com/blog/18682/)
- [【过期】Linux/Macos 环境下使用 steamcommunity 302 教程](https://www.dogfight360.com/blog/2319/)
- [【过期】steamcommunity 302 Ver.12.1.47（历史更新日志）](https://www.dogfight360.com/blog/686/)
- [UsbEAm Hosts Editor V3.63](https://www.dogfight360.com/blog/475/)
- [UsbEAm Hosts Editor 4.X](https://www.dogfight360.com/blog/18627/)
- [Watt Toolkit 说明（第三方对比参考）](https://sourceforge.net/projects/watt-toolkit.mirror/files/2.8.5/2.8.5.zip/download)
