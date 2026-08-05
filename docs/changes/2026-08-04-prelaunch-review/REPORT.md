# 上线前检查报告（源码复核版）

- 检查日期：2026-08-04
- 最新复核：2026-08-05
- 复核范围：`unio-gateway`、`unio-admin`、`unio-blueprint` 当前工作树
- 状态说明：本报告按后续处置持续更新；已关闭项以当前代码、测试和 Blueprint 为准

## 一、复核结论

原报告的大多数待修问题已按当前代码、测试和迁移基线复核；本轮直接修复项已关闭，剩余代码风险集中在 C-3，发布执行风险仍是 B-3R：

- B-4 已修好，并已在一次性 PostgreSQL 16 数据库中实际跑过对应测试，应移入已关闭项。
- B-10 已关闭：Admin 主表、详情和评分提示统一显示 `probe_only` 为“仅探测”，不再展示普通排序总分等式。
- C-4、C-5、C-7、C-9、C-10、C-11、C-14 已按本轮代码和专项测试关闭。
- 请求列表已经先分页再补充详情，`COUNT(*) OVER()` 的风险明显降低，不应继续列为首要慢查询。
- 服务商级共享熔断已经写入 Blueprint，不属于文档缺口。
- C-13 已消除，且本轮完成数据库整理：历史索引、`request_attempts(provider_id, created_at)`、
  `request_records(route_id, created_at)` 和 `permit_id` 事实均已并入建表基线；独立 `000040` 已删除，
  当前 up/down 最大迁移号为 39。
- B-1R 已修复：attempt 保存 Redis permit ID。孤儿 worker 会保留 permit 仍 active 的正常长请求；permit
  已失效且客户尚未收到内容时，才在事务内重查 recovery job 和 attempt 死亡证明，收口 request/attempt 并
  释放冻结。旧 attempt 没有 permit ID 时，只自动处理尚未开始上游、也未交付首 Token 的记录。
- C-8 / B-11 已修复：recovery job 保存目标 request/attempt 终态、错误事实和独立长上下文策略；worker
  按 job 原样重放，并接受与目标终态一致的 partial 幂等重放。这批重放事实列已并回
  `000032_settlement_recovery_jobs` 建表基线；原迁移里"升级前排空 pending/running job"的守卫随之删除——
  基线建的是空表，守卫恒真通过，留着只会误导。
- A-5 已修复：登录入口使用 Redis 共享同来源和同账号两层失败窗口，超限返回 429 与 `Retry-After`；
  Redis 故障时返回 503，不会绕过限速继续登录。
- B-8 已修复：Anthropic 非流式响应必须带有完整的输入、输出用量；缺失时不会再按 0 元成功，也不会
  换另一条渠道重复请求。
- B-5 已修复：流式请求取得可靠最终 usage 后再发生尾部错误，仍按真实 usage 结算，但 request/attempt
  会按普通错误或客户端取消分别收为 failed/canceled，metrics、路由 trace 和数据库不再互相矛盾。
- B-6 已修复：启用长上下文价格时，预授权会把普通价和长上下文价都算一遍并取较高金额，不再等本地
  输入估算先越过门槛才提高冻结金额；最终结算仍按上游可靠 usage 决定实际价格档位。
- B-7 已消除：TPM 硬限制整体删除，`finish_request_admission.lua` 不再做任何 token 对账，
  "分钟桶过期导致计数偏低" 这个失效模式随之不存在。新的分钟级观测器按真实 chunk 时间记账，
  并且明确规定目标桶过期即放弃修正、绝不重建，不让同一个问题在观测侧复现。
- C-1 已消除：`ReserveIfPresent` 连同整条 TPM Reserve 链路一起删除，不再有"session 缺失就静默
  跳过"的路径。`UsageSession` 收敛为 `RequestSession`，只暴露 attempt 绑定与候选快照。
- C-2 已修复：Provider 成本的七个分项先分别保留 10 位小数，总额再由这些已舍入分项相加，
  不会再因两边分别四舍五入而违反成本快照约束。
- 线路和 Dashboard 的历史统计已经改用请求创建时保存的 Route；API Key 后续改绑不会再移动旧请求。

仍需特别注意的发布验证：

- 端到端统一采用 CLI 直测；本轮复核尚未执行真实上游与运行态故障场景，因此上线前仍需取得当次 CLI 实测结果。

## 二、已完成改造复核

| 原编号 | 当前结论 | 复核说明 |
| --- | --- | --- |
| A-1 依赖漏洞 | 已修复 | 项目语言基线已统一为 `go 1.26.5`；本轮 `govulncheck` 可达漏洞为 0，`DEVELOPMENT.md` 已同步更新。 |
| A-2 指标未采集 | 风险接受 | 代码保留指标但暂不采集，属于明确的产品决定，不是这轮代码修复。 |
| A-3 硬编码 admin token | 已修复 | 已改为用户名口令换取随机 Redis 会话，token 可过期、可吊销；旧静态 token 不再被后端接受。 |
| A-5 Admin 登录防猜测 | 已修复 | Redis 共享两层固定窗口：默认同一来源与用户名 15 分钟 5 次、同一用户名跨来源 20 次；超限返回 429 和等待时间，成功登录清除计数，Redis 故障返回 503。 |
| A-4 三表排序 400 | 已修复 | routes、channels、sync-jobs 的允许排序字段与 SQL 已对齐，Admin 当前检查通过。 |
| Admin 线路运行倍率请求放大 | 已修复 | 线路运行响应复用候选批量查询中已有的当前模型定价事实，区分绝对成本、倍率成本和未配置；Admin 首屏不再按渠道分别请求成本倍率与充值倍率。 |
| B-1 预扣搁浅泄漏 | 已修复 | stranded sweeper 已回收失败/取消请求的搁浅冻结。orphan sweeper 使用 attempt permit 区分正常长请求与重启遗留：active 保留，失效且尚未交付内容时收口；事务内重查 recovery job 和完整 attempt 证明。attempt 创建与 recovery job 创建都受同一 request 行锁保护，不能在清扫提交后迟到插入。 |
| B-2 reservation 缺失时提前返回 | 源码已修 | reservation 缺失后会继续把 running 请求收为 failed；但目前没有直接覆盖 finalizer 数据库事务的回归测试，建议补一条。 |
| B-3/C-6 旧端到端测试失败 | 发布策略已调整 | 仓库内自动套件不再作为端到端发布口径；以后统一使用 CLI 直测，见 B-3R。 |
| B-4 `breakdown_ledger_test` | 已修复 | 渠道优先级已从 1 改为 10；本轮在隔离 PostgreSQL 16 中实际执行并通过。 |
| B-5 流式尾部错误终态不一致 | 已修复 | 最终 usage 后的普通尾部错误按真实 usage 结算并把 request/attempt 收为 failed；客户端取消收为 canceled。两者都保留错误事实、交付记 interrupted、不 fallback、不绑定 Sticky，外层 outcome 和流事件与数据库一致。 |
| B-6 长上下文预授权档位不足 | 已修复 | 每个候选都用同一份 token 估算分别计算普通价和长上下文价，再取全体结果中的最高金额冻结。本地估算与真实 usage 分处门槛两侧时，不会再额外产生价格档位差额；token 数量本身估少、或余额只能部分冻结，仍按既有补扣/核销机制处理。Admin 已能汇总计费异常数量和金额，并按 `authorization_underfunded` 查看明细。 |
| B-8 Anthropic 非流式缺 usage | 已修复 | usage 不存在、为 null，或缺少输入/输出任一字段时都会失败；不再继续换渠道，不向客户扣费，并留下上游可能已经收费的风险记录。明确返回 0 输入、0 输出仍按正常响应处理。 |
| B-7 长请求 TPM 分钟桶过期 | 已消除 | TPM 硬限制整体删除：`reserve_request_tokens.lua` 与 finish 侧的 token 对账都不复存在，桶过期不再影响任何计数。TPM 改为纯观测，`obs:tpm:v1:*` 分钟桶按真实 chunk 时间记账，可靠 usage 到达后按分钟权重修正；目标桶过期或超出回溯窗口时放弃修正并计 `expired_correction`，绝不重建。 |
| C-1 `ReserveIfPresent` 静默跳过 | 已消除 | 整条 Reserve 链路（含六条协议路径的调用）删除，不存在「session 缺失就跳过」的分支。`UsageSession` 收敛为 `RequestSession`，只保留 `BindAttempt` / `SnapshotMany` / `AggregateChannelSamples`。 |
| C-2 Provider 成本舍入不一致 | 已修复 | 七个成本分项先分别按数据库的 10 位小数精度四舍五入，`total_cost_amount` 再直接汇总这些最终分项。两个分项同时在进位边缘，以及原始总额会进位但分项分别舍为零的测试均已覆盖。 |
| C-8 partial recovery 终态丢失 | 已修复 | recovery job 保存 request/attempt 目标终态和错误事实；恢复与幂等校验都保留 partial 的 `final_usage_received=false`。取消、中断、正常缺 usage 三种数据库测试通过；重放事实列已并回建表基线，不再需要升级前排空活动 job。 |
| B-11 recovery 长上下文倍率丢失 | 已修复 | recovery job 独立保存长上下文开关、门槛和输入/输出倍率，不再通过成本来源 ID 推断。绝对成本覆盖路径的恢复测试证明售价和 Provider 成本都应用倍率；旧活动 job 不做不可靠回填。 |
| 死代码清理 | 源码改造存在 | 相关读取器、查询和占位目录已删除；本机没有 `deadcode` / `staticcheck`，本轮没有重新声明“当前全仓干净”。 |
| 长上下文结算 | 已修复 | 普通结算和 recovery 重放都按同一份结算时策略及真实输入量判断阶梯价。 |
| B-10 half-open 前端展示 | 已修复 | `probe_only` 统一显示“仅探测”；主表、详情和评分提示不再把 half-open 当普通候选或展示普通总分等式。 |
| C-4 Responses 错误快照 | 已修复 | OpenAI Responses 非 2xx metadata 读取受限 `ResponseSnippet`，非流式和流式错误均有回归测试。 |
| C-5 错误分类 | 已修复 | `Code.Category()` 使用定义分类白名单，`rate_limit_exceeded` 与 `channel_rate_limited` 均归入 `ratelimit`。 |
| C-7 线路存储错误 | 已修复 | 仅 `pgx.ErrNoRows`、空 ID 或停用线路返回“线路未配置”；其他读取错误保留 `routing_store_failed`。 |
| C-9 Admin 选择器分页 | 已修复 | Provider、Model 和 Route Channel 选择器循环读取全部分页；Admin 测试覆盖第 101 条渠道。 |
| C-10 排除原因与毛利检查 | 已修复 | 稳定排除原因补齐中文映射；`not_evaluated` 不再伪造 margin 检查失败。 |
| C-11 模型缓存命中率 | 已修复 | 模型详情改为 `cache_read_tokens / input_tokens`，不再把 cache write 合并进命中率。 |
| C-14 Sticky 设置说明 | 已修复 | Gateway 注释、设置定义和 Admin 提示统一为“读取命中不续期，原绑定渠道完整成功后重新延长 TTL”。 |
| 数据库基线与索引 | 已完成 | 隔离 PostgreSQL 16 以 100 万请求/attempt 验证执行计划，并从零执行 39 个迁移；本地 `unio` 开发库已备份后重建。 |

## 三、仍待处置：高风险

### B-3R CLI 端到端发布检查尚未执行

**是否存在：存在，但属于本次发布的执行项，不是测试套件缺口。** 端到端统一通过 CLI 直接请求运行中的 Gateway；
本轮复核尚未执行公开接口到路由、Redis、数据库和账务的完整链路，也未执行真实 Redis 状态丢失、
AOF/RDB 恢复、half-open、Sticky 和长流场景。仓库保留的 adapter 级真实上游测试不能代替本次 CLI 发布直测结果。

**影响：** 当前单元测试全绿只能证明局部逻辑正常；在 CLI 直测完成前，本次发布仍缺少完整请求、账务、
Redis 恢复和真实供应商链路的当次运行证据。

**建议怎么做：** 上线前用 CLI 完成三种公开接口各一条完整请求、一次 settlement recovery、一次 Redis
状态丢失恢复、一次 Sticky/fallback 和一次真实上游冒烟，并记录本次结果。真实密钥只通过运行环境注入，
不能写入仓库；不要求恢复或新增仓库内端到端测试套件。

## 四、仍待处置：中风险

### C-3 账户级 403 只能按“渠道 + 模型”逐个暂停

**是否存在：存在。** 当前暂停键绑定具体模型。欠费、账号停用这类影响整个渠道账号的 403，需要每个模型各失败一次。

**建议怎么改：** 只有能确认是账号级错误时才增加渠道级暂停；不确定的 403 继续保持模型级，避免一次误判停掉整条渠道。

## 五、SQL 与性能

按流量增长后的优先级处理：

1. `UpsertRoutingDecisionTrace` 每个请求都会反复更新 JSONB，应观察写放大和行锁等待。
2. `FindRouteCandidates` 是每次请求的关键查询，应持续用真实数据量跑 `EXPLAIN ANALYZE`。
3. Dashboard `Radar()` / `percentile_cont` 需要对大量数据排序，时间范围越大越慢。
4. `ChannelsOpsTable` 仍在分页前聚合全部匹配 attempt，数据增长后应先缩小渠道页或预聚合指标。
5. 请求列表已经在 `filtered_page` 先分页，再只对当前页补充 usage、成本和渠道信息。`COUNT(*) OVER()` 现在只作用于基础请求表，风险已降低，先用大数据量 EXPLAIN 观察，不必优先改。
6. 两个索引已并入建表基线：`request_attempts(provider_id, created_at DESC)`、
   `request_records(route_id, created_at DESC)`。代表性 100 万行验证中，Provider 查询从约 1.38ms/105 buffers
   降至 0.10ms/8 buffers；Route 查询同步改写为保留旧请求回退语义的可索引谓词，实际计划使用 Route 索引。
7. 线路和 Dashboard 聚合已经改用 `request_records.route_id` 请求快照；只有没有快照的旧请求才回退
   `api_keys.route_id` 当前绑定。API Key 换线路后，已有快照的旧请求不会再移动到新线路。

在已有大表上补索引建议使用 `CREATE INDEX CONCURRENTLY`，避免长时间阻塞写入。多个历史 down 迁移使用 `DROP TABLE ... CASCADE`，生产回滚前必须人工确认影响范围。

## 六、Blueprint 复核

仍需修改：

- `error-semantics.md` 仍把“全池渠道并发满”写成 429；当前代码和 ADR-0016 实际返回 503，并带 `Retry-After: 1`。
- partial 60/40 已写明，但没有说明“客户售价没有 cache-read 单价时会退化为全 uncached”。
- 长上下文只写了“按真实输入合计触发”，还缺字段说明：门槛是严格大于，不是大于等于；输入合计包含 uncached、cache read 和三档 cache write；输入、输出倍率分别应用。
- partial 60/40 在 `billing-settlement.md`、`request-lifecycle.md`、ADR-0003 三处重复维护。建议功能文档保留完整口径，另外两处只做摘要和链接。
- `quality.md` 与 `roadmap.md` 仍是 draft。前者已经有实际资源护栏但未批准 SLO，后者仍是空占位，应由负责人决定是否升状态或继续明确保留为占位。

已经补齐或原报告误判的内容：

- Provider 公共故障域、两层 breaker 和多 Gateway 共享状态已经在 `resilience-circuit-breakers.md` 与 ADR-0014 中写清楚，不需要再列为缺口。
- 孤儿、搁浅、recovery 三方边界、permit 存活判断、默认参数和巡检边界已经同步到 Blueprint。
- Sticky 滑动续期在 Blueprint 中是正确的，错误在 Gateway 设置说明，见 C-14。

## 七、风险接受项

- 渠道 credential 和客户 API Key 在数据库中保留明文是现有产品决定；B-9 的列表批量暴露问题也明确决定
  本轮不处理，作为已接受风险保留。
- 预授权不足时允许 overage debit，仍不足时由平台核销，是现有账务机制。B-6 已消除长上下文价格档位切换
  造成的额外差额；输入数量本身估少或余额只能部分冻结时，这套补扣、核销和异常明细仍然保留。
- DeepSeek 无法转换的部分字段静默丢弃，属于 DEC-012 的已接受行为。
- 指标暂不采集、告警暂不投递，属于 A-2 的风险接受；这意味着上线后很多问题只能靠日志和数据库事后发现。
- 普通请求审计不保存上游错误正文是安全边界，不建议为了排错直接放开。
- C-12 的 Admin token `localStorage`、宽 CORS 与无 CSP 维持现状，本次上线明确接受风险，不再列为待处置项。

## 八、上线前运维清单

- 必配：`DATABASE_URL`（生产建议启用 TLS）、`ADMIN_USERNAME`、强 `ADMIN_PASSWORD`、`ADMIN_SESSION_TTL`、`REDIS_*`、`GATEWAY_ENV=production`、`UNIO_SKIP_DOTENV=true`。
- Admin 登录限制默认按同一来源与用户名 5 次、同一用户名跨来源 20 次、窗口 15 分钟执行；可用
  `ADMIN_LOGIN_SOURCE_FAILURE_LIMIT`、`ADMIN_LOGIN_ACCOUNT_FAILURE_LIMIT`、`ADMIN_LOGIN_FAILURE_WINDOW`
  调整。修改后需要重启 Admin。反向代理可按部署策略增加独立限速。
- 前端生产构建必须提供非 localhost、HTTPS 的 `VITE_ADMIN_API_BASE`；重新构建并部署 `dist/`，确保旧硬编码 token 产物不再对外。
- 如果旧 token 曾经提交或部署，仍应清理历史和旧制品；新后端上线后确认旧 token 已不能访问 Admin API。
- 迁移由外部工具执行，服务启动不会校验 schema 版本。当前 39 个 up/down 全部配平，最大迁移号 39；
  `request_attempts.permit_id`、两个索引均已写入建表基线。本地 `unio` 开发库已备份后从零重建。
- 探针：Gateway 有 `/healthz` 和 `/readyz`；Admin 只有 `/healthz`；Worker 没有 HTTP 探针，需要用进程存活、任务心跳或日志监控。
- Redis 不支持 Cluster；生产应使用受支持的单节点、主从或 Sentinel 方案，并开启持久化。
- 原状态丢失演练曾经通过 15/15，但当前套件已删除。重新建立可重复演练前，不应把旧结果当作当前版本的自动保证。
- `HTTP_SHUTDOWN_TIMEOUT` 默认 10 秒，可能截断长流；按可接受的发布等待时间调大。
- 每条线路至少放入两个不同 Provider 的渠道，避免一个 Provider breaker 打开后整条线路没有候选。
- 增加账务巡检：终态请求仍有 authorized 冻结、余额 reserved 与冻结合计不一致、dead recovery job、超过阈值仍 running 的请求都应告警。

## 九、本轮验证快照

| 验证项 | 当前结果 |
| --- | --- |
| Gateway `go test -count=1 ./...` | 通过；本轮连接一次性 PostgreSQL 16 与隔离 Redis，数据库和 Redis 用例未跳过 |
| Gateway `go vet ./...` | 通过 |
| Gateway `go build ./cmd/...` | 通过 |
| B-4 专项数据库测试 | 一次性 PostgreSQL 16 全迁移后通过；临时容器已删除 |
| B-1R 孤儿清扫专项数据库测试 | 一次性 PostgreSQL 16 全迁移后通过：active permit 长流保护、失效 permit 收口、旧记录安全回收、Redis 读取失败保守停止、recovery 重查、迟到 attempt/recovery 插入拒绝、重复执行幂等 |
| C-8 / B-11 recovery 专项数据库测试 | 一次性 PostgreSQL 16 全迁移后通过：取消、中断、正常缺 usage、绝对成本覆盖长上下文恢复均通过。重放事实列并回建表基线后，原「旧活动 job 迁移拒绝」路径已不存在 |
| A-5 Admin 登录限速测试 | 通过：同来源上限、跨来源上限、多实例共享、成功清除、窗口过期、429 等待时间、Redis 故障 503 |
| B-8 Anthropic 非流式 usage 测试 | 通过：usage 缺失、null、缺输入、缺输出均失败；明确 0/0 合法；失败后不换渠道、不结算并记录资金风险 |
| B-5 流式尾部错误终态测试 | 通过：最终 usage 后普通错误按真实 usage 结算并收为 failed；客户端取消收为 canceled；outcome 与流事件同步，均不 fallback |
| B-6 长上下文预授权测试 | 通过：门槛以下估算仍覆盖长上下文价格；倍率低于普通价格时仍取较高的普通价格，不会减少冻结 |
| B-7 / C-1 TPM 观测改造 | 通过：一次性 PostgreSQL 16（改后历史迁移全量重建至 39）与隔离 Redis 上跑完整套件。观测专项覆盖跨分钟权重分配与整数余数、批次 operation id 幂等、队列溢出丢弃、usage 缺失只计 missing、过期桶放弃修正且不重建、字段夹 0、跨进程重放不二次修正、观测写入不置位基础设施故障 latch。临时容器已删除 |
| C-2 Provider 成本舍入测试 | 通过：两组进位边缘单测通过；一次性 PostgreSQL 16 全迁移后，成本快照以两个 `0.0000000001` 分项成功保存为 `0.0000000002` 总额 |
| 线路历史归因修复 | 通过：一次性 PostgreSQL 16 全迁移后模拟 API Key 从线路 A 改绑到线路 B；Dashboard、线路概览、趋势、模型和请求列表仍把旧请求保留在线路 A。临时容器已删除 |
| `govulncheck ./...` | 可达漏洞 0；依赖模块中另有 4 条不可达漏洞提示 |
| Admin `typecheck` / `eslint` / `vitest` | 通过；16 个测试文件、59 个测试用例；覆盖 probe_only、排除原因、全分页和首屏无逐渠道倍率请求 |
| Blueprint `make validate` | 通过；140 个 Markdown 文件、41 个目录 |
| CLI 端到端 / 运行态故障演练 / 真实上游 | 本轮未执行；adapter 级 OpenAI/DeepSeek 真实上游测试仍在，但不能代替本次 CLI 发布直测。旧执行结果不作为本次发布证据 |
| 索引执行计划与迁移基线 | 通过：隔离 PostgreSQL 16 装载 100 万 request/attempt；Provider 索引使用 Index Only Scan，Route 改写谓词使用 Route 索引，旧/新 Route 归因逐线路计数差异为 0；空库 39 个迁移成功，`permit_id` 约束和唯一索引存在。 |
| `deadcode` / `staticcheck` | 本机未安装，本轮未重新执行 |

结论：当前代码可以正常构建，Gateway 全量测试/vet、Admin 检查/构建和文档校验均通过；本轮 B-10、C-4、C-5、
C-7、C-9、C-10、C-11、C-14 与数据库基线整理已关闭。B-9 已转为风险接受；剩余代码风险是 C-3，
剩余发布执行风险是 B-3R（本次 CLI 端到端发布检查尚未执行），建议两项分别处理后再做上线确认。

注意：本轮改造直接修改了 `000002` / `000004` 历史迁移并删除了 `routes.tpm_limit` 与 `api_keys` 的三列
废弃限额。任何已有开发库都必须 drop 重建——`app_settings` 里旧的 `{"rpm":0,"tpm":0,"rpd":0}` 不会被
seed 覆盖，而新的解码器拒绝未知字段，不重建会让 Gateway 与 Admin 在 runtime-control reconciliation
阶段直接启动失败。
