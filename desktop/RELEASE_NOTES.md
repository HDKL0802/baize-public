# 白泽 0.7.0

这一版解决两件事：**国内能下、国外能下**，以及**不用重装就能换后端**。

## 本版变化

### 1. 双源发布：国内 Gitee / 国外 GitHub

- 发布源拆成两个镜像：国内 `gitee.com/szk0037/baize-public`，国外
  `github.com/HDKL0802/baize-public`，两个仓内容与 Release 资产完全一致。
- 官网下载菜单会自动探测 GitHub 可达性（2.5 秒超时），不通就走 Gitee；
  菜单底部还留了「下载源：自动 / 国内 Gitee / 国外 GitHub」的手动切换，
  选择记在本地，下次打开还是它。
- 官网与文档里的仓库地址、Releases、文档原文链接都按镜像切换：
  国外点进去是 GitHub，国内点进去是 Gitee。
- 桌面端检查更新不再只认 GitHub：清单依次从 GitHub → Gitee → 本地后端取，
  哪条通走哪条，国内网络不会卡在「检查更新失败」。
- NAS 一键脚本同样双源：哪边快用哪边，也可以 `BAIZE_MIRROR=gitee` 固定。

### 2. 设置页新增「更新后端」

桌面端与手机端的设置页都多了一张「后端更新」卡片：

- 显示当前后端版本、发布源最新版本、正在使用的下载源；
- 「检查更新」只比对版本，不动文件；
- 「更新后端」让后端自己从发布源下载新二进制、sha256 校验、落到
  `<dataDir>/bin/baize-backend.new`，容器重启时自动替换并接管，
  不需要 Docker socket、不需要 SSH、不用重新部署整套服务。
- 非容器（本机直接跑二进制）的情况下只下载不重启，会提示落盘路径，
  由你重启后端后生效。

### 3. NAS 部署与更新更顺

- `install.sh` 的 `deploy` 现在按镜像自动挑下载地址，带 sha256 校验失败即中止。
- 容器内新增 `BAIZE_IN_CONTAINER=1` 与 `entrypoint.sh`：
  启动时若发现 `baize-backend.new`，先提升为正式二进制再启动，
  配合 compose 的 `restart: unless-stopped` 实现「点一下按钮就换后端」。
- `baize-nas` 的 `status / logs -f / rollback / uninstall` 保持原样，
  取消部署仍然要显式加 `--yes`。

### 4. 版本号

桌面端与后端统一到 0.7.0（后端由 CI 用 `-ldflags -X main.version=<版本>` 注入），
官网下载菜单里的版本号跟着 Release 走。
手机端仍走自己的版本线（`bz-todo-v0.10.17`，`versionCode 1017`）——
Android 不允许版本号往回退，旧的 0.10.16 只能往上升级；
手机端设置页也一并加上「更新后端」卡片，连同这版一起装。

## 安装 / 升级

1. 新装：下载 `baize-desktop-setup-0.7.0.exe`（用户级安装，不需要管理员）。
2. 已装旧版：桌面端「设置 → 关于」点检查更新即可自动升级；
   也可以直接运行新安装包，会沿用原安装目录。
3. NAS 升级：`baize-nas deploy`，或设置页里点「更新后端」（更快，只换二进制）；
   也可以重跑一遍 `curl -fsSL https://baize.wydhd.fun/nas/install.sh | sudo bash`。
