# 支持文件示例（references/checklist.md）

`references/` 里的文件会**跟着技能一起装到用户机器上**（落在技能目录的 `references/` 下）。
适合放：检查清单、模板、长参考、代码片段。

技能正文里引用它：

> 按 `references/checklist.md` 里的清单逐条检查。

放这里的好处：**不占系统提示的预算**——正文里只写一句"去看哪个文件"，需要时再读。

## 发布前自检清单（示例）

- [ ] `plugin.json` 的 `id` 与目录名一致，且只含小写字母/数字/`-`/`.`/`_`
- [ ] 每个技能的 `SKILL.md` 以 `---` 开头，`name` 与 `description` 都写了
- [ ] `description` 不超过 60 个字，且先说清触发场景
- [ ] 正文里出现的每个工具名都是白泽真实注册的工具
- [ ] 技能目录名没有和内置技能（`make-skill`/`file-reader`/`office-files`/`cron`/`note-taking`）撞名
- [ ] 跑过 `tools\build.ps1`，`index.json` 与 `packages/` 是**同一次**生成的
