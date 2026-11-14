# 白泽更新日志（本地兜底摘要）

> 本文件是官网更新日志页在 GitHub / Gitee Releases 都拉不到时的本地兜底内容。
> 正常情况下页面直接同步 GitHub Releases（HDKL0802/baize-public），不读这份文件。
> 格式约定：每条以 @@@ tag: 开头，可跟 name / date / url，其后为 Markdown 正文。
> 完整发布说明见仓库 desktop/RELEASE_NOTES.md；安装包见 GitHub / Gitee Releases。

@@@ tag: desktop-v0.8.3
@@@ name: 白泽桌面端 0.8.3
@@@ date: 2026-11-14T22:30:00Z
# 白泽 0.8.3

> 补丁版本（+0.0.1）。0.8.2「多生态插件接入」的补齐版，把上一版如实挂着的六个缺口逐一收口。

## 这一版做了什么

- 修登记接口语义缺口：register 登记前校验白泽格式 index.json，DSH 原始格式 1 明确 400 并指引先 sync。
- 探测改为有界并发（8 worker）+ 同仓库缓存（含负缓存）+ 单条 / 整轮双超时。
- 桌面端内置「DSH 生态市场（内置）」只读源（离线快照 11141 条，含 143 个桌宠类插件），npm / cordis 形态明确禁用安装。
- Trae 生态适配器可选开关（需自备白泽格式索引地址；Trae 自身格式本轮未逆向）。
- 官网新增更新日志页，数据同步 GitHub Releases，失败回退 Gitee 再回退本地兜底。
- 补三接口 handler 级测试、探测池测试、内置市场测试；WSL 跑 Go 全量测试；Docker 构建并起容器实测接口。

## 不含（如实说明）

- 未逆向 Trae，Trae 适配器只是自带索引地址的挂载钩子。
- 内置 DSH 市场是离线快照，不会联网自动刷新。
- 手机端沿用 bz-todo-v0.10.17（versionCode 1017）。

@@@ tag: desktop-v0.8.2
@@@ name: 白泽桌面端 0.8.2
@@@ date: 2026-11-14T09:01:33Z
# 白泽 0.8.2

> 多生态插件接入：把别的 Agent 生态的插件市场接进白泽（当前支持 DSH 插件市场的索引格式 1）。

## 这一版做了什么

- 新增「生态插件源」能力：能读 DSH 插件市场的索引，按内容形态把插件分成技能型（带 SKILL.md）与工具型（带 MCP 配置）转译成白泽插件包；纯 cordis 运行时插件只进目录、不假装能装。
- 新增接口：GET /api/agent/ecology/dsh（拉目录）、POST /api/agent/ecology/dsh/sync（转译）、POST /api/agent/ecology/dsh/register（登记插件源）。
- 生态插件一律标为社区来源，逐条记录来源仓库与许可证；探测不到内容形态时如实说明「现在还说不准」，不与「确认不能装」混为一谈。
- 全程只读第三方公开内容，不执行第三方代码、不自动安装。

@@@ tag: desktop-v0.8.1
@@@ name: 白泽桌面端 0.8.1
@@@ date: 2026-11-14T04:27:27Z
# 白泽 0.8.1

> 补丁版本（+0.0.1）。把「技能」页的编辑改成读磁盘上的 SKILL.md 原文再改，不再用 name / description / 正文重组整篇。

## 变更

- 后端新增 GET /api/agent/skills/raw?name=<技能名>：返回磁盘原文；缺参数 400、不存在 404、读盘失败 500，都是明确报错，不静默返回空内容。
- 桌面端点「编辑」先拉原文；读取失败直接报错并停止编辑，不再用重组内容假装成功。保存时按原文整篇写回，front-matter 原样保留。
- 新增后端测试 TestSkillRawRead，断言带 triggers / version 字段的 SKILL.md 能被原样读出。

## 不含

- 手机端沿用 bz-todo-v0.10.17（versionCode 1017），本版不动手机端。
- 模型通道 / 外部 Agent / 频道 / 插件市场 / 人设 / 备份 / 可观测性 / 文件浏览器 / 独立知识库 / 内置浏览器在 0.8.0 已接线，本版无改动。
