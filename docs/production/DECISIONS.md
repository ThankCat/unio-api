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

状态：accepted

决策：

```text
Unio Gateway 从"协议为先 + adapter 出站 Drop"（DEC-012）升级为"协议为先 + 模型能力声明 +
运行时能力闸门 + 显式 capability 错误"。落地为阶段 12 Capability Architecture。

能力模型分三层：

Layer 1 — Model Metadata（模型元数据）
  来源：models.dev daily cron + 人工补全
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

Layer 3 — Channel Capability Overrides（渠道能力收紧）
  来源：admin（阶段 13）配置
  存储：channel_capability_overrides 表 (channel_id × capability_key)
  规则：只能做减法（disable / limits 收紧），不能反向放开 Layer 2 未声明的能力
  用途：某 channel 受供应商限制的具体闸门

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
               docs/protocol/MODELS_DEV_LICENSE.md (license 摘要与 attribution)

7. 欠账登记为 GAP-12-001 ~ GAP-12-009；阶段 11 [GAP-11-010](TODO_REGISTER.md#gap-11-010)
   reasoning_effort doc/code drift 在阶段 12 TASK-12.06 中关闭。
```

