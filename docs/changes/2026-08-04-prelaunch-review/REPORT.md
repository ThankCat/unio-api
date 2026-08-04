# 上线前检查报告（源码复核版）

- 检查日期：2026-08-04
- 复核范围：`unio-gateway`、`unio-admin`、`unio-blueprint` 当前工作树
- 状态说明：本报告按后续处置持续更新；已关闭项以当前代码、测试和 Blueprint 为准

## 一、复核结论

原报告的大多数待修问题在当前源码中仍然存在，但有几项结论已经过期，需要先纠正：

- B-4 已修好，并已在一次性 PostgreSQL 16 数据库中实际跑过对应测试，应移入已关闭项。
- B-10 的后端接口已经返回两层熔断状态和 `probe_only`，剩下的是前端展示不一致，不再是“接口缺字段”。
- C-11 的 Dashboard 已能拆开显示缓存读取和缓存写入，问题只剩模型详情页仍把两者合称“缓存命中率”。
- 请求列表已经先分页再补充详情，`COUNT(*) OVER()` 的风险明显降低，不应继续列为首要慢查询。
- 服务商级共享熔断已经写入 Blueprint，不属于文档缺口。
- `000040` 已恢复为正式迁移，不再有“版本从 40 回退到 39”的问题；当前真正的问题是只有 up、没有 down。
- B-1R 已修复：孤儿扫描与收口都排除 running attempt，收口事务重查 recovery job，任务创建与清扫使用
  同一 request 行锁串行化。

同时发现几项原报告没有充分说明的风险：

- A-3 已经移除硬编码 token，会话实现本身合理；但公开登录入口没有失败次数限制，仍可被高速反复猜口令。
- 删除整个 `internal/blackbox` 虽然让 B-3/C-6 的失败消失了，也同时删除了真实上游和运行态故障演练，不能把“删测试”当成“功能已再次证明正常”。
- partial settlement 的恢复任务没有保存原本应写入的失败/取消状态，重放时可能把请求改记为成功。
- recovery 重放在部分成本路径上会丢失长上下文倍率，可能少扣客户费用、少记平台成本。

## 二、已完成改造复核

| 原编号 | 当前结论 | 复核说明 |
| --- | --- | --- |
| A-1 依赖漏洞 | 已修复 | `toolchain go1.26.5` 已生效；本轮 `govulncheck` 可达漏洞为 0。`DEVELOPMENT.md` 仍只写“固定为 1.25.5”，建议顺手改准。 |
| A-2 指标未采集 | 风险接受 | 代码保留指标但暂不采集，属于明确的产品决定，不是这轮代码修复。 |
| A-3 硬编码 admin token | 主问题已修，仍有残留 | 已改为用户名口令换取随机 Redis 会话，token 可过期、可吊销；登录防猜测问题见 A-5。 |
| A-4 三表排序 400 | 已修复 | routes、channels、sync-jobs 的允许排序字段与 SQL 已对齐，Admin 当前检查通过。 |
| B-1 预扣搁浅泄漏 | 已修复 | stranded sweeper 已回收失败/取消请求的搁浅冻结；orphan sweeper 排除 running attempt，并在 request 行锁内重查 recovery job 与 attempt。recovery job 创建使用同一行锁，不能在清扫提交后迟到插入。 |
| B-2 reservation 缺失时提前返回 | 源码已修 | reservation 缺失后会继续把 running 请求收为 failed；但目前没有直接覆盖 finalizer 数据库事务的回归测试，建议补一条。 |
| B-3/C-6 blackbox 失败 | 测试文件已删除，不等于功能修复 | 过期断言确实不存在了，但真实上游与故障演练也一起消失，见 B-3R。 |
| B-4 `breakdown_ledger_test` | 已修复 | 渠道优先级已从 1 改为 10；本轮在隔离 PostgreSQL 16 中实际执行并通过。 |
| 死代码清理 | 源码改造存在 | 相关读取器、查询和占位目录已删除；本机没有 `deadcode` / `staticcheck`，本轮没有重新声明“当前全仓干净”。 |
| 长上下文主结算 | 主路径正常 | 普通结算按真实输入量判断阶梯价；recovery 重放仍有单独问题，见 B-11。 |

## 三、仍待处置：高风险

### A-5 Admin 登录没有防止反复猜密码

**是否存在：存在。** `/admin/v1/login` 只有用户名口令比较，没有按 IP 或用户名限制失败次数，也没有逐步延迟或临时锁定。口令只做一次快速 SHA-256 比较，公开入口可以被高速尝试。

**影响：** 如果 Admin 暴露在公网，弱口令或重复使用的口令更容易被猜中。登录成功后权限很大，还能读取当前列表接口返回的明文凭据。

**建议怎么改：** 上线前至少在反向代理和应用两层选一层加登录限速，例如同一 IP 和用户名连续失败后逐步延迟，短时间失败过多就临时拒绝，并记录告警。口令应足够长且独立使用。后续再考虑使用专门的慢哈希保存口令摘要。

### B-3R 删除整套 blackbox 后，关键上线能力失去自动验证

**是否存在：存在。** `internal/blackbox` 已整树删除，原来的 OpenAI、Anthropic、真实上游、Redis 状态丢失、AOF/RDB 恢复、half-open、Sticky 和长流演练都不在当前仓库中。

**影响：** 当前单元测试全绿只能证明局部逻辑正常，不能再次证明完整请求、账务、Redis 恢复和真实供应商仍能连起来工作。以后相关行为退化时，更可能在上线后才发现。

**建议怎么改：** 不必恢复全部旧套件，但应保留一组小而稳定的发布检查：三种公开接口各一条完整请求、一次 settlement recovery、一次 Redis 状态丢失恢复、一次 Sticky/fallback、一次真实上游冒烟。真实密钥仍由显式开关和环境变量注入，不能写入仓库。

### B-5 流式尾部报错后，数据库和运行结果说法不一致

**是否存在：存在。** 已取得最终 usage 并完成结算后，如果流尾再报错，代码仍返回原错误，`RunResult` 默认保持 Failed，也不会执行 `Sticky.BindSuccess`；结算事务却可能已把请求和 attempt 写成 succeeded。

**影响：** 同一请求可能在数据库里显示成功，在 metrics 和路由 trace 里显示失败，客户也可能在已扣费后看到连接异常。运维会很难判断它到底算成功还是失败。

**建议怎么改：** 先确定产品口径，再让所有记录保持一致。如果“拿到终态 usage 就算完整成功”，应返回成功并正常绑定 Sticky；如果“流尾错误仍算交付失败”，结算时也应把请求/attempt 按失败收口，只保留已经发生的计费事实，不能数据库写成功、外层又报失败。

### B-6 长上下文预授权与最终结算使用不同 token 数量

**是否存在：存在。** 预授权按本地估算判断是否超过长上下文门槛，最终扣费按上游真实 usage 判断。估算没有超过、真实值超过时，冻结的是普通价格，结算用的是阶梯价格。

**影响：** 差额会走二次补扣；余额不足时平台核销。上游可能额外加入系统提示，真实输入量会明显高于客户看到的内容，因此这不是纯理论情况。

**建议怎么改：** 预授权接近门槛时增加安全余量，或直接按长上下文价格冻结；同时监控 `authorization_underfunded` 的数量和金额。若产品决定继续接受低估，也应把它写成明确的资金风险，而不是只依赖事后核销。

### B-7 长请求结束时，TPM 分钟桶可能已经过期

**是否存在：存在。** 请求完成时如果原分钟桶已经过期，`finish_request_admission.lua` 会直接跳过调整。大约超过 7 分钟的请求就可能遇到。

**影响：** 请求明明消耗了 token，TPM 计数却偏低，长请求多时可能绕过限制。

**建议怎么改：** 请求续租时同步延长对应桶的保留时间，或在 Finish 时按保存的桶身份重建并补差额。增加“请求跨过桶过期时间后再返回真实 usage”的 Redis 测试。

### B-8 Anthropic 非流式缺 usage 时会按 0 结算

**是否存在：存在，但只限明确路径。** Anthropic 非流式响应里的 `usage` 不是可空结构，字段缺失或为 null 后，输入和输出会变成已知的 0。OpenAI Chat 和 Responses 非流式已经会拒绝缺失 usage；流式缺 usage 也已有 partial settlement 或释放/失败路径，不应再笼统算在这里。

**影响：** 上游可能已经计费，平台却把客户费用和平台成本都记为 0。

**建议怎么改：** 把 Anthropic 非流式 usage 改为可判断“有没有返回”的结构，并要求 input/output 两个必需字段存在。缺失时按“上游可能有成本但无可靠 usage”收口，不能按 0 元成功。

### B-9 Admin 列表接口批量返回完整凭据

**是否存在：存在。** 渠道运维列表返回完整 `credential`，请求列表返回完整 `api_key_plaintext`。

**影响：** 只要一个 Admin 会话被窃取，就能一次拿到大量可直接使用的密钥。明文存储是已接受的产品决定，但不代表每个列表都需要把明文带回浏览器。

**建议怎么改：** 列表只返回掩码或前缀；确实需要复制时，放到单条详情接口并要求明确点击，可再加一次权限确认和操作日志。请求记录列表没有展示完整 API Key 的必要，应直接删掉该字段。

### C-8 Partial recovery 会丢失原本的失败/取消状态

**是否存在：存在，而且比原报告写得更宽。** recovery job 保存了 usage 和价格，但没有保存 `RequestFinalStatus`、`AttemptFinalStatus` 及对应错误事实。partial 首次结算失败后由 worker 重放时，会回到默认 succeeded。若首次结算已提交但 job 的完成标记失败，重放又会因为请求已经是 failed/canceled 而持续冲突；同时 partial 的 `final_usage_received=false` 也与成功幂等检查要求不一致。

**影响：** 客户取消或上游中断的请求可能在恢复后被审计为成功；另一种时序下 recovery job 会反复失败直至 dead。费用可能已经正确扣除，但状态和告警会失真。

**建议怎么改：** recovery job 必须一并保存原请求状态、attempt 状态和错误信息，重放时原样使用；幂等检查也要允许“partial + final usage 未收到”的等价重放。补上取消、上游中断、正常缺 usage 三种恢复测试。

### B-11 Recovery 重放可能丢失长上下文倍率

**是否存在：存在。** 重放只通过 `CostBaseModelPriceID` 找回长上下文规则。使用 `channel_prices` 绝对成本覆盖时，这个 ID 为 0，worker 得到的是空策略，即使原请求已经超过长上下文门槛也不会放大价格。

**影响：** 只有“长上下文请求 + 首次结算失败 + recovery 重放 + 绝对成本覆盖”同时出现时触发，但一旦触发，会少扣客户费用，也会少记平台成本。

**建议怎么改：** recovery job 单独保存结算当时的长上下文开关、门槛、输入倍率和输出倍率，不能借成本来源 ID 间接推断。用同一组事实同时重放客户售价和平台成本。

## 四、仍待处置：中风险

### B-10 half-open 的前端展示互相矛盾

**是否存在：存在，但现在是前端展示问题。** API 已返回 `provider_breaker_state`、`channel_breaker_state` 和 `eligibility.status=probe_only`。前端仍把 half-open 总分显示为 0，同时保留各项加分；详情页又把 `probe_only` 统一显示成“有候选资格”。

**建议怎么改：** `probe_only` 单独显示“仅允许探测”，不要归到普通“有资格”；half-open 时隐藏普通总分等式，或明确写“探测状态，总分不参与普通排序”。

### C-1 `ReserveIfPresent` 会在 session 缺失时静默跳过 TPM

**是否存在：存在。** 六条生产生成路径都使用 `ReserveIfPresent`，session 未安装时直接当作不需要预留。

**建议怎么改：** 生产生成接口改为“必须存在 session”，缺失就返回内部错误并告警；仅测试或明确不计入 TPM 的路径才允许可选模式。

### C-2 成本分项和总额分别四舍五入

**是否存在：存在。** 分项逐个保留 10 位小数，总额却从未舍入的原值相加后再舍入，极小数位可能进位不同，数据库又要求总额严格等于分项之和。

**建议怎么改：** 先得到各个已经舍入的分项，再用这些分项相加生成总额；增加一组刚好落在进位边缘的价格测试。

### C-3 账户级 403 只能按“渠道 + 模型”逐个暂停

**是否存在：存在。** 当前暂停键绑定具体模型。欠费、账号停用这类影响整个渠道账号的 403，需要每个模型各失败一次。

**建议怎么改：** 只有能确认是账号级错误时才增加渠道级暂停；不确定的 403 继续保持模型级，避免一次误判停掉整条渠道。

### C-4 OpenAI Responses 的错误诊断信息与其他 adapter 不一致

**是否存在：部分存在。** 请求审计不保存上游错误正文是安全设计，不应作为通用缺陷。真正不一致的是 OpenAI Responses 非 2xx 错误没有填写受限长度的 `ResponseSnippet`，而 Chat/Messages 有。

**建议怎么改：** 只在内部 adapter 错误中加入经过长度限制和清理的 snippet，供渠道检测和 Debug 使用；继续禁止把上游原文写进公开响应和普通请求审计。

### C-5 `Code.Category()` 会产生没有定义过的分类

**是否存在：存在。** `rate_limit_exceeded` 会得到 `rate`，`channel_rate_limited` 会得到 `channel`，都不是系统定义的 `ratelimit` 分类。

**建议怎么改：** 不要按第一个下划线自动截断，改成明确映射表，并为全部稳定错误码加测试。

### C-7 数据库读取失败会误报“线路未配置”

**是否存在：存在。** `loadEnabledRoute` 把所有 `GetRouteByID` 错误都当成找不到线路。

**建议怎么改：** 只有真正的无记录才返回“线路未配置”；数据库超时、断连等要保留为服务故障，方便运维定位。

### C-9 Admin 多处把前 100 条当成全部数据

**是否存在：存在。** 模型、服务商和线路表单渠道列表有固定 100 条上限，超过后后面的选项不会出现。

**建议怎么改：** 通用选择器支持继续分页或服务端搜索；线路表单至少循环拉完 enabled/disabled 渠道，不能静默截断。

### C-10 排除原因翻译不完整，还会多显示一条无关毛利失败

**是否存在：存在。** `channel_cost_missing` 等原因会显示半中文半代码；数据库先排除的渠道默认 `MarginStatus=not_evaluated`，前端又把它当成一次毛利检查失败。

**建议怎么改：** 所有后端稳定原因码建立完整中文映射；只有真正执行过毛利检查才显示通过或失败，`not_evaluated` 显示“未检查”或直接隐藏。

### C-11 模型详情页的“缓存命中率”仍包含缓存写入

**是否存在：存在，但范围已缩小。** Dashboard 悬浮说明已经把读取和写入拆开；模型详情后端仍用 `(cache read + cache write) / input`，页面却只写“缓存命中率”。

**建议怎么改：** 最好把模型详情改成真正的 `cache read / input`；如果要保留读写合计，名称改成“缓存相关占比”，并像 Dashboard 一样展示读取、写入和未缓存三部分。

### C-12 `localStorage`、宽 CORS 与无 CSP 叠加

**是否存在：存在。** Admin token 保存在 `localStorage`，CORS 允许任意来源，页面没有 CSP。需要说明的是，恶意网站不能凭空读取另一个来源的 `localStorage`；风险主要在 Admin 页面发生 XSS、恶意扩展读取 token，或 token 已经泄露之后。

**建议怎么改：** 优先改为 `HttpOnly + Secure + SameSite` Cookie，并把 CORS 收窄到实际 Admin 域名；增加 CSP，至少限制脚本来源和接口连接目标。短期做不到时，先缩短会话时间并限制 Admin 网络入口。

### C-13 `000040` 迁移缺少 down 文件

**是否存在：存在。** 当前有 40 个 `.up.sql`，只有 39 个 `.down.sql`；`000040_admin_query_indexes.up.sql` 存在，对应 down 没有恢复。

**建议怎么改：** 补回只删除这 6 个索引的 down 文件，并在隔离 PostgreSQL 中验证 up、down、再 up。当前最大迁移号仍是 40，不需要再做“40 回退到 39”的版本处理。

### C-14 Sticky TTL 的设置说明写反了

**是否存在：存在。** 实现和 Blueprint 都是“原绑定渠道完整成功后滑动续期”，但 `gateway_settings.go` 的注释仍写“绝对过期、命中不刷新”；Admin 可见的设置说明同时写了“绝对过期”和“滑动续期”，前后矛盾。

**建议怎么改：** 统一改成“读取命中本身不续期，只有原绑定渠道完整成功才把 TTL 重新延长”。

## 五、SQL 与性能

按流量增长后的优先级处理：

1. `UpsertRoutingDecisionTrace` 每个请求都会反复更新 JSONB，应观察写放大和行锁等待。
2. `FindRouteCandidates` 是每次请求的关键查询，应持续用真实数据量跑 `EXPLAIN ANALYZE`。
3. Dashboard `Radar()` / `percentile_cont` 需要对大量数据排序，时间范围越大越慢。
4. `ChannelsOpsTable` 仍在分页前聚合全部匹配 attempt，数据增长后应先缩小渠道页或预聚合指标。
5. 请求列表已经在 `filtered_page` 先分页，再只对当前页补充 usage、成本和渠道信息。`COUNT(*) OVER()` 现在只作用于基础请求表，风险已降低，先用大数据量 EXPLAIN 观察，不必优先改。
6. 两个缺失索引仍存在：`request_attempts(provider_id, created_at)`、`request_records(route_id, created_at)`。
7. 部分线路和 Dashboard 聚合仍使用 `api_keys.route_id` 当前绑定，而不是 `request_records.route_id` 请求快照。API Key 换线路后，旧请求会被算到新线路，这是统计口径错误，应优先修正。

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
- 孤儿、搁浅、recovery 三方边界、默认参数和巡检脚本已经由当前 Blueprint 未提交改动补充。本轮没有覆盖这些用户已有修改。
- Sticky 滑动续期在 Blueprint 中是正确的，错误在 Gateway 设置说明，见 C-14。

## 七、风险接受项

- 渠道 credential 和客户 API Key 在数据库中保留明文，是现有产品决定；B-9 只要求减少列表接口的批量暴露。
- 预授权不足时允许 overage debit，仍不足时由平台核销，是现有账务机制；B-6 要求降低和监控这种情况，而不是否定该机制。
- DeepSeek 无法转换的部分字段静默丢弃，属于 DEC-012 的已接受行为。
- 指标暂不采集、告警暂不投递，属于 A-2 的风险接受；这意味着上线后很多问题只能靠日志和数据库事后发现。
- 普通请求审计不保存上游错误正文是安全边界，不建议为了排错直接放开。

## 八、上线前运维清单

- 必配：`DATABASE_URL`（生产建议启用 TLS）、`ADMIN_USERNAME`、强 `ADMIN_PASSWORD`、`ADMIN_SESSION_TTL`、`REDIS_*`、`GATEWAY_ENV=production`、`UNIO_SKIP_DOTENV=true`。
- Admin 没有应用内登录限速前，至少在反向代理限制 `/admin/v1/login` 的失败频率，并限制 Admin 的公网访问范围。
- 前端生产构建必须提供非 localhost、HTTPS 的 `VITE_ADMIN_API_BASE`；重新构建并部署 `dist/`，确保旧硬编码 token 产物不再对外。
- 如果旧 token 曾经提交或部署，仍应清理历史和旧制品；新后端上线后确认旧 token 已不能访问 Admin API。
- 迁移由外部工具执行，服务启动不会校验 schema 版本。补齐 `000040` down 后，在隔离库验证 up/down，再安排生产迁移。
- 探针：Gateway 有 `/healthz` 和 `/readyz`；Admin 只有 `/healthz`；Worker 没有 HTTP 探针，需要用进程存活、任务心跳或日志监控。
- Redis 不支持 Cluster；生产应使用受支持的单节点、主从或 Sentinel 方案，并开启持久化。
- 原状态丢失演练曾经通过 15/15，但当前套件已删除。重新建立可重复演练前，不应把旧结果当作当前版本的自动保证。
- `HTTP_SHUTDOWN_TIMEOUT` 默认 10 秒，可能截断长流；按可接受的发布等待时间调大。
- 每条线路至少放入两个不同 Provider 的渠道，避免一个 Provider breaker 打开后整条线路没有候选。
- 增加账务巡检：终态请求仍有 authorized 冻结、余额 reserved 与冻结合计不一致、dead recovery job、超过阈值仍 running 的请求都应告警。

## 九、本轮验证快照

| 验证项 | 当前结果 |
| --- | --- |
| Gateway `go test -count=1 ./...`（显式移除 DB/Redis 环境变量） | 通过；依赖 PostgreSQL/Redis 的用例按设计跳过 |
| Gateway `go vet ./...` | 通过 |
| Gateway `go build ./cmd/...` | 通过 |
| B-4 专项数据库测试 | 一次性 PostgreSQL 16 全迁移后通过；临时容器已删除 |
| B-1R 孤儿清扫专项数据库测试 | 一次性 PostgreSQL 16 全迁移后通过：活跃长流保护、列表后 recovery 重查、迟到 recovery 插入拒绝 |
| `govulncheck ./...` | 可达漏洞 0；依赖模块中另有 4 条不可达漏洞提示 |
| Admin `typecheck` / `eslint` / `vitest` | 通过；15 个测试文件、53 个测试用例 |
| Blueprint `make validate` | 通过；140 个 Markdown 文件、41 个目录 |
| blackbox / 运行态故障演练 / 真实上游 | 当前源码已删除对应套件，本轮未执行；旧手工结果不作为当前自动验证 |
| `deadcode` / `staticcheck` | 本机未安装，本轮未重新执行 |

结论：当前代码可以正常构建，普通单测、Admin 检查和文档校验均通过；B-1R 账务清扫边界已经关闭，
但 partial recovery、登录防猜测、长请求 TPM 和凭据批量暴露仍不适合仅靠运维规避，建议优先按本报告
其余高风险项处理后再做上线确认。
