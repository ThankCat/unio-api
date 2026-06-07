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

状态：planned

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

状态：planned

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

状态：planned

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

关联 GAP：[GAP-12-004](../../production/TODO_REGISTER.md#gap-12-004)、[GAP-12-005](../../production/TODO_REGISTER.md#gap-12-005)

<a id="task-12-05-public-capability-surface"></a>
### TASK-12.05 Public capability surface（/v1/models 与 /console/v1/models）

状态：planned

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

状态：planned

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

<a id="task-12-07-observability-audit"></a>
### TASK-12.07 Observability 与 audit

状态：planned

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

状态：planned

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
