# 白泽智能体 · DSH 项目记忆

> 落盘时间：2026-10-31 21:30（Asia/Shanghai）
> 来源：`D:\xm\白泽智能体\2026-10-31\migration-to-dsh\outputs\` 交接包（4 份文档 + 配置原文件 + 数据包）
> 本文只记「事实与规矩」，**不写任何凭据明文值**。

---

## 0. 结论：本机原地接续（方案 C，用户 2026-10-31 拍板）

这台机器 `DESKTOP-3IM4KHB` **就是白泽的源机，不搬家**：
- 仓库在 `D:\xm\白泽智能体`（git，`origin`=HDKL0802/baize，`public`=HDKL0802/baize-public，分支 `main`，HEAD `f7ff1ca`）
- WSL 后端正在跑：`baize-backend.service` = **active + enabled**，监听 8787，`/api/health` → `ok:true`
- 工具链齐全，无需重装

**没有**把项目搬到 `G:\DeepSeek Harness`（G 盘仅剩 19.6 GB，且会导致 WSL 服务路径失配）。

---

## 1. 数据安全红线（最重要，已实测确认）

- `agent/data/` 是**活的 SQLite**，被 `.gitignore` 排除，**永不可提交**。
- 交接包里的 `数据包/agent/data` 是 **21:12 停服快照**；此后活库 `agent.db-wal` 已涨到 **288 KB 且仍在增长**（21:23 仍在写）。
- ⛔ **绝对禁止**用数据包覆盖 `D:\xm\白泽智能体\agent\data` —— 会丢掉 21:12 之后的所有写入。
- 数据包只能用于「新位置」，或做对照参考。
- 拷 SQLite 必须**先停服**，且 `.db` + `-wal` + `-shm` 三个一起拷；只拷 `.db` 会丢 WAL 里的数据。

## 2. 敏感文件（均实测为 untracked，安全）

| 文件 | 状态 |
|---|---|
| `agent/data/config.json`（含 API key） | untracked ✅ |
| `agent/data/token` | untracked ✅ |
| `agent/data/admin-password.txt` | untracked ✅ |
| `deploy.local.env` | untracked ✅ |
| `mobile/android/debug.keystore` | untracked ✅ |
| `2026-10-31/` 整个交接包 | untracked（`git status` 只此一条 `??`） ✅ |

发公开仓前自检：`git grep -n -I -E 'sk-[A-Za-z0-9]{16,}|ghp_|GITEE_TOKEN|szk0037'` 必须无输出。

---

## 3. 工具链实际状态（2026-10-31 实测，以此为准）

| 组件 | 实际路径 / 版本 | 备注 |
|---|---|---|
| **Go** | `D:\xm\tools\go\bin\go.exe`，**go1.27.2** | 不在 PATH，脚本已写死路径 |
| **Node/npm** | `C:\Program Files\nodejs`，**v24.22.1 / 11.19.1** | 另有 `C:\node-v22.14.0-win-x64\npm.cmd` 存在 |
| **JDK** | `JAVA_HOME` = `C:\Program Files\BellSoft\LibericaJDK-17`（17.0.20.1） | `build.ps1` 实际命中 `D:\HBuilderX\plugins\amazon-corretto`（17.0.12） |
| ~~`D:\mc\Java\17-lts`~~ | **已不存在** | 文档旧记录，别照抄 |
| **Android SDK** | `build.ps1` 命中 **`D:\android-sdk`**（有 build-tools\34.0.0 + platforms\android-34） | 另有 `D:\Android\Sdk`（含 platform-tools、NDK r30），**但不在 build.ps1 候选链里** |
| **NSIS** | `D:\xm\tools\nsis\Bin\makensis.exe` v3.13 | |
| **adb** | `D:\Android\Sdk\platform-tools\adb.exe`（不在 PATH） | 另有 MuMu 模拟器的 adb |
| **Docker** | Windows 侧**无** docker CLI | 只有 WSL / NAS 有 |
| **Git** | `C:\Program Files\Git\cmd\git.exe` | GCM 管凭据 |

**已知文档错误**：记忆交接包 §4.1 写「主机无 Node/npm」——**错，实际有 v24.22.1**。

---

## 4. 版本号纪律（白泽专用，补充全局规则）

| 端 | 版本单一来源 | 当前值 |
|---|---|---|
| 后端 | `agent/cmd/backend/main.go:40` | 源码 **0.8.0**（WSL 在跑的旧二进制报 0.9.37） |
| agentsvc | `agent/internal/agentsvc/service.go` | **0.8.0** |
| core | `core/service.go:16` | **0.10.0** |
| 桌面端 | `desktop/device_windows.go` 的 `desktopAppVersion`（**单一来源**） | **0.8.0** |
| 手机端 | `mobile/android/AndroidManifest.xml` + `build.ps1` + 设置页「关于」**三处同改** | **0.10.17 / versionCode 1017** |

- 手机 `versionCode` = `主*100 + 次*10 + 补丁`（0.10.17 → 1017），**必须单调递增**。
- 提交粒度：只改后端→1 次；只改前端/手机/桌面→1 次；**前后端都改→2 次分开提**。

## 5. 发版流程（有改动就必须全走）

1. 重新打包：`desktop/build.ps1` + `desktop/build-installer.ps1`；`mobile/android/build.ps1`；后端 Docker 镜像
2. 推 GitHub：`origin`（私有）+ `public`（公开），**两仓都推**
3. 同步 Gitee：`HDKL0037/baize` + `HDKL0037/baize-public`，用 `tools/sync-gitee.ps1`（需 `GITEE_TOKEN`，**本机未存，需新发**）
4. 发三件套：`baize-desktop-setup-<版本>.exe`、`bz-todo-v<版本>.apk`、`ghcr.io/hdkl0802/baize-backend`
5. 每升一版，**所有项目文档**再向两平台各提交一份

---

## 6. 已知坑（照抄别踩）

- `.ps1` 必须**纯 ASCII**；`agent/test-all.ps1` 必须 **UTF-8 with BOM**
- `installer.nsi` 必须 **UTF-8 with BOM**；中文路径让 ANSI `makensis` 失败 → 先拷到 ASCII 临时目录
- APK 中间目录必须 ASCII（`$STAGE`），aapt2 不认中文路径
- 安装包别叫 `setup.exe`（触发兼容性 shim）
- 换 keystore = 不能覆盖安装；换后端 token = 所有端要重新配对
- 不用 x86 模拟器联调 arm64 Go 内核（结论不可信）
- **Go module 是分模块的**：根目录没有 `go.mod`，必须 `go -C agent build` / `go -C core build` / `go -C desktop build`
- 后端 webui/panel.html 是 `go:embed`，**改完必须重编后端**才生效

## 7. 验证命令与实测结果（2026-10-31 全部通过）

```powershell
# 后端健康（WSL 旧二进制，ok:true, 0.9.37）
curl.exe -s http://127.0.0.1:8787/api/health

# 四个 Go 模块编译（全部 EXIT=0）
$env:GOPROXY="https://goproxy.cn,direct"
$go = "D:\xm\tools\go\bin\go.exe"
& $go -C agent   build -o "$env:TEMP\v-backend.exe" .\cmd\backend   # 22.14 MB
& $go -C desktop build -o "$env:TEMP\v-desktop.exe" .               # 桌面端
& $go -C core    build -o "$env:TEMP\v-bzcore.exe" .\cmd\bzcore     # 手机内核
& $go -C cli     build -o "$env:TEMP\v-bz.exe" .                    # CLI

# 三个前端（全部 EXIT=0）
$npm = "C:\Program Files\nodejs\npm.cmd"
cd desktop\webui; & $npm run build   # 8.24s
cd desktop\float; & $npm run build   # 0.93s
cd mobile\webui;  & $npm run build   # 7.11s
```

**源码编译版后端实跑验证**（隔离数据目录 + 8899 端口，不动活库）：
```
{"devicesOnline":0,"ok":true,"protocol":"0.1.0","tasksPending":0,"uptimeSec":3,"version":"0.7.1"}
```
→ 版本 `0.7.1` 与源码常量一致，**证实 WSL 上的 0.9.37 是旧二进制**，不是丢东西。
启动时自动生成：4 个 db + 5 个内置技能 + persona 三件套 + 初始 admin 账号。

## 8. 交接包位置与内容

`D:\xm\白泽智能体\2026-10-31\migration-to-dsh\outputs\`
- `搬迁执行说明.md`（16.5 KB）— 10 节落地手册
- `白泽项目迁移清单.md`（31.4 KB）— 12 节盘点
- `白泽记忆交接包.md`（44.3 KB）— 记忆整理
- `凭据清单-明文.md`（11.3 KB）— **禁止进 Git**
- `配置原文件/`（6 文件）、`数据包/`（0.42 MB）、`记忆原文件/`（0.23 MB）

## 9. 待确认 / 待办

- Gitee 私人令牌未存，需用户新发（`projects` + `releases` 权限）
- NAS `/vol1/@appdata/baize/data` 体积未盘点；NAS 当前跑 Docker 还是 systemd、版本是否 0.7.1 **【待核】**
- `local-embed`（端口 8484）是否仍在用未定
- `core.Version`(0.10.0) 与 App 版本(0.10.17) 漂移，用户未拍板
- `.git/baize-history-backup.bundle`（含旧真实数据）删不删待用户定
