# 本地上线前冒烟测试报告

- 测试日期：2026-08-06
- 代码基线：`41c53a3`（Gateway）加当前工作树 dead settlement finalizer 修复；Admin 基线 `4afdf02`
- 执行环境：本机 Docker PostgreSQL 16、Redis 7，本机 Gateway/Admin/Worker 进程
- 数据隔离：使用独立 PostgreSQL 冒烟数据库和独立 Redis namespace；未写入现有 `unio` 开发业务数据
- 脱敏规则：不记录 API key、上游凭据、管理员口令、完整上游响应或完整错误正文

## 执行状态

| 项目 | 状态 | 证据 / 说明 |
| --- | --- | --- |
| 本机基础设施 | 通过 | `unio-postgres`、`unio-redis` 均为 healthy。 |
| 现有开发数据保护 | 通过 | 原 `unio` 数据库已有 2 个 Provider、6 个 Channel、954 条请求；本轮不直接操作。 |
| 隔离冒烟数据库 | 通过 | `unio_smoke_20260806` 已从 43 个迁移建成，含 41 张表、Provider 成本风险表；隔离拓扑含 2 个 Provider、3 个 Channel、6 个 Model、1 条 Route、1 个测试用户。 |
| Gateway / Admin / Worker 启动 | 通过 | Gateway `/healthz`、`/readyz` 均为 `200`；Admin `/healthz` 为 `200`；Admin 登录及受保护的 ping 为 `200`。Gateway 启动时成功恢复运行时控制并进入就绪。 |
| 鉴权与基础 OpenAI 接口 | 通过 | 无认证模型列表为 `401`；有效测试 key 的模型列表为 `200` 且返回 6 个模型；无效 key、临时禁用后已恢复的测试 key 均为 `401`；`/v1/responses/input_tokens` 为 `200`。 |
| OpenAI Chat Completions | 通过 | 非流式和 SSE 流式均为 `200`；流式包含数据帧和完成帧。每次成功请求均留下成功的 request/attempt 终态。 |
| OpenAI Responses | 通过 | 非流式响应为 `200`、终态为 completed；流式为 `200`，观测到 `response.created`、`response.in_progress`、文本增量和 `response.completed`。非法空 input 返回 `400 invalid_request`。 |
| Anthropic Messages | 有条件通过 | 为覆盖协议，隔离库经 Admin API 建立了指向本机 mock 的 Anthropic Provider/Channel/Route/API key；非流式和流式均为 `200`，流式包含完整 message 生命周期事件。真实本机拓扑没有 Anthropic Channel，真实入口请求正确返回 `503 routing_no_available_channel`，因此尚无真实 Anthropic 上游证据。 |
| 路由、fallback、账务 | 通过 | 观测到一次 Chat 请求的候选 0 为 `503`、候选 1 成功，最终 request 成功。成功请求均存在 user ledger、Provider ledger、usage 和已 capture 的 reservation；账务组件行数随用量构成变化，未以固定行数作为通过条件。 |
| Sticky | 通过 | 两次携带相同 `prompt_cache_key` 的 Responses 请求成功；路由 trace 先为 `bind_if_absent`，后为 `refresh_if_current`，均选择同一 Channel。 |
| Redis 状态丢失恢复 | 通过（fail-closed） | 仅删除隔离 namespace 的 state-integrity marker 后，`/readyz` 和新 Chat 请求立刻为 `503`；服务没有在无法证明运行态一致时放行上游。重启 Gateway 后 marker 和就绪状态恢复。 |
| settlement recovery：可恢复路径 | 通过 | 使用仅隔离测试的 `billing_e2e=once` 构建，让内联结算失败一次；客户端仍获 `200`，job 先为 pending、账务为零，Worker 重放后 job/request/attempt 均成功。5 秒后账务计数保持不变。 |
| settlement recovery：dead 收口 | 通过 | 修复后使用 `billing_e2e=always` 的 Gateway/Worker 和最大尝试数 1 重跑隔离 CLI：客户端获 `200`；job 为 dead（1/1），request/attempt 均 failed，reservation released，风险异常一条，用户与 Provider debit 均为零；5 秒复查所有状态和计数不变。 |

## 已知发现

1. **Redis 部分运行态丢失需要运维恢复（预期行为，非代码缺陷）**：仅删除 `unio:smoke:20260806:runtime-control:v1:state-integrity-marker` 后，Gateway 立即 fail-closed；即使 Redis 本身仍可访问，也不能证明限流、并发、熔断等运行态与 PostgreSQL 权威版本一致，因此不应继续接收请求。重启 Gateway 后启动期 `EnsureStateEpochSeed` 可恢复 marker 与就绪。

   **影响：** Redis 发生 key 丢失或选择性清理时，网关会保持不可用，直到重启或执行受控恢复。这是牺牲可用性换取运行态和账务安全的 fail-closed 取舍，不应把“Redis 还能读写”误判为“运行态安全”。

   **运维建议：** 保持当前 fail-closed 语义；监控 marker 缺失、readiness 变为 `503` 和 runtime infrastructure fault，并在 Redis 部分状态丢失时按 runbook 重启 Gateway 或执行经过完整 reconciliation proof 的受控恢复。不要只补写 marker 后直接放行流量。

2. **dead settlement recovery 未收口 request attempt（已修复并复验）**：此前 job 达到最大尝试后，`FinalizeDeadChatSettlement` 只将 request 标记为 failed，造成关联 attempt 长期为 `running`。

   **修复：** 在释放 reservation 后、标记 request 前，同一事务调用 `requestlog.MarkSettledAttemptFailed` 收口 `job.AttemptID`，使用 recovery job 固化的上游响应、模型、finish、状态码、请求 ID、usage mapping 等事实，并以结算失败错误码记录终态。request 与 attempt 共用同一 `completed_at`。

   **回归证据：** 在临时迁移数据库 `unio_dead_finalizer_20260806_2092` 上执行 `TestFinalizeDeadChatSettlementClosesAttemptAndIsIdempotent` 通过。随后在另一临时迁移数据库运行带 `billing_e2e=always` 注入的 Gateway/Worker，以 CLI 发起一次非流式 Chat 请求：job dead（1/1）、request/attempt failed、delivery completed、reservation released、风险异常一条、用户与 Provider debit 均为零；attempt 保留上游 response/model/finish/status/request ID/usage mapping，且与 request 使用相同完成时间。5 秒后复查无重复账务或状态变化。两个临时数据库均已删除，未访问 `unio` 开发库。

3. **测试拓扑初始化被废弃字段阻断（中）**：`scripts/test/test_db/config.local.json` 仍含已删除的 `routes.tpm_limit`，而 `init_test_db.py init` 会拒绝该字段，导致按文档的一键初始化无法运行。

   **本次处置：** 仅在进程内从配置副本移除该字段后初始化隔离数据库，原配置和原开发库均未改动。

   **修改建议：** 修正 `init_test_db.py` 的导出逻辑，不再导出 `tpm_limit`；对旧 `config.local.json` 增加一次性兼容迁移（忽略该已废弃字段并提示用户移除），使本地测试准备可重复执行。

4. **真实 Anthropic 上游覆盖缺失（中，发布前配置项）**：本机真实路由拓扑中只有 `protocol=openai` Channel。隔离 Anthropic mock 验证了 Gateway 的 Messages 协议、流式编码、路由和结算路径，但不能证明真实 Anthropic Provider 凭据、网络和响应差异。

   **修改建议：** 发布前为计划承载 Anthropic 流量的环境配置一条真实但低额度的 Anthropic Channel，并以 CLI 发送最小非流式和流式 Messages 请求；若该协议暂不发布，应明确从发布范围和路由策略中排除，而不是依赖“无可用 Channel”的运行时 `503`。

## 关键恢复证据

### Redis fail-closed

- 删除前 marker 存在且无 TTL；只删除该精确 key，未删除同 namespace 的其他 key。
- 删除后立即：`/readyz = 503`，带有效 key 的 Chat 请求也为 `503`，marker 仍不存在。
- 持续观察 12 秒：`/readyz` 仍为 `503`，reconciliation proof 存在但 marker 仍缺失。
- 重启同配置 Gateway：启动期恢复 marker，`/readyz = 200`，随后真实 Chat 请求为 `200`。

### Settlement Recovery

- `once` 注入：响应已交付时 job 为 pending、request/attempt 暂为 running、user/Provider ledger 均为零；Worker 重放后 job 仅尝试一次并变为 succeeded，request/attempt 均 succeeded，reservation captured，usage 与 Provider ledger 均恰有一份；5 秒后计数未变化。
- `always` 注入（max attempts = 1，修复前）：job 在约 3 秒内变为 dead；request failed、delivery completed、reservation released、user/Provider debit 均为零、ledger risk exception 为一条。关联 attempt 保持 running，发现了上述问题。
- 修复后 `always` CLI 复验：客户端仍获 `200`；Worker 将 job 置为 dead 后，同一事务把 request 与关联 attempt 收口为 failed，释放 reservation 并只写一条风险异常。5 秒后风险异常仍为一条，user/Provider debit、usage、price snapshot、cost snapshot 均为零。

## 发布结论

**dead settlement recovery 上线阻断已关闭。** 修复后的数据库回归和 Gateway/Worker 隔离 CLI 调度链路均通过，当前未发现新的账务或恢复阻断。

若本次发布包含 Anthropic，仍应完成一条真实 Anthropic 上游 CLI 验证；否则应正式将 Anthropic 从本次发布范围中排除。

## 判定标准与覆盖范围

- 公开协议：每个协议至少完成一次认证、路由、上游调用、计量/结算链路；只保存响应类型和关键结构，不保存正文。
- 账务：验证请求成功后的 request/attempt/ledger/provider ledger 终态一致，且不产生重复记账。
- Redis 恢复：删除本次隔离 namespace 的运行态 key 后，服务必须按设计 fail closed，并留下可审计事实；本轮符合预期，恢复动作由重启/受控运维流程完成。
- settlement recovery：构造可恢复终态后，Worker 必须只收口一次，不重复扣费或释放；可恢复与 dead 收口路径的数据库回归、Gateway/Worker CLI 调度链路均通过。
- 每个发现必须附上可落地的修改建议；未找到问题时也明确测试盲区。

## 测试盲区

- Anthropic 流程使用本机 mock，上游真实契约和网络仅有配置缺口的失败证据，尚无真实成功证据。
- 未对公网暴露场景做渗透或浏览器安全策略验证；本轮限定为本机运行与协议/账务/恢复烟测。

## 代码回归基线

`go test -count=1 ./internal/service/gateway/lifecycle ./internal/app/workers ./internal/platform/breakerstore ./internal/core/runtimecontrol` 通过。

修复后额外执行：`DATABASE_URL=<isolated temporary database> go test -count=1 ./internal/service/gateway/lifecycle -run '^TestFinalizeDeadChatSettlementClosesAttemptAndIsIdempotent$'`，通过。

修复后 CLI 复验：使用 `-tags billing_e2e` 构建 Gateway/Worker，设置 `BILLING_E2E_INJECT_SETTLEMENT_FAIL=always` 和 `WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS=1`，在独立 PostgreSQL 数据库、独立 Redis namespace 与本机 OpenAI mock 上执行，通过。

## 清理结果

- 已停止本轮 Gateway、Admin、Worker 和 Anthropic mock 进程；测试端口 `18521`、`18522` 均已释放。
- 已删除本轮创建的 `unio_smoke_20260806` 数据库和 `unio:smoke:20260806*` Redis key；核验剩余数量均为 0。
- 已删除修复复验创建的两个临时 PostgreSQL 数据库和 `unio:smoke:dead-finalizer:20260806:*` 的 32 个 Redis key；核验数据库、Redis key 和测试端口剩余数量均为 0。
- 已删除本轮临时 mock、测试二进制、日志和 Python 缓存；现有 `unio` 数据库和非 smoke Redis key 未触碰。
