# 白泽桌面端 0.6.0

界面换成「墨玉」，自动更新改走 GitHub Releases。这一版没有新增动作协议，
但**安装包 / 更新包的内容变了**，所以必须升版本才能让老客户端真正拿到新界面。

## 本版变化

### 1. 墨玉视觉（四端同一套令牌）

- 桌面主控台（`desktop/webui`）：深色 `#070807 / #0E100E / #131614 + #3BE0C4`，
  浅色为暖纸 `#F2ECE0 / #FAF6EC + #0B6E63`；圆角统一 2px、1px 细描边、衬线标题。
- 浮窗（`desktop/float`，含实时字幕小窗）：同步同一套令牌，去掉所有硬编码色值。
- 手机端（`mobile/webui` / `mobile/todo`）与 NAS 上的 WebUI（`agent/internal/httpapi/webui`）：
  同一份 `--bg/--surface/--line/--fg/--accent` 令牌；换肤只改 `:root` 一组值。
- 管理控制台 `panel.html` 同步换色。

### 2. 自动更新：GitHub Releases 优先，后端回退

- 检查更新先请求
  `https://github.com/HDKL0802/baize-public/releases/latest/download/latest.json`
  （公开仓，匿名可下，不需要令牌）；
- GitHub 不可达（网络受限 / 还没发版）时，再回退到本机配置的后端地址 `/dl/latest.json`；
- 下载仍然做 sha256 校验；更新包改为同时替换 `ui/`、`float/`、`browser-ext/`，
  不再出现「exe 升了、界面还是旧的」。

### 3. 打包链路修复

- `desktop/build.ps1`：先构建 `webui` 与 `float` 的 Vite 产物，再编译 exe，
  最后把 `ui/`、`float/dist/`、`browser-ext/` 复制到 `bin/`，使 `bin/` 就是一个完整的便携版。
- `desktop/build-installer.ps1` + `installer.nsi`：安装包补齐 `$INSTDIR\float\dist`，
  卸载时一并清理。
- `tools/release-desktop.ps1`：更新包按安装形态打包（exe + ui/ + float/ + browser-ext/）。
- 新增 GitHub Actions 流水线：打 tag / 手动触发即在 `baize-public` 构建 Windows exe、
  打包 zip 与安装包、发布 Release（官网下载菜单会自动显示新版本）。

## 安装 / 升级

1. 新装：下载 `baize-desktop-setup-0.6.0.exe`（用户级安装，不需要管理员）。
2. 已装旧版：桌面端「设置 → 关于」里点检查更新即可自动升级（GitHub 优先）；
   也可以直接运行新安装包，会按更新流程沿用原安装目录。
3. 官网 `https://baize.wydhd.fun` 的「下载」菜单始终拉 GitHub 最新 Release，发版后自动更新。
