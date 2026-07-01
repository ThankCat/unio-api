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
  5. 若候选为空：按三协议各自原生格式返回 model_capability_unavailable
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

状态：accepted

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

4. provider / channel / model 概念锚点（本项目内固定语义，防止再次混淆）：
   - model：对外售卖的产品 / 档（model_id 对外、UNIQUE）；用户可见。
   - provider：供应商身份 / 记账主体（官方品牌或中转商）；用户不可见。
   - channel：某 provider 下一条具体可路由线路（protocol + adapter_key + base_url
     + 凭据 + priority）。一个 provider 可以有多条 channel（多 key、多区域端点）；
     用户不可见。
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
  - Native 命中「上游不支持」（404/405、响应无可计费 usage、无法解析，收敛为 adapter sentinel
    ErrCompactUnsupported）时，按 compactNativeFallback（默认开启，整改 Q2）回落 SyntheticCompact
    并打 warn 日志，避免 Codex 断链；其余上游错误（鉴权/限流/超时/5xx）按正常上游错误处理。
  - 两条路径共用 runNonStream 资金关键 scaffold（routing / authorization / settlement / 终态），
    与 CreateResponse 共享同一份候选 fallback 计费循环，账务与 lifecycle 不变量一致。

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
4. Native 默认回落 Synthetic 保证 Codex 在上游临时不支持 compact 时不断链（可由运营关闭）。
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
+ 自定义 MarshalJSON）。DEC-010/DEC-014/DEC-018 边界不变；账务零改动。
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
    三协议 handler 的能力错误渲染只保留 model 分支（对客户文案不变）。
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
    闸门 metric(IncCapabilityCheck/Required/Missing)；三协议 service 的 required 构造与 enforce 渲染分支。
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

状态：accepted（2026-07-01 定稿）

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
```

原因：

```text
1. 历史 billableTPMTokens 把 cache_read 按全额计入 TPM，与 Anthropic cache-aware ITPM 相悖；
   Codex 类高缓存复用负载会瞬间打满每分钟窗口，产生「刚配置好、发两句就 429」的误拦截。
2. 历史实现只有胜出候选走 backfill 对账，失败/取消/无结算的请求与 fallback 落选渠道的 TPM 预占
   从不显式回退，只能干等 60s 滑动窗口过期——额度「泄漏」在窗口里，造成 bursty/易错负载过早 429，
   并让渠道级 TPM 负载虚高（每个尝试过的渠道都被计入，却只有胜者被对账）。
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
4. TPM 计数从「按预估膨胀、只减不还」转为「按真实用量收敛、失败即回退」，Codex 多轮不再误 429，
   限流窗口更贴近真实吞吐。
5. 计费（billing）不受影响：本改造只动 TPM 限流口径，扣费仍按完整 usage facts（含 cache_read 折扣价）。
6. E2E（真实 Codex，本地 Docker DB）验证：大上下文会话 cache_read 累计 472,576、旧口径 TPM 559,665（会超 300k 上限
   而中途 429），新口径仅 87,089、全程 0 次 429；取消场景预占 49,378 在结算前被释放归零。详见
   DESIGN-route-rate-limit.md §15.4。
```

设计文档：[DESIGN-route-rate-limit.md](DESIGN-route-rate-limit.md) §14–§15
