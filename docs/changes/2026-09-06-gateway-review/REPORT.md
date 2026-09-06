# Gateway 全维度只读审查报告

- 审查日期：2026-09-06
- 审查对象：`unio-gateway` HEAD `966d305`（工作树干净，除未跟踪 `.DS_Store`）
- 审查范围：安全与鉴权（A）、账务与结算（B）、并发与可靠性（C）、性能与 SQL（D）、架构分层与代码质量（E）
- 审查方式：只读。全仓静态验证 + 定点深读；数据库/Redis 套件在一次性 `unio-review-*` 容器（PostgreSQL 16 / Redis 7）上执行，未触碰 `unio-dev` 数据，结束后容器已删除
- 规模基线：5 个二进制；非测试 Go 约 11.3 万行（另 sqlc 生成物约 3.9 万行）；测试 Go 约 7.8 万行；82 对迁移；52 个 Redis Lua 脚本；149 个 internal 包
- 编号约定：每条标注「已证实 / 部分成立 / 不成立」；不成立的排查线索也保留，避免下次重查

## 一、审查结论

代码整体质量高于常见同规模项目：资金链路（预授权 → 结算 → ledger → recovery）幂等与守恒设计严谨，重放逐事实比对；Console 认证栈（Argon2id、HttpOnly Cookie、Origin 白名单 CORS、可信代理 XFF、HMAC 验证码）没有发现实质缺陷；Redis 运行态 fail-closed、错误脱敏契约（构造处脱敏 + 测试覆盖）执行到位。构建、vet、全量测试（含一次性 PG16/Redis 上的 DB/Redis 用例）全部通过。

需要处理的问题集中在三处：

1. **1 个高风险**：`golang.org/x/image` v0.43.0 的 VP8L 解码漏洞（GO-2026-6222）在客户可控的 token 估算热路径上可达，另有 7 个 stdlib 可达漏洞已在 Go 1.26.6 修复（当前钉 1.26.5）。升级依赖与工具链即可关闭。
2. **2 个中风险**：Admin 登录限速按 `RemoteAddr` 分桶，反代之后退化为「任何人 5 次失败可锁定管理员登录」；路由 trace 每请求两次全量 JSONB 落库，是 08-04 报告首要观察项的加倍版。
3. **门禁红灯**：HEAD 上 `gofmt` 有 19 个文件漂移、`make check-lua` 失败（luacheck 5 警告 + stylua 9 文件），仓库自己的质量门禁当前不通过。

统计：高风险 1 项（A-1）、中风险 2 项（A-2、D-1）、中低风险 6 项（B-1、C-1、D-2、D-3、E-1、E-6）、低风险 16 项，另有信息级与「排查后不成立」线索 8 组（保留在各节，避免下次重查）。

## 二、A 安全与鉴权

### A-1 可达依赖漏洞：x/image VP8L 解码 + Go 1.26.5 stdlib（高）

**是否存在：已证实。** `govulncheck` 报告 8 个可达漏洞。其中 GO-2026-6222（`golang.org/x/image@v0.43.0` VP8L 解码过量内存分配，修复于 v0.45.0）的调用链是 `tokenest.decodeBase64Dims → image.DecodeConfig → vp8l.Decode`：

- WebP 解码器显式接线：`internal/core/tokenest/media.go:17`（blank import `golang.org/x/image/webp`）；
- 客户内联 base64 图片总是本地解码：`media.go:87-88`，开关 `TOKEN_ESTIMATE_COUNT_MEDIA` 默认 true（`internal/platform/config/config.go:743`）；
- 该路径在预授权 token 估算上，任何认证客户经 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 均可触达；Test nginx 允许 256MB 请求体。

其余 7 个为 stdlib（net/http、crypto/tls、html/template、encoding/xml、encoding/asn1、net/url、x/net idna），全部修复于 go1.26.6。

**影响：** 构造的 lossless WebP 可造成 Gateway 内存暴涨（OOM DoS），发生在计费前，不消耗攻击者余额。

**建议怎么做：** `go get golang.org/x/image@latest`（≥v0.45.0）+ 工具链升至 go1.26.6，重跑 `govulncheck` 确认清零。可另评估对内联图片 base64 长度设独立上限（当前仅受 JSON body 总上限约束）。

### A-2 Admin 登录限速按 RemoteAddr 分桶，反代后退化（中）

**是否存在：已证实。** `internal/app/adminapi/login.go:65,87` 把 `r.RemoteAddr` 直接传给限速器；`internal/platform/adminlogin/limiter.go:126-141` 以 `hash(账号+来源IP)` 与 `hash(账号)` 双桶计数。部署上 Admin 位于 nginx（`deploy/nginx/test.conf:129-139`，仅设置 `X-Real-IP`/`X-Forwarded-For` 头）与 Caddy 之后，Go 进程看到的 `RemoteAddr` 恒为代理地址。

**影响：** 「同一来源+账号 5 次/15 分钟」退化为「全网合计 5 次/15 分钟」：任何人对 `admin` 用户名发 5 次错误口令即可把真实管理员锁在门外（拒绝服务）；同时来源维度对真实攻击者失去区分度。

**建议怎么做：** Admin 侧引入可信代理感知的来源 IP 解析后再喂给限速器——`internal/app/consoleapi/middleware/client_ip.go` 已有按可信 CIDR 解析 XFF 的实现可直接复用；不可信来源回落 `RemoteAddr`。

### A-3 Admin 可配置外连目标不限制内网地址（低，设计权衡）

**是否存在：部分成立。** `internal/service/admin/provider/provider.go:366-394`（`NormalizeOrigin`）只校验 scheme/userinfo/query/fragment，允许 loopback、RFC1918、link-local host；`internal/service/admin/proxy/proxy.go:230-249` 对代理 host 只查非空。渠道测试与模型发现会由服务端主动外连这些地址。

**影响：** 构成 Admin 权限下的 SSRF 面（可探测宿主内网端口、读取内部 HTTP 服务响应片段——渠道测试日志会保留上游片段）。但本产品的职责就是连接管理员配置的任意上游，且当前是单管理员全信任模型，实际增量风险低。

**建议怎么做：** 不必阻断；在 Blueprint 中把「origin/代理允许内网地址」记为已知边界。若未来出现低权限运营角色，再增加私网段拦截或显式白名单开关。

### A-4 /metrics 无鉴权常驻，两侧监听器均挂载（低）

**是否存在：已证实。** `metrics.New()` 无条件创建（`internal/bootstrap/gateway_server.go:120`、`admin_http.go:181-183`），`/metrics` 总是挂载（`gatewayapi/router.go:67-69`、`adminapi/router.go:155-157`）。当前 Test 拓扑安全：nginx 对 API 域显式 404 `/metrics` 与 `/internal/*`（`test.conf:61-71`），Admin 域只代理 `/v1/`。

**影响：** 防护完全依赖反代配置；容器端口一旦直接暴露（或未来新增部署形态漏配），运营指标（模型/渠道流量、延迟、错误分布）即对外可读。

**建议怎么做：** 低成本纵深：`/metrics` 复用 `GATEWAY_INTERNAL_TOKEN` 的 Bearer 校验，或监听独立内网端口；至少在部署文档把「反代必须屏蔽 /metrics」列为硬性检查项。

### A-5 每次认证同步写 last_used_at，且写失败即拒绝请求（低，已知缺口提级）

**是否存在：已证实，且为自知缺口 [GAP-3-001]。** `internal/core/auth/apikey.go:128-140`：认证热路径同步执行 `UpdateAPIKeyLastUsedAt`，任何写失败直接返回 `auth_store_failed`（请求 500）。

**影响：** 每请求一次额外 DB 写（写放大与热点行）；且一个纯观测字段的写失败会挡掉本可正常服务的请求——数据库只读降级场景下全网关拒绝服务。

**建议怎么做：** 节流（如同 Key 每 N 秒最多一次）或异步批量更新；无论如何，写失败应降级放行而非 500。

### A-6 28MB `seed` 二进制被 git 跟踪（低）

**是否存在：已证实。** 仓库根 `seed`（arm64 Mach-O，28.4MB）在 git 索引中。

**影响：** 仓库膨胀（每次改动叠加历史）、无法 code review 的供应链盲点。

**建议怎么做：** 从 git 移除（保留本地文件或改为源码+构建脚本产出、或 Git LFS）；`.gitignore` 增加该产物。

### A-7 核实为不成立/维持现状的线索

- **XFF 伪造影响安全决策：不成立。** `httpx.ExtractClientIP` 明确标注仅展示/审计（`client_ip.go:30`），消费方只有请求记录 `client_ip` 列与访问日志（`request_lifecycle.go:439`、`gateway_logger.go:63`）；限流按用户维度、登录限速按 RemoteAddr，均不消费 XFF。
- **SMTP 口令 GET 回显：成立但为声明的产品决策。** `internal/app/adminapi/system/email_smtp.go:17` 注释明确「与渠道凭据同权」；归入第四节风险接受项复核。
- **固定验证码后门：不成立。** `fixedCode` 仅测试注入；生产装配传空串（`internal/bootstrap/console_server.go:104`）。
- **正面清单：** Console 密码 Argon2id（64MiB/t=3/p=2）、验证码 crypto/rand 6 位 + 5 次尝试 + 10 分钟 TTL、刷新令牌路径限定 `/v1/auth/sessions`、CORS 白名单外 Origin 直接 403（兼作 CSRF 防线）、Admin 会话为随机 32 字节 token 且 Redis 只存 SHA-256、`/internal/v1/logging/status` 常数时间比较、API Key 248-bit 熵 + 无盐 SHA-256（高熵下可接受）、日志与错误响应未发现记录请求正文/密钥。

## 三、B 账务与结算

### B-1 账号终身统计两处口径不一致（中低）

**是否存在：已证实。** 运行时增量按**应收售卖额** `charge.Amount` 累加（`internal/service/gateway/lifecycle/settlement.go:1276-1284`），即使实际只 capture 到部分（余额不足进入 write-off 核销）也按全额记；而迁移回填按 **ledger 净实扣**（debit + adjustment_debit − refund − adjustment_credit）聚合（`migrations/000077_subscription_account_stats.up.sql` `sale_agg`），列注释写的也是「客户实扣净额」。核销与事后退款场景下两口径分叉，同一列混两种定义。

**影响：** 账号「用量」列在核销/退款账号上失真；未来做「单号摊销成本 vs 售卖额」经营分析时会得出不一致结论。

**建议怎么做：** 二选一并对齐注释：统一为实收（增量改用 `reservation.CapturedAmount + OverageCapturedAmount`，与回填一致），或统一为应收（回填改按 price snapshot 重算）。倾向前者——ledger 是事实源。

### B-2 号池「成本恒 0」无代码守卫（低）

**是否存在：已证实。** 池型渠道校验只覆盖「不持凭据、adapter 必须 codex、账号并发限池型」（`internal/service/admin/channel/channel.go:461-489`），成本倍率与渠道供给形态之间无任何约束；结算只是「金额为 0 就不写 provider 流水」（`settlement.go:1139`）。运营给池型渠道配了非零成本倍率时，会持续产生无意义的 provider 成本流水与余额扣减。

**建议怎么做：** 池型渠道创建/更新时校验（或自动写入）成本倍率 0；或在结算侧对 `supply_form=pool` 且成本非零时告警。

### B-3 三份 half-up 舍入实现并存（低，维护性）

**是否存在：已证实。** `internal/core/billing/numeric.go:74-110` 与 `internal/core/fx/fx.go:146-160` 是两份相同的 big.Rat half-up（均注明只服务非负值）；`internal/core/providerledger/providerledger.go:522-526` 则经 `v.FloatString(scale)` 字符串回转再 `Scan`，且忽略 Scan 错误、scale 类型为 `int`（其余为 `int32`）。数值上当前等价（FloatString 四舍五入、非负域一致），但三处演化极易漂移。

**建议怎么做：** 收敛到一个共享 helper（放 `core/fx` 或独立 money 包），providerledger 去掉字符串回转。

### B-4 capture 无币种断言（信息，核实为按构造安全）

**是否存在：机制成立，不可利用。** `ledger.CaptureParams` 无 currency 字段，金额直接按 `reservation.Currency` 入账（`internal/core/ledger/reservation.go:248-368`）。安全性由构造保证：售价快照在路由规划期一次解析、同请求全候选共享、结算不重读价格（`settlement.go:148-151` 注释与实现一致）；冻结币种取自同一份候选价（`authorization.go:175-182`）；幂等重放按已存 price snapshot 重算比对（`settlement.go:1889-1914`）。

**建议怎么做：** 纵深防御可在 capture 入口加一条 currency 断言（reservation.Currency vs charge.Currency），一行代价换掉未来重构翻车的隐患。非必须。

### B-5 充值汇率缺失回退 1.0（信息，有意兜底）

**是否存在：部分成立，设计如此。** `settlement.go:499-521`：`FindActiveProviderRechargeRate` 无行时按 1.0 记账并 Warn，`rate id` 置 NULL 作为显式兜底标记；注释说明路由候选与渠道启用已拦截无汇率服务商，此处只覆盖配置变更竞态。**建议**：可加一个计数指标，避免只靠日志发现长期兜底。

### B-6 排查后不成立的线索

- **`settlement_recovery.go:117` 忽略 MarkSucceeded 错误：** 有意——job 标记失败不应让已成功的用户请求失败，pending job 由 worker 幂等重放收口（注释明确、`FOR UPDATE SKIP LOCKED` + 逐事实幂等校验兜底）。
- **partial 60/40 用 float64 拆分（`partial_stream.go:94`）：** token 数量级下 float64 精度充分，结果夹逼在 `[0, inputTokens]`，且该比例本身是声明的临时口径（文件内 TODO 已注明后续方案）。
- **正面清单：** CDKey 兑换（行锁 + 决定性幂等键 + 单事务 + 同用户重放返回原结果，`console/cdkey/service.go:119-196`）；FX 钉汇率 fail-closed（非 USD 无汇率宁可让结算失败进 recovery，`settlement.go:620-657`）；水位暂停边界处理（缺窗口视为不限、标称已重置却报高水位时不锁号，`subscription/health/health.go:200-225`）；费用上限计数含超额补扣（`settlement.go:1198-1223`）。

## 四、C 并发与可靠性

### C-1 worker 单循环顺序执行，账务补偿排在慢探测之后（中低）

**是否存在：已证实。** `internal/app/workers/runner.go:46-101` 顺序遍历全部 worker；同一循环里既有 settlement recovery（账务一致性关键路径）也有渠道检测、模型发现这类外呼 HTTP 的慢单元。一个 60 秒的探测批次会让 recovery claim 延后 60 秒。

**建议怎么做：** 把账务类（settlement recovery、sweepers）与探测类拆成两个独立 Runner（各自 goroutine），或给 Runner 增加按单元的并发调度。

### C-2 多实例 worker 无护栏（低）

**是否存在：已证实。** `VerifySingleNodeDeployment` 只拒绝 Redis Cluster（`breakerstore/store.go:166-180`），不能阻止多个 worker-server 实例并行。有锁的只有 settlement recovery（`FOR UPDATE SKIP LOCKED`）、permission recheck（Redis claim）、token refresh（Redis 锁）；orphan/stranded sweeper、fx 同步、模型目录同步、渠道检测、发现验证、工单维护均无分布式锁，双实例会重复探测（重复消耗上游额度）与重复扫描。

**建议怎么做：** 在部署文档明确「worker-server 单实例」，或给无锁 worker 统一加 Redis 租约锁。

### C-3 Shutdown 超时直接 os.Exit(1)，跳过 app.Shutdown（低）

**是否存在：已证实。** `cmd/gateway-server/main.go:150-155`：`server.Shutdown(ctx)` 超时（默认 `HTTP_SHUTDOWN_TIMEOUT` 10 秒，长 SSE 流易触发）报错即 `os.Exit(1)`，不再执行 `app.Shutdown`（TPM 观测器最后一个周期的内存数据、未导出 trace 丢失）；`internal/bootstrap/gateway_server.go:67-77` 的 Shutdown 也只等 TPM 观测器，不等 settings applier 与 runtime-control reconciler 退出。结算正确性不受影响（recovery job 兜底）。

**建议怎么做：** HTTP Shutdown 失败后仍以短限时执行一次 `app.Shutdown`；发布前按需调大 `HTTP_SHUTDOWN_TIMEOUT`（08-04 运维清单已有此项）。

### C-4 finish.lua 计算 interaction_evidence 但从未使用（低）

**是否存在：已证实。** `internal/platform/breakerstore/lua/ops/finish.lua:30-33` 由三个 ARGV（`request_write_state`/`response_headers_received`/`first_token_eligible`，Go 侧 ARGV 21-23 传入）计算 `interaction_evidence`，之后无任何引用；同文件 `apply_scope` 的 `is_channel` 参数未用；`observe_channels.lua` 有未使用函数 `parse_circuit_breaker_payload`。三处正是 luacheck 报的警告，也是 `make check-lua` 失败的一部分。看起来是「交互证据参与熔断裁决」特性接了输入没接逻辑，或重构残留。

**建议怎么做：** 确认设计意图：要么把 evidence 接进 outcome 裁决，要么删除计算与 Go 侧三个 ARGV；同步修掉 luacheck 告警让门禁回绿。

### C-5 Sticky 热路径用裸 Eval（低）

**是否存在：已证实，影响有限。** `internal/platform/stickysession/store.go:254,279` 用 `client.Eval` 每次发送脚本全文；仓库其余 52 个脚本统一走 `redis.NewScript`（EVALSHA + fallback）。Redis 服务端会缓存编译结果，实际开销是每调用多传几百字节脚本文本。

**建议怎么做：** 换成包级 `redis.NewScript` 常量，与全仓一致。

### C-6 proxyclient 缓存与直连回退（低）

**是否存在：部分成立。** `internal/platform/proxyclient/proxyclient.go:47-75`：按代理 URL 缓存 `http.Client` 永不淘汰——实际有界（键空间 = 曾配置过的代理数），仅备注即可。更值得注意的是解析失败静默回退直连：号池场景「同号同出口」是风控约束（本包注释自己强调），直连恰恰破坏它。入口校验（协议白名单/host 非空/端口范围，`admin/proxy/proxy.go:230-249`）+ 服务端拼 canonical URL 使该分支几乎不可达，但一旦可达就是坏降级。

**建议怎么做：** 该分支改为返回错误（fail-closed）而不是直连；顺带在代理删除/禁用时清理缓存项。

### C-7 核实为设计如此的线索

- **`launchStreamAudit` 信号量满静默丢弃（`attempt_runner_stream.go:118-139`）：** 有意——只丢实时观测写，终态时间与首字事实由收口/结算路径同步持久化；可选加一个丢弃计数指标。
- **正面清单：** Redis 启动 fail-closed（Cluster 拒绝、不可达拒绝就绪，`gateway_server.go:123-152`）；attempt permit renew 循环生命周期完整（stop channel + done + first-terminal-wins + `abortAttemptPermitOnExit` 补漏，`attempt_permit.go:687-728`）；SSE keepalive goroutine ctx 绑定；`ReadTimeout/WriteTimeout=0` 为已声明设计，由 ReadHeaderTimeout/IdleTimeout/httpx 每写 deadline/nginx 超时四层兜底（`cmd/gateway-server/main.go:169-184`）。

## 五、D 性能与 SQL

### D-1 路由 trace 每请求两次全量 JSONB upsert（中）

**是否存在：已证实。** `internal/service/gateway/lifecycle/routing_trace.go:214-220`：`Record`（规划期 partial）与 `complete`（收口）各执行一次 `UpsertRoutingDecisionTrace`，每次整体替换 `trace_payload` JSONB（`sql/queries/gateway/routing_traces.sql:1-57`）。这是 08-04 §5 第 1 条「应观察写放大」的加倍版：表随流量线性膨胀且每行写两次、JSONB 全量重写造成 TOAST 与 WAL 放大。

**建议怎么做：** 短期用真实流量观测该表写入占比；中期改为只在 complete 落一次库（partial 状态只在异常收口路径需要），或对成功请求采样落 trace、失败/异常全量。

### D-2 Console 时序查询无桶数上限（中低，新增面）

**是否存在：已证实。** `internal/app/consoleapi/requests/query.go:147-157` 的 `from/to` 只校验 RFC3339 格式，`bucket` 允许 `minute`（`query.go:118-122`）；`sql/queries/console/usage.sql:165-205` 用 `generate_series` 为展示窗补全空桶。认证客户请求 `bucket=minute&from=2020-01-01&to=2026-01-01` 会生成约 315 万个桶行，先打 PostgreSQL 再打 console-server 内存。usage 页时序接口同理。

**建议怎么做：** 服务层按「桶数 = 窗长 ÷ 桶宽」设上限（如 ≤ 1000，超限 422），或按 bucket 粒度限制最大窗长（minute ≤ 2 天、hour ≤ 60 天）。

### D-3 大表索引迁移未用 CONCURRENTLY，且有迁移期全表回填（中低）

**是否存在：已证实。** `000071`（request_records）、`000072`（routing_decision_traces）、`000081`（request_attempts）均为普通 `CREATE INDEX`，锁写直到建完；`000077` 在迁移期对 request_records + usage_records + ledger_entries 做一次全量聚合回填。当前发布流程 migrate 容器先行、旧 Gateway 容器仍在服务，建索引期间写请求会被阻塞。08-04 §5 已建议大表补索引用 `CREATE INDEX CONCURRENTLY`，新迁移未沿用。

**建议怎么做：** 为迁移工具确认 no-transaction 单语句能力后，把「已存在大表上的新索引一律 CONCURRENTLY」写成迁移规范；回填类迁移标注预期时长与锁面。

### D-4 ChannelsOpsTable 仍在分页前全量聚合（低，08-04 遗留未变）

**是否存在：已证实。** `sql/queries/admin/channel.sql:695-811`：对全部匹配渠道 JOIN 时间窗内全部 attempt，逐渠道算 4 个 `percentile_cont` 再排序取页。渠道数少时可接受，attempt 增长后线性变慢。08-04 §5 第 4 条原样保留。

**建议怎么做：** 维持 08-04 建议：先缩小渠道页再补指标，或预聚合。

### D-5 核实与观察项

- **`COUNT(*) OVER()`：** admin/console 请求列表均已是「窄 CTE 先过滤分页、当前页再富化」模式（`admin/requests.sql:9-16`、`console/requests.sql:7-11`），维持 08-04「风险已降低」结论；`console/wallet.sql:14` 与 `admin/cdkeys.sql:41,205` 为单表窗口计数，当前规模可接受。
- **`percentile_cont`** 仍在 5 个 admin/console 查询中（overview/channel/model/provider/console requests），维持「数据量增长后观察」定级。
- **`FindModelCandidates`**（原 FindRouteCandidates）仍是每请求关键查询，号池改造新增账号存在性子查询；08-04「持续用真实数据量 EXPLAIN ANALYZE」建议保留。
- **正面：** `console/usage.sql:5-8` 注释明确记录了 LATERAL vs CTE 的执行计划退化教训并固化写法；账号维度新索引均带 `WHERE ... IS NOT NULL` 部分索引条件。

## 六、E 架构分层与代码质量

### E-1 service 层反向依赖 HTTP 层 DTO（中低）

**是否存在：已证实。** 三个协议编排包共 26 处 import HTTP 层：`internal/service/gateway/openai/responses` → `internal/app/gatewayapi/openai/responses`（15 文件）、`.../openai/chatcompletions`（8 文件）、`.../anthropic/messages`（5 文件）。请求 DTO（如 `ResponsesRequest`）定义在 app 层却是 service 的输入契约，与 AGENTS.md「HTTP 层处理协议与 DTO；service 层负责编排」的方向相反（Go 禁止循环导致 handler 反而不import自己的 service，由 bootstrap 装配）。

**建议怎么做：** 把协议 DTO 下沉到 service 层或独立 contract 包（如 `internal/service/gateway/openai/responses/dto`），app 层只做解码与错误渲染。改动面大，可与下次协议改造顺路做。

### E-2 internal/app/workers 直连 sqlc（低）

**是否存在：已证实。** 7 个 worker 文件直接 import `internal/platform/store/sqlc`（`fx_rate_sync_worker.go:16`、`fx_margin_recheck_worker.go:11`、`fx_reconciliation_worker.go:12`、`channel_test_worker.go:10`、`stranded_reservation_sweeper_worker.go:11`、`orphan_reservation_sweeper_worker.go:11`、`settlement_recovery_worker.go:13`），跳过 service 层。app 层其余包均无此问题。

**建议怎么做：** 接受现状（workers 本质是调度壳）则在 AGENTS.md 给 workers 开明确豁免；否则把查询下沉到对应 service。

### E-3 gofmt 漂移 19 个文件（低，门禁）

**是否存在：已证实。** `gofmt -l` 报 19 个非生成文件，包括 `settlement.go`、`router.go`、`attempt_runner*.go` 等核心文件；抽样 diff 显示是 `sale_discount` 重命名（e7d6319）后字段对齐未重排。违反 AGENTS.md「Go 代码运行 gofmt」。

**建议怎么做：** 一次 `gofmt -w` 收口；建议提交前钩子或 CI 加 `gofmt -l` 检查。

### E-4 make check-lua 在 HEAD 失败（低，门禁）

**是否存在：已证实。** luacheck 5 警告（2 个行超长：`gate_and_acquire.lua`、`snapshot_many.lua` 装配后行；3 个未使用符号即 C-4 所列）+ `stylua --check` 9 个文件未格式化。仓库自己的 `make check-lua` 当前红灯。

**建议怎么做：** `stylua` 格式化 + 处理 C-4 未使用符号 + 拆长行；与 E-3 一并把门禁修绿。

### E-5 staticcheck 27 项（低）

**是否存在：已证实。** 全仓 `staticcheck` 报 27 项：约 15 个未使用函数/字段/类型（生产代码含 `service/admin/cdkey/service.go:876`、`service/admin/modelprice/modelprice.go:1022`、`service/admin/channelprice/channelprice.go:405`、`core/requestlog/store.go:581`、`service/console/requests/service.go:567` 等）；1 个 API 形状问题 `service/admin/cdkey/service.go:718`（ST1008，error 不是最后一个返回值）；1 个 ST1005 大写错误串；4 个 S1016 结构体转换建议。无正确性问题，但未使用代码集中在无测试的钱相关包（cdkey/modelprice），属于该清理的死代码。

**建议怎么做：** 逐条清理；把 staticcheck 纳入本地/CI 常规检查（本机此前未安装）。

### E-6 39/149 包零测试，盲区集中在钱与鉴权的 Admin/Console 面（中低）

**是否存在：已证实。** 全仓 110 包有测试、39 包没有。按非测试行数排序的最大盲区：`service/admin/subscriptionaccount`（1249 行，账号池管理）、`service/admin/cdkey`（953，发卡）、`service/admin/modelcatalog`（714）、`app/adminapi/user`（707）、`service/console/ticket`（629）、`app/consoleapi/auth`（609，登录 HTTP 层）、`app/adminapi/overview`（607）、`app/adminapi/cdkey`（597）。core 层覆盖很好（32/35），盲区几乎全在 admin/console 的 HTTP 与 service 层。

**建议怎么做：** 优先给 `service/admin/cdkey`（生成/吊销资金凭证）与 `service/admin/subscriptionaccount`（账号导入/重授权/删除）补服务层测试；`app/consoleapi/auth` 的 handler 测试可参照同层 `consoleapi/apikeys` 现有风格。

### E-7 体量热点（信息）

`service/gateway/lifecycle` 包 13,421 行/38 文件；最大文件 `settlement.go` 2,561 行、`admin/channel/channel.go` 1,410、`admin/modelprice/modelprice.go` 1,401、`admin/subscriptionaccount/subscriptionaccount.go` 1,249、`attempt_runner_stream.go` 1,235、`attempt_permit.go` 1,179、`platform/config/config.go` 1,119。未见混乱，但 settlement.go 已同时承载解析/校验/落库/幂等/风险登记，继续增长会难以审读。重构时机自定。

### E-8 核实合规项与其余信息

- **上游错误透传边界：合规。** 客户面只输出 sanitize 后的 `ErrorCode/ErrorMessage`（唯一写点 `core/adapter/openai/responses/errors.go:101-102`，URL 打码/空白折叠/300 字截断，附测试）；契约在 `core/adapter/upstream_error.go:70-77` 写明「写入时必须已脱敏」。`ResponseSnippet`（上游原文截断）只进 Admin 渠道测试日志（`channel_testing.go` 的 `upstream_error` 字段），gateway 请求记录不消费——Admin 面留原文属排障设计，边界清晰。
- **TODO 盘点：** 全仓 8 个 TODO 均为 `[GAP-*]` 生产就绪缺口（last_used_at 写放大见 A-5、API Key 审计日志 GAP-3-002、`platform/store/postgres.go:52` 迁移版本校验、partial 固定比例临时口径等），无 FIXME/HACK。
- **依赖与生成物健康：** `go mod tidy -diff`/`go mod verify` 干净；`sqlc diff` 无漂移（sqlc v1.31.1 与生成物版本戳一致）；导入图上无孤儿包（唯一「无人 import」的 `app/gatewayapi/openai` 是有意的 doc-only 包）；`0000{71,72,74,79,80,81,82}` 等近期迁移 up/down 全部配平（82 对）。
- **console requests 汇总卡片（进行中提交 966d305）：** 构建与单测通过；除 D-2 桶数上限外，另见 `service.go:567` 未使用 `int8Ptr`（staticcheck）、`query.go:87-89` 重复的 `if err != nil` 拷贝残留、汇总主窗口非法（to<from）静默跳过 Series 而 compare 内上一周期非法却返回 422 的不一致——收尾时一并处理即可。

## 七、08-04 报告遗留与风险接受项复核

| 项 | 当前结论 |
| --- | --- |
| B-3R CLI 端到端发布检查 | **仍未执行，保持为发布执行项。** 本轮为静态审查 + 一次性容器全量测试，不能替代真实上游/运行态故障的 CLI 直测。 |
| C-3 账号级 403 自动冻结 | **决策保持且实现精确。** 403 只暂停精确 (channel, model) 绑定并固化三类 revision（`breakerstore/store.go:653-661`，「只暂停该绑定，不影响同 Channel 其它模型」），新增 permission recheck 队列自动复核；未引入账号级/Provider 级冻结。 |
| 渠道 credential / 客户 Key 明文（B-9） | **维持原状。** credential 仍明文入库并在 Admin 列表/详情/Ops 表返回（`admin/channel.sql:707` 等）；SMTP 口令 GET 回显（A-7）为同权的新增声明决策。 |
| Admin 宽 CORS / 无 CSP（C-12） | **维持原状。** `httpmw/cors.go` 仍 `*`（Bearer 无 Cookie，风险有限）；无安全响应头中间件。 |
| A-2 指标暂不采集 | **维持原状。** 进程内指标与 `/metrics` 常驻（见 A-4），observability 栈仅 Loki+Alloy（日志），无 Prometheus 抓取。 |
| DeepSeek 字段静默丢弃（DEC-012） | 未见变更，维持接受。 |
| §5 SQL 观察清单 | 第 1 条恶化为每请求两写（D-1）；第 4 条未变（D-4）；请求列表窄 CTE 结论维持（D-5）；索引与 Route 归因结论维持。 |
| 运维清单增量 | 迁移数 43 → 82；新增号池相关运行态（账号槽/permission/水位暂停）；worker 仍无 HTTP 探针。 |

## 八、建议处置顺序

**P0（本周内，改动小收益大）**

1. A-1：升级 `golang.org/x/image` ≥ v0.45.0 与 Go 工具链 1.26.6，重跑 govulncheck 清零。
2. E-3 + E-4 + C-4：`gofmt -w` 收口 19 文件；stylua 格式化 + 清理三处 Lua 未使用符号，把 `make check-lua` 修绿（顺带确认 interaction_evidence 的设计意图）。

**P1（近期迭代）**

3. A-2：Admin 登录限速改用可信代理感知的来源 IP（复用 console 的 client_ip 实现）。
4. D-2：Console 时序/汇总接口加桶数上限（进行中的时间分桶工作收尾时一并做）。
5. D-1：路由 trace 双写改单写或采样，先用真实流量观测占比。
6. C-1：账务 worker 与探测 worker 拆分调度。
7. B-1：统一账号终身统计口径（倾向实收），修正 000077 与增量二者其一。
8. A-5：last_used_at 写节流/异步 + 失败降级放行。
9. D-3：迁移规范增加「大表索引一律 CONCURRENTLY」。

**P2（排期内消化）**

10. B-2 池型渠道成本倍率守卫；B-3 舍入实现收敛；B-4 capture 币种断言；C-5 Sticky 换 NewScript；C-6 proxyclient fail-closed；A-4 /metrics 纵深；A-6 seed 出库；E-5 staticcheck 清理；E-6 cdkey/subscriptionaccount/console auth 补测试；E-1/E-2 分层收敛（可与下次协议改造合并）；D-4 ChannelsOpsTable 预聚合。

## 九、本轮验证快照

| 验证项 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test -count=1 ./...`（无 DATABASE_URL/REDIS_ADDR，仅单测） | 通过（全部包） |
| `go test -count=1 -p 4 ./...`（一次性 PG16 + Redis 7，从零跑完 82 个迁移后执行） | 通过（0 failed）；抽查 `TestChatSettlementSettlesSuccessfulChat` 以 `-v` 复跑确认实际执行 0.34s 而非跳过 |
| 82 个 up 迁移从零依序执行 | 通过；57 张表；`000042` 有一条无害 WARNING（`SET CONSTRAINTS` 在事务块外） |
| `gofmt -l`（排除 sqlc 生成物） | **19 个文件漂移**（见 E-3） |
| `sqlc diff`（sqlc v1.31.1） | 无漂移 |
| `make check-lua`（luacheck 1.2.0 + stylua 2.5.2） | **失败**：luacheck 5 警告 / 49 文件；stylua 9 个文件未格式化（见 E-4、C-4） |
| `staticcheck ./...`（v0.8.1） | **27 项**（见 E-5） |
| `govulncheck ./...`（v1.7.0） | **8 个可达漏洞**：1 个 x/image（高，见 A-1）+ 7 个 stdlib（go1.26.6 已修）；另有 8 个不可达提示 |
| `go mod tidy -diff` / `go mod verify` | 无需变更 / 全部校验通过 |
| 一次性容器清理 | `unio-review-pg`、`unio-review-redis` 已删除；未触碰 `unio-dev` Compose 项目 |
| CLI 端到端 / 真实上游 / 运行态故障演练 | 本轮未执行（超出只读审查范围；B-3R 维持为发布执行项） |

结论：构建与全量测试（含 DB/Redis）全绿；需要立即处理的是依赖漏洞升级（A-1）与两项质量门禁回绿（E-3/E-4）；Admin 登录限速分桶（A-2）与 trace 双写（D-1）建议进入近期迭代。资金链路与认证栈经定点深读未发现资损或越权缺陷。

## 十、专项补充：废弃逻辑与无用代码（2026-09-06 第二轮）

方法：`golang.org/x/tools/cmd/deadcode` 全程序可达性分析（分别以生产 main 与「main+测试」为根，区分完全死代码与仅测试可达），与 staticcheck U1000 交叉印证；对 `sql/queries` 全部 525 个命名查询做调用方审计；Lua manifest、磁盘脚本与 Go 装载三方对账；`Deprecated/废弃/legacy` 标记全量扫描并逐项判定。全部只读，未改代码。

### 10.1 死 SQL 查询：20 个手写查询无任何调用方（含测试）（已证实）

删除以下 `-- name:` 定义后运行 `sqlc generate` 即可同步消除生成物中的死方法：

| 文件 | 死查询 | 备注 |
| --- | --- | --- |
| `sql/queries/shared/schema_health_checks.sql` | `CreateSchemaHealthCheck`、`GetSchemaHealthCheckByName` | **整特性死**：连同 `migrations/000028` 建的 `schema_health_checks` 表，Go 侧零引用。疑为 `platform/store/postgres.go:52` 迁移版本校验 TODO（GAP）预建后从未接线——要么接线，要么删查询文件并补 drop 迁移 |
| `sql/queries/shared/users.sql` | `GetUserByEmail` | 整文件仅此一条，可删整文件（console 用户查询在 console 目录另有实现） |
| `sql/queries/shared/user_balances.sql` | `ReserveUserBalance`、`UpdateUserBalance` | 被 `ReserveAvailableUserBalance` / capture 链取代 |
| `sql/queries/shared/cdkeys.sql` | `GetCDKeyByHashForUpdate`、`GetCDKeyRedemptionByIdempotencyKey`、`UpdateCDKeyRedeemed` | 被 `GetCDKeyForRedemptionByHashForUpdate` / `UpdateConsoleCDKeyRedeemed` 取代（CDKey v1 残留） |
| `sql/queries/console/cdkeys.sql` | `GetConsoleCDKeyRedemption` | 同上 |
| `sql/queries/admin/cdkeys.sql` | `ListCDKeyIDsByFilter` | 同上 |
| `sql/queries/admin/supply.sql` | `ListDisabledModelsRecoverable`、`ListModelRuntimeProtocols`、`ListModelsWithoutRuntimeSupply`、`ListSupplyCandidatesForChannels`、`ModelHasConfiguredSupply` | 号池/供给改造批次中被替换的中间产物（同文件其余 14 条在用） |
| `sql/queries/admin/provider.sql` | `CountProviderCurrencyRefs`、`ListProviders` | |
| `sql/queries/admin/channel.sql` | `ListChannelsByProvider`、`UpdateChannelCredential` | 凭据轮换已走独立路径 |
| `sql/queries/gateway/request_attempts.sql` | `HasRunningRequestAttempt` | permit 改造前的孤儿判定残留 |

另有 5 条**仅测试调用**的查询（`CreateUser`、`CreateUserBalance`、`GetProviderLedgerEntryByCostSnapshotID`、`ListLedgerEntriesByUser`、`SeedRuntimeStateEpoch`）：属测试夹具，保留，建议在查询注释里标注「仅测试使用」。

### 10.2 完全死的 Go 函数：21 项（deadcode 含测试根仍不可达，已证实）

生产文件 12 项（与 E-5 staticcheck 结果交叉印证，另含 staticcheck 不报的导出符号）：

- `internal/platform/proxyclient/proxyclient.go:79` `RuntimeProxyURL` —— 见 10.4-1，非普通死代码；
- `internal/service/admin/supply/supply.go:187` `ChannelBulkImpact`（导出的批量影响评估 API，HTTP 层已无挂载点）；
- `internal/service/admin/opsutil/opsutil.go:131` `DivideDecimal`；
- `internal/service/admin/cdkey/service.go:876` `cdkeyFromModel`、`internal/service/admin/modelprice/modelprice.go:1022` `toModelPrice`、`internal/service/admin/channelprice/channelprice.go:405` `toChannelPrice`、`internal/service/admin/customer/apikey.go:359` `int4ToPtr`、`internal/service/admin/customerops/customerops.go:287` `textPtr`、`internal/app/adminapi/user/api_keys.go:212` `int64Value`、`internal/core/requestlog/store.go:581` `int8OrNull`、`internal/service/console/requests/service.go:567` `int8Ptr`、`internal/service/console/cdkey/service.go:236` `normalizeCodeForTest`。

测试文件 9 项死脚手架：`internal/service/admin/modelrouting/candidates_test.go` 的 `stubRuntime`/`stubBreaker` 整组 5 个方法（对应用例已被删除或重写）、`internal/platform/store/sqlc/{breakdown_ledger,model_channel}_test.go` 3 个 helper、`internal/app/gatewayapi/openai/models/router_testutil_test.go` `ptrInt64`。

### 10.3 仅测试可达的生产代码：27 项，分三类处置（已证实）

**a) 被新 API 取代的残留（建议删除，共 12 项）：**

- `sessionhint.OpenAISessionKey` / `AnthropicSessionKey`（`sessionhint.go:79,135`）——生产已全部改用 `OpenAISessionHint` / `AnthropicSessionHint`（chat/responses/messages 六条路径核实在用，无粘性缺口）；
- `ingresslog.RecordInvalidJSON`（`json_decode.go:55`）——生产用 `RecordRequestBodyFailure`（5 个 handler 核实在用）；
- `appsettings.AdminBackendChannelTestWorkerEnabled/Interval/LogRetention`（`admin_backend_settings.go:139-153`）——生产读聚合 `AdminBackendChannelTest`，三个单字段访问器为旧接口；
- `appsettings.RestoreCriticalRuntimeControls`（`runtime_control.go:35`）薄包装——生产直接调 `...Observed` 变体（`bootstrap/runtime_control_recovery.go:59`）；
- `billing.ValidateNonNegativeMargin`、`billing.ProviderCostToSaleRatio`——多币种改造后生产统一走 FX 变体，同币种糖无人调用；
- `core/apikey.NewService` / `Service.Create` / `Verify`（`service.go:33-55`、`apikey.go:128`）——生产创建密钥直接用 `apikey.Generate()`（console `apikeys/service.go:348`、admin `customer/apikey.go:166`），整个 Service 是平行死实现。**注意：GAP-3-002（API Key 审计日志）TODO 挂在该死文件尾部，删除时把 TODO 迁到真实创建路径**；
- `breakerstore.DefaultConfig`（`types.go:202`）、`runtimecontrol.NewStateEpochRecoveryTransition`（`state_epoch_transition.go:48`）。

**b) 预置未接线，有意超前（保留，建议注明）：** `httpx.DecodeTextJSON` / `TextMaxJSONBodyBytes` / `InvalidJSONDiagnosticOf`（embeddings/search 纯文本上限路径，nginx `test.conf:75` 已预置对应 location，`GATEWAY_TEXT_MAX_JSON_BODY_MB` 配置链路完整）；`usage.UnknownTokens`、`failure.CategoryOf`、`SSEWriter.Err`、`StreamTimeoutState.TimeoutPhase`、`listquery.OrderByCase`、`DecodePositiveMsSetting`、`streamEncoder.Started` 等 API 完整性项。

**c) 测试便利构造器（保留）：** `lifecycle.newAttemptPermitOwner`、`newAttemptTimingObserver`、`stickyRedisKey`、`ChatSettlementRecoveryScheduledError`；以及 `app/gatewayapi/anthropic/messages/stream.go` 的类型化流事件族（`EncodeStreamEvent` + 7 个 `EventName`，约百行）——生产 Anthropic 流式为原文透传（`handler.go:93` 直接 `WriteEvent` 上游帧），类型化编码仅测试消费；若无「网关侧重建 Anthropic 事件」的计划可整组删除。

### 10.4 半接线特性：两处需要设计决策，非纯清理（已证实）

1. **出站代理回退链未收口。** `proxyclient.RuntimeProxyURL`（`proxyclient.go:77-84`）注释声称「三条账号路径与渠道出站统一走这一条决策」，实际零调用；同一「账号代理 → 渠道代理」逻辑手工内联在 `core/adapter/openai/chatcompletions/chat.go:40-42` 与 `core/adapter/openai/codex/responses/adapter.go:63-65` 两处，账号实体与 legacy 列的取舍另在 `subscription/outbound.go:273-279`。建议把内联点改为调用该函数（恢复单一事实源），或删函数并改注释——现状是文档性谎言 + 双份逻辑漂移风险。
2. **「请求写入证据」链两端各剩半截。** Go 侧：`adapter.MarkRequestWritten`（`core/adapter/attempt_timing.go:51`）设计为 adapter 在请求体写完上游时打点，生产无调用方，写入状态全靠 lifecycle 启发式推断（`attempt_timing.go:110-135` 的 uncertain 路径）；Lua 侧：`finish.lua:30-33` 由三个 ARGV 计算 `interaction_evidence` 后从未使用（即 C-4）。建议一次决策：接通两端（adapter 打点 + Lua 用证据参与熔断裁决），或两端一起拆除（删 `MarkRequestWritten`、`finish.lua` 计算与 Go 侧三个 ARGV 传参）。

### 10.5 Lua 与生成物

49 个脚本在 manifest、磁盘、Go `luaScript()` 装载三方完全一致，无孤儿脚本、无未装载条目；Lua 死代码仅 C-4 所列三处符号（`finish.lua` 未用变量与参数、`observe_channels.lua` 未用函数）。sqlc 生成目录的死方法与 10.1 一一对应，删查询后再生自动消除，不需手改生成物。

### 10.6 废弃/兼容逻辑盘点（标记扫描逐项判定）

- **可退役的遗迹**（当前部署均从零迁移基线建库，以下取值无任何写入方、不可能出现在数据里）：`routing_decision_traces.trace_status` 的 `legacy_sampled` 枚举值（`000033` CHECK + `admin/routing_traces.sql:4` 注释；`routing_trace.go:25` 自述「0 保留给改造前的 legacy 行」）；`runtime_control_operations` 的 `legacy awaiting_release` 态映射（`shared/runtime_control_operations.sql:124`）。清理属可选（动 CHECK 需迁移），至少注释应改为「历史兼容位，从零建库不会出现」。
- **有意保留、不建议动**：`HTTP_MAX_JSON_BODY_MB` 旧环境变量播种新的分体上限（`config.go:427-445`，老部署兼容）；`subscription_accounts.proxy_url` legacy 裸 URL 列及其回退（`outbound.go:273-279`）——文件导入仍活跃写入（`importer.go:222`），是双来源设计不是死路径；`capability_keys.deprecated` 软退役位（产品功能）。
- **已清理干净、确认无残留**：`api_keys` 旧 rpm/tpm/rpd 三列（`000003_api_keys.up.sql:64` 注释与事实一致，schema 无残留）；`routes`/`route_id` 概念（`sql/queries` 与 `migrations` 零命中，08-04 时代的线路回退语义已随重构整体消失）。

### 10.7 处置建议

一次 `chore(cleanup)` 提交可收口的部分：10.1 的 20 条死查询（含 `users.sql`、`schema_health_checks.sql` 两个整文件）+ `sqlc generate`；10.2 的 12 个生产死函数与 9 项测试死脚手架；10.3(a) 的 12 项被取代残留（GAP-3-002 TODO 先迁移再删）。合计净删约 700+ 行手写代码（生成物另随 sqlc 再生缩减约 40 个死方法），并与 E-3/E-4 的 gofmt、Lua 门禁修复合并为同一批清理。需要单独决策的两项：10.4 的代理回退链收口与「写入证据」链接通/拆除；`schema_health_checks` 表接线或 drop（涉及迁移与 `postgres.go:52` 的 GAP 取舍）。
