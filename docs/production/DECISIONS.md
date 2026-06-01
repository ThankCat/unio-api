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

## DEC-009 OpenAI-first 公开契约与 adapter 响应翻译

状态：superseded by DEC-010

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

## DEC-010 双协议公开入口、协议原生响应与统一事实

状态：accepted

决策：

```text
Unio Gateway 从 OpenAI Chat Completions 单协议公开入口升级为两个公开协议族：

OpenAI:
  POST /v1/chat/completions

Anthropic:
  POST /v1/messages

两套协议分别维护 HTTP DTO、adapter contract、provider wire DTO、响应 DTO、
stream event 和公开错误结构，不强行转换为一套“大一统聊天 DTO”。

共享能力收口到 gateway lifecycle：
  API key 身份
  request record
  routing
  channel 熔断
  authorization
  attempt
  retry / fallback
  settlement
  recovery
  metrics / tracing
  delivery audit

adapter 每次解析 upstream response 时同时生成：
  1. 协议原生响应或 stream event，返回对应 SDK。
  2. ResponseFacts，进入审计、settlement 和 recovery。

成功交付边界：
  非流式响应只能在 immutable recovery facts 已持久化后返回。
  流式终态 `[DONE]` 或 `message_stop` 只能在 immutable recovery facts 已持久化后写出。
  首次 settlement 失败但 recovery job 已持久化时，可以按 pending recovery 成功交付；
  recovery facts 无法持久化时，不能向客户宣告成功完成。
```

Provider 双协议规则：

```text
provider 只表达业务服务商身份。
channel 使用 protocol + adapter_key 绑定具体协议实现。

DeepSeek:
  channel.protocol=openai,    adapter_key=deepseek
  channel.protocol=anthropic, adapter_key=deepseek

对应代码：
  adapter/openai/deepseek
  adapter/anthropic/deepseek

同一个 (protocol, adapter_key) 下分别登记：
  non_stream
  stream
  input_tokenizer

不定义 FullChatAdapter、FullMessagesAdapter 等强制组合接口。
三个 capability 分别注册、分别做编译期断言、分别参与 routing 过滤。

tokenizer 接口按协议族定义：
  openai.ChatInputTokenizer       → 消费 OpenAI ChatCompletionRequest
  anthropic.MessagesInputTokenizer → 消费 Anthropic MessageRequest

同一个 provider 的不同协议入口必须分别实现 tokenizer：
  adapter/openai/deepseek/tokenizer.go
  adapter/anthropic/deepseek/tokenizer.go

两个实现分别按各自 DeepSeek wire 请求估算，不共享 provider tokenizer facade，
不把 OpenAI messages 和 Anthropic content blocks 归一成一套中间 DTO。
如果实现稳定后确认底层纯文本编码 primitive 完全一致，可以提取窄工具；
协议 framing、字段校验、估算入口和返回语义仍留在各自 protocol adapter。

共享 lifecycle 不消费协议 DTO，只调用协议 service 提供的候选级估算 closure。
```

Routing 规则：

```text
第一版只允许同协议 routing 和 fallback：
  OpenAI ingress    → OpenAI upstream channel
  Anthropic ingress → Anthropic upstream channel

不得隐式做 OpenAI ↔ Anthropic 跨协议桥接。
未来需要 bridge 时，必须另做字段损失矩阵、模型能力矩阵、计费映射和黑盒验收。

routing SQL 只按数据库 channel.protocol 选择同协议候选。
lifecycle 再按内存 registry capability 和熔断状态过滤候选。
authorization 对可用 fallback candidates 使用各自 tokenizer，并按保守 token 结果冻结余额。
```

原因：

```text
OpenAI Chat Completions 与 Anthropic Messages 的 system、messages、content block、
tools、thinking、usage、stream 和错误结构差异明显。

如果强行统一请求和响应 DTO，会产生大量 nullable 字段、map[string]any 和 vendor 分支，
并让 billing 依赖协议细节。

商业生命周期和账务事实可以稳定复用，因此应统一 lifecycle 与 ResponseFacts，
而不是统一公开协议 DTO。
```

影响：

```text
Phase 10 按 docs/chapters/phase-10-dual-protocol-gateway/ 实现。
原阶段 10 后台管理顺延为阶段 11。
DEC-009 的 OpenAI-first 原则保留为历史决策，其中“gateway 不写 vendor 分支”
和“响应翻译收口 adapter”继续有效；“唯一客户契约”由本决策替代。
```

## DEC-011 生产 Adapter 不使用官方 Go SDK，retry 归 lifecycle

状态：accepted

决策：

```text
生产 adapter 不引入 OpenAI 或 Anthropic 官方 Go SDK。

adapter 自行维护：
  provider wire DTO
  outbound HTTP
  response decode
  SSE translation
  usage → ResponseFacts
  provider error → UpstreamError

adapter 一次调用只允许发送一次真实 upstream HTTP 请求。
retry 和 fallback 由 gateway lifecycle 决定。
每次真实上游调用都必须对应一条 request_attempt。
```

允许共享：

```text
adapter/upstreamhttp
  outbound HTTP primitive、body limit、连接关闭、安全 metadata 提取

adapter/sse
  SSE reader primitive
```

不允许共享成模糊大包：

```text
common
util
helper
```

原因：

```text
SDK 能减少部分请求和响应样板代码，但无法替代 provider 显式转换、禁止 silent drop、
ResponseFacts、审计、settlement、fallback 和 recovery。

SDK 默认 retry 或 SDK 类型泄漏会让真实 upstream 调用次数、成本、熔断和审计不一致。
Unio 需要自己掌握 wire 边界。
```

影响：

```text
OpenAI 和 Anthropic SDK 仍可作为 Phase 10 黑盒验收客户端使用，
但不进入生产 adapter 依赖图。
```
