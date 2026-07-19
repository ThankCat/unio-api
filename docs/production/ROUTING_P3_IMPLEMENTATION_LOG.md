# P3 线路内负载均衡改造记录

> 状态：**已完成**
> 开始/完成日期：2026-07-19
> 设计基线：[ROUTING_P3_LOAD_SPREAD.md](./ROUTING_P3_LOAD_SPREAD.md)

本文是本次改造的执行账本。每完成一个模块立即更新对应状态、实际改动、测试结果与偏差，最终作为上线验收和复盘依据。

## 1. 完成定义

- 所有线路只使用显式 `route_channels`，删除 `pool_kind/all`；
- mode 只保留 `balanced/fixed`；
- balanced 只在线路池内按全局渠道容量和健康度调度，成本不参与排序；
- sticky、fallback、重试永远不突破线路池；
- 毛利硬校验覆盖线路、模型和渠道成本变更，运行时保留最后一道摘除；
- `/v1/models` 与 API Key 当前线路一致；
- 渠道/服务商归档不能让启用线路空池；
- Admin 可在 3 秒刷新窗口看到容量、健康、权重和最近路由决策，10 秒判定陈旧；
- 真实上游 E2E 是发布门禁，纯 mock 不替代真实验收。

## 2. 工作区保护

开始改造时检测到已有未提交改动，视为用户工作并保留：

- 后端：adapter 上游错误/响应体限制、`channeltest` 等文件；
- 前端：渠道模型/价格/测试弹窗、模型列、请求单元格、sidebar 等文件。

若本次必须触碰同一文件，先逐段读取并在现有改动上增量编辑，不回退、不覆盖。

## 3. 执行清单

### A. 审计与基线

- [x] 设计决议收口并加入商业门禁、真实上游 E2E、实时可观测要求。
- [x] 完成 schema、routing、lifecycle、价格服务、Admin 和测试影响面清单。
- [x] 记录改造前可运行的基线测试结果。

### B. Schema 与线路契约

- [x] `routes.mode` 只允许 `balanced/fixed`。
- [x] 删除 `routes.pool_kind`、相关 CHECK、SQL/sqlc/DTO/前端字段。
- [x] 后端线路写入强制显式渠道池；balanced >=1，fixed ==1。
- [x] 新增 routing decision trace 持久化结构和清理查询。
- [x] 空库重建与 sqlc generate 通过。

### C. Routing 边界与模型可见性

- [x] `FindRouteCandidates` 始终通过 `route_channels`。
- [x] 后端删除 `PoolKind` 运行时参数和全局候选分支。
- [x] `/v1/models` 按 API Key 线路返回可路由模型并集。
- [x] 未绑定渠道在首选、sticky、fallback、重试中均不可出现。

### D. Balanced 调度与容量

- [x] 实现可注入随机源的加权不放回排序。
- [x] 容量分使用 channel-level 全局并发/TPM 剩余比例最小值。
- [x] 健康因子覆盖错误率和延迟，breaker open 继续硬摘除。
- [x] 容量未知、部分未知、全部为零的退化规则落地。
- [x] 排序只读，实际 admit 继续使用 Redis 原子预占。

### E. 毛利与配置护栏

- [x] 共享毛利校验器覆盖倍率路径、绝对成本和全部计价分项。
- [x] route/model price/channel cost/multiplier/recharge 变更联动校验并原子回滚。
- [x] 运行时负毛利候选硬摘除并告警。
- [x] 渠道/服务商归档空池护栏和“替换并归档”原子操作。

### F. 可观测与 Admin API

- [x] balanced/sticky/margin/trace 指标和结构化日志。
- [x] routing decision trace：异常 100%，普通成功默认 5%，默认保留 7 天。
- [x] Admin route runtime 聚合 Redis 容量、多 gateway 健康和 1m/5m 统计。
- [x] 3 秒刷新、10 秒 stale 所需 DTO 与接口。
- [x] request/route 路由决策查询接口。

### G. Admin 前端

- [x] 线路表单删除池类型和旧 mode，只保留 balanced/fixed 与渠道选择。
- [x] 线路详情运行时视图、模型/协议过滤、最近决策详情。
- [x] 毛利、无冗余、空池、归档影响和 stale 状态展示。
- [x] 引入 Vitest/Testing Library 与 Playwright 测试入口。

### H. 验证与发布

- [x] Go 单元、DB/sqlc、Redis、race、build、vet 全绿。
- [x] Admin lint/build/component/E2E 全绿。
- [x] 空库重建和 Redis 旧 ID 清理验证。
- [x] 多线路共享渠道容量测试无超卖。
- [x] 至少两条同模型真实上游渠道完成 balanced/fallback/sticky/fixed E2E。
- [x] Admin 在真实请求后 10 秒内显示运行时和 routing decision trace。

## 4. 实施日志

### 2026-07-19 - 启动

- 创建本执行账本。
- 已确认数据库可重建、真实上游费用不设限，但仍采用独立 E2E 配置和凭据脱敏。
- 改造前后端基线已执行：Go 仅 `internal/platform/config` 受当前 `LOG_FORMAT=json` 环境污染失败，其余包通过；Admin build/lint 存在既有未提交改动导致的 TypeScript/ESLint 错误。
- 影响面审计完成：
  - schema/query：`000002_routes`、`000030_route_channels`、API `channel_models/models`、Admin `route/channel`；
  - 后端契约：route service/admin API、routeops/channelops、routing router、modelcatalog；
  - lifecycle：候选排序、三协议候选装配、ratelimit/concurrency 只读快照；
  - 商业护栏：route/modelprice/channelprice/cost multiplier/recharge factor、channel/provider archive；
  - 实时观测：现有 `gatewayruntime.Client` 已支持多实例 breaker worst-wins，可扩展容量与健康快照；
  - 前端：routes API/form/detail/table/display、channel route 引用列和 requests mode label。
- 下一步：修改 schema/SQL/route 契约并重新生成 sqlc。

### 2026-07-19 - Schema 与后端线路契约

- 目标 schema 的 `routes.mode` CHECK 已收紧为 `balanced/fixed`，删除 `routes.pool_kind`；`route_channels` 成为唯一线路渠道来源。
- Admin route/channel SQL 删除 `all` 分支和池类型字段；API `FindRouteCandidates` 无条件要求候选命中 `route_channels`。
- 已执行 `sqlc generate`，生成的 `sqlc.Route`、Create/Update/FindRouteCandidates 参数和运维 DTO 不再含 `PoolKind`。
- route service/Admin API 只接受 `balanced/fixed`；创建、更新、单独换池分别强制 `balanced >= 1`、`fixed == 1` 个去重后的有效渠道，事务内整体替换渠道池。
- router 不再传递池类型；blackbox fixture 改为创建渠道后显式写入 `route_channels`。
- 本检查点未做数据库重建，也未改 Admin 前端；两项分别留在 B/H 与 G 阶段验收。
- 下一步：修正 `/v1/models` 的线路边界，并用显式线路池补齐 DB 负向测试；随后替换 lifecycle 旧排序。

### 2026-07-19 - 路由边界与模型可见性

- `/v1/models` 从认证 principal 读取必填 `route_id`；model catalog 查询参数改为 `(user_id, route_id)`。
- 可见模型查询只统计当前 `route_channels` 内满足 model/channel/provider enabled、credential valid、模型基准售价生效且渠道成本可解析的模型；不同协议渠道取并集。
- DB 测试夹具不再使用 `route_id=0` 全局候选，统一显式创建 balanced 线路并绑定渠道。
- 新增负向 DB 用例：未绑定渠道即使全量启用且价格完整，也不进入 `FindRouteCandidates`；仅存在于未绑定渠道的模型不进入目录。
- 定向测试在当前未配置 `DATABASE_URL` 时 DB 用例按既有机制 skip；空库重建阶段必须在真实 PostgreSQL 上重跑。
- 下一步：实现 Redis 全局容量事实和 weighted-random-without-replacement balanced 调度。

### 2026-07-19 - Balanced 调度与全局容量

- lifecycle 删除成本/稳定/纯随机旧策略分支；balanced 在 capability + breaker/cooldown 硬过滤后评分，fixed 保持唯一候选。
- scorer 统一产出并发/TPM 剩余比、`capacity_score=min(...)`、健康因子、最终 weight 和压力；成本快照不参与评分。
- 加权随机采用不放回完整排序，随机源可注入；正权重候选优先，零容量候选保留在 fallback 尾部；全部为零时最低综合压力候选排首进入现有短等。
- 容量单项未知用另一项、全部未知池内均匀随机；读取错误标记 `CapacityDegraded`，不扩大候选池。
- TPM 快照复用 Redis 滑动窗口 `amount=0` 只读汇总；实际 `AllowChannel` 预占不变。
- 生产并发限制从单进程计数切到 Redis ZSET 租约信号量，Lua 原子准入，TTL + 心跳覆盖长流，进程崩溃后租约自动回收；channel key 在多线路/多 gateway 间共享。
- breaker 健康分加入延迟 EWMA，与错误率组合后保持 `(0,1]` 的正健康因子；open 仍在评分前硬过滤。
- 注册热配置 `gateway.routing_balance`：默认启用容量权重；关闭总开关时线路池内均匀分流，关闭容量权重时只按健康度加权。
- scorer 事实已挂到 `Candidate.Balance`，为后续 decision trace 和 Admin runtime 复用，避免后台复制算法。
- 下一步：实现共享毛利校验器、配置事务门槛和运行时负毛利硬摘除。

### 2026-07-19 - 毛利硬门槛与归档护栏

- billing 新增逐分项精确有理数毛利校验，覆盖 uncached/cache read/cache write 5m/1h/30m/output/reasoning fallback 语义，币种或计价单位不一致 fail-closed。
- router 在最终 route ratio 售价和渠道真实成本（绝对覆盖或倍率×充值）解析后执行运行时硬过滤；负毛利候选记 `routing_negative_margin`，不会进入 lifecycle。
- migration `000040_routing_margin_guard` 增加 deferred constraint triggers，在事务提交前全局检查启用线路：
  - 绝对成本路径按全部计价分项和重叠窗口比较；
  - 倍率路径比较 route ratio 与 cost multiplier × recharge factor；
  - route、route_channels、model/channel/provider 状态、绑定和五类价格表任一变化都会触发；失败约束名固定为 `ck_non_negative_route_margin`。
- Admin HTTP 将该约束稳定映射为 `admin_negative_margin` / 422，并返回不含敏感信息的 route/channel/model/component 定位。
- 渠道/服务商归档前查询受影响启用线路；会清空线路池时返回 409，归档写入不执行。当前操作要求运营先替换或停用，单请求“替换并归档”仍待补。
- 空库迁移时发现既有 `capability_keys` 依赖手工 seed 导致 DB 门禁失败；已把幂等基线字典并入 `000006`，属于空库可重建验收所需修复。
- 两次执行 `migrate drop -f && up` 均到版本 40；真实 PostgreSQL 测试验证安全毛利放行、负毛利事务以目标约束回滚；真实 Redis 测试验证两个 limiter 实例共享同一渠道并发名额。
- 下一步：补 routing decision trace、Admin runtime 聚合与指标，再完成前端。

### 2026-07-19 - Routing decision trace 与保留策略

- migration `000041_routing_decision_traces` 新增请求一对一的决策追踪表、线路时间索引和级联清理边界；trace 不保存 prompt、请求体、credential、API Key、完整 URL 或上游正文。
- gateway 六条协议执行路径记录初始排序与 fallback 后状态；fallback、容量读取退化、全部容量为零、sticky 失效、负毛利和路由失败强制保存，普通决策按 request ID FNV 哈希稳定采样。
- 注册热配置 `gateway.routing_trace`：默认普通采样 `5%`、保留 `7` 天、每小时清理、每批 `1000` 条；gateway 通过原子变量热更新采样率，worker-server 分批排空过期 trace。
- 清理查询使用 `FOR UPDATE SKIP LOCKED`，支持多 worker 实例并发；只删除 trace，不删除 request/attempt/usage/ledger 事实。
- 新增 Admin 鉴权接口：`GET /admin/v1/routes/{id}/ops/decisions`（分页）与 `GET /admin/v1/requests/{request_id}/routing-decision`（单请求详情）。
- migration 41 已应用到真实本地 PostgreSQL；DB 测试验证 upsert、线路列表、request ID 查询和过期删除，并证明 request/attempt 仍保留。
- 当前 trace 的评分与候选顺序已经持久化；“显式线路池内所有渠道的硬过滤结果/排除原因”将在 runtime 聚合阶段复用同一候选诊断结构补齐，因此 F 的 trace 总项暂不勾选。
- 下一步：实现 route runtime 聚合，并让 runtime/trace 共用完整池诊断与 scorer 输出。

### 2026-07-19 - 实时 runtime、P3 指标与归档原子替换

- 新增 `GET /admin/v1/routes/{id}/ops/runtime?model_id=&protocol=`：返回显式池大小、有效候选、无冗余、全部满载、容量退化、并发/TPM 全局使用量、breaker/错误率/延迟、health/capacity/weight、确定性权重排名、1m/5m 选择和 fallback 统计。
- runtime 的容量与健康输入并发读取：Redis channel key 是跨线路/跨 gateway 全局事实；gateway fan-out 保留实例明细，breaker 和 closed health 均 worst-wins。任一 gateway 失联/超过 10 秒或 Redis 读取失败即 `stale=true`。
- `lifecycle.ScoreBalanceCandidate` 成为调度与 Admin 共用 scorer；单候选 balanced 也计算真实分数但不改变选路。测试证明 capacity=`min(并发剩余, TPM 剩余)`、health factor、weight 与调度一致，且 runtime 不增加在途计数。
- 抽出 `core/routingdiagnostic` 稳定排除原因；trace 查询完整 `route_channels`，同时保存 `eligible/excluded_reason`，覆盖 DB 状态、凭据/URL、协议、模型绑定/价格/成本、capability 与 breaker/cooldown 摘除，不保存敏感值。
- fixed 运行时新增池计数 fail-closed：即使数据库被手工改成多渠道，也不会跨渠道 fallback。
- 新增 P3 Prometheus 指标族：balance result/candidate/pool/selected/fallback/load skew/capacity read、margin guard、trace write；配置期负毛利拒绝和 gateway 运行时摘除分别计数。结构化 routing decision 日志包含 request/route/mode/channel/pool/candidates/order/fallback reason。
- channel/provider 归档接口接受可选 `replacement_channel_id`；数据库用单条数据修改 CTE 原子加入替代渠道、归档目标、移除旧绑定，deferred 毛利/空池约束只观察最终状态。普通无 body 归档仍保留空池 409 护栏。
- 真实 PostgreSQL 测试证明 channel/provider 替换归档后线路仍非空，fixed 线路最终恰好只绑定替代渠道；service 与 HTTP 测试覆盖替代渠道合法性和请求透传。
- 后端全仓在真实 PostgreSQL/Redis 环境执行 `go test ./...` 全绿，`go vet ./...` 与 `git diff --check` 通过。
- 下一步：改造 Admin 前端线路契约、3 秒可见轮询、10 秒陈旧提示与最近决策详情。

### 2026-07-19 - Admin 线路契约与显式渠道池

- Admin 新增强类型 `RouteMode = "balanced" | "fixed"`；route、route ops 和 channel ops DTO 删除 `pool_kind`，展示字典删除 cheapest/stable/random。
- 线路表单不再提供全量动态池：所有线路始终提交 `channel_ids`；balanced 至少选择一条渠道，fixed 恰好一条，切到 fixed 时自动收敛为当前首条选择。
- 线路列表、线路详情和渠道引用线路表删除池类型列；渠道数量始终代表显式 `route_channels`，hover 只读取该线路绑定池。
- 倍率试算与毛利预览删除全量池分支，只计算当前手动绑定渠道；详情成本视图对数据库异常空池给出显式告警。
- `rg` 已确认 Admin 路由代码不存在 `pool_kind/cheapest/stable/random` 遗留。`bun run build` 未出现本模块新增错误，仍只剩基线已记录的表格泛型、模型价格字面量 state 和未使用 import。
- 下一步：接入 route runtime 与 routing decision API，实现实时运维视图。

### 2026-07-19 - Admin 实时路由与决策追踪

- Admin 新增 route runtime、route decision list 和 request decision 三组强类型 API，candidate score/fallback/source/instance DTO 与后端 JSON 契约对齐。
- 线路详情默认进入“实时路由”：必须选择可达模型，可选 OpenAI/Anthropic 协议过滤，展示完整显式池、有效候选、无冗余、全部满载和容量降级。
- 渠道运行态表展示 Redis 全局并发/TPM、剩余比例、breaker/error/latency、共享 scorer 的 capacity/health/final weight、确定性当前排名及 1m/5m 分流与 fallback。
- 数据源区展示 PostgreSQL/Redis 与每个 gateway 实例状态；页面可见时每 3 秒轮询，隐藏标签停止轮询，恢复可见后立即刷新；后端 stale 或页面观测超过 10 秒均显示陈旧告警。
- 最近决策每 3 秒刷新，抽屉展示异常信号、sticky、初始顺序、实际 fallback chain，以及包括硬排除渠道在内的完整线路池评分。
- 本模块定向 ESLint 和 `git diff --check` 通过；`bun run build` 仍只报告实施前已登记的 Admin 基线错误，没有新增 P3 TypeScript 错误。
- 下一步：实现渠道/服务商归档冲突的替代渠道选择与原子归档交互。

### 2026-07-19 - Admin 归档影响与替换操作

- channel/provider 归档 API 支持可选 `replacement_channel_id`，无替代时也显式发送 `{}`，与当前后端 JSON 解码契约一致。
- 新增共享归档确认弹窗：归档前读取所有引用线路并计算会被清空的启用线路；渠道按“是否为线路唯一成员”判断，服务商按“线路全部渠道是否属于该服务商”判断。
- 会清空线路时列出受影响线路并强制选择外部启用、具备凭据和 base URL 的替代渠道；提交后由后端继续校验 credential/provider/margin 并原子替换归档，失败时弹窗保留并显示原因。
- 不会清空启用线路时允许直接归档；成功后刷新 channel/provider/route 相关查询，恢复操作仍不自动重建线路绑定。
- 新模块定向 ESLint 与 `git diff --check` 通过，TypeScript 未增加新错误。
- 下一步：引入 Vitest/Testing Library/Playwright，补线路表单、轮询/stale、决策详情和归档替换测试。

### 2026-07-19 - Admin 测试基础设施

- 安装 Vitest 4、Testing Library、user-event、jest-dom、jsdom 与 Playwright；新增隔离的 Vitest/Playwright 配置和 `test/test:watch/test:e2e` 脚本。
- 组件测试覆盖：balanced 创建必须显式选渠道且请求体无 `pool_kind`；页面观测超过 10 秒显示 stale 并展示硬排除原因；归档会清空启用线路时必须选择并提交 `replacement_channel_id`。
- Playwright P3 规格使用 `E2E_ADMIN_TOKEN/E2E_ROUTE_ID` 连接真实本地 Admin API，验证线路详情实时工作区、runtime 响应和最近决策入口；Chromium 运行时已安装。
- `bun run test`：3 个文件、3 个测试全部通过；`playwright test --list` 正确发现 P3 规格；测试配置 TypeScript 和定向 ESLint 通过。
- 下一步：修复 Admin 既有 build/lint 基线错误，再进入空库、全仓和真实上游总验收。

### 2026-07-19 - 隔离环境最终代码门禁

- 新建独立 PostgreSQL 数据库 `unio_p3_test`，从空库执行 migration 1 -> 41；后端测试使用独立 Redis 命名空间，未再污染开发业务库。
- 最新归档 handler 修复后重新执行 `go test -count=1 ./...` 与 `go test -race -count=1 ./...`，真实 PostgreSQL/Redis 集成用例全部通过。
- `go vet ./...`、`go build ./...`、后端 `git diff --check` 全部通过。
- Admin 已清理完基线阻塞；`bun run build`、`bun run lint`、`bun run test`（3 files / 3 tests）和 `git diff --check` 全部通过。
- Playwright 真实线路用例仍留在 StarAPI 数据落库后执行，因此 Admin H 门禁暂不整体勾选。
- 下一步：重建开发业务库并写入脱敏的 StarAPI E2E 数据，执行真实协议、负载、fallback、sticky、fixed、实时观测和 CLI 验收。

### 2026-07-19 - StarAPI 落库、真实 smoke 与 balanced 分布

- 开发业务库从空库执行 migration 1 -> 41，写入 1 个 StarAPI provider、6 条渠道、25 个模型、54 个按渠道实际能力配置的模型绑定、模型基准价、渠道成本倍率/充值倍率、两条显式线路和两把隔离 E2E API Key；事务通过 deferred 毛利门禁。
- `VIP-Codex` 为 balanced + 4 条 OpenAI 渠道，售价倍率 0.2；`VIP-Claude` 为 balanced + 2 条 Anthropic 渠道，售价倍率 0.3。凭据只进入本地数据库，未写文档和代码。
- 直接上游探测发现并按事实收敛配置：四条 OpenAI 渠道的 `gpt-5.4` Responses 均成功；两条低倍率渠道当前不提供 spark。Anthropic 第一条凭据返回确定性 `GROUP_DISABLED`，因此保留在线路池但设 `credential_valid=false`；高缓存渠道的 `claude-sonnet-5` Messages 成功。
- `/v1/models` 真实验证 API Key 线路隔离：Codex Key 仅见 7 个 OpenAI 模型，Claude Key 仅见 11 个当前可用 Anthropic 模型，互不泄漏。
- Gateway 真实 smoke：OpenAI Responses 与 Anthropic Messages 均返回 200，生成 request/attempt/usage/ledger 事实并完成余额扣减；OpenAI 首次请求还因一次发送失败真实 fallback 到线路内下一渠道。
- 无会话键并发 24 次 `gpt-5.4` Responses：24/24 成功、无 fallback，四条渠道最终选择为 `7 / 5 / 6 / 6`，证明成本倍率不参与 balanced 排序且四条渠道均实际承载流量。
- 真实压测暴露普通成功 trace 写入缺陷：无异常时 nil `[]string` 被 pgx 编码为 SQL NULL，违反 `abnormal_reasons NOT NULL`；已改为非 nil 空数组并新增回归测试。定向 lifecycle + 真实 DB 测试通过，重启后真实正常请求写入 `abnormal=false`、空原因、sampled=true 和 4 条候选评分。
- 容量偏斜使用生产 Redis 并发租约格式模拟另一 gateway 占用，并把 `openai_0.035` 临时并发上限设为 1：Admin runtime 立即显示 used=1/limit=1、remaining=0、weight=0、stale=false。
- 容量占满期间并发 16 个真实请求：16/16 成功，饱和渠道 0 次命中，其余三条最终选择为 10/3/3，16 条普通成功 trace 全部持久化。测试结束后已恢复并发上限并删除临时租约。
- sticky 真实复用：同一 `prompt_cache_key` 的连续两次成功请求命中同一渠道，第二次 trace 为 `sticky_pinned=true`。
- sticky 失效测试发现 trace 会在清绑定后丢失原渠道 ID；新增请求级 immutable `ResolvedChannelID`，六条 OpenAI/Anthropic、流式/非流式路径统一使用原始绑定记录诊断。定向测试通过。
- 修复后把已粘渠道临时设为 `credential_valid=false`：下一次同会话真实请求切到另一线路内渠道，trace 正确记录原渠道、`sticky_invalid=true` 和 `{sticky_invalid}`；随后恢复渠道凭据状态。
- fixed 真实成功：`VIP-Codex` 临时收敛为 fixed + `openai_0.15`，请求返回 200 且只有 1 次 attempt，trace 显示 mode=fixed/pool=1。
- fixed 无跨渠道 fallback：把唯一绑定渠道临时设为 credential invalid 后返回 503 `model_unavailable`、0 次 attempt；三条启用但未绑定的 OpenAI 渠道没有任何 attempt。异常 trace 为 `routing_no_available_channel`。
- fixed 测试结束后已恢复 `VIP-Codex` 为 balanced + 4 条渠道，四条凭据状态均恢复有效。
- Anthropic balanced fallback 真实进入两条线路内渠道；首条使用临时连接故障，第二条调用真实高缓存上游。测试窗口中高缓存上游有间歇性发送失败，稍后的直接命中恢复 200；由于首条真实凭据分组已停用，不宣称 Anthropic 双活。
- Admin runtime API 返回 stale=false、pool=4/candidate=4 和四条渠道实时评分；最近决策列表与 request detail 均返回完整候选评分。真实 Chromium Playwright 1/1 通过。
- 跨线路容量：`VIP-Codex` 与 `VIP-Claude` 临时 fixed 到同一条 concurrency_limit=1 渠道并同时请求，一条 200、另一条在 attempt 前 429；数据库总 attempt=1，证明 channel-global Redis 租约跨线路不超卖。随后恢复两条线路原池和上限。
- Codex CLI `0.145.0-alpha.18` 使用隔离 CODEX_HOME 和临时 model provider 指向本地 gateway，`gpt-5.4` 输出 `CODEX_CLI_OK`；数据库确认 operation=responses，线路内多次 attempt 后成功。
- Claude Code `2.1.215` 通过临时 npx、`--bare` 和隔离配置目录指向本地 gateway，`claude-sonnet-5` 输出 `CLAUDE_CLI_OK`；数据库确认 operation=messages，命中高缓存渠道成功。
- 最终全量回归：后端普通/race `go test ./...`、vet、build、diff-check 全绿；Admin build、lint、3 个 component tests、diff-check 全绿，真实 Playwright 已在前一检查点通过。
- 通过 Admin 正式设置接口把普通 trace 采样从 E2E 的 100% 恢复为 5%，DB/Redis 值一致；清除 E2E sticky/concurrency/ratelimit 临时键，保留 request/attempt/usage/ledger/trace 审计事实。
- 最终运行态：`VIP-Codex=balanced/4`、`VIP-Claude=balanced/2`，渠道并发上限均恢复 20；仅 `anthropic_0.12` 因上游分组停用保持 credential invalid。

## 5. 验证记录

| 时间 | 范围 | 命令/方式 | 结果 | 备注 |
|------|------|-----------|------|------|
| 2026-07-19 | 文档 | `git diff --check` | 通过 | 设计文档格式检查 |
| 2026-07-19 | 后端基线 | `go test ./...` | 既有失败 | 仅 `platform/config`：环境中 `LOG_FORMAT=json`，测试期望默认 `console`；其余包通过 |
| 2026-07-19 | Admin 基线 | `bun run build` | 既有失败 | 表格泛型、ModelPricesDialog 字面量 state、3 处未使用 import 等 |
| 2026-07-19 | Admin 基线 | `bun run lint` | 既有失败 | 未使用 import、render 中 `Date.now()`、react-table 声明等 |
| 2026-07-19 | Schema/sqlc | `sqlc generate` | 通过 | 生成契约已删除 `pool_kind` |
| 2026-07-19 | 后端线路契约 | 定向 `go test`（admin route/ops、routing、bootstrap、lifecycle、sqlc、requestlog、ledger） | 通过 | 旧 lifecycle mode 实现尚未替换，进入下一阶段 |
| 2026-07-19 | 模型可见性 | `go test ./internal/core/modelcatalog ./internal/app/gatewayapi/... ./internal/platform/store/sqlc` | 通过 | DB 集成用例在未配置 `DATABASE_URL` 时 skip，后续真实 DB 门禁重跑 |
| 2026-07-19 | Balanced/容量 | `go test`（ratelimit、appsettings、lifecycle、三协议 service、bootstrap、middleware） | 通过 | Redis 多实例压力测试留到 H；核心排序使用确定性随机源单测 |
| 2026-07-19 | 空库迁移 | `migrate drop -f && migrate up` | 通过 | 版本 1→40；Redis 开发 DB 同步清空旧 ID |
| 2026-07-19 | 毛利/全局容量 DB | `go test ./internal/platform/store/sqlc ./internal/core/capability` + Redis 双实例测试 | 通过 | 使用真实本地 PostgreSQL 16 / Redis 7 |
| 2026-07-19 | Trace 单元/HTTP | `go test`（appsettings、lifecycle、workers、adminapi、bootstrap） | 通过 | 覆盖稳定采样、异常强制写、热配置、批量清理、分页/详情和 Admin 鉴权 |
| 2026-07-19 | Migration 41 / Trace DB | `migrate up` + `TestRoutingDecisionTraceQueryAndRetention` | 通过 | 真实 PostgreSQL 到版本 41；清理 trace 后 request/attempt 保留 |
| 2026-07-19 | Runtime/完整池诊断 | `go test`（routeruntime、gatewayruntime、lifecycle、adminapi、sqlc） | 通过 | 覆盖共享 scorer、worst-wins、stale、只读容量、排除原因和接口 DTO |
| 2026-07-19 | 原子替换归档 | service/HTTP 测试 + `TestArchive*WithReplacement` | 通过 | 真实 PostgreSQL；channel/provider 替换后线路非空，fixed 最终单渠道 |
| 2026-07-19 | 后端阶段门禁 | `LOG_FORMAT=console DATABASE_URL=... go test ./...`、`go vet ./...`、`git diff --check` | 通过 | DB/Redis 集成用例启用，非全量 Skip |
| 2026-07-19 | 最终后端代码门禁 | 独立 DB/Redis namespace 下 `go test -count=1 ./...`、`go test -race -count=1 ./...`、vet/build/diff-check | 通过 | migration 1 -> 41；包含最新归档 handler 修复 |
| 2026-07-19 | 最终 Admin 代码门禁 | `bun run build`、`bun run lint`、`bun run test`、diff-check | 通过 | 3 files / 3 component tests；真实 Playwright 待 StarAPI 线路 |
| 2026-07-19 | StarAPI 双协议 smoke | Gateway OpenAI Responses + Anthropic Messages | 通过 | 两条请求均 200；request/attempt/usage/ledger/余额事实完整 |
| 2026-07-19 | StarAPI balanced 分布 | 24 个无 sticky 并发 `gpt-5.4` 请求 | 通过 | 24/24 成功；四渠道选择 7/5/6/6；无 fallback |
| 2026-07-19 | 普通 trace 真实修复 | 真实请求 + lifecycle/sqlc 定向测试 | 通过 | nil 原因修为 SQL 空数组；正常 100% 采样 trace 成功持久化 |
| 2026-07-19 | 全局容量偏斜 | Redis 跨 gateway 租约 + 16 个真实请求 | 通过 | 满载渠道 weight=0、0 次命中；Admin runtime 非 stale；16/16 trace |
| 2026-07-19 | Sticky 复用与失效 | 同 session 三次真实 Responses + 临时 credential invalid | 通过 | 复用同渠道；硬摘除后改绑；trace 保留原绑定与 sticky_invalid |
| 2026-07-19 | Fixed/线路池边界 | fixed 真实成功 + 唯一渠道不可用 | 通过 | 成功只 1 attempt；失败 0 attempt；3 条未绑定渠道从未出现 |
| 2026-07-19 | Admin 实时工作区 | runtime/decision API + Chromium Playwright | 通过 | stale=false；真实候选/决策可见；Playwright 1/1 |
| 2026-07-19 | 跨线路全局容量 | 两线路共享 concurrency_limit=1 渠道并发请求 | 通过 | 200/429；总 attempt=1；无超卖，配置已恢复 |
| 2026-07-19 | Codex CLI | 0.145.0-alpha.18 / Responses / gpt-5.4 | 通过 | 输出 CODEX_CLI_OK；真实线路内 fallback 后成功 |
| 2026-07-19 | Claude CLI | Claude Code 2.1.215 / Messages / claude-sonnet-5 | 通过 | 输出 CLAUDE_CLI_OK；真实高缓存渠道成功 |
| 2026-07-19 | 最终总门禁与恢复 | backend normal/race/vet/build + Admin build/lint/test + runtime restore | 通过 | trace 恢复 5%；临时 Redis key=0；线路/限制恢复 |

## 6. 偏差与待决事项

当前无产品决策待确认。实现、代码门禁和真实上游 E2E 已收口。

剩余运营事实（不属于代码缺陷）：

- StarAPI 的 `anthropic_0.12` 在直接鉴权时返回 `GROUP_DISABLED`；当前保留在线路显式池但标记 credential invalid，Admin 会展示硬排除。上游恢复分组后须先主动检测成功，再恢复凭据有效状态。
- 本次 25 个模型的 `model_prices` 是本地 E2E 结算基线，用于验证倍率、毛利门禁和账务闭环；正式售卖前须由运营按实际官方/合同价核准绝对单价。倍率关系已按 route 0.2/0.3 与 channel 0.035-0.15/0.12 验证无负毛利。
