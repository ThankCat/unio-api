# Phase 12 Plan - 能力架构（Capability Architecture）

## 阶段定位

阶段 12 把"协议字段是否被该模型支持"从 **adapter 静态代码 + 文档约定** 升级为 **运行时事实 + 显式闸门 + 公开能力契约**。

由 [DEC-015](../../production/DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位) 决定：

- 协议（OpenAI Chat Completions / Anthropic Messages / OpenAI Responses）保持统一对外契约；
- 模型能力（image generation、audio output、multimodal input、structured outputs、reasoning、prompt cache、内置工具 …）逐项落库；
- ingress 收到合法协议请求时，按"required capabilities × channel capability 矩阵"决定 **Drop / Reject / Route**；
- 不能因 provider 能力静默吞掉客户期望的功能（避免"付了钱没拿到图片"反人类体验）。

阶段 11（OpenAI Responses）已在 `CAPABILITY_MATRIX.md` 用静态文档表达 Responses 的能力边界。阶段 12 把同一套思路扩展到 **所有协议 × 所有 channel** 并落到运行时表与闸门。

## 与其他阶段的关系

```text
Phase 10 双协议 Gateway (done)            ← 共享 lifecycle / facts / routing
   ↓
Phase 11 OpenAI Responses (in progress)   ← Codex 接入；CAPABILITY_MATRIX 静态版
   ↓
Phase 12 Capability Architecture (本阶段) ← 把静态约定 → 运行时事实
   ↓
Phase 13 Admin (planned)                  ← 后台可视化管理 capability + models.dev 触发
```

阶段 12 不引入新协议族、不改账务事实 schema、不动 Phase 10 的 lifecycle/facts。它只增加：

```text
1. 数据：models / model_capabilities / channel_capability_overrides 表 + cron 同步
2. 逻辑：ingress 能力推断 + routing capability filter + 三协议 capability errors
3. 公开面：/v1/models 扩展（cap-tags）+ /console/v1/models（运营视图）
4. 观测：cap_check metrics + dropped/rejected capability 审计
```

## 三层能力模型（实现锚点）

```text
Layer 1: Model Metadata (canonical)
  来源：models.dev 同步 + 人工补全
  存储：models 表
  字段：canonical_id / lab / context_window / max_output / input_price / output_price /
        release / coarse_caps_hint (reasoning/tools/structured/vision，仅作初始 hint)
  用途：catalog 展示、模型选择、价格基线

Layer 2: Protocol Capability Matrix
  来源：Unio 自己维护，按"协议字段/子能力"粒度
  存储：model_capabilities 表 (model_id × capability_key)
  字段：support_level (full / limited / unsupported)、limits (json)、source (synced/manual/adapter_seed)
  用途：runtime gating、cap-tags 公开

Layer 3: Channel Capability Overrides
  来源：admin 后台手动配置（阶段 13）
  存储：channel_capability_overrides 表 (channel_id × capability_key → enabled false 或 limits 收紧)
  规则：只能做减法；不能在 channel 上声明 Layer 2 未声明的能力
  用途：某 channel 受供应商限制的具体闸门
```

## 涉及目录

| 目录 / 文件 | 作用 |
| --- | --- |
| [migrations/](../../../migrations) | `models`、`model_capabilities`、`channel_capability_overrides`、`model_capability_sync_jobs` 表 + 索引。 |
| [sql/queries/](../../../sql/queries) | 上述表 query。 |
| [internal/core/capability](../../../internal/core/capability) | 新核心包：能力 keys、support level、required capability inference、闸门 service。 |
| [internal/core/modelcatalog](../../../internal/core/modelcatalog) | 与 capability 表对接，扩展 `/v1/models` 输出。 |
| [internal/core/routing](../../../internal/core/routing) | routing 加 capability filter。 |
| [internal/app/gatewayapi/openai/chatcompletions](../../../internal/app/gatewayapi/openai/chatcompletions) | ingress 推断 + capability error 渲染。 |
| [internal/app/gatewayapi/anthropic/messages](../../../internal/app/gatewayapi/anthropic/messages) | 同上。 |
| [internal/app/gatewayapi/openai/responses](../../../internal/app/gatewayapi/openai/responses) | 同上（阶段 11 落地后挂入）。 |
| [internal/app/workers](../../../internal/app/workers) | models.dev sync worker。 |
| [docs/protocol/](../../protocol) | capability_keys 注册表。 |

## 任务

<a id="task-12-01-capability-schema"></a>
### TASK-12.01 Capability schema 与基础查询

状态：done

> 落地说明（与计划文本的对齐）：
> - **复用现有 `models` 表**（[migrations/000007](../../../migrations/000007_create_models.up.sql)，`id BIGSERIAL` + `model_id TEXT UNIQUE`）而非新建 uuid 表；Layer 1 列以追加方式加入。`enabled` 复用现有 `status`（enabled/disabled），不新增 boolean。三张能力表外键用 `models.id`/`channels.id`（BIGINT），PK 统一 BIGSERIAL（对齐现有约定）。
> - `capability_key` 在 DB 存 `TEXT` 不加枚举 CHECK（注册表只增不删、由代码 + 文档治理），合法性在 app 层 `internal/core/capability` 校验。
> - 仅交付事实基座（schema + sqlc + `core/capability` 访问层 + 公开注册表 + DB 门控测试）；推断/闸门/同步/种子归 12.02~12.06。
> - GAP-12-010 只就位数据列 `models.max_output_tokens`，authorization 代码接线待该列有数据后单独处理。

目标：

```text
把"模型 / 模型能力 / channel 能力覆盖"沉淀为可读可写的数据事实，
并定义稳定的 capability_key 注册表。
```

计划实现：

1. 新建 `models` 表：
   - `id`（uuid）、`canonical_id`（如 `deepseek/deepseek-chat`）、`lab`、`display_name`
   - `context_window_tokens`、`max_output_tokens`、`input_price_usd_per_million_tokens`、`output_price_usd_per_million_tokens`
   - `release_date`、`updated_at`、`source`（`seed_models_dev` / `manual` / `import`）
   - `enabled` boolean
   - **预授权联动（[GAP-12-010](../../production/TODO_REGISTER.md#gap-12-010)）**：`max_output_tokens` 落库后，`lifecycle` authorization 在客户省略输出上限时改用该模型上限替代全局 `DefaultAuthorizationMaxCompletionTokens=4096` 兜底，消除 DeepSeek-V4 长输出预冻结不足导致的平台核销漏收。
2. 新建 `model_capabilities` 表：
   - `model_id` × `capability_key`（PK）
   - `support_level` enum：`full` / `limited` / `unsupported`
   - `limits` jsonb（如 `reasoning_effort` 允许值集合、`tools.max_count`）
   - `source` enum：`models_dev` / `manual` / `adapter_seed`
   - `updated_at` / `updated_by`
3. 新建 `channel_capability_overrides` 表：
   - `channel_id` × `capability_key`（PK）
   - `support_level` 只允许 `limited` / `unsupported`（不能反向放开 Layer 2 未声明的能力）
   - `limits` jsonb（更严的收紧）
   - `reason` text、`updated_at` / `updated_by`
4. 新建 `model_capability_sync_jobs` 表（worker 用）：
   - `id`、`source`、`status`、`started_at`、`finished_at`、`stats_json`、`error_text`
5. 定义稳定 `capability_key` 注册表（写入 `docs/protocol/CAPABILITY_KEYS.md`），初版至少包含：

   ```text
   text.output                    # 文本输出（基本必有）
   text.input                     # 文本输入
   image.input                    # 多模态图片输入
   image.output                   # 图片生成输出
   audio.input                    # 多模态音频输入
   audio.output                   # 语音输出（modalities=audio）
   file.input                     # 文件输入
   tools.function                 # function 工具
   tools.custom                   # custom 工具（含 grammar / apply_patch）
   tools.parallel                 # parallel_tool_calls
   tools.choice_required          # tool_choice="required"
   tools.builtin.web_search       # 内置 web_search 工具
   tools.builtin.file_search      # 内置 file_search
   tools.builtin.code_interpreter # 内置 code_interpreter
   tools.builtin.computer_use     # 内置 computer_use
   tools.builtin.image_generation # 内置 image_generation
   tools.builtin.mcp              # MCP 工具
   reasoning.effort               # reasoning_effort 参数
   reasoning.budget               # Anthropic thinking budget
   reasoning.summary              # reasoning summary 返回
   response_format.json_object    # json_object 输出
   response_format.json_schema    # json_schema 输出
   prompt_cache                   # prompt caching
   logprobs                       # logprobs
   service_tier                   # service_tier
   stream                         # 流式
   stream.tools                   # 流式 tool_calls delta
   stream.usage                   # 流式 final usage
   server_state.store             # store=true 服务端持久化
   server_state.background        # background=true 异步
   responses.encrypted_content    # reasoning encrypted_content 透传
   ```

6. sqlc 生成；提供 `core/capability/store.go` 的最小查询接口（LookupModel、ListModelCapabilities、ListChannelOverrides、SyncJob CRUD）。
7. 业务注释完备；每张表 down migration 完整。

依赖：无（基础 schema 任务）。

关联 GAP：[GAP-12-001](../../production/TODO_REGISTER.md#gap-12-001)、[GAP-12-010](../../production/TODO_REGISTER.md#gap-12-010)

<a id="task-12-02-capability-inference"></a>
### TASK-12.02 Ingress capability inference

状态：done

> 落地说明（与计划文本的对齐）：
> - **分层修正**：计划文本写的 `InferFromOpenAIChat(*openai.ChatRequest)` 直接放 `core/capability` 会让 core 依赖 app DTO，违反依赖方向。实际落地为：`core/capability` 持有协议无关的 `RequestSignals` + `Infer(signals) Set` + `Set` 类型（集中规则、可单测）；三协议 ingress 包各自实现 `capabilitySignals(req) capability.RequestSignals` 从自身 DTO 抽取信号（content/tools/thinking JSON 解析留在 app）。规则集中、解析分散，既不破坏分层又满足「每条规则单测」。
> - **基线决策**：`text.input` + `text.output` 作为三协议统一基线始终纳入 required（对话类模型普遍支持，零误拒风险）。
> - **协议语义差异**：Anthropic `thinking`→`reasoning.budget`（非 effort）、`tool_choice=any|tool`→`tools.choice_required`、无 `include_usage` 故不推断 `stream.usage`；Anthropic 客户 custom 工具计为 `tools.function`，未建模 server tool（bash/text_editor/memory/tool_search/web_fetch）不造 cap。Responses `namespace`(Codex MCP)→`tools.builtin.mcp`。
> - **范围边界**：本任务只交付推断纯函数 + 抽取 + 单测/fuzz；handler decode 后挂 request context 的接线（计划步骤 4）按工程顺序并入 [TASK-12.03](#task-12-03-capability-filter) 闸门，避免无消费方 dead code。**接线已在 TASK-12.03 完成**（三协议 `RequiredCapabilities(req)` 导出 + 6 service 调用点透传 `routing.ChatRouteRequest.RequiredCapabilities` + observe 消费 + `request_attempts.required_capabilities` 审计）。
> - **档位值抽取已收口（[GAP-12-012](../../production/TODO_REGISTER.md#gap-12-012) 关闭）**：`RequestSignals.ReasoningEffortLevel` + `InferLimits(signals) RequestLimits` 把 `reasoning.effort` 档位值（low/medium/high）抽进请求侧约束；三协议各导出 `RequestLimits(req)`（Anthropic 用 thinking budget 无 effort 档位，恒空）→ 6 service 调用点透传 `routing.ChatRouteRequest.RequestLimits` → 闸门 `Evaluate(...,in.Limits)` 真正消费 limited 超限判定。单测覆盖 `InferLimits` 与三协议抽取。
> - **残留**：[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002)（推断覆盖面 enforce 切换前按 observe 期 metric 复核）随 enforce 切换归阶段 13（受控 deferred）。

目标：

```text
把 ingress 收到的合法请求自动推断出 required_capabilities 集合，
作为 routing capability filter 的输入。
```

计划实现：

1. 新建 `core/capability/inference.go`，按协议族提供推断函数：
   - `InferFromOpenAIChat(req *openai.ChatRequest) capability.Required`
   - `InferFromAnthropicMessages(req *anthropic.MessagesRequest) capability.Required`
   - `InferFromOpenAIResponses(req *responses.Request) capability.Required`
2. 推断规则覆盖（初版）：
   - `modalities` 含 `audio` → `audio.output`
   - 任一 message content part 是 `image_url` / `input_image` → `image.input`
   - `audio` 字段 / `input_audio` part → `audio.input`
   - `tools[].type=="function"` → `tools.function`
   - `tools[].type=="custom"` → `tools.custom`
   - `parallel_tool_calls=true` → `tools.parallel`
   - `tool_choice=="required"` → `tools.choice_required`
   - `tools[].type=="web_search"` 等内置 → `tools.builtin.*`
   - `reasoning.effort` 非空 → `reasoning.effort`
   - `response_format.type=="json_schema"` → `response_format.json_schema`
   - `response_format.type=="json_object"` → `response_format.json_object`
   - `stream=true` → `stream`（+ `stream.tools` 若 tools 同时存在）
   - `store=true`、`background=true` → 对应 `server_state.*`
3. 推断纯函数，无 IO，输入只读，输出 set 数据结构（去重、稳定序）。
4. handler 在 decode 完成后立刻推断并把结果挂入请求 context，供 routing 与审计读取。
5. 单元测试覆盖每条规则；fuzz 测试确认未识别字段不产生空 cap。

依赖：[TASK-12.01](#task-12-01-capability-schema)。

关联 GAP：[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002)

<a id="task-12-03-capability-filter"></a>
### TASK-12.03 Routing capability filter 与 capability error

状态：done

> 落地说明（observe 闭环 + enforce 代码已交付，默认 observe；实际 enforce 切换归阶段 13）：
> - **范围决策（observe 闭环先行，enforce 代码随 TASK-12.08 就位）**：本任务按灰度先行交付「观察闭环」——推断接线（三协议）+ `gate.go` 纯判定 + routing observe filter（**只记录 + metric + 持久化，不拒绝、不删候选**）+ 定义 capability 错误码/类型。enforce 决策与三协议客户错误渲染由 [TASK-12.08](#task-12-08-backfill-and-migration) 补齐（默认 observe，开关全 false）；[GAP-12-003](../../production/TODO_REGISTER.md#gap-12-003) 已关闭（渲染 + 开关代码就位），实际 observe→enforce 切换是运营决策（[GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)），随观察期归阶段 13。
> - **零声明放行策略（DEC-015 灰度约定）**：模型零能力声明行 → 闸门判 `unprovisioned`（视为未 provisioning，记录后放行）；一旦模型有任意声明行 → 按 key 严格判定（缺该 key 或 `unsupported`/`limited` 超限 = `model_unavailable`）。避免在 Layer 2 数据铺好前误拒全量请求。判定顺序见 `gate.go` `Evaluate` 注释。
> - **已交付**：
>   - `core/capability/gate.go`：纯判定 `Evaluate(modelCaps, channels, required, limits) Evaluation`，稳定结论 `GateResult`（`ok` / `model_unavailable` / `channel_unavailable` / `unprovisioned` / `no_required` / `error`）；`gate_test.go` 覆盖零声明、无 required、缺/unsupported、limited 超限、channel override 只减、多候选放行。
>   - `core/routing/router.go`：`CapabilityChecker` 接口 + `CapabilityCheckInput`/`CapabilityObservation` + `SetCapabilityChecker` + `observeCapability`（仅把结论写进 `ChatRoutePlan.Capability`，**不过滤候选**）；sentinel `ErrModelCapabilityUnavailable`/`ErrChannelCapabilityUnavailable` 与 `failure` 错误码 `routing_model_capability_unavailable`/`routing_channel_capability_unavailable` 就位待 enforce 消费。
>   - `service/gateway/capabilitygate/checker.go`：`routing.CapabilityChecker` 服务层实现（读 store → `Evaluate` → metric → 结构化审计；store 异常 fail-open 记 `result=error` 放行），把 platform 可观测设施挡在 core 之外。
>   - 推断接线（三协议）：`app/gatewayapi/{openai/chatcompletions, anthropic/messages, openai/responses}` 各导出 `RequiredCapabilities(req)`；6 个 service 调用点（chat/stream、messages/stream、responses create/stream）在 `PlanChat` 前推断并透传 `routing.ChatRouteRequest.RequiredCapabilities`。
>   - 持久化审计：`request_attempts.required_capabilities TEXT[] NOT NULL`（[000010](../../../migrations/000010_create_request_attempts.up.sql)，`CreateRequestAttempt` 用 `COALESCE(...,'{}')` 对 `nil` 兜底）+ sqlc + `requestlog` / `lifecycle.CreateAttempt` / `AttemptRunner`(非流式/流式) / Anthropic messages 循环串联，把推断结果落每次 attempt 审计。
>   - metric：`metrics.IncCapabilityCheck` → `unio_gateway_capability_check_total{result}`；observe 审计日志（would-be 拒绝记 Warn，其余 Debug）。
>   - bootstrap：`gateway_server.go` 用 `capabilitygate.NewChecker` 接线到 `chatRouter.SetCapabilityChecker`。
> - **已收口（原 GAP）**：
>   - [GAP-12-012](../../production/TODO_REGISTER.md#gap-12-012) 已关闭：三协议 `RequestLimits(req)` 抽取 `reasoning.effort` 档位值，经 `routing.ChatRouteRequest.RequestLimits` 透传，闸门 `Evaluate(...,in.Limits)` 真正消费 limited 超限判定（`checker_test.go` 覆盖 high>medium → model_unavailable）。
>   - [GAP-12-003](../../production/TODO_REGISTER.md#gap-12-003) 已关闭：三协议 capability error 对外渲染 + enforce 开关随 TASK-12.08 交付（默认 observe）。

目标：

```text
routing 在选定模型候选后，按 required_capabilities × (model_capabilities ∩ channel overrides)
过滤候选；候选为空时返回稳定的 capability 错误。
```

计划实现：

1. 扩展 `core/routing/router.go`：在原"按 model + protocol + project policy"候选过滤之后，加入 capability filter。
2. 实现 `core/capability/gate.go`：
   - `Check(modelID, channelID, required) (Ok bool, Missing []capability.Key)`
   - 同时校验 limits（如 `reasoning.effort` 请求 `high` 但模型仅支持 `low/medium`）
3. routing 入口返回三类语义：
   - `model_not_found`：模型不存在或 disabled（已有）
   - `model_capability_unavailable`：模型本身不支持某能力（Layer 2 缺失）
   - `channel_capability_unavailable`：所有模型支持的 channel 都被 override 关闭了该能力
4. 错误对外渲染（三协议各自原生格式）：
   - OpenAI Chat：`error.type=invalid_request_error`, `error.code=model_capability_unavailable`, `error.message` 列出缺失 capability
   - Anthropic Messages：`type=error`, `error.type=invalid_request_error`, 同上
   - OpenAI Responses：`error.type=invalid_request_error`, `error.code=model_capability_unavailable`
5. 错误不暴露 channel 名字、credential 或上游身份；只告诉客户"模型 X 不支持 Y 能力，请切换支持该能力的模型"。
6. metric：`unio_gateway_capability_check_total{result="ok|model_unavailable|channel_unavailable"}`。

依赖：[TASK-12.01](#task-12-01-capability-schema)、[TASK-12.02](#task-12-02-capability-inference)。

关联 GAP：[GAP-12-003](../../production/TODO_REGISTER.md#gap-12-003)

<a id="task-12-04-models-dev-sync"></a>
### TASK-12.04 models.dev daily cron 同步

状态：done

目标：

```text
把 models.dev 每日同步到 `models` 表，作为 Layer 1 元数据种子源；
同步过程对 Unio 现有运营数据不破坏、可审计、可回滚。
```

models.dev 现有 4 个接口，结构与字段以 [docs/datasources/MODELS_DEV_API.md](../../datasources/MODELS_DEV_API.md) 为准：
`api.json`（provider×model+价格）、`models.json`（canonical 元数据）、`catalog.json`（二者合并）、`logos/<id>.svg`（UI 资产）。

计划实现：

1. 新建 `app/workers/model_catalog_sync_worker.go`：
   - 每日定时（默认 03:00 UTC，cron 表达式可配）
   - **canonical 元数据**拉 `https://models.dev/models.json`（≈122 KB，按 `lab/model` 键控，与 `models.canonical_id` 对齐）；**价格基线**取 `https://models.dev/api.json`（或 `catalog.json.providers`）的 per-provider `cost`；超时与重试由 `platform/httpx` 共享。
   - 解析为 `models.dev` schema 中间结构，写入 `model_capability_sync_jobs.stats_json`。
2. 冲突与合并规则：
   - 同 `canonical_id` 已存在 → 仅在 `source=seed_models_dev` 行上覆盖元数据字段（context_window、max_output、price、release）。
   - `source=manual` 的行**永不被覆盖**；输出 conflict 列表供后台 review。
   - 缺失（models.dev 删了某条）→ 不删本地，标记 `enabled=false` + `removed_upstream_at`，由人工决定。
3. 不自动开通新模型：models.dev 新模型只 upsert 元数据，`enabled=false`，必须 admin 手动开通才进入 routing。
4. License 处理：在 `docs/datasources/MODELS_DEV_LICENSE.md` 记录 license 摘要 + attribution 文本；首次同步与每次 license 变化要写入 audit log。
5. `model_capabilities` 不被 cron 直接覆盖；仅在新 model 首次入库时按 models.dev 粗能力位写入 `source=models_dev` 默认值（可由 admin 改写为 `source=manual`）。
6. 失败处理：拉取失败、解析失败、DB 失败均写入 sync_job error，不影响下次定时；连续 3 次失败发出 metric alert。
7. 提供 `cmd/worker-server` 的子命令或 admin 触发入口（阶段 13 接管 UI）：

   ```text
   worker-server sync-models --source=models-dev --dry-run
   ```

依赖：[TASK-12.01](#task-12-01-capability-schema)。

关联 GAP：[GAP-12-004](../../production/TODO_REGISTER.md#gap-12-004)、[GAP-12-005](../../production/TODO_REGISTER.md#gap-12-005)、[GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)

#### 落地说明（done）

实现包 `internal/core/modelcatalog`，分层与上面"计划实现"一致：

- `feed.go`：解析 `models.json`（canonical 元数据，必需）+ `api.json`（per-provider 价格，best-effort），合并为 `CanonicalModel`；价格用 `json.Number` 承载避免 float 精度损失（价格仅展示、绝不计费）；按 `provider.id == lab` 取第一方价格口径。
- `merge.go`：**纯函数** `PlanSync` 推导 insert / update / manual-conflict / removal，可独立单测、可审计。
- `store.go`：`SyncStore`（sqlc 实现）——新增 `UpsertSeedModelByCanonicalID`（`ON CONFLICT (canonical_id) ... WHERE source='seed_models_dev'` 守护，manual/import 永不被覆盖，竞态安全）、`ListCanonicalModels`、`MarkSeedModelRemovedUpstream`，复用 `UpsertModelCapability` / sync_job 生命周期查询。
- `fetcher.go`：`HTTPFetcher`（超时、`io.LimitReader` 体量上限、`models.json` 致命 / `api.json` best-effort）。
- `syncer.go`：编排 fetch → parse → plan → apply → `model_capability_sync_jobs` 生命周期，`stats_json` 落 license 指纹（`license=MIT` + `source_fingerprint`，对齐 [MODELS_DEV_LICENSE.md](../../datasources/MODELS_DEV_LICENSE.md)）；`--dry-run` 只算计划不写库。
- `app/workers/model_catalog_sync_worker.go`：cron worker，以最近 sync_job 为准做 interval 门控（跨实例/重启幂等）、失败指数退避、连续 3 次失败发结构化告警日志。
- 接线：`config.ModelCatalogSyncConfig`（`MODEL_CATALOG_SYNC_*`，默认 `Enabled=false` opt-in）+ `bootstrap.NewWorkerServerApp` 条件挂载 worker + `worker-server sync-models --source=models-dev --dry-run` 子命令。

合并规则落地：新模型默认 `disabled`+`source=seed_models_dev`、首次入库写 `source=models_dev` 粗能力位（`tool_call`/`reasoning`/`structured_output`/`attachment`/`modalities` → cap-tags，admin 后续精化）+ `max_output_tokens`/价格基线；上游删除只 `MarkSeedModelRemovedUpstream`（disabled + `removed_upstream_at`，不删本地）；manual 行进 conflict 列表供 review。

测试：`feed_test.go`/`merge_test.go`/`syncer_test.go` 纯函数 + fake 覆盖 parse、合并规则、dry-run、manual 守护竞态、fetch 失败、apply 失败标记 sync_job failed；`sync_db_test.go` DB 门控集成覆盖 manual 守护、首次入库 + 粗能力位 + 价格、上游删除标记、sync_job license 审计。

未尽（[GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)）：精确 UTC time-of-day cron 表达式（当前 interval 门控）、Prometheus sync 指标（当前告警日志，指标归 [GAP-12-008](../../production/TODO_REGISTER.md#gap-12-008)）、prod 启用 cron 前的 license/运营签字。

<a id="task-12-05-public-capability-surface"></a>
### TASK-12.05 Public capability surface（/v1/models 与 /console/v1/models）

状态：in_progress

> 落地说明（`/v1/models` cap-tags 已交付，`/console/v1/models` 受控 deferred 阶段 13）：
> - **已交付（`/v1/models` cap-tags + 过滤）**：`app/gatewayapi/openai/models` handler 在 OpenAI 标准 shape 上扩展 `capabilities` cap-tags 数组（SDK 忽略未知字段，未声明能力返回空数组）+ `?capability=a,b` AND 过滤（`parseCapabilityFilter`）；`core/modelcatalog.ListAvailableModels` 经 sqlc `ListAvailableModelsForProject`（`array_agg ... FILTER (support_level<>'unsupported')`）按 project 可见性 + cap-tags 输出，未识别 capability key 自然匹配不到（lenient）。`docs/protocol/CAPABILITY_KEYS.md` v1 公开稳定注册表已就位（TASK-12.01）。`handler_test.go` 覆盖 cap-tags 输出与过滤。对应计划步骤 1、5，关闭 [GAP-6-006](../../production/TODO_REGISTER.md#gap-6-006) 的 cap-tags 暴露子项。
> - **受控 deferred 阶段 13**：`/console/v1/models`（计划步骤 2）依赖 console-server 认证表面（user/project token），随阶段 13 console-server 落地；进程缓存 + pub/sub 失效（计划步骤 4）随 admin 编辑能力一并实现。见 [GAP-12-006](../../production/TODO_REGISTER.md#gap-12-006)。

目标：

```text
让客户与 console 前端能直接读取每个模型的 cap-tags，预检模型能力，
避免请求发出后才知道缺能力。
```

计划实现：

1. `GET /v1/models`（已有 OpenAI 标准 shape）：
   - 保留 `id` / `object` / `created` / `owned_by`，SDK 不破坏。
   - 新增可选嵌套 `capabilities` 字段（如 `["text.output","stream","tools.function","reasoning.effort"]`），按 OpenRouter 风格输出 cap-tags 数组。
   - 提供 query 参数 `?capability=image.input,tools.function` 过滤（AND 语义）。
2. `GET /console/v1/models`（新）：
   - 给运营 / 控制台前端用，包含 cap-tags、价格、context_window、支持的 provider 数量、是否启用。
   - 需要 console 认证（沿用阶段 3 user/project token，console-server 接管）。
3. 不在 ingress 错误响应里"建议替换模型"逻辑（避免向客户泄漏其他客户可见的模型）；只引导到 `/v1/models?capability=...`。
4. 缓存：cap-tags 数据在 modelcatalog 进程缓存 60s，admin 编辑后通过 pub/sub 失效（沿用 routing 的 cache invalidate 机制）。
5. 文档：在 `docs/protocol/CAPABILITY_KEYS.md` 公开稳定 capability_key 列表与版本号；列表只能新增，不能删除。

依赖：[TASK-12.01](#task-12-01-capability-schema)、[TASK-12.03](#task-12-03-capability-filter)。

关联 GAP：[GAP-12-006](../../production/TODO_REGISTER.md#gap-12-006)

<a id="task-12-06-adapter-alignment"></a>
### TASK-12.06 Adapter drop 清单与 capability 矩阵对齐

状态：in_progress

目标：

```text
把 adapter dropUnsupported 的静态清单与 Phase 11 CAPABILITY_MATRIX 数据沉淀为 model_capabilities 初始种子，
彻底消除"代码里 drop 但 capability 表未声明 unsupported"或反之的 doc/code drift。
```

计划实现：

1. 扫描 `core/adapter/openai/deepseek/drop.go`、`core/adapter/anthropic/deepseek/drop.go` 与 `phase-11-openai-responses-api/CAPABILITY_MATRIX.md`，整理为初始种子（CSV/SQL）。
2. 落入 `migrations` seed（仅 `source=adapter_seed`），后续 admin 编辑覆盖。
3. 在 capability 闸门通过的前提下，adapter 仍保留 `dropUnsupported` 作为 defense-in-depth：但任何被闸门放行的字段不应再被 adapter Drop；如果出现这种"闸门说支持、adapter 又 Drop"，必须有 test 报错。
4. 修正 [GAP-11-010](../../production/TODO_REGISTER.md#gap-11-010)（`reasoning_effort` doc/code drift）：把验证结论写入 `model_capabilities`（DeepSeek Reasoner 是否真正消费 `reasoning_effort`、允许枚举值），关闭 GAP-11-010。
5. 单元测试：对 DeepSeek 两个协议入口的所有 model 跑"capability 闸门 vs adapter drop 清单"一致性 assertion。

依赖：[TASK-12.01](#task-12-01-capability-schema)、阶段 11 完成 [TASK-11.09](../phase-11-openai-responses-api/PLAN.md#task-11-09-deepseek-upstream)。

关联 GAP：[GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007)

落地说明（实现修正）：

- **种子形态改为“adapter 同源画像 + drift 守护”，不落 migrations seed。** 原计划第 2 步“落入 `migrations` seed”与 migration 规则冲突（表 migration 只能建表/约束/索引，不塞数据），且当前没有 migration runner、也没有模型 provisioning（`model_capabilities` 依赖 DeepSeek `model_id` 行存在，归阶段 13 admin）。因此把 adapter 能力事实声明为代码层 `CapabilityProfile()`，与 `dropUnsupported` 同源，作为 `source=adapter_seed` 的唯一事实来源。
- **已交付**：
  - `core/capability/seed.go`：`Declaration` / `AdapterProfile` / `AdapterProfile.Validate()` / `MaterializeAdapterSeed`（按 `model_id` 幂等 upsert，`source=adapter_seed`，admin/models.dev 后续覆盖）。
  - `core/adapter/openai/deepseek/capability_profile.go` 与 `core/adapter/anthropic/deepseek/capability_profile.go`：两协议 adapter 各自的能力画像（unsupported=出站 Drop、limited=Adapt、full=透传）。
  - 两个 `capability_profile_test.go`：drift 守护——对每个 Drop 可观测能力构造探针请求跑 `dropUnsupported`，断言“声明级别 ⟺ 实际处置”；并强制 unsupported/limited 必须有探针证明（防止只声明不验证）。对应第 3、5 步。
  - GAP-11-010 结论沉淀：`reasoning.effort = limited`，limits `{high,max}`（对应第 4 步；GAP-11-010 本就 `done`，此处只做沉淀）。
- **范围边界（本任务不做）**：把画像物化进真实 `model_capabilities` 行的 loader / `worker-server` 子命令（需要 channel/model 拓扑枚举），随模型 provisioning（阶段 13 admin）或 enforce 切换（[TASK-12.08](#task-12-08-backfill-and-migration)）落地。`MaterializeAdapterSeed` 已就位，届时直接调用。
- **粒度结论**：DeepSeek `dropUnsupported` 是 provider/adapter 级（不看具体 model），故画像按 `provider × protocol` 声明；per-model 差异（如 deepseek-chat 是否消费 reasoning）属 models.dev/manual 来源，不在 adapter_seed 范围。

<a id="task-12-07-observability-audit"></a>
### TASK-12.07 Observability 与 audit

状态：done

> 落地说明（闸门可观测 + 审计列已交付）：
> - **metric（计划步骤 1，已交付 3 个闸门指标）**：`unio_gateway_capability_check_total{protocol,result}`、`unio_gateway_capability_required_total{protocol,capability}`、`unio_gateway_capability_missing_total{protocol,capability,scope=model|channel}`（`metrics.go` + `capabilitygate.Checker` 发射）。`unio_worker_model_catalog_sync_total` 同步指标归 [GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)（当前结构化告警日志）。
> - **structured log（计划步骤 2）**：`capabilitygate.Checker.log` 写 `ingress_protocol`/`model_db_id`/`candidate_channels`/`capability_result`/`required_capabilities`/`missing_model|channel_capabilities`；would-be 拒绝记 Warn 供观察期复核，其余 Debug；不带敏感数据（无 channel 凭据/上游身份）。
> - **审计列（计划步骤 3，本次交付）**：`request_records.capability_check_result TEXT`（[000009](../../../migrations/000009_create_request_records.up.sql)，`CHECK IN (ok/model_unavailable/channel_unavailable/unprovisioned/no_required/error)` 或 NULL=bypassed）+ sqlc `MarkRequestCapabilityCheckResult` + `requestlog.Store.SetCapabilityCheckResult`（与状态机解耦的纯审计写，best-effort）+ `lifecycle.RecordCapabilityResult`（`PlanChat` 成功后写，observation 为 nil 时保持 NULL）；`request_attempts.required_capabilities TEXT[]` 已在 TASK-12.03 持久化。`store_test.go` DB 往返覆盖创建 NULL → 写入回读 → CHECK 拒绝未知值。
> - **账务分离（计划步骤 4）**：cap-tag 与判定结论只进 metric / log / 审计列，绝不写 ledger / cost_snapshot。
> - **残留**：sync 指标 + cap-tag schema 漂移 / 闸门 5xx 告警（计划步骤 5 部分）随同步指标归 [GAP-12-008](../../production/TODO_REGISTER.md#gap-12-008) 残留项 / [GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)。

目标：

```text
让能力闸门的命中、拒绝、limits 不一致都可观测、可审计。
```

计划实现：

1. metrics（沿用 Phase 8 Prometheus 命名规范）：
   - `unio_gateway_capability_check_total{protocol, result}`
   - `unio_gateway_capability_required_total{protocol, capability}`
   - `unio_gateway_capability_missing_total{protocol, capability, scope=model|channel}`
   - `unio_worker_model_catalog_sync_total{source, result}`
2. structured log fields：`required_capabilities`、`missing_capabilities`、`override_applied`（不带敏感数据）。
3. `request_records` 写入 `capability_check_result` 列（success / model_unavailable / channel_unavailable / bypassed）；`request_attempts` 写入 `required_capabilities`（数组）作为审计。
4. 不把 cap-tag 数据写入 ledger / cost_snapshot（与账务事实分离）。
5. 告警：sync 连续失败 / cap-tag 注册表 schema 漂移 / capability 闸门 5xx。

依赖：[TASK-12.03](#task-12-03-capability-filter)、[TASK-12.04](#task-12-04-models-dev-sync)。

关联 GAP：[GAP-12-008](../../production/TODO_REGISTER.md#gap-12-008)

<a id="task-12-08-backfill-and-migration"></a>
### TASK-12.08 已上线模型回填与灰度迁移

状态：in_progress

> 落地说明（observe/enforce 代码就位，默认 observe；实际切 enforce + 观察期受控 deferred 阶段 13）：
> - **已交付（enforce 代码 + 三协议错误渲染，计划步骤 1A/1B 机制 + 步骤 2）**：
>   - config `CapabilityConfig`（`CAPABILITY_ENFORCE_OPENAI_CHAT` / `CAPABILITY_ENFORCE_ANTHROPIC_MESSAGES` / `CAPABILITY_ENFORCE_OPENAI_RESPONSES`，**全默认 false = observe**）。
>   - router 按 ingress 表面独立判断：`CapabilityEnforcement{OpenAIChat/AnthropicMessages/OpenAIResponses}` + `ChatRouteRequest.Operation`（`chat_completions`/`messages`/`responses`）+ `enforceCapability`（仅匹配表面开关 ON 时把 `model_unavailable`/`channel_unavailable` 升级为 sentinel + 稳定错误码，缺失能力 key 附 `missing_capabilities` field）；`SetCapabilityEnforcement` 由 bootstrap 注入。
>   - 三协议客户错误渲染：OpenAI Chat / Responses → 400 `invalid_request_error` + code `model_capability_unavailable`；Anthropic Messages → 400 `invalid_request_error`；统一渲染为「模型不支持能力 X」，列出缺失 capability key，**绝不暴露 channel 拓扑/凭据/上游身份**（`routing.MissingCapabilities` 封装 field 提取）。
>   - 测试：router enforce 决策（拒绝/放行/按表面 scope）+ chat handler capability error 渲染（列 key / 无 field 兜底）。
> - **受控 deferred 阶段 13（计划步骤 1B 切换 + 步骤 3/4）**：实际 observe→enforce 切换需 7~14 天观察期 + 按 metric/审计校准种子能力位（[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002)）+ 模型 provisioning 物化 adapter 画像（[GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007)），均依赖阶段 13 admin/provisioning；切换决策 + `DECISIONS.md` 实施日志见 [GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)。

目标：

```text
让现有运行中的请求平滑切换到新 capability 闸门，不在生产期间出现"昨天能跑今天 400"。
```

计划实现：

1. 上线分两步：
   - 步骤 A：闸门以 **观察模式**（observe）上线：检查但不拒绝，仅写 metrics + log + `capability_check_result`。
   - 步骤 B：观察 7~14 天，按 metric/audit 调整种子能力位，无误拒后切换为 **enforce 模式**。
2. 提供 config 开关 `capability.enforce_mode = true|false`，按 protocol 级别独立可控（先 OpenAI Chat 再 Anthropic 再 Responses）。
3. enforce 切换日记入 `docs/production/DECISIONS.md` 实施记录。
4. release blocker 联动：enforce 切换前必须确认 [GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007) 已关闭。

依赖：TASK-12.03 / TASK-12.06 / TASK-12.07。

关联 GAP：[GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)

## 实现顺序建议

```text
TASK-12.01 schema
   ↓
TASK-12.02 inference   ←  纯函数，可与 TASK-12.04 并行
TASK-12.04 models.dev sync
   ↓
TASK-12.06 adapter seed 回填
   ↓
TASK-12.03 routing gate（observe 模式）
   ↓
TASK-12.05 /v1/models 扩展
TASK-12.07 metrics + audit
   ↓
TASK-12.08 灰度切 enforce
```

## 非目标

阶段 12 不做：

1. 后台 admin UI（归阶段 13）。
2. 跨 provider 拼接（如客户在 DeepSeek 上请求图片，Unio 转去 OpenAI dall-e）。明确拒绝；Unio 不做能力 sub-routing。
3. 客户级 capability quota（限制某 user 不能用 image.output）；归账务 / project policy。
4. 新协议族（Codex Custom / GLM 等）的 ingress 适配；归后续阶段。

## 参考

- [docs/production/DECISIONS.md#dec-015](../../production/DECISIONS.md)
- [docs/chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md](../phase-11-openai-responses-api/CAPABILITY_MATRIX.md)
- [models.dev](https://models.dev/) 数据源接口（api.json / models.json / catalog.json / logos）：见 [docs/datasources/MODELS_DEV_API.md](../../datasources/MODELS_DEV_API.md)
- 业界对照：OpenRouter `/api/v1/models` `architecture.modality` + `pricing` + `top_provider`
