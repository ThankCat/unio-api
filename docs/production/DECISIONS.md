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

状态：accepted（**部分条款由 [DEC-025](#dec-025-stream-partial-settlement) 修订**）

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

2026-06-25 修订：emitted 且无 final usage 时改走 partial settlement（DEC-025），含 cancel/interrupt（路线 B）
与 adapter 正常结束缺 usage（路线 D）；不再写 risk_exposure。路线 D 额外记录渠道异常。
首 token 前 cancel 与无 emit 路径仍适用本决策原意。
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
原阶段 10 后台管理先顺延为阶段 11，再顺延为阶段 12，最终顺延为阶段 13（阶段 11 改为 OpenAI Responses API ingress 见 DEC-014；阶段 12 改为能力架构 Capability Architecture 见 DEC-015）。
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
SDK 能减少部分请求和响应样板代码，但无法替代 provider 显式转换、出站 allowlist、
ResponseFacts、审计、settlement、fallback 和 recovery。

SDK 默认 retry 或 SDK 类型泄漏会让真实 upstream 调用次数、成本、熔断和审计不一致。
Unio 需要自己掌握 wire 边界。
```

影响：

```text
OpenAI 和 Anthropic SDK 仍可作为 Phase 10 黑盒验收客户端使用，
但不进入生产 adapter 依赖图。
```

## DEC-012 协议为先与 Provider 映射 Drop 策略

状态：accepted

决策：

```text
Unio Gateway 以 ingress 客户协议为对外契约边界，provider 差异在 adapter 出站/入站
映射层消化，默认不因「当前 channel 无法转换某合法协议字段」而 400。

两层边界：

1. Ingress 协议校验（gatewayapi）
   - 只校验客户协议 JSON 结构、类型、必填与 union 合法性。
   - 非法协议请求返回协议原生 400。
   - 合法 OpenAI / Anthropic 字段必须进入 typed DTO 或登记 extension，禁止 decode 阶段
     因 provider 能力而丢弃。

2. Provider 出站映射（adapter → upstream wire）
   - 只允许 mapping 表标记为 Pass / Adapt 的字段进入 upstream body。
   - 无法转换或无 upstream 对应项 → Drop：不写入 upstream JSON。
   - 禁止 Extensions 无脑 merge 进 upstream；禁止把 Drop 字段透传给上游赌其忽略。

3. Provider 入站映射（upstream wire → 客户协议）
   - 只填充 ingress 协议 DTO 需要的字段。
   - 协议不需要或无法映射的上游字段 → Drop，不进入公开响应。
   - 账务、审计、settlement 所需事实走 ResponseFacts / usage.Facts，不污染公开 DTO。

Provider mapping 策略表只允许：
  Pass | Adapt | Drop

Drop 不是 silent drop：
  - request_attempt 或 structured log 必须记录 dropped_request_fields（内部字段名）。
  - 可选 debug 开关向管理员暴露 drop 摘要；默认不对客户返回 drop 列表。

Reject（400）仅保留于：
  - ingress 协议非法
  - auth / billing / routing / rate limit 等业务拒绝
  - 上游 hard failure 且无法映射为协议原生成功响应
```

原因：

```text
客户按 OpenAI 或 Anthropic SDK 传参时，期望的是「协议收下了」而不是「因后端
provider 不支持某字段而 400」。把无法转换的字段 Drop 在出站边界，可同时：

1. 避免 upstream 未来变严格时对未知字段报 400。
2. 保持公开协议 drop-in 体验。
3. 通过内部 dropped_fields 审计保留可追踪性。

ResponseFacts 旁路保证 billing 不依赖「公开响应里是否出现某 usage 维度」。
```

影响：

```text
docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_*_MAPPING.md
  Reject / No-op / Ignored 收口为 Drop；Pass / Adapt 规则保留。

docs/chapters/phase-10-dual-protocol-gateway/OPENAI_CHAT_COMPLETIONS_MATRIX.md
docs/chapters/phase-10-dual-protocol-gateway/ANTHROPIC_MESSAGES_MATRIX.md
  矩阵只负责 ingress Typed / Passthrough；provider Drop 见 mapping。

AGENTS.md
  「禁止 silent drop」改为「禁止 decode 丢字段；provider 层 Drop 必须登记 mapping 并写内部审计」。

生产代码（待实现）：
  - 移除 adapter 层 CodeAdapterRequestUnsupported 作为默认手段。
  - buildUpstreamWire 改为出站白名单，不再 mergeJSONObjects(Extensions) 全量透传。
  - gatewayapi decode 层移除 service_tier / store / web_search_options 等 provider 级 Reject。
  - 黑盒 DS-OAI-09 / DS-ANT-09 改为断言 upstream body 不含 dropped 字段且请求仍 200。

DEC-010 双协议边界、ResponseFacts 与 lifecycle 不变；本决策只改 provider 映射语义。
```

## DEC-013 协议 beta header 宽进接受与出站 Drop

状态：accepted

决策：

```text
Unio Gateway 对 provider 协议 beta header（当前 Anthropic `anthropic-beta`）采用宽进策略：

1. ingress 接受任意 beta（含未登记值、逗号分隔多值、看似畸形的 token），不再因登记
   allow-list 而 400；这是 DEC-012「协议为先，不因 provider 能力 Reject」在 header 维度的
   延伸。
2. provider 出站映射层按 mapping 处理：当前 DeepSeek Anthropic endpoint 忽略所有 beta 且
   出站不发送，按 Drop 处理。
3. Drop 不静默：handler 以脱敏 Debug 日志记录 `dropped_beta_headers`（仅 beta 能力名，
   非敏感）；绝不在公开响应里假装某 beta 已生效。
4. 未来接入真实 Anthropic 1P adapter（直连 Anthropic）时，应改为按登记表把支持的 beta
   Pass 转发到 upstream `anthropic-beta`，无法支持的继续 Drop。

红线：beta 永远只作为协议透传/Drop 的输入，不得反向影响账务事实或被当成已生效能力。
```

原因：

```text
1. drop-in 兼容是产品北极星：真实 Anthropic SDK / Claude Code 会默认携带 beta header，
   且 Anthropic beta 列表更新频繁。硬编码 allow-list + 未知即 400 会在客户毫无过错时拒绝
   合法请求，并在每次 Anthropic 发布新 beta 后需要重新部署才能解除阻断。
2. 与 DEC-012 一致：body 不支持字段走 Drop + 审计而非 400，beta header 不应是唯一例外。
3. 对当前唯一 provider（DeepSeek）而言转发与否对行为无影响（全忽略），因此「拒绝客户」没有
   任何收益，只有破坏兼容的代价。
4. 对照业界（OpenRouter）：其聚合网关用 `x-anthropic-beta`，pass-through 已支持的 beta、
   strip 不支持的（如 strict 未带 structured-outputs 头时剥字段照常路由），同样不因 beta 400。
   我们公开面本身就是 Anthropic 协议，故保留原生 `anthropic-beta` 头名，但采用同样的宽进语义。
```

影响：

```text
docs/chapters/phase-10-dual-protocol-gateway/ACCEPTANCE.md
  安全验收 4 由「未知 beta → ingress 400」改为「beta 一律接受、出站 Drop + 脱敏审计」。

docs/chapters/phase-10-dual-protocol-gateway/ANTHROPIC_MESSAGES_MATRIX.md
  anthropic-beta 行由 IngressReject 改为 Passthrough（接受任意值，provider Drop）。

docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_ANTHROPIC_MAPPING.md
  anthropic-beta 说明由「ingress 仅接受登记 beta」改为「ingress 接受任意 beta（DEC-013）」。

生产代码：
  - app/gatewayapi/anthropic/messages/handler.go 删除 registeredAnthropicBetas 登记表与
    400 校验；NewMessagesHandler 注入 *slog.Logger，新增 auditIgnoredBetaHeaders 脱敏审计。
  - 测试由「拒绝未登记 beta」改为「接受任意 beta（含未登记/逗号列表）并继续调用 service」。

取代 TASK-10.11 实现期间「ingress 仅接受登记 beta」的临时设计；DEC-010 / DEC-012 边界不变。
```

## DEC-014 OpenAI Responses ingress 下转 Chat Completions 桥接

状态：accepted

决策：

```text
Unio Gateway 新增第三个公开操作 POST /v1/responses（OpenAI Responses API，Codex CLI 主入口），
作为阶段 11 商业级、生产级能力，排在能力架构（阶段 12，DEC-015）与后台管理（阶段 13）之前。
它是 ingress-only 协议：在 gateway 内部把 Responses 请求下转到既有内部 OpenAI Chat 契约
（core/adapter/openai.ChatRequest），复用 Phase 10 已验收的 OpenAI Chat 上游、adapter registry
capability、routing、authorization、attempt、settlement、recovery 与 ResponseFacts。

不新增上游 Responses adapter：DeepSeek 及绝大多数第三方上游只提供 Chat Completions，没有
Responses endpoint，因此 Unio 不去对接任何 provider 的 Responses 上游，而是在内部做
responses-to-chat 双向翻译（请求下转 + 响应/SSE 上转）。

边界沿用 DEC-010：请求协议分离、响应协议分离、账务事实统一、商业生命周期统一。Responses 公开
DTO 独立维护，不并入 Chat DTO，也不进 billing。

第一版无状态：
  - 只支持 store=false 语义；Codex 对第三方 provider 默认每轮回传完整 input。
  - previous_response_id、server-side store=true、responses retrieve/delete/input_items 不实现。
  - Responses 专属字段（store/include/truncation/prompt_cache_*）在出站 Drop 并记内部审计。

模型指定：复用 Phase 6 routing + model catalog，客户请求的 model 即 Unio 模型目录 model_id，
经 IngressProtocol=openai routing 命中 channel 的 upstream_model（DeepSeek）。Unio 不在翻译层
硬编码 provider 模型名、不静默替换客户模型；未知模型返回 Responses model_not_found。模型别名
（Codex 默认模型名 → Unio 上游模型）与后台可视化管理分别归 GAP-11-006 与阶段 13。

账务零改动：上游仍是一次 Chat Completions 调用，ResponseFacts / usage.Facts / 价格快照 /
成本快照 / recovery 全部复用，不新增账务 schema。唯一新增是 requestlog.Operation="responses"，
用于在审计与 metrics 中区分 Responses 请求。

“第一版无状态” 是经过权衡的生产范围决策（Codex 对第三方 provider 默认 store=false 并每轮回传
完整 input），不是质量妥协；ingress、响应、流式、错误、usage、工具/reasoning 必须按生产标准完整
实现并黑盒验收。
```

翻译要点：

```text
请求（Responses → 内部 ChatRequest）：
  input(string|items[]) → messages；function_call/function_call_output → assistant.tool_calls/tool message；
  instructions → 顶部 system；tools 扁平→嵌套；tool_choice 归一；reasoning.effort→reasoning_effort；
  text.format→response_format；max_output_tokens→max_tokens。
响应（内部 ChatResponse/Chunk → Responses）：
  output items(reasoning/message/function_call)；usage 字段名转换；finish_reason→status；
  流式翻译成命名事件状态机（response.created → output_item/content_part/output_text.delta/
  function_call_arguments.delta → ... → response.completed），带单调 sequence_number。
Codex 专属：custom/grammar 工具(apply_patch) 与 local_shell 在 Chat 无等价，按 convert→function/Drop
  处理；Responses 内置工具(web_search 等) 第一版不真实执行。
```

原因：

```text
1. Codex CLI 固定用 Responses API（wire_api="responses"），而 DeepSeek 等上游只懂 Chat Completions；
   社区通行做法是中间放一个 Responses↔Chat 翻译代理。Unio 把这层翻译收进 gateway，复用既有计费、
   审计、fallback 与 settlement，比让用户额外跑本地代理更可控、可计费、可审计。
2. Responses 与 Chat Completions 同属 OpenAI 协议族，语义可逆映射；下转到内部 Chat 契约能直接
   复用 Phase 10 全部基础设施，无需新协议族、无需账务改造。
3. 任何已实现 openai.ChatAdapter 的上游都自动获得 /v1/responses 能力，桥接不依赖具体 provider。
```

影响：

```text
新增子包 app/gatewayapi/openai/responses 与 service/gateway/openai/responses（与 chatcompletions 对称，
不在协议族根包平铺）；adapter/openai 完全复用。requestlog 新增 OperationResponses。
DEC-010/DEC-011/DEC-012/DEC-013 边界不变。
实现与验收以 docs/chapters/phase-11-openai-responses-api/ 为准；字段映射以该目录
RESPONSES_CHAT_BRIDGE.md 为准；公开能力声明以同目录 CAPABILITY_MATRIX.md 为准（商业网关
对客户的能力契约）。欠账登记为 GAP-11-001 ~ GAP-11-010。
```

接口范围（本决策一并冻结）：

```text
完整实现：POST /v1/responses（主路径，含流式 + tools + reasoning + 错误安全）
降级实现：POST /v1/responses/compact          —— 无状态 DeepSeek 摘要 + Unio 自编码 compaction item（GAP-11-007）
本地估算：POST /v1/responses/input_tokens     —— 复用 ChatInputTokenizer 估算（GAP-11-008）
501 stateless：
  GET    /v1/responses/{id}
  DELETE /v1/responses/{id}
  GET    /v1/responses/{id}/input_items
  POST   /v1/responses/{id}/cancel
Reject：POST /v1/responses 带 background:true（明确报错，不静默转同步）
```

结构性不可桥接能力（公开能力边界，不模拟伪能力）：

```text
- encrypted_content（reasoning 跨轮加密透传，需 OpenAI 服务端密钥）
- 内置工具 web_search / file_search / code_interpreter / computer_use / image_generation / mcp
- 服务端存储 store=true 真实持久化与 retrieve/delete/input_items
- background=true 异步任务
- prompt（server-side prompt template 引用）
社区里 LiteLLM / codex-relay / codex-deepseek / codex-bridge 均不实现这些能力——是结构性
不可能，而非实现疏漏。Unio 把它做成显式商业承诺。
```

开源参考的角色分工（不复制代码）：

```text
主参考：MetaFARS/codex-relay (Rust)             —— 流式事件状态机、tool_calls delta 累积、apply_patch 处理
辅参考 1：BerriAI/litellm responses transformation —— 字段映射百科全书（请求/响应/usage 字段名）
辅参考 2：yangfei4913438/codex-deepseek / wujfeng712-ui/codex-bridge —— DeepSeek wire 行为 sanity check
明确不参考：任何项目的错误处理 / 重试 / fallback / 日志 / 审计 / metrics / 架构分层 / 命名风格——
  以 Unio 已验收的 Phase 8/9/10 实现为准。代码评审以"是否像 Unio 模块"作一票否决标准。
```

## DEC-015 能力架构三层模型与 models.dev 定位

状态：**部分 superseded**（2026-06-23 → [DEC-024](#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明)）

> 运行时**能力闸门**（observe/enforce、required 推断、capability_signals）已删除；「模型层能力声明」
> （`model_capabilities`）以**纯展示**形式保留，合法 key 真源迁到 `capability_keys` 字典表。
> 详见 [DESIGN-capability-manual-declaration.md](DESIGN-capability-manual-declaration.md)。Layer 3 渠道收紧此前已由 DEC-023 移除。

决策：

```text
Unio Gateway 从"协议为先 + adapter 出站 Drop"（DEC-012）升级为"协议为先 + 模型能力声明 +
运行时能力闸门 + 显式 capability 错误"。落地为阶段 12 Capability Architecture。

能力模型分三层：

Layer 1 — Model Metadata（模型元数据）
  来源：models.dev daily cron + 人工补全（接口与字段见 docs/datasources/MODELS_DEV_API.md：
        api.json / models.json / catalog.json / logos/<id>.svg 共 4 个）
  存储：models 表（canonical_id / lab / context_window / max_output / price / coarse_caps_hint）
  用途：catalog 展示、价格基线、模型选择 UI
  models.dev 只作为"种子源"，不作为运行时事实源；source=manual 行永远不被同步覆盖；
  新同步模型默认 enabled=false，必须 admin 手动开通。

Layer 2 — Protocol Capability Matrix（协议字段能力矩阵）
  来源：Unio 自己维护，粒度对齐到"协议字段 / 子能力"
  存储：model_capabilities 表 (model_id × capability_key)
  capability_key 注册表见 docs/protocol/CAPABILITY_KEYS.md，公开稳定契约，
  只能新增不能删除（如同 OpenAPI 字段）。
  用途：runtime gating + cap-tags 公开

Layer 3 — Channel Capability Overrides（渠道能力收紧）【已移除，见 DEC-023】
  来源：admin（阶段 13）配置
  存储：channel_capability_overrides 表 (channel_id × capability_key)
  规则：只能做减法（disable / limits 收紧），不能反向放开 Layer 2 未声明的能力
  用途：某 channel 受供应商限制的具体闸门
  现状：DEC-023 已移除该层——默认所有渠道完美支持模型层声明的能力，能力判定只看 Layer 2 模型层。

Ingress 处理顺序：
  1. ingress decode → DTO 校验合法（DEC-012 不变）
  2. capability.Infer(req) → required_capabilities 集合
  3. routing.SelectCandidates(model, protocol, project_policy)
  4. capability.Gate(candidates, required) → 过滤候选
  5. 若候选为空：按三个生成执行面各自所属协议的原生格式返回 model_capability_unavailable
                  或 channel_capability_unavailable
  6. 否则按余下候选走 reservation / attempt / settlement

Drop vs Reject 划线：
  - Drop（adapter 出站静默剥离）：协议合规但对功能无影响的字段
    例：frequency_penalty、presence_penalty、service_tier、safety_identifier
  - Reject（capability 闸门 4xx）：客户实质要求的功能能力
    例：image.input / image.output / audio.* / 内置工具 / json_schema / reasoning.effort

灰度策略：
  capability.enforce_mode 按协议独立切换；observe 阶段只记录不拒绝（≥ 7 天观察期），
  enforce 阶段闸门生效。先 OpenAI Chat，再 Anthropic，再 OpenAI Responses。
```

明确不做：

```text
1. 跨 provider 拼接（如客户用 DeepSeek 请求图片，Unio 自动调 OpenAI dall-e 拼回去）——
   网关边界，不做 agent 平台动作；多协议拼接会全面破坏账务、审计与可追溯。
2. ingress 错误响应里"建议替换模型"——避免向客户泄漏其他客户可见的模型；客户应通过
   GET /v1/models?capability=... 自行筛选。
3. 客户级 capability quota（限制某 user 不能使用 image.output）——归账务 / project policy，
   不属于能力架构。
4. 把 capability 数据写入 ledger / cost_snapshot——能力失败永远是 ingress 级 4xx，不进
   reservation，账务事实保持纯净（DEC-008、DEC-010 不变）。
```

原因：

```text
1. 协议（OpenAI Chat / Anthropic Messages / OpenAI Responses）是"行业认可契约"，但即使
   OpenAI 自家不同模型对同一协议的字段支持也是梯度（modalities:["audio"] 只 gpt-4o-audio
   支持；老 gpt-3.5 不支持 tools）。Unio 作为多 provider × 多模型网关，能力差异比单家 lab
   更大。

2. DEC-012 的"协议为先 + adapter Drop"对"协议合规细节"是好策略，但对"客户付钱要的实质
   能力"会变成静默欺骗：客户以为下单了图片，付了 token 钱，却只拿到一段文字。这破坏商业
   信任，且难以被发现和审计。

3. 业界主流商业网关（Portkey、OpenRouter、Vercel AI Gateway、LiteLLM router）全部采用
   capability-aware routing：required cap × channel cap 过滤候选，缺失时返回明确 error。
   Unio 选择同一路线。

4. models.dev 是社区维护的模型元数据数据库，作为 Layer 1 种子源能显著降低初始入货工作量；
   但它的能力位粒度太粗（Reasoning Y/N / Tool Call Y/N / Structured Y/N），不能直接作为
   运行时字段闸门事实，必须由 Unio 自己在 Layer 2 维护精化版本。

5. capability_keys 一旦发布即公开契约：客户 SDK / Console UI / 监控告警都会消费；只能
   新增不能删除，等同于 OpenAPI 字段管理纪律。
```

影响：

```text
1. 阶段 12 Capability Architecture：
   PLAN 见 docs/chapters/phase-12-capability-architecture/PLAN.md
   8 个任务覆盖 schema / inference / gate / models.dev sync / public API / adapter 对齐 /
   observability / 灰度迁移。

2. DEC-012 不变；本决策是对 DEC-012 Drop 策略的"按能力分级化"补充：
   - 协议合规但无功能影响的字段继续走 adapter Drop（与 DEC-012 一致）。
   - 协议合规但属于实质能力的字段改走 capability 闸门 Reject（本决策新增）。

3. 阶段 11 docs/chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md 中静态声明的
   能力边界（内置工具不真实执行、encrypted_content 不透传、background 拒绝等）在阶段 12
   迁入 model_capabilities 表；阶段 11 公开 API 表面不变。

4. 阶段 13 Admin（原阶段 12 顺延）：admin CRUD 直接基于阶段 12 schema 实现，不需要重新
   设计能力数据模型；admin 主要工作是 UI、权限、审计与运营流程。

5. 公开协议表面新增：
   - GET /v1/models 可选返回 capabilities 数组 + ?capability= 过滤（SDK 向后兼容）
   - GET /console/v1/models 新 endpoint，给 console 前端使用

6. 数据库新增 4 张表 + capability_keys 注册表：
   migrations: models / model_capabilities / channel_capability_overrides /
               model_capability_sync_jobs
   docs:       docs/protocol/CAPABILITY_KEYS.md (公开稳定列表 + 版本)
               docs/datasources/MODELS_DEV_LICENSE.md (license 摘要与 attribution)

7. 欠账登记为 GAP-12-001 ~ GAP-12-009；阶段 11 [GAP-11-010](TODO_REGISTER.md#gap-11-010)
   reasoning_effort doc/code drift 已在 TASK-11.09 关闭（adapter 出站归一 + 两份 doc 对齐，
   见 DEC-016）；最终能力位沉淀仍归阶段 12 TASK-12.06。
```

## DEC-016 Responses reasoning 为 opt-in，DeepSeek 出站归一 reasoning_effort 与 thinking 闸门

状态：accepted

决策：

```text
1. reasoning_effort 归一（DEC-012 协议为先的 provider 出站具体化）：
   DeepSeek 官方 reasoning_effort 枚举仅 high/max（thinking 模式生效），并自带
   low/medium→high、xhigh→max 兼容映射；Codex（gpt-5 家族）另发 minimal，不在 DeepSeek
   枚举内。Unio 在 DeepSeek adapter 出站显式归一为 high/max：
     minimal/low/medium/high → high；xhigh/max → max；未知枚举 Drop（DeepSeek 回退默认 high）。
   不依赖上游隐式兼容、杜绝 minimal 触发上游 422。实现：deepseek/adapt.go normalizeReasoningEffort
   + drop.go。

2. Responses reasoning 为 opt-in（thinking 闸门）：
   OpenAI Responses 的 reasoning 是显式可选项；缺省/null 表达「不要推理」。DeepSeek thinking
   默认 enabled，若不处理，则 Codex effort=none 的非 reasoning run 仍触发 DeepSeek CoT
   （额外 token 成本 + reasoning 输出）。因此：
     - 桥接层（responses_chat_map）：reasoning 缺省/null → 置内部意图标志
       ChatRequest.ReasoningDisabled=true；有 effort → 写 reasoning_effort。
     - DeepSeek adapter：ReasoningDisabled 且客户未显式带 thinking 时，出站注入
       thinking:{type:"disabled"}，并 Drop 矛盾的 reasoning_effort。

3. 分层与防泄漏（关键约束）：
   thinking 是 DeepSeek 私有字段，base OpenAI adapter 的 mergeJSONObjects 会把所有
   Extensions 原样并入 wire（无白名单），若在桥接层直接塞 thinking 会泄漏到真 OpenAI 上游。
   因此用「provider 无关的内部意图标志 ReasoningDisabled」承载语义：
     - 它是 ChatRequest 内部契约字段，不在 wire struct（chatCompletionRequest）中，永不序列化。
     - 由具体 provider adapter 翻译为自家私有字段（DeepSeek→thinking:disabled）。
     - chat completions ingress 不设置该标志，保持 DeepSeek 默认 enabled（不回归 DS-04）。
```

原因：

```text
1. reasoning_effort 在 Phase 10 chat 流量几乎不触发，但 Phase 11 Codex 每请求必带，是首次实战；
   原 doc 标 Adapt 而代码裸 pass-through（GAP-11-010）。以官方 API 文档为权威依据定调 Adapt，
   消除 doc/code/doc 三方漂移。
2. 「付费要推理才推理」符合 Responses opt-in 语义，也避免非 reasoning run 的隐性思考成本，
   对商业网关是正确的成本与语义边界。
3. 用内部意图标志而非直塞私有字段，既满足「ingress 禁止 silent leak」也保持桥接层 provider 无关、
   provider 能力归一只在各自 adapter（与 DEC-002 / DEC-012 一致）。
```

明确不做：

```text
1. 不在桥接层注入 DeepSeek 私有 thinking（防泄漏到真 OpenAI 上游）。
2. 不在 DeepSeek adapter 仅凭 reasoning_effort==nil 关 thinking（会回归 chat 默认 enabled）。
3. 不覆盖客户显式 thinking（chat ingress extra_body.thinking 优先）。
```

影响：

```text
1. 代码：internal/core/adapter/openai/contract.go 新增内部字段 ReasoningDisabled；
   responses_chat_map.go 置位；deepseek/drop.go + adapt.go 出站归一与 thinking 注入；均有单测。
2. doc：Phase 9 DEEPSEEK_UPSTREAM.md、Phase 10 DEEPSEEK_OPENAI_MAPPING.md、Phase 11
   RESPONSES_CHAT_BRIDGE.md 已对齐；GAP-11-010 关闭。
3. 真实 key 端到端 smoke（含 thinking on/off 行为）归 TASK-11.15；阶段 12 TASK-12.06 将
   reasoning.effort 能力位沉淀进 model_capabilities，与本决策出站归一不冲突（一个是能力闸门，
   一个是 provider 出站适配）。
```

## DEC-017 产品定位定档：分档网关（卖档 Path B）、绝不降级、全透明市场顺延

状态：accepted（第 4 点的 BaseURL 与故障域归属由 [DEC-032](#dec-032-providerendpoint-承载-baseurl-与公共故障域修订-dec-017-第-4-点) 修订）

决策：

```text
Unio 的产品定位定档为「分档网关（卖档）」，既不是「中转站」，也不是
「全透明聚合市场（OpenRouter 式）」。本决策终止在三者之间反复摇摆。

1. 对外售卖单位是「模型 / 档」，不是「渠道」。
   - 每个对外可选项是一个独立 model 行（独立 model_id），例如
     gpt-5.5（经济档）与 gpt-5.5-official（官方档）各为一行。
   - 用户只看到模型 / 档这一个干净单位；provider 与 channel 全部对用户隐藏。
   - 「档」在数据层就是 model 行，不是代码里写死的枚举。新增供应商 / 新增档
     = 新增 model 行 + 绑定渠道 + 定价，不改代码。

2. 定价沿用 DEC-008（明确金额、无倍率），并补充「档内定价口径」：
   - 每档售价（prices）按「覆盖该档内最贵渠道成本 + margin」定，保证任意一次
     档内路由命中都不赔。
   - 同一模型的不同档可定不同价（经济档低、官方档高），由用户自行按价格 / 品质选档。
   - 客户售价在 authorization 阶段按 (model, 时间窗口) 锁定，与命中哪条渠道无关
     （承接 DEC-006 / DEC-008）。渠道成本波动只影响平台毛利，不改变用户账单——
     这从结构上解决了「同模型这次扣 5 刀、下次扣 10 刀」的不公平问题。

3. 路由与重试锁在档内（model_id 内），绝不跨档降级——产品红线：
   - FindRouteCandidates 只在「被请求 model_id 绑定的渠道集合」内选候选
     （承接 DEC-010 routing 规则）。
   - 失败重试（DEC-011 lifecycle retry / fallback）只在同一 model_id 的候选间进行，
     永不切换到更低价 / 更低品质的档。

4. provider / endpoint / channel / model 概念锚点（本项目内固定语义，防止再次混淆；由 DEC-032 修订）：
   - model：对外售卖的产品 / 档（model_id 对外、UNIQUE）；用户可见。
   - provider：供应商身份 / 记账主体（官方品牌或中转商）；用户不可见。
   - provider_endpoint：某 provider 下的一条 API Root，持有唯一规范化 base_url，并作为
     上游公共故障域；一个 provider 可以有多个 endpoint。用户不可见。
   - channel：某 endpoint 下的一组账号级路由事实（protocol + adapter_key + 凭据 + priority）；
     不再持有 base_url。同一 endpoint 可以有多条 channel（多 key）。用户不可见。
   - channel_models：channel ↔ model 多对多绑定 + upstream_model 名称翻译。

5. 全透明聚合市场（OpenRouter 式：摊开每个模型的渠道 / 价格、用户选路由偏好、
   成本 + 抽成定价）顺延为「有量之后的第二产品线」，当前阶段不做：
   - 现有后台（providers / channels / channel_cost_prices / FindRouteCandidates /
     熔断）本身已是该模式的后端骨架；将来若要做，只需新增「前台暴露层 +
     成本 + 抽成定价层 + 目录 / 可用率运营」，不需重写架构。
   - 渠道健康检测（成功率）与「价格优先 / 速度优先」作为后台路由策略 / M8 运营能力
     实现，不作为「用户在创建 API 时多选渠道」的产品入口（该入口会暴露 channel
     基础设施概念，并造成 channel ↔ model 多对多关系的反直觉体验）。
```

原因：

```text
1. 单运营者起步阶段，「厚利（渠道价差）+ 稳定账单」比「薄利（透传抽成）+ 全透明运营」
   更能先活下来。OpenRouter 的薄抽成模式依赖规模与批发议价，是「有量之后」的游戏，
   不是单人起步阶段的模式。
2. 「档 = model 行」把分组 / 分级表达成数据而非代码，既满足「用户自选官方 / 经济」的诉求，
   又避免把 channel（基础设施概念）暴露给用户所造成的多对多反直觉。
3. 路由 / 重试锁在档内，从结构上同时保证「绝不降级」与「账单稳定」，无需运营每次手工干预。
4. 把全透明市场显式记为「第二产品线、暂不做」，终止「中转站 vs 网关 vs 聚合市场」之间反复
   摇摆的决策成本——这是本决策最重要的收益。
```

影响：

```text
1. 阶段 13 后台按「卖档」形态推进：渠道 / 供应商管理是运营内部工具，不进客户可见面；
   未来客户可见面（console）只展示模型 / 档。
2. 不新增「用户多选渠道」产品入口；渠道健康检测归 M8，价格 / 速度优先归后台路由策略
   （如确需对外，后续做成 per-request sort 参数，而非 key 级渠道多选）。
3. 多档若需前端聚合展示（同一模型的多个档归到一张卡），需要一个非唯一的分组字段
   （canonical_id 是 UNIQUE 不可用），留待 console 设计时再定；当前 admin 按独立 model
   行展示。
4. DEC-006 / DEC-008 / DEC-010 / DEC-011 全部不变；本决策是它们在「产品定位 / 定价口径 /
   路由档界」层面的收束与锚定。
```

## DEC-018 上游 Responses 直传 + 第三方桥接分流（DEC-014 补充）

状态：accepted

决策：

```text
在 DEC-014「Responses ingress 下转 Chat Completions 桥接」之上新增一个维度：当上游原生支持
POST /responses（OpenAI 官方，或 Codex 标准中转）时，Unio 直连上游 /responses，请求 / 响应 /
SSE 事件零结构转换透传，不再「只下转 chat」。chat-only 第三方（DeepSeek 等）继续走 DEC-014 桥接。

分流发生在 service 层、按候选 adapter 能力判定，routing 不变：
  - 候选 adapter 注册了 Responses 直传能力（HasResponses/HasStreamResponses）→ 走直传 adapter，
    直连上游 <base>/responses；
  - 否则（chat-only）→ 落既有 responses→chat 桥接分支（即 DEC-014 现状）。
deepseek 等无直传能力者天然走 else 桥接，行为与现状逐字一致。

新增载体而非新协议族：在 core/adapter/openai/responses 子包定义 Responses 直传契约
（ResponsesAdapter / StreamResponsesAdapter / ResponsesInputTokenizer + Request/Response/StreamChunk
DTO），与 chat completions adapter 并列。直传 adapter 不依赖任何 ingress（gatewayapi）DTO；
ingress↔adapter 的请求 / 响应原文搬运由 service 编排层完成（依赖纪律沿用 DEC-002）。

零转换 + 仅改写 model 回显（直传保真）：
  - 出站：以客户原始请求体为基底零损耗重放，仅改写 model（→ candidate upstream_model）与
    stream（→ 本次调用方式）；其余字段原样转发，不二次结构化改写。
  - 入站：上游响应体 / SSE 事件 data 原文透传给客户，只把顶层 model 与嵌套 response.model
    回显改写为客户请求名（分档卖档需要回显客户模型名，承接 DEC-017）。
  - 直传流式：response.completed/incomplete 由上游下发，gateway 不二次补发收尾帧；
    桥接流式仍由 streamEncoder 在结算后补发 response.completed（DEC-014 行为不变）。

账务零改动（承接 DEC-010/DEC-014）：直传同样在 adapter 解析的同一次里抽取 adapter.ResponseFacts
（usage 由 responses 的 input_tokens/output_tokens/*_details 归一到 ChatUsage，与桥接侧 mapResponsesUsage
反向）；routing / authorization / settlement / recovery / 价格 / 成本快照全部复用，不新增账务 schema。
直传与桥接两类候选共享同一条 AttemptRunner 流式 fallback 循环（chunk 载体泛型化为类型参数 + meta
提取器，资金关键逻辑逐行不变），混合候选池在「首字节前」仍可互相 fallback。

channel 零新字段：直传与桥接的区别只体现在 channel.adapter_key——绑定 openai-responses 即走直传，
绑定 deepseek（或其它 chat adapter_key）即走桥接。不在 channel 上加 upstream_endpoint 等标记，
新增真实第三方直传渠道 = 配 adapter_key=openai-responses 的 channel，不改代码。
```

原因：

```text
1. 部分上游（OpenAI 官方、Codex 中转）原生 /responses；对它们做 responses→chat→responses 双向
   翻译是无谓的有损往返（reasoning / encrypted_content / namespace / 内置工具语义在压平到 chat
   时不可避免地降级）。直传零转换是这些上游的保真上限，也最贴近真实 Codex CLI 行为。
2. 协议分离落在 adapter 层、生命周期统一落在 AttemptRunner——既拿到「直传保真」，又不牺牲
   Unio 既有的统一计费 / 审计 / fallback / recovery（DEC-010 边界）。
3. 「按 adapter 能力分流」让两条路径在一个候选池里共存：同一 model 可同时绑定官方直传渠道与
   第三方桥接渠道（分档卖档，DEC-017），routing 无需感知协议差异。
```

影响：

```text
新增 core/adapter/openai/responses 子包（contract/adapter/stream/usage/tokenizer/errors）与
service/gateway/openai/responses 的分流分支（create_response/stream_response/direct_response）；
registry 增 Responses/StreamResponses/ResponsesInputTokenizer 槽与 Has* 访问器；
lifecycle.AdapterRegistry 增 responses-serve capability（含 stream 变体）与 HasAny 识别；
ingress ResponsesRequest/Response/StreamEvent 增 raw 原文透传（自定义 MarshalJSON），
bootstrap 注册 adapter_key=openai-responses。DEC-010/DEC-014 边界不变；DEC-014 的桥接路径
逐字保留为 chat-only 第三方的 else 分支。
保真欠账登记为 GAP-11-012（直传下 namespace/encrypted_content 等保真细节，仅做 model 回显改写，
不解析重排上游其它字段）。
```

增补（2026-06-13，注册形态收敛，行为不变）：

```text
原设计把 OpenAI 1P 拆成 adapter_key=openai（仅 chat 三槽）与 adapter_key=openai-responses
（仅 responses 三槽）两个 key，导致同一个 OpenAI 官方上游要配两个 channel、且每个 channel 只能
服务一个端点。现合并为单个 adapter_key=openai（chat 三槽 + responses 三槽），一个 channel 即可
同时直传 /chat/completions 与 /responses；openai-responses key 移除。

分流机制与账务一律不变：仍按候选 adapter 是否注册 responses 直传能力（HasResponses）决定
直传 vs 桥接；DEC-018 「按 adapter 能力分流」原样保留，仅注册形态从「两 key」收敛为
「一 key 双能力组」（Registration 本就支持同 key 注册两组能力）。

同时把 channels.adapter_key 在 admin 创建时改为可选：留空默认取 protocol 同名的忠实透传
adapter（openai→openai、anthropic→anthropic），普通 OpenAI/Anthropic 兼容上游（含中转站）免填；
仅需特殊方言/Drop 的上游才显式指定。channels 表结构与 NOT NULL 约束不变（默认值在 admin 层填充）。

若将来接入「只会 /responses、不提供 /chat/completions」的上游（如纯 responses codex 中转），
再单独注册一个仅含 responses 三槽的 adapter_key 即可，与本次合并不冲突。
```

## DEC-019 可配置请求体上限 + Compact 双路径（Native 透传 / Synthetic 降级）

状态：accepted

决策：

```text
针对 Codex 长会话两类现场问题（整改方案见 docs/production/REMEDIATION-context-compaction-and-payload-limit.md）：

A. 请求体上限可配置（GAP-11-013）
  - httpx.DecodeJSON 原硬编码 1MB（DefaultMaxJSONBodyBytes）导致长会话 /responses 与 /compact
    在网关入口 413（与模型 context window 无关）。
  - 新增 HTTP_MAX_JSON_BODY_MB（单位 MB，默认 128，对齐 new-api 方向）经 config 注入进程级
    httpx.SetMaxJSONBodyBytes（gateway / admin server 启动期各设一次），保留 1MB 作未配置 fallback。
  - 解压后大小仍受 http.MaxBytesReader 约束，超限稳定 413（OpenAI-compatible）；前置代理
    client_max_body_size 须 ≥ 此值（文档 + .env.example 注明）。属网关安全/稳定性配置，非业务计费。

B. Compact 双路径（GAP-11-014），与 DEC-018 直传/桥接分流对称
  - NativeCompact：选中渠道 adapter 注册了原生 /responses/compact 能力（adapter_key=openai）→
    原文透传上游 POST /responses/compact，响应原样返回（仅改写顶层 model 回显），保留上游加密
    compaction + encrypted_content，能力等于上游本身。
  - SyntheticCompact：chat-only 第三方（DeepSeek 等）→ 沿用 DEC-014 chat 摘要降级（迁自原
    compact_response.go → compact_synthetic.go），单条 assistant message 承载摘要，不签发加密
    compaction item（Synthetic 永久限制，GAP-11-007）。
  - 运行期分流以 adapter 代码能力 HasResponsesCompact 为准（与 DEC-018 直传分流以 HasResponses
    为准一致），不以 DB 能力位作强制前置；能力 key responses.compact.native /
    responses.compact.synthetic（CAPABILITY_KEYS v1.1）用于契约/矩阵声明与可观测。
  - 只有 Native 真实返回 404/405（或 adapter 对该状态的等价 sentinel）且 compactNativeFallback=true
    时才回落 SyntheticCompact 并打 warn 日志。2xx 缺可计费 usage、响应解析失败以及其它上游错误
    （鉴权/限流/超时/5xx）均不得二次调用 Synthetic，按原错误与风险暴露规则收口。
  - 两条路径共用 runNonStream 资金关键 scaffold（routing / authorization / settlement / 终态），
    与 CreateResponse 共享候选 fallback 计费循环；但每次真实 transport 必须独立取得 permit 并创建 attempt。
    Native 404/405 必须先终结第一条 attempt，再回到 lifecycle 为 Synthetic 重新准入，禁止 adapter 内二次 HTTP。

非目标（本阶段不做，单独 follow-up）：context_management.compact_threshold 服务端自动压缩；
BM25 / middle-out 截断；gateway gzip 解压中间件；Synthetic 路径解析入站 type:compaction item（整改 Q3）。
```

原因：

```text
1. 1MB 硬限制与 compact 在入口被 413 互相锁死（compact 也要读完整 input[] → 历史无法缩短 →
   后续请求继续膨胀）。把上限做成可配置安全阈值，是消除现场 413 的根因修复。
2. 多上游网关普遍「能透传则透传，不能则网关自实现」：OpenAI/Codex 原生 compact 直传可拿到官方
   加密 compaction 语义，第三方无此 API 时摘要降级仍可用。与 DEC-018 主路径分流哲学一致，避免
   compact 成为唯一不分流的端点。
3. 分流以 adapter 代码能力为准（而非 DB 能力位强制前置）沿用 DEC-018 既有事实模式，避免「要求
   能力位 → 无能力位模型在 enforce 下被拒」与「需要 Synthetic 兜底」自相矛盾；能力 key 仍登记，
   供矩阵/可观测与未来按模型细粒度开关。
4. Native 明确返回 404/405 时默认回落 Synthetic，保证上游确实不支持 compact 时 Codex 不断链（可由运营关闭）；
   已可能产生上游成本或属于其它故障的结果不回落，避免重复调用和成本暴露。
```

影响：

```text
config 增 HTTP.MaxJSONBodyBytes（HTTP_MAX_JSON_BODY_MB）；httpx 增进程级可配置上限 +
SetMaxJSONBodyBytes/MaxJSONBodyBytes，bootstrap gateway/admin 启动期注入。
capability 增 responses.compact.native / responses.compact.synthetic（CAPABILITY_KEYS v1.1，33 个 key）。
core/adapter/openai/responses 增 ResponsesCompactAdapter 契约 + Adapter.CompactResponse（compact.go）
+ sentinel ErrCompactUnsupported；openai.Registry 增 responsesCompact 槽与 ResponsesCompact/
HasResponsesCompact，bootstrap 为 adapter_key=openai 注册该槽。
service/gateway/openai/responses 抽出共享 runNonStream scaffold（create_response.go），compact 编排拆为
compact_orchestrator.go（双路径选路）+ compact_synthetic.go（迁自 compact_response.go）+ compact_native.go
（不支持判定）；删除 compact_response.go。ingress CompactHistoryResponse 增 raw 原文透传（RawCompactHistoryResponse
+ 自定义 MarshalJSON）。P4 实施必须把当前 invoke 闭包内的 404/405 二次 HTTP 移回 lifecycle，分别记录
responses_compact/chat_completions permit 与 attempt；DEC-010/DEC-014/DEC-018 边界不变，账务口径不变。
```

## DEC-020 被动证据式模型能力自动校正

状态：**superseded**（2026-06-23 → [DEC-024](#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明)）

决策：

```text
现场：observe 闸门刷「model capability unavailable」WARN——模型 model_capabilities 手工声明不全，
与真实请求所用能力不一致；第三方中转上游能力难手工对全。设计见
docs/production/DESIGN-capability-autocalibration.md（已审核，Q1~Q7 全按推荐默认）。

新增「能力自动校正」后台任务：被动从 request_attempts(status=succeeded) + usage_records 学习模型
实际具备的能力并补齐 model_capabilities。纪律：被动（不主动探针、零额外上游成本）、增量（watermark）、
证据式、add-only、manual 永远优先、per-model 可控、全程可审计可撤销。

per-model 开关 models.capability_autocalibrate ∈ {off, suggest（默认）, auto}：
  - off：不学习；suggest：只产生建议待 admin 一键采纳；auto：强证据自动补、弱证据仍只建议。

证据分级（关键安全点：请求成功 ≠ 该能力真被支持）：
  - 强证据（响应真用到）：finish_class=tool_use → tools.function/custom/parallel/choice_required；
    usage cache_read>0 → prompt_cache；reasoning_output_tokens>0 → reasoning.effort/budget。
  - 弱证据（带字段且成功但未体现用到）：builtin web_search/mcp、encrypted_content 等 → 只建议。
  - 自动补全部前置：档位 auto + 强证据且比例≥阈值 + 非 limits 维度键 + 模型单渠道（多渠道只建议，
    避免强渠道能力误套弱渠道）+ 该 (模型,能力) 无 manual 声明（manual 永远优先）。

规模化：增量 watermark（只扫新行）+ rollup 聚合表（成本与历史总量解耦）+ 单轮变更封顶。
工程现实：finish_class=tool_use 只证明「某工具被调」，无法精确区分 function/custom；Responses 直传上游
的 finish_class 恒为 stop（不出 tool_use），故 tools.* 在该类上游恒弱证据（只建议）。要让 tools.* 也能
强证据自动补，需给 adapter 加「按 key 命中埋点」（后续 TASK-H）。
```

原因：

```text
1. 真实流量已把模型实际能力暴露出来（request_attempts.required_capabilities + status + finish_class
   + usage 已持久化），被动挖掘比让运营手工对齐第三方中转能力更准、零成本。
2. 「证据式 + 强/弱分级」直面「成功≠支持」陷阱：只有响应真用到才敢自动补，其余只建议，避免过度声明在
   enforce 或路由信任时反噬。
3. add-only + manual 优先 + 单渠道才 auto + 封顶，是规模化与误判的护栏；只在 observe 期有用，契合
   「observe 学习 → 声明稳定 → 切 enforce」的上线节奏。
```

影响：

```text
迁移 000037（models.capability_autocalibrate 列）+ 000038/039/040（model_capability_observations rollup /
model_capability_suggestions / capability_calibration_state watermark）。新增 core/capability/calibration
（BuildPlan 纯函数 + Store + Calibrator，含 DryRun）+ sqlc 查询；core/capability.Store 增 suggestion 读写；
app/workers 增 capability_calibration_worker；bootstrap 增 NewCapabilityCalibrator + 条件注册；
cmd/worker-server 增 calibrate-capabilities --dry-run；config 增 CapabilityAutocalibrateConfig（默认关）；
admin 增能力建议列表/采纳/忽略（/capability/suggestions、/models/{id}/capability-suggestions/{key}/{accept,dismiss}）。
登记 GAP-12-013（含 TASK-H 残留）。能力 key 注册表不变（不新增 key）；账务/路由/enforce 边界不变。

补充交付（2026-06-18，闭合 DESIGN 风险 A + add-only 退化护栏）：
- 多实例分布式锁：迁移 000041 给 capability_calibration_state 加 locked_by/locked_until 租约列；calibration.Lease
  （DB 行锁，抢到才跑、运行中按 TTL/3 续租、丢锁即中止本轮、崩溃后据 TTL 自动释放）；config 增
  CAPABILITY_AUTOCALIBRATE_LOCK_TTL（默认 10m）；lock=nil 退化为单实例语义。解决 cron 重入 / 多实例并发重复计 rollup。
- 上游退化告警：calibration.DetectDegradations 纯函数——对已声明的强证据能力，本轮新窗口成功量达阈值但证据比例塌陷
  （< 0.2）即出告警（alert=capability_upstream_degradation），worker + CLI 打日志。坚持 add-only：只告警，绝不据此
  自动下调/删除能力声明（删除永远人工，防一次抖动误删）。
```

## DEC-021 看板「经营驾驶舱」三层架构：按币种拆卡不引汇率

状态：accepted

决策：

```text
M9 工作台看板（DEC-017 分档网关的运营内部工具）从「数据展示页」升级为「经营驾驶舱」三层信息架构。
设计与全文口径见 docs/chapters/phase-13-admin/DESIGN-dashboard-business-overview.md，§9.1 推荐结论
经 owner 确认定稿，本决策锚定其四项口径：

1. 三层信息架构（首页是决策层，不是数据仓库）：
   - 第一层 首屏决策层（DB，/dashboard/overview）：8 KPI + 本期/上期环比 + 状态 Banner（≤30 秒看完）。
     8 KPI：收入 / 毛利 / 利润率 / 缓存贡献（估）/ 请求数 / 成功率 / 异常请求 / 客户余额池。
     首屏不放任何明细表、排行榜、Token 拆解、逐渠道明细。
   - 第二层 二级分析中心（DB + rollup，独立路由/Tab）：利润 / 渠道 / 模型 / 缓存 / 用户 / 异常中心。
   - 第三层 实时监控页（Prometheus，独立路由）：QPS/TPS/RPM/TPM/P99/错误率，不走 DB 聚合。

2. 金额口径：按币种拆卡，不引汇率。
   沿用 DEC-008/DEC-017「按币种分组、绝不跨币种相加」。引汇率= 把一个会过期/有误差的外部事实
   引进资金展示面，不值。首页 KPI 卡默认展示主币种（USD，可配），其余币种折叠在卡内展开。
   利润率 / ROI 是同币种内的比值，天然无跨币种问题。→ 不做主币种折算、不引汇率源。

3. 付费率定义：区间内有 debit（实际消费）的去重用户 / 区间内有请求的去重用户。
   「有余额」会把充了钱没用的算进来失真；「有充值」依赖充值事实另算。先用「有消费」最贴近经营意义。

4. 缓存贡献是反事实估算，不是账本事实：
   节省 = cache_read_tokens ×（未缓存单价 − 缓存读单价），是「若不命中会多花多少」的假设值，
   账本里不存在这笔钱。UI 必须标「估算」，不与真实利润 / 账本金额同列误导。

5. rollup 保留期：hour 桶保留 90 天，day 桶永久。
   hour 桶用于近期细看（≤90 天足够），day 桶用于长期趋势永久留存；超 90 天 hour 桶由 worker 定期清理。

6. AI 经营摘要本轮顺延（依赖二级聚合先就绪 + 涉及 LLM 调用 / 缓存 / 成本，单独立项）。

7. 三类无数据源指标移出本轮、单独立项：供应商（上游）余额 / 可用天数（providers 无余额字段）、
   在线用户 / 排队请求（无运行时 gauge 埋点）、熔断 / 降级状态（无状态机快照）。
```

原因：

```text
1. 产品侧「16 KPI + 11 中心」清单跳进了它要解决的坑——把所有东西堆进首页反而让运营更迷茫。
   真正的解法是分层（决策层 / 分析层 / 实时层），不是堆叠。
2. 单运营者起步阶段，资金展示面追求账务清晰可审计（DEC-008）；按币种拆卡 + 不引汇率，避免把
   外部汇率误差 / 过期风险引进毛利与余额展示。
3. 三类指标当前确无数据源，硬做会产生假数据；显式移出、单独立项，避免污染首屏可信度。
```

影响：

```text
1. 阶段 13 新增 TASK-13.09（看板升级三层架构），切片 D1–D5：
   - D1 决策层重构已落地（Slice 8，2026-06-18）：扩展 /dashboard/overview（current/previous 环比 +
     margin_rate 派生 + 缓存贡献估算 + health 状态）+ 前端 DashboardPage 重排为 8-KPI 决策层。
     后端 go build/test 绿、前端 tsc/eslint/vite build 绿。
   - D2 rollup 基础设施、D3/D4 二级中心、D5 实时监控页登记 GAP-13-001~003；AI 摘要 GAP-13-004；
     三类无数据源指标 GAP-13-005。
2. DEC-008 / DEC-017 不变；本决策是它们在「看板展示口径」层面的收束。
3. 环比 query D1 暂直扫原表（低量可上），上量前迁 rollup（GAP-13-001），首页不扫原表。
```

---

## DEC-022 能力证据体系 v2：used_capabilities 闭环 + 证据注册表

状态：**superseded**（2026-06-23 → [DEC-024](#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明)）

决策：

```text
现场：DEC-020 校正上线后，除 prompt_cache 外几乎全是「弱证据·证据 0」。根因是证据链 L1→L2→L3 断链
（adapter 已解析 ResponseFacts.UsedCapabilities，但 settlement 未落库、校正 SQL 未读取，tools.* 仅看
finish_class，而 Responses 直传 finish_class 恒为 stop）。设计见
docs/production/DESIGN-capability-evidence-v2.md（implemented，Q1~Q6 定稿）。

把 TASK-H 升级为四层闭环：
  L1 生产：OpenAI Responses/Chat adapter 填满 ResponseFacts.UsedCapabilities（含 stream 族、
    reasoning.summary、responses.encrypted_content）。
  L2 落库：settlement 成功路径 + recovery 重放写 request_attempts.used_capabilities；新增
    delivery_mode（迁移 000043，stream|batch，仅审计/二级佐证，不作证据来源）。
  L3 注册表：新增 internal/core/capability/evidence——每个 key 有稳定 Tier（Strong/Medium/Weak/Manual）
    + HasStrongEvidence 纯函数判定，calibration 统一委托（删除各自硬编码 map）。
  L4 消费：校正 SQL/store/calibrator 改读 used_capabilities + upstream_protocol + delivery_mode；
    rationale 带 tier/evidence_source；dry-run 与 worker 完成日志输出 tier 与 strong/weak 分布。

关键证据规则（对 DEC-020 v1 的修订）：
  - tools.*（function/custom/parallel/choice_required）：主路径一律 used_capabilities 命中；唯一回退是
    Chat 的 tools.function（finish_class=tool_use + required），custom/parallel 禁用 finish_class 笼统归因。
  - builtin tools（web_search/file_search/code_interpreter/computer_use/image_generation/mcp）：升 Strong，
    但仅凭 used_capabilities（Responses 终态 output *_call item 是服务端执行后回填，证明真执行；
    Chat finish_class 不得用于 builtin）。随之 builtin 也纳入退化告警。
  - stream/stream.tools/stream.usage：定为 Medium——只建议、永不 auto、不参与退化告警（Q6：流式是分发
    方式而非模型能力，Codex 近乎恒流式无区分度且偶发 buffer 会误报退化）。
  - prompt_cache（cache_read>0）/ reasoning.effort,budget（reasoning_tokens>0，带 limits 维度仍只建议）不变。

首验范围：OpenAI 协议族（Responses + Chat），Codex E2E。Anthropic 对等留 Phase 5。
```

原因：

```text
1. 证据本就被 adapter 解析出来了，只是没流到校正侧——补三段管线即可让 tools.* 在 Codex 流量产生真证据，
   消除「证据 0」误报，并为切 enforce 把声明对齐。
2. 证据规则集中到注册表（纯函数、可单测）杜绝多处硬编码漂移，是 DEC-020「证据式、可审计」的自然延伸。
3. builtin 升 Strong 安全，因为换了证据来源（响应 output item = 服务端执行结果，而非请求声明）。
4. stream 故意不升 Strong：它是分发方式，纳入 auto/退化只会噪声化，Q6 定稿降 Medium。
5. delivery_mode 与 used 含 stream 冗余，故仅作审计、不作证据，HasStrongEvidence 不依赖它。
```

影响：

```text
1. 迁移 000043 给 request_attempts 增 delivery_mode（NOT NULL DEFAULT 'batch'）。used_capabilities 列
   早在 000042 就位，本次接通写入与读取。
2. 新增包 internal/core/capability/evidence（evidence.go/produce.go + 单测）。calibration 删除
   strongEvidenceKeys/toolCallEvidenceKeys/limitedDimensionKeys/AttemptHasEvidence，改委托 evidence。
3. 退化告警语义扩展：builtin 强证据键纳入；stream 永不纳入。calibration_test 相应更新。
4. P4-2 Prometheus 指标降级为结构化日志（批处理 worker 无 /metrics，价值低管线重）；rationale 的
   tier/evidence_source + worker/dry-run 日志提供等效可观测。
5. 顺带修复前置 groundwork 遗留的测试编译破损（CreateRequestAttemptRow→RequestAttempt 用 convert 助手、
   admin fakeStore 补 autocalibrate 方法）。go build/vet/单测全绿。
6. 待续：Phase 5（Anthropic Messages 对等）、真实非 dry 运行 / HTTP E2E（worker 默认关闭，客户启用后复跑）。
```

---

## DEC-023 移除能力架构 Layer 3「渠道能力收紧」

状态：implemented

决策：

```text
移除 DEC-015 能力架构 Layer 3（channel_capability_overrides，渠道能力收紧）。默认所有渠道完美支持
模型层（model_capabilities）声明的全部能力；能力闸门判定只看模型层。

具体移除：
  - DB：迁移 000044 DROP TABLE channel_capability_overrides（down 重建，结构同 000024）。
  - 数据访问：删除 sql/queries/channel_capability_overrides.sql + sqlc 生成物 + core/capability.Store 的
    ListChannelOverrides/UpsertChannelOverride/DeleteChannelOverride 与 ChannelOverride/UpsertChannelOverrideParams 类型。
  - 闸门：capability.Evaluate 去掉 channels 入参与 channel 评估，删除 GateResultChannelUnavailable /
    Evaluation.MissingChannel / capability.ChannelCaps；capabilitygate.Checker 不再读渠道 override。
  - 路由：CapabilityCheckInput 去 ChannelIDs、CapabilityObservation 去 MissingChannel、enforce 去
    channel 分支、删除 routing.ErrChannelCapabilityUnavailable 与 failure.CodeRoutingChannelCapabilityUnavailable；
    三个生成执行面 handler 的能力错误渲染只保留 model 分支（对客户文案不变）。
  - admin：删除渠道收紧 service 方法 + adminapi 端点（/admin/v1/channels/{id}/capability-overrides）+
    前端 ChannelCapabilityOverridesDialog 与 capability.ts 渠道 override API + ChannelsPage「能力收紧」入口。
  - keys：删除 IsValidChannelOverrideLevel。

能力自动校正（DEC-020/DEC-022）不受影响：仍按 (model, channel) 学证据，auto 仅对单渠道模型补模型层声明
（渠道维度只用于安全判定，不写渠道 override）。
```

原因：

```text
1. 渠道收紧是 opt-in 的：不配 override 即等于「渠道完美」，本就是默认行为。运营负担来自「可能要手工维护
   (channel × capability) 矩阵」，而非该能力存在本身。本项目当前阶段假设渠道均高质量、能力同质，YAGNI。
2. Layer 3 的「(channel × capability) 一刀切、不分模型」对「同渠道多模型能力不一致」表达力有限（需拆渠道），
   维护心智与价值不匹配；删除后心智回归单层（模型层）声明，最简单清晰。
3. 真正消除运营负担的方向是「自动化产出建议」（DEC-020/DEC-022 校正），而非让人手填收紧矩阵。
```

影响：

```text
1. 用户（owner）明确选择「彻底移除」（非仅停用），故连带删除已不可达的路由错误码/sentinel/枚举，保持代码整洁。
2. 不可逆需重建时：迁移 000044 down 重建表；其余按 git 历史恢复。
3. enforce 语义收窄为只判模型层；observe 默认行为对客户无变化（渠道不支持的请求仍由上游报错，与移除前 observe 一致）。
4. go build / go vet / 单测全绿；前端 tsc / eslint / vite build 全绿。
5. 关联：DEC-015（Layer 3 标注已移除）、CAPABILITY_KEYS.md（去渠道收紧层）。
6. 注：DEC-023 原因 §3 曾指向 DEC-020/DEC-022 校正作为运营减负方向；该方向已被
   [DEC-024](#dec-024-移除能力自动校正与能力闸门改为能力字典--人工声明) 取代（删除校正、改能力模板 + 人工声明）。
```

---

## DEC-024 移除能力自动校正与能力闸门，改为能力字典 + 人工声明

状态：**accepted**（2026-06-23 定稿，待 P1–P4 实施）

决策：

```text
彻底移除两套机制——① 能力自动校正（DEC-020/DEC-022/证据 v2 全栈）② 能力闸门（DEC-015 observe/enforce
+ required 推断 + capability_signals + 闸门 metric/审计列）。capability 子系统退化为「纯数据/文档」（不再有预检/拒绝/observe）：
新增 capability_keys 字典表作为合法 key 唯一真源（取代 keys.go，带中文描述供运维区分），
model_capabilities 收敛为「创建/采纳时人工声明 + 展示」。注：model_capabilities 仍有读取者——
面向客户的 /v1/models（cap-tags + ?capability= 过滤），走独立 SQL 直读、不经闸门，删闸门不影响、必须保留。
Admin 新建模型合并为下拉「自定义 | 从模型目录同步」。

删除自动校正（破坏性，迁移 000045+）：
  - 表：model_capability_suggestions、model_capability_observations、capability_calibration_state；
    models.capability_autocalibrate；request_attempts.used_capabilities/delivery_mode；
    settlement_recovery_jobs.used_capabilities。
  - 代码：core/capability/calibration、evidence、calibration worker、calibrate-capabilities CLI、
    used_capabilities 生产链、Admin suggestions/autocalibrate API 与前端。

删除能力闸门（本轮新增）：
  - 表：request_attempts.required_capabilities（及 request_records 同列，如有）。
  - 代码：core/capability/gate.go(Evaluate)、inference.go(Infer/InferLimits/RequestSignals)、keys.go（整体）、
    set.go（若仅服务闸门）；service/gateway/capabilitygate 整包；app/gatewayapi/*/capability_signals.go ×3；
    routing 的 CapabilityChecker/Enforcement/observeCapability/enforceCapability/相关错误码；
    service/admin/capability/enforcement.go + enforce 开关 UI；requestlog 的 capability 持久化；
    闸门 metric(IncCapabilityCheck/Required/Missing)；三个生成执行面 service 的 required 构造与 enforce 渲染分支。
  - keys.go=活法二：删除 Key 常量/registeredKeys/IsRegisteredKey；幸存生产者（adapter capability_profile ×2、
    目录 coarseCapabilities）改用字符串字面量；加一致性测试断言其 key ∈ capability_keys 字典（补编译期检查缺口）。
  - 保留：SupportLevel 枚举（迁 support_level.go，model_capabilities 仍校验档位）；adapter dropUnsupported（DEC-012 出站 Drop，与闸门无关）。
  - 不动：/v1/models 的 cap-tags + capability 过滤（models/handler.go、catalog.ListAvailableModels、
    ListAvailableModelsForProject SQL），它直读 model_capabilities、不经闸门。

新增 capability_keys 字典表（迁移）：
  - 列：key(PK)、domain、display_name、description(中文)、sort_order、deprecated、时间戳；seed 当前 33 个 key。
  - model_capabilities.capability_key FK→capability_keys(key)（ON DELETE RESTRICT，软退役用 deprecated）。
  - IsRegisteredKey 改查字典（带缓存）；keys.go 常量真源废止。
  - Admin 最小做 GET /admin/v1/capability/keys；写操作（POST/PUT/DELETE）按需。

数据三件套（详见 DESIGN-capability-manual-declaration.md）：
  ① capability_keys — 合法 key 唯一真源 + 中文描述；
  ② model_catalog_capabilities — 目录粗能力提示（阶段 14）；
  ③ model_capabilities — 声明/展示，support_level/limits 创建或采纳时人工填；读取者为 /v1/models + Admin 矩阵（不经闸门）。

实施分期：P1 删校正 → P2 删闸门 → P3 建字典表 + 切 key 来源 → P4 新建下拉/清理 UI。
```

原因：

```text
1. 自动校正：Codex 真实流量下长期「成功 N · 证据 0」，运营无法理解/信任；证据 v2 补链仍依赖复杂
   流式语义与 finish_class 边界，维护成本高于价值。
2. 能力闸门：enforce 全程未开（CapabilityEnforcement 零值=observe），生产里只产 observe WARN 与 metric，
   不拒绝请求、不参与选路——对线上零功能性作用，仅噪声与维护成本。删除后生产请求行为零变化。
3. 产品方向是「以官网/models.dev 为准、人工声明」，且要求「新增一个能力只改数据不改代码」。
   保留闸门时新 key 要参与拦截仍需写 3 协议解析代码；删闸门后 capability 变纯数据，新增能力 = 插字典行，零代码。
4. capability_keys 字典带中文描述，运维可直接区分 OpenAI/Anthropic 等语境，无需读代码。
5. 删除为破坏性但可接受（开发期）：suggestions/observations/used/required 数据可丢；down 仅恢复 schema。
```

影响：

```text
1. superseded：DEC-020、DEC-022、DESIGN-capability-autocalibration.md、DESIGN-capability-evidence-v2.md。
2. 部分 superseded：DEC-015——能力闸门(observe/enforce)删除；「模型层能力声明」以纯展示形式保留。
3. 有效延续：DEC-023（无渠道收紧）、阶段 14 catalog。
4. CAPABILITY_KEYS.md 降级为人类参考；权威 key 列表移到 capability_keys 字典 seed。
5. 删闸门后 gateway 不再有能力预检/拒绝/observe；如需「客户请求了哪些能力」可后续 ingress 轻量打点，不依赖闸门。
6. 若未来需要预检/选路/被动学习，须新开设计，不得直接恢复已删表与代码。
```

设计文档：[DESIGN-capability-manual-declaration.md](DESIGN-capability-manual-declaration.md)

---

## DEC-025 Stream partial settlement

状态：**accepted**（2026-06-25 定稿，待 [TASK-7.23](../chapters/phase-07-billing-ledger/PLAN.md#task-7-23-stream-partial-settlement) 实施）

决策：

```text
流式请求在「已向用户 emit 可见内容（emitted）、但 streamFacts == nil（无 adapter final usage）」时，
不再 release + risk_exposure，而是合成 partial_stream_estimate 的 ResponseFacts，
走与 full bill 相同的 SettleSuccessfulChat → Capture 管道；
captured_amount = min(estimated_charge, authorized_amount)。

A1 终态：partial 后 usage/price/cost/ledger 仍走同一条 settlement 管道，但 request/attempt
生命周期终态按原因落库：
- 客户端取消（stream_client_canceled_without_final_usage）：status=canceled，指标 Outcome=Canceled。
- emit 后上游/链路中断（stream_interrupted_without_final_usage）：status=failed，指标 Outcome=Failed。
- adapter 正常结束但上游未返回 final usage（stream_final_usage_missing）：status=succeeded，指标 Outcome=Success。
attempt 列 final_usage_received=FALSE 承载「没拿到 usage」这一事实。

结束原因靠合成 Finish.RawReason → upstream_finish_reason 区分：
- 路线 B1：客户端取消。
- 路线 B2：emit 后中断。
- 路线 D：adapter 正常结束但上游未返回 final usage。
  渠道异常复用现有口径（succeeded + final_usage_received=FALSE + 该 finish reason），
  不写入 risk_exposure，不新增 Admin 专用指标。

B4：full vs partial 只看 streamFacts == nil；finalUsage 永不参与计费。
首 token 前结束（!emitted）：普通 release，0 扣费，不写 risk_exposure。
有 final usage（streamFacts != nil）：永远 full bill（upstream_stream），不因 cancel 降级。
合成 facts 在 lifecycle 层补齐字段以通过现有 settlement 校验（A2-i）；settlement 扩展 final status 入参以区分 succeeded/failed/canceled。
```

原因：

```text
1. 用户已消费部分内容却 0 扣费，平台在 cancel 场景下承担不可持续的 margin 风险。
2. 预授权 + Capture + write_off 闭环已在 TASK-7.17 落地，DEC-003 当时「无 freeze 不敢估算」的前提已变化。
3. 客户端 ctx 已绑定上游 HTTP；用户取消后上游停止，partial 估算是「已产生可见服务」的合理对价，
   而非 new-api 式「上游继续跑用户无感知」。
4. capped 估算 + usage_source 审计，可在误扣风险与平台贴钱之间折中。
```

影响：

```text
1. 部分修订 DEC-003：emitted + streamFacts==nil（路线 B 与 D）均 partial settlement。
2. 新增 usage.SourcePartialStreamEstimate（partial_stream_estimate）。
3. settlement 支持 settled succeeded / failed / canceled 三种终态；partial 均传 final_usage_received=FALSE。
4. partial 要在两处独立循环实现：RunStreamGeneric（OpenAI chat / Responses 直传 / Responses→chat）
   与 message_stream.go（Anthropic Messages，独立实现，非共享 RunStreamGeneric）。
   两处把 cancel/interrupt/缺 usage 三分支按 emitted 分流为 partial 或 release；
   interrupt 分支需重排普通 MarkAttemptFailed，使 partial 能先结算 usage/ledger，再由 settlement 写 failed 终态。
5. StreamChunkMeta 增加可见文本字段；输出 token 复用 adapter tokenizer 增量计数（偏保守）。
6. 渠道异常复用现有 attempt 错误/健康口径（succeeded + final_usage_received=FALSE +
   upstream_finish_reason=stream_final_usage_missing，统计渠道故障时无需包含 failed/canceled partial）；无专用 Admin 计数。
7. 已 emit 的无 usage 流不再写 risk_exposure。
```

设计文档：[STREAM_PARTIAL_SETTLEMENT.md](../chapters/phase-07-billing-ledger/STREAM_PARTIAL_SETTLEMENT.md)

## DEC-026 产品转向 new-api 式分档网关：线路=分组（挂 Key）+ 倍率定价（超越 DEC-008、修订 DEC-017）

状态：accepted（2026-06-29 定稿）

决策：

```text
第一产品线转向 new-api 式「分档网关」，尽快上线；第二产品线顺延为 OpenRouter 式透明聚合
（差价 + 抽成两条腿），复用同一路由水管（承接 DEC-017 第 5 点）。

1. 「档」= 线路 route = 客户可见的分组，选在 API Key 上（一个 key 一条线路）。渠道对客户隐藏。
   沿用现有 routes / route_channels / api_keys.route_id / projects.default_route_id，不退役。
2. 倍率定价（超越 DEC-008「第一版不支持倍率」）：客户售价 = 模型基准价(model_prices, 明确金额向量)
   × 线路倍率(routes.price_ratio, 标量)。渠道只记成本（channel_prices 退化为成本表）。
   售价按 (线路, 模型, 时间) 锁定，fallback 命中别的渠道客户价不变（保留 DEC-017 稳定账单）。
   审计补偿：price snapshot 记 model_price_id + route_id + price_ratio，历史账单可复算。
3. 档的载体从 model_id 改为 Key 上的线路（修订 DEC-017 第 1 点：档=独立 model 行）。
   原因：Codex 等客户端写死模型名、自动请求一族模型，无法用「模型名后缀档」选档。
4. 路由锁档内：线路池内服务该模型的渠道中按 成本最低/健康/固定 挑一条；cheapest 口径由「售价」改「成本」。
5. 不做「用户多选渠道」（守 DEC-017 第 5 点，不暴露渠道基础设施）。
```

原因：

```text
1. 市场窗口紧，new-api 式分组是已验证、能最快上线变现的形态；倍率配置少、运营快。
2. Codex 等固定模型名客户端，从产品上否决了 DEC-017「档=model_id」的写法（实测击穿）。
3. 厚利（差价）先活下来；透明聚合（薄利+规模）作为第二产品线顺延，两套共用水管不重写。
```

影响：

```text
1. 废弃旧设计（DESIGN-key-channel-routing / EXECUTION-PLAN-key-channel-routing，已删）；
   新方案见 DESIGN-route-group-pricing.md。
2. 新增 model_prices（模型基准售价 versioned 向量）+ routes.price_ratio（线路倍率标量）。
3. channel_prices 去售价列、退为成本表；毛利不再 DB CHECK 保证，改录入期应用校验 + 运行期 write_off。
4. settlement：售价 = 基准 × 倍率；price snapshot 记 model_price_id + route_id + price_ratio。
5. 路由 cheapest 由「售价升序」改「成本升序」。
```

设计文档：[DESIGN-route-group-pricing.md](DESIGN-route-group-pricing.md)

## DEC-028 缓存感知 TPM：排除 cache_read、保留 cache_write；预占在未结算时释放（补充 DEC-027）

状态：accepted（2026-07-01 定稿；P4 候选级原子准入与资源所有权由 [DEC-041](#dec-041-候选级资源原子准入与统一-attemptpermit-所有权) 补充，不改变本决议的 cache-aware TPM 与准入门槛）

决策：

```text
TPM（每分钟 token）限流改为「缓存感知」，对齐 Anthropic「cache-aware ITPM」行业口径，并修复两处预占泄漏：

1. TPM 计数明确「排除」cache_read（缓存命中读取）：
   billableTPMTokens = uncached_input + cache_write(5m+1h) + output_total（含 reasoning）。
   cache_read 权重 = 0（不计），不做「打折」（0.1~0.25 是计费折扣，不是限流权重；限流口径要么全额要么不计）。
   理由：缓存命中的 token 上游不重新计算、几乎零吞吐负载；Codex 等 agent 每轮重发 ~8-9 万缓存上下文，
   若按全额计入每分钟窗口，会「发两句话就 429」。cache_write 仍全额计入（首次处理有真实上游负载）。

2. 预占（reserve）/ 对账（reconcile）/ 释放（release）闭环：
   - 入场按 ConservativeInputTokens（全量输入的保守估算，cache 命中率在入场时未知）预占 route+user 与
     每个通过渠道级预占的候选 channel 的 TPM。
   - 结算成功后 backfill 差额（actual - est，actual 已排除 cache_read）对账胜出 route+user 与胜出 channel。
   - 请求收尾时，用 defer 释放所有「未被结算对账」的 TPM 预占：失败/取消/无结算的 route+user；
     fallback 中落选/失败的候选 channel。释放用脱离 cancel 的上下文，客户端断开也能回退。

3. 只释放 TPM（token 维度）。channel/ingress 的 RPM/RPD（请求计数）代表「确实发起过一次尝试」，
   按上游保护/防滥用的行业惯例不回退。

4. TPM 判定改用「new-api 式准入门槛」（2026-07-02 二次修复）：门槛只看「进入前窗口已用量 sum 是否 >= 上限」，
   不把本次请求的预估计入门槛——单条请求无论多大都不因自身预估超上限而被拒。RPM/RPD 仍用严格门槛。
   理由：Codex 每轮重发整段上下文，会话涨到 30 万+ token 后单条保守预占自身就 >= 300k 上限，旧的
   「sum+est>limit 即拒」会让空窗口也「一说话就 429」。准入放行后仍照常预占、结算回填退回缓存部分收敛窗口。
```

原因：

```text
1. 历史 billableTPMTokens 把 cache_read 按全额计入 TPM，与 Anthropic cache-aware ITPM 相悖；
   Codex 类高缓存复用负载会瞬间打满每分钟窗口，产生「刚配置好、发两句就 429」的误拦截。
2. 历史实现只有胜出候选走 backfill 对账，失败/取消/无结算的请求与 fallback 落选渠道的 TPM 预占
   从不显式回退，只能干等 60s 滑动窗口过期——额度「泄漏」在窗口里，造成 bursty/易错负载过早 429，
   并让渠道级 TPM 负载虚高（每个尝试过的渠道都被计入，却只有胜者被对账）。
3. 旧的「sum+est>limit 即拒」严格门槛把「单条请求自身大小」当拒绝依据：Codex 大会话单条预占 >= 上限时，
   即使窗口为空也进门即 429（实测 req_d5b7c1e8… 窗口早已清空仍 98ms 秒拒、未触上游）。new-api 的模型限流是
   准入型（已用量未达上限即放行，请求本身大小不作拒绝依据），别家中转站「不会因一条请求大就拒」正是此理。
```

影响：

```text
1. internal/service/gateway/lifecycle/ratelimit_gate.go：billableTPMTokens 去掉 CacheReadInputTokens；
   新增 tpmReservations 跟踪器 + recordKey/recordChannel/markReconciled/releaseUnreconciledTPM。
2. attempt_runner.go / attempt_runner_stream.go：预占成功后登记，结算 backfill 后 markReconciled，
   函数入口 defer releaseUnreconciledTPM 释放未对账预占。
3. internal/platform/ratelimit/sliding.go：checkAndAddScript 判定前把窗口聚合和 floor 到 0（E2E 发现的加固）——
   负向回填/释放落在比预占更晚的秒桶，预占桶先滚出窗口时聚合和会短暂为负；用量不可能为负，floor 到 0
   避免负额度授予「超过配置上限」的余量（底层桶仍保留负值随 TTL 自愈）。
4. internal/platform/ratelimit/{sliding.go,guard.go}：新增 admitThenAddScript + CheckThenAdd（准入门槛，
   门槛只看进入前 sum>=limit）；guard 引入 gateMode，TPM 走 gateAdmit、RPM/RPD 走 gateHard。
   guard_test.go 按准入语义更新并补：空窗超大单请求放行、已超才拒、回填退回后恢复放行。
5. TPM 计数从「按预估膨胀、只减不还」转为「按真实用量收敛、失败即回退」；准入门槛让单条大请求进得来，
   Codex 多轮/大会话都不再误 429，限流窗口更贴近真实吞吐。
6. 计费（billing）不受影响：本改造只动 TPM 限流口径，扣费仍按完整 usage facts（含 cache_read 折扣价）。
7. E2E（真实 Codex，本地 Docker DB）验证：大上下文会话 cache_read 累计 472,576、旧口径 TPM 559,665（会超 300k 上限
   而中途 429），新口径仅 87,089、全程 0 次 429；取消场景预占 49,378 在结算前被释放归零；
   30 万+ token 大会话续跑（旧代码进门即 429）全程 0 次 429。详见 DESIGN-route-rate-limit.md §15.4。
```

设计文档：[DESIGN-route-rate-limit.md](DESIGN-route-rate-limit.md) §14–§15

## DEC-029 慢上游 + 客户端重试风暴防护：在途并发上限 + 失败软冷却（唯一渠道保护）

状态：accepted（2026-07-10 定稿；P4 把 Channel concurrency 的实现与续租所有权改由 [DEC-041](#dec-041-候选级资源原子准入与统一-attemptpermit-所有权) 接管，入口 `(route,user)` concurrency 与限流改由 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威) 的 request-admission token 接管；本决议第 2 项进程内失败软冷却由 [DEC-045](#dec-045-只有真实上游责任结果进入-breaker并删除进程内失败软冷却) 废止）

决策：

```text
针对「慢上游（如 sub2api 中转，断开仍计费）+ 客户端自动重试（Claude Code 最多 ~10 次）」的成本放大
场景，新增两项相互独立的保护，均为运行时配置（app_settings，热改免重启）：

1. 在途并发上限（in-flight concurrency limit；本决议落地时为进程内计数，P4 的入口实现由 DEC-043、Channel 实现由 DEC-041 替换）：
   - 「线路+用户」级：ingress 中间件在认证后检查，超出全局默认 key_limit 的并发请求立即 429
     快速失败——不发上游、不冻结余额、不被上游计费。主体口径与 DEC-027 一致（ru:<route>:<user>）。
   - 渠道级：attempt runner 在调用上游前占用渠道在途名额（含整段流式传输，params.Stream 返回即释放），
     满员即跳过该候选 fallback 到下一渠道（与熔断 open 同语义，不写 attempt）。P4 实现后，该名额与
     breaker、Channel RPM/RPD/TPM 由 DEC-041 单 Lua全有或全无取得，不再先占并发再分步占限流；
     渠道行 channels.concurrency_limit 覆盖全局默认（NULL=继承，0=不限，>0=上限）。
   - 全局默认 gateway.concurrency_defaults {key_limit, channel_limit}，默认 0/0（关闭）——并发限制是
     选择性开启的保护，避免默认值误伤合法的 agent 并发扇出。
   - 与 RPM（按时间的请求速率）正交：RPM 挡不住「10 个 200s 长请求同时挂着」这种堆积。
   - 对齐业界：LiteLLM max_parallel_requests（per-deployment）、sub2api account.Concurrency、
     Envoy circuit breakers max_requests。

2. 渠道失败软冷却（failure soft-cooldown，历史规则，P4 不再实施）：
   - 本决议原先在 timeout / 5xx 后写入进程内 `gateway.channel_failure_cooldown_ms`，只把该 Channel
     移到本实例 fallback 顺序末尾；这会让多个 Gateway 对同一 Channel 得出不同顺序。
   - P4 由 DEC-045/046 的 Redis 全局错误事实、10 秒连续 3 次快速触发和比例熔断替代该机制；删除
     `gateway.channel_failure_cooldown_ms`、进程内 failure registry、候选 demote 接线和对应 Admin 设置。
   - 上游 429 仍使用独立的 Redis 全局 Channel 冷却；401 继续由凭据闸门处理；timeout/5xx 是否影响
     Channel/Endpoint breaker 只按 DEC-045 的真实上游归因执行，不再叠加一层本地软冷却。
```

原因：

```text
1. anthropic_0.16（sub2api 中转）事故复盘：39 次首字节超时 + 59 次 5xx + 11 次 TLS 损坏，全部发生在
   首字节前；上游 sub2api 断开不取消、drain 到底照扣费。客户端（Claude Code）超时后自动重试最多 10 次，
   每次都是新请求、都可能被上游计费——渠道 timeout 放大到 200s 后，单次用户操作最坏可堆 10×200s 在途。
2. 重试放大是「跨请求」现象，单请求内的 retry 分类器/每渠道重试次数都治不到；业界（LiteLLM/sub2api/
   Envoy/Google SRE）一致用「并发上限 + 冷却/重试预算」处理，没有一家按渠道配重试次数。
3. 唯一渠道保护是硬要求：opus 系列当前只有一条渠道，任何「冷却=剔除」的实现都会把该模型打成全灭。
```

影响：

```text
1. migrations/000072：channels.concurrency_limit（可空，CHECK >= 0）；FindRouteCandidates/channels 全行
   查询带出该列；routing.ChatRouteCandidate.ConcurrencyLimit 传递到 attempt runner。
2. internal/platform/ratelimit/concurrency.go：历史进程内 ConcurrencyLimiter（AcquireRouteUser/AcquireChannel/
   SetDefaults 热改；release 幂等；limit<=0 不限但仍计数）。P4 生产路径分别迁移到 DEC-043 request token 与
   DEC-041 AttemptPermit，旧本机默认不再是执行权威。
3. P4 删除 `cooldown.go` 中的 failureUntil/RecordFailure/FailurePreferred/SetFailureCooldown，删除
   `PrepareCandidatesParams.FailurePreferred` 与 demoteFailureCooled；429 Redis 全局冷却继续保留。
4. ingress：internal/app/gatewayapi/middleware/concurrency.go 的职责保留为请求级生命周期，但 P4 重构为
   request-admission token 的 Acquire/Renew/Finish；真实 limited 429 与 Store/runtime-sync 503 必须区分。
5. 新 failure code：channel_concurrency_limited（三个生成执行面 handler 均映射 429）。
6. `gateway.concurrency_defaults` 继续存在；`gateway.channel_failure_cooldown_ms` 在 P4 从 app_settings
   注册表、settings applier 和 Admin 系统设置中删除。
7. admin：渠道 rate_limits 请求对象增加 concurrency 字段；unio-admin 渠道表单增加「并发」输入。
8. 推荐运维配置：sub2api 类渠道 concurrency_limit 2~5、key_limit 3~5（略高于终端用户合法并发）；
   P4 不再配置失败软冷却时长。
9. P4 切换检查点删除候选路径对进程内 AcquireChannel/独立 refresher 的调用，Channel concurrency 改由
   AttemptPermit Renew/Finish/Abort 唯一续租和释放；入口级旧 AcquireRouteUser/refresher 同样退出生产路径，
   改由 request-admission token 独立续租和终结，仍不并入候选 permit。
```

## DEC-030 缓存写入 30m 维度：OpenAI GPT-5.6 独立成档，不塞进 5m/1h 桶

状态：accepted（2026-07-10 定稿）

决策：

```text
OpenAI GPT-5.6（2026-07-09 GA）起，缓存写入（cache_write_tokens）按未缓存输入价 1.25x 计费，且只有
「30 分钟单档」这一种 TTL；Anthropic 则是 5m（1.25x）/ 1h（2x）双档且可并存。二者 TTL 语义与计价倍率
都不同。为账目按 TTL 精确区分、便于对账与未来分档定价，在既有 cache_write_5m / cache_write_1h 之外
显式新增第三个协议无关维度 cache_write_30m，而不是把 OpenAI 的写入塞进 5m 桶。

映射口径：
- OpenAI 协议族（Chat Completions / Responses，含 DeepSeek 兼容）：cache_write_tokens → 30m 维度；
  5m/1h 标 not_applicable。uncached = prompt − cached − cache_write（写入 token 是 uncached 的子集，
  从中扣出改按 1.25x 计，避免同一批 token 既按 1x 又按 1.25x 双重计费）。
- Anthropic：5m/1h 维持原样，30m 标 not_applicable。
- 计费公式 token_v1 不升级：新增维度对全部历史数据恒为 0（回填 not_applicable / 0），复算结果不变。
- usage_mapping_version 升级 v1→v2（openai.v2 / openai.responses.v2）：新增解析了以前忽略的
  cache_write_tokens 字段，按复算纪律必须升版以区分映射规则。
```

原因：

```text
1. 单一 CacheWriteInputTokens 维度对 Anthropic 行不通：一条响应可同时含 5m + 1h 写入，两档单价不同，
   一个维度只能挂一个单价。故写入维度天然需要多档。
2. 把 OpenAI 的 30m 归入 5m 桶会丢失 TTL 语义（30m ≠ 5m），一旦两家按 TTL 差异定价即被动，且请求详情
   / 对账口径与上游账单对不上。
3. adapter/anthropic/messages/usage.go 早有先例：无 TTL 分级的上游（DeepSeek flat 写入）归入默认桶 +
   另一档 not_applicable。此处沿用「按 TTL 语义分档、缺失档 not_applicable」的统一口径。
```

影响：

```text
1. migrations/000075：6 张表（model_prices / channel_prices / price_snapshots / cost_snapshots /
   usage_records / settlement_recovery_jobs）新增 cache_write_30m 列（价/成本/金额/token+state），
   并把 30m 并入 cost_snapshots 总额校验与 usage/recovery 的「非 known 值置零」约束；历史行回填
   0 / not_applicable，token_v1 复算不变。
2. usage.Facts 新增 CacheWrite30mInputTokens（+ Valid()）；billing types/price/service/scale 全链路
   新增 30m 单价/金额/校验/授权估算；adapter.ChatUsage 新增 CacheWriteTokens 并解析
   prompt_tokens_details.cache_write_tokens / input_tokens_details.cache_write_tokens。
3. settlement / settlement_recovery / router / partial_stream / cost_exposure / ratelimit_gate /
   admin(query+service+api DTO) 所有 usage/price/cost 映射点同步补 30m；sqlc 查询（价目、快照、
   请求详情、dashboard/radar/models_ops 聚合）纳入 30m。
4. unio-admin：价格/成本录入表单、成本分解、成本计算器、请求详情、recovery 详情、dashboard 缓存卡
   均新增 30m 展示/录入；cost-breakdown 的 30m 行按「tokens>0」条件渲染，Anthropic/旧请求不显示空行。
5. seed-test-data.sql 新增 GPT-5.6 Sol/Terra/Luna + gpt-5.6 别名（官方价，cache_write_30m = 输入价 1.25x）。
6. 后续若 OpenAI 推出更长 TTL 的更高价写入档，可复用现有多档结构（新增维度或复用现档），无需重构。
```

## DEC-027 渠道成本倍率

状态：accepted（已实现；成本**基数来源**由 [DEC-031](#dec-031-成本基数改用模型基准价退役-model_reference_costs) 修订）

决策：

```text
渠道真实成本从「每 (渠道, 模型) 绝对金额」升级为「基数 × 价格倍率 × 充值倍率」
（channel cost multiplier + recharge factor）。承接 DEC-026 售价侧倍率，把同一套「基数 × 倍率」
模式对称地补到成本侧：

  真实成本 = 参考价基数 × channel_cost_multipliers（价格倍率，默认/逐模型）
                        × channel_recharge_factors（充值倍率，账户级）
  个别渠道/模型按自有价表计价时，用 channel_prices 手填绝对成本覆盖（最高优先级，不再乘任何倍率）。

结算时相乘得成本向量并冻结进 cost_snapshots：除绝对成本向量与各分项金额外，再记倍率行 id +
充值倍率行 id + 当时倍率标量，历史成本可按原事实独立复算、不随后续改倍率漂移。成本在路由期即
解析进候选（cheapest 排序 + 敞口用），派生成本统一走 Go ScaleProviderCost 保证精度一致；充值倍率
承担名义→真实结算币种换算，cost_snapshots.currency 语义为「真实结算币种」，与收入同币种、毛利可直接相减。

正文详见 DESIGN-channel-cost-multiplier.md。
```

原因：

```text
1. 售价侧（DEC-026）已用「基数 × 倍率」在路由/结算时算 + 快照存 source id + 倍率标量；成本侧照抄
   这套，架构一致、心智负担最小、单一事实源、无行膨胀（不物化成 N 行 channel_prices）。
2. 上游中转按「官方价 × 价格倍率」扣名义额度，而你充值时又以「充值倍率」把真金白银换成名义额度；
   两者都决定真实成本，故拆成 每模型参考价（稳定）× 每渠道价格倍率（调价即变）× 每渠道充值倍率（充值即变）。
3. 上游改一次倍率 / 换一次充值档 → 各改 1 个数，该渠道所有模型同步生效。
```

影响：

```text
1. 新增 channel_cost_multipliers（价格倍率，默认/逐模型，versioned）与 channel_recharge_factors
   （充值倍率，账户级，versioned）；channel_prices 退为绝对成本覆盖逃生通道。
2. cost_snapshots 增倍率标量 + 来源行 id 列；billing 新增 ScaleProviderCost；FindRouteCandidates
   路由期解析成本；请求详情费用处对称展示成本侧倍率（价格倍率 / 充值倍率 / 上游参考价）。
3. 冻结/扣费仍只用售价，改成本倍率不影响预授权冻结额；历史请求读快照不重算。
4. 已实现并 e2e 验收；其中「成本基数来自独立 model_reference_costs 表」这一部分由 DEC-031
   修订为复用 model_prices（模型基准价）。
```

## DEC-031 成本基数改用模型基准价，退役 model_reference_costs

状态：accepted（2026-07-14 定稿并实现）

决策：

```text
成本基数不再使用独立的 model_reference_costs 表，改为复用 model_prices（模型基准价）作为售价与成本
的唯一共用基数。这是对 DEC-027 基数来源部分的修订——DEC-027 的价格倍率 × 充值倍率 + 绝对覆盖机制不变，
只换掉「成本基数从哪来」：

  客户售价    = model_prices × 线路倍率 (routes.price_ratio)                              —— DEC-026，不动
  渠道真实成本 = 同一 model_prices × 价格倍率 (channel_cost_multipliers) × 充值倍率 (channel_recharge_factors)
              或 channel_prices 绝对覆盖（最高优先级）

退役 model_reference_costs：删表 + 删 admin CRUD / UI / sqlc / 路由 JOIN / 结算 pin 对该表的依赖。
成本 pin cost_snapshots.model_reference_cost_id 重命名为 cost_base_model_price_id，保持可空、保持无 FK
（审计权威是冻结金额列，配置表可删不破历史），数据来源 = 路由已算好的 candidate.ModelPriceID。

正文详见 DESIGN-cost-base-from-model-price.md。
```

原因：

```text
1. 双录消除：model_prices 与 model_reference_costs 列 1:1（*_price ↔ *_cost），同一模型要录两遍，
   窗口版本不同步时继续漂移。
2. 修根因：只配基准价 + 渠道倍率仍被判「未定价 / 不可售」——路由要求参考成本表，admin 徽标又只查
   channel_prices，两处都与真实可售条件不一致；本改造让判价与路由对齐、售价成本共用单一基数。
3. 心智对称：DEC-026 已立「基准价」，DEC-027 又造「参考成本」，两个「稳定基数」说不清谁权威；
   统一到 model_prices 后，售价/成本都从同一基数出发，只差各自倍率。
```

影响：

```text
1. migration 000037（增量叠加在已 consolidation 的 000001-000036 之上）：rename 成本 pin 列
   （cost_snapshots / settlement_recovery_jobs 的 model_reference_cost_id → cost_base_model_price_id）
   + drop model_reference_costs 表 + 历史非空 pin 值置 NULL（旧值是 refcost id，作 model_price id 无意义）
   + 保持该列无 FK。
2. 路由 FindRouteCandidates 删 refcost LATERAL；priced 过滤改为 cost.id IS NOT NULL OR mult.id IS NOT NULL
   （base model_prices 为 INNER JOIN 恒在）。
3. admin has_price / bindings_available 重写对齐路由：可售 = 有 channel_prices 覆盖 或（模型有启用基准价
   且渠道有可用倍率——逐模型或默认）；修复此前只查 channel_prices 的「启用·不可售」徽标（净新增逻辑，需独立测试）。
4. settlement / router / recovery 成本 pin 改 cost_base_model_price_id + 复用 model_prices；结算兜底改调
   已存在的 FindActiveModelPrice（原为 dead code），取代 FindActiveModelReferenceCost。
5. 新增 billing.ModelPriceToProviderCost（路由/结算/预览三处共用）；删除 modelreferencecost admin
   service / handler / 前端 ModelReferenceCostDialog。
6. 修订 DEC-027 的成本基数来源部分；DEC-027 的倍率/充值倍率/绝对覆盖机制与快照审计不变量继续有效。
```

## DEC-032 ProviderEndpoint 承载 BaseURL 与公共故障域（修订 DEC-017 第 4 点）

状态：accepted（本项决议已于 2026-07-20 定稿，待实现；BaseURL 安全更新由 [DEC-033](#dec-033-attemptpermit-与状态代际隔离迟到结果) 补充）

决策：

```text
Provider 继续表示供应商身份与记账主体；新增 ProviderEndpoint 表示 Provider 下的一条 API Root，
由 ProviderEndpoint 持有唯一、规范化后的 BaseURL，并作为公共网络故障与服务故障的熔断边界。

数据关系固定为：

  Provider 1 -> N ProviderEndpoint 1 -> N Channel

1. providers 不持有 base_url；同一供应商有多个独立 API Root 时，在同一 Provider 下创建多个 Endpoint，
   不为不同地址伪造多个供应商。
2. provider_endpoints 持有 provider_id、name、base_url、status 等 Endpoint 事实；规范化后的 base_url
   全局唯一，防止同一公共故障域被拆开后绕过保护。
3. channels 删除 base_url，通过 provider_endpoint_id 引用 Endpoint；Channel 继续持有 provider_id，
   数据库使用复合外键保证 Channel 的 Provider 与 Endpoint 所属 Provider 一致。
4. Channel 继续表示账号级运行事实，包括 protocol、adapter_key、凭据、模型绑定、价格、限流、并发和
   优先级；一个 Endpoint 可以挂多条 Channel，例如同一地址下的多个 API Key。
5. 公共故障熔断按 Endpoint 执行，账号级故障按 Channel 执行；Provider 只聚合所属 Endpoint 的历史表现，
   不设置 Provider breaker，避免一个供应商的独立地址相互误伤。
6. request_attempts 冻结实际 provider_endpoint_id；凡 request_records 现有 final_provider_id /
   final_channel_id 收口会被写入时，同步冻结 final_provider_endpoint_id。Provider 在成本快照和账本中的
   供应商/记账语义不变。
7. 当前本地 StarAPI 的 3 条 Channel 共用同一 BaseURL，目标迁移结果为
   1 Provider + 1 ProviderEndpoint + 3 Channels。

本决策仅修订 DEC-017 第 4 点中由 Channel 持有 base_url 的概念锚点，不改变用户不可见 Provider/Channel、
线路内路由、定价、结算和账务规则；DEC-026 对“档/线路”产品载体的后续修订继续有效。
```

原因：

```text
1. BaseURL 是公共网络和服务故障的边界，不是供应商身份，也不是 API Key 账号本身。
2. BaseURL 放在 Channel 会重复保存：同一地址的三把 Key 会被当成三个故障域，地址宕机时依次失败。
3. BaseURL 放在 Provider 又会合并过度：同一供应商的两个独立地址中一个宕机，会错误摘除另一个健康地址。
4. 独立 Endpoint 层既保留供应商和账务语义，又能让相同地址共享熔断、不同地址彼此隔离。
```

影响：

```text
1. 数据库新增 provider_endpoints；Channel 改为引用 Endpoint；request/attempt 增加 Endpoint 身份快照；
   BaseURL revision 与更新 fencing 由 DEC-033 定义。
2. Gateway、主动检测和 Admin 测试请求从 Endpoint 读取 BaseURL；Admin 增加 Endpoint 管理与绑定界面。
3. Redis 公共故障 key、状态机、指标、日志、trace 和告警从 Provider 维度改为 Endpoint 维度。
4. 具体 schema、状态机、迁移、发布和验收要求以 ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-033 AttemptPermit 与状态代际隔离迟到结果

状态：accepted（本项决议已于 2026-07-20 定稿，待实现；Channel 配置版本由 [DEC-036](#dec-036-channel-config-revision-隔离迟到结果) 补充，Endpoint status 围栏由 [DEC-038](#dec-038-endpoint-status-revision-与全局准入围栏) 补充，候选级资源所有权由 [DEC-041](#dec-041-候选级资源原子准入与统一-attemptpermit-所有权) 确定）

决策：

```text
每次准备调用真实上游时，BreakerStore 的 AcquireAttempt 必须签发一张服务端有记录的 AttemptPermit。
它代表本次请求对 Endpoint/Channel breaker、可能的 half-open 探测以及相关准入资源的临时资格。

1. AttemptPermit 至少包含唯一 permit_id、Endpoint/Channel ID、Endpoint BaseURL/status revision/fence、两个作用域
   各自的 state generation、两个 half-open probe 标志和有效期；不能只靠调用方把这些字段原样传回。
2. Redis 保存 active permit；Finish 或 Abort 后保留有界 terminal tombstone。两种终态 first-terminal-wins，
   同终态重复提交幂等返回第一次结果，冲突终态不得覆盖或重复计数。
3. 已经调用真实上游的请求只能用 Finish(permit, outcome) 收口；拿到 permit 但未调用上游的路径使用
   Abort(permit, reason)，只释放资格，不虚构成功或失败。
4. 所有仍在调用上游的请求都 Renew；长流是主要场景。Renew 继续维护真实在途的 Channel concurrency，
   但 generation 已失效时不得延长旧 half-open 权利，也不得重复增加 RPM/RPD/TPM。Finish/Abort 无论 breaker
   是否应用都要按该 permit 的服务端 resource token 收口资源；具体所有权、原子边界和入口级分层由 DEC-041 定义。
5. Endpoint 与 Channel 在真实状态迁移、管理员 Reset 和新一轮 half-open 探测时推进各自 generation；
   普通 closed 窗口计数或 EWMA 更新不推进。half-open 的两次恢复成功必须来自两个不同、当前有效的 permit。
6. Finish 先用 Endpoint BaseURL/status revision 对整张 permit 做全局 fencing：任一 revision/fence 不匹配时
   Endpoint 和 Channel 都不更新；全部匹配后再分别校验两个 state generation，允许未被 Reset 的作用域独立应用。
7. BaseURL/status 更新、Reset 和新一轮探测必须有不可回退的 revision/generation fence；旧 permit 在 fence 后
   不得修改新运行态。P4 使用 provider_endpoints 的独立单调 base_url_revision/status_revision 加 Redis
   prepare/commit/abort 实现跨 PostgreSQL/Redis 的更新协议；具体 status/Provider 批量语义由 DEC-038 补充。
   Redis/BreakerStore 整体故障时的客户准入由 DEC-040 固定为 fail-closed，且恢复后旧 permit 不得穿过 fence。
8. Endpoint BaseURL/status/state generation 已 stale，或 permit 已 expired/unknown 时，真实请求仍按事实完成
   request_attempt、usage、settlement 和账务；仅对对应当前 breaker 状态做 no-op，并保留可审计的
   applied/ignored disposition。Channel 配置 revision 已在 PostgreSQL 递增、但 Redis 尚未 compare-and-rotate
   时，旧 Finish 可写仅属于旧 revision 的 bucket；该 bucket 永不作为当前事实并在首次新 Acquire 时清空，
   具体由 DEC-036 补充。stale/expired 只让 breaker/TTFT 应用 no-op；只要服务端 permit record 尚在，资源仍按
   DEC-041 精确终结，物理 key 已丢失的 unknown 才只能依赖租约/窗口 TTL。disposition 的具体存储列和枚举由
   P4 实现基线定义；permit_id 不进入公开 API、指标 label 或 routing trace。

本决策当时未决定 BreakerStore 整体故障策略，现由 DEC-040 固定为 fail-closed。BaseURL 管理操作本身要求在
无法建立 revision fence 时不先修改数据库；具体 prepare/commit/abort 顺序由 P4 实现基线落地。Redis/Store
故障时不存在 degraded admission，未取得服务端 AttemptPermit 的请求不得调用上游，也不得恢复后补写 breaker。
```

原因：

```text
1. 长流可能超过 half-open 租约：新探测已经恢复后，旧流才失败，若只按 Endpoint/Channel ID 记结果，
   旧失败会重新打开刚恢复的状态；反过来，旧成功也可能错误关闭新一轮 open。
2. Reset 和 BaseURL/status 更新不会取消已经在途的请求；没有 revision/generation fencing，旧地址或旧状态的结果
   会污染新运行态，并可能一次误摘同 Endpoint 下全部 Channel。
3. 单独 permit 记录和 terminal tombstone 才能处理 Redis 响应丢失后的重试、重复回调和 Finish/Abort 竞争；
   仅传一个 lease_id 不能证明它仍有效，也不能实现幂等。
4. breaker 只是运行保护，不是账务事实；结果被 fencing 忽略不能反过来删除真实 attempt 或改写客户结算。
```

影响：

```text
1. 当前 P4 实现基线把 BreakerStore 接口改为 SnapshotMany、AcquireAttempt、Renew、Finish、Abort、Reset 和
   带 endpoint_id 的 Endpoint BaseURL/status revision prepare/commit/abort；删除无 permit 的 RecordResult 写入口。
2. Redis 新增 permit key、generation/revision 字段和 terminal tombstone；需评估峰值 key 数、内存和续租写放大。
3. Gateway 三个生成执行面、流式/非流式和每次 fallback 共用同一 renew/terminal helper；Channel concurrency、
   RPM/RPD/TPM 归统一 permit 所有，route+user 请求级资源保持独立，具体由 DEC-041 定义并验收。
4. provider_endpoints 增 base_url_revision/status_revision；request_attempts 增 Endpoint/Channel breaker
   disposition；Admin BaseURL/status 更新与 Reset 增 fencing、pending 恢复和审计。
5. 具体 TTL、Lua 原子性、故障注入、跨 Gateway 竞态和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-034 流式与非流式延迟信号隔离

状态：superseded（2026-07-20 曾 accepted，2026-07-21 由 [DEC-035](#dec-035-仅以流式-firsttoken-生成唯一-ttft-ewma) 替代；不得实施）

决策：

```text
balanced 不再把流式与非流式请求写入同一个 Channel 首响 EWMA。两种请求的上游返回行为不同，必须
分别采集、保存和选择：

1. 流式延迟从真实上游调用开始，计算到第一个非 SuppressEmit、可向客户交付的有效 chunk 到达；保存为
   stream_response_start_ewma_ms，并独立保存 stream_response_start_samples。
2. 非流式延迟从真实上游调用开始，计算到 http.Client.Do 成功返回；此时完整响应头已解析且 HTTP 状态码
   已知。保存为 non_stream_header_ewma_ms，并独立保存 non_stream_header_samples。禁止用只表示响应头
   第一个字节的 httptrace.GotFirstResponseByte，也禁止用完整 Invoke 返回时间猜测响应头时间。
3. AcquireAttempt 把本次 request_mode 固化进服务端 permit record；流式只读取/更新流式 EWMA，非流式
   只读取/更新非流式 EWMA。当前模式无样本时延迟保持中性，不得借用另一模式样本，也不得生成混合的
   Channel response_start_ewma。
4. 上游可能等非流式完整结果生成后才连续发送响应头和 JSON，因此非流式 header latency 可能明显长于
   流式首 chunk；该事实只能影响非流式权重，不能降低同一 Channel 的流式权重。
5. response-start 时间可在调用中先捕获，但只有最终可归类为成功的 attempt 才更新对应成功 EWMA；
   非 2xx、body 读取/超限/解析/协议校验失败及其它失败只进入相应错误事实。
6. request_records.response_started_at 和 request_attempts.response_started_at 保持“Gateway 首次成功开始
   向客户交付响应”的审计语义：流式为首个 SSE 写出成功，非流式为最终 JSON 首次写出成功。它们不得
   改成上游 chunk 到达或响应头到达，也只有客户写出成功才能推进 delivery_status。
7. request_attempts 新增 upstream_started_at、upstream_response_started_at、upstream_completed_at，分别保存
   真实上游调用开始、按模式定义的上游响应开始和 adapter 调用完成；完整上游总耗时继续单独落库和展示，
   不参与 balanced 权重。
```

原因：

```text
1. 非流式协议只承诺返回一个完整 JSON 结果，并不保证提前 flush 响应头。供应商或代理可以等完整结果生成后
   再连续发送响应头和 body；同一模型可能流式 1 秒出首 chunk，非流式 30 秒才收到响应头。
2. 若两类样本进入同一个 EWMA，Channel 的分数会由其流式/非流式流量占比决定：一次 30 秒非流式样本会
   错误降低后续流式请求的权重，即使该 Channel 的流式首响实际仍是 1 秒。
3. Go HTTP Client 在完整响应头可用后返回 Response，body 随后按需读取，因此 OpenAI Chat/Responses/Compact
   和 Anthropic Messages 都能在 client.Do 返回处统一捕获非流式 header time；网络上 header/body 紧邻到达
   时两项时间可以接近，但应用层事实仍可分开。
4. 现有 response_started_at 会推进客户交付状态。把上游响应头写入该字段会在客户尚未收到 JSON 时错误显示
   delivery in_progress，也会污染客户可见 TTFT。
```

影响：

```text
1. Redis Channel state、SnapshotMany、scorer 和 reset/BaseURL revision 清理改为两套 EWMA 与样本数；
   AcquireAttempt 把 request_mode 固化进服务端 permit record，Finish 根据该不可变模式只更新匹配 EWMA，
   不信任调用方在终态临时声明模式。
2. lifecycle/adapter 增加协议无关 timing hook；非流式在 client.Do 返回后上报完整响应头时间，流式区分
   有效 chunk 到达与客户写出成功，OpenAI/Anthropic/provider-specific wrapper 共用同一契约。
3. PostgreSQL attempt 保存三项 upstream 时间；历史查询、Prometheus、routing trace 和 Admin 按 mode 展示，
   不再返回一个混合“首响”。线路实时权重必须明确 stream/non_stream 模式。
4. 测试必须覆盖晚响应头、提前响应头后延迟 body、header/body 紧邻、流式长尾、跨模式不污染、无对应模式
   样本不互借，以及上游已响应但客户写出失败不推进 delivery。
5. 具体 schema、Redis 字段、查询、UI、故障注入和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-035 仅以流式 FirstToken 生成唯一 TTFT EWMA

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；替代 [DEC-034](#dec-034-流式与非流式延迟信号隔离)，Channel 配置版本隔离由 [DEC-036](#dec-036-channel-config-revision-隔离迟到结果) 补充）

决策：

```text
P4 参考 sub2api 的 FirstToken/TTFT 采样方式：非流式请求不构造首 token 或响应头延迟样本；每个 Channel
只维护一个由流式有效 FirstToken 样本产生的 TTFT EWMA，balanced 的流式和非流式调度都可以参考该值。

1. 流式请求从真实上游调用开始，计算到第一个非 SuppressEmit、可向客户交付的有效 chunk 到达，记录
   FirstTokenMs；只要实际观测到有效 FirstToken 就可更新 ttft_ewma_ms/ttft_samples。若随后流尾失败，
   FirstToken 延迟样本保留，失败独立进入错误率和 breaker 事实。
   FirstTokenEligible 必须独立于 SuppressEmit：Chat role/output delta、Responses response.created/首个 output
   delta、Anthropic message_start/首个 content delta 有效；空帧、usage、ping、error/failed、finish-only、
   message_stop、[DONE] 无效。FirstToken 是兼容命名，语义为首个有效应用层流开始事件，不保证是文本 token。
2. 非流式请求 FirstTokenMs=nil；不安装 GotFirstResponseByte 或响应头完成计时，不把 http.Client.Do 返回、
   响应头到达、完整 Invoke 返回或总耗时转换成 TTFT 样本。
3. Redis 每个 Channel 只保存一套 ttft_ewma_ms + ttft_samples。流式和非流式 balanced 请求都读取这一个
   EWMA；它表达“该 Channel 已观测到的流式首 token 能力”，不声称是非流式本次请求的客户等待时间。
4. Channel 尚无流式有效 FirstToken 样本时，流式和非流式评分的延迟项都保持中性；不得用非流式样本填补。
5. AcquireAttempt 把 request_mode=stream|non_stream 固化进服务端 permit record。Finish 只接受当前 Channel 配置
   revision 的 stream permit 有效 FirstToken 样本更新当前 EWMA；non-stream permit 的 FirstToken 必须为 nil。
   Finish outcome 的成功/失败仍独立更新 breaker 事实，不改变已经观测到的 FirstToken 延迟真实性。
6. 流式和非流式都记录完整上游总耗时；总耗时只用于 PostgreSQL、Prometheus 和 Admin 历史观测，不参与
   balanced 权重。
7. request_records.response_started_at 与 request_attempts.response_started_at 只表示流式客户首帧成功写出，
   不等于上游 FirstToken 到达。非流式保持 NULL，完整 JSON 写出成功时 delivery 从 not_started 直接进入
   completed 并记录 response_completed_at。
8. request_attempts 新增 upstream_started_at、upstream_first_token_at、upstream_completed_at；其中
   upstream_first_token_at 仅流式可非空。历史 TTFT 查询只聚合 stream=true，非流式只进入总耗时查询。
```

原因：

```text
1. 非流式上游可以等完整结果生成后才连续发送响应头和 body，响应头延迟与流式首 token 不具有同一语义；
   将它们合并会污染 EWMA，拆成两套又会让调度与 sub2api 参考方案偏离。
2. sub2api 的口径更简单：流式 FirstTokenMs 有值，非流式为 nil；唯一 TTFT EWMA 的样本来源清晰、容易审计，
   不需要为不同供应商是否提前 flush 响应头建立额外假设。
3. 非流式仍可利用同一 Channel 的流式 TTFT 历史作为“模型开始生成速度”的旁证，同时真实非流式等待时间
   由总耗时独立展示，不冒充 TTFT。
4. 没有流式样本时保持中性，比使用非流式总耗时或伪造 0ms 更诚实，也避免只跑非流式流量的 Channel 被
   错误奖励或惩罚。
```

影响：

```text
1. 删除 DEC-034 规划的 non_stream_header_ewma、按模式选择权重、route runtime mode 参数和非流式响应头
   timing hook；Redis 恢复为每 Channel 一套 ttft_ewma/ttft_samples。
2. OpenAI Chat/Responses/Compact 与 Anthropic Messages 的非流式 adapter 只记录 upstream start/completed；
   流式 adapter/lifecycle 统一产生 FirstToken 候选样本，provider-specific wrapper 复用同一契约。非流式
   delivery 不再由 runner 在返回 DTO 前提前完成，而由 handler 在 WriteJSON 成功/失败后通过内部一次性
   finalizer 收口 completed/interrupted。transport start 必须在紧邻 client.Do 前由共享 timing observer 记录，
   不能使用包含编码/构造请求的 runner 外层时间；三项 timing 覆盖成功、失败、取消、partial、fallback，
   并在创建 settlement recovery job 前持久化到 attempt。
3. routing trace、Prometheus 和 Admin 必须标明 ttft_sample_source=stream_only；非流式请求详情 TTFT 显示空值，
   但完整总耗时正常展示。
4. 测试必须证明：非流式响应头/body 任意延迟都不会更新 TTFT EWMA；流式有效 FirstToken 会更新唯一 EWMA，
   流尾后续失败独立进入错误率；随后的流式与非流式 balanced 评分都读取该值；无流式样本时两者均保持中性。
   还必须锁定各协议 FirstTokenEligible 事件、逐帧首写成功/失败、重复 Finish 不重复采样、全部 attempt 终态
   与 settlement recovery 不丢 timing，以及 legacy 非流式 response_started_at 不进入 TTFT 查询。
5. 具体 schema、Redis 字段、查询、UI、故障注入和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-036 Channel config revision 隔离迟到结果

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；失效凭据轮换后的恢复语义由 [DEC-037](#dec-037-失效凭据轮换后自动检测) 补充，有效凭据轮换由 [DEC-039](#dec-039-有效凭据轮换先暂停并自动检测) 补充）

决策：

```text
Channel 使用 PostgreSQL 权威的单调 config_revision，把旧 Endpoint 绑定、协议、adapter、credential、
credential_valid、timeout 或启停状态产生的迟到结果，与当前 Channel 运行态隔离。

1. channels.config_revision 为 bigint，创建时从 1 开始。provider_endpoint_id、protocol、adapter_key、
   credential、credential_valid、timeout_ms 或 status 任一值真正变化时，在同一数据库事务中只增加 1；
   同值幂等更新不增加。name、priority、价格/计费字段和检测遥测不增加；RPM/RPD/TPM/concurrency 的原子
   准入与资源所有权由 DEC-041 确定，热更新对旧 permit 与新 Acquire 的作用由 DEC-042 确定；有效限额的
   多实例权威来源与独立版本由 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威) 确定，不进入 config_revision。
2. Gateway 每个客户请求重新从 PostgreSQL 查询候选并冻结 config_revision，不跨请求复用没有 revision
   失效协议的候选缓存。每个 attempt 和服务端 AttemptPermit 都保存实际 Channel revision；credential 正文
   不进入 permit、attempt、日志或指标。
3. SnapshotMany 携带 PostgreSQL 当前 revision。Redis Channel state 缺失或 revision 不等时只返回
   stale/no-sample、中性评分，不使用旧 open、错误率或 TTFT。AcquireAttempt 原子 compare-and-rotate：state
   缺失则初始化；revision 相等则复用；入参更高则推进 Channel generation、清空 breaker/错误窗口/退避/
   half-open/TTFT 后绑定新 revision；入参更低则拒绝 stale_config_revision，不能回退。
4. 配置提交前已经取得旧候选快照的请求仍可尝试 Acquire，但不保证一定获准：旧 revision 先获准可以继续，
   新 revision 先 rotate 时旧候选必须被拒绝并走线路内 fallback/无可用收口。已经开始真实 transport 的旧
   请求不主动取消，仍按真实结果完成客户响应。
5. Finish 只有在服务端 permit 的 Channel config_revision 与 Redis 当前 Channel revision 匹配时，才能修改
   该 revision 的 Channel breaker/TTFT。新 revision 尚未首次 Acquire 时，旧 Finish 可以写旧 revision bucket；
   配置提交前已冻结旧 revision 的请求仍可按自己的快照读取，携带新 revision 的请求不得借用，Admin 当前
   视图也必须判为 stale。首次新 Acquire 会原子清空；rotate 后的旧 Finish 返回 stale_config_revision。
   无论是否应用运行态，request/attempt、usage、settlement、cost、ledger 和 recovery 都按真实调用事实完成。
6. Channel config revision 只 fence Channel scope。旧 request 只要实际 Endpoint 的 BaseURL revision 和
   state generation 仍匹配，仍可独立更新该旧 Endpoint breaker；不得通过 Channel 当前绑定把结果归到新
   Endpoint。
7. 连续 401 计数改按 (channel_id, config_revision, endpoint_base_url_revision, endpoint_status_revision) 隔离。
   运行时异步 invalidator 与 manual/worker 主动检测必须携带读取配置时的三类 expected revision，使用数据库
   JOIN/CAS 翻转 credential_valid；只有 Endpoint 两类 revision 与 Channel revision 都仍匹配时，true/false
   真跳变才能更新状态并把 Channel revision 再加一。stale 成功/失败只留带三类 tested revision/applied 状态的
   历史日志，不覆盖当前 last_test_*、credential_valid，也不得清空新 revision 的 401 计数。
8. status=disabled/archived 提交后，之后发起的请求在 PostgreSQL 候选查询即被排除；已经开始 transport 的
   请求继续完成。重新启用产生新 revision，首次准入必须清空禁用前 Channel 运行态。
9. Channel 普通配置热更新不复制 Endpoint BaseURL 的 prepare/commit/abort。正确性由 PostgreSQL 单调
   revision、每请求候选快照、Redis compare-and-rotate 和 credential CAS 共同保证；Redis/BreakerStore
   故障时客户准入按 DEC-040 fail-closed。
```

原因：

```text
1. 当前运行时连续 401 只按 channel_id 聚合，异步失效和主动检测也只按 channel_id 写数据库。轮换 credential
   后，旧 credential 的迟到 401 可以把新 credential 标为无效，旧成功也能错误清空新 credential 的 401。
2. Channel 改绑 Endpoint 或修改 protocol/adapter/timeout/status 后，旧 request 的失败与 TTFT 已不代表当前
   配置。只用 Channel ID 或 Redis state generation 无法识别 PostgreSQL 配置已经变化。
3. breaker/TTFT 是可丢弃、可重建的运行态，request/usage/ledger 是不可伪造的业务事实。配置 fencing 只能
   阻止迟到结果影响当前运行态，不能删除真实 attempt 或跳过结算。
4. 当前 Router 每个客户请求都直接查询 PostgreSQL、没有跨请求候选缓存，因此 PostgreSQL revision 权威加
   Acquire compare-and-rotate 能在不引入第二套跨系统 prepare 协议的情况下闭合竞态。
```

影响：

```text
1. channels、routing candidate、request_attempts、Redis Channel state 和 AttemptPermit 增加 config_revision；
   breaker disposition 增加 stale_config_revision，Admin runtime 必须与 PostgreSQL 当前 revision 对账。
2. Channel CRUD、credential rotation、archive/restore、Provider 级联归档、runtime 401 invalidator 与主动检测
   全部改用统一 revision/CAS 契约；stale probe 的历史日志必须区分 tested revision 和 applied 状态。
3. SnapshotMany 保持只读，revision mismatch 中性；AcquireAttempt 在 Lua 中完成单调 compare-and-rotate，
   并与并发旧 Finish 串行化。已有 transport 不因 revision stale 被主动 cancel。
4. 测试覆盖旧/新 Acquire 两种竞态顺序、双 Gateway、credential 轮换与 401/成功交错、probe 迟到、禁用/
   重新启用、fallback、旧 Endpoint 独立归因，以及 stale 运行态不影响 settlement/usage/ledger/recovery。
5. Endpoint status fencing 由 DEC-038 补充。DEC-041 已决定 RPM/RPD/TPM/concurrency 的候选级原子准入，
   DEC-042 决定其热更新时间边界，DEC-043 决定多实例权威参数来源与独立版本。轮换
   credential 后如何恢复已经为 false 的 credential_valid，由 DEC-037 补充；轮换前为 true 的路径由
   DEC-039 补充。
6. 具体 schema、Redis 字段、管理 API、测试和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-037 失效凭据轮换后自动检测

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；轮换前为 true 的路径及统一响应由 [DEC-039](#dec-039-有效凭据轮换先暂停并自动检测) 补充）

决策：

```text
当 Channel 在轮换前已经 credential_valid=false 时，保存新 credential 不直接恢复路由；后端保存后立即自动
执行一次真实渠道检测，只有检测成功且测试所绑定的 Endpoint BaseURL/status revision 与 Channel config revision
都仍是当前版本时，才恢复为 true。

1. 后端提供 RotateCredentialAndTest（或等价 application use-case）。先提交 credential；credential 真正变化时
   按 DEC-036 将 config_revision 增加 1，credential_valid 继续保持 false，验证完成前 Gateway 不得选中。
2. 保存提交后同步复用现有 channeltest.Service.Test，以 credential_rotate 作为日志 source，并把保存所得
   Channel config revision 与当时 Endpoint BaseURL/status revision 固定为 expected revisions。该流程不得依赖
   前端串联保存与测试请求，也不得启动没有持久保证的 handler goroutine。
3. 自动检测使用独立、有界且不随 Admin 客户端断开而取消的 context，完成 probe、数据库 CAS 和日志收口。
   周期 worker 只作为后续复检手段，不能代替这次即时检测。
4. 检测成功且三类 expected revision 仍匹配时，数据库用 CAS 将 credential_valid 从 false 改为 true，并按
   DEC-036 再把 config_revision 增加 1。检测失败时保留已保存的 credential 和 false 状态，不增加第二次
   revision，也不允许真实客户流量替新 credential 试错。
5. 无可测模型、数据库或 prober 编排错误等 execution_failed 不回滚已经提交的 credential，也不恢复路由；
   管理员可稍后手动检测或幂等重试。
6. 检测期间发生第二次 credential/Channel 配置、Endpoint BaseURL/status 或父 Provider 有效状态修改时，旧检测
   结果为 stale，只记录三类 tested revision 和 state_change_applied=false；不得覆盖当前 last_test_*、
   credential_valid 或新 revision 的 401 计数，也不得自动替新 revision 重试。
7. 相同 credential 的重复 PUT 不增加 revision；Channel 仍为 false 时，可对当前 revision 再执行一次自动检测。
8. 对本决议覆盖的 false 路径，credential 保存失败沿用 4xx/5xx 且不检测；保存成功后返回 HTTP 200 JSON，
   分别表达 credential_saved 与 verification=passed|failed|stale|execution_failed。真实上游检测失败不是保存失败，
   不得返回普通 5xx 诱导调用方重复保存。
9. API、日志和检测记录不得回显 credential；只返回 saved/current Channel revision、测试所用的 Endpoint
   BaseURL/status 与 Channel config revision、状态是否实际应用，以及既有脱敏 TestResult。
10. 本决议只覆盖轮换前已经为 false 的恢复路径。轮换前为 true 的暂停/检测规则与 PUT 统一响应形状，已由
    DEC-039 补充。
```

原因：

```text
1. credential_valid=false 是客户流量的硬闸门。仅保存新 credential 会让渠道永久保持摘除；保存后直接置 true
   又会把未经验证的密钥投入真实客户请求。
2. 现有 channeltest.Service.Test 已统一处理真实 probe、结果映射和检测日志；后端复用它能避免保存路径形成
   第二套检测语义。
3. 若由前端串联 PUT 和测试 POST，页面关闭、网络中断或并发轮换都可能让“已保存但未检测”长期悬空；周期
   worker 默认有间隔且可关闭，不能提供保存后的即时保证。
4. credential 保存与上游验证不是同一个可回滚事务。用 200 组合结果明确区分二者，能避免管理员因检测失败的
   5xx 误以为保存失败并重复轮换。
5. 三类 expected revision CAS 能阻止旧 credential 或旧 Endpoint 状态的迟到成功恢复新配置，也能阻止旧失败
   覆盖新 revision 的检测事实。
```

影响：

```text
1. Admin 后端新增窄编排入口，credential 写路径与 manual/worker probe 共用 DEC-036/DEC-038 的三类
   revision/CAS 契约；channel_test_logs 的 source 增加 credential_rotate，并记录两类 tested Endpoint revision、
   tested_config_revision 和 state_change_applied。
2. 本决议覆盖的 false 路径中，PUT /admin/v1/channels/{id}/credential 从仅返回 204 改为不含密钥的 200 组合
   结果；前端分别展示“凭据已保存”和验证 passed/failed/stale/execution_failed，不把检测失败显示成保存失败。
3. 测试覆盖保存失败、验证成功/失败/执行失败、相同 credential 重试、并发二次轮换或 Endpoint/Provider 状态
   变化导致 stale、客户端断开和 worker 关闭；failed/execution_failed 路径继续不可路由，stale 路径只验证
   不改当前状态，所有响应均不回显 credential。
4. 轮换前 credential_valid=true 的准入与响应契约由 DEC-039 补充；两条路径实施时使用同一后端编排与统一
   HTTP 200 响应。
5. 具体服务边界、API 字段、实施清单、E2E 场景和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-038 Endpoint status revision 与全局准入围栏

状态：accepted（本项决议已于 2026-07-21 定稿，待实现）

决策：

```text
Endpoint 的 enabled/disabled/archived 允许热更新，但必须用独立 status_revision 和 fail-closed Redis fence
让 PostgreSQL 业务状态、旧候选准入和重新启用后的运行态保持一致。status_revision 不借用只表示地址变化的
base_url_revision。

1. provider_endpoints.status_revision 为 bigint，创建时从 1 开始。Endpoint 自身 status 真变化，或父 Provider
   status 变化使该 Endpoint 的有效可路由状态变化时，在同一数据库事务内增加 1；同值更新、改名和单独修改
   BaseURL 不增加。
2. routing candidate、request_attempt、Redis Endpoint/子 Channel state 和服务端 AttemptPermit 冻结实际
   endpoint_status_revision；permit 另保存不可回退的 status_fence_generation。BaseURL/status/Channel config
   三类 revision 分别校验，不得互相代替。
3. Admin 先持久化不含敏感数据的 PostgreSQL routing operation，再用 token/payload hash 执行
   PrepareEndpointStatusRevision 建立 pending fence；Redis prepare 后 operation 进入 prepared，业务 status/revision
   与 operation=db_committed 同事务提交，最后 Commit 激活并写 committed。失败使用 first-terminal-wins 的
   Abort/Recovery 收口。prepare 失败时业务数据不变，数据库已提交但 commit 未确认时返回 runtime_sync_pending；
   pending 期间 Redis 可用时一律拒绝新准入。
4. AcquireAttempt 是状态切换的线性化分界：prepare 前已成功取得 permit 的调用可以继续；只持有旧内存候选、
   尚未取得 permit 的请求在 pending 或 status revision stale 时必须拒绝并 fallback。Acquire 从当前 control 把
   status fence generation 固化进 permit，用于隔离 prepare 前 permit；候选本身不携带 fence generation。
5. disabled/archived 不主动取消围栏前 permit。旧调用继续客户响应、attempt、usage、settlement、cost、ledger
   和 recovery；新 status fence 后，它的 Finish 对 Endpoint 与所有子 Channel 当前 breaker/TTFT 都只能 stale/no-op，
   其迟到 401/成功也不得修改当前 credential_valid 或当前 revision 的 401 计数。runtime invalidator 与所有主动
   probe 必须同时 CAS Endpoint BaseURL/status revision 和 Channel config revision。
6. 每次有效 status 变化都推进 Endpoint 与子 Channel 的运行代际。离开 enabled 后旧运行态失效；重新进入
   enabled 后，Endpoint breaker/错误窗口/退避/half-open/跨 Channel 500 证据及子 Channel breaker/错误窗口/
   退避/half-open/唯一流式 TTFT 从 closed/no-sample 开始。禁止仅删除 key 后复用初始 generation。
7. status 切换或限额热更新不清空 429 cooldown、RPM/RPD/TPM、concurrency、sticky 或任何 request/usage/
   ledger 事实；既有 permit 的候选级资源按 DEC-041/DEC-042 收口。
8. Endpoint archived 在 fence 后只清 breaker counters/子 Channel 样本，保留包含 revision、fence generation
   和 effective_status=archived 的最小 control；业务实体永久删除后才删除 control。restore 提交新的 disabled
   revision，仍不可路由；之后真正 enabled 时再递增 revision 并建立 fresh 代际。Provider status 真
   变化必须把全部受影响 Endpoint 作为一个有界批次，全量 prepare、单事务更新 PostgreSQL，并以同一 batch
   token 和单个 Lua 原子 prepare/commit/abort；不允许部分生效或拆批。批次受已校验配置和绝对上限约束，
   空集合直接提交 Provider。所有写与 recovery 按 Provider -> Endpoint ID 升序锁定，Endpoint create/status/
   restore 同样先锁父 Provider，不能穿过正在进行的 Provider 状态批次。
9. 同一管理请求同时修改 BaseURL/status 时，两类 fence 必须关联并由同一数据库事务及 recovery 一起收口，
   不允许单边 commit。Redis 在操作开始前不可用时返回 503 且数据库不变；prepare 后 Redis 整体失联时按
   DEC-040 拒绝所有尚未取得 permit 的客户 attempt，直到 Store 恢复并完成 recovery。
10. Endpoint revision/fence control 在业务行存在期间（包括 disabled/archived）不得按 breaker 样本 TTL 消失。control 缺失时
    Snapshot/Acquire 返回 runtime_sync_required 并 fail-closed，由启动/后台 reconciler 从 PostgreSQL 当前
    Provider/Endpoint status 与 revision 重建；旧请求候选不得自行初始化 control。Redis/BreakerStore 整体
    连接或执行失败同样按 DEC-040 fail-closed。
11. Endpoint create 使用专门的 expected-absent control create fence，把 reserved Endpoint ID 初始化为两类
    revision=1；已有 control 不得覆盖，数据库唯一冲突/回滚时 recovery 清理 orphan pending control。相同 token
    但不同 next effective status 或 payload hash 必须 conflict。
```

原因：

```text
1. 当前状态只在候选查询时过滤。请求一旦取得内存候选，管理员随后停用或归档，旧请求仍可能在排队结束或
   fallback 时访问已经停用的上游；只更新 PostgreSQL 不能提供即时停用边界。
2. 已经取得调用许可的长流不能被状态按钮强制截断，否则客户得到半截响应，上游却可能已经计费，usage 与
   settlement 也会变复杂。以 AcquireAttempt 为分界可以形成可实现、可测试的原子边界。
3. 维修前的 open、失败窗口和 TTFT 若在重新启用后继续使用，会出现页面显示 enabled 但仍被旧熔断拒绝，或
   旧长流迟到结果重新污染新状态。status revision、fence generation 和 fresh 代际共同阻止这类迟到写入。
4. BaseURL revision 只表达地址变化。让 status 借用它会导致状态切换伪装成地址更新，并破坏历史归因和恢复逻辑。
5. Provider status 也是所属 Endpoint 的有效硬门禁；只 fence 直接修改 Endpoint 的入口，Provider 停用仍会留下
   同样的旧候选漏洞。
6. Redis flush 后如果允许旧候选按自身 revision 重建 Endpoint control，停用前快照可能抢先复活旧状态；control
   缺失 fail-closed 与 PostgreSQL 对账恢复才能消除这一 ABA 窗口。
```

影响：

```text
1. provider_endpoints、routing candidate、request_attempts、Redis state 和 AttemptPermit 增加 status_revision；
   permit/Redis 增加 status_fence_generation，breaker disposition 增加 stale_status_revision。
2. PostgreSQL 增加 durable endpoint routing operation；BreakerStore 与 Admin 增加 create/status/combined/batch
   prepare/commit/abort/recovery。Provider 多 Endpoint 状态操作使用单个 Lua 原子批次、单事务 revision 更新和
   幂等 recovery，并与统一锁序、线路空池/归档替换护栏共同执行。
3. Admin runtime/UI 展示 BaseURL/status 两类 current/pending revision。重新启用后 breaker/TTFT 显示 closed/no-sample，
   不能回显停用前数据；限流、并发和账务不被本决议重置。
4. Endpoint control 在业务行存在期间不按样本 TTL 过期；新增启动/后台 reconciler 和 runtime_sync_required 状态。
5. 测试覆盖旧候选尚未 Acquire、围栏前长流、disabled/archive/restore/enable、Provider 批量部分失败、组合
   BaseURL/status 更新、响应丢失和 recovery。所有旧调用保留真实审计与账务。
6. 重新启用会暂时失去停用前流式 TTFT 样本，balanced 延迟项保持中性直到产生新流式样本；这是有意取舍。
7. 具体 schema、Redis 字段、管理 API、测试、风险和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-039 有效凭据轮换先暂停并自动检测

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；补充 [DEC-037](#dec-037-失效凭据轮换后自动检测)，并统一 credential PUT 响应契约）

决策：

```text
当 Channel 在轮换前 credential_valid=true 且 credential 真正变化时，后端必须先把新 credential 保存为
不可路由状态，再立即执行真实渠道检测；只有检测成功且 Endpoint BaseURL/status revision 与 Channel
config revision 都仍匹配时，才恢复为 true。未经验证的新 credential 不得进入真实客户流量。

1. RotateCredentialAndTest（或等价 application use-case）统一承接 credential PUT。同值/真变化和当前
   credential_valid 必须在锁定 Channel 行的单个事务或等价原子条件写中判定。credential 真变化时，该事务
   同时保存新值、把 credential_valid 置为 false、清空旧 credential 的 last_test_* 当前摘要，并按 DEC-036
   只把 config_revision 增加 1；历史 channel_test_logs 保留。不得出现“新 credential 已提交但仍为 true”
   的可见中间状态。
2. 保存提交后，新请求因 credential_valid=false 不再选中该 Channel。提交前已冻结的旧候选继续遵循 DEC-036：
   旧 revision 若先取得 permit，可以使用候选快照中的旧 credential 完成；已经开始 transport 的请求不主动
   取消。旧候选不得重新查询或使用新 credential，其结果不得修改新 revision 的 Channel breaker、TTFT、
   credential_valid 或 401 计数。
3. 需要检测的路径使用保存事务返回的 credential、Channel config revision 和当时 Endpoint BaseURL/status
   revision 完整快照执行 probe；或在网络调用前先按三类 revision preflight，完全匹配后再冻结 runtime。若
   已 stale，直接记录 stale 且不调用上游，禁止重新读取另一轮 credential 却按本轮 revision 记检测事实。
4. 自动检测使用独立、有界、不随 Admin 客户端断开而取消的 context。检测成功且三类 expected revision
   仍匹配、credential_valid 仍为 false 时，数据库以 CAS 恢复 true，并把 config_revision 再增加 1。检测失败
   或 execution_failed 保留新 credential 和 false 状态，不产生第二次 revision；stale 只写 tested revisions
   与 state_change_applied=false 的历史日志，不覆盖当前状态。
5. 相同规范化 credential 且当前 credential_valid=true 时按幂等请求处理：不暂停、不调用 prober、不增加
   revision、不修改 updated_at/last_test_*，也不写虚假的检测日志。允许记录不含 credential 的 Admin 操作日志。
6. 相同 credential 且当前 credential_valid=false 时不增加保存 revision，但允许按 DEC-037 使用当前三类
   revision 再执行一次自动检测；成功恢复时仍只为 false -> true 真跳变增加一次 revision。
7. credential PUT 在所有保存成功或同值幂等路径统一返回 Admin API `{data: ...}` HTTP 200 组合响应，包含
   credential_saved、credential_changed、saved/current revision 和 verification。verification.state 仅允许
   passed|failed|stale|execution_failed|not_required；not_required 只用于同值且当前 true 的未检测路径，不能
   伪装成一次 fresh passed。credential_saved 表示请求已持久收口或数据库已有同值，credential_changed 表示
   是否实际换值。
8. 保存失败继续返回原有 4xx/5xx 且不检测。响应、日志、attempt、permit 和指标均不得回显 credential。
```

原因：

```text
1. 原本有效的 Channel 如果保存新 credential 后继续保持 true，错误密钥会立即由真实客户请求验证：balanced
   请求会先失败再 fallback，fixed 或唯一渠道会直接失败；403 等结果还可能无法及时触发现有 401 持久闸门。
2. 在同一事务内保存 credential 并置 false，可以消除“新密钥已生效但尚未验证”的可见状态；即使正确密钥
   会暂停数秒，也比让客户流量承担试错更可控。
3. DEC-036 已允许配置提交前冻结的旧请求按旧快照竞争准入。它们使用旧 credential，不会试用新 credential；
   revision fencing 又能阻止旧结果污染新状态，因此本项不新增 Channel 级 Redis prepare fence。
4. 保存时固定 probe runtime 和三类 expected revision，既能阻止并发二次轮换、Endpoint 改址或状态变化的
   迟到结果恢复错误配置，也能保证检测日志确实描述实际调用的 credential/version。
5. credential 真变化时清空旧 last_test_* 当前摘要，避免新凭据尚未检测或执行失败时仍显示旧密钥的成功结果；
   历史日志继续保留审计事实。
6. 相同且已经有效的 credential 无需人为制造短暂停机；相同但仍无效的 credential 应保留重新检测能力。统一
   HTTP 200 组合响应能明确区分保存、验证和幂等未检测。
```

影响：

```text
1. DEC-037 的 RotateCredentialAndTest 从仅覆盖原 credential_valid=false，扩展为统一处理 true/false 两条
   credential PUT 路径；两者共用 channeltest、日志和三类 revision/CAS 契约。
2. credential 真变化时，保存事务必须原子执行 credential 更新、credential_valid=false、旧检测摘要清理和
   一次 config_revision 递增；检测成功再执行 false -> true 和第二次递增。
3. routing candidate 必须冻结实际 credential，transport 不得在 Acquire 后重读当前值；credential probe 必须
   使用 pinned runtime 或 revision preflight，避免检测事实与真实请求不一致。
4. PUT /admin/v1/channels/{id}/credential 全部成功路径改为统一 HTTP 200 结构；前端增加 credential_changed 与
   not_required 展示，并继续分别显示“凭据已保存”和验证结果。
5. 正确新 credential 在检测期间也暂时不可路由；唯一 Channel 可能短时无可用渠道。这是本决议接受的取舍，
   不引入旧/候选双 credential 存储和零停机原子切换机制。
6. 测试增加 true + 新 credential 的 passed/failed/execution_failed/stale、相同 true 的幂等 not_required、
   相同 false 的重新检测、旧候选只使用旧 credential、probe 前/在途并发二次轮换、旧检测摘要清理、revision
   精确递增和所有响应不回显 credential。
7. 具体服务边界、SQL/CAS、响应字段、实施清单、E2E 场景和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-040 Redis/BreakerStore 基础设施故障统一 fail-closed

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；补充 [DEC-033](#dec-033-attemptpermit-与状态代际隔离迟到结果) 与 [DEC-038](#dec-038-endpoint-status-revision-与全局准入围栏) 的 Store 故障分支；入口 request-admission、admission/五个关键 runtime control 的故障与恢复边界由 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威) 和 [DEC-054](#dec-054-线路默认限流与渠道默认限流完全拆分) 扩展）

决策：

```text
Redis 与 BreakerStore 是 Gateway 上游准入的必需基础设施。基础设施不可信时，上层不得继续尝试提供上游
调用服务；所有新 request admission 与尚未取得 AttemptPermit 的新 attempt 统一 fail-closed。

1. Redis 连接拒绝/断开、超时、Lua/脚本错误、协议或返回解析错误、BreakerStore 内部错误都视为 Store
   基础设施故障。入口 AcquireRequestAdmission 失败时返回安全 503 且不创建 request token；ReserveRequestTokens、
   SnapshotMany 或候选 AcquireAttempt 失败时立即终止本次路由/fallback，不继续尝试其它 Channel、不创建本候选
   permit/attempt、不调用真实上游，但此前已签发的 request token 仍由 route wrapper outer finalizer 唯一 Finish。
   真实 limited 仍返回 429。
2. open、half_open_busy、stale_revision、stale_config_revision 等合法业务结果不是基础设施故障，仍按既定
   skip/fallback 规则处理。单 Endpoint control 缺失继续返回 runtime_sync_required、拒绝受影响候选并由
   reconciler 恢复。
3. AttemptAdmission 只允许 permit|denied。删除 mode=degraded、store_fail_open、failure-mode 配置和紧急绕过
   开关；Redis 恢复后也不得补写未获 permit 的调用结果。候选级 concurrency/限流由 DEC-041 与 breaker 在
   同一 Lua 全有或全无取得，不能绕过统一 permit 直接调用上游。
4. Store 故障发生在入口或上游调用前时，OpenAI Chat/Responses 与 Anthropic Messages 统一返回安全的 HTTP 503
   service_unavailable，不暴露 Redis/Store 名称、底层错误或候选信息。流式首帧前可返回协议错误 envelope；
   首帧后不能重写 HTTP 状态，按既有流中断和 partial settlement 边界收口。
5. Store 故障前已经签发 request token/permit 且正在处理或调用上游的请求不强制取消，继续真实客户响应、
   attempt、usage、settlement、cost 和 ledger。两类 Renew 失败不伪造终态；FinishRequestAdmission 与 permit
   Finish/Abort 使用同一 ID 有界幂等重试，仍无法确认时记录 result_unknown，并分别依靠入口/候选租约与窗口
   TTL 有界自愈。该调用若还需要下一次 fallback，新 attempt 仍必须 denied。
6. Gateway 启动时 Redis/BreakerStore 未就绪则启动失败或保持 readiness=false。运行中确认 Store 故障后，
   liveness 保持以执行后台健康检查和 recovery，但 readiness=false 且所有新 request admission/attempt denied。
   PostgreSQL 保留 `gateway.runtime_state_epoch` 的随机 epoch、单调 revision 与 recovering|ready 状态，Redis 保存
   无普通 TTL 的 marker；Redis marker 不能自证完整。三类新准入及旧 token/permit 的 Renew/Finish/Abort 都必须
   携带刚从 PostgreSQL 强一致读取的 expected epoch，并在 Lua 中与 marker、服务端记录三方匹配；读取失败或任一
   不匹配都 fail-closed。客户请求/control reconciler 不得创建或替换 marker。数据完整的短暂断连只有在 PostgreSQL
   epoch=ready、marker 的 epoch/revision 相同、Store、必需脚本和全部 control/pending operation 对账完成后才恢复
   readiness；单次 PING、marker 仅存在或只恢复 control 均不足。全量 flush、已申报回档或任何无法证明 active
   token/permit、限流窗口、breaker/cooldown/permission 完整的事件一律进入 runtime_state_lost。
7. 依赖 Store 正确性的 Admin Endpoint create/BaseURL/status/reset、Channel admission 与五个关键 setting
   发布操作在故障时返回 503，并继续遵守 prepare 前数据库不变、提交后 pending 等 recovery 的契约。PostgreSQL 只读管理页面可以用于诊断，但 runtime 必须
   明确显示“基础设施故障，准入已拒绝”，不能显示正常、无样本或降级权重。
8. routing trace 的 breaker_store_admission 只允许 normal|denied。Prometheus、readiness、结构化日志和 Admin
   都记录 breaker_store_unavailable、denied 与恢复；日志必须采样/限速，响应和日志不得泄露敏感数据或底层
   Redis 错误正文。
9. 本决议不自行定义 RPM/RPD/TPM/concurrency 的多实例参数来源与版本一致性；候选级资源顺序、原子边界和
   Store 错误策略由 DEC-041 固定，热更新对新旧请求的作用由 DEC-042 固定，权威参数来源与版本协议由
   DEC-043 固定。只要 BreakerStore 未成功签发入口 request-admission token，或未签发包含全部候选级资源的
   permit，就绝不能调用真实上游。
10. 全量运行态丢失优先恢复同一份可信 Redis 持久化备份。任何 restore/rollback 必须先阻断外部流量并创建 durable
    epoch operation；operation 必须持久化不可变 old/new epoch+revision、reason、state_loss_confirmed、detected_at/
    not_before，另有可 CAS 的 exact observed marker hash 与严格 recovery evidence。payload hash 只覆盖不可变
    transition，不覆盖 recovering|ready、activated_at、observed marker 或 evidence。Redis Prepare 把 exact old marker
    变为 pending（absent 时写同 token pending op）；确认成功后先 CAS operation preparing->prepared，随后 PostgreSQL
    事务才写 new epoch/state=recovering/revision 和 prepared->db_committed，不得跳过 prepared。该 pending 线性化点
    关闭“先读旧 PostgreSQL epoch、后写旧 marker”的竞态；新 epoch 才使 revision +1，同一 epoch recovering->ready
    不再次递增。回档覆盖 pending 时，reconciler 依据 durable transition 严格解码本次 observed marker；只有 absent
    或 epoch/revision 与 immutable transition old 值完全一致的 ready marker 才能更新 exact expected hash 并重建
    同 token pending，同 operation pending/new ready 只走幂等分支且不更新 expected；marker 真值只允许 absent、
    durable old ready、同 token pending、同 operation new ready和其它 conflict 五类，最后一类不得更新 expected、
    永不覆盖；new ready 也只有在 PostgreSQL epoch=next/recovering 且 operation=
    db_committed 时才允许收口数据库，否则继续隔离。即使 Prepare 后进程崩溃且 PostgreSQL 仍 preparing、Redis 又 flush，
    confirmed transition 仍必须重建 pending、补 prepared 并继续，绝不能 Abort 回旧 ready。

    未经过恢复 hook、且 marker 随快照回到同一旧值的回档无法由应用自行识别，生产部署必须以权限和恢复流程禁止。
    无可信备份时不提供人工 fail-open：所有旧 Gateway request/transport 必须全局排空或保持维护停机；无法可靠重建
    的限流事实等待最长窗口自然过期（当前 RPD 为近似滑动 24 小时）；breaker/open 与 429 cooldown 恢复或等待最大
    有效期；丢失的 Channel-Model permission pause 恢复，否则相关绑定全部按 recheck_required 排除并真实复检。
    达到 not_before 且 control、离线脚本和受审计 maintenance probe 通过后，受信任 reconciler 才可 Commit 同 token
    pending 为 new ready marker；普通 Gateway smoke 在 pending/recovering 时必然失败，不能作为 Commit 前提。Redis
    Commit 确认后，PostgreSQL 在一个事务中提交 epoch recovering->ready、operation db_committed->committed、
    completed_at/evidence/audit；禁止 SET NX、无条件覆盖或 db_committed Abort。外部 ingress 继续关闭，真实 Gateway
    smoke 成功后才开放客户流量。smoke 失败只保持 ingress 关闭并诊断；若 PostgreSQL/Redis 仍 matching ready 且未证明
    state loss，修复代码、配置或上游问题后在当前 epoch 重跑；只有独立证据确认 state loss 或决定 restore 时，才以
    当前 ready epoch 为 old 创建全新随机 epoch、revision+1 与新 durable operation并完整重走 Prepare/pending/Commit。
    禁止原地 ready->recovering 或复用已 committed operation/token。旧 key 缺失时迟到终态分别为
    unknown_request_admission/unknown_permit；回档旧 key 存在但 epoch 不符时为 stale_integrity_epoch。两者都不得
    释放/对账 Redis 资源或把 breaker 结果写入新 epoch。
```

原因：

```text
1. Redis/BreakerStore 承载全局 breaker、half-open lease、Endpoint BaseURL/status fence 和 Channel revision
   对账。Store 不可信时继续放行，Gateway 无法证明候选没有 open、停用、改址或属于旧配置。
2. fail-open 会让客户流量承担基础设施故障：已故障 Endpoint 会被重新冲击，balanced 请求增加失败/fallback
   延迟，fixed 或唯一渠道直接失败，还可能绕过管理员停用与地址切换围栏。
3. 当前 Gateway 启动和并发控制本来已依赖 Redis。只让 BreakerStore 假装 fail-open 并不能保证整套服务在
   Redis 故障时真正可用，反而制造不同准入组件语义不一致的假可用。
4. 立即返回可观测的安全 503，比在基础设施状态未知时继续调用上游更一致、可恢复，也符合“基础设施有问题，
   上层不继续提供服务”的运维原则。
5. 已经开始的上游调用是不可抹掉的业务事实。强杀会产生半截流、上游已计费但客户响应被截断以及账务恢复
   复杂度，因此 fail-closed 只禁止新 attempt，不伪造或取消既有调用。
```

影响：

```text
1. 删除 BreakerStore degraded admission 与所有 fail-open 配置/分支；Snapshot/Acquire 基础设施错误成为终止
   整次 fallback 的稳定 denied，而不是普通候选 skip。
2. Gateway 增加 BreakerStore readiness、PostgreSQL 持久化 epoch、Redis pending/ready marker、后台完整健康检查
   和 control/epoch recovery 门禁；liveness 与 readiness 分离，数据完整的短暂恢复不要求重启进程。
3. OpenAI 与 Anthropic 两个公开协议族的三个生成执行面统一安全 503；内部 request/trace 保存 denied 和零上游调用事实，Admin 显示基础设施故障，
   不显示降级权重或虚假正常。
4. Redis/BreakerStore 故障会让健康上游也暂时不可服务，这是本决议接受的取舍；生产发布必须依靠高可用
   Redis、容量规划、快速告警和恢复演练控制停机时间。
5. 测试覆盖局部 BreakerStore 错误、整套 Redis 停止、入口 Acquire/Reserve、Snapshot/候选 Acquire 各错误类型、
   停止 fallback、零上游调用、两个公开协议族的三个生成执行面/流式边界、既有 request token/permit 收口、
   readiness/liveness、数据保留型自动恢复，以及全量 state loss/旧快照回档在 control 已恢复后仍保持隔离、
   旧调用排空、24h 窗口/permission、PostgreSQL epoch 与 Redis pending/ready marker CAS 门禁。
6. breaker、Channel concurrency、RPM/RPD/TPM 和队首等待的先后顺序已由 DEC-041 固定，热更新时间边界由
   DEC-042 固定，多实例参数来源与版本一致性由 DEC-043 固定。无统一 permit 不调用上游仍是不可突破的最终门禁。
7. 具体接口、观测、测试、风险和完成门禁以 ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-041 候选级资源原子准入与统一 AttemptPermit 所有权

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；限额热更新对新旧请求的时间作用语义由 [DEC-042](#dec-042-channel-限额热更新只影响新-acquire既有-permit-自然排空) 补充，多实例限额权威与版本协议由 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威) 补充）

决策：

```text
每次真正准备调用某个 Channel 时，BreakerStore.AcquireAttempt 必须用一次 Redis Lua 原子完成候选级准入。
不得再分别取得 breaker、half-open、Channel concurrency 和 Channel RPM/RPD/TPM 后依赖调用方补偿拼接。

1. 请求级资源与候选级资源保持分层：API Key 解析出的 (route,user) RPM/RPD 在 ingress 由 DEC-043 的
   request-admission token 对每个客户请求只计算一次；对应 TPM 由同一 token 在进入候选循环前只预占一次，
   成功结算按最终真实 usage 对账，整次请求失败、取消或无结算时按 DEC-028 释放；(route,user) concurrency
   由该 token 覆盖整次客户请求并独立持有。fallback 到多个
   Channel 不得重复计算上述请求级资源。
2. 队首短等整次请求最多一次，只允许对已确认的 Channel concurrency 或 Channel RPM/RPD/TPM 容量不足进行
   等待。等待期间不持有任何候选级 permit、half-open、Channel concurrency 或限流预占；入口级资源仍按第 1 点
   覆盖整次请求。醒来后使用新的 permit_id 对冻结候选重跑完整原子 Acquire，不重新计算入口级 RPM/RPD/TPM。
3. 每次候选准入由同一个 Lua 原子检查 Endpoint BaseURL/status control、revision/fence 与有效状态，Endpoint/
   Channel breaker 和 half-open，Channel concurrency，Channel RPM/RPD 严格门槛，以及 DEC-028 的 Channel TPM
   准入门槛与预估 token 预占。脚本必须先完成全部参数、key 类型、revision 和门槛校验，再进入统一写阶段；
   只有全部通过，才能同时创建 active AttemptPermit、取得租约并写入限流预占。任一业务拒绝时所有候选级资源
   零变化，也不创建 request_attempt。
4. open、half_open_busy、concurrency_limited、rate_limited、stale revision 等业务拒绝返回稳定原因，可按
   既有线路内 fallback 和最终外部错误契约处理；Redis/BreakerStore 的连接、超时、Lua、协议、解析或内部错误
   按 DEC-040 立即终止整次 fallback，返回安全 503，不得伪装成普通候选 busy/429。
5. 调用方在 Acquire 前预生成 permit_id，并把完整 admission 指纹一并传入。若首次已经签发 permit，同 ID/
   同指纹的响应丢失重试返回同一 permit 且不得重复占资源；已存在 permit 的同 ID/不同指纹必须 conflict。
   业务 denied 不创建 permit 或 admission tombstone，是零取得且可重新评估的结果；已经观察到 denied 后的
   fallback/队首重试使用新的 ID。AttemptPermit 的服务端记录固化实际取得的 Channel concurrency lease、
   RPM/RPD 预占标记、TPM 原始桶/预占量和幂等回退/对账 token，调用方不得自行声明资源。
6. permit 已取得但尚未进入真实 transport 的所有路径必须 Abort，包括 attempt 创建失败、adapter 缺失、请求
   构造失败和调用前取消。协议无关 AttemptTimingObserver.TransportStarted 紧邻 http.Client.Do 的调用点作为
   本地分界；该回调前收口为 Abort。Abort first-terminal-wins，并按 permit 固化的原始桶精确归还 half-open、
   Channel concurrency、RPM、RPD 和 TPM 预占；窗口桶仍存在且 token 尚未终结时才修改，桶已自然过期视为
   该维度自愈，严禁重建旧桶或向当前桶写猜测性负数；不写 breaker 成功或失败。
7. AttemptTimingObserver.TransportStarted 已触发、完整请求交给真实 transport 后只能 Finish：释放 Channel
   concurrency；Channel RPM/RPD 作为真实上游调用保留到自然窗口，不因失败、取消或 fallback 回退；Channel
   TPM 有权威 usage 时按 cache-aware actual 与 estimate 对账，无权威 usage 时按 DEC-028 释放；对账/释放只
   修改仍存在的原始窗口桶，已自然过期时该维度 no-op，不得重建旧桶或写当前桶。breaker、错误事实与流式
   TTFT 继续遵守 DEC-033/035/036/038 的 revision、generation 和 request_mode。A 真实调用失败再
   fallback 到 B 时，客户请求级 RPM 只计 1，A/B 的 Channel RPM 各计 1。该本地分界不能消除回调与真实
   network dispatch 间的最后一跳崩溃不确定性，仍按第 10 点保守处理。
8. Renew 只延长 active permit、真实在途 Channel concurrency 与仍匹配的 half-open lease，不重复增加 RPM/RPD/
   TPM。Channel concurrency 由 permit renewer 唯一接管，P4 切换后旧独立 AcquireChannel/refresher 退出；入口级
   (route,user) concurrency 由 request-admission token 的续租器独立接管，不并入候选 permit。
9. Finish/Abort 的 breaker applied/stale disposition 与资源终结彼此独立。Endpoint BaseURL/status fence、
   Channel config revision 或 breaker generation 已变化，只能阻止旧结果写入当前 breaker/TTFT；只要服务端
   permit record 仍在，就必须释放并发并按真实调用事实保留、回退或对账限流资源。逻辑 expired 不复活 permit，
   但也不能把仍可识别的资源终结误写成纯 no-op；物理 key 丢失的 unknown 才依靠租约/窗口 TTL 自愈。
10. Gateway 在 Acquire 后崩溃，或 Acquire/Abort 用同一 permit_id 有界重试后仍无法确认时，不猜测是否已经发出
    上游请求，也不得恢复后补发。half-open/Channel concurrency 由 lease 回收，RPM/TPM/RPD 最坏分别保守保留
    到各自分钟/分钟/日窗口自然过期；短时过计优先于错误删除一次可能真实发生的上游调用。
11. 一张 AttemptPermit 最多对应一次 adapter 调用和一次真实 upstream HTTP transport。adapter、provider wrapper
    或 operation 内不得在同一 permit 下发第二次 HTTP；retry、跨 Channel fallback 与同 Channel operation fallback
    都必须返回 lifecycle，使用新 permit_id 重新 Acquire 并创建新 request_attempt。
12. Responses Compact 的 Native /responses/compact 404/405 -> Synthetic chat 是两次真实 transport。候选准备按
    Synthetic chat tokenizer 口径冻结同时覆盖两条路径的 ConservativeInputTokens；request-admission token 只 Reserve
    一次。Native 使用 upstream_operation=responses_compact 的 permit/attempt 并先 Finish，404/405 不进 breaker、
    保留第一次 Channel RPM/RPD且按无 usage 释放其 TPM；随后用同一 request token、同一 routing candidate 新 Acquire
    upstream_operation=chat_completions 的 permit并创建第二条 attempt。第二次业务 denied 不建 attempt/transport并按
    冻结顺序继续 fallback，Store 错误终止请求。入口资源只计一次，Channel RPM/RPD 计两次；2xx 缺 usage、解析失败
    和其它非 404/405 错误不得触发 Synthetic 二次调用。
13. 本决议只确定原子边界、计数次数和 permit 所有权。DEC-042 已确定限额热更新不追溯旧 permit、只改变
    新 Acquire 门槛；DEC-043 已确定多 Gateway 只接受 Redis admission control 的当前有效限额与独立 revision。

所有 `AcquireAttempt/Renew/Finish/Abort` Lua 都必须先校验 permit/terminal、相关 key 类型、resource token、
原始桶存在性和待写参数，再进入不应失败的统一写阶段；Lua 运行时错误不会自动回滚，禁止先释放/回退/写
terminal 后才发现畸形状态。缺失且已过期的窗口桶是预期 no-op，不得重建；其它 Store 错误按 DEC-040 处理。
```

原因：

```text
1. 当前候选路径把 breaker、Channel concurrency 和 RPM/RPD/TPM 分步取得。多个 Gateway 并发时，前一步成功、
   后一步拒绝或失败会留下半个资源；fallback 越多，额度和并发事实越容易失真。
2. API Key/线路级限制保护一次客户请求，Channel 限制保护每次真实上游尝试。把两者都塞进候选 permit 会让
   fallback 重复扣客户额度；把 Channel 资源留在 permit 外又无法原子判断和统一续租/释放。
3. permit 持有服务端 resource token，才能在响应丢失、重复 Finish/Abort、revision stale 和跨 Gateway 重试时
   精确知道应释放什么；调用方在收口时重新读取当前限额或当前时间桶会退错资源。
4. pre-transport 失败不是上游调用，应完整回退；真实 transport 已发生时 RPM/RPD 必须保留，TPM 继续使用
   DEC-028 的 cache-aware 预占/对账/释放口径，避免 P4 偷偷改变已经验证过的限流语义。
5. Gateway 崩溃无法与上游 HTTP dispatch 做分布式事务，Redis 不能可靠判断最后一步是否真实发出。unknown 时
   等待限流窗口自然过期虽会短时少放行，却不会误删一次可能真实发生的上游调用或形成永久泄漏。
6. Redis Lua 运行时错误不会自动撤销已经执行的写入；先校验、后统一写是“业务拒绝零资源变化”的必要条件。
7. Compact 的 Native 与 Synthetic 若共用一张 permit，第二次 HTTP 会少记一次 Channel RPM/RPD、覆盖 attempt 和
   错误归因；把 operation fallback 提升到 lifecycle 才能保持一 transport 一份权威事实。
```

影响：

```text
1. BreakerStore AcquireAttempt 扩展为统一 Channel admission Lua，入参包含调用方生成的 permit_id/admission 指纹；
   有效限额及版本按 DEC-043 从 Redis admission control 原子解析。AttemptPermit 保存服务端权威 resource token，Renew/Finish/Abort
   统一处理候选级资源。
2. Gateway 保留入口级 (route,user) concurrency 与 API Key/线路级 RPM/RPD/TPM 的请求级语义，但生产实现改由
   DEC-043 request-admission token 的 Acquire/Reserve/Renew/Finish 接管；Channel concurrency 删除旧独立
   AcquireChannel/refresher 接线，由 permit renewer 唯一接管。
3. 业务 denied 不创建 attempt 且所有候选资源零变化；Store 基础设施错误继续按 DEC-040 终止整次 fallback。
   所有候选因业务容量不足时最终返回 429、其它不可服务与混合原因返回 503 的边界由 DEC-048 确定。
4. revision/generation stale 只隔离 breaker/TTFT，不能阻止旧 permit 的并发释放和限流保留/回退/对账；status
   切换仍不清空已有 429 cooldown、RPM/RPD/TPM/concurrency 事实。
5. 测试新增单 Lua全有或全无、两 Gateway concurrency=1、A->B fallback 计数、Compact 404/405 双 permit/attempt/
   operation 与入口一次/Channel 两次计数、非回落错误单 transport、队首零资源、Acquire/Abort/
   Finish 响应丢失、kill -9 orphan、长流单续租、stale/expired 资源收口、Lua 畸形状态零部分写和 Store 503；
   覆盖长流/延迟 Abort 跨原始桶 TTL 后不重建旧桶、不污染当前窗口。
6. 具体接口、TTL、实施清单、E2E、风险和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-042 Channel 限额热更新只影响新 Acquire，既有 Permit 自然排空

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；本决议确定限额热更新的时间作用语义，多 Gateway 参数来源与版本一致性由 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威) 补充，`0=不限` 持续计数由 [DEC-044](#dec-044-0不限时仍持续记录准入用量) 补充）

决策：

```text
Channel concurrency_limit、RPM、RPD、TPM 限额允许热更新，但更新不能追溯改写已经签发的 AttemptPermit。

1. 已签发 permit 沿用签发时固化的 resource token、原始窗口桶和预占事实。限额降低后不强制取消 transport、
   不提前终止 permit，也不要求 Renew 按新限额重新准入；该 permit 的 Channel concurrency 仍计入当前在途数。
2. 只有新的 AcquireAttempt 才按准入系统已经认定的当前有效限额执行 DEC-041 单 Lua 原子准入。仅持有候选
   快照、尚未取得 permit 的请求不享有旧限额；fallback 与队首等待后的再次 Acquire 同样属于新准入。
3. concurrency 从 5 降到 2、当前 active=4 时，4 个旧调用继续，新 Acquire 在 used>=2 时拒绝，直到旧 permit
   Finish/Abort 或 TTL 回收到 used<2 后才能再取得一个名额。降低 RPM/RPD/TPM 时同理：保留已有窗口用量，
   新 Acquire 按新阈值判断，等待窗口自然回落，不追杀历史请求。
4. 提高限额后，下一次已经使用当前配置的新 Acquire 可以立即使用新增余量，不等待旧 permit 或旧窗口过期。
   “立即”指准入系统已经认定新配置为当前后的下一次 Acquire；不能让各 Gateway 各自猜测生效时刻。
5. 升额、降额、有限与 0=不限、NULL=继承默认之间切换，都不清空 Channel concurrency active set，也不删除、
   归零或重建 RPM/RPD/TPM 历史窗口，不推进 breaker generation，不清 breaker、错误窗口、退避、half-open 或
   流式 TTFT。限额变化只改变后续准入门槛，不改写已经发生的资源事实。
6. 旧 permit 的 Renew/Finish/Abort 始终按签发时服务端固化的 resource token、原始桶和预占量收口，不重新
   读取当前限额。Renew 只续既有 concurrency lease；Finish 释放并发、保留真实 RPM/RPD 并按 DEC-028 对账
   TPM；Abort 精确归还 pre-transport 资源。限额变化不得导致重复释放、错桶回退或把旧 permit 判成无资源。
7. 本决议已经确定“旧 permit 不追溯、新 Acquire 用当前限额、降额自然排空、升额立即可用、历史窗口不清零”。
   DEC-043 进一步确定 Redis admission control 是唯一当前权威，使用独立 admission_limits_revision；DEC-054
   将共享默认拆为渠道默认与线路默认。PostgreSQL Channel override 与渠道默认通过 fail-closed
   prepare/commit/recovery 线性化发布；携带旧
   revision 的 Acquire 必须拒绝并重新读取当前 control，不能继续按旧高限额放行。
8. DEC-044 已确定 `0=不限` 期间仍持续记录 RPM/RPD/TPM 和 Channel concurrency 使用事实；从不限切换为
   有限值时，下一次 Acquire 立即使用完整历史窗口判断，不从零重新计数。

“等待自然回落”只表示未来新请求何时恢复资格，不新增无限阻塞：每个客户请求仍只允许 DEC-041 规定的一次
队首短等，超时后正常 fallback/收口。成功 Acquire 因响应丢失使用同 permit_id 重取原 permit，不算新 Acquire，
也不按更新后的限额重验。配置版本不得进入 concurrency/RPM/RPD/TPM resource key 或窗口桶 key；版本只校验
新准入门槛，stable resource key 保证历史 used 连续。temporary over-limit 的 remaining 必须 clamp 为 0，不能
产生负 capacity/weight；0=不限按 unlimited 口径处理，不能误作零容量。
```

原因：

```text
1. 降额时强杀旧流会让客户收到半截响应，而上游可能已经计费；旧调用是已经发生的业务事实，不能被配置
   保存追溯撤销。
2. 若旧 permit 在 Renew/Finish/Abort 时重新读取新限额，可能被误判“超限”而丢失并发释放、TPM 对账或精准
   Abort；服务端 resource token 才是旧调用的稳定所有权事实。
3. 限额只描述允许多少新流量，不是 breaker 状态或历史用量。更新时清空并发/窗口/breaker/TTFT 会人为赠送
   额度、失去真实容量，或把健康样本与故障证据无故删除。
4. 降额允许短暂 used>limit 并自然排空，既保护在途客户，又保证新请求不会继续扩大超限；升额只需要让
   之后的新 Acquire 看到新增余量，不需要重签旧 permit。
```

影响：

```text
1. AttemptPermit 与 resource token 保持签发时事实；Renew/Finish/Abort 不读取当前限额，限额热更新不会改变
   terminal first-wins、原始桶或预占量。
2. 新 Acquire 必须使用 DEC-043 的 Redis admission control 当前有效限额；pending、control 缺失或 revision stale
   时 fail-closed，不允许任何 Gateway 使用本地旧默认继续准入。
3. Admin runtime 允许显示 temporary over-limit，例如 used=4/limit=2/remaining=0；这不是资源泄漏，也不能通过
   删除旧 lease 修正。
4. 测试覆盖 concurrency 5->2/2->5、RPM/RPD/TPM 高低调整、0/NULL/有限值切换、历史窗口不清零、旧 permit
   并发 Renew/Finish/Abort、限额变化不清 breaker/TTFT，以及只持旧候选/队首醒来必须作为新 Acquire。
5. 具体实施清单、Redis/生命周期/E2E、风险和完成门禁以
   ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-043 Redis admission control 是多 Gateway 当前限额权威

状态：accepted（本项决议已于 2026-07-21 定稿并实现；补充 [DEC-041](#dec-041-候选级资源原子准入与统一-attemptpermit-所有权) 与 [DEC-042](#dec-042-channel-限额热更新只影响新-acquire既有-permit-自然排空)；原共享 rate control 的部分由 [DEC-054](#dec-054-线路默认限流与渠道默认限流完全拆分) 修订）

决策：

```text
PostgreSQL 继续保存限额配置的管理事实；Redis admission control 是所有 Gateway 执行入口与候选新准入时
唯一可信的当前生效事实。Gateway 本地 settings cache、Guard/ConcurrencyLimiter 默认或候选行中的旧值都不能
直接决定准入门槛。

1. Channel 增加 `admission_limits_revision bigint`，从 1 开始且只在 rpm_limit、tpm_limit、rpd_limit 或
   concurrency_limit 真变化时递增；不复用 channel_config_revision，改限额不得清空 breaker、错误窗口、
   退避、half-open 或流式 TTFT。
2. `app_settings` 每行增加 `revision bigint`；线路默认限流、渠道默认限流与 `gateway.concurrency_defaults`
   各自具备单调 setting revision。Redis 分别保存线路默认、渠道默认、并发默认与 Channel override control。
   第一个入口 Lua 只读取线路默认并合并可信认证快照中的线路/API Key 显式 override；`SnapshotMany` 与
   `AcquireAttempt` 只读取渠道默认并合并 Channel override；两阶段共同从 `gateway.concurrency_defaults` 分别取
   `key_limit` 与 `channel_limit`。限额均解析 `NULL=继承默认、0=不限、>0=明确上限`。禁止 Gateway 从不同版本
   的本机值拼出门槛，也禁止候选 Acquire 传入 effective limits、窗口桶、lease 或 resource token，后两类资源
   只能由 Lua 从 control 读取并生成。具体 key 与作用域由 DEC-054 固定。
3. 新增 `runtime_control_operations`，统一承载 Channel 四维限额、五个关键设置
   `gateway.route_rate_limit_defaults|gateway.channel_rate_limit_defaults|gateway.concurrency_defaults|gateway.circuit_breaker|gateway.routing_balance`，以及
   DEC-040 维护专用 `gateway.runtime_state_epoch` 的单目标 durable 发布。Channel、普通 setting 与 epoch 三种目标
   互斥，同一目标同时最多一条非终态 operation；epoch 行不进入普通 Admin settings API/cache。Endpoint
   BaseURL/status、Endpoint create 和 Provider status batch 继续使用专用 `endpoint_routing_operations`，不能
   把两种恢复模型混入同一张表。operation 固化唯一 token、current/next revision 和规范化 payload hash，
   next 必须等于 current+1；epoch kind 额外持久化不可变 transition、可 CAS exact marker hash 与 recovery evidence，
   非 epoch kind 对应列必须为空。终态 operation 与 Redis op tombstone 至少保留 24 小时，非终态不得清理。
4. runtime operation 状态机严格为 `preparing -> prepared -> db_committed -> committed`，普通 Channel/setting
   operation 另允许 `preparing|prepared -> aborted`。先持久化 preparing，再执行 Redis Prepare 并把 operation CAS 为 prepared；
   随后在同一 PostgreSQL 事务提交业务值/revision 与 db_committed，最后执行 Redis Commit 并把 operation
   终结为 committed。只有业务 revision 尚未提交的 preparing/prepared 可以 Abort；db_committed 表示
   PostgreSQL 已经提交权威业务事实，只能重试/恢复 Commit，严格禁止转为 aborted。对 runtime_state_epoch，
   Prepare 将精确旧 marker 切为 pending（absent 时写 pending op），确认 Redis 结果后必须先 CAS preparing->prepared；
   业务事务再写新 epoch/state=recovering 与 prepared->db_committed。恢复门禁通过后 Commit 新 ready marker，最后
   在一个 PostgreSQL 事务完成 epoch ready、operation committed、completed_at/evidence/audit。三种正式 epoch reason
   `bootstrap|state_loss|restore` 均确认 state loss；epoch operation 在任何阶段都不得 Abort，本期不提供
   `abort_allowed` 字段、Abort API 或 Lua，只能保持隔离并 Recover/Commit。状态机不能跳步，不允许 SET NX 快捷路径。
5. PostgreSQL 保存业务值、单调 revision、持久化完整性 epoch 和 durable operation，是管理与恢复事实；Redis control 同时保存
   committed active 与可空 pending payload/revision，是热路径执行事实。Redis Lua 无法查询或验证 PostgreSQL：
   受信任的 application service/reconciler 必须先读取并验证 PostgreSQL operation、业务 revision 与 payload
   hash，再携带 token/hash/current/next revision 调用 Lua；Lua 只校验 Redis control/op record 中的 token、
   hash、active/pending revision 并原子迁移 Redis 状态，不能自行决定数据库是否已提交。普通 control 的 payload
   hash 覆盖完整目标值；epoch payload hash 只覆盖 immutable transition envelope，recovering|ready、activated_at、
   expected marker 与 recovery evidence 由 operation/state CAS 分别校验，不能让一个 hash代表两个可变状态。
6. Redis Commit 响应丢失或提交结果暂时无法确认时，Admin 返回 runtime_sync_pending；reconciler 按
   PostgreSQL 业务 revision、payload hash、operation state 与 Redis op record 重试/恢复。pending、control
   缺失、payload hash 不一致或 revision stale 时，新的入口 admission 或受该 control 约束的 Snapshot/Acquire
   一律 fail-closed，旧 Gateway 不得按旧高限额继续放行；已签发 token/permit 仍按服务端 resource token 完成
   资源收口。线路默认 control 的 pending 只阻止新的 request admission；渠道默认 control 的 pending 只阻止新的
   候选 Snapshot/Acquire，不能把两个作用域重新绑成同一 revision。
7. 单个 control absent、但 DEC-040 PostgreSQL ready epoch 与 Redis marker 强一致匹配、其它运行态仍可证明完整时，恢复不能受普通 Prepare 的
   next=current+1 或初始 0->1 限制。仅受信任的
   reconciler 在锁定并验证 PostgreSQL 当前事实，以及存在时的非终态 durable operation 后，可 recovery-only 安装当前任意 revision；
   已存在 control 绝不覆盖，preparing/prepared 只恢复旧 active 并 Abort，db_committed 直接恢复新 committed
   active 且严格禁止 Abort。即使当前 revision>1 且旧终态 operation 已清理，也必须能够恢复。全量 flush、marker
   缺失/失配或 PostgreSQL epoch=recovering 时可以在隔离中重建 control，但不得据此恢复 readiness，必须执行
   DEC-040 的完整 state-loss/restore 门禁；普通 control recovery 绝不能创建、替换或激活完整性 marker。epoch pending
   被 flush/回档覆盖时，只能按其 durable transition 和 exact observed-marker 真值表重建同一 pending fence；只有
   absent 或与 immutable transition old epoch/revision 完全一致的 ready marker 才可更新 expected hash，同 operation
   pending/new ready 走幂等分支且不更新 expected，其它 marker 不得更新 expected、必须 conflict。已经是
   同 operation new ready 时也只有 PostgreSQL epoch=next/recovering 且 operation=db_committed 才能原子收口，任何
   其它 marker/数据库组合都 conflict 并继续隔离。
8. 第一个入口 Lua 先使用强一致 PostgreSQL ready epoch/revision 与 Redis marker 做相等校验，再返回 request-scoped 服务端 token，原子取得该客户请求唯一一次的 route-user RPM/RPD 与
   concurrency；输入 token 估算后的 TPM 预占是同一 token 的一次性幂等延续，不因后续 control pending/换版
   重验。首次 Reserve 的估算值与 reserved|limited 结果必须固化，同值重试取回第一次结果、异值 conflict；
   续租复用同一 token 且只延长 concurrency lease。协议感知 route wrapper 唯一拥有 Acquire、renewer 和最终
   FinishRequestAdmission；service 只能 Reserve 和发布非 partial 权威 usage，不能提前 Finish。outer finalizer 在
   handler JSON/SSE 写出结束后恰好一次释放 concurrency、保留 RPM/RPD，并按可空权威 usage 对账或释放 TPM；
   fallback 不重复计入口资源。入口接口必须区分 allowed/limited/store_unavailable/runtime_sync_*，只有真实 limited
   返回 429，基础设施或 control 同步错误返回安全 503。现有 AttemptRunner Key TPM Guard/route-user reservation 与
   新 session 互斥，P4 切换时必须删除其生产接线。
9. 默认更新不逐 Channel fan-out。入口 Lua 每次读取线路默认 active revision；候选 Lua 每次读取渠道默认 active
   revision 与 Channel override。两个作用域分别线性化，因此不会因不同 Gateway 的 settings applier 时间差执行
   不同阈值，也不会因调整线路默认而错误改变渠道 fallback，反之亦然。
10. request token 只固化入口 Acquire 时的完整性 epoch、线路默认限流 revision、并发默认 revision 和入口
    resource token；它在选定 Channel 前签发，绝不能包含渠道默认或 Channel admission revision。AttemptPermit
    固化同一完整性 epoch、候选 Acquire 时的渠道默认限流 revision、并发默认 revision、所选 Channel admission
    revision 和候选 resource token。两次 Acquire 之间普通 control 换版时允许旧 request token 保持线路默认
    revision N、后续新 permit 使用当时当前的渠道默认/Channel revision；各资源所有者内部必须一致。成功 Acquire
    使用同 ID 重试时取回原记录；普通限额 revision 更新不追溯已签发记录，但 DEC-040 完整性 epoch 换代会使旧
    token/permit 所有 Redis 写入 fail-closed。
11. concurrency/RPM/RPD/TPM resource key 与窗口桶使用稳定 key，不带 admission revision；revision 只决定
    新 Acquire 使用哪套门槛，更新前后的真实 used 必须连续。
12. 旧 `gateway.rate_limit_defaults`、其 `failure_policy` 与全部 fail_open 分支退役。Redis/control 不可信时继续按
    DEC-040 fail-closed；`settingsApplier`、`Guard.SetDefaults/SetFailOpen`、`ConcurrencyLimiter.SetDefaults` 和
    普通 Redis settings cache 不再作为五个关键设置的生产执行权威，旧 JSON/cache 必须版本化或清理。
```

原因：

```text
1. 当前 settings applier 在每个 Gateway 内独立轮询，限额从 5 降到 2 时可能出现 A 已更新、B 仍按 5 放行。
2. 限额若复用 channel_config_revision，会让纯粹的容量修改错误清空 breaker 和 TTFT；独立 revision 才能只
   改变新准入门槛。
3. PostgreSQL 与 Redis 的发布若没有 pending fence 和 recovery，数据库已经显示新值时仍可能有实例继续执行
   旧值。Redis 作为执行面唯一权威，才能在线性化多 Gateway 的实际生效点。
4. stable resource key 保留更新前后的真实历史用量，避免改配置时通过换桶变相赠送额度。
```

影响：

```text
1. Channel schema 增加 admission_limits_revision，app_settings 增加逐行 revision及维护专用 runtime_state_epoch 行；新增
   `runtime_control_operations` 及同目标非终态唯一约束，Redis 新增 active/pending admission/runtime control 与
   有界 operation tombstone、request-admission token；epoch operation 额外持久化 immutable transition、exact marker
   hash 和 evidence。线路默认与渠道默认使用独立 control/revision；入口与 AcquireAttempt 不再接收或信任 Gateway
   本机默认。
   request/permit key 同时固化各自完整性 epoch，runtime epoch 使用同表 durable pending/commit recovery。当前 10 个认证 `/v1` method+path 都取得 request token；只有 Chat、Responses、Compact、Messages Reserve TPM，
   本地/静态 surface 不 Reserve、不创建 permit/attempt。
2. Admin 分别保存线路默认与渠道默认限流，并返回 active|runtime_sync_pending 等同步状态；runtime 必须分别展示
   两套 active revision。
3. 两 Gateway 测试必须覆盖入口与 Channel 升降额、NULL/0/有限值、旧 revision Acquire、fallback 单次入口计数、
   429/503 错误区分、Prepare/Commit/Abort 响应丢失、`db_committed` 永不 Abort、stable revision>1/op 已清理后的
   单 control recovery、全量 state-loss/旧快照回档隔离、epoch 五分支 marker 真值表、Prepare 后/PG 前崩溃、
   pending 丢失重建、原子 PostgreSQL 终结与线路/渠道默认独立切换；Lua 只断言 Redis 原子校验，PostgreSQL 前置条件在
   service/reconciler 证明。
4. 具体 key、Lua、发布步骤和完成门禁以 ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md 为准。
```

## DEC-044 0=不限时仍持续记录准入用量

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；补充 [DEC-042](#dec-042-channel-限额热更新只影响新-acquire既有-permit-自然排空) 与 [DEC-043](#dec-043-redis-admission-control-是多-gateway-当前限额权威)）

决策：

```text
concurrency/RPM/RPD/TPM 的有效限额为 0 时，只表示该维度不以阈值拒绝新准入，不表示停止维护使用事实。

1. 入口与 Channel RPM/RPD/TPM 在 0=不限期间都继续写入与有限值相同的稳定窗口。候选 pre-transport Abort
   精确回退候选预占，真实 transport Finish 保留 Channel RPM/RPD 并按 DEC-028 对账 TPM；入口 RPM/RPD 对已
   接收的客户请求只计一次并保留，入口 TPM 在无用量事实时按 request token 释放或对账。
2. Channel 与入口 (route,user) concurrency active set 无论当前 limit 是否为 0 都持续维护和续租。
3. 0 -> 有限值后，使用新 active admission revision 的下一次 request token/AttemptPermit Acquire 立即依据完整
   历史窗口和当前 active 数判断；不得从零开始重新计数，也不得额外赠送一个分钟、日或并发窗口。
4. 有限值 -> 0、0 -> 有限值、NULL=继承默认与其它限额更新都不得删除、归零或换掉稳定资源 key；旧 request
   token/permit 始终按签发时 resource token 收口。
5. 不限期间增加 Redis 写入和内存占用是已接受的安全取舍；Redis 或 control 故障继续按 DEC-040 fail-closed。
```

原因：

```text
如果不限时不计数，过去一分钟已经真实调用 300 次后把 RPM 改为 100，系统会把窗口误认为 0 并再放 100 次。
持续计数能让启限后的第一条新请求立即遵守过去一分钟已经发生的真实用量。
```

影响：

```text
1. 入口与 Channel 限流 Lua 都不能在 limit=0 时直接跳过写桶；并发实现也不能停止维护 active lease。
2. 容量与 Admin runtime 在 unlimited 状态仍可显示 used，但 remaining 使用 unlimited 口径，不能误算为负值
   或零容量。
3. 测试覆盖 0 期间累计请求后切换 RPM/RPD/TPM/concurrency 有限值，以及 Abort/Finish/TTL 边界。
```

## DEC-045 只有真实上游责任结果进入 breaker，并删除进程内失败软冷却

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；修订 [DEC-029](#dec-029-慢上游--客户端重试风暴防护在途并发上限--失败软冷却唯一渠道保护) 的失败软冷却部分）

决策：

```text
只有已经进入真实 transport、且可以合理归因给上游 Channel 或 Endpoint 的结果可以进入 breaker。平台内部
错误不能伪装成渠道失败，旧进程内 failure soft-cooldown 在 P4 删除。

1. DNS、TLS、连接拒绝/重置、EOF、网络断开、连接 timeout，以及 HTTP 502/503/504，计入 Channel 与
   Endpoint；它们通常与账号无关，属于公共 Endpoint 故障。
2. HTTP 500、首 token timeout 和 body 读取 timeout 先计入 Channel；只有短窗内至少两个不同 Channel、
   且至少两个不同请求模型出现同类证据时，才计入 Endpoint。单模型或单 Channel 的问题不能摘除整个地址。
3. HTTP 429 不进入 Channel 或 Endpoint breaker，只写 Redis 全局 Channel cooldown，遵守 Retry-After 和
   系统上限；不能在 cooldown 之外再用比例窗口把同一限流熔断一次。
4. HTTP 403 只暂停当前 Channel-Model 绑定并触发权限/凭据复检，不直接摘除整条 Channel；HTTP 401 继续
   由凭据闸门处理。HTTP 400/404/405/422 与客户取消不进入 breaker；Compact 的 404/405 只表示原生
   operation 不支持。
5. 真实上游返回 2xx 但响应不符合协议时计入 Channel，不扩大到 Endpoint；adapter 查找、请求编码/构造、
   adapter 本地实现错误、Store/Redis、数据库、settlement、账务或客户响应写出错误均不进入 breaker。
6. 删除 gateway.channel_failure_cooldown_ms、进程内 failure registry 与候选 demote。低流量快速保护由
   DEC-046 的 Redis 全局连续失败触发承担；多个 Gateway 对同一 Channel 使用同一运行事实。
```

原因：

```text
1. 把平台编码、Store 或数据库错误算给 Channel，会在上游完全正常时误熔断渠道，掩盖真正的平台事故。
2. 500 和读取慢可能只影响一把 Key 或一个模型；需要跨 Channel、跨模型证据后才能证明是公共 Endpoint。
3. 429 已明确告诉系统何时退避，重复进入 breaker 会把短时限流放大成更长停机。
4. 进程内软冷却让 Gateway A、B 排序不同，无法形成全局、可解释的路由事实。
```

影响：

```text
1. failure 分类必须携带 transport_started、HTTP 状态、scope attribution 和请求模型证据，禁止靠上游 body
   字符串判断。
2. channel_models 需要可审计的 403 暂停/复检状态机；429 cooldown 改为 Redis 全局事实。
3. 403 复检只有在 Channel config、Endpoint BaseURL/status revision 与 model_id 全部匹配时才能 CAS 恢复；
   失败/stale 保持暂停，不翻整个 Channel credential_valid。429 cooldown 跨 Gateway 共享，Reset breaker 不清
   cooldown 或 permission key。
4. 测试覆盖每类错误的 Channel/Endpoint/不计入矩阵、同错误类别跨 Channel+跨模型门槛，以及平台错误零
   breaker 变化；不同错误类别不能拼成 Endpoint 样本。
```

## DEC-046 熔断阈值、样本边界与恢复退避

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；错误分类以 [DEC-045](#dec-045-只有真实上游责任结果进入-breaker并删除进程内失败软冷却) 为前提）

决策：

```text
Channel 与 Endpoint 使用 Redis 全局双触发状态机：

  快速触发：10 秒内连续 3 次可归因故障
  比例触发：30 秒内至少 20 个 eligible 上游样本，且 eligible_failure / eligible_total >= 50%

1. 比例窗口的分子只能是 DEC-045 已归因到对应 Channel 或 Endpoint 的真实上游错误。
2. 分母只能是已经真实调用上游、且能够评价该作用域质量的 eligible_success + eligible_failure。pre-transport
   失败、Store/Redis、数据库、编码/请求构造、adapter 查找或本地实现错误、settlement/账务、客户响应写出、
   客户取消，以及 401/403/429/400/404/405/422 等被 DEC-045 排除的结果，既不进分子也不进分母。
3. 只有对应作用域的 eligible_success 才清空连续失败；被排除结果不能冒充成功清零，也不能增加失败。
4. half-open 同一作用域同一时刻只放一个探测；必须由两个不同且仍有效的 permit 连续成功才恢复 closed，
   重复 Finish 不能充当第二次成功；任一次可归因失败立即重新 open。
5. 重复 open 依次退避 15、30、60、120、300 秒；closed 稳定一个完整 30 秒窗口后重置退避等级。
6. open 是硬摘除；minimum routing factor、sticky 或 fixed 都不能把 open/half_open_busy 候选带回普通流量。
7. `gateway.circuit_breaker` 继续以 PostgreSQL `app_settings` 行保存管理事实并维护独立单调 revision；Redis
   revisioned runtime control 同时保存 committed active 与可空 pending payload/revision，作为多 Gateway
   Snapshot、Acquire 和 Finish 更新 breaker 状态时的当前执行事实。普通 Redis settings cache 和各实例
   settings applier 不是执行权威。
8. circuit-breaker 设置与 Channel/global admission、`gateway.routing_balance` 共用 DEC-043 的
   `runtime_control_operations` durable publisher 和 Prepare/Commit/Abort/Recovery 协议。pending、control
   缺失、payload hash 不一致或 expected revision stale 时，新 Snapshot/Acquire fail-closed；PostgreSQL 已到
   db_committed 后只能恢复 Commit，不能 Abort。受信任的 application/reconciler 先验证 PostgreSQL，Lua 只
   原子验证 Redis token/hash/revision，不能声称 Lua 查询了数据库。
9. active control 激活后无需重启 Gateway。request-admission token 与 AttemptPermit 共用本决议设置中的
   `attempt_permit_*` 生命周期参数；两类新 Acquire 使用新 TTL/renew/terminal 值，已签发 token/permit 沿用
   服务端固化参数；后续 breaker 样本更新使用当时 committed active 的窗口、阈值和退避配置。热更新不清空
   breaker、eligible 窗口、连续失败、open/half-open、TTFT、限额窗口、429 cooldown、permission 或 active
   request token/permit；需要清空 breaker 只能执行管理员 Reset。
10. `enabled=false` 只关闭 Endpoint/Channel breaker 的 open 门禁与新 eligible 窗口写入，不能关闭或绕过
    request admission、AttemptPermit、Endpoint BaseURL/status revision 围栏、Channel-Model permission、429 cooldown、
    入口或 Channel concurrency/RPM/RPD/TPM，Redis/BreakerStore 故障仍按 DEC-040 fail-closed。关闭与重新开启不隐式删除运行态；
    重新开启前按 Redis TIME 淘汰过窗样本和已到期 open/lease，再从剩余当前事实继续。流式 TTFT 归
    `gateway.routing_balance` 管理，不因 breaker disabled 停止采集。
```

原因：

```text
用户明确要求 50% 只能反映渠道错误，而不是平台自身错误。若把平台错误或未调用上游的失败塞进分母/分子，
Channel 可能被无辜熔断，或真正的上游故障被大量无关样本稀释。快速触发保护低流量渠道，比例触发抑制偶发
错误；两者配合才同时覆盖低流量连续故障和高流量错误率故障。
```

影响：

```text
1. Finish outcome 必须显式携带 eligible、success/failure 和两个作用域各自 attribution；Lua 不从笼统 attempt
   status 猜测样本资格。
2. Redis 保存连续失败、eligible 成功/失败窗口、open level 和 half-open permit 身份，全部使用 Redis TIME。
3. 测试至少覆盖 20 样本 10 失败触发、19 样本不触发、平台错误不增分子/分母、被排除结果不清连续失败、
   两个不同 permit 恢复和完整退避序列。
4. 设置/API/Redis/多 Gateway 测试必须覆盖 revision 热更新、pending/缺失/stale fail-closed、commit 响应丢失
   recovery、旧 permit 参数固化，以及 enabled=false 不绕过任何非 breaker 门禁、重新 enabled 不清状态。
```

## DEC-047 Balanced 默认参数进入系统设置并支持热更新

状态：accepted（本项决议已于 2026-07-21 定稿并实现；不含成本的最终权重公式由
[DEC-055](#dec-055-balanced-加入受限渠道成本因子并修正过期错误窗口) 修订，TTFT 样本来源继续遵守
[DEC-035](#dec-035-仅以流式-firsttoken-生成唯一-ttft-ewma)）

决策：

```text
Balanced 继续以容量为基础，只使用当前 eligible 错误率和唯一流式 TTFT EWMA 调整权重。初始默认参数为：

  ttft_target_ms = 2000
  ttft_weight = 0.35
  minimum_routing_factor = 0.05
  ttft_ewma_alpha = 0.2

1. 计算保持：error_factor = 1 - clamp(error_rate, 0, 1)；latency_penalty = ttft_ewma_ms /
   (ttft_ewma_ms + ttft_target_ms)；routing_factor = max(minimum_routing_factor,
   error_factor * (1 - ttft_weight * latency_penalty))；final_weight = capacity_score * routing_factor。
2. TTFT 更新固定为 ewma_new = alpha * sample + (1 - alpha) * ewma_old，首个样本直接初始化。Channel
   没有流式 TTFT 样本时延迟项中性；流式与非流式调度读取同一个 stream-only TTFT EWMA。
3. minimum_routing_factor 只适用于 closed 且已经通过 status、credential、revision、breaker 与容量硬门禁的
   Channel，用于保留少量新样本；open、half_open_busy、disabled、stale 或容量为零的候选权重仍为 0。
4. 四项参数替换现有系统设置 gateway.routing_balance 的 `enabled/weight_by_remaining` 值形状；线路是否使用
   balanced 继续由 route mode 决定。新形状严格解码并维护独立单调 setting revision。PostgreSQL
   `app_settings` 行是管理事实；Redis revisioned runtime control 同时保存 committed active 与可空 pending
   payload/revision，是多 Gateway 评分与 TTFT 更新的执行事实，不能依赖各 Gateway 轮询或本机 settings cache。
5. routing-balance 与 Channel/global admission、`gateway.circuit_breaker` 共用 DEC-043 的
   `runtime_control_operations` durable publisher 和 Prepare/Commit/Abort/Recovery 协议。受信任的
   application/reconciler 先验证 PostgreSQL operation、业务 revision 与 payload hash，再调用只验证 Redis
   token/hash/active/pending revision 的 Lua；db_committed 只能恢复 Commit，不能 Abort。
6. 成功 `SnapshotMany` 是本次客户请求的 routing-balance revision 线性化点。Snapshot 返回 committed active
   revision/payload，本请求后续候选评分和排序固定使用该快照；Snapshot 后才激活的新 revision 不重排本请求，
   下一客户请求必须读取新 revision。pending、control 缺失、payload hash 不一致或 Snapshot 携带的 expected
   revision stale 时评分 fail-closed，不得退回本机旧参数；Snapshot 之后 Acquire 仍独立校验 circuit-breaker
   与 admission revisions。
7. 参数热更新只改变后续评分，以及 control 激活后执行的后续流式样本 EWMA alpha，不清 breaker、错误窗口、
   TTFT 样本、限额窗口、429 cooldown、Channel-Model permission、sticky 或在途 permit。
8. 校验要求：ttft_target_ms > 0，0 <= ttft_weight <= 1，0 < minimum_routing_factor <= 1，
   0 < ttft_ewma_alpha <= 1；未知字段拒绝。
```

原因：

```text
2 秒目标和最多 35% 的延迟降权能体现首 token 速度但不让速度压倒错误率与容量；5% 最低因子让尚未 open 的
渠道保留恢复样本；alpha=0.2 避免单个偶发慢请求让权重剧烈抖动。放入系统设置后可以按真实运行数据调整，
无需重启或修改代码。
```

影响：

```text
1. gateway.routing_balance JSON、Admin 系统设置、`runtime_control_operations`、Redis active/pending control、
   scorer、trace 与测试统一增加四项参数和 setting revision。
2. routing trace 必须保存使用的 setting revision 和四项参数快照，便于解释一次最终权重。
3. 测试覆盖首样本、alpha 公式/热更新、边界校验、无 TTFT 样本、stream/non-stream 共用 EWMA、最低因子与
   open/busy/stale/permission/容量为零等硬摘除不被最低因子绕过，以及 unlimited/temporary over-limit 容量；
   两 Gateway 还必须覆盖 Snapshot 线性化、pending/stale/缺失 fail-closed 和 runtime publisher recovery。
```

## DEC-048 外部 429、503 与 model_not_found 边界

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；不改变内部完整 routing trace 与排除原因）

决策：

```text
客户最终错误按聚合后的原因分类，不能把所有 no-available 都统一成同一个状态码：

1. 客户自身 (route,user) / API Key RPM、RPD、TPM 或 concurrency 超限，返回协议原生 429。
2. 模型存在，且所有内部候选都只因 Channel concurrency/RPM/RPD/TPM 容量不足或 429 cooldown 不可用，
   返回安全 429。系统能够证明最早恢复时间时返回 Retry-After，秒数向上取整并限制在 1..300。
3. breaker open、有效配置或模型绑定为空、credential gate、revision/control pending/stale、Store/Redis/DB
   基础设施故障，或候选排除原因混合，返回安全 503 service_unavailable。
4. 模型真实不存在或客户无权访问时保持现有 model_not_found，不得伪装成 429 或 503。
5. OpenAI Chat/Responses 的 429 使用 rate_limit_error/rate_limit_exceeded，503 使用
   api_error/service_unavailable；Anthropic 使用相应协议原生 error envelope。
6. 响应正文和 header 不得出现 Channel、Provider、Endpoint、BaseURL、候选数、breaker 状态或内部
   failure.Code；保留 Unio request_id 用于报障。流式首帧写出后不能重写 HTTP 状态，只按协议内中断和
   partial settlement 边界收口。
```

原因：

```text
429 告诉 Codex、Claude Code 等客户端这是有明确退避意义的容量问题；503 表示平台当前不能证明有可用服务。
混为 503 会失去正确的限速重试语义，混为 429 又会把配置、breaker 或基础设施事故伪装成客户超限。
```

影响：

```text
1. routing 最终结果需要聚合候选排除原因，handler 不能只检查一个 ErrNoAvailableChannel。
2. 两个公开协议族的三个生成执行面 handler、首帧前流式错误、Retry-After 和 SDK 黑盒测试都要覆盖纯容量、纯 503 与混合原因。
3. request/trace 继续保存内部原因，但公开错误只输出上述稳定、安全类别。
```

## DEC-049 Admin 删除主观健康标签，只展示客观事实

状态：accepted（本项决议已于 2026-07-21 定稿，待实现）

决策：

```text
Admin 删除由人工阈值生成的 healthy/degraded/unhealthy/no_data 主观分桶，不再用一个“健康”徽章覆盖
凭据、实时 breaker、主动检测和历史表现等不同事实。

1. 删除 admin_backend.channel_health_thresholds、health/health_bucket API 字段、筛选、卡片、徽章和前端
   healthBucketOf 等重复计算。
2. Channel、Endpoint、Provider、Model、Route 与 Dashboard 并列展示 credential 状态、主动检测、Channel/
   Endpoint breaker、eligible 错误率和样本数、流式 TTFT、流式/非流式总耗时、容量/temporary over-limit、
   runtime 同步状态、最终权重和实际分流。
3. 允许展示客观的“当前可服务/不可服务/基础设施故障”，但必须由当前硬门禁和 Store readiness 直接推导，
   不能再配置主观红黄绿阈值，也不能把 24 小时历史表现冒充当前状态。
4. Redis/BreakerStore 故障必须明确显示“基础设施故障，准入已拒绝”，与无样本严格区分；stale revision
   不展示旧 open、TTFT 或权重。
```

原因：

```text
主动检测刚成功时页面可能显示“健康”，但真实客户流量已经让 breaker open；一个主观标签会掩盖互相矛盾的
事实。把各项客观输入并列展示，运营才能知道问题是凭据、公共 Endpoint、单 Channel、容量还是同步故障。
```

影响：

```text
1. 删除设置定义、API DTO/查询、前端类型、列、筛选和本地算法；Provider 只聚合历史，不生成 Provider breaker。
2. Admin component、Playwright 与 API contract 测试覆盖删除字段、客观服务状态和 Store 故障展示。
3. 历史数据仍保留成功率、错误、TTFT 和总耗时，不因删除标签而删除可审计事实。
```

## DEC-050 当前开发库使用停机空库重建，不支持新旧版本混跑

状态：accepted（本项决议已于 2026-07-21 定稿，待实现；只适用于当前可重建开发环境）

决策：

```text
当前环境按可清理、可重建的开发库处理。P4 发布使用维护窗口停机、导出配置、空库 migration 和整体启动，
不为当前开发数据设计在线兼容迁移。

1. 发布前导出 Provider、Channel、Route、Model、Price 等脱敏配置，并生成按规范化 BaseURL 分组的 Endpoint
   导入清单。
2. 停止 Gateway、Admin、Worker 和相关写入，清理开发业务库，从空库执行新 migrations；先导入 Provider，
   再导入去重 Endpoint，最后绑定 Channel。清理与新 ID 相关的旧 sticky、限流、并发和 breaker Redis 状态。
3. 启动 Redis/PostgreSQL，再整体启动 Gateway/Worker/Admin；确认 control/recovery、pending operation、
   readiness 和真实协议 smoke 正常后才开放流量。
4. P4 新增 Endpoint、删除 channels.base_url 并替换 breaker/准入契约，新旧 Gateway/Admin 不得滚动混跑；
   回滚同样停止全部进程，以旧 migration 空库重建并从发布前配置快照恢复。
5. 任何未来包含必须保留的真实客户、余额、request、usage 或 ledger 历史的环境不得照搬此清库方案；上线前
   必须另行决议并实现保留历史的前向迁移、备份与回滚策略。
```

原因：

```text
当前数据库可重建时，停机空库迁移比同时维护两套 schema、运行态和兼容分支更直接、更容易验证；新旧版本
混跑会让 BaseURL、Endpoint、breaker 和 admission permit 的事实互相矛盾。
```

影响：

```text
1. 发布必须安排维护窗口并准备可重复的脱敏导出/导入脚本和真实 smoke 清单。
2. 当前不提供在线 down migration 或旧新双写；request/ledger 历史若需要保留，必须在清库前独立备份。
3. 发布验收必须证明空库 migration、配置恢复、Redis 清理、control recovery 和整体回滚演练可重复执行。
```

## DEC-051 P4 不支持 Redis Cluster，只支持同一逻辑主节点部署

状态：accepted（本项决议已于 2026-07-21 定稿，待实现）

决策：

```text
P4 的多 key 原子 Lua 本期不支持 Redis Cluster。支持单 Redis、主从/Sentinel，或语义等价的托管高可用
Redis，但所有相关 key 必须位于同一逻辑主节点并由一次 Lua 原子处理。

1. Gateway 启动和 readiness 检查必须验证部署模式与必需 Lua；检测到 Redis Cluster、CROSSSLOT 或无法证明
   多 key 原子性时拒绝就绪，不能带风险启动。
2. 运行中出现 CROSSSLOT、脚本、协议或拓扑错误时按 DEC-040 fail-closed：停止整次 fallback、零上游调用并
   返回安全 503，直到 Store 健康和 control 对账完成。
3. 禁止为了兼容 Cluster 把 breaker、permit、concurrency、RPM/RPD/TPM 或 revision control 拆成多步命令；
   这样会破坏 DEC-041 要求的全有或全无。
4. 未来若生产必须使用 Redis Cluster，必须在上线前单独设计统一 hash slot/key tag、容量热点、迁移和故障
   恢复方案，完成原子性与压测后再修订本决议。
```

原因：

```text
当前统一 AcquireAttempt 需要同时读取和修改多个 scope key。Redis Cluster 的跨 slot Lua 会直接 CROSSSLOT；
把它拆成多步会重新引入部分取得、补偿失败和多 Gateway 不一致，本期不接受该正确性退化。
```

影响：

```text
1. 部署文档、启动探针、集成测试和发布门禁必须明确支持的 Redis 形态。
2. 故障注入覆盖 Cluster/CROSSSLOT，证明 Gateway readiness=false、客户安全 503 且零上游调用。
3. 高可用依靠主从/Sentinel 或等价托管服务，不以牺牲 Lua 原子性换取 Cluster 支持。
```

## DEC-052 Provider 列表直接展示 Endpoint，并提供行级创建入口

状态：accepted（本项决议已于 2026-07-23 定稿并实现）

决策：

```text
Provider 运维列表必须直接展示所属 Endpoint，且可从 Provider 行级操作快速创建 Endpoint。

1. 列表增加端点列，列名显示为“端点”，展示 Endpoint 名称、规范化 BaseURL 与业务状态；零 Endpoint
   明示为空，多个 Endpoint 先紧凑展示前两项，其余项可在当前列查看。
2. 非归档 Provider 的“更多”菜单增加“新建端点”，复用 Provider 详情页同一表单和校验；归档
   Provider 不提供创建入口，与后端业务约束一致。
3. Provider Ops 列表 API 在分页主查询中一次聚合 Endpoint 的 id/name/base_url/status，禁止前端按每个
   Provider 单独请求 Endpoint；创建成功后同时刷新 Provider 列表、Endpoint 与 Channel 相关查询。
4. 本列只展示 PostgreSQL 业务事实，不为列表逐 Endpoint 读取 Redis breaker/control。Endpoint breaker、
   revision 与运行态同步事实继续在 Provider 详情和实时路由页面展示，避免引入 Redis N+1 和热 key 轮询。
5. Admin 面向用户的可见文案统一使用“端点”，不暴露 `Endpoint` 或 `ProviderEndpoint` 技术类型名；代码、
   API 和架构文档仍使用 `ProviderEndpoint` 作为领域实体名。
```

原因：

```text
Provider 与 Endpoint 已拆成两个业务层级后，如果主列表只显示 Provider，运营无法直接判断一个服务商是否已
配置 API Root、配置了几个故障域以及它们指向哪里；创建时还必须先进入详情页。把稳定的 Endpoint 业务事实
放回 Provider 列表并提供行级创建入口，能提高配置效率和基础可观测性，同时保持运行态读取边界清晰。
```

影响：

```text
1. GET /admin/v1/providers/ops 的每行新增非空 endpoints 数组，元素为 id/name/base_url/status。
2. Admin Provider 表格新增 Endpoint 列和行级创建入口；零/单/多 Endpoint、归档 Provider 与创建后刷新均需
   自动化覆盖。
3. 该变化不改变 ProviderEndpoint 故障域、路由、熔断、计费或公开 Gateway API 契约。
```

## DEC-053 线路与渠道 RPM/TPM/RPD 代码默认均为不限

状态：accepted（本项决议已于 2026-07-23 定稿并实现；原共享 key 由 [DEC-054](#dec-054-线路默认限流与渠道默认限流完全拆分) 替换，`0/0/0` 结论继续有效）

决策：

```text
线路默认与渠道默认限流的代码默认均为 RPM=0、TPM=0、RPD=0；0 表示该维度不限。

1. 新建空库或 app_settings 缺行时，启动 seed 分别为 `gateway.route_rate_limit_defaults` 与
   `gateway.channel_rate_limit_defaults` 写入 {"rpm":0,"tpm":0,"rpd":0}。
2. 线路/API Key 未显式配置限额时继承线路默认不限；Channel 未显式配置限额时继承渠道默认不限；显式 0
   仍表示不限，正数表示明确上限。
3. 本决议只改变默认拒绝门槛，不删除限流能力；运营需要保护某条线路、客户或 Channel 时必须显式配置正数。
4. 不限期间仍按 DEC-044 持续记录入口与 Channel 的 RPM/RPD/TPM 和并发事实，后续改为有限值时使用完整历史窗口。
5. 已存在的 app_settings 行不会因代码默认变化自动改写，两套默认必须分别通过 durable runtime-control 热更新
   发布并推进各自 revision。
```

原因：

```text
Codex 长上下文请求的单次或分钟 token 用量可能显著高于通用 API 场景。默认限流容易在尚未根据真实上游配额和
业务峰值完成容量标定前，由平台自身提前返回 429。两套默认均不限可以避免隐式门槛；具体保护值继续由线路、
API Key 和 Channel 显式承担，并保留完整用量事实支持后续定标。
```

影响：

```text
1. DefaultRateLimitDefaultsSettings 与 P4 计划中的两套默认 JSON 均为 0/0/0，相关测试同步更新。
2. 当前运行环境通过 Admin durable publisher 分别发布线路默认与渠道默认的新 revision，各自 Redis Commit 后
   在对应准入阶段立即生效。
3. 已显式配置的线路、API Key 或 Channel 限额不受影响；它们仍可能独立返回 429 或触发候选 fallback。
```

## DEC-054 线路默认限流与渠道默认限流完全拆分

状态：accepted（本项决议已于 2026-07-23 定稿并实现）

决策：

```text
废止共享 `gateway.rate_limit_defaults`，拆为两个互不继承、互不共用 revision 的关键运行态设置：

- `gateway.route_rate_limit_defaults`
- `gateway.channel_rate_limit_defaults`

1. 两套设置值形状相同，均为 `{"rpm":0,"tpm":0,"rpd":0}`；各自在 `app_settings` 中维护独立 revision，
   通过独立 Redis active/pending control、durable operation、reconciler 和 readiness 门禁发布，支持系统设置热更新。
2. 线路默认只服务 request admission。入口 Lua 将它与可信认证快照中的线路/API Key 显式 override 合并，
   对整次客户请求只计算一次 `(route,user)` RPM/RPD/TPM；RPM/RPD 在入口 Acquire 判定，TPM 在候选估算完成后
   由同一 request token 幂等 Reserve。任一维度命中后由 Gateway 直接返回 429，不创建 AttemptPermit，也不调用上游。
3. 渠道默认只服务候选阶段。`SnapshotMany` 与 `AcquireAttempt` 将它与 Channel 的 `NULL=继承/0=不限/正数上限`
   override 合并；当前渠道命中 RPM/RPD/TPM 时只拒绝该候选，路由继续尝试下一渠道。只有全部候选都不可用时，
   Gateway 才按聚合原因返回最终错误。
4. `gateway.concurrency_defaults` 不拆：`key_limit` 继续用于 request admission，`channel_limit` 继续用于候选
   AcquireAttempt；它拥有自己的共享 concurrency revision，但不能代替两套 rate revision。
5. request token 固化 `route_rate_limits_revision` 与 `global_concurrency_revision`；AttemptPermit 固化
   `channel_rate_limits_revision`、`global_concurrency_revision` 与 Channel admission revision。线路默认 revision
   不进入 AttemptPermit，渠道默认 revision 不进入 request token。
6. 已显式配置的线路/API Key/Channel 限额保持原优先级与语义，默认拆分不能覆盖业务行中的显式值；`0=不限`
   期间继续按 DEC-044 记录稳定窗口用量。
7. 任一必需 PostgreSQL 行、Redis control、payload hash、revision 或完整性 epoch 不可信时，受影响的新准入继续
   按 DEC-040 fail-closed。基础设施/同步错误返回安全 503，不能伪装成限流 429，也不能因两个 rate control 已拆分
   而恢复 fail-open。
8. Admin 系统设置必须提供两张独立配置卡；路由运行态必须同时显示线路默认与渠道默认的 PostgreSQL/Redis
   revision 和同步状态，避免运营把“入口直接 429”与“当前渠道跳过并 fallback”误认为同一类限流。
```

原因：

```text
线路默认保护客户入口，渠道默认保护具体上游资源，两者的责任主体、命中后的用户体验和调参依据不同。共享一个
默认值时，为避免渠道 429 而调高默认会同时放宽线路保护；为保护线路而调低默认又会让渠道更频繁 fallback，
无法独立运营，也会让运行态 revision 难以解释。拆分后可以分别控制入口拒绝与渠道切换，同时保留 Redis 原子准入、
热更新和 fail-closed 不变量。
```

影响：

```text
1. AppSettings 注册、migration seed、runtime_control_operations allowlist、Redis key、readiness、runtime facts、
   request token、AttemptPermit、SnapshotMany/AcquireAttempt、Admin DTO/页面与自动化测试均改为两套 rate control。
2. 旧 `gateway.rate_limit_defaults` 不再是注册设置或执行权威；部署升级必须建立两条新设置及 control，不能让旧 key
   与新 key 同时参与准入。
3. DEC-043 的 Redis 权威、durable publisher、pending fence、recovery 和 stable resource key 原则继续有效；仅其
   “入口与渠道共享一个 global rate control”的旧表述被本决议替代。
4. DEC-053 的两类默认均为 0/0/0、不限仍计数、显式限额不受影响的结论继续有效。
```

## DEC-055 Balanced 加入受限渠道成本因子并修正过期错误窗口

状态：accepted（本项决议已于 2026-07-23 定稿并实现）

决策：

```text
balanced 在容量、客观错误率和 stream-only TTFT 之外加入渠道真实成本，但成本只能在通过全部硬门禁的候选间
调整概率，不能让低价绕过 breaker、权限、revision、限流、并发或负毛利保护。

1. 成本必须使用路由期已解析并通过毛利校验的真实 ChannelCost：优先 channel_prices 绝对覆盖，否则使用
   model_prices × channel_cost_multiplier × recharge_factor。禁止按 Channel 名称、priority 或单独的倍率字段猜价格。
2. cost_ratio 是七个归一化计价分项中 max(channel_cost / customer_sale)，覆盖 uncached input、三种 cache write、
   cache read、普通 output 与 reasoning output；同分项 sale=0 且 cost=0 时比例为 0。负毛利仍在进入评分前硬摘除。
3. gateway.routing_balance 新增 cost_weight，范围 [0,1]，新环境默认 0.5。旧四字段 payload 为升级兼容形态，
   明确解释为 cost_weight=0；只有通过 durable runtime-control 发布带该字段的新 revision 后才启用成本影响，
   禁止仅靠部署新二进制无 revision 改变现有流量。
4. balanced 计算为：cost_factor = max(minimum_routing_factor,
   1 - cost_weight × clamp(cost_ratio,0,1))；final_weight = capacity_score × routing_factor × cost_factor。
   cost_weight=0 时与 DEC-047 完全一致；较低成本只提高相对抽中概率，不获得确定性首选。
5. fixed 不执行成本加权；sticky 仍在 balanced 形成初始顺序后置顶当前有效绑定。成本影响新会话首次选路和
   fallback 顺序，但不主动迁移已经成功绑定的会话，避免反复切换破坏上游 prompt cache。
6. routing trace 与线路实时运行态必须显示 cost_ratio、cost_weight、cost_factor 和最终权重，系统设置支持
   cost_weight 热更新；不得展示或记录 credential，也不得把 Channel 成本暴露给客户 API。
7. SnapshotMany 读取 closed Channel/Endpoint 时，若 eligible 错误窗口已超过当前 circuit_breaker.window_ms，
   本次评分必须把 eligible 成功/失败样本视为 0，不能让一次旧失败无限期降权；该只读归一化不清除或重置
   stream-only TTFT EWMA，也不推进 breaker 状态机。
```

原因：

```text
原公式完全不考虑平台成本：质量相近时，高成本 Channel 与低成本 Channel 的抽中概率相同。另一方面，直接按
最低价确定性选路会让低价覆盖稳定性和容量，形成故障集中与恢复饥饿。受限成本因子保留可靠性优先，同时让
质量接近的候选更倾向真实成本较低者。旧错误窗口未在只读评分时自然失效，会把已经过窗的一次失败长期保留为
100% 错误率，与“当前 eligible 错误率”语义冲突，必须同时修正。
```

影响：

```text
1. app_settings、Redis routing-balance control/SnapshotMany、Gateway scorer、Admin 设置与 DTO、trace 和测试增加
   cost_weight/cost_ratio/cost_factor；旧 payload 只作为 cost_weight=0 的升级兼容输入。
2. 新旧 Gateway 不能混跑：发布时按 DEC-050 停止旧进程、整体升级，再通过 Admin durable publisher 保存目标
   cost_weight；Redis Commit 后的新请求使用新 revision，已形成的候选计划和已有 sticky 不追溯重排。
3. 测试必须覆盖旧 payload 中性、严格新 payload、相同质量下低成本增权、低价故障候选仍受健康度压制、fixed/
   sticky 边界、绝对成本覆盖与倍率成本，以及错误窗口过期后错误样本中性但 TTFT 保留。
```
