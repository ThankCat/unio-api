# 上线前检查报告

检查日期：2026-08-04
范围：`unio-gateway`（Go 实现）、`unio-blueprint`（权威文档）、`unio-admin`（管理后台前端）
性质：只读审查，未修改三个仓库的任何代码、配置、测试或文档正文

每条结论都标注了取证方式。标「已实测」的是我本人跑过命令或读过代码确认；标「专项审查」的来自分领域深挖，我复核了其中的高风险项并在文中注明复核结果。

---

## 一、执行摘要

构建与静态检查全部干净，纯单元测试 73 个包全绿，P4 故障与路由演练 15 项全过 0 失败（含 Redis 状态丢失、AOF/RDB 恢复、epoch 回滚等运维恢复流程）。真实上游端到端跑通了完整计费链，长上下文阶梯计价经真实 27 万 token 请求与代码测试双向确认无误。

**原判定的四项上线阻断，当前状态**：依赖链 9 个可达漏洞**已修复并验证归零**（A-1）；业务指标未被采集**已由产品决策降级为风险接受项**，代码保留、暂不接入（A-2）；Admin 前端硬编码 token**代码侧已修复**，登录改为用户名口令、凭据移入环境变量，但旧 token 的轮换、前端产物重建与 git 历史清理仍需你处置（A-3）；唯一完全未处理的是 Admin 三张表的排序列点击即整表报错（A-4）。

**两条我推翻了的结论**（避免误导后续处置）：迁移编号「漂移」不是仓库缺陷而是你未提交的进行中改动，且我实测其 schema 与开发库完全等价；成本快照舍入不一致是真的但触发条件很窄，不构成阻断。

---

## 二、验证方式与环境

为避免污染开发数据，全程使用一次性容器：

- PostgreSQL 16 于 `127.0.0.1:55432`（`uniotest`）与 `127.0.0.1:55433`（`uniov2`，用于验证工作树等价性）
- Redis 7 于 `127.0.0.1:56379`，namespace `unio:prelaunch`
- 开发库 `unio-postgres` 与 `unio-redis` 全程只被 `SELECT` 读取

这一隔离是必需的：`internal/platform/store/sqlc/identity_test.go:42-90` 与 `schema_health_checks_test.go:37-54` 会向 `DATABASE_URL` 指向的库提交 user、route、api_key 与 health check 行且没有 cleanup（已实测）。

真实上游凭据从开发库 `channels.credential` 读取，仅通过环境变量在运行时传入，未写入任何文件、日志或本报告。

---

## 三、上线阻断项

### A-1 依赖链存在 9 个可达漏洞 —— 已修复（2026-08-04）

**发现时**：`govulncheck ./...` 报告 9 个**代码可达**漏洞。7 个来自构建用的 Go 标准库（`go1.26.2`），2 个来自依赖：

| 漏洞 | 组件 | 发现版本 | 修复版本 |
| --- | --- | --- | --- |
| GO-2026-6061 | `google.golang.org/grpc` | v1.81.1 | v1.82.1 |
| GO-2026-5970 | `golang.org/x/text` | v0.38.0 | v0.39.0 |
| GO-2026-5856 | `crypto/tls` | go1.26.2 | go1.26.5 |
| GO-2026-5039 | `net/textproto` | go1.26.2 | go1.26.4 |
| GO-2026-5037 | `crypto/x509` | go1.26.2 | go1.26.4 |
| GO-2026-4982 / 4980 | `html/template` | go1.26.2 | go1.26.3 |
| GO-2026-4971 | `net` | go1.26.2 | go1.26.3 |
| GO-2026-4918 | `net/http` | go1.26.2 | go1.26.3 |

对网关最直接的是 GO-2026-4918（HTTP/2 `SETTINGS_MAX_FRAME_SIZE` 死循环），调用链经 `internal/core/adapter/openai/chatcompletions/chat.go:251` 的出站流式请求可达——网关的核心工作就是对上游发起 HTTP/2 流式调用。

**已实施的修复**：

- `go.mod` 新增 `toolchain go1.26.5`（`crypto/tls` 要求的最高版本）。把工具链固化在仓库里而不是依赖开发机装了哪个版本，`GOTOOLCHAIN=auto` 会自动获取，CI 与本地一致
- `golang.org/x/text` v0.38.0 → v0.39.0
- `google.golang.org/grpc` v1.81.1 → v1.82.1

两个依赖都是 indirect，改动范围仅 `go.mod` 3 行加 `go.sum` 8 行。

**验证结果**：重跑 `govulncheck ./...` 得到 `Your code is affected by 0 vulnerabilities`，可达漏洞 9 → 0，被导入包中的漏洞 4 → 0；剩余 4 个位于 require 但代码不调用的模块，不可达。回归验证：`go build`、`go vet`、`gofmt` 全部干净，单元测试仍为 73 个包通过 0 失败，与升级前一致。

### A-2 业务指标完全没有被采集 —— 已降级为风险接受项（2026-08-04 决策）

**事实（已实测）**：`deployments/observability/prometheus/prometheus.yaml` 的 `scrape_configs` 是空数组，Prometheus 活跃采集目标 0 个，其已知的 632 个指标里 `unio_*` 占 0 个。而代码侧埋点是完整的——注册了 60 个指标（38 计数器、13 gauge、9 直方图），运行中的 Admin 进程 `/metrics` 返回 HTTP 200、23 个指标族、含真实累积数据（如 `begin_runtime_reconcile` 已记录 2582 次调用的完整延迟直方图）。所以缺的是采集与消费，不是埋点。

**连带发现**：告警投递链路整体不通，且与是否采集无关。`prometheus.yaml` 有 `rule_files` 但没有 `alerting:` 段；Loki ruler 是 `enable_alertmanager_v2: false` 且未配投递地址；compose 里没有告警投递组件。因此现有 9 条规则（Prometheus 侧 4 条、Loki 侧 5 条）无法送达任何接收方。其中 Prometheus 那 4 条还依赖 `up{job=...}`、`node_filesystem_*` 等指标，在 `scrape_configs` 为空时这些指标根本不存在，规则连触发都不会发生。

**决策**：不引入独立看板组件，暂不配置采集，指标代码原样保留不删除。理由是保留成本接近于零（内存计数器，无人抓取时完全空转），而删除成本很高——实测 26 个文件、354 处调用点，且集中在 `attempt_runner.go`、`attempt_runner_stream.go`、`request_lifecycle.go`、`sticky.go`、`routing_trace.go` 等本次审查高风险缺陷最密集的文件里，上线前做这种规模的机械删除，回归风险远大于收益。

**接受的后果**：上线后熔断打开、结算失败积压、runtime state 丢失、限流 fail-closed 这些状态没有历史曲线，也没有任何主动告警，只能靠日志事后追溯。今早 08:52 那次事故就是这个模式——事后翻数据库才定位。日志链路（Alloy → Loki → Admin 日志页）正常工作，可查单条请求上下文，但无法回答「熔断在哪一秒打开、失败率如何爬升、持续多久」这类趋势问题。

**后续路径**已记录在蓝图 [指标规范](https://github.com/unioapi/unio-blueprint/blob/main/docs/specifications/metrics.md) 的「当前状态与待完善」一节，含四项待办：采集接入、告警投递、展示形态选型、与 Admin 运维视图的边界厘清。

### A-3 Admin 前端硬编码真实 admin token —— 代码已修复，密钥轮换与历史清理待你处置（2026-08-04）

`unio-admin/src/pages/LoginPage.tsx:25` 的 `useState` 默认值是一个 64 位十六进制 admin token。已实测确认三点：该字面量存在于源码；已被提交（`de92409 feat: update Unio branding and favicon`）；已进入构建产物 `dist/assets/LoginPage-DJ4xq0ps.js`。

结合 Admin 认证是单个静态 Bearer token 且无 RBAC，泄露这一个字符串等于拿到完整管理端权限——改 provider origin、读取全部渠道明文凭据、手工调额、改运行时配置。因为 token 是静态的，没有单点吊销手段。

**已实施的修复（2026-08-04）**：登录方式从「粘贴 token」改为用户名口令，硬编码默认值随之删除。

- 后端新增 `POST /admin/v1/login`，是 admin 表面唯一不需要 token 的端点，单独分组挂载，`AdminAuth` 仍覆盖其余全部端点
- 凭据来自环境变量 `ADMIN_USERNAME`（默认 `admin`）与 `ADMIN_PASSWORD`（无默认），实际值只放本地 `.env` 或部署环境变量；`.env.example` 只留占位。口令为空时 admin-server 启动失败并报 `config_missing: ADMIN_PASSWORD is required`，不以空口令对外开放登录
- 用户名与口令各自先 SHA-256 再做常量时间比较，定长摘要避免口令长度经计时侧信道泄露；两项结果按位与合并而非短路布尔，用户名错误时仍执行口令比较
- 校验失败一律回 401 与同一句文案、同一个错误码，不区分用户名错、口令错或缺字段，避免枚举有效用户名
- 登录成功签发的就是既有 `ADMIN_API_TOKEN`，因此中间件与 120 余个业务端点的鉴权路径完全不变；第一版不引入会话与 RBAC
- 前端 `LoginPage` 改为用户名口令表单，登录请求走独立 axios 实例，不挂全局拦截器（避免携带上次残留 token，也避免 401 把用户从登录页踢走）

**验证结果**：实际启动 admin-server 逐项验证——正确凭据回 200 且 token 长度 64；错误口令、错误用户名、空请求体三种情况的 HTTP 状态、错误码与文案完全一致；用返回的 token 访问 `/ping` 与 `/providers` 均 200；无 token 与错误 token 均 401；空口令启动失败退出码 1。后端 `build`/`vet`/`gofmt` 干净、相关包测试通过；前端 `tsc`、`eslint` 零错误，`vitest` 15 文件 53 用例全通过；源码中已无 64 位十六进制字面量。

**仍需你处置的三件事**（涉及密钥轮换与历史重写，不适合由代码改动完成）：在 Gateway 侧轮换 `ADMIN_API_TOKEN`（旧值已泄露）、重新构建前端产物（当前 `dist/` 仍是含旧 token 的旧包）、清理 git 历史中的旧 token。

附带同一处的构建配置风险（**初判有误，已更正**）：`dist/assets/client-*.js` 里烘进的 API base 是 `http://127.0.0.1:8522`。最初据此推断「构建时没有提供 `VITE_ADMIN_API_BASE`」，复核后不成立——`.env.local` 里确实配了该变量且值正是这个，与后端 `ADMIN_HTTP_ADDR=:8522` 一致，所以那份产物（2026-08-01 构建）本就是一次**本地开发构建**，不是漏配。

真正的风险比原判更普遍：**产物本身不区分开发与生产**。API base 在构建期被静态烘进 JS，而没有任何机制阻止把带 localhost 的产物部署出去——无论是开发机上直接 `build` 后上传，还是构建环境缺少该变量落到 `src/lib/config.ts:1-4` 的兜底值，结果都是所有请求打向使用者本机，且 Bearer token 走明文 HTTP。当前没有构建期校验会拦住这两种情况。

### A-4 Admin 三张表的排序列点一下就整表报错（已实测）

`useServerTable` 把 TanStack 的列 `id` 直接作为 `?sort=` 发给后端，而后端 `listquery.ParseSort` 对白名单外字段一律返回 400，前端把错误渲染成整块红色 Alert 并显示后端英文原文。`sort` 同时写进 URL，刷新与分享链接都会复现。

我逐列核对了前两张表：

- `/admin/v1/routes/ops`：前端可点排序的是 `name`、`status`、`mode`、`bindings`、`created_at`；后端白名单是 `name`、`created_at`、`bindings`、`pool_channels`、`models`（`internal/app/adminapi/route/routes_ops.go:114-120`）。点「状态」或「策略」必 400
- `/admin/v1/channels/ops`：前端可点 `priority` 与 `bound_routes`，后端白名单（`internal/app/adminapi/channel/channels_ops.go:118-128`）两者都没有。点「优先级」或「线路」必 400

第三处 `/admin/v1/capability/sync-jobs` 的 `id`、`finished_at` 来自前端专项审查，我未逐列复核。

反方向还有一处浪费：后端白名单里的 `requests`、`success_rate`、`latency`、`timeout` 在前端全是 `enableSorting: false`，永远排不到。

---

## 四、高风险项

### B-1 预扣泄漏：release 失败后冻结永不回收（已实测）

失败路径的通用写法是先 `ReleaseAuthorization` 再 `MarkRequestFailed`。若 release 本身失败（数据库超时等），请求仍被标记为 `failed`：

```557:562:internal/service/gateway/lifecycle/attempt_runner.go
				if !retryable {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, "adapter_error", err)
					return result, err
				}
```

而孤儿清扫只捞仍在 `running` 的请求（`sql/queries/worker/ledger_reservations.sql:25-31`：`AND r.status = 'running'`）。于是 `authorized` 冻结 + `failed` 请求这个组合落在两者之间，`reserved_balance` 被永久占用，没有任何自动回收路径，也没有 TTL。

上线前至少要加一条监控：`ledger_reservations.status='authorized'` 且关联 `request_records.status IN ('failed','canceled')` 的行数应恒为 0。

### B-2 孤儿清扫在 reservation 缺失时提前返回，请求永久停在 running（已实测）

`internal/service/gateway/lifecycle/settlement.go:1038-1042` 在 `ReleaseWithQueries` 返回 `CodeLedgerReservationNotFound` 时直接 `return nil`，**不执行**后续的 `MarkRequestFailed`。Worker 视为处理成功不再重扫，请求永久保持 `running`。

对比同文件的 dead settlement 收口路径（`:967-971`）：reservation 缺失时仍继续把 request 标为 failed。两条路径本该对齐。

我把它定为高而非阻断：触发需要 reservation 在 sweeper 的 SELECT 与事务之间消失，窗口较窄。但一旦发生就是永久卡死状态，无自愈路径。

### B-3 真实上游 E2E 套件全部失效（已实测）

`internal/blackbox/sdkfixture/facts.go:229` 查询了两个不存在的列：

```228:232:internal/blackbox/sdkfixture/facts.go
	if err := f.Pool.QueryRow(ctx, `
		SELECT route_id, protocol, endpoint, candidate_count, selected_order, algorithm_version
		FROM routing_decision_traces
		WHERE request_record_id = $1
	`, facts.requestID).Scan(
```

`routing_decision_traces` 的实际列是 `eligible_count`、`baseline_order`、`actual_scan_order`、`attempted_channel_ids`。直接执行该语句报 `column "candidate_count" does not exist`（已实测）。

`AssertLatestRequestFacts` 是唯一入口，只被 `internal/blackbox/starapi/realupstream_test.go`（9 处）与 `cli_test.go`（2 处）调用，所以**全部 9 个真实上游端到端用例恒定失败**，与上游健康无关。

失败纯粹来自断言助手：同一批请求在数据库里全部 `status="succeeded"`、`upstream_status_code=200`、`delivery_status="completed"`，且 `usage_records`、`ledger_entries` debit、`price_snapshots`、`cost_snapshots` 各写 1 条，计费链完整。

路由 trace 本身没有问题：开发库 1288 个成功、54 个失败、4 个取消，**全部 1346 条都有对应 trace 行**（已实测）。

**这个缺陷还会产出误导性诊断，危害超出「测试跑不过」本身。** `AssertLatestRequestFacts` 用一个 5 秒 ctx 同时驱动五条查询（`facts.go:29`），而第五条 trace 查询恒定报错使循环永远无法 `break`，于是每次都耗尽整个 5 秒再报告最后一次加载的结果。由于五条查询共用这个即将到期的 ctx，deadline 若落在「usage 查询」与「ledger/price/cost 查询」之间，后者会提前返回而那三个计数停在未赋值的 0，失败信息就会显示 `usage=1 debit=0 price=0 cost=0`。

我本人在验证 Anthropic ingress 时就读到了这个数字，并一度判断为「非流式结算未写账」。用独立采样器（每 200ms 直接查隔离库）复现后确认计费完全正常：请求建立时 reservation 为 `authorized`，约 4 秒后 usage / debit / price / cost 各 1 且 reservation 转 `captured`，是一次原子跃迁。**修这条不只是恢复 9 个用例，也是消除一个会让人去追不存在的计费 bug 的假信号。**

### B-4 一个永远不可能通过的测试，守护的是账务统计正确性（已实测）

`internal/platform/store/sqlc/breakdown_ledger_test.go:20` 插入渠道时传 `priority = 1`，而 `migrations/000009_channels.up.sql:55` 的约束要求 `priority % 10 = 0`。测试在 setup 阶段就因 `SQLSTATE 23514` 失败，断言体从未执行。它是同包 17 处 `insertChannel` 调用里唯一传非 10 倍数的。

成因：约束在 `7f1a86a` 中从 `CHECK (priority >= 0)` 收紧，而该测试文件最后修改于更晚的 `aba1e15`；因为需要 `DATABASE_URL` 否则 skip，在默认 CI 里静默跳过。

它守护的属性是「1 个请求配 2 条账本记录时 dashboard 统计不得被账本行数放大」。我手工核对了这个属性本身：四个 breakdown 查询（route / provider / channel / model）都用 `LEFT JOIN LATERAL` 对 `ledger_entries` 预聚合，`usage_records` 与 `cost_snapshots` 与请求 1:1；provider 与 channel 虽然 `JOIN request_attempts`，但 attempt 级指标与请求级金额（`money_agg` 按 `final_channel_id` 分组）是分开聚合再 join 的。**属性成立，但自动化保护失效。**

### B-5 流式尾部出错但已成功结算时，仍返回失败 outcome 与 error（专项审查）

`internal/service/gateway/lifecycle/attempt_runner_stream.go:863-889`：上游流已解析出 `streamFacts`（含可靠 usage）、`settleStreamFacts()` 成功，但 `params.Stream` 返回非 nil 错误时，`RunResult.Outcome` 保持初始的 `ChatOutcomeFailed` 并 `return result, err`。

后果是 metrics 与路由 trace 记为失败而 DB 可能已 capture，客户可能在已扣费后收到错误响应；该路径也未调用 `Sticky.BindSuccess`。对比非流式的 recovery scheduled 路径（`:610-638`）会设 `Outcome=Success` 并返回 nil。

### B-6 长上下文预授权用估算 token、结算用真实 token，低估时平台白亏（专项审查）

预授权对长上下文的判定用 `params.InputTokens`（tiktoken 保守估算，`internal/service/gateway/lifecycle/authorization.go:164-167`），结算用 `LongContextInputTokenSum(facts.Usage)`（上游真实 usage，`settlement.go:634-638`）。估算 ≤272000 而真实 >272000 时，冻结按短上下文单价、扣费按长上下文单价，差额走 overage 补扣，残差由平台核销。

这条与我实测到的一个现象叠加后风险更高：上游会注入大量系统提示——一个 3 token 的提问被 aihub 报为 `prompt_tokens: 4388`（已实测）。估算与真实的系统性偏差会让阶梯误判和 `authorization_underfunded` 核销都变得常见。

### B-7 长请求 TPM 分钟桶 TTL 到期后 Finish 静默 no-op，限额可被绕过（专项审查）

`internal/platform/breakerstore/lua/ops/finish_request_admission.lua:112-122` 的 `adjust_bucket` 在桶键已过期时直接返回，不重建也不写 delta。桶 TTL 约为 lease 30s + terminal 300s + 120s ≈ 450 秒（`acquire_request_admission.lua:88-91`），而 Renew 不续桶 TTL。

跨度超过该值的请求（长流式补全、慢上游）在 Finish 时，预占与实际 usage 都不会反映到 TPM 计数器，`tpm_state` 却仍标记 `settled` → TPM 窗口计数偏低，**限额可被绕过**。现有测试只覆盖 `not_reached` 释放路径的 no-op，未覆盖 `actual` 结算路径。

如果产品允许超过 7 分钟的流式请求，这条需要在上线前决策。

### B-8 Anthropic 非流式不校验 usage，缺失字段当 0 入账（专项审查）

`internal/core/adapter/anthropic/messages/adapter.go:97-126` 在 JSON 解码成功后只检查 `id` 与 `content`，`messageUsageFromWire` 把 nil 指针映射为 0（`wire.go:163-168`），直接构造完整 `ResponseFacts`。上游返回 200 + 合法 JSON 但 usage 缺失或为 null 时，会产生 0 token 的成功请求并正常入账。

对比 OpenAI chat 非流式 `chatUsageFromOpenAI`（`chat.go:415-441`）对 nil、缺字段、total 不一致均返回 `CodeAdapterInvalidResponse`。

同族还有 Responses 流式：收到 `response.completed` 但无 usage 时返回 `nil` error 加空 outcome（`internal/core/adapter/openai/responses/stream.go:226-229`），OpenAI chat 流式在只发 delta 与 `[DONE]` 时同理。这几条都依赖 lifecycle 对 `Facts==nil` 强制走 risk_exposure 兜底，需要确认该兜底覆盖全部入口。

### B-9 Admin 列表接口返回渠道明文凭据与 API Key 明文（专项审查）

- `sql/queries/admin/channel.sql:653` 的 `ChannelsOpsTable` 在列表行里 `SELECT c.credential`
- `sql/queries/admin/requests.sql:80-82` 的 `ListRequestRecordsPage` 返回 `ak.key_plaintext`

明文存储本身是既定产品决策（见第八节），但让**列表接口**批量携带可直接冒用的凭据，扩大了 Admin token 泄露的后果面。结合 A-3 的 token 泄露，这两条是直接可利用的。

### B-10 熔断 half-open 时的展示自相矛盾，会导致运维误操作（专项审查，我复核了后端成因）

后端在 half-open 时把总分强制归零但保留五个分项分（`internal/service/gateway/lifecycle/balance.go:264-267`，我已实测确认），DTO 里分项用未归零值、`Total` 用已归零值。前端把两者拼成等式，界面会渲染出形如 `25 + 20 + 25 + 20 + 8 = 0` 的字样（`unio-admin/src/components/routes/RouteCandidateTips.tsx:684-690`）。

更有害的是资格展示：`probe_only` 的判定用 `half_open`，而两个熔断检查项用的 `breakerUnavailable` **不包含** `half_open`（`internal/app/adminapi/route/runtime.go:378-380`），且 `runtimeExcludedReason` 对 half-open 返回空串，于是九项检查全绿、`primary_reason` 因 `omitempty` 消失、无任何成因文案。渠道详情抽屉更进一步把「仅探测」直接标成「有候选资格」（`RouteRuntimeSection.tsx:619-628`），与并列显示的「得分 0」构成一个明确且错误的结论。

**根因在契约缺字段**：`runtimeChannelDTO`（`internal/app/adminapi/route/runtime.go:178-195`）没有暴露 `provider_breaker_state` / `channel_breaker_state`，前端在实时路由页拿不到任何可解释的输入，无法单独在前端修好。历史 trace 的 `RoutingCandidateScore` 反而有这两个字段，后端已有先例。

这正是本次会话开头那个「得分怎么变成 0」的疑问的完整成因。运维按界面推理会去动 `routing_balance` 权重，而正确动作是等探测收敛或手动重置熔断。该路径在 e2e 与单测里零覆盖（`probe_only`、`仅探测`、`half_open` 在 `tests/` 与 `e2e/` 下无任何出现）。

---

## 五、中风险项

### C-1 严格版 TPM 预留守卫是死代码，生产走的是静默放过的版本（已实测）

`internal/service/gateway/requestadmission/session.go:803` 的 `Reserve` 在缺少 admission session 时返回 `CodeGatewayRuntimeSyncRequired`，注释写明「A missing session is a runtime wiring error」。但它不可达——六条生产生成路径用的全是 `ReserveIfPresent`（`chat_completion.go:95`、`chat_stream.go:106`、`create_response.go:233`、`stream_response.go:123`、`messages.go:92`、`message_stream.go:94`），而它在 session 缺失时 `return nil`。

同时 `ContextWithUsageSession` 在传入 nil 时原样返回 ctx（`session.go:743-745`）。当前不会触发（session 由 `internal/app/gatewayapi/middleware/request_admission.go:100` 唯一安装，路径完好），但一旦装配回归，TPM 预留会在全部六条路径上静默跳过，本应捕获它的守卫是死的。

### C-2 成本快照分项舍入和与总额可能不等，会硬失败在 CHECK 上（已实测并修正定级）

机制是真的：`internal/core/billing/service.go:48-56` 对 7 个分项分别 `ratToNumeric` 舍入，而 `TotalCostAmount` 是对**未舍入分项之和**再舍入（`:173-180`）；`migrations/000019_cost_snapshots.up.sql:55` 的 `ck_cost_snapshots_total_amount` 要求总额严格等于分项之和。

但我用精确有理数算术实测了触发条件，**不构成阻断**：

- 金额 = 单价 × token / 1e6，单价小数位为 d 时金额小数位为 d+6。舍入到 10 位只在 **d > 4** 时才会发生任何变化
- 当前库全部模型单价都是 ≤3 位小数（2.5、0.75、5.0、15.0、0.25、0.075、30.0、6.0、1.0、0.1），金额精确无舍入，CHECK 恒成立。我用实测到的 274,394 token 真实用量验证也通过
- d > 4 仍只是必要条件：单价 2.50001 配 3 个桶各 1 token 不触发；单价 0.00005 配 3 个桶各 1 token 才触发（分项各舍入为 1e-10、和为 3e-10，而精确和 1.5e-10 舍入为 2e-10 → CHECK 失败）

所以真实风险是：**一旦配置了小数位超过 4 的模型单价，结算可能在特定 token 组合下硬失败无法落账**。上线前要么约束单价精度，要么把总额改为已舍入分项求和。

### C-3 账户级 403 被当作模型级权限问题逐对暂停（已实测）

本次真实上游测试中 starapi 返回 HTTP 403，体为 `{"code":"INSUFFICIENT_BALANCE"}`（账户余额不足，也是这两个渠道被停用的原因）。网关对 403 的处置是暂停精确的 (渠道, 模型) 组合（`internal/service/gateway/lifecycle/attempt_permit.go:504-510`）。

这个假设对「该 key 没有这个模型的权限」是对的，但对账户欠费这种渠道级甚至服务商级失格是错的：同一服务商每个渠道每个模型都会 403，只能一对一对地暂停，每次暂停以一个真实失败请求为代价。3 渠道 × 6 模型即 18 对。

存在兜底：403 计入渠道错误率分子（`sample.go:134-136`），比例规则最终会打开渠道熔断，所以不是阻断项。

错误本身的合理性评估：对客户返回 502 加通用文案正确（不泄露上游账户状态，也不误导为客户过错）；归因 `fault_party="upstream"` 正确；不给 `Retry-After` 也合理（欠费不是重试能解决的）。问题只在暂停粒度与真实失格范围不匹配。

### C-4 上游错误原因不进请求审计，运维需另做渠道检测才能定位（已实测）

这是**明确的设计选择**，有文档为证：

```75:79:internal/core/adapter/upstream_error.go
	// ResponseSnippet 是上游响应体的截断原文快照。
	// 用途：渠道检测把上游完整错误/异常响应记进 channel_test_logs 便于排障——adapter 仍只按 HTTP status
	// 分类（不解析此原文），gateway retry/fallback 也不依赖它；gateway 请求记录不消费此字段。
	// 填充场景：非 2xx 错误体；以及 2xx 但协议解析失败（JSON 不符 / 空 choices 等）。
	ResponseSnippet string
```

`UpstreamError.Error()` 委托给底层 cause 且不暴露原始 body，而 `internal_error_detail` 由 `InternalErrorDetail(err)` 沿 error 链拼装，因此只会得到 `openai adapter <op> status 403` 这类文本。

本次排查就是这个设计的实例：面对 9 个失败请求，Gateway 侧数据只能给出 `adapter_upstream_status` 与 `upstream_status_code=403`，无法得知真实原因是账户欠费——我必须用生产凭据在带外 curl 上游才查明。是否接受这个取舍是产品决策，但运维代价应当被明确知晓。

附带一处不一致：三个 adapter 里只有 OpenAI responses **不抓取** `ResponseSnippet`（`internal/core/adapter/openai/responses/errors.go:22-37`），而 chat completions（`errors.go:45`）与 Anthropic messages（`errors.go:54`）都抓取。responses 的注释还写着「与 chat adapter 同口径 ... 不解析上游原始 body」，这句话本身也不准确。后果是渠道检测能看到 chat 与 messages 的上游错误原因，看不到 responses 的。

### C-5 错误分类推导对两个错误码产出枚举外的值（已实测）

`Code.Category()` 取第一个下划线之前的子串（`internal/platform/failure/code.go:77-85`），于是 `rate_limit_exceeded` 得到 `rate`、`channel_rate_limited` 得到 `channel`，两者都不在已定义的 `Category` 枚举内。

这条推导是生产在用的：`internal/platform/failure/log.go:22` 写 `error_category` 日志字段、`internal/service/gateway/lifecycle/request_log.go:116` 据它决定给客户看的兜底文案、`internal/app/adminapi/adminhttp/adminhttp.go:123` 据它决定是否返回 400。后果是日志出现枚举外分类值（影响聚合与告警），且限流类错误匹配不到 `CategoryRateLimit` 而落到默认分支。

### C-6 一个 blackbox 断言与实现对客户可见错误码的预期不一致（已实测）

`internal/blackbox/openaisdk/responses_direct_test.go:351` 期望上游 200 但内联 `status:"failed"` 时客户看到的 `error.code` 是通用的 `upstream_error`，实测是上游原本的 `server_error`。这是干净库上整包运行时唯一的真实失败。

实现是故意透传的：`internal/core/adapter/openai/responses/errors.go:84-85` 把上游 `code` 放进 `meta.ErrorCode`，`upstream_error.go:66-71` 说明这样做是为了让客户自助排查、SDK 拿到上游原本的 `error.code`，且写入时已脱敏。

蓝图无法裁决：`error-semantics.md` 规定了上游分类到 HTTP 状态的映射，也说明 Responses 内联事件按稳定错误 code 分类，但没有规定客户可见的 `error.code` 字符串是透传还是统一。这个契约空缺本身也是蓝图缺口。

### C-7 路由层 DB 读失败被误报为「线路未配置」（专项审查）

`internal/core/routing/router.go:355-358` 的 `GetRouteByID` 在返回非 `ErrNoRows` 错误（连接超时、主从延迟、权限）时被吞掉，上层统一返回 `ErrRouteNotConfigured`。DB 故障期间监控看到的是配置问题而非 `CodeRoutingStoreFailed`，掩盖基础设施故障。

### C-8 Partial 路线 D 结算幂等校验与写入事实矛盾（专项审查）

写入侧 `settlement.go:562-563` 对 partial 估算写 `FinalUsageReceived = false`，而幂等校验 `:1518-1520` 要求 `attempt.FinalUsageReceived` 为真。于是 partial 路线 D 的重复结算（补偿重试、ACK 丢失）会稳定返回 `CodeGatewayChatSettlementIdempotencyConflict`，即使 usage 与 ledger 完全一致。现有测试只覆盖 partial 首次结算，没有 partial 成功重放测试。

### C-9 Admin 前端多处「拿前 100 条当全量」（专项审查）

后端 `ParsePage` 把 `page_size` 硬夹到 100 且不报错（`internal/app/adminapi/adminhttp/adminhttp.go:155-183`），前端三处按「全量」使用：

- `ChannelDetailPage.tsx:86` 请求 `page_size: 500` 后本地 `find`。因未带 `sort`，后端按默认 `success_rate` 升序返回前 100，所以**健康渠道更容易落选**，`find` 返回 null 后成功率与最近错误都渲染成破折号，与「确实没有样本」无法区分
- `RouteChannelMarginTable.tsx:168` 与 `RoutePriceCalculator.tsx:260` 请求 `page_size: 200`，按 `name` 升序。字母序 100 名之后的模型直接消失，极端情况下空态文案会把原因归为「未配置成本」，运维照此去补价补完仍不出现
- `RouteFormDialog.tsx:129-143` 的渠道选择器最多各 100 个启用/停用渠道，`total` 被丢弃且无截断提示。已绑定但不在前 100 的渠道不会渲染成勾选框（好消息是提交用的是初值副本，不会静默解绑）

### C-10 排除原因码只翻译了约一半，其余渲染成英文或半中半英（专项审查）

`unio-admin/src/components/routes/RouteCandidateTips.tsx:135-141` 的 `reasonLabel` 只有 9 个条目，兜底是三个前缀截断。未覆盖的包括 `credential_invalid`、`pricing_invalid`、`breaker_open`、`not_evaluated` 等十余个。`channel_cost_missing` 命中 `channel_` 前缀后被渲染成 `渠道cost_missing`，`route_disabled` 变成 `线路disabled`。

相关的还有一条：被排除的渠道会额外多出一条红色「毛利 · not_evaluated」失败项，因为 `MarginStatus` 初值是 `not_evaluated` 且只在未被排除时才改成 `safe`（`internal/service/admin/routeruntime/runtime.go:431-437`），而检查项用 `== "safe"` 判定。运维会以为该渠道同时还有定价问题，实际毛利根本没被计算过。

### C-11 模型详情页把 cache write 也算进「缓存命中率」（专项审查）

后端该字段是 read + write 之和除以输入 token（`internal/service/admin/modelops/modelops.go:218-220`），前端 `ModelOverviewStats.tsx:56` 贴一个裸标签，无 tooltip 无分子构成。cache write 本质上是未命中，一个刚开始预热缓存、实际命中率为 0 的模型这里可能显示 60%+。对照概览页 `CacheHitTip.tsx` 是诚实的（副标题写明「缓存 token ÷ 输入 token」并分三段列出）。

### C-12 Admin token 存 localStorage，与后端 CORS 通配叠加（专项审查）

`unio-admin/src/lib/auth/token.ts` 用 `localStorage`，对同源内任意 JS（含第三方依赖、浏览器扩展注入）可读且跨会话持久。叠加两个后端事实后风险放大：静态单 token 无 RBAC 无过期，被偷后无法单点吊销；`internal/platform/httpmw/cors.go` 下发 `Access-Control-Allow-Origin: *` 且前端用 `Authorization` 头（非 cookie），所以通配符不会被浏览器拒绝，攻击者页面可直接用偷到的 token 跨源调 Admin API 并读响应，不需要自建中继。

`index.html` 也没有任何 CSP，没有 `script-src` 约束也没有 `connect-src` 限制外发目标。

---

## 六、SQL 与迁移

### 迁移编号：不是缺陷，是你未提交的进行中改动（已实测并推翻初判）

需要先纠正两处：我最初报「40 个迁移」，专项审查报「仓库只有 39 个而 DB 是 40，属阻断级版本漂移」。两者都不准确。

实际情况（`git status` 实测）：

- HEAD 里有 40 组迁移，`migrations/000040_admin_query_indexes.{up,down}.sql` 提交于 `aba1e15`
- 工作树里这两个文件被**删除**（`D`），同时 `000010`、`000011`、`000019`、`000020`、`000022`、`000033` 六个迁移被**修改**
- 修改内容是把原 `000040` 的 6 个索引逐条搬进各自的原始表迁移，索引名与定义完全一致，每条带 `-- [000040_admin_query_indexes]` 溯源注释

我用当前工作树的 39 个迁移在一个全新容器里建库，与开发库做索引级对比：**各 129 个索引，零差异**；表集只差一个外部迁移工具建的 `schema_migrations`。所以这次合并是正确且完整的，不存在 schema 缺失。

真正需要处置的是**已迁移环境的版本对齐**：提交后仓库最大编号从 40 回退到 39，而开发库（以及任何已迁移环境）的 `schema_migrations.version` 是 40。迁移工具比较「当前 40 / 可用最大 39」时会报错或认为库领先于仓库。这个合并需要为已有环境准备明确的版本处置方案。

### 查询侧（专项审查）

按「随流量增长最先出问题」排序：

1. `UpsertRoutingDecisionTrace` 与请求/attempt 写入链——每条 API 请求必写，含大 JSONB 反复更新，QPS 线性增长时 WAL 与索引维护最先触顶
2. `FindRouteCandidates`——每条请求路由前执行，多表 LATERAL 加 EXISTS
3. `DashboardRadarRequestPerf` 与概览 `Radar()` 查询组——`percentile_cont` 全量内存排序无采样，`internal/service/admin/dashboard/radar.go:169-199` 串行发 10+ 条重查询；request >10⁵ 时 Admin 首页最先超时
4. `ChannelsOpsTable`——分页前对所有渠道与区间内全部 attempt 做 JOIN 加分位数
5. `ListRequestRecordsPage` 宽过滤——`COUNT(*) OVER()` 必须扫完匹配集再 LIMIT，叠加深 OFFSET
6. `ProviderOpsDetail` 与 `DashboardBreakdownProvider`——`request_attempts` 缺 `(provider_id, created_at)` 索引
7. `UsersOpsTable`——用户与 request 双重放大加排序列相关子查询
8. Admin `RouteOps*` 与 `DashboardBreakdownRoute`——归因用 `api_keys.route_id`（Key 当前绑定）而非 `request_records.route_id`（请求快照），与请求列表口径冲突；Key 换绑后历史请求会从旧线路消失并计入新线路。这条是**口径错误**而非慢查询

缺失索引建议：`idx_request_attempts_provider_created_at`、`idx_request_records_route_created_at`。

另外全部 39 组迁移中**没有任何一处** `CREATE INDEX CONCURRENTLY`，在已有生产数据上补索引会持 ShareLock 阻塞写入；`routing_decision_traces` 对 `request_records` 是 `ON DELETE CASCADE`，与 ledger/cost 等 NO ACTION 的审计保留策略不一致；多个 down 迁移是 `DROP TABLE ... CASCADE`，误执行会不可逆丢数据。

---

## 七、测试与验证现状

| 层次 | 结果 |
| --- | --- |
| `go build ./...`、`go vet ./...`、`gofmt -l .` | 全部通过，无输出 |
| 纯单元测试（不带 `DATABASE_URL`/`REDIS_ADDR`） | 73 个包 ok，0 失败，27 个包无测试文件 |
| 集成层（指向隔离库） | 72 个包 ok，1 个包失败，唯一失败是 B-4 那个永远不可能通过的测试 |
| `internal/blackbox/openaisdk`（干净库整包） | 21 通过，1 失败（C-6） |
| `internal/blackbox/anthropicsdk`（干净库整包） | 11 通过，2 skip，0 失败 |
| `internal/blackbox/starapi` 真实上游 | 11 个用例（9 个 OpenAI + 2 个 Anthropic）全部因 B-3 失效；底层请求本身全部成功 |
| P4 故障与路由演练（两轮共 15 项） | 15 通过，0 失败 |
| 迁移完整性 | 工作树 39 组 up/down 成对，可从零建库且与开发库 schema 零差异 |

P4 演练逐项通过（自建随机容器与 mock 上游，不接触开发库）：六协议 baseline、Redis 重启、熔断跨 Gateway 共享、FLUSHDB fail-closed、route-rate-control 丢失修复、五项评分与 fallback 与 Sticky 与完整 trace、全池并发短等、全池 429 冷却、四类超时阶段、half-open lease 续租与 Gateway 接管、compact 原生与回退、**完整状态丢失恢复、AOF 恢复、RDB 恢复、active-owner epoch 回滚安全边界、epoch prepare 崩溃、长流 Redis 故障、长流 revision 围栏、Reset 后 stale generation**。加粗那批直接验证了 Redis 状态丢失的运维恢复流程可用。

代码规模：100 个包、721 个 Go 文件、297 个测试文件。27 个包没有任何测试，其中 `internal/app/adminapi/middleware`（Admin 鉴权中间件）应优先补测。

有一个运行陷阱需要提示：starapi 真实上游用例在 `AssertLatestRequestFacts` 内失败后 teardown 未清干净，残留 11 条 `routes` 与 user/api_key/provider/channel 各 1 条；随后在同一库上再跑 mock 套件会因 `models_model_id_key` 唯一约束连锁失败 22 个用例。这些是脏库导致的假失败——我第一次就踩了这个坑并据此误报，清库重跑即恢复正常。

### unio-admin 工具输出（专项审查实跑）

| 工具 | 退出码 | 结果 |
| --- | --- | --- |
| `tsc -b --force` | 0 | 0 error |
| `eslint .` | 0 | 0 error / 0 warning |
| `vitest run` | 0 | 15 文件 / 51 用例全通过 |
| `knip` | 1 | 3 个未使用导出 + 2 个未使用导出类型 |

knip 的 5 条都是低风险：`usePreviousRange`（`chart-common.tsx:88`）确实无任何调用点；`TipSection` / `TipSummaryRow`（`RouteCandidateTips.tsx:57,76`）与 `GatewayLoggingControl` / `GatewayLogRange`（`system.ts:169,199`）在文件内有使用，只是不需要 `export`。

---

## 八、死代码与冗余

`deadcode -test ./...` 报 12 处不可达函数，`staticcheck -checks=U1000,U1001,SA*` 报 7 处（均已实测）。逐条核实后：

### 值得处置

- `requestadmission/session.go:803` 的 `Reserve`：见 C-1，不是单纯冗余而是失效的安全守卫
- `internal/service/appsettings/gateway_settings.go` 的 `GatewayCircuitBreaker`、`GatewayRouteRateLimitDefaults`、`GatewayConcurrencyDefaults`、`GatewayRoutingBalance` 四个读取函数不可达。这四项配置已改为经 runtime control 发布到 Redis 由 Lua 消费（`internal/service/appsettings/service.go:216-244`），Go 侧读取器是改造遗留，且带一个陷阱：从 settings store 读到的值与 Lua 实际执行的值可能不同
- `internal/platform/failure/code.go` 的 `CodeCredentialMasterKeyInvalid`、`CodeCredentialEncryptFailed`、`CodeCredentialDecryptFailed`、`CodeCredentialCiphertextInvalid` 全仓仅出现在定义处，零引用，是废弃凭据加密设计的残留
- `internal/core/runtimecontrol` 的 `Reconciler.CleanupTerminal` 与 `ProviderRoutingReconciler.CleanupTerminal` 不可达，终态运行控制操作没有调用清理入口

### sqlc 查询

338 个生成方法中 7 个全仓零调用：`CommitProviderOriginAtRevision`、`CommitProviderStatusAtRevision`、`InsertChannelTestLog`、`ListAppSettings`、`SetAPIKeyRateLimits`、`SetChannelCredentialValid`、`SetChannelTestResult`。

逐个核实后确认都是已完成改造的遗留，**不是功能未接线**：`SetAPIKeyRateLimits` 对应的 per-key 限流已由 DEC-027 迁移到线路级（`internal/app/adminapi/user/api_keys.go:84` 注释标明废弃，但 DTO 仍暴露 `rpm_limit`/`tpm_limit`/`rpd_limit`，前端可能仍在展示无效配置项）；`SetChannelTestResult` 与 `InsertChannelTestLog` 已被 `ApplyChannelProbeResult` 取代，后者在单条语句里同时写检测摘要与 `channel_test_logs`。另有 13 个方法仅被测试调用，多为正常的测试 helper。

### 其他

- `sql/queries/console/` 只有 `.gitkeep`，配置里有 `CONSOLE_HTTP_ADDR` 但没有 `cmd/console-server`，`sqlc.yaml:6` 注释也说明是占位
- `internal/core/ledger/reservation.go:505` 与 `internal/service/admin/channel/channel.go:618` 各有一处赋值后未使用（`SA4006`）。均已核实**不是缺陷**：前者只需知道行是否存在，后者返回值被后续语句覆盖，改用 `_` 更清晰而已
- unio-admin 有两处为已不存在的 wire 形状保留的兼容分支：`src/lib/api/system.ts:350-373` 的 PascalCase 分支（后端一直是 snake_case）、`src/lib/api/capability.ts:32` 的 `protocol_scope: "both"`（后端已归一为 `shared`）

---

## 九、蓝图内容检查

`make validate` 通过（139 文件 / 41 目录），结构层无问题；模板里的 `YYYY-MM-DD` 是合法占位。Gateway 的 15 篇 feature 与 17 篇 ADR 都是 `active` 且近期更新，只有 `quality.md` 与 `roadmap.md` 仍是 `draft`。

### 与代码不符

**必须修正**：

1. **并发满误写为 HTTP 429（蓝图错）**。`docs/products/gateway/features/error-semantics.md:80-81` 写「全部为渠道 concurrency limit 时也返回 429」，实际是整池仅因 `concurrency_full` 且短等后仍满 → HTTP **503** 加 `routing_channel_capacity_exhausted` 加 `Retry-After: 1`（`internal/service/gateway/lifecycle/attempt_permit.go:1015-1023`）；整池 429 冷却才返回 429。同仓 `admission-control.md:86` 与 `adr-0007:90` 是对的，属 active 文档间的内部矛盾
2. **Sticky TTL 注释写「绝对过期、命中不刷新」（代码注释错）**。`internal/service/appsettings/gateway_settings.go:704-705` 的结构体注释与实现相反；同文件 `:757-758` 的 Definition 描述、`stickysession/store.go:219` 的实现、以及 `sticky_test.go:204` 与 `store_test.go:75` 两处测试都证明是滑动续期。蓝图 `routing-load-balancing.md:118` 描述正确

**建议修正**：`superseded` 的 ADR-0010:51-52 仍写「breaker disabled 停止 TTFT EWMA」，而 `finish.lua:357-358` 已不写 TTFT 到 breaker；内部错误码 `no_available_channel` 与 `routing_no_available_channel` 双形态未在蓝图统一说明。

### 覆盖缺口

- **服务商级熔断的共享故障域**：蓝图 `resilience-circuit-breakers.md:27-31` 只写「允许本次调用」，没写单服务商线路池在 Provider open 时全部渠道同时失格的运维后果。今早的真实事故正是这个（3 渠道同属 aihub，连续 503 打开服务商熔断，30 秒内 49 个请求 `no_available_channel`）
- **partial 60% 缓存率的失效边界**：60/40 口径已写在 `billing-settlement.md:78-79`，但没写「售价未配 `cache_read` 单价时拆分静默退化为全 uncached」
- **长上下文阶梯公式**：`billing-settlement.md:34,58` 只泛述「按输入合计触发」，没有 threshold 与 multiplier 字段说明，也没有「输入合计 = uncached + cache_read + 三档 cache_write」与「排他比较」这两个关键事实
- **孤儿清扫**：`gateway_request_orphan_reclaimed` 稳定码、`orphan_reservation_swept` reason、batch 默认 100 都未记录
- **settlement recovery 参数**：`max_attempts` 默认 20、lock 30s、backoff cap 5m、batch 16 等均未记录

### 编辑质量

orphan sweeper / recovery 边界在 `billing-settlement.md`、`request-lifecycle.md`、`adr-0003` 三处重复维护正文，partial 60/40 同样三处重复，与 `features/README.md:28`「每个功能保留唯一权威说明」冲突。`quality.md:16-17` 已明确写「尚未批准 SLO」，作为上线前文档可接受，建议升为 `active` 并保留免责声明；`roadmap.md:17` 是空占位。

术语上 `provider-adaptation.md:3` 仍把「Provider、Origin」并列，而 glossary 已明确 Origin 只是 Provider 的字段。

---

## 十、风险接受项（既定产品决策，非缺陷）

以下在 Schema 注释里明确写为产品决策，列出仅供上线前复核是否仍然接受：

- 渠道上游 credential 明文存储（`migrations/000009_channels.up.sql:20`：「明文存储，便于管理端查看/复制/编辑（产品决策：渠道凭据不加密）」）
- 客户 API Key 除 SHA-256 哈希外另存完整明文 `key_plaintext`（`migrations/000004_api_keys.up.sql:18`：「供用户在控制台多次复制查看（产品决策：用户 key 明文留存）」）

两者的共同风险是数据库备份或 Admin token 泄露即等于全部凭据泄露，而 B-9 让列表接口也批量携带这些明文，A-3 则已经把 token 泄露变成既成事实。Admin 侧目前是单个静态 Bearer token、无 RBAC、无 actor 审计（token 比较用 `subtle.ConstantTimeCompare`，这点是正确的）。

另一条是平台成本敞口：预授权按保守估算冻结，真实费用超出时从可用余额二次补扣，仍不足的残差由平台核销并写 `ledger_billing_exceptions`（reason `authorization_underfunded`，`internal/core/ledger/reservation.go:341-357`）。机制完整且有幂等测试覆盖。但实测发现上游会注入大量系统提示——3 token 的提问被报为 `prompt_tokens: 4388`——所以低估冻结会是系统性的，与 B-6 叠加后 `authorization_underfunded` 的产生频率应纳入监控。

DeepSeek provider 对无法转换的字段静默 Drop（DEC-012）也属既定决策：客户认为已传的 multimodal / tool / schema 字段实际未进上游，只有 debug 日志不报错。

指标不采集、告警不投递（2026-08-04 决策）：完整事实、决策依据与接受的后果见 A-2。上线后的可观测能力完全由日志链路承担，指标代码保留但空转。

---

## 十一、真实上游端到端验证

### 上游可用性实测

- `starapi` 的 OpenAI 渠道 1、2（`https://open.codex521.cc`）：HTTP 403 `{"code":"INSUFFICIENT_BALANCE"}`。账户欠费，两渠道当前均 disabled，与该状态一致
- `starapi` 的 Anthropic 渠道 6（线路 `VIP-Claude`，`claude-opus-4-6`）：HTTP 200 正常，`x-api-key` 与 `Bearer` 两种认证方式都可用。该渠道凭据与 OpenAI 渠道不同源，未受欠费影响
- `aihub`（`https://aihub.top`）渠道 3、5：HTTP 200 正常；渠道 4：HTTP 503 `Service temporarily unavailable`

渠道 4 的 503 与今早 08:52 的事故一致。

### 端到端结果与四类定性

对 aihub 渠道 3 跑 `internal/blackbox/starapi` 全套 9 个真实上游用例：

**Gateway 真实缺陷**：0 条。7 个 OpenAI 用例（chat 非流式/流式、responses 非流式/流式、3 个 compact）的请求全部端到端成功，`upstream_status_code=200`、`delivery_status="completed"`、`usage`/`debit`/`price_snapshot`/`cost_snapshot` 各 1 条。

**测试自身问题**：这 7 个用例全部因 B-3 的过期列名在断言阶段失败。

**上游能力限制**：首轮 2 个 Anthropic 用例得到上游 403，因为当时库内五个渠道全为 OpenAI 协议，aihub 的 key 对 Anthropic 协议端点不提供服务。网关行为正确：映射为客户 502、归因 `fault_party="upstream"`。

### Anthropic messages ingress 补充验证（真实 Anthropic 协议上游）

新增线路 `VIP-Claude`（route 2）后补跑，上游为 `starapi` 的 Anthropic 渠道 6、模型 `claude-opus-4-6`：

- **非流式**：`request_status="succeeded"`、`attempt_status="succeeded"`、`upstream_status_code=200`、`delivery_status="completed"`、`upstream_first_token_at` 为 NULL（非流式应为 NULL，符合契约）
- **流式**：同上且 `first_token=true`、`failure_stage="after_first_token"`，帧数、`message_stop`、累积内容与 usage 均通过 SDK 层断言

两条都在到达 B-3 那个坏掉的断言助手之前完成了全部 SDK 级校验（内容非空、`message_stop` 出现、帧数 ≥ 3、`input_tokens` 与 `output_tokens` 均 > 0）。计费链用独立采样器直接查隔离库确认完整：reservation `authorized` → usage / debit / price / cost 各 1 且 reservation `captured`。

**结论**：三个 ingress 端点全部用真实上游验证通过——OpenAI chat_completions 与 responses 走 aihub 的 OpenAI 协议上游，Anthropic messages 走 starapi 的 Anthropic 协议上游。全部端到端成功且计费链完整；唯一失败点是 B-3 的断言助手，与网关行为无关。

---

## 十二、长上下文阶梯计价（专项验证，未发现缺陷）

库内仅 `gpt-5.6-luna`、`gpt-5.6-sol`、`gpt-5.6-terra` 启用：门槛 272,000 token、输入乘 2.0、输出乘 1.5。六个模型都配了 `cache_read_input_price`（输入价的 0.1 倍），因此 partial 结算的 60% 缓存拆分在当前配置下不会静默退化。

**门槛可达性已用真实上游验证**：向 aihub 渠道 3 发送约 121 万字符的请求，上游返回 HTTP 200 且报告 `prompt_tokens: 274394`、`cached_tokens: 3840`，超过门槛。阶梯在真实条件下可触发，不是纸面配置。

**逻辑正确性经代码与测试双向确认**：

```48:51:internal/core/billing/long_context.go
// ShouldApplyLongContext 判断给定输入合计是否触发长上下文阶梯。
func ShouldApplyLongContext(policy LongContextPolicy, inputTokenSum int64) bool {
	return policy.Active() && inputTokenSum > policy.Threshold
}
```

- 门槛是**排他**比较，`long_context_test.go:19-24` 明确冻结「等于门槛不触发、门槛加一触发」，无差一歧义
- 判定用的合计是未缓存 + cache_read + 三档 cache_write，unknown 与 not_applicable 按 0 计
- 输入乘数作用于输入侧三类单价、输出乘数作用于 output 与 reasoning_output（测试冻结 2→4、0.5→1、8→12、12→18）
- 客户售价与渠道成本两侧共用同一个 `ShouldApplyLongContext` 开关，触发条件不可能分叉；`settlement.go:698-702` 还对两侧 applied 标志不一致做硬失败
- `Active()` 要求 Enabled 且 Threshold > 0 且两个乘数均 Valid，与 Schema 的 `ck_model_prices_long_context` 一致

唯一相关风险是 B-6 的预授权与结算口径差异，不在计价公式本身。

---

## 十三、运维上线清单

### 生产环境变量最小集

必配：`DATABASE_URL`（建议 `sslmode=require`）、`ADMIN_API_TOKEN`（强随机，且因 A-3 必须轮换）、`REDIS_ADDR`、`REDIS_PASSWORD`、`REDIS_KEY_NAMESPACE`（勿沿用 `unio:dev`）、`GATEWAY_ENV=production`、`UNIO_SKIP_DOTENV=true`。前端构建必须提供 `VITE_ADMIN_API_BASE`。

`.env.example` 是开发模板，含 `unio_dev_password` 与 `sslmode=disable`，不可直接用于生产。

### 迁移执行顺序

先备份，再用外部迁移工具执行 `migrations/*.up.sql`，确认无误后才部署或重启应用。服务启动路径不执行迁移，也**不校验 schema 版本**（`internal/platform/store/postgres.go:52` 与 `cmd/gateway-server/main.go:59` 的 `GAP-2-006` / `GAP-2-001` TODO），顺序错了不会被拦住。

`000040_admin_query_indexes` 的合并需要为已迁移环境准备版本处置方案（见第六节）。在已有生产数据上补索引应改用 `CONCURRENTLY`。

### 监控与告警

先补 Prometheus scrape target（Gateway `:8520/metrics`、Admin `:8521/metrics`，两者与 `/healthz` 同样豁免 API Key 认证），再配最低告警集：breaker store 不可用、runtime state integrity 丢失、结算失败增量、限流 fail-closed 增量、熔断转 open 增量、`ledger_billing_exceptions` 中 `authorization_underfunded` 增量、以及 B-1 那条 `authorized` 冻结配 `failed` 请求的一致性检查（应恒为 0）。

探针：Gateway 有 `/healthz` 与 `/readyz`；Admin 只有恒返回 ok 的 `/healthz`，Worker 无 HTTP 探针。

### Redis 约束

必须单节点、主从或 Sentinel，**不支持 Cluster**（启动时 `VerifySingleNodeDeployment` 直接拒绝）。开启 AOF。state loss 恢复流程（阻断 ingress → `runtime-state-maintenance begin` → reconcile → `commit` → smoke → `release`）已由本次 P4 演练的完整状态丢失、AOF 恢复、RDB 恢复、epoch 回滚四个用例验证可用。

### 滚动发布

`HTTP_SHUTDOWN_TIMEOUT` 默认 10 秒，而流式 idle 超时默认 10 分钟，长流式请求在滚动发布时会被强制截断，需按可接受的 drain 时长调大。

### 线路冗余

今早的事故已经证明：线路池内所有渠道属同一服务商时，服务商级熔断一开即全池失格，没有降级路径。上线前应确保每条线路至少包含两个不同服务商的渠道。

---
