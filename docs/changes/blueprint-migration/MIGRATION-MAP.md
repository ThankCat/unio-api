# Gateway 文档迁移映射

状态：待审核

基线日期：2026-07-25

## 1. 基线与使用方法

本表覆盖迁移开始前 `unio-gateway/docs/` 下全部 **127** 份 Markdown 文档。表中的“动作”记录处置类型；
“迁移核验”记录迁移者是否已逐份核对，“Owner 审核”和“最终处置日期”分别记录语义批准与来源处置。
迁移者核验完成不表示 Blueprint 已批准，也不表示允许删除来源。

本目录新建的 `PLAN.md`、`MIGRATION-MAP.md`、`DECISION-MAP.md` 和 `OWNER-REVIEW.md` 属于
`retain-temporary`，不计入 127 份来源基线，切换完成后删除。`OWNER-REVIEW.md` 是迁移期的
Owner 决策包；它不替代 Blueprint ADR，也不构成对来源删除或实现改动的授权。

### 动作定义

| 动作 | 含义 |
| --- | --- |
| `merge` | 文档主体是长期概念；去重、校正后合并到一个或多个 Blueprint 权威文档。 |
| `extract` | 只抽取少量长期不变量、风险或未决事项；任务过程和实现细节不迁移。 |
| `history-only` | 仅是阶段、状态、日志或已失效计划；完成证据核验后不迁移正文。 |
| `external-reference` | 外部/官方材料不复制；必要时仅在 Blueprint 保留来源链接、版本或差异说明。 |
| `code-owned` | 法律归属、生成数据或本地开发事实应留在实现仓库的非权威位置，不迁为产品文档。 |

### Blueprint 目标代号

以下代号解析为当前 Blueprint 工作树中的实际目标。尚未形成提交或 PR，版本引用待评审批次确定：

| 代号 | 拟议权威位置 |
| --- | --- |
| `A-OV` | `docs/architecture/overview.md`、`context.md`、`glossary.md`、`principles.md` |
| `A-DEPLOY` | `docs/architecture/deployment.md` |
| `A-QUALITY` | `docs/architecture/quality.md` |
| `A-RISK` | `docs/architecture/risks.md` |
| `G-OV` | `docs/products/gateway/overview.md`、`glossary.md` |
| `G-API` | `docs/products/gateway/features/public-api-contracts.md` |
| `G-COMPAT` | `docs/products/gateway/features/protocol-compatibility.md` |
| `G-ADAPTER` | `docs/products/gateway/features/provider-adaptation.md`、`provider-mapping-contracts.md` |
| `G-LIFECYCLE` | `docs/products/gateway/features/request-lifecycle.md` |
| `G-BILLING` | `docs/products/gateway/features/billing-settlement.md` |
| `G-ROUTING` | `docs/products/gateway/features/routing-load-balancing.md` |
| `G-ADMISSION` | `docs/products/gateway/features/admission-control.md` |
| `G-RUNTIME` | `docs/products/gateway/features/runtime-control-recovery.md` |
| `G-ERROR` | `docs/products/gateway/features/error-semantics.md` |
| `G-CAP` | `docs/products/gateway/features/model-capabilities-catalog.md` |
| `G-ACCESS` | `docs/products/gateway/features/access-control.md` |
| `G-DATA` | `docs/products/gateway/features/data-lifecycle.md` |
| `G-QUALITY` | `docs/products/gateway/quality.md` |
| `G-ROADMAP` | `docs/products/gateway/roadmap.md` |
| `G-ADR` | `docs/products/gateway/decisions/adr-0002` 至 `adr-0011` 及其索引 |
| `ADMIN-OV` | `docs/products/admin/overview.md`、`glossary.md` |
| `ADMIN-OPS` | `docs/products/admin/features/operations-management.md`、`operations-observability.md` |
| `ADMIN-PAGES` | `docs/products/admin/pages/operations-dashboard.md`、`provider-origin-channel-management.md` |
| `ADMIN-QUALITY` | `docs/products/admin/quality.md` |
| `ADMIN-ROADMAP` | `docs/products/admin/roadmap.md` |
| `SPEC-API` | `docs/specifications/api-style.md` |
| `SPEC-CODE` | `docs/specifications/coding.md` |
| `SPEC-LOG` | `docs/specifications/logging.md` |
| `SPEC-PERM` | `docs/specifications/permissions.md` |
| `PLATFORM-ROADMAP` | `docs/roadmap/platform-roadmap.md` |

## 2. 顶层与架构（3）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/PROJECT_STATUS.md` | `extract` | 只迁仍成立的产品边界、开放风险和未来目标；完成度、阶段号和当前下一步不迁。 | `G-ROADMAP`, `A-RISK` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/README.md` | `history-only` | 旧文档体系索引；切换时由根 README 的 Blueprint 链接取代。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/architecture/PROJECT_STRUCTURE.md` | `extract` | 抽取稳定的服务边界、依赖方向和概念部署；源码树、包名和目标目录不迁。 | `A-OV`, `A-DEPLOY`, `SPEC-CODE` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
## 3. 阶段文档（63）

### 3.1 索引与 Phase 1-6（19）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/chapters/README.md` | `history-only` | 阶段体系索引，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-01-go-web/ACCEPTANCE.md` | `history-only` | 初始骨架验收记录，不定义长期产品行为。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-01-go-web/PLAN.md` | `history-only` | Go Web 初始实施顺序与目录细节，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-01-go-web/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-02-infrastructure/ACCEPTANCE.md` | `history-only` | 基础设施批次验收记录；由部署约束核验代替。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-02-infrastructure/PLAN.md` | `extract` | 只保留 PostgreSQL/Redis、迁移和运行依赖中的长期部署/数据约束。 | `A-DEPLOY`, `G-RUNTIME` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-02-infrastructure/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-03-identity-api-key/ACCEPTANCE.md` | `history-only` | 原始验收记录；长期身份行为从实现和计划抽取。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-03-identity-api-key/PLAN.md` | `merge` | 抽取认证与审计边界；历史 Project 归属和一次展示目标与当前实现的冲突须显式登记，不作为现行契约照搬。 | `G-ACCESS`, `SPEC-PERM`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-03-identity-api-key/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-04-openai-compatible-api/ACCEPTANCE.md` | `history-only` | 原始阶段验收记录；当前契约由后续协议文档校正。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-04-openai-compatible-api/PLAN.md` | `extract` | 抽取仍有效的公开认证、请求记录和 Chat Completions 契约；早期实现步骤不迁。 | `G-API`, `G-LIFECYCLE` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-04-openai-compatible-api/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-05-adapter-boundary/ACCEPTANCE.md` | `history-only` | 原始验收记录；以当前 adapter 边界设计为准。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-05-adapter-boundary/PLAN.md` | `merge` | adapter 纯协议职责、运行配置注入和 retry 归属。 | `G-ADAPTER`, `G-LIFECYCLE`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-05-adapter-boundary/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-06-model-channel-routing/ACCEPTANCE.md` | `history-only` | 原始验收记录；当前领域模型由后续决策覆盖。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-06-model-channel-routing/PLAN.md` | `extract` | 抽取 Model/Provider/Channel/Route 关系和线路内路由不变量，使用 Blueprint 新术语校正。 | `G-OV`, `G-ROUTING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-06-model-channel-routing/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
### 3.2 Phase 7-9（15）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/chapters/phase-07-billing-ledger/ACCEPTANCE.md` | `history-only` | 原始验收清单；长期账务规则由设计与决策迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-07-billing-ledger/BILLING_E2E_TEST_PLAN.md` | `extract` | 只提炼仍有效的账务不变量和验收场景；命令、账号、测试数据及运行顺序不迁。 | `G-BILLING`, `G-QUALITY` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-07-billing-ledger/PLAN.md` | `merge` | 余额、授权、冻结、结算、账本、幂等和恢复语义。 | `G-BILLING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-07-billing-ledger/STATUS.md` | `history-only` | 已完成阶段状态和 GAP 进度不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-07-billing-ledger/STREAM_PARTIAL_SETTLEMENT.md` | `merge` | 流式取消、无 final usage、partial settlement 和成本/风险收口。 | `G-BILLING`, `G-LIFECYCLE`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-08-observability-stability/ACCEPTANCE.md` | `history-only` | 原始验收记录，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-08-observability-stability/PLAN.md` | `extract` | 抽取日志关联、指标、健康、优雅关闭、恢复和可靠性场景。 | `G-QUALITY`, `A-QUALITY`, `SPEC-LOG` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-08-observability-stability/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/ACCEPTANCE.md` | `history-only` | 原始协议批次验收；由兼容契约和质量场景取代。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/COMPATIBILITY_MATRIX.md` | `merge` | OpenAI Chat Completions 的 Unio 支持/拒绝/忽略差异。 | `G-COMPAT`, `G-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/DEEPSEEK_UPSTREAM.md` | `merge` | DeepSeek 上游特有映射和限制，去掉实现路径。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/END_TO_END_PIPELINE.md` | `merge` | 公开请求、路由、adapter、响应、审计和结算的稳定职责边界。 | `G-LIFECYCLE`, `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/OPENAI_PROTOCOL.md` | `extract` | 只保留 Unio 偏差和兼容选择；官方字段说明不复制。 | `G-COMPAT`, `SPEC-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/PLAN.md` | `history-only` | 阶段任务拆分；长期行为由矩阵和 DEC 提取。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-09-openai-protocol-parity/STATUS.md` | `history-only` | 已完成阶段状态，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
### 3.3 Phase 10-12（18）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/chapters/phase-10-dual-protocol-gateway/ACCEPTANCE.md` | `history-only` | 原始验收清单；契约结果由矩阵迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/ANTHROPIC_MESSAGES_MATRIX.md` | `merge` | Anthropic Messages 的支持、校验、Drop 和流式行为。 | `G-COMPAT`, `G-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/ARCHITECTURE.md` | `merge` | 双公开协议边界、统一商业生命周期和 adapter 分层。 | `G-OV`, `G-ADAPTER`, `G-LIFECYCLE`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_ANTHROPIC_MAPPING.md` | `history-only` | 已迁移占位链接，不再迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md` | `history-only` | 已迁移占位链接，不再迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/OPENAI_CHAT_COMPLETIONS_MATRIX.md` | `merge` | 当前 OpenAI Chat Completions 契约矩阵；与 Phase 9 去重。 | `G-COMPAT`, `G-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/PLAN.md` | `history-only` | 任务顺序和叶子批次不迁；长期行为由架构、矩阵和 DEC 覆盖。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/RESPONSE_FACTS.md` | `merge` | 协议响应事实、usage、审计和计费解耦语义。 | `G-LIFECYCLE`, `G-BILLING`, `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/SETTLEMENT_EXTRACTION_DESIGN.md` | `extract` | 仅保留结算/恢复/授权职责和失败语义；函数抽取过程不迁。 | `G-BILLING`, `G-LIFECYCLE` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-10-dual-protocol-gateway/STATUS.md` | `history-only` | 阶段状态和实施记录不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-11-openai-responses-api/ACCEPTANCE.md` | `history-only` | 原始验收清单；可观察行为由能力矩阵迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md` | `merge` | Responses 公开支持范围、stream/compact/错误和限制。 | `G-COMPAT`, `G-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-11-openai-responses-api/PLAN.md` | `history-only` | 实施步骤、GAP 和代码位置不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md` | `merge` | native passthrough 与 Responses-to-Chat bridge 的字段/流式/usage 语义。 | `G-COMPAT`, `G-ADAPTER`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-11-openai-responses-api/STATUS.md` | `history-only` | 阶段状态、测试运行和剩余任务不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-12-capability-architecture/ACCEPTANCE.md` | `history-only` | 已被后续 DEC 部分取代的验收记录，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-12-capability-architecture/PLAN.md` | `extract` | 仅迁 DEC-024 后仍有效的能力字典和人工声明；自动学习/闸门历史不迁。 | `G-CAP`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-12-capability-architecture/STATUS.md` | `history-only` | 阶段状态不迁；以当前决策谱系为准。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
### 3.4 Phase 13-15（11）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/chapters/phase-13-admin/ACCEPTANCE.md` | `history-only` | 原始 Admin slice 验收，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-13-admin/ADMIN_MODULES_DRAFT.md` | `extract` | 只迁已被当前产品证实的运营领域和边界；未接受草案保持 proposed 或进入 roadmap。 | `ADMIN-OV`, `ADMIN-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-13-admin/CONTRACT.md` | `merge` | 稳定的 Admin 运营 API/行为契约，去掉 handler 和 SQL 细节。 | `ADMIN-OPS`, `ADMIN-PAGES`, `SPEC-PERM` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-13-admin/PLAN.md` | `history-only` | 实施任务和 slice 顺序不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-13-admin/STATUS.md` | `extract` | 只抽取当前已知产品范围、未解决风险和路线图，不迁完成日志。 | `ADMIN-OV`, `ADMIN-QUALITY`, `ADMIN-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-14-model-catalog-decoupling/ACCEPTANCE.md` | `history-only` | 原始批次验收记录，不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-14-model-catalog-decoupling/PLAN.md` | `merge` | 外部模型目录与正式售卖目录解耦、采纳和数据所有权语义。 | `G-CAP`, `ADMIN-OPS`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-14-model-catalog-decoupling/STATUS.md` | `history-only` | 阶段状态不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-15-channel-productization/ACCEPTANCE.md` | `history-only` | 原始批次验收记录；当前行为从设计和实现核验。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-15-channel-productization/PLAN.md` | `merge` | 线路商品、渠道池、fixed/balanced 策略、倍率和失败切换边界。 | `G-OV`, `G-ROUTING`, `G-BILLING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/chapters/phase-15-channel-productization/STATUS.md` | `extract` | 只抽取当前未完成风险/路线图；完成过程不迁。 | `G-ROADMAP`, `A-RISK` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
## 4. 外部数据源（3）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/datasources/MODELS_DEV_API.md` | `external-reference` | 不复制第三方 API 说明；在模型目录设计中保留官方来源链接和采纳边界。 | `G-CAP`（仅链接/差异） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/datasources/MODELS_DEV_LICENSE.md` | `code-owned` | License/Attribution 按实际数据或依赖的法律要求留在实现仓库合适位置；Blueprint 仅说明外部来源约束。 | `G-CAP`（仅约束） | `保留实现仓库；边界已核验` | `未指定` | `—` |
| `docs/datasources/README.md` | `history-only` | 旧数据源文档目录规则不迁。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
## 5. Production 文档（28）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/production/DECISIONS.md` | `merge` | DEC-001~055 按当前有效概念重组 ADR，保留来源编号、日期和取代链。 | `G-ADR`、必要的全局/Admin ADR | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-bill-on-cancel-cost-reconciliation.md` | `merge` | bill-on-cancel 的客户结算、渠道成本和取消责任边界。 | `G-BILLING`, `G-LIFECYCLE` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-capability-autocalibration.md` | `history-only` | 自动校正已由 DEC-024 取代；只在目标 ADR 来源历史中出现。 | `G-ADR`（谱系） | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-capability-evidence-v2.md` | `history-only` | 证据 v2 已由 DEC-024 取代；不迁实现方案。 | `G-ADR`（谱系） | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-capability-manual-declaration.md` | `merge` | 能力字典、人工声明和无能力闸门的当前模型。 | `G-CAP`, `ADMIN-OPS`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-channel-cost-multiplier.md` | `merge` | 渠道成本倍率、充值倍率、绝对覆盖和历史快照。 | `G-BILLING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-channel-test.md` | `merge` | 渠道检测触发、凭据状态、结果语义和安全边界。 | `ADMIN-OPS`, `G-RUNTIME`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-cost-base-from-model-price.md` | `merge` | 售价/成本共用模型基准价及历史可审计关系。 | `G-BILLING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-env-to-runtime-settings-migration.md` | `extract` | 迁运行时可配置域、权威来源、热更新和故障语义；env/代码迁移步骤不迁。 | `G-RUNTIME`, `A-DEPLOY` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-route-group-pricing.md` | `merge` | 线路作为产品档位、API Key 绑定和售价倍率语义。 | `G-OV`, `G-BILLING`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-route-rate-limit.md` | `merge` | 线路+用户准入主体、RPM/RPD/TPM/并发和结算对账语义。 | `G-ADMISSION`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-runtime-settings-batch2-domains.md` | `extract` | 迁配置域、owner、运行时行为和安全门禁；批次/函数清单不迁。 | `G-RUNTIME`, `ADMIN-OPS`, `A-QUALITY` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/DESIGN-runtime-settings.md` | `merge` | durable 设置、Redis 缓存、发布/恢复和可观测语义。 | `G-RUNTIME`, `A-DEPLOY`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/E2E-channel-cost-multiplier.md` | `extract` | 只提炼长期验收场景；真实密钥、CLI 步骤和测试记录不迁。 | `G-BILLING`, `G-QUALITY` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/ERROR_HANDLING_CATALOG.md` | `merge` | 外部错误契约、内部分类、敏感信息边界和责任归因。 | `G-ERROR`, `G-LIFECYCLE`, `SPEC-API` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/GATEWAY_LIFECYCLE_AUDIT.md` | `extract` | 抽取请求生命周期不变量、风险和质量场景；文件/函数审计结果不迁。 | `G-LIFECYCLE`, `G-QUALITY`, `A-RISK` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/GATEWAY_PRELAUNCH_FIX_REPORT.md` | `extract` | 只迁仍开放的风险或稳定质量要求；修复清单与测试输出不迁。 | `G-QUALITY`, `A-RISK`, `G-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/PLAN-archive-lifecycle-2026-07.md` | `merge` | Provider/Channel/Route 归档、引用保护、可恢复性和审计语义。 | `G-DATA`, `ADMIN-OPS`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/PLAN-channel-credential-gate-2026-07.md` | `merge` | 凭据失效门禁、轮换、自动检测和可观察状态。 | `G-RUNTIME`, `ADMIN-OPS`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/RELEASE_BLOCKERS.md` | `extract` | 开放项转为质量风险或路线图；已关闭 blocker 和动态列表不迁。 | `G-QUALITY`, `A-RISK`, `G-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/REMEDIATION-context-compaction-and-payload-limit.md` | `merge` | 请求体上限、native/synthetic compact、错误与降级边界。 | `G-API`, `G-ADAPTER`, `G-LIFECYCLE`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/ROUTING_P3_IMPLEMENTATION_LOG.md` | `history-only` | P3 逐步实施记录；稳定结果由路由设计和 DEC 迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/production/ROUTING_P3_LOAD_SPREAD.md` | `merge` | 候选打散、限流、错误/TTFT/成本信号和线路内 fallback。 | `G-ROUTING`, `G-ADMISSION`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md` | `merge` | Provider Origin 故障域、permit、全局准入、熔断、代际和 fail-closed；实现批次不迁。 | `G-ROUTING`, `G-ADMISSION`, `G-RUNTIME`, `G-ADR` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/ROUTING_P4_IMPLEMENTATION_LOG.md` | `history-only` | P4 实施日志不迁；完成事实从代码/测试核验，长期结论由 DEC 迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/production/ROUTING_P4_OPEN_DECISIONS.md` | `extract` | 已解决项并入 ADR；真正未决项转为风险/路线图，不保留会议式清单。 | `G-ADR`, `A-RISK`, `G-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/THIRD_PARTY_POLICY.md` | `merge` | 跨仓库依赖评估、核心逻辑/通用能力边界和安全维护要求。 | `SPEC-CODE` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/production/TODO_REGISTER.md` | `extract` | 开放长期风险/目标转入 Blueprint；已关闭 GAP、任务链接和动态状态不迁。 | `G-ROADMAP`, `A-RISK`, `PLATFORM-ROADMAP` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
## 6. 公开协议资料（10）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/protocol/CAPABILITY_KEYS.md` | `merge` | 当前有效能力 key、等级和声明语义；删除已取代的自动学习/渠道收紧内容。 | `G-CAP`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/protocol/README.md` | `history-only` | 旧协议快照维护规则；Blueprint 只维护 Unio 契约和外链。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/protocol/anthropic/messages/official.md` | `external-reference` | 不迁 Anthropic 官方协议快照；保留官方链接和快照日期作为核验来源。 | `G-COMPAT`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/protocol/anthropic/messages/params.md` | `merge` | 只迁 Unio 支持/拒绝/Drop 的字段差异；通用参数解释不迁。 | `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/protocol/openai/chat-completions/official.md` | `external-reference` | 不迁 OpenAI 官方快照；保留官方链接和版本日期。 | `G-COMPAT`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/protocol/openai/chat-completions/params.md` | `merge` | 只迁 Unio 字段支持和兼容差异。 | `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/protocol/openai/responses/official-other-endpoints.md` | `external-reference` | 不迁官方 endpoint/Error schema 快照。 | `G-COMPAT`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/protocol/openai/responses/official-streaming-events.md` | `external-reference` | 不迁官方流式事件目录；只在 Unio 差异文档链接来源。 | `G-COMPAT`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/protocol/openai/responses/official.md` | `external-reference` | 不迁 OpenAI Responses 官方全文快照。 | `G-COMPAT`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/protocol/openai/responses/params.md` | `merge` | 只迁 Unio 支持范围、桥接和偏差；通用字段说明不迁。 | `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
## 7. Provider 适配资料（20）

| 来源 | 动作 | 长期内容/处置 | 目标 | 迁移核验 | Owner 审核 | 最终处置日期 |
| --- | --- | --- | --- | --- | --- | --- |
| `docs/providers/README.md` | `extract` | 抽取 Provider 文档的长期责任边界；旧目录维护流程不迁。 | `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/README.md` | `merge` | Anthropic 上游支持范围、协议/端点和已知限制。 | `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/adaptation.md` | `merge` | Anthropic 请求/响应转换、header、流式和错误差异。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/billing.md` | `merge` | Anthropic usage 与缓存计费维度的 Unio 归一规则。 | `G-ADAPTER`, `G-BILLING` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/passthrough-audit.md` | `extract` | 抽取仍有效的 beta header、未知字段、透传和计费维度差异；审计过程不迁。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/protocol-and-params.md` | `merge` | 上游支持矩阵与 Unio 映射差异；官方通用参数不复制。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/anthropic/upgrade-plan.md` | `history-only` | 初始新增/升级实施计划；当前支持事实由其他文档迁移。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/README.md` | `merge` | DeepSeek 上游边界、双格式支持和限制。 | `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/anthropic-api-reference.md` | `external-reference` | 不复制 DeepSeek API 参考摘要；仅保留官方来源和 Unio 差异。 | `G-ADAPTER`（仅链接） | `外链/差异边界已核验` | `未指定` | `—` |
| `docs/providers/deepseek/anthropic/adaptation.md` | `merge` | DeepSeek Anthropic 格式的转换与限制。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/anthropic/protocol-and-params.md` | `merge` | DeepSeek Anthropic 格式映射矩阵，去掉官方通用说明。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/billing.md` | `merge` | DeepSeek usage、缓存和计费事实归一。 | `G-ADAPTER`, `G-BILLING` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/openai/adaptation.md` | `merge` | DeepSeek OpenAI 格式的转换、reasoning 和错误差异。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/openai/protocol-and-params.md` | `merge` | DeepSeek OpenAI 格式映射矩阵，去掉官方通用说明。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/deepseek/upgrade-plan.md` | `history-only` | 初始新增/升级实施计划；不迁任务顺序。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
| `docs/providers/openai/README.md` | `merge` | OpenAI 官方上游支持范围、native Responses 和限制。 | `G-ADAPTER` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/openai/adaptation.md` | `merge` | OpenAI 请求/响应、透传、流式和错误适配差异。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/openai/billing.md` | `merge` | OpenAI usage、缓存写入档位和计费事实归一。 | `G-ADAPTER`, `G-BILLING` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/openai/protocol-and-params.md` | `merge` | OpenAI 上游与公开契约的映射差异；官方通用字段不复制。 | `G-ADAPTER`, `G-COMPAT` | `草案已迁入；迁移者已核验` | `未指定` | `—` |
| `docs/providers/openai/upgrade-plan.md` | `history-only` | 初始新增/升级实施计划；不迁任务顺序。 | 无 | `无需迁正文；迁移者已核验` | `未指定` | `—` |
## 8. 数量校验与完成记录

| 区域 | 基线数量 |
| --- | ---: |
| 顶层与架构 | 3 |
| `chapters/` | 63 |
| `datasources/` | 3 |
| `production/` | 28 |
| `protocol/` | 10 |
| `providers/` | 20 |
| **合计** | **127** |

每行目标代号已由第 1 节解析到当前 Blueprint 路径；目标提交/PR 尚未形成，Owner 与最终处置日期
保持待填。任何来源删除前，应补齐目标版本和审核记录，并重新以文件系统清单与本表做集合比对，
要求无遗漏、无多余且每个来源只有一个主处置行。
