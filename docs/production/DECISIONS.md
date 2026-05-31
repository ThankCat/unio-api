# Decisions

本文档记录会影响后续实现和商业语义的关键决策。

## DEC-001 个人账户余额先落 user

状态：accepted

决策：

```text
当前个人账户模式下，余额事实落在 user_balances 和 ledger_entries.user_id。
project 只作为应用空间、API key 容器、用量归集和未来预算边界。
```

原因：

```text
现阶段没有 organization/billing_account。把余额先落 user 可以避免过早引入团队账务模型。
```

影响：

```text
request_records 必须同时记录 user_id、project_id、api_key_id，保证扣费、审计和统计都可追溯。
```

## DEC-002 Adapter 不读取 provider/channel 配置

状态：accepted

决策：

```text
adapter 只接收 channel.Runtime，不读取 env，不查询数据库，不保存业务状态。
```

原因：

```text
provider、channel、model、price、credential 属于业务数据，后续必须由后台管理和数据库驱动。
```

影响：

```text
gateway/routing 负责选择 channel 并解析 credential，adapter 只负责协议转换和上游 HTTP 调用。
```

## DEC-003 Stream 无 final usage 暂不扣费

状态：accepted

决策：

```text
第 7 阶段在没有 final usage 的 stream 请求中不强行按已输出 chunk 估算扣费。
```

原因：

```text
估算扣费可能导致误扣，且当前没有余额冻结和 release 闭环。
```

影响：

```text
公开生产前必须实现余额冻结、异常状态记录和风控策略。
关联任务：../chapters/phase-07-billing-ledger/PLAN.md#task-7-17-preauthorization
```

## DEC-004 request_id 与 correlation id 分离

状态：accepted

决策：

```text
request_records.request_id 使用服务端生成的业务请求 ID。
HTTP X-Request-ID 只作为 correlation id 用于日志/链路关联。
```

原因：

```text
客户端可控 ID 不适合作为账务和请求记录事实主键。
```

影响：

```text
业务请求记录必须由 requestlog.GenerateRequestID 创建。
HTTP correlation id 仍需要输入约束。
```

## DEC-005 第三方库选择不以“少用”为目标

状态：accepted

决策：

```text
业务核心逻辑保持自研可审计；通用基础设施、安全、协议解析、可观测性和精度计算优先评估成熟库。
```

原因：

```text
商业项目的目标不是展示手写能力，而是在核心边界可控的前提下降低维护风险。
```

影响：

```text
新增通用能力前必须先检查 docs/production/THIRD_PARTY_POLICY.md。
```

## DEC-006 部分余额放行与平台差额核销

状态：accepted

决策：

```text
Chat 请求采用严格不透支用户余额的预付费模型，但低余额用户可以消耗剩余可用余额。

调用上游前，billing 计算 estimated_amount；ledger 实际冻结 authorized_amount。
当 available_balance >= estimated_amount 时，authorized_amount = estimated_amount。
当 0 < available_balance < estimated_amount 时，authorized_amount = available_balance，请求仍可继续。
当 available_balance <= 0 时，请求直接拒绝，不调用上游。

请求成功后按真实 actual_amount 结算：
captured_amount = min(actual_amount, authorized_amount)
written_off_amount = max(actual_amount - captured_amount, 0)

written_off_amount 是平台差额核销，不形成用户隐性欠费，不允许用户余额变负。
```

原因：

```text
用户不应该为了发起 API 请求而精确计算 token 或预估费用。
如果要求余额必须覆盖最坏冻结金额，低余额用户会出现“有余额但花不出去”的反人类体验。
如果允许追扣或负余额，又会引入欠费、追款和充值抵扣等新的产品与账务复杂度。

当前阶段选择平台承担少量估算差额，用 tokenizer、模型 max_tokens、核销上限和告警把风险压到低频异常。
```

影响：

```text
authorization 必须拆分 estimated_amount 与 authorized_amount。
settlement 不能再把 actual_amount > authorized_amount 当作普通失败；
应 capture 已冻结金额，记录 write-off 账务事实，并在上游成功且有 usage 时让 request 成功收口。

该规则已由 [GAP-7-014](TODO_REGISTER.md#gap-7-014) 在 2026-05-25 落地；provider/model tokenizer 已由 [GAP-7-013](TODO_REGISTER.md#gap-7-013) 在 2026-05-25 接入。后续仍需用模型 max_tokens、核销上限和告警继续降低平台风险。
```

## DEC-007 Settlement 失败补偿归属 worker

状态：accepted

决策：

```text
上游已经成功并返回可靠 usage 后，如果 SettleSuccessfulChat 失败，不能简单 release 冻结余额。
这类失败应通过后续 worker 持久化 recovery job 和幂等 settlement 重试收口。

当前阶段暂不实现 gateway goroutine 补偿，也不在 tokenizer 小节插队实现 worker。
```

原因：

```text
上游成功后 provider 侧可能已经产生成本，release 会把应收款变成平台损失。
但 settlement 失败又可能导致 reservation 长期 authorized、reserved_balance 悬挂。

goroutine 不是可靠账务补偿机制：进程退出会丢任务，多实例下也难以审计和去重。
补偿任务必须落到数据库事实，并由 worker 使用幂等逻辑重试。
```

影响：

```text
settlement 成功重放检查和外部事务内 debit 幂等重入已完成，GAP-7-012 已在 2026-05-26 关闭。
worker 持久化 recovery job 已在 2026-05-28 落地：gateway 先写入 `settlement_recovery_jobs`，首次 settlement 失败后由 worker 复用幂等 settlement 重试，GAP-7-007 已关闭并移出 release blocker。
```

## DEC-008 第一版不支持倍率，金额快照属于账务事实

状态：accepted

决策：

```text
Unio API 第一版价格体系不支持倍率。

后台直接维护明确金额的客户售价和 provider/channel 成本价。
结算时必须使用明确金额的客户售价、provider/channel 成本价和请求级快照。

一次成功请求必须保存：

1. price snapshot：本次请求当时卖给用户的价格。
2. cost snapshot：本次请求当时调用 provider/channel 的成本。
3. usage record：本次请求真实用量。
4. ledger entry / billing exception：用户扣费、平台核销或风险敞口事实。
```

原因：

```text
倍率适合部分中转站快速运营，但它会额外引入基准价、模型倍率、补全倍率、分组倍率和折扣解释成本。

Unio API 当前优先追求账务清晰和可审计。第一版不引入倍率，可以减少概念层级，并避免历史账单、渠道成本、毛利、fallback 成本和价格变更后的复算依赖运营系数。

商业 API 网关必须回答这些问题：

1. 这次请求向用户收了多少钱。
2. 这次请求调用上游成本是多少。
3. 当时命中了哪个 provider/channel。
4. 当时使用的客户售价和成本价分别是什么。
5. 历史价格变化后，本次请求是否仍能按原事实审计。
```

影响：

```text
TASK-7.20 落地成本价与 cost snapshot，不做倍率系统。

后续 Admin API 第一版直接填写 input/output/cached/reasoning 的明确金额。
如果未来确有批量调价或代理运营需求，再另做倍率/折扣的产品决策；即使未来引入，也只能作为后台辅助工具，结算层仍只消费明确金额和快照。
```

## DEC-005 OpenAI-first 公开契约与 adapter 响应翻译

状态：accepted

决策：

```text
Unio Gateway 对外 `/v1/chat/completions` 以 OpenAI Chat Completions 为唯一客户契约。
上游厂商 JSON 只存在于 adapter wire 层；gateway 不做 vendor 分支。

厂商差异统一在 adapter 内完成：
  请求翻译：OpenAI request → upstream wire
  非流式响应翻译：upstream wire → OpenAI response
  流式响应翻译：upstream SSE → OpenAI chunk（吸收原 normalizer/ 过渡代码）

不再单独维护 Normalizer 作为架构层；文档与代码统一称为 stream response translation。
```

适配原则：

```text
1. 能完整适配 OpenAI 的必须实现。
2. 上游字段名不同但语义等价，映射到 OpenAI 字段。
3. vendor extension 允许 passthrough。
4. 确实无法适配且无等价语义，明确 Reject（400）并写入 Compatibility Matrix。
5. 禁止 silent drop。
```

原因：

```text
产品目标是客户只改 base_url 和 api_key；若对外暴露 vendor 字段或在 gateway 写 vendor 分支，
会导致 SDK/Agent 框架无法 drop-in，且每接一个 upstream 都要污染 gateway 编排与计费层。
```

影响：

```text
Phase 9 全链路实现与验收以 docs/chapters/phase-09-openai-protocol-parity/ 为准。
DeepSeek 作为第一个 upstream 全链路验收（TASK-9.14），不是第一个 special case patch 点。
关联任务：../chapters/phase-09-openai-protocol-parity/PLAN.md
```
