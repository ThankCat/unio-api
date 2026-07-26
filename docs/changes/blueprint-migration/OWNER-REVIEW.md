# Gateway 迁移 Owner 审核包

状态：Owner 语义决定已记录（11/11），待 Blueprint 状态与来源分类接收

来源清单基线日期：2026-07-25

代码核验日期：2026-07-26

## 使用边界

本文件汇集迁移核验中不能由迁移者自行裁决的产品、账务、安全和架构问题，供指定 Owner 直接作出
可追溯决定。来源清单基线用于固定 127 份待迁文档；“当前实现事实”则以代码核验日期的 Gateway 本地
工作树、schema 和测试为准。它不自动构成产品批准，也不要求 Owner 选择当前实现；Phase 6 切换前若
代码继续变化，必须重新核验。链接到 Blueprint 的内容是拟议或现有目标，状态以目标文档的 front matter
为准。

每个未决项必须先记录当前实现，再把可选方案写成未来目标。优化建议进入 Blueprint 的风险、路线图、
待解决问题或 `proposed` ADR；只有实现并通过相应测试后，才能升级为当前行为。推荐目标本身不得覆盖
当前代码事实。

本文件、`PLAN.md`、`MIGRATION-MAP.md` 和 `DECISION-MAP.md` 均为迁移期 `retain-temporary`
材料，不计入迁移开始前的 127 份来源基线。任何“推荐方案”都不是批准、发布、数据清除或删除 Gateway
来源文档的授权；删除仍须满足 `PLAN.md` 的全部删除门禁。

除非 Owner 填写对应的“Owner 决定”（包括拆分决定）和“决定日期”，本文件的决定状态一律是**未决定**。

## 审查项

### OR-001 API Key 完整明文留存与回显

- **建议 Owner**：安全 Owner、Gateway 产品 Owner、客户支持 Owner。
- **目标文档**：[访问控制](../../../../unio-blueprint/docs/products/gateway/features/access-control.md)、[数据生命周期](../../../../unio-blueprint/docs/products/gateway/features/data-lifecycle.md)、[架构风险](../../../../unio-blueprint/docs/architecture/risks.md)。
- **Gateway 证据**：[schema 同时保存 hash 与 plaintext](../../../migrations/000004_api_keys.up.sql#L16-L19)、[认证按 hash 查询](../../../sql/queries/api/api_keys.sql#L7-L15)、[创建服务写入 plaintext](../../../internal/core/apikey/service.go#L102-L109)、[Admin DTO 回显 plaintext](../../../internal/app/adminapi/user/api_keys.go#L250-L253)。当前已注册 HTTP 面包括创建、API Key 运维列表、请求列表/详情、更新与吊销响应；底层虽有 Get Service，但没有独立的 `GET /api-keys/{id}` 路由。
- **可选方案**：A. 保留可重复回显的密文或明文；B. 加密保存并限定具备理由的恢复读取；C. 只保存 hash/prefix，创建和轮换时一次展示。
- **推荐方案与理由**：2026-07-26 已确认当前行为为 A：保存 `key_plaintext`，Admin 的 API Key 运维列表、请求列表/详情、更新与吊销响应可重复回显完整值；这与 schema、创建路径和已注册路由一致。认证继续按 hash 进行。该确认记录现状，不把更安全的目标态伪装为已实现。
- **兼容/迁移风险**：完整密钥留存在数据库、备份和管理面会扩大暴露面；单一静态 Admin token 也不能区分实际操作人。细粒度权限、脱敏、带理由的显式 reveal、一次展示和 hash/prefix-only 均是独立优化项；在获批并实施前不得改变当前行为描述或暗示已清除历史数据。
- **验收门禁**：当前事实的核验覆盖创建后保存、已注册 Admin 响应的重复回显和 hash 认证；优化项若立项，另建改造计划并覆盖权限、读取路径、轮换、审计、历史数据处置和回滚。
- **Owner 决定**：确认方案 A 为当前行为；脱敏/显式 reveal 仅列为优化。
- **决定日期**：2026-07-26。

### OR-002 是否重新引入 Project

- **建议 Owner**：Gateway 产品 Owner、数据 Owner、账务 Owner。
- **目标文档**：[访问控制](../../../../unio-blueprint/docs/products/gateway/features/access-control.md)、[Gateway 词汇表](../../../../unio-blueprint/docs/products/gateway/glossary.md)、[计费与结算](../../../../unio-blueprint/docs/products/gateway/features/billing-settlement.md)。
- **Gateway 证据**：[迁移明确折叠 Project，并使 API Key 直接归属 User 和必绑 Route](../../../migrations/000038_user_model_policies.up.sql#L1-L10)、[请求记录迁移说明](../../../migrations/000011_request_records.up.sql#L99-L105)、[账本归属字段](../../../migrations/000021_ledger_entries.up.sql#L12-L15)。
- **可选方案**：A. 维持 `User Account -> API Key -> Route`；B. 仅新增不承载余额、认证、默认 Route 或配额的分组；C. 重建 Project 为完整租户、账务和授权边界。
- **推荐方案与理由**：2026-07-26 已确认选 A，不重新引入 Project。当前 `User Account -> API Key -> Route` 同时是已确认的领域边界和代码事实；若未来出现管理分组需求，再以独立决定评估 B 或 C。
- **兼容/迁移风险**：不能从已折叠数据臆造历史 Project；C 会影响 API Key、Route、请求、账本、配额、权限、报告与迁移回滚，必须有独立产品和账务 ADR。
- **验收门禁**：A 的验收是 Blueprint 和公开/管理契约不声称 Project 存在；B 必须证明分组不改变任何授权或计费边界；C 必须先批准领域模型、数据迁移、账本归属、默认 Route、权限和客户迁移计划。
- **Owner 决定**：不引入 Project，维持 `User Account -> API Key -> Route`。
- **决定日期**：2026-07-26。

### OR-003 DEC-006 超额结算与平台核销

- **建议 Owner**：账务 Owner、财务 Owner、Gateway 产品 Owner。
- **目标文档**：[计费与结算](../../../../unio-blueprint/docs/products/gateway/features/billing-settlement.md)、[ADR-0003 账务与结算](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0003-billing-settlement.md)。
- **Gateway 证据**：[DEC-006 的来源映射与冲突登记](DECISION-MAP.md#4-决策逐条映射55)、[历史审计中的旧封顶/核销口径](../../production/GATEWAY_LIFECYCLE_AUDIT.md#L179-L189)、[现有预授权约束](../../../migrations/000022_ledger_reservations.up.sql#L42-L52)、[结算将 overage 计入 `spent_total`](../../../internal/service/gateway/lifecycle/settlement.go#L789-L800)、[`spend_limit` 的软上限实现](../../../internal/core/auth/apikey.go#L119-L127)。
- **可选方案**：A. 客户实扣永远不超过预授权，全部差额核销；B. 先 capture 预授权，再从当时剩余可用余额独立补扣，余额不足部分核销；C. 预授权不足即拒绝交付或改为事后欠费。
- **推荐方案与理由**：2026-07-26 已确认当前行为为 B：先 capture 预授权，再以独立、幂等 overage debit 从剩余可用余额补扣，余额不足的残差 write-off。该行为取代 DEC-006 的“客户实扣不超过原授权”子句，且不扩大 reservation capture。
- **兼容/迁移风险**：这会改变客户实扣、`spent_total`、账单解释、重放和恢复语义。当前 `spend_limit` 在结算后累加、认证时检查，因此是允许近上限并发轻微超额的软上限；不得称为预授权式硬上限。
- **验收门禁**：当前行为须有 capture、overage debit、write-off 残差、幂等重放、partial/recovery 与 `spent_total` 累加测试；补齐软上限并发超额和 overage 计入 `spent_total` 的测试，作为质量缺口，不改变已经确认的当前口径。
- **Owner 决定**：确认当前 overage debit 取代 DEC-006 的原授权封顶子句；`spend_limit` 为软上限，并补齐相关测试。
- **决定日期**：2026-07-26。

### OR-004 孤儿预授权恢复

- **建议 Owner**：账务 Owner、运行 Owner、SRE Owner。
- **目标文档**：[计费与结算](../../../../unio-blueprint/docs/products/gateway/features/billing-settlement.md)、[运行控制与恢复](../../../../unio-blueprint/docs/products/gateway/features/runtime-control-recovery.md)、[Gateway 质量](../../../../unio-blueprint/docs/products/gateway/quality.md)。
- **Gateway 证据**：[孤儿预授权问题与建议](../../production/GATEWAY_LIFECYCLE_AUDIT.md#L542-L560)、[孤儿扫描查询及与 recovery worker 的互补约束](../../../sql/queries/worker/ledger_reservations.sql#L1-L10)、[当前 sweeper](../../../internal/app/workers/orphan_reservation_sweeper_worker.go#L14-L91)、[预授权索引](../../../migrations/000022_ledger_reservations.up.sql#L74-L76)。
- **可选方案**：A. 不做主动恢复，仅人工处理；B. 仅按超时释放无 recovery job 的 `authorized` reservation；C. 在 dispatch 前持久化执行意图或 attempt correlation，区分未执行、已执行和执行结果未知，并以 sweeper 作兜底核验与收口。
- **推荐方案与理由**：2026-07-26 已确认按当前 sweeper 迁移：扫描超时、仍 `authorized`、请求仍 running 且无 settlement recovery job 的 reservation，在事务中释放冻结、记录 risk exposure 并将请求收口为 failed。它与 recovery worker 互补，是当前行为而非对上游未 dispatch 的证明。
- **兼容/迁移风险**：阈值过短仍可能误判长请求，过长会持续冻结客户余额；“无 recovery job”也不等于可证明未 dispatch。持久执行意图、未执行/已执行/结果未知状态模型和未知隔离是优化项，不能作为描述或验收当前 sweeper 的前置条件。
- **验收门禁**：当前行为须覆盖扫描条件、与 recovery job 互斥、单事务 release/risk exposure/request failed、幂等重放及单条失败后的后续扫描。若优化项立项，再单独验证执行意图、未知隔离和相应故障注入。
- **Owner 决定**：确认按当前 sweeper 行为迁移；持久执行意图与 unknown 隔离仅列为优化。
- **决定日期**：2026-07-26。

### OR-005 Anthropic `web_search` / `web_fetch` 按次计量

- **建议 Owner**：协议 Owner、账务 Owner、财务 Owner。
- **目标文档**：[Provider 映射契约](../../../../unio-blueprint/docs/products/gateway/features/provider-mapping-contracts.md)、[计费与结算](../../../../unio-blueprint/docs/products/gateway/features/billing-settlement.md)、[协议兼容](../../../../unio-blueprint/docs/products/gateway/features/protocol-compatibility.md)。
- **Gateway 证据**：[公开 usage 字段](../../../internal/app/gatewayapi/anthropic/messages/response.go#L47-L52)、[adapter 将两种调用次数写为 metered item](../../../internal/core/adapter/anthropic/messages/usage.go#L97-L110)、[正数 line item 持久化](../../../migrations/000036_usage_line_items.up.sql#L9-L27)、[当前 billing 只计算 token](../../../internal/core/billing/service.go#L13-L31)、[recovery 列缺少状态且默认零](../../../migrations/000034_settlement_recovery_jobs.up.sql#L55-L56)、[partial stream 不携带工具 usage](../../../internal/service/gateway/lifecycle/partial_stream.go#L72-L116)。
- **当前实现事实**：当整组 usage facts 合法时，完整 Anthropic usage 中的正数 `web_search`/`web_fetch` 次数会形成受控 line item，并随正常结算或 recovery 保存。当前 `token_v1` 客户扣费和平台成本公式不消费这些 line item，因此不会产生独立按次收费。显式零会生成 quantity 为零的 item，但通用校验要求 quantity 大于零，可能使整组 facts 校验失败；recovery 也不能区分缺失、零与不适用，partial stream 则不携带这些次数。
- **可选目标**：A. 维持当前“只保存可观察正数、不参与客户计费”；B. 作为两个独立、按次的可计价 usage 项；C. 在计量闭环完成前拒绝会产生这些服务端工具费用的能力。不得把次数折算为 token 或任意固定请求费。
- **推荐方案与理由**：目标 B 已获确认，但当前迁移仍必须记录上述未收费事实。上游返回的是明确调用次数而非 token；每项应保存独立 metric identity、`known`/`not_applicable`/`unknown` 数量状态、按次售价与成本快照和公式版本，不以任意 token 比率或默认零掩盖差异。
- **兼容/迁移风险**：某些 Provider/流事件可能不回传或只回传增量；当前 recovery 默认零、partial stream 缺失工具 usage，无法区分缺失、已知零与不适用。缺少价格配置、重复事件合并和权威 usage 补达规则会导致漏计或重复计；不得把“adapter 能解析/line item 已保存”表述为“所有线路已收费”。
- **验收门禁**：迁移基线继续固定“合法正数可保存、显式零可能使 facts 失败、当前金额只按 token、partial/recovery 无三态”。实施 B 必须覆盖非流式、流式、partial、recovery、三态、独立按次售价/成本快照、授权估算、公式版本、权威 usage 后到和防双计；unknown 的客户收费、平台成本敞口与历史数据迁移须在 Gateway 改造计划中明确并由账务、协议与财务 Owner 验收。
- **Owner 决定**：选择 B。`web_search` 与 `web_fetch` 作为两个独立按次计价项；该决定批准目标语义，不表示当前代码已经收费或允许跳过上述实施门禁。
- **决定日期**：2026-07-26。

### OR-006 Responses 原生直传与 Chat bridge

- **建议 Owner**：协议 Owner、Gateway 产品 Owner、账务 Owner。
- **目标文档**：[公开 API 契约](../../../../unio-blueprint/docs/products/gateway/features/public-api-contracts.md)、[协议兼容](../../../../unio-blueprint/docs/products/gateway/features/protocol-compatibility.md)、[Provider 映射契约](../../../../unio-blueprint/docs/products/gateway/features/provider-mapping-contracts.md)、[ADR-0006 协议 Adapter 边界](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0006-protocol-adapter-boundary.md)。
- **Gateway 证据**：[当前 registry 的 OpenAI/DeepSeek 能力注册](../../../internal/bootstrap/adapters.go#L39-L58)、[按候选 `AdapterKey` 选择原生 `/responses` 直传或 Chat bridge](../../../internal/service/gateway/openai/responses/create_response.go#L95-L136)、[Responses 专属 multi-agent 在 bridge 显式拒绝](../../../internal/service/gateway/openai/responses/create_response.go#L70-L80)、[bridge 的 reasoning 关闭映射](../../../internal/service/gateway/openai/responses/responses_chat_map.go#L75-L80)、[bridge 流式首事件判断](../../../internal/service/gateway/openai/responses/direct_response.go#L170-L180)、[DeepSeek OpenAI Drop 仅写应用日志](../../../internal/core/adapter/openai/deepseek/chatcompletions/adapter.go#L59-L73)、[DeepSeek Anthropic Drop 仅写应用日志](../../../internal/core/adapter/anthropic/deepseek/messages/adapter.go#L63-L75)。
- **当前实现事实**：候选的 `AdapterKey` 注册 Responses adapter 时走原生 `/responses` 直传，否则要求同 key 的 Chat adapter 并执行 Responses-to-Chat bridge；两条路径复用同一请求、attempt、授权和结算 runner。bridge 当前会忽略或 Drop 无承载字段与工具，`multi_agent` 在 bridge 候选上显式拒绝，并只合成 Chat 可表达的最小 SSE 事件族。mapper 虽生成 `DroppedFields`，五个生产调用点却丢弃 translation 返回值，因而连对应应用日志都没有。Provider Adapter 的 Drop 是另一层：DeepSeek OpenAI 仅写 `DEBUG` 字段名日志，DeepSeek Anthropic 仅写 `WARN` 字段名日志，均未进入 response facts 或持久审计表。
- **可选目标**：A. 所有 Responses 只 bridge 到 Chat；B. 上游原生 Responses 直传，chat-only 候选 bridge；C. 只允许原生 Responses Provider。
- **推荐方案与理由**：目标 B 已获确认。它与当前分流方向一致，为具备原生端点的候选保留协议保真度，同时让 chat-only Provider 有受控兼容路径；路径选择获批仍不等于完整 bridge 兼容范围或当前实现质量已经验收。
- **兼容/迁移风险**：bridge 无法承载 stateful、background、server tool、multi-agent 或其他 Responses 专属语义时，当前既有显式拒绝，也有 Drop/忽略；不能笼统宣称全部稳定拒绝。SSE 事件、final usage、tool arguments、Drop 观测和 compact 边界均可能在两条路径不一致；应用日志不能替代可关联、可授权查询的持久审计。
- **验收门禁**：迁移基线继续固定按 registry 能力分流、直传与 bridge 的现有 Drop/Reject/SSE/usage 行为，并登记 bridge 诊断未消费、Provider Drop 仅有应用日志。实施 B 必须形成字段/工具/错误/SSE 的 Pass/Adapt/Drop/Reject 支持矩阵、必要直传改写、客户可观察降级信号、可关联的脱敏持久审计、完整 wire/SDK/SSE/中断恢复验收，以及两路径一致的计费、取消、retry 和错误分类。验收前目标文档保持 draft/proposed。
- **Owner 决定**：选择 B。注册原生 Responses 能力的 Adapter 走直传，只有 Chat 能力的 Adapter 保留 Responses-to-Chat bridge；该决定批准路径选择，不表示 bridge 已完整兼容或上述实施门禁已经通过。
- **决定日期**：2026-07-26。

### OR-007 `adapter_seed` 是否作为能力声明来源

- **建议 Owner**：模型/能力 Owner、协议 Owner、Admin Owner。
- **目标文档**：[模型能力目录](../../../../unio-blueprint/docs/products/gateway/features/model-capabilities-catalog.md)、[Provider 映射契约](../../../../unio-blueprint/docs/products/gateway/features/provider-mapping-contracts.md)、[ADR-0004 模型能力](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0004-model-capabilities.md)。
- **Gateway 证据**：[Admin 物化会以 `adapter_seed` 覆盖同 key 声明](../../../internal/service/admin/capability/seed.go#L28-L32)、[物化入口](../../../internal/service/admin/capability/seed.go#L70-L95)、[声明 schema 无来源字段](../../../migrations/000024_model_capabilities.up.sql#L2-L18)、[模型目录暴露 cap-tags](../../../sql/queries/api/models.sql#L10-L14)、[运行时由 AdapterRegistry 做能力过滤](../../../internal/service/gateway/lifecycle/adapter_registry.go#L124-L141)、[DeepSeek Anthropic adapter 画像声明](../../../internal/core/adapter/anthropic/deepseek/messages/capability_profile.go#L9-L50)。
- **当前实现事实**：Admin 当前提供 Adapter 画像物化入口，并把画像逐项 upsert 到 `model_capabilities`；同一模型、同一 key 的既有值会被覆盖。Schema 没有可持久区分 `adapter_seed` 与人工声明的来源字段，因此 `adapter_seed` 只是入口/代码路径名称。能力声明可用于 `/v1/models` 的 cap-tags 和预检筛选，但不参与 ingress 请求拒绝、真实候选 Adapter 准入/路由或计费。
- **可选目标**：A. 保留可覆盖人工声明的 `adapter_seed`；B. Adapter 画像仅生成只读建议，人工确认后才写入声明；C. 移除物化入口，只保留 Adapter 代码能力测试。
- **推荐方案与理由**：推荐并已确认目标 B：只读建议不得直接写入 `model_capabilities` 或继续成为可覆盖声明的入口。Adapter 画像描述当前代码路径，不足以单独证明某个模型、渠道或商业线路的可用承诺。
- **兼容/迁移风险**：A 会在不易察觉时覆盖运营声明；C 增加维护成本且可能失去可审计的初始建议。无论选项如何，能力目录不得重新成为 ingress runtime gate，除非另有决定。
- **验收门禁**：迁移基线先固定“当前入口可覆盖、schema 无来源、声明只用于目录/预检而非运行时闸门”。目标 B 必须验证画像/差异只读、人工确认通过既有声明工作流写入、未经确认不得覆盖、权限与操作审计明确，并盘点无法按来源区分的存量行及其回滚/兼容语义；同时核对画像与真实 Adapter Drop/Adapt 的差异。
- **Owner 决定**：选择 B。Adapter 画像只生成只读建议或差异，人工确认后才写入模型能力声明；该决定批准长期治理语义，不表示当前物化入口已退役、现有数据已重分类或改造门禁已经通过。
- **决定日期**：2026-07-26。

### OR-008 `protocol_scope` 当前行为

- **建议 Owner**：模型/能力 Owner、协议 Owner、Admin Owner。
- **目标文档**：[模型能力目录](../../../../unio-blueprint/docs/products/gateway/features/model-capabilities-catalog.md)、[协议兼容](../../../../unio-blueprint/docs/products/gateway/features/protocol-compatibility.md)、[ADR-0004 模型能力](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0004-model-capabilities.md)。
- **Gateway 证据**：[字典定义与归一规则](../../../internal/core/capability/keys.go#L30-L65)、[Admin 字典写入先归一 scope](../../../internal/service/admin/capability/dictionary.go#L92-L147)、[Admin 只注册模型能力列表与批量 PUT](../../../internal/app/adminapi/capability/register.go#L12-L25)、[模型能力写入没有协议入参](../../../internal/service/admin/capability/capability.go#L95-L210)、[目录只注册采纳入口](../../../internal/app/adminapi/model/register.go#L43-L50)、[模型能力 SQL 只按模型与 key 写入](../../../sql/queries/admin/model.sql#L15-L36)、[DeepSeek Anthropic 画像含 `openai` scope key](../../../internal/core/adapter/anthropic/deepseek/messages/capability_profile.go#L9-L50)、[`/v1/models` 不按 scope 或 Channel 协议过滤](../../../sql/queries/api/models.sql#L10-L42)。
- **当前实现事实**：33-key 字典为每个 key 保存 `shared`、`openai` 或 `anthropic`。数据库约束最终枚举值；Admin 写入前把 `both` 归一为 `shared`，空白或未知原始值也会归一为 `shared`。`model_capabilities` 只有 `(model_id, capability_key)`，没有协议列；当前 Admin HTTP 只暴露列表与批量整表覆盖，没有注册 per-key PUT/DELETE。目录采纳和 Adapter 画像物化均可写入声明且不执行跨 scope 校验；代码中的目录刷新服务同样不检查 scope，但当前没有注册刷新路由。同一模型可关联不同协议的 Channel。DeepSeek Anthropic 画像中的 `reasoning.effort`、`service_tier`、`tools.builtin.web_search` 和 `tools.builtin.mcp` 当前均为 `openai` scope key，且可被物化。OpenAI-compatible `/v1/models` 以 Route 内任意协议 Channel 的绑定判断模型可见性，聚合所有非 `unsupported` 声明，不读取或过滤 `protocol_scope`；真实请求候选查询才按 ingress protocol 过滤 Channel，并使用代码级 Adapter registry。能力声明仍不参与请求拒绝、真实候选路由或计费。
- **迁移结论**：以当前代码为唯一事实来源。`protocol_scope` 当前是能力 key 字典中的协议语境分类，不是模型能力写入约束；当前不存在跨 scope 拒绝、override 理由或例外审计行为。迁移文档不把这些未实现机制写成现状或批准目标。
- **Owner 决定**：按当前代码事实迁移，只记录上述已实现行为。
- **决定日期**：2026-07-26。

### OR-009 TTFT 归属与 ADR-0001 / ADR-0009 的取代关系

- **建议 Owner**：架构 Owner、Gateway 运行 Owner、协议 Owner。
- **目标文档**：[ADR-0001 统一领域术语](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0001-domain-terminology.md)、[ADR-0009 客观 Balanced 路由](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0009-objective-balanced-routing.md)、[路由与负载均衡](../../../../unio-blueprint/docs/products/gateway/features/routing-load-balancing.md)。
- **Gateway 证据**：[协议无关 timing observer](../../../internal/service/gateway/lifecycle/attempt_timing.go#L15-L72)、[非流式不产生 FirstToken 的测试](../../../internal/service/gateway/lifecycle/attempt_timing_test.go#L13-L47)、[首事件记录与客户写帧相互独立](../../../internal/service/gateway/lifecycle/attempt_runner_stream.go#L464-L510)、[只有 stream permit 可提交 FirstToken](../../../internal/platform/breakerstore/store.go#L437-L456)、[Redis 只更新 Channel TTFT](../../../internal/platform/breakerstore/lua.go#L721-L730)、[候选快照读取 Channel TTFT EWMA](../../../internal/service/gateway/lifecycle/candidates.go#L230-L247)、[无样本中性评分](../../../internal/service/gateway/lifecycle/balance.go#L192-L213)。
- **当前实现事实**：用于路由的 TTFT 从 Adapter 紧邻 `http.Client.Do` 前开始，到协议层标记为 `FirstTokenEligible` 的第一个应用层流事件为止；它与 `SuppressEmit`、客户 SSE write-ack 和请求记录的 `response_started_at` 相互独立。非流式 observer 不产生该样本。首事件到达时只记录 attempt timing；Adapter 返回后，流式 permit 的 `Finish` 才在围栏、代际、outcome 和运行控制校验通过时更新 Channel 的 `ttft_ewma_ms/ttft_samples`。Origin 状态不写这两个字段。候选排序只读取 Channel 快照；无样本时延迟惩罚为零，合法的 0ms 样本则以 samples 大于零与无样本区分。流式与非流式请求的候选评分共用该 Channel EWMA。
- **口径边界**：请求列表和 Dashboard 的 `ttft_ms` 是流式请求从 `request_records.started_at` 到首个客户写帧 `response_started_at` 的请求级事实；attempt 详情的 `upstream_ttft_ms` 是 `upstream_started_at` 到 `upstream_first_token_at`；Channel 路由 EWMA 由后者在 permit `Finish` 时形成。Provider Origin runtime DTO 当前复用通用 breaker DTO 并暴露 TTFT 字段，但生产写入链路不为 Origin 生成 TTFT，不能据此把 Origin 解释为该 EWMA 的所有者。
- **迁移结论**：以当前代码为唯一事实来源。修正 ADR-0001 中把流式 TTFT 归 Provider Origin 的旧表述；Provider Origin 保留 base URL、围栏和公共 breaker 故障域语义，Channel 保存当前路由使用的 stream-only TTFT EWMA。本次只校正现状，不批准未来双指标、Origin 聚合或其他 TTFT 架构，也不建立新的 ADR 取代关系。
- **Owner 决定**：按当前代码事实迁移，只记录上述已实现行为和现有展示差异。
- **决定日期**：2026-07-26。

### OR-010 容量快照排序与 `AttemptPermit` 原子准入的时序

- **建议 Owner**：Gateway 运行 Owner、架构 Owner、SRE Owner。
- **目标文档**：[路由与负载均衡](../../../../unio-blueprint/docs/products/gateway/features/routing-load-balancing.md)、[准入控制](../../../../unio-blueprint/docs/products/gateway/features/admission-control.md)、[ADR-0007 原子准入](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0007-atomic-admission-control.md)、[ADR-0009 客观 Balanced 路由](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0009-objective-balanced-routing.md)。
- **Gateway 证据**：[公开 `/v1` 路由的认证与 request admission 接线](../../../internal/app/gatewayapi/router.go#L96-L131)、[生成请求的候选、TPM Reserve 与账务授权顺序](../../../internal/service/gateway/openai/chatcompletions/chat_completion.go#L46-L101)、[一次 SnapshotMany 同时用于资格过滤和评分](../../../internal/service/gateway/lifecycle/candidates.go#L175-L245)、[快照整批 revision 门禁](../../../internal/platform/breakerstore/snapshot.go#L315-L345)、[每个真实调用前单独 Acquire](../../../internal/service/gateway/lifecycle/attempt_permit.go#L129-L213)、[首候选容量拒绝时的单次短等](../../../internal/service/gateway/lifecycle/head_wait.go#L17-L46)。
- **当前请求时序**：所有当前注册且受保护的公开 `/v1` 端点都在 API Key 认证后取得一次 `(Route, User Account)` request-admission token。生成请求随后形成 Route 与候选计划，执行一次 `SnapshotMany` 并完成候选输入 token 估算，再由同一 request session 一次性 Reserve 请求层 TPM，之后才做账务授权。候选 fallback 不重新取得入口 token。
- **快照与排序事实**：`SnapshotMany` 是只读线性化点，用于运行态资格，并提供容量/质量事实和 balance 参数；成本评分使用候选计划中已经冻结的 `CostRatio`。该步骤完成评分和排序，但不创建 permit，也不预占 Channel 并发、RPM、RPD 或 TPM。快照若判定 runtime-sync/pending/stale revision/config，则整批失败；Origin disabled、cooldown、permission pause、breaker open/half-open busy 等是候选级排除。容量为零不会统一摘除：当此次参与评分的全部候选容量分为零时，普通 closed 候选按压力稳定排序，half-open 候选另行保序追加；其他场景的零权重普通候选保留在 fallback 尾部。最终 sticky 置顶仍可移动一个继续合格的 half-open 绑定候选。
- **逐 transport 准入事实**：非流式、流式和透明 fallback 都在每次真实 transport 前为当前候选取得新的 `AttemptPermit`。每次 Acquire 新建 permit ID，并强读 integrity、admission facts 中的 ChannelRate/GlobalConcurrency revision 及 routing facts 中的 CircuitBreaker revision；RouteRate revision 已绑定 request token，RoutingBalance revision 只用于 `SnapshotMany`，ChannelAdmission revision 与 Origin/Channel config revision 来自本请求冻结的候选计划。业务拒绝发生在 attempt 创建和上游 transport 前，因此该候选零 attempt、零新增上游调用、零候选级资源变化；执行器继续后续候选。Store 调用错误或 `breaker_store_unavailable` 会终止执行；其他 denied reason 当前按候选跳过，最终仅纯 `rate_limited`/`concurrency_limited` 聚合为 Channel 429，混合或其他拒绝聚合为 `no_available_channel`。
- **队首短等事实**：整次请求最多短等一次，且只在首个 Route 候选返回 `concurrency_limited` 或 `rate_limited` 时触发；`rate_limited` 同时可能来自 Channel RPM/RPD/TPM 或 429 cooldown。等待计入客户请求 deadline，并继续持有请求层 token、入口并发、已 Reserve 的请求 TPM 和账务预授权冻结，只是不持有候选级资源。入口 RPM/RPD 已经计数，但不是等待期间持有的租约。醒来后使用新 permit ID，并重新读取上述 integrity、ChannelRate、GlobalConcurrency 与 CircuitBreaker control facts；breaker open、half-open busy 与其他拒绝不等待。首 Route 候选的透明 fallback 与普通调用共享同一个短等已用标记。
- **迁移结论**：以当前代码为唯一事实来源，按上述“只读快照排序 + 每个真实 transport 前逐候选原子 Acquire”迁移。不批准快照预占、全量锁定 fallback 池或“容量为零必先摘除”等未来语义；若代码以后改变，必须先有独立改造计划、实现与测试，再归档 Blueprint。
- **现有验证与观测差距**：当前单元/集成测试和调用链已经覆盖核心时序，但多 Gateway 竞态、响应丢失幂等、revision/epoch 组合变化，以及快照、Acquire、transport、Abort/Finish 在运营面的完整关联仍未形成一组端到端验收证据。这些差距不改变上述现状。
- **Owner 决定**：按当前代码事实迁移，只记录已实现的快照、排序、逐 transport 准入、拒绝与短等行为。
- **决定日期**：2026-07-26。

### OR-011 模块化单体与运行依赖的事实收口

- **建议 Owner**：架构 Owner、SRE Owner、安全 Owner、数据 Owner。
- **目标文档**：[架构部署](../../../../unio-blueprint/docs/architecture/deployment.md)、[ADR-0011 运行部署边界](../../../../unio-blueprint/docs/products/gateway/decisions/adr-0011-runtime-deployment-boundaries.md)、[运行控制与恢复](../../../../unio-blueprint/docs/products/gateway/features/runtime-control-recovery.md)、[准入控制](../../../../unio-blueprint/docs/products/gateway/features/admission-control.md)。
- **Gateway 证据**：[当前 Go module](../../../go.mod#L1)、[实际 `cmd` 入口集合](../../../cmd)、[Gateway 直接装配核心服务](../../../internal/bootstrap/gateway.go#L18-L75)、[通用 PostgreSQL 只建池与 Ping](../../../internal/platform/store/postgres.go#L11-L54)、[ledger 预授权显式事务](../../../internal/core/ledger/reservation.go#L73-L132)、[结算显式事务](../../../internal/service/gateway/lifecycle/settlement.go#L476-L521)、[运行控制发布显式事务](../../../internal/core/runtimecontrol/publisher.go#L207-L229)、[单地址 Redis client](../../../internal/platform/redis/client.go#L11-L35)、[非 Cluster 与 Redis 7+ verifier](../../../internal/platform/breakerstore/store.go#L143-L171)、[Gateway 启动恢复](../../../internal/bootstrap/gateway_server.go#L108-L193)、[Admin 对账但不做 topology verifier](../../../internal/bootstrap/admin_server.go#L145-L174)、[Worker runner verifier](../../../internal/bootstrap/worker_server.go#L98-L109)、[维护 CLI verifier](../../../cmd/runtime-state-maintenance/main.go#L63-L84)、[Gateway health/readiness HTTP](../../../internal/app/gatewayapi/router.go#L77-L94)、[readiness 强读事实](../../../internal/service/gateway/readiness/readiness.go#L56-L145)。
- **进程与事务事实**：当前只有三个常驻服务入口 `gateway-server`、`admin-server`、`worker-server`，以及一次性 `runtime-state-maintenance` CLI；`worker-server sync-models` 也是一次性路径，当前没有 `console-server`。这些入口属于同一 Go module，直接复用核心、服务、数据访问和 bootstrap 模块，不通过 RPC 复制 billing、ledger、routing 或 runtime-control。每个入口独立从 `DATABASE_URL` 建立 pool；代码复用同一 Schema 与事务实现，但不校验进程是否指向同一物理 PostgreSQL 实例。所谓事务边界是预授权、结算、ledger 和运行控制发布等单次操作各自在执行进程内以显式 PostgreSQL 事务收口，不是跨进程事务。
- **启动与依赖事实**：所有入口共用的 `OpenPostgres` 启动检查只执行配置解析、建池和 `Ping`，没有 migration runner 或 schema-version 兼容门禁；之后仍会执行依赖 Schema 的装配与业务查询，并可能因此失败。Redis 配置只有一个 `Addr` 并使用普通 `redis.NewClient`，没有 Sentinel discovery、自动 failover 或 Cluster client。Gateway、Worker 常驻 runner 和维护 CLI 会拒绝 Cluster 并要求 Redis 7+；`sync-models` 不连接 Redis。Admin 生产入口要求 Redis `Ping` 成功并执行一次及周期 control reconciliation，但未执行同一 topology/version verifier，也不执行 Gateway 的 epoch ensure、reconciliation proof 与 fault-latch clear。Admin bootstrap 的 nil Redis 只供测试或其他装配退化普通 settings 到 DB + 本地缓存；生产 `cmd/admin-server` 不走该路径。
- **健康与故障事实**：Gateway `/healthz` 静态返回 200；`/readyz` 每次强读 PostgreSQL ready epoch、五项关键 control revision，以及 state epoch、Channel admission、关键 app setting 和 Origin routing operation 的终态，再 `Ping` Redis 并原子核验 marker、五项关键 control 的 active/pending/revision/payload hash、instance proof 与 fault latch；普通探针不扫描全部候选 control、只读且不检查 schema version。readiness、request admission 与 candidate permit 都比较当前 Redis `run_id` 和完整对账 proof；实例变化保持 fail-closed。approved epoch recovery 的完整 proof 可供隔离 maintenance smoke 清 latch，但普通 `/readyz` 在 `ReleaseRecovery` 前仍为 503。request admission 的 Store/control 故障在 handler 前失败；`SnapshotMany` 的 runtime-sync/pending/stale 判定整批失败；快照后的 candidate permit 业务拒绝只跳当前候选并可 fallback；Go/Store 错误或 `breaker_store_unavailable` 才终止候选执行。当前没有本机限流或 breaker 放行降级，已开始 transport 的调用仍按真实账务与审计事实收口。
- **迁移结论**：以当前代码为唯一事实来源，只迁移上述实际入口、模块/事务、启动检查、健康与分层 fail-closed 行为。不批准服务拆分、Redis 高可用形态或 schema-version 方案；没有 schema 门禁、Admin 预检不一致、单地址 client 和缺少高可用证明只记录为当前实现缺口。
- **验收与优化差距**：迁移核验已覆盖当前入口、装配与热路径；多实例竞态、control 响应丢失、epoch/revision 组合、完整恢复演练，以及任何新增 Schema 门禁、Admin 统一预检或 Redis 高可用能力，都必须在独立改造计划中实现并测试后才能更新 Blueprint 事实。
- **Owner 决定（架构边界）**：按当前代码事实迁移；不在本次迁移中选择或批准未来服务拆分、依赖拓扑或可用性方案。
- **Owner 决定（schema-version 边界）**：记录当前没有 migration runner/schema-version 门禁；是否新增门禁仅作为未实现优化，不在本次迁移中批准。
- **决定日期**：2026-07-26。
