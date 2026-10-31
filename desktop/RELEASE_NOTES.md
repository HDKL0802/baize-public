# 白泽 0.6.1

图标与 NAS 部署这两件事一起改完：全端换成同一枚「白泽」独角瑞兽线描图标，
NAS 端从「本机 PowerShell 推过去」改成「在 NAS 上一条 curl 命令」。

## 本版变化

### 1. 新图标：白泽独角瑞兽线描（全端同一枚）

- 源图是矢量：`desktop/assets/icon.svg`（墨玉底 + 象牙线条 + 玉青独角与眼），
  由 `desktop/build-icon.ps1` 一次栅格化成所有落点，不再有第二张图。
- 替换范围：
  - 桌面主控台侧栏品牌标（`desktop/webui/src/App.vue`，原来是「白」字方块）
  - 安装包 / exe / 任务栏（`desktop/assets/icon.ico` + `desktop/rsrc.syso`）
  - 官网 logo 与 favicon（`web/assets/logo.png`、`web/assets/favicon.ico`，缓存号升到 `?v=3`）
  - NAS WebUI 与管理控制台（`agent/internal/httpapi/webui/icon.png`，`/icon.png` 即它）
  - 桌面浮窗球面图标（`desktop/webui/public/icon.png` → `bin/ui/icon.png`）
  - 手机端 `icon.svg`（webui / todo / android assets）

### 2. NAS 部署：一条 curl 命令 + 自带运维命令

```bash
curl -fsSL https://baize.wydhd.fun/nas/install.sh | sudo bash
```

- 脚本源在 `web/nas/install.sh`，随官网一起发到 Pages；安装后自身落到
  `/usr/local/bin/baize-nas`，之后直接用短命令：
  `deploy / status / start / stop / restart / logs -f / rollback / uninstall / version`。
- 部署流程：拉 GitHub 最新 Release 的 `baize-backend-linux-amd64` → sha256 校验 →
  NAS 本地 `docker build`（alpine + chromium + 中文字体）→ `docker compose up -d` →
  健康检查（失败自动回滚上一版镜像）。
- 「取消部署」=`baize-nas uninstall --yes`：停容器、删容器与镜像、清空
  `/vol1/@appdata/baize/data`（可加 `--keep-data` 保留数据），必须显式加 `--yes`。
- 每次 deploy 会把正在跑的镜像标记为 `baize-backend:previous`，`rollback` 一键回退。
- 发版流水线新增后端二进制（linux/amd64）与 `.sha256` 资产，供一键脚本下载。

## 安装 / 升级

1. 新装：下载 `baize-desktop-setup-0.6.1.exe`（用户级安装，不需要管理员）。
2. 已装旧版：桌面端「设置 → 关于」里点检查更新即可自动升级（GitHub 优先）；
   也可以直接运行新安装包，会按更新流程沿用原安装目录。
3. NAS 升级：`baize-nas deploy`，或重新跑一遍 curl 那条命令。
