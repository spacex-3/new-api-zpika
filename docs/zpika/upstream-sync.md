# Zpika 上游同步与发布台账

本文件与 `upstream-sync.json` 一起维护。Git 历史记录代码来源，台账记录完整基线、单独回移的修复和未集成清单；镜像与生产部署独立记录。不能用镜像内 VERSION 或“最近看过的上游版本”代替实际同步进度。

## 当前状态（2026-11-28）

| 项目 | 固定版本 |
| --- | --- |
| 完整上游基线 | `bd6bb53019fb7e21804b31ab7365c9b2097f4b6c`（2026-11-09；最近祖先标签 `v1.0.0-rc.49`） |
| 本次比较的上游 | `04dfcbb7128982d78b63a2ecb15f6d20c7305f44`，`v1.0.0-rc.52` |
| 最新业务代码 | `ec3b6d0cc1f4220e874a52fbf4aeaf232e627e19` |
| 新版本固定镜像 | `ghcr.io/spacex-3/new-api-zpika:sha-ec3b6d0`（已发布 amd64 / arm64） |
| GitHub 测试和镜像构建 | [46134721348](https://github.com/spacex-3/new-api-zpika/actions/runs/46134721348)，全部成功 |
| ARM 部署 | 2026-11-28 11:20（北京时间）完成；`da2a4ce` → `3f9f5c3`，arm64，healthy |

**这是旧基线加 14 项回移修复，不是完整升级到 rc.52。** 基线之后共 63 个上游提交，14 个已按下面的映射移植，49 个未集成。后者包括功能、修复、文档、重构及撤销提交，并不等于 49 个缺陷。第二批仅发布代码与镜像，ARM 仍运行第一批 `3f9f5c3`。基线仅在真正合入完整上游历史后推进。

第一批将多个修复合并为一个二开提交，第二批使用 `cherry-pick -x`，因此 Git ancestry 仍显示落后 63 个上游提交。这个数字不会自动扣除手工移植内容；以本台账映射核对，不能只看 ahead/behind，也不能用 ours 合并伪造已同步历史。

## 第一批已移植修复

| 上游提交 | 修复 | 移植方式 |
| --- | --- | --- |
| [`4407b8470`](https://github.com/QuantumNous/new-api/commit/4407b8470f4a7e286247da2b59e674639d158dec) | 任务进度 100% 后继续轮询并进入成功/失败处理 | 原补丁移植 |
| [`48f9df96e`](https://github.com/QuantumNous/new-api/commit/48f9df96ea7c68eae04a0f3687869f232f48929f) | 私有渠道同源媒体下载 | 原补丁移植 |
| [`1c63cc8c9`](https://github.com/QuantumNous/new-api/commit/1c63cc8c903630cf16611515d81b9785134daf19) | 请求错误不误禁渠道 | 移植；参数覆盖测试并入现有测试文件 |
| [`3a407cc9b`](https://github.com/QuantumNous/new-api/commit/3a407cc9b064fe79a4ffac4146b2690c465dc929) | 已删除用户的 API Key 返回 401 | 原补丁移植 |
| [`5cc159213`](https://github.com/QuantumNous/new-api/commit/5cc159213b0df1243acebfdb18cecb6f7879ddc4) | JSON 构建纯文本 multipart 请求 | 适配旧版宿主；不引入新凭据运行时 |

所有上述修复落在业务提交 `3f9f5c3090c8ca6977d2452c2c87d5061d539aa1`。保留原任务插件源码、同步/异步调用方式、定价配置与 R2 存储；未引入新供应商/请求插件体系、pluginkit 拆分、依赖升级或敏感词迁移。

## 第一批差异规模与验证

以下统计固定到业务提交 `3f9f5c3`，不包含后来新增的本台账：

- 本次修复相对 `485805b`：18 文件，+865 / -43；业务代码 9 文件，+140 / -42，其余为测试、CI 和接口说明。
- 二开相对完整基线 `bd6bb5301`：148 文件，+9,495 / -354；含后来回移的上游修复，不全是原创二开代码。
- 当前上游 `04dfcbb71` → 二开业务版本：685 文件，+18,654 / -89,615；该口径同时包含二开新增及未合入上游内容，不能解释为二开重写了九万行。
- 任务查询 `go test -count=1 -v ./model -run '^TestUnfinishedTaskQueriesDatabaseMatrix$'`：SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 15.20 全部通过。
- `go test -count=1 -p 2 ./model ./service ./controller ./middleware ./relay ./relay/common ./relay/helper ./relay/channel/task/jsplugin ./relay/channel/openai ./plugins ./pkg/jsplugin ./setting/system_setting`：全部通过。
- `bun test plugins/az.test.js` 与 `bun test workers/task-artifact-r2/src/index.test.js`：各 9 项通过。
- GitHub 完成前端及 Go 镜像编译并发布 amd64 / arm64；后续镜像发布受上述回归检查阻挡。

后续完整同步仍为中高难度：任务路由/即时结果保留、模型计费、错误告警、渠道模型获取及插件运行时是主要冲突区域。前次对 `81c1fd20e` 的预演有 30 个冲突文件；**不是当前 rc.52 的重新预演结果**。最新版已进一步拆分 pluginkit，未来同步前应重新预演。

## 第二批：模型兼容、用量与分组可见性（2026-11-28）

本批将用户同意的六类修复一起处理：GPT-6.2 参数识别、Gemini 文本/思考等级、缓存与思考 token 元数据、Gemini 音频兜底计费、Claude 工具 schema 兼容、私有分组价格与性能数据可见性。共 9 个上游提交，全部原样 `cherry-pick -x`；每个提交保留上游来源。Gemini 思考等级的早期修复由紧随其后的 family-prefix 修复完善，两个来源均保留。

| 上游提交 | 二开提交 | 修复 |
| --- | --- | --- |
| `3546e9382` | `7d736e680` | fix(relay): apply GPT-6 chat parameter rules to gpt-6.2-astra and gpt-6.2-sol (#8375) |
| `e8c6a5d95` | `89760f26c` | fix(relaykit): join Gemini text parts verbatim in chat responses (#8136) |
| `669d77497` | `c5f2eb553` | fix(relaykit): map Gemini 4 thinking levels per model (#8367) |
| `a85f7e844` | `23738b494` | fix(relaykit): match Gemini 4 thinking levels by family prefix (#8412) |
| `c53dcf335` | `a11bf08e4` | fix(relaykit): report cached tokens in Gemini usageMetadata built from chat usage (#8333) |
| `5f8525d83` | `7e07ad4fe` | fix(claude): map output_tokens_details.thinking_tokens to reasoning_tokens (#8431) |
| `3333fc676` | `115a4758d` | fix(gemini): bill audio inlineData by length, not as images, when usage is missing (#8193) |
| `5ff8157dd` | `134d66dd1` | fix(relaykit): flatten top-level oneOf/anyOf/allOf in Claude input_schema (#8476) |
| `8542c9d64` | `9b7fadd14` | fix(pricing): 修复模型广场显示私有分组的问题 |

额外二开兼容改动：反馈入口 `/api/feedback-page` 补齐 session-only 路由登记，保持原有登录要求及 PAT 拒绝行为；CI 增加 relaykit 独立构建、三数据库分组过滤与前端检查，全部通过后才能发布镜像。PostgreSQL 测试 DSN 改用 URL，以符合控制器测试的本机地址校验。

任务插件源码、宿主契约、依赖版本、数据库结构与迁移均未改动。未合入新插件运行时或 pluginkit 拆分。Claude 顶层联合 schema 转换沿用上游的有损展平策略，并保留诊断标记；不含顶层联合类型的工具定义不受此逻辑影响。Gemini 音频改动只处理缺失 usage 时的估算，避免将 PCM 音频当成图片计量。

差异统计固定到业务提交 `ec3b6d0cc1f4220e874a52fbf4aeaf232e627e19`，不含随后更新的本台账：

- 本批相对 `261050077`：34 文件，+1,607 / -160；其中业务代码 22 文件，+496 / -144，其余为测试与 CI。
- 二开累计相对完整基线：181 文件，+11,667 / -509（含已有台账文件）。
- 最新上游 `04dfcbb71` → 二开：658 文件，+19,109 / -88,053（含已有台账文件；同时计入未合入的上游内容）。
- 本批 9 个补丁无 cherry-pick 冲突；后续完整合并仍为中高风险，主要在任务/插件运行时、计费、渠道、公共路由及前端二开入口；本次未重新做完整合并预演。

实际本地验证（Go 1.26.10、Bun 1.4.4；数据库均在本地临时容器，未接入生产）：

```sh
GOWORK=off go build -p 2 ./...
# 以下包分两批运行；路由登记修复后另复测 router 和 middleware。
GOWORK=off go test -count=1 -p 2 ./model ./service ./controller ./middleware ./relay ./relay/common ./relay/helper ./relay/channel/task/jsplugin ./relay/channel/openai ./relay/channel/gemini ./relay/channel/claude ./router ./pkg/perf_metrics ./plugins ./pkg/jsplugin ./setting/system_setting
# relaykit/ 下独立执行：
GOWORK=off go build ./...
GOWORK=off go test -count=1 -p 2 ./...
# 设置仅指向本机临时实例的 TEST_MYSQL_DSN / TEST_POSTGRES_DSN 后：
go test -count=1 -v ./model -run '^TestUnfinishedTaskQueriesDatabaseMatrix$'
go test -count=1 -v ./controller -run '^TestModelSquareGroupVisibility$'
bun test plugins/az.test.js workers/task-artifact-r2/src/index.test.js
# web/ 下：
bun check -b
bun run build
bun run test src/features/performance-metrics/__tests__/summary.test.tsx
bun x --no-install oxlint -c .oxlintrc.json src/features/performance-metrics/api.ts src/features/performance-metrics/__tests__/summary.test.tsx src/features/dashboard/components/models/performance-overview.tsx src/features/dashboard/components/overview/performance-health-panel.tsx
bun x --no-install oxfmt --check src/features/performance-metrics/api.ts src/features/performance-metrics/__tests__/summary.test.tsx src/features/dashboard/components/models/performance-overview.tsx src/features/dashboard/components/overview/performance-health-panel.tsx
```

以上最终检查均通过：16 个根模块包、relaykit 全包；SQLite 3.50.4 / MySQL 8.0.46 / PostgreSQL 15.20 两项矩阵测试；前端 7 项与 AZ/R2 18 项。前端回归先在旧实现验证 3 项失败，再回移实现后全部通过。全仓前端 lint 另有既存无关错误，本次仅对 4 个改动文件完成 lint/格式验证，不宣称全仓 lint 通过。

安全参考：[OWASP ASVS 5.0.0 V8](https://github.com/OWASP/ASVS/blob/v5.0.0_release/5.0/en/0x17-V8-Authorization.md) 的 8.2.1、8.2.2、8.2.3、8.3.1，以及 Authentication、Session Management、Authorization Cheat Sheets。分组过滤在服务端执行，性能接口禁用缓存；管理汇总覆盖匿名 401、普通用户 403、管理员错误 PAT scope 403、管理员 `log:read` 200。这里只验证相关授权路径，不代表完整 ASVS 认证。

发布状态：GitHub 已推送 `ec3b6d0cc1f4220e874a52fbf4aeaf232e627e19`，GitHub 回归与双架构构建全部成功，`docker buildx imagetools inspect` 已确认 amd64 / arm64 镜像存在，索引 digest 为 `sha256:5d8ff08fd8670673992285bbdfd45e59134f7d77a174df3031136bab48b71ba8`。本批未部署 ARM、未进行本批生产收费调用或失败退款试验。第一批用户已确认运行正常；未清理任何生产镜像，线上插件热更新状态仍以此前部署记录为准。

## ARM 发布记录：2026-11-28

- 只更新目标 Compose 项目的应用服务 `new-api-vip`，使用 GitHub 已构建的 `sha-3f9f5c3`；未在 ARM 编译。其余运行中容器的 ID、镜像和启动时间保持一致。
- 新镜像 revision：`3f9f5c3090c8ca6977d2452c2c87d5061d539aa1`；镜像 ID：`sha256:bb9de28edc999ef0553f203edeabcc93844fd372463089c1b871fc0134c58f61`。
- 发布镜像索引 digest：`sha256:3b3a9fbcf821f4107394294b1b3f8b25dce6d736f77795c222bf6ce9c5430cce`。
- 切换前已备份 PostgreSQL 数据库（custom archive，109,600,141 字节，`pg_restore --list` 校验通过）、Compose 和运行配置；旧镜像已打持久回滚标签，Compose 新 image 行旁保留回滚注释。完整备份在 ARM 受限目录；额外本机副本因传输缓慢取消，不算异机备份。
- 实际执行 `docker compose pull new-api-vip` 和 `docker compose up -d --no-build --no-deps --timeout 130 new-api-vip`。数据库、Redis 及其他服务未重建；没有新增数据库迁移或调整现有插件、渠道、价格设置。
- 14 个插件注册正常。下面四个数据库热更新覆盖插件的版本、哈希、启用状态与部署前一致；源码哈希见 JSON 的 deployment.plugin_overrides。

| 插件 | 继续使用的线上版本 |
| --- | --- |
| az | 1.1.0 |
| cangyuan | 1.1.1 |
| doubao | 1.4.3 |
| maitoken | 1.1.4 |

生产验证及边界：

- `docker inspect`：正确 revision、arm64 镜像、healthy、RestartCount=0；无 panic 或启动失败。后续流量日志包含旧版本已出现的 Grok 上游超时/500，不代表所有渠道均健康。
- 状态接口、管理令牌身份验证、登录页面、55 个渠道、插件运行状态、78 项模型价格、消费日志与任务列表均 HTTP 200。未使用密码进行浏览器登录。
- 数据库插件记录与站点 options 哈希不变；从备份抽取渠道数据逐字段比较，只有正常消费引起的 used_quota 变化。价格列表使用 Go map 生成，数组顺序不稳定，原始 JSON 哈希不作为价格变更判据；确认的是持久定价配置不变及价格接口可读。
- 升级前后各一次 `gpt-5.6-luna` 普通同步请求成功，均有 usage 和对应消费日志（每次 quota=6、tokens=16）。上线后另观察到普通模型真实成功流量。
- 四个常用任务插件的历史成功任务及制品列表读取成功。较新的 cangyuan 图片签名链接经应用 307 跳转后，使用浏览器请求头的 `curl --head --location --max-time 25` 返回 200。Python 默认请求头曾被 Cloudflare 1010 拦截；较旧 AZ/豆包/MaiToken 媒体样本返回 410，首个 AZ 样本升级前后相同。
- 本次未额外提交收费图片/视频任务，也未在生产主动制造失败/退款；提交、轮询、失败、退款及私有媒体处理的回归以以上 CI 为依据，不能把历史任务读取说成全流程生产实测。
- 未清理旧镜像。用户已确认运行正常；回滚时切换 Compose 中相邻的固定旧镜像注释，再执行 `up -d --no-build --no-deps --timeout 130 new-api-vip`。本次未新增 schema 变更，常规应用回滚不需要恢复数据库；数据库备份仅在数据恢复确有需要时使用，避免覆盖上线后的新增交易。

## 每次更新的维护方式

1. `git fetch origin --prune`，把评估目标固定为完整 SHA 和标签。主分支是移动目标，报告必须带日期与 SHA。
2. 完整同步用保留历史的 merge；独立修复优先 `git cherry-pick -x`，冲突适配在提交说明与台账列明。共享 main 不 rebase、不强推。
3. 回移修复只追加 backports，不推进 full_merge_base。记录上游 SHA → 二开 SHA、用途、测试、是否修改数据库/插件契约。
4. 更新 pending_commits 清单；已被后续替代或明确不采用的内容记录原因，不能无说明删除。确认 Git patch-id 相同只能辅助检查，适配后的补丁仍需人工核对。
5. 生产部署另外记录代码 SHA、固定镜像、时间、插件覆盖版本/哈希及结果。台账等纯文档提交可用 `[skip ci]`；此时仓库 HEAD 与运行镜像 SHA 不同是正常现象，必须标明业务版本。
6. 上线前备份数据库和 Compose、给当前运行镜像打持久回滚标签、在新 image 行旁保留回滚注释。只更新目标服务，检查健康/日志/业务，失败立即切回固定旧镜像。旧镜像清理必须等用户确认运行正常后另行执行。

复核命令（不自动合并或部署）：

```sh
git merge-base ec3b6d0cc1f4220e874a52fbf4aeaf232e627e19 origin/main
git rev-list --count bd6bb53019fb7e21804b31ab7365c9b2097f4b6c..origin/main
git log --reverse --oneline bd6bb53019fb7e21804b31ab7365c9b2097f4b6c..origin/main
git diff --shortstat origin/main ec3b6d0cc1f4220e874a52fbf4aeaf232e627e19
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
| `58f786fbf` | docs(security): document expected access to Midjourney images (#8383) |
| `a1d1fb0cc` | docs(security): clarify scope for root-published site content (#8384) |
| `b2d12187a` | perf(relay): parallelize image compression with moego WebP |
| `329e899eb` | fix: 修复图片任务插件参数覆盖并补充百炼回归测试 (#8027) |
| `ac1330c5c` | feat(kling): map sizes to the 4:3 and 3:4 aspect ratios of kling-4.0 (#8348) |
| `185e1e51a` | chore(deps): 升级 moego 至 v0.1.0-alpha.5 并统一正则库 |
| `1a395c76a` | fix(payment): verify partner ID on Epay RSA callbacks (#8422) |
| `51a1ceeae` | fix(deepseek): report supported endpoint metadata (#8296) |
| `ede88eb1c` | feat(plugins): 支持批量更新已安装的市场插件 |
| `cb074d31e` | fix(pricing): 按阶梯展示价格同步中的上游表达式 |
| `6627f0e3f` | chore(deps): bump go.opentelemetry.io/otel/trace from 1.41.0 to 1.47.0 (#8326) |
| `def76a60d` | feat(plugins): 放宽插件响应和 JSON 克隆的大小限制 |
| `c27abf362` | feat(pricing): 模型广场支持推荐顺序和按用量排序 |
| `81c1fd20e` | fix(models): 打开排序弹窗时再读取系统选项 (#8454) |
| `fde819b05` | fix(groups): 匿名请求公开分组接口时跳过用户分组查询 (#8461) (#8463) |
| `00056f5db` | Merge commit from fork |
| `caa32a301` | fix(relay): remove trailing newline from ClaudeChunkData data frame (#8474) |
| `0c1be7b1a` | refactor(plugins): 把插件 API v1 拆成独立模块 pluginkit (#8475) |
| `dc2750cd5` | fix(pluginkit): 单独构建时使用仓库内的 relaykit (#8477) |
| `d6ab606bd` | feat(hailuo): support MiniMax-H3-Max with its own durations, resolutions and usage profile (#8453) |
| `bea361647` | feat(relay): 路由转换器支持 OpenAI Responses 转 Anthropic Messages (#8465) |
| `92866e5ea` | feat(plugin): 提供商插件的路由可按渠道配置开关（enabled_by） (#8478) |
| `5456425f9` | Revert "feat(plugin): 提供商插件的路由可按渠道配置开关（enabled_by） (#8478)" (#8479) |
| `50a976702` | fix(relay): return 404 for models missing from all eligible groups (#8480) |
| `d35242c55` | chore(plugins): release Kling 1.2.1 |
| `944464420` | feat(plugin): 插件渠道可按渠道开关提供商插件的路由 (#8482) |
| `04dfcbb71` | docs(agents): fix stale facts and trim repeated rules in agent instructions |
