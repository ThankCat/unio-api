# Gateway 文档迁移至 Blueprint 计划

状态：执行中；Owner 语义决定已记录（11/11），Gateway 功能文档 14/14、ADR 4/11 已接收，待其余状态接收与来源分类批准

创建日期：2026-07-25

## 1. 目标

将 `unio-gateway` 中具有长期价值的产品、架构、契约、决策、质量和运行语义，按概念整理后迁移到
`unio-blueprint`。迁移完成后：

- Blueprint 是 Gateway 长期知识的唯一权威来源（SSOT）。
- Gateway 不再维护产品、架构、决策、协议说明、阶段状态和历史实施文档。
- Gateway 只保留仓库入口、协作约束、本地开发、构建、测试、数据库迁移和代码生成说明。
- 新改造先在 Gateway 使用临时 `docs/changes/<change-id>/PLAN.md`，实现和测试完成后把长期结论回写
  Blueprint，再删除临时计划；Git 历史保留实施过程。

本计划只定义迁移工作。当前批次已修改 Blueprint 草案和本目录迁移台账，但未修改 Gateway 生产代码、
未删除现有 Gateway 来源文档，也未切换仓库入口；全过程不读写 PostgreSQL、Redis 或其他本地运行数据。

## 2. 迁移范围

### 2.1 纳入评估

- `docs/` 下迁移开始前已有的 127 份 Markdown 文档。
- `docs/production/DECISIONS.md` 中 DEC-001 至 DEC-055。
- Gateway 根目录的 `README.md`、`AGENTS.md` 及本地开发说明，仅在最终切换阶段做瘦身和链接更新。
- Blueprint 现有 Gateway、Admin、Architecture、Specifications、Decisions 和 Roadmap 文档。

### 2.2 不进入 Blueprint 正文

- 调试日志、命令输出、测试运行记录和实施日志。
- 已完成阶段的任务顺序、进度百分比、状态报告和失效 TODO。
- 源文件路径、函数名、SQL 细节、框架配置和本地操作命令。
- 从 OpenAI、Anthropic、DeepSeek、models.dev 等复制的官方或第三方文档。
- 只证明一次实施完成、但不定义长期行为的原始测试矩阵。

这些材料可以作为迁移核验的证据，但不会成为新的权威说明。

## 3. 权威边界

### 3.1 最终文档归属

| 位置 | 长期职责 |
| --- | --- |
| Blueprint | 产品定义、领域模型、架构与部署约束、公开 API 行为、协议兼容、路由、计费与账本、安全、数据语义、ADR、质量要求、风险和路线图。 |
| Gateway `README.md` | 仓库用途、快速开始、Blueprint 权威入口和开发文档入口。 |
| Gateway `AGENTS.md` | 仅保留在本仓库协作时必须遵守、且无法由 Blueprint 取代的轻量规则。 |
| Gateway `DEVELOPMENT.md` | 构建、测试、本地依赖、迁移、sqlc、调试和仓库实现约定。 |
| Gateway `docs/changes/<change-id>/PLAN.md` | 尚未完成的单次代码改造计划；不是长期事实。 |

迁移基线核验时 Gateway 根目录尚无 `README.md` 与 `DEVELOPMENT.md`。Phase 6 若获批准，应先创建精简的
仓库入口和开发说明，再切换旧文档入口；本阶段不提前创建，也不把 `docs/README.md` 当作根入口替代品。

### 3.2 迁移期间的事实优先级

发现冲突时，先区分“当前行为”“已批准目标”和“优化缺口”，再按以下顺序处理：

1. 当前代码、数据库 schema 和测试共同证明的可观察结果，是当前行为的唯一依据。
2. 有日期、范围和取代关系的 Owner 新决定可以取代先前决策，但在代码实现并通过相应验证前，只能记录为
   未实现目标或决策谱系，不得进入当前行为。
3. active ADR 与当前行为冲突时，分别记录 ADR 的批准目标、代码事实和二者差距；active ADR 不会改写
   未实现的当前行为，代码事实也不会自动把 ADR 变为失效。
4. Gateway 中仍有效的已接受 DEC，用于解释来源和取代链；它不能覆盖后续 Owner 决定或已证明的当前行为。
5. 当前阶段状态或验收记录可补充核验；历史计划、实现日志和调试记录仅保留必要谱系。

代码、schema 和测试是当前事实的唯一证据，不自动成为新的产品决策。Blueprint 的当前系统说明只记录这些
已验证事实。任何优化建议、风险或未来目标都必须单列为风险、路线图或临时改造项，不得用建议的目标态覆盖
当前行为。冲突记录至少写明当前行为、批准目标、差距、Owner 和后续处置（修正文档、安排改造或新建取代决定）。

## 4. 迁移处理原则

### 4.1 按概念迁移

不按源文件一对一复制。每个长期概念只进入一个权威位置，其他文档改为链接。一个来源可以拆到多个
目标；多个来源也可以合并为一个设计或 ADR。

### 4.2 细节保留标准

满足任一条件的细节应保留：

- 改变客户或运营人员可观察到的行为、错误、计费或兼容结果；
- 约束多个模块、服务或后续实现，且违反后会破坏一致性；
- 定义数据、金额、身份、安全、审计、幂等、恢复或故障处置不变量；
- 解释一项仍有效决策为何成立，或记录被否决方案对未来仍有警示价值；
- 定义可验证的质量目标、部署约束、支持边界或风险；
- 是理解取代关系所必需的来源编号、日期、范围或后果。
- 已证明的当前行为，即使同时存在更优的目标态或安全/运行风险；目标态必须与当前事实分层书写。

以下细节默认不保留：

- 只对当前目录结构、函数、SQL、命令或实现批次有意义；
- 可以直接从代码生成或由测试发现，且不表达产品意图；
- 重复上游官方文档且没有 Unio 特有差异；
- 仅描述“做过什么”，不约束“系统现在必须怎样”；
- 已被明确取代，且不需要作为 ADR 历史保留。

### 4.3 状态与决策处理

- 导入 Blueprint 的普通文档初始为 `draft`；待决策内容初始为 `proposed`。
- Gateway 的 `accepted` 或 `implemented` 不自动把 Blueprint 文档变为 `active`。
- 只有 Blueprint owner 评审后才能设为 `active`。
- DEC 来源编号、明确日期、原始状态、部分修订和 `superseded-by` 链必须保留在目标 ADR 的来源说明中。
- 不把 55 条 DEC 机械复制为 55 份 ADR；按当前有效概念形成 ADR 集群，已取代条目作为来源历史挂到
  取代它的 ADR。
- Owner 对当前事实作出的确认必须记录日期、范围和受影响的 DEC/ADR；Owner 的新目标决定还必须记录其
  实现状态，避免把“已决定”误写成“已实现”。

### 4.4 删除门禁

任一 Gateway 来源文档只有同时满足下列条件才能删除：

1. 已在 `MIGRATION-MAP.md` 登记处理方式和目标；
2. 其中所有长期事实已迁移，或明确判定为无需迁移；
3. DEC 来源和取代链已在 `DECISION-MAP.md` 及目标 ADR 中保留；
4. Gateway 和 Blueprint 内部引用已更新；
5. Blueprint 校验通过；
6. 负责人完成语义评审；
7. 删除与目标文档变更处于可审查、可回滚的迁移批次中。

## 5. 目标信息架构（拟议）

最终文件名在迁移批次评审时确定，当前按下列概念归位：

| 概念 | Blueprint 权威区域 |
| --- | --- |
| 平台产品、商业模型和领域关系 | `docs/architecture/overview.md`、Gateway/Admin 概览与词汇表 |
| 公开 API、协议兼容和错误语义 | `docs/products/gateway/features/`、`docs/specifications/api-style.md` |
| Provider 适配和协议转换边界 | Gateway 功能设计与领域 ADR |
| 请求生命周期、计费、账本和恢复 | Gateway 功能设计、平台质量与领域 ADR |
| 模型目录和能力声明 | Gateway 功能设计、词汇表与领域 ADR |
| 线路、候选、负载均衡、限流和熔断 | 现有 `routing-load-balancing.md`、补充功能设计与领域 ADR |
| 运行时配置、部署和 Redis/PostgreSQL 约束 | Gateway 功能设计、`architecture/deployment.md`、领域 ADR |
| Admin 运营行为和页面契约 | `docs/products/admin/` 下的概览、功能、页面、质量和 ADR |
| 跨仓库工程与第三方依赖原则 | `docs/specifications/coding.md` 等共享规范 |
| 当前风险与建设顺序 | Gateway/Admin quality、roadmap、平台 risks/roadmap |

## 6. 执行阶段与门禁

### Phase 0：批准治理规则

输出：

- 在 Blueprint 文档规范中加入按概念迁移、证据优先级、细节保留和导入状态规则。
- 在贡献指南中加入来源删除门禁。
- 在编码规范中加入“代码变更计划 -> 实现/测试 -> Blueprint 归档”的交接规则。

门禁：规则评审通过且 `make validate` 通过后，才能迁移正文。

### Phase 1：冻结清单与决策谱系

输出：

- `MIGRATION-MAP.md` 覆盖全部 127 份来源文档。
- `DECISION-MAP.md` 覆盖 DEC-001 至 DEC-055，并明确有效范围、取代关系和目标 ADR 集群。
- 登记 Blueprint 现有内容与 Gateway 来源冲突，尤其是领域术语和已存在的路由设计。

门禁：文档数量、DEC 编号和来源链接无遗漏；负责人批准分类和 ADR 集群。

### Phase 2：迁移产品、架构、商业和账务核心

输出：

- Gateway 产品边界、领域模型、线路商品模型和定价/成本语义。
- 请求、用量、余额、预授权、结算、核销和恢复不变量。
- 跨产品部署、安全、数据和审计约束。

门禁：没有将实现状态写成产品承诺；金额和账本语义由相关 owner 复核。

### Phase 3：迁移协议、Provider 和请求生命周期

输出：

- OpenAI Chat Completions、Responses 和 Anthropic Messages 的 Unio 特有兼容行为。
- Provider 适配、Drop/Reject、直传/桥接、usage/ResponseFacts 和错误边界。
- 只链接官方资料，不迁移官方协议快照。

门禁：每项行为能追溯到 DEC、测试或当前实现；不存在复制的第三方全文。

### Phase 4：迁移路由、运行控制、安全和 Admin

输出：

- 候选准入、负载均衡、fallback、限流、熔断、配置代际和 fail-closed 语义。
- Provider Origin、Channel 凭据检测、归档生命周期和 Admin 客观运行事实。
- 质量场景、部署限制、风险和未完成路线图事项。

门禁：区分“已实现”“已接受待实现”“提议”；不能用文档掩盖代码差距。

### Phase 5：交叉核验与评审

执行：

- 以代码、schema 和测试做只读证据抽查，不修改生产代码。
- 将不能由迁移者裁决的冲突集中到 `OWNER-REVIEW.md`，逐项记录方案、推荐、风险、验收门禁和签署字段。
- 对 Blueprint 运行 `make validate`。
- 检查 Front Matter、相对链接、目录索引、状态和 owner。
- 检查每个 DEC 来源编号在目标 ADR 或明确的历史处置中可搜索到。
- 检查每个来源文档在映射表中恰好有一行。

门禁：所有语义差异有结论或风险登记；负责人批准需要成为 `active` 的内容。

### Phase 6：切换唯一权威来源

执行：

- 先更新 Gateway 根 `README.md`、`AGENTS.md` 和 `DEVELOPMENT.md`，指向 Blueprint。
- 再删除已经通过门禁的 `docs/` 长期/历史文档。
- 保留迁移计划直到切换验证完成，然后将其作为最后一个临时计划删除。
- 检查仓库内不存在指向已删除文档的链接或把 Gateway 文档称为权威来源的文字。

门禁：Gateway 的构建和常规单元测试不依赖已删除文档；Blueprint 校验通过；迁移批次可独立回滚。

### Phase 7：采用日常交接流程

后续每项 Gateway 改造使用：

1. 在 `docs/changes/<change-id>/PLAN.md` 写清问题、预期行为、兼容/数据风险、测试和 Blueprint 影响。
2. 实现代码并完成与风险匹配的测试。
3. 根据实际结果更新 Blueprint 的设计、ADR、质量、风险或路线图；不得把临时实施步骤搬入 Blueprint。
4. 运行 Gateway 测试和 Blueprint `make validate`。
5. 评审通过后删除临时计划；提交或 PR 保留计划历史和测试证据。

如果改造没有长期语义变化，应在临时计划中明确写“无需更新 Blueprint”及原因，避免为了归档而制造
无价值文档变更。

## 7. 批次策略与回滚

- 每个迁移批次只处理一个可独立评审的概念集，不进行全仓一次性重写。
- 目标文档先落为 `draft`/`proposed`，源文档在通过删除门禁前保持不动。
- 每批记录来源路径、DEC 编号和目标链接，允许用版本控制整体撤销该批。
- 不通过删除数据库、清空 Redis、重建本地环境或运行破坏性集成测试来验证文档迁移。
- 如需本地服务验证，只使用隔离命名空间或临时资源；结束后验证临时资源已清除。默认本迁移不需要启动服务。

## 8. 完成标准

- 127 份来源文档全部有最终处置，DEC-001 至 DEC-055 全部可追溯。
- Blueprint 中每个长期概念只有一个权威位置，状态、owner 和链接符合规范。
- Gateway 只保留根级仓库/开发文档及正在实施的临时 change plan。
- Gateway 中不存在继续维护的阶段、生产、协议、Provider 或架构文档树。
- Blueprint 校验、链接检查和 Gateway 常规测试通过。
- 没有修改、清除或污染用户现有 PostgreSQL、Redis 及其他本地数据。

## 9. 当前执行状态

| 阶段 | 当前结果 | 门禁状态 |
| --- | --- | --- |
| Phase 0 | Blueprint 治理、贡献与编码规范已形成迁移草案。 | 文档校验通过，待 Owner 接收。 |
| Phase 1 | `MIGRATION-MAP.md` 覆盖并逐份核验 127 份来源；`DECISION-MAP.md` 覆盖 DEC-001~055。 | 集合、数量、列结构与决策编号核验通过；语义差异已收口，待来源分类与 ADR 集群批准。 |
| Phase 2~4 | 产品、架构、账务、协议、Provider、路由、运行控制、安全与 Admin 长期内容已迁入 Blueprint。 | Gateway ADR 已接收 4/11，其余新 ADR 与普通文档继续按各自 `proposed`/`draft` 状态评审。 |
| Phase 5 | 已按 2026-07-26 本地工作树中的代码、Schema 和现有测试证据完成只读核验；Owner Review 语义项完成 11/11，Gateway 功能文档完成 14/14，Gateway ADR 完成 4/11，并均按当前事实接收为 `active`。 | Gateway 功能文档状态接收已完成，ADR 状态接收继续进行；Phase 5 整体门禁仍待其余 Blueprint `draft`/`proposed` 内容和来源分类批准。按次工具收费、完整 bridge/SDK 验证、画像来源治理、跨实例竞态、Admin 统一预检、schema-version 门禁和 Redis 高可用等仍是当前未实现或未验证事实。当前状态接收与校验通过都不构成 Phase 6 删除或 SSOT 切换授权。 |
| Phase 6 | 尚未开始。 | 不删除来源，不修改 Gateway `AGENTS.md`/`docs/README.md`，不切换 SSOT 入口。 |
| Phase 7 | 日常“临时计划 -> 实现/测试 -> Blueprint 归档”流程已写入治理草案。 | 待 Phase 6 与 Owner 批准后正式采用。 |

最终校验结果（2026-07-26）：Blueprint 校验器搬迁已收口，`Makefile` 现调用
`docs/scripts/validate-docs.py`，脚本从新层级正确推导仓库根目录，脚本 README 的 `related` 路径已更新。
`make validate` 已校验通过 131 份 Markdown 文件和 41 个文档目录。这只关闭 Phase 5 的工具与文档
校验门禁，不替代 Blueprint 状态接收、来源分类批准，也不授权 Phase 6 删除或 SSOT 切换。

Blueprint 状态接收进度（2026-07-26）：Gateway 功能文档已完成 14/14，以下文档均为 `active`：

- `access-control.md`、`public-api-contracts.md`、`protocol-compatibility.md`、`provider-adaptation.md`、
  `provider-mapping-contracts.md`；
- `request-lifecycle.md`、`error-semantics.md`、`billing-settlement.md`、`admission-control.md`；
- `model-capabilities-catalog.md`、`routing-load-balancing.md`、`resilience-circuit-breakers.md`、
  `runtime-control-recovery.md`、`data-lifecycle.md`。

接收口径是当前 Gateway 代码、Schema 和测试为唯一实现事实；文档只记录当前行为、当前限制与现有验证
边界。已移除功能文档中的未实现目标方案、长期方向和待办式验收。API Key 明文风险、Responses 直传
成功终态早于 recovery、partial/recovery 缺口、Drop 无持久审计、工具次数不参与收费、能力画像直接覆盖
声明、跨 scope 写入、运行控制与多实例验证缺口等继续按当前事实保留。

Gateway ADR 状态接收进度为 4/11：`ADR-0001`、`ADR-0002`、`ADR-0003` 与 `ADR-0004` 为 `active`。ADR-0002 已按
代码事实收紧为 API Key 隐式绑定的 Route 供给、调度与定价边界，并记录 Admin 可绑定停用/归档 Route、
零倍率无法通过预授权、`/v1/models` 不按协议过滤、历史 Route 名称/mode 未快照等当前限制。ADR-0003
已收紧为当前预授权、`token_v1`、capture/overage/write-off、快照、partial 和 recovery 事实，删除未实现
的工具按次收费目标，并保留 partial/长上下文 recovery、orphan 竞态和工具次数三态等缺口。ADR-0004
已按代码改为模型声明与运行时能力分离，记录 Admin replace-all、目录采纳/刷新 service、Adapter 画像
物化四类写入，以及 `/v1/models` 与真实 Adapter registry 的边界。其余 7 篇 Gateway ADR 继续保持
`proposed`，逐篇核验后再决定状态。

## 10. Owner 审核清单

逐项方案、推荐理由、兼容风险、验收门禁和签署栏见 [`OWNER-REVIEW.md`](OWNER-REVIEW.md)。下表只作为
审核领域索引，不以迁移者建议替代 Owner 决定。

| 审核方 | 必须确认的事项 | 当前阻断 |
| --- | --- | --- |
| 安全与产品 Owner | OR-001 已确认按当前 `key_plaintext` 保存与 Admin 重复回显迁移；后续评估脱敏、显式 reveal、轮换、读取审计与兼容迁移。 | 当前行为已确认；安全优化仍须另立改造计划，不得写成现状。 |
| 产品、数据与账务 Owner | OR-002 已确认不重新引入 Project，维持 `User Account -> API Key -> Route`。 | 当前无阻断；未来新分组或账务边界须另立决策。 |
| 账务 Owner | OR-003 已确认当前 overage debit 与软 `spend_limit`；OR-004 已确认当前 sweeper；OR-005 已选择 `web_search`/`web_fetch` 两个独立按次计价项。 | OR-005 目标尚未实现；partial/recovery 三态、独立售价/成本快照、授权估算、公式版本、unknown 口径与防重复收费须进入 Gateway 改造计划。 |
| 协议与能力 Owner | OR-006 已选择“原生 Responses 能力直传、chat-only Adapter bridge”；OR-008 已确认仅按当前代码记录 `protocol_scope`。 | B 路径已确认但 bridge 支持矩阵与验收未完成；`DroppedFields` 当前被生产调用点丢弃，Provider Adapter Drop 也仅写应用日志。`protocol_scope` 当前只是字典分类；模型声明没有协议维度，Anthropic Adapter 画像可声明并物化多个 `openai` scope key，`/v1/models` 不按 scope 或 Channel 协议过滤，真实请求候选才按 ingress protocol 过滤。 |
| 架构与运行 Owner | OR-010 已确认当前“只读候选快照 + 每个真实 transport 前逐候选原子准入”；OR-011 已确认只迁实际进程、模块/事务、依赖预检、健康与分层 fail-closed 事实，不批准未来部署方案。 | 当前事实已收口；OR-010 完整多实例竞态与运营关联证据仍缺。OR-011 的 Admin 统一 Redis 预检、schema-version 门禁与高可用能力仍未实现。 |
| Gateway 与 Admin Owner | Gateway 14 篇功能文档和 4/11 篇 Gateway ADR 已按当前实现事实接收；其余 Admin 文档与 `proposed` ADR 仍按各自状态评审。 | 当前 `adapter_seed` 入口仍可直接覆盖模型能力声明，声明没有来源或协议维度；这些事实已记录，但不自动完成其余文档和 ADR 的状态接收。 |

来源处理分类和 Phase 6 审核完成前，`MIGRATION-MAP.md` 的 Owner 状态保持“未指定”，最终处置日期保持
空缺；Gateway 功能文档 14/14 与 ADR 4/11 接收不构成删除授权。其余 Blueprint 文档和 ADR 仍按各自
`draft`/`proposed` 状态评审。只有完成 Phase 6 的独立评审后，Gateway 才能删除旧文档并切换唯一权威入口。
