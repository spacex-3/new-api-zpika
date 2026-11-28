# Zpika 上游同步与发布台账

本文件与 `upstream-sync.json` 一起维护。Git 历史记录代码来源，台账记录完整基线、单独回移的修复和未集成清单；镜像与生产部署独立记录。不能用镜像内 VERSION 或“最近看过的上游版本”代替实际同步进度。

## 当前状态（2026-11-28）

| 项目 | 固定版本 |
| --- | --- |
| 完整上游基线 | `bd6bb53019fb7e21804b31ab7365c9b2097f4b6c`（2026-11-09；最近祖先标签 `v1.0.0-rc.49`） |
| 本次比较的上游 | `04dfcbb7128982d78b63a2ecb15f6d20c7305f44`，`v1.0.0-rc.52` |
| 当前业务代码 | `3f9f5c3090c8ca6977d2452c2c87d5061d539aa1` |
| 固定镜像 | `ghcr.io/spacex-3/new-api-zpika:sha-3f9f5c3` |
| GitHub 测试和镜像构建 | [46033034760](https://github.com/spacex-3/new-api-zpika/actions/runs/46033034760)，成功，amd64 / arm64 |
| ARM 部署 | 待执行；原运行版本 `da2a4ce134fe137cee662b31469e743052e12206` |

**这是旧基线加 5 项回移修复，不是完整升级到 rc.52。** 基线之后共 63 个上游提交，5 个已按下面的映射移植，58 个未集成。后者包括功能、修复、文档、重构及撤销提交，并不等于 58 个缺陷。基线仅在真正合入完整上游历史后推进。

本次将多个修复合并为一个二开提交，因此 Git ancestry 仍显示落后 63 个上游提交。这个数字不会自动扣除手工移植内容；以本台账映射核对，不能只看 ahead/behind，也不能用 ours 合并伪造已同步历史。

## 已移植修复

| 上游提交 | 修复 | 移植方式 |
| --- | --- | --- |
| [`4407b8470`](https://github.com/QuantumNous/new-api/commit/4407b8470f4a7e286247da2b59e674639d158dec) | 任务进度 100% 后继续轮询并进入成功/失败处理 | 原补丁移植 |
| [`48f9df96e`](https://github.com/QuantumNous/new-api/commit/48f9df96ea7c68eae04a0f3687869f232f48929f) | 私有渠道同源媒体下载 | 原补丁移植 |
| [`1c63cc8c9`](https://github.com/QuantumNous/new-api/commit/1c63cc8c903630cf16611515d81b9785134daf19) | 请求错误不误禁渠道 | 移植；参数覆盖测试并入现有测试文件 |
| [`3a407cc9b`](https://github.com/QuantumNous/new-api/commit/3a407cc9b064fe79a4ffac4146b2690c465dc929) | 已删除用户的 API Key 返回 401 | 原补丁移植 |
| [`5cc159213`](https://github.com/QuantumNous/new-api/commit/5cc159213b0df1243acebfdb18cecb6f7879ddc4) | JSON 构建纯文本 multipart 请求 | 适配旧版宿主；不引入新凭据运行时 |

所有上述修复落在业务提交 `3f9f5c3090c8ca6977d2452c2c87d5061d539aa1`。保留原任务插件源码、同步/异步调用方式、定价配置与 R2 存储；未引入新供应商/请求插件体系、pluginkit 拆分、依赖升级或敏感词迁移。

## 差异规模与验证

以下统计固定到业务提交 `3f9f5c3`，不包含后来新增的本台账：

- 本次修复相对 `485805b`：18 文件，+865 / -43；业务代码 9 文件，+140 / -42，其余为测试、CI 和接口说明。
- 二开相对完整基线 `bd6bb5301`：148 文件，+9,495 / -354；含后来回移的上游修复，不全是原创二开代码。
- 当前上游 `04dfcbb71` → 二开业务版本：685 文件，+18,654 / -89,615；该口径同时包含二开新增及未合入上游内容，不能解释为二开重写了九万行。
- 任务查询 `go test -count=1 -v ./model -run '^TestUnfinishedTaskQueriesDatabaseMatrix$'`：SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 15.20 全部通过。
- `go test -count=1 -p 2 ./model ./service ./controller ./middleware ./relay ./relay/common ./relay/helper ./relay/channel/task/jsplugin ./relay/channel/openai ./plugins ./pkg/jsplugin ./setting/system_setting`：全部通过。
- `bun test plugins/az.test.js` 与 `bun test workers/task-artifact-r2/src/index.test.js`：各 9 项通过。
- GitHub 完成前端及 Go 镜像编译并发布 amd64 / arm64；后续镜像发布受上述回归检查阻挡。

后续完整同步仍为中高难度：任务路由/即时结果保留、模型计费、错误告警、渠道模型获取及插件运行时是主要冲突区域。前次对 `81c1fd20e` 的预演有 30 个冲突文件；**不是当前 rc.52 的重新预演结果**。最新版已进一步拆分 pluginkit，未来同步前应重新预演。

## 每次更新的维护方式

1. `git fetch origin --prune`，把评估目标固定为完整 SHA 和标签。主分支是移动目标，报告必须带日期与 SHA。
2. 完整同步用保留历史的 merge；独立修复优先 `git cherry-pick -x`，冲突适配在提交说明与台账列明。共享 main 不 rebase、不强推。
3. 回移修复只追加 backports，不推进 full_merge_base。记录上游 SHA → 二开 SHA、用途、测试、是否修改数据库/插件契约。
4. 更新 pending_commits 清单；已被后续替代或明确不采用的内容记录原因，不能无说明删除。确认 Git patch-id 相同只能辅助检查，适配后的补丁仍需人工核对。
5. 生产部署另外记录代码 SHA、固定镜像、时间、插件覆盖版本/哈希及结果。台账等纯文档提交可用 `[skip ci]`；此时仓库 HEAD 与运行镜像 SHA 不同是正常现象，必须标明业务版本。
6. 上线前备份数据库和 Compose、给当前运行镜像打持久回滚标签、在新 image 行旁保留回滚注释。只更新目标服务，检查健康/日志/业务，失败立即切回固定旧镜像。旧镜像清理必须等用户确认运行正常后另行执行。

复核命令（不自动合并或部署）：

```sh
git merge-base 3f9f5c3090c8ca6977d2452c2c87d5061d539aa1 origin/main
git rev-list --count bd6bb53019fb7e21804b31ab7365c9b2097f4b6c..origin/main
git log --reverse --oneline bd6bb53019fb7e21804b31ab7365c9b2097f4b6c..origin/main
git diff --shortstat origin/main 3f9f5c3090c8ca6977d2452c2c87d5061d539aa1
```

## 尚未集成的上游提交（固定到 rc.52）

完整 SHA、状态同时保存在相邻 JSON 文件；下表按历史从旧到新列出。

| 上游提交 | 内容 |
| --- | --- |
| `0409deccf` | fix(deepseek): map developer role to system for chat completions (#8287) |
| `579573ac7` | feat(payment): 易支付支持 RSA 签名方式 (#8291) |
| `be5e3153d` | fix(channels): 切换渠道时重置模型重定向同步状态 (#8304) |
| `a9932d1f9` | fix(web): sync risk confirmation text on composition end and blur |
| `f3d8b737b` | perf: 升级 MoeForge moejs/moego，请求体按需流式读取 (#8311) |
| `7b857f5a1` | feat(plugins): add provider plugins, request plugins and a community marketplace (#8271) |
| `f2aa91232` | chore(plugins): release vertex-ai 1.3.1 (#8313) |
| `0bda086fb` | chore(web-deps): upgrade frontend dependencies (#6999) |
| `6ef123cd5` | perf(password): improve password validation and feedback (#6928) |
| `87c8848f3` | fix(i18n): name the super admin in the compliance lock notice (#6955) |
| `b20e0566b` | fix(web): align TanStack router devtools with the router core (#8320) |
| `1122e0f78` | fix(web): keep channel plugin settings inside the drawer (#8323) |
| `c9c6da278` | fix: 升级 moego 到 v0.1.0-alpha.3，修复无 AVX 的 x86-64 CPU 上的 SIGILL 崩溃 (#8322) |
| `e00ec8819` | feat: add doubao-seedream-5-5-261028 (#8324) |
| `b388140ac` | docs(plugins): recommend a summary length that fits one marketplace line |
| `57345547b` | feat(plugins): 请求处理插件可声明作用范围，管理员确认一次即自动覆盖 (#8335) |
| `09ac035a2` | fix(plugins): match each request plugin scope on its own (#8341) |
| `288dea549` | docs(github): slim agent issue and PR templates (#8344) |
| `f36c460f0` | docs(github): let core maintainers skip agent issue and PR templates (#8352) |
| `ab85ae75d` | fix(web): collapse special group ratio rules by default |
| `ce8fc49e4` | fix(channel): read batch tag changes within the transaction |
| `bb30732b8` | fix(web): unify cache billing categories and duration labels |
| `e8c6a5d95` | fix(relaykit): join Gemini text parts verbatim in chat responses (#8136) |
| `669d77497` | fix(relaykit): map Gemini 4 thinking levels per model (#8367) |
| `3546e9382` | fix(relay): apply GPT-6 chat parameter rules to gpt-6.2-astra and gpt-6.2-sol (#8375) |
| `58f786fbf` | docs(security): document expected access to Midjourney images (#8383) |
| `a1d1fb0cc` | docs(security): clarify scope for root-published site content (#8384) |
| `b2d12187a` | perf(relay): parallelize image compression with moego WebP |
| `329e899eb` | fix: 修复图片任务插件参数覆盖并补充百炼回归测试 (#8027) |
| `a85f7e844` | fix(relaykit): match Gemini 4 thinking levels by family prefix (#8412) |
| `ac1330c5c` | feat(kling): map sizes to the 4:3 and 3:4 aspect ratios of kling-4.0 (#8348) |
| `8542c9d64` | fix(pricing): 修复模型广场显示私有分组的问题 |
| `185e1e51a` | chore(deps): 升级 moego 至 v0.1.0-alpha.5 并统一正则库 |
| `1a395c76a` | fix(payment): verify partner ID on Epay RSA callbacks (#8422) |
| `51a1ceeae` | fix(deepseek): report supported endpoint metadata (#8296) |
| `ede88eb1c` | feat(plugins): 支持批量更新已安装的市场插件 |
| `cb074d31e` | fix(pricing): 按阶梯展示价格同步中的上游表达式 |
| `c53dcf335` | fix(relaykit): report cached tokens in Gemini usageMetadata built from chat usage (#8333) |
| `6627f0e3f` | chore(deps): bump go.opentelemetry.io/otel/trace from 1.41.0 to 1.47.0 (#8326) |
| `def76a60d` | feat(plugins): 放宽插件响应和 JSON 克隆的大小限制 |
| `c27abf362` | feat(pricing): 模型广场支持推荐顺序和按用量排序 |
| `81c1fd20e` | fix(models): 打开排序弹窗时再读取系统选项 (#8454) |
| `fde819b05` | fix(groups): 匿名请求公开分组接口时跳过用户分组查询 (#8461) (#8463) |
| `00056f5db` | Merge commit from fork |
| `5f8525d83` | fix(claude): map output_tokens_details.thinking_tokens to reasoning_tokens (#8431) |
| `3333fc676` | fix(gemini): bill audio inlineData by length, not as images, when usage is missing (#8193) |
| `caa32a301` | fix(relay): remove trailing newline from ClaudeChunkData data frame (#8474) |
| `0c1be7b1a` | refactor(plugins): 把插件 API v1 拆成独立模块 pluginkit (#8475) |
| `5ff8157dd` | fix(relaykit): flatten top-level oneOf/anyOf/allOf in Claude input_schema (#8476) |
| `dc2750cd5` | fix(pluginkit): 单独构建时使用仓库内的 relaykit (#8477) |
| `d6ab606bd` | feat(hailuo): support MiniMax-H3-Max with its own durations, resolutions and usage profile (#8453) |
| `bea361647` | feat(relay): 路由转换器支持 OpenAI Responses 转 Anthropic Messages (#8465) |
| `92866e5ea` | feat(plugin): 提供商插件的路由可按渠道配置开关（enabled_by） (#8478) |
| `5456425f9` | Revert "feat(plugin): 提供商插件的路由可按渠道配置开关（enabled_by） (#8478)" (#8479) |
| `50a976702` | fix(relay): return 404 for models missing from all eligible groups (#8480) |
| `d35242c55` | chore(plugins): release Kling 1.2.1 |
| `944464420` | feat(plugin): 插件渠道可按渠道开关提供商插件的路由 (#8482) |
| `04dfcbb71` | docs(agents): fix stale facts and trim repeated rules in agent instructions |
