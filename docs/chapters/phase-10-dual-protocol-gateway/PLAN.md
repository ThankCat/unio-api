# Phase 10 Plan - 双协议 Gateway 全链路改造

## 目标

把 Unio Gateway 从“OpenAI Chat Completions 单协议实现”升级为“OpenAI Chat Completions + Anthropic Messages 双协议公开入口”，并让 DeepSeek 作为第一个同时提供两套上游协议的 provider 完成全链路验收。

本阶段不是 MVP，也不是只补一条 Anthropic handler。目标是完成可长期扩展的商业网关边界：

```text
OpenAI 客户请求
→ gatewayapi/openai
→ service/gateway/openai
→ 共享 lifecycle
→ routing 选择 protocol=openai 的 channel
→ adapter/openai/deepseek
→ DeepSeek OpenAI endpoint
→ adapter/openai/deepseek 响应翻译
→ 共享 ResponseFacts 进入审计、settlement、recovery
→ OpenAI 原生响应返回客户

Anthropic 客户请求
→ gatewayapi/anthropic
→ service/gateway/anthropic
→ 共享 lifecycle
→ routing 选择 protocol=anthropic 的 channel
→ adapter/anthropic/deepseek
→ DeepSeek Anthropic endpoint
→ adapter/anthropic/deepseek 响应翻译
→ 共享 ResponseFacts 进入审计、settlement、recovery
→ Anthropic 原生响应返回客户
```

本阶段的“全量”指两个对话 endpoint 的协议字段面完整：HTTP DTO 能识别，
校验能定位，provider mapping 能明确 `Pass`、`Adapt` 或 `Drop`（见
[DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)）。
它不表示本阶段扩展图片、视频、音频、文件等模型能力。
这些能力字段必须被 typed 并进入 ingress DTO；provider 无法转换时在 adapter
出站 **Drop**（不写入 upstream body），ingress 不因 provider 能力而 400。

模型是否支持某类输入、输出和工具能力，后续模型契约应参考
[models.dev API](https://models.dev/api.json) 这类可更新模型能力数据，并结合
provider 官方文档和黑盒验收落库。本阶段在 mapping 中登记 Drop 行为，不实现
完整模型能力管理系统。

## 非目标

本阶段只对以下两个公开操作做全量协议改造：

```text
POST /v1/chat/completions   OpenAI Chat Completions Create
POST /v1/messages           Anthropic Messages Create
```

以下能力不在本阶段内：

1. OpenAI Responses API。
2. Anthropic Message Batches、Files、Token Counting 等其他 endpoint。
3. Gemini 协议。
4. OpenAI ingress 自动 fallback 到 Anthropic upstream，或 Anthropic ingress 自动 fallback 到 OpenAI upstream。
5. 依赖官方 Go SDK 发送上游请求。
6. 图片、视频、音频、文件等多模态模型能力扩展；相关字段只做识别、校验和
   明确 Reject/映射，不在本阶段承诺端到端支持。
7. 完整模型能力管理系统；模型能力来源后续以 `models.dev/api.json`、provider
   官方文档和黑盒验收为输入。

“不在本阶段”不等于允许静默丢字段。本阶段两个目标 endpoint 的请求、非流式响应、流式响应、错误响应、usage 和 DeepSeek 双协议转换必须完整设计、实现和验收。

## 章节文档

| 文档 | 作用 |
| --- | --- |
| [ARCHITECTURE.md](ARCHITECTURE.md) | 双协议目录、依赖方向、接口边界、routing、retry、stream 和错误处理。 |
| [RESPONSE_FACTS.md](RESPONSE_FACTS.md) | 协议响应与统一账务审计事实的双轨模型。 |
| [OPENAI_CHAT_COMPLETIONS_MATRIX.md](OPENAI_CHAT_COMPLETIONS_MATRIX.md) | OpenAI Chat Completions 全量字段清单与实现策略。 |
| [ANTHROPIC_MESSAGES_MATRIX.md](ANTHROPIC_MESSAGES_MATRIX.md) | Anthropic Messages 全量字段清单与实现策略。 |
| [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md) | OpenAI 客户协议与 DeepSeek OpenAI endpoint 的请求、响应、stream 和错误转换。 |
| [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md) | Anthropic 客户协议与 DeepSeek Anthropic endpoint 的请求、响应、stream 和错误转换。 |
| [ACCEPTANCE.md](ACCEPTANCE.md) | 阶段完成标准。 |
| [STATUS.md](STATUS.md) | 当前任务状态。 |

协议原始参考：

| 协议 | 本地参考 |
| --- | --- |
| OpenAI Chat Completions Create | [docs/protocol/openai_chat_completion.md](../../protocol/openai_chat_completion.md) |
| Anthropic Messages Create | [docs/protocol/anthropic_message.md](../../protocol/anthropic_message.md) |

## 必须遵守的设计原则

1. `openai` 和 `anthropic` 是两个独立公开协议族，不共享公开 DTO。
2. DeepSeek 同时提供两套协议时，在两个协议族下各实现一个薄 adapter。
3. API key 身份、request record、routing、authorization、fallback 生命周期、settlement、metrics、tracing 和 recovery 复用共享 lifecycle。
4. 客户响应保留协议原生结构；账务和审计统一消费 `ResponseFacts`。
5. `ResponseFacts` 与客户响应必须在同一次上游解析中生成，不能在 billing 中二次猜测。
6. adapter 一次调用只允许发起一次真实上游 HTTP 请求。retry 和 fallback 归 lifecycle 管理，每次真实调用都必须有 `request_attempt`。
7. 默认只允许同协议 routing 和 fallback。跨协议桥接必须未来单独设计兼容矩阵，不得隐式发生。
8. 协议为先（DEC-012）：ingress 禁止 decode 丢字段；provider 出站只允许 Pass/Adapt
   写入 upstream，无法转换则 Drop 并记内部审计；禁止 Extensions 无脑 merge 进 upstream。
9. 不使用 OpenAI 或 Anthropic 官方 Go SDK。adapter 自己维护 wire DTO、HTTP 请求、响应解析和 SSE 翻译。
10. 共享代码只提取稳定能力，不新建 `common`、`util`、`helper` 包。

## 目标目录

`cmd/gateway-server` 保持一个进程入口。OpenAI 与 Anthropic 的分类发生在
`gatewayapi`、`service/gateway` 和 `core/adapter` 内部，不拆成两个公网进程。

```text
internal/
├── app/gatewayapi/
│   ├── router.go
│   ├── middleware/
│   ├── openai/
│   │   ├── chatcompletions/
│   │   │   ├── handler.go
│   │   │   ├── dto.go
│   │   │   ├── decode.go
│   │   │   ├── validation.go
│   │   │   └── stream.go
│   │   ├── models/
│   │   └── response.go
│   └── anthropic/
│       ├── messages/
│       │   ├── handler.go
│       │   ├── dto.go
│       │   ├── decode.go
│       │   ├── validation.go
│       │   └── stream.go
│       └── response.go
│
├── service/gateway/
│   ├── lifecycle/
│   │   ├── executor.go
│   │   ├── authorization.go
│   │   ├── attempt.go
│   │   ├── fallback.go
│   │   ├── settlement.go
│   │   ├── recovery.go
│   │   ├── delivery.go
│   │   ├── metrics.go
│   │   └── tracing.go
│   ├── openai/
│   │   └── chatcompletions/
│   └── anthropic/
│       └── messages/
│
├── core/
│   ├── adapter/
│   │   ├── facts.go
│   │   ├── registry.go
│   │   ├── upstream_error.go
│   │   ├── upstreamhttp/
│   │   │   ├── client.go
│   │   │   └── response.go
│   │   ├── sse/
│   │   ├── openai/
│   │   │   ├── contract.go
│   │   │   ├── dto.go
│   │   │   ├── request.go
│   │   │   ├── response.go
│   │   │   ├── stream.go
│   │   │   └── deepseek/
│   │   │       ├── adapter.go
│   │   │       ├── request.go
│   │   │       ├── response.go
│   │   │       ├── stream.go
│   │   │       ├── error.go
│   │   │       └── tokenizer.go
│   │   └── anthropic/
│   │       ├── contract.go
│   │       ├── dto.go
│   │       ├── request.go
│   │       ├── response.go
│   │       ├── stream.go
│   │       └── deepseek/
│   │           ├── adapter.go
│   │           ├── request.go
│   │           ├── response.go
│   │           ├── stream.go
│   │           ├── error.go
│   │           └── tokenizer.go
│   └── usage/
│       └── facts.go
```

目录规则：

1. `adapter/openai` 和 `adapter/anthropic` 表达协议族。
2. `adapter/openai` 和 `adapter/anthropic` 根目录放协议族内稳定可复用的 request、response 与 stream 基础逻辑。
3. `adapter/openai/deepseek` 和 `adapter/anthropic/deepseek` 表达 provider 在对应协议族下的具体实现，复用根目录能力并显式实现 DeepSeek 差异。
4. 原 `adapter/openai/streamtranslate` 不作为长期包：baseline 流式翻译内联进 `adapter/openai` 根目录的 `openai.Adapter`；provider 专属流式差异（若有）才进 `adapter/openai/<provider>/stream.go`。DeepSeek 当前无专属差异，故包已移除且未新建 `deepseek/stream.go`。
5. `adapter/upstreamhttp` 只共享 outbound HTTP primitive，不做协议转换、不做 retry、不解析 provider 业务语义。
6. 现有 `adapter/sse` 保留为协议无关 SSE reader。
7. `service/gateway/lifecycle` 只做共享生命周期，不 import `app/gatewayapi` DTO。
8. `service/gateway/openai` 与 `service/gateway/anthropic` 各自持有 typed DTO 映射，不通过 `any` 复用。
9. 两个 `deepseek/tokenizer.go` 都是 authorization 调用上游前使用的内部输入 token
   估算能力，不新增公网 tokenizer endpoint；OpenAI 与 Anthropic 必须分别实现，
   估算输入、字段校验和返回语义必须与各自协议 wire 保持一致。
10. 旧 `adapter.ChatInputTokenizer` 的 OpenAI 偏向 DTO 不继续作为双协议根接口；
    OpenAI 与 Anthropic 在各自协议族根目录定义 typed tokenizer contract。
11. 不定义 `FullChatAdapter`、`FullMessagesAdapter` 等强制组合接口；三个 capability
    分别注册，具体 provider 分别做编译期断言。
12. 本阶段不抽取共享 DeepSeek tokenizer facade，也不建立跨协议 tokenizer 中间 DTO。
    如果两个实现稳定后确认底层纯文本编码 primitive 完全一致，只允许提取不感知
    OpenAI messages、Anthropic content blocks 和 wire framing 的窄工具。
13. DeepSeek 双协议 tokenizer 直接进入对应 adapter 实现与验收，不新增独立
    playground 前置任务。

## 数据模型改造

本阶段需要同步改造数据库事实，不允许只改 handler 和 adapter。

### Channel 与 routing

`providers.adapter` 不能表达 DeepSeek 同时提供两套协议。目标模型：

```text
providers
  slug = deepseek

channels
  protocol    = openai | anthropic
  adapter_key = deepseek
```

示例：

| provider | channel | protocol | adapter_key | base_url |
| --- | --- | --- | --- | --- |
| `deepseek` | `deepseek-openai-primary` | `openai` | `deepseek` | `https://api.deepseek.com` |
| `deepseek` | `deepseek-anthropic-primary` | `anthropic` | `deepseek` | `https://api.deepseek.com/anthropic` |

迁移决策：

1. 开发期直接修改源 migration，把 runtime adapter 绑定迁移到 `channels.adapter_key`。
2. `providers.adapter` 从 runtime 路由、adapter registry、preflight、sqlc query 和
   bootstrap seed 中移除；代码不得继续读取它。
3. `providers` 只保留 provider 业务身份字段，例如 `slug`、`name`、`status`。
4. 如果为了兼容本地旧库短期保留字段，也必须标记为 deprecated，并在 Phase 10
   关闭前删除，不允许作为正式 schema 留存。
5. `channel.Runtime.AdapterKey` 必须来自 `channels.adapter_key`，不能从 provider 派生。

`routing` 必须接收 ingress protocol，并且只返回同协议 candidate。

术语约定：

```text
routing candidate
= routing SQL 按数据库事实返回的同协议候选，只保证 model、channel、status、
  project policy、protocol 等数据库条件成立。

attempt candidate / fallback plan
= lifecycle 在 routing candidate 之上继续过滤 registry capability、熔断和
  authorization 后形成的真实可尝试计划。
```

SQL 不感知 Go registry，也不负责判断某个 adapter 是否实现 non-stream、stream
或 input tokenizer；这些能力过滤属于 lifecycle。

### Request 与 attempt 审计

`request_records` 至少增加：

```text
ingress_protocol
operation
response_protocol
response_id
delivery_status
response_started_at
response_completed_at
```

`request_attempts` 至少增加：

```text
upstream_protocol
upstream_response_id
upstream_finish_reason
finish_class
response_started_at
final_usage_received
usage_mapping_version
```

必须区分：

```text
upstream 是否成功
settlement 是否成功
客户响应是否完整送达
```

流式请求存在“上游已完成且有可靠 usage，但客户端中途断开”的场景。此时可以结算，但 `delivery_status` 必须记录为 `interrupted`，不能把交付结果和账务结果揉成一个状态。

### Usage、价格与 recovery

当前 `prompt_tokens / completion_tokens / cached_tokens / reasoning_tokens` 偏 OpenAI，需要升级为协议中立事实。详细规则见 [RESPONSE_FACTS.md](RESPONSE_FACTS.md)。

至少覆盖：

```text
uncached_input_tokens
cache_read_input_tokens
cache_write_5m_input_tokens
cache_write_1h_input_tokens
output_tokens_total
reasoning_output_tokens
server_tool_usage
usage_source
usage_mapping_version
```

对应改造范围：

```text
usage_records
prices
price_snapshots
channel_cost_prices
cost_snapshots
settlement_recovery_jobs
```

recovery job 必须持久化不可变 `ResponseFacts`，worker 不允许重新解析原始 response body。

## 分节实施计划

每一节只完成一个稳定边界。结构迁移、协议实现和数据库语义升级不能混成一次大改。

<a id="task-10-01-dual-protocol-adr"></a>
### TASK-10.01 双协议 ADR 与范围冻结

状态：done（2026-06-01）

目标：

```text
用 DEC-010 / DEC-011 固化双协议、统一事实、无 SDK、lifecycle 管 retry 的边界。
```

实现内容：

1. 将原 OpenAI-first 决策标记为被双协议决策 superseded。
2. 固化两个公开操作、两个协议族、同协议 routing 原则。
3. 固化“协议响应原生返回，账务审计统一事实结算”。
4. 固化 adapter 单次调用只发一次上游请求。

验收：

1. [ARCHITECTURE.md](ARCHITECTURE.md) 与 [RESPONSE_FACTS.md](RESPONSE_FACTS.md) 完成 review。
2. 不存在 `openai-compatible` 作为协议族包名。

<a id="task-10-02-directory-migration"></a>
### TASK-10.02 目录迁移与依赖方向整理

状态：partial（2026-06-01；adapter 层与 gatewayapi 层完成，service 层迁移与 lifecycle 抽取重排到 TASK-10.05）

目标：

```text
先完成不改变行为的包迁移，让 OpenAI 旧链路进入新目录。
```

实现内容：

1. 【done】将 `gatewayapi` 的 OpenAI handler、DTO、decode、validation、stream 和 models 迁入 `gatewayapi/openai`，并新增 `NewChatCompletionsHandler`/`NewModelsHandler` 导出构造函数；router 与 middleware 留根并改用构造函数装配。
2. 【done】将 OpenAI 编排整包迁入 `service/gateway/openai/chatcompletions`（package `chatcompletions`，behavior 不变；bootstrap 以别名 `gateway` 导入新路径）。
3. 【顺延 Anthropic（TASK-10.09）】抽出协议无关 `service/gateway/lifecycle`。
4. 【done】将通用根包中实际属于 OpenAI 的 `ChatRequest / ChatResponse / ChatStreamChunk`（连同 `ChatMessage`、`ChatAdapter`/`StreamChatAdapter` 接口、tools、`ChatInputTokenizer`、单键 `Registry`）迁入 `adapter/openai`；`ChatUsage` 与 `upstream_error.go` 暂留 `adapter` 根包以避免循环依赖。
5. 【done（TASK-10.07 Lesson 4）】baseline 流式翻译内联进 `openai.Adapter`，移除整个 `streamtranslate` 包（DeepSeek 无专属流式差异，不新建空壳 `deepseek/stream.go`）。
6. 【done】保持 OpenAI 现有行为不变，通过原有测试。

重新排序说明：

```text
item 2（整包迁入 chatcompletions）已作为行为不变迁移完成，与 item 1/4 一起补齐三层协议分离。
item 3（抽出协议无关 lifecycle）推迟到 Anthropic（TASK-10.09）一起做：在 OpenAI 单协议阶段
抽“共享 lifecycle”是投机抽象；有了第二个协议后再抽，才有真实复用对象，且能与 settlement
改吃 facts 一次到位。settlement/billing/recovery 改吃 facts（TASK-10.04 item 6/7）同样随 Anthropic 落地，
因为对 OpenAI 而言 facts 由 ChatUsage 派生、金额不变，现在改是纯风险、零即时收益。
item 5 依赖 TASK-10.07 的 DeepSeek provider adapter 落点，合并到 TASK-10.07。
```

约束：

1. 不在本节引入 Anthropic。
2. 不在本节顺手重写 billing 公式。
3. 不创建 `common`、`util`、`helper`。

验收：

```bash
go test ./...
go vet ./...
git diff --check
```

<a id="task-10-03-channel-protocol-routing"></a>
### TASK-10.03 Channel protocol 与 adapter registry

状态：done（2026-06-02；schema/routing/preflight、registry facade 与 executor capability 消费已完成）

目标：

```text
让 routing 能表达 provider 的多协议 channel，并让 registry 按协议族解析具体 adapter。
```

分项状态：

```text
【done】1  channels 增加 protocol 与 adapter_key（源 migration 直接改，本地 down/up 验证）。
【done】2  删除 providers.adapter runtime 职责；migration、sqlc query、preflight、seed 改用 channels.adapter_key。
【done】3  routing 输入增加 ingress protocol（ChatRouteRequest.IngressProtocol），OpenAI 链路传 routing.ProtocolOpenAI。
【done】4  FindRouteCandidates 只按 channel.protocol 选择同协议候选，并改用 channels.adapter_key；SQL 不感知 Go registry。
【done】10 channel.Runtime 仍只携带 runtime 参数；adapter_key 由 routing.ChatRouteCandidate 承载，不进 Runtime。
【done】5 registry facade 使用复合键 (protocol, adapter_key)，openai/anthropic 分别登记 non_stream/stream/input_tokenizer 三能力。
【done】11 启动 preflight 改为按启用 channel 的 (protocol, adapter_key) 校验 registry 绑定存在；不强制一个 channel 实现全部能力。
【done，TASK-10.10B-2a】6 lifecycle executor 已消费保持 SQL 顺序的 capability 过滤结果，并生成最终 fallback plan。
【done，TASK-10.10B-2a】7/8/9 openai/anthropic 分协议 tokenizer 接口、DeepSeek 双 tokenizer、候选级估算 closure 已落地；OpenAI tokenizer 消费完整 `openai.ChatRequest` wire，包含 tools。
```

落地说明：`service/gateway/lifecycle.AdapterRegistry` 持有 OpenAI 与 Anthropic 两套协议族
registry，统一按 `(protocol, adapter_key)` 查询代码能力。SQL 仍只按数据库字段筛选同协议
候选；共享 executor 在 authorization 前消费 facade 的 capability 过滤结果。

实现内容：

1. `channels` 增加 `protocol` 与 `adapter_key`。
2. 删除 `providers.adapter` 的 runtime 职责；开发期源 migration、sqlc query、
   bootstrap seed 和 preflight 均改为使用 `channels.adapter_key`。
3. `routing` 输入增加 ingress protocol。
4. routing SQL 只按数据库字段选择同协议 channel；SQL 不感知 Go registry。
5. registry 支持 `(protocol, adapter_key)`，并分别登记 non-stream、stream 与 input tokenizer capability。
6. provider 可以只实现非流式或流式接口，也可以同时实现；lifecycle 在 SQL routing 后按内存 registry 过滤缺少本次 capability 的 channel。
7. `openai.ChatInputTokenizer` 消费 OpenAI DTO，`anthropic.MessagesInputTokenizer`
   消费 Anthropic DTO；不复用旧的 OpenAI 偏向 `adapter.ChatInputTokenizer`。
8. 同一个 provider 的不同协议入口分别实现 tokenizer；DeepSeek 对应
   `adapter/openai/deepseek/tokenizer.go` 与 `adapter/anthropic/deepseek/tokenizer.go`。
9. 协议 service 向 lifecycle 提供候选级 token 估算 closure；lifecycle 不接触协议 DTO。
10. `channel.Runtime` 只携带 adapter 调用所需 runtime 参数，不读取 DB/env。
11. 启动 preflight 校验 channel 协议与 adapter registry 一致。

验收：

1. 同一个 DeepSeek provider 可配置 OpenAI 与 Anthropic 两个 channel。
2. OpenAI ingress 不会命中 Anthropic channel，反之亦然。
3. 同协议 channel retry/fallback 行为保持可审计。
4. 缺少当前 operation capability 的 channel 可以出现在 SQL routing candidate 中，
   但必须被 lifecycle registry 过滤，不能进入最终 fallback plan / attempt plan。
5. Anthropic tokenizer 不依赖 OpenAI Chat DTO。
6. DeepSeek 两套 tokenizer 没有共享 provider tokenizer facade 或跨协议中间 DTO。

<a id="task-10-04-response-facts-schema"></a>
### TASK-10.04 ResponseFacts、usage 与审计 schema

状态：partial（2026-06-02；事实契约与双协议 adapter 映射完成；10.12A 已改 schema 且 OpenAI settlement/recovery 生产路径已消费 facts；10.12B 测试/sqlc 回归待补）

目标：

```text
建立协议无关、可持久化、可 recovery 的响应事实。
```

实现内容：

1. 【done】增加 `adapter.ResponseFacts` 与 `usage.Facts`（新增 `internal/core/usage` 包）。
2. 【done】`ResponseFacts.UpstreamResponseID` 与客户可见 response ID 分列建模（持久化分列随 item 6）。
3. 【done】增加 `FinishFacts`（稳定 `FinishClass` + provider 原始 `RawReason`）。
4. 【done】`TokenCount`+`CountState` 区分 known / not_applicable / unknown；`BillableValue` 保证 unknown 不被当 0。
5. 【done】`usage.Facts` 覆盖 cache read、cache write 5m/1h、inclusive output、reasoning output 与 server tool usage（`MeteredItem`/`MeteredKind`）。OpenAI 侧 `ChatUsage.ToUsageFacts()` 已实现并单测。
6. 【done，10.12A】扩展 request、attempt、usage、price snapshot、cost snapshot、recovery job schema + `usage_line_items`（源 migration 已改）。
7. 【done，10.12A，OpenAI 路径】settlement 与 recovery 改为只消费不可变 `ResponseFacts`/`usage.Facts`（`chat_settlement*.go` + `billing`）；Anthropic 公开链路与 recovery worker 全量回归在 10.12B/10.10B-2b。

重排理由：item 1~5 是协议无关契约，可独立定义并单测，是 TASK-10.05 lifecycle 与 TASK-10.06/10.07/10.09 adapter 产出 facts 的前置。双协议 adapter 已能产出 `ResponseFacts`，10.10B-2a 也证明 Anthropic handler 不能临时复用 OpenAI 偏向的结算事实；因此 item 6（schema 列）与 item 7（settlement/recovery 消费 facts）在 TASK-10.12A 一次收口。

验收：

1. OpenAI 与 Anthropic usage 都能映射到统一 facts。
2. recovery worker 不重新解析 response body。
3. billing 公式不会重复收费或把未知值当作 0。

<a id="task-10-05-lifecycle-executor"></a>
### TASK-10.05 共享 Lifecycle Executor

状态：done（2026-06-02；架构 B 终局——四批叶子抽取 + settlement/recovery 迁入 + 编排骨架 thin wrapper 收口完毕；OpenAI/Anthropic 两侧 service 现在共享一份 `lifecycle.RequestLifecycle` 协议无关基础设施实现）

目标：

```text
抽出协议无关的完整商业生命周期，避免 OpenAI 和 Anthropic 各复制一套账务逻辑。
```

进度：

1. 【done，10.10B-2a】`lifecycle.AdapterRegistry` + `Executor.PrepareCandidates`；OpenAI 非流式/流式已接入。
2. 【done，10.10B-2b 第四批】`chat_settlement` / `chat_settlement_recovery` 只消费 `ResponseFacts`，并已整体迁入 `lifecycle` 包（`settlement.go`/`settlement_recovery.go`），OpenAI 与 Anthropic 共用同一份账务收口。
3. 【done，10.10B-2b：架构 B 分阶段】先抽协议无关叶子组件到 `lifecycle` 共享、编排骨架暂复制、后续再泛型化（2026-06-02 用户拍板）。四批叶子抽取全部完成。
   - **叶子抽取第一批（done）**：retry classifier 抽到 `lifecycle/retry.go`（`RetryClassifier` 接口 + `NeverRetryClassifier` + `ProviderErrorClassifier`），测试移至 `lifecycle/retry_test.go`；`chatcompletions` 删除本地接口与 `chat_retry_classifier.go`，改用 `lifecycle.RetryClassifier`；`bootstrap` 与相关测试改引用 `lifecycle.ProviderErrorClassifier`。全绿。
   - **叶子抽取第二批（done）**：channel breaker 抽到 `lifecycle/breaker.go`（`ChannelBreaker` 接口 + `ChannelCircuitBreaker` + `ChannelCircuitBreakerConfig` + `IsChannelFaultError`）；`chatcompletions/channel_breaker.go` 只留 service 方法（`candidateAvailable`/`breakerAllow`/`recordChannelHealth`，后者调 `lifecycle.IsChannelFaultError`）；`service.go` 字段/参数改 `lifecycle.ChannelBreaker`；测试拆分——纯熔断器单测 + `IsChannelFaultError` → `lifecycle/breaker_test.go`，service 集成测试留 `chatcompletions`；`bootstrap` 改用 `lifecycle.NewChannelCircuitBreaker`。全绿。
   - **叶子抽取第三批（done）**：metrics/tracing 抽到 `lifecycle/metrics.go`（`MetricsRecorder` 接口 + `MetricsID`）与 `lifecycle/tracing.go`（`StartGatewaySpan`/`EndGatewaySpan`/`UpstreamSpanAttrs`）；`chatcompletions` 保留 `recordX` service 方法（改调 `lifecycle.MetricsID`）与 settlement 耦合部分（`settlementOutcomeFromErr`/`endSettlementSpan` 依赖 `IsChatSettlementRecoveryScheduled`）；`service.go`/`bootstrap` 字段改 `lifecycle.MetricsRecorder`；编排主体 `chat_completion.go`/`chat_stream.go` span/metricsID 调用改 `lifecycle.X`。全绿。
   - **叶子抽取第四批：账务子系统（authorization + settlement + recovery，~1700 行）——done**：设计见 [SETTLEMENT_EXTRACTION_DESIGN.md](SETTLEMENT_EXTRACTION_DESIGN.md)。账务子系统已协议无关（只消费 `ResponseFacts`/`usage.Facts`），整体移到 `lifecycle`（`RecoverableChatSettlementExecutor`、`IsChatSettlementRecoveryScheduled`）；`worker_server` 与 `gateway` bootstrap 改依赖 `lifecycle`；仅 release helper 留 chatcompletions；`Chat*` 命名保留、去前缀留 10.15。全绿。
   - **再后续骨架**：`request_record` / `candidates` 协议无关部分随编排骨架泛型化一并处理。
4. 【done，10.10B-2b】Anthropic `messages` service（编排骨架镜像 OpenAI，复用 lifecycle 叶子）+ gatewayapi handler + router `/v1/messages` + bootstrap `NewMessagesGateway` 装配；service 编排单测 + handler 集成测试 + `messages_parity_e2e_test.go`（真实 adapter↔mock 上游全链路 parity）全绿。
5. 【done，2026-06-02 架构 B 终局——thin wrapper 复制清单收口】抽 `lifecycle.RequestLifecycle` struct（`internal/service/gateway/lifecycle/request_lifecycle.go`，~340 行），集中承载协议无关基础设施 + 协议常量 + 协议族 ad-hoc string code 文案闭包：
   - 字段：`requestLog`/`authorizer`/`metrics`/`breaker`/`ingressProtocol`/`operation`/`safeMessage`。
   - 方法：`CandidateAvailable` / `BreakerAllow` / `RecordChannelHealth`（channel breaker）；`ReleaseAuthorization` / `ReleaseAuthorizationForBillingException`（脱离客户端取消 ctx + 5s 补偿窗口）；`RecordRequest` / `RecordRoutingSelected` / `RecordUpstream` / `RecordSettlement` / `RecordStreamEvent`（metrics 上报）；`CreateRequest` / `CreateAttempt` / `MarkRequestFailed` / `MarkAttemptFailed` / `MarkRequestCanceled`（request log 创建/推进/失败/取消，统一写 ingress 协议 + operation + 错误三元 facts）。
   - 协议无关 nil-safe 语义：纯读判定与指标 record 系列方法支持 nil receiver（等价于「未启用熔断/不采集指标」，no-op 或全部放行），与抽取前 `breaker == nil` / `metrics == nil` 守护行为一致。
   - `SafeMessage` 闭包合约：返回非空表示命中协议族 ad-hoc 文案；返回空串表示「未识别此 code」，由 `safeMessageFor` 兜底到 `BaseSafeRequestLogErrorMessage`，避免协议族复制协议无关兜底表。
   - 同步 hoist `lifecycle.SettlementOutcomeFromErr`（原 `settlementOutcomeFromErr` 包级函数两侧逐字相同），两侧 service 改引用 `lifecycle.SettlementOutcomeFromErr`，删除本地副本。
   - 两侧 `service.go`（`ChatCompletionService` / `MessagesService`）增加 `lifecycle *lifecycle.RequestLifecycle` 字段；构造函数签名不变，内部立即用已注入的 `requestLog`/`authorizer`/`metrics`/`breaker` + 协议族常量 + 协议族 safeMessage 闭包构造一次。
   - 两侧 wrapper 文件（`channel_breaker.go` / `chat(message)_authorization.go` / `chat(message)_metrics.go` / `chat(message)_request_record.go`）改为 1-line forward 到 `s.lifecycle.X(...)`，保留协议族命名（`createMessageRequestRecord` vs `createRequestRecord` / `recordMessageRequest` vs `recordChatRequest`），编排骨架可读性 + 调用点零改动。
   - 收益：原本两侧字面相同的 ~290 行 wrapper 实现（含 channel breaker 3 法 + release 2 法 + metrics 5 法 + request log 5 法 + safeMessage 兜底）合并为单一份共享实现 + 两侧 ~100 行 forward stub；消除「两份字面相同的方法体」长期维护风险，新增协议族只需提供协议常量 + ad-hoc safeMessage 闭包即可复用。
   - 验证：`go vet ./...`（除既存 seed/ 双 main 包）+ `go build` + `go test ./internal/service/...`（lifecycle / openai/chatcompletions / anthropic/messages 全绿）+ `go test -tags=blackbox ./internal/blackbox/...`（OpenAI/Anthropic SDK blackbox 全部通过）+ `git diff --check` 干净。原 `TestRequestLogErrorFactsSeparateSafeMessageAndInternalDetail` 改为通过 `lifecycle.NewRequestLifecycle` + `MarkRequestFailed` + `fakeRequestLogService` 三段断言，行为覆盖语义不变。

承接说明：本任务额外完成原 TASK-10.02 item 2——把 OpenAI 编排迁入
`service/gateway/openai/chatcompletions`。当前 `service/gateway` 把 OpenAI 专属编排
（`chat_completion.go`、`chat_stream.go`、`chat_dto_map.go`、`service.go` 等）与协议无关
lifecycle（`chat_settlement*.go`、`chat_metrics.go`、`chat_tracing.go`、`chat_retry_classifier.go`、
`channel_breaker.go`、`chat_request_record.go`）混在一个 `gateway` 包；本任务先按
`ResponseFacts`/`usage.Facts` 边界抽出 `service/gateway/lifecycle`，再把 OpenAI 专属部分落到
`service/gateway/openai/chatcompletions`。

本任务还承接 TASK-10.03 顺延的 registry 升级：把 adapter registry 从单键 `adapter_key`
升级为 `(protocol, adapter_key)` 复合键、按 non_stream/stream/input_tokenizer 三能力分别注册，
并由 lifecycle 在 SQL routing candidate 之上做 capability 过滤与候选级保守 token 估算。
Anthropic 侧 tokenizer 与第二协议 adapter 由 TASK-10.08/10.09 提供。

TASK-10.04 顺延的 item 6/7 在 TASK-10.12A 先行收口：按真实映射形状扩展
`usage_records`、`prices`、`price_snapshots`、`channel_cost_prices`、`cost_snapshots`、
`settlement_recovery_jobs` schema 与新增 `usage_line_items`，并把 settlement 与 recovery
改为只消费不可变 facts（recovery worker 不重新解析 response body）。完成后，本任务继续
抽取共享 attempt/delivery lifecycle 并接 Anthropic service。

共享职责：

```text
API key 身份
request record
routing
channel 熔断
authorization
attempt record
retry / fallback
settlement
recovery
metrics / tracing
delivery audit
```

协议层职责：

```text
typed request
typed response
typed stream event
协议原生错误输出
adapter registry 选择
```

验收：

1. lifecycle 不 import OpenAI 或 Anthropic HTTP DTO。
2. lifecycle 不使用 `map[string]any` 传递账务事实。
3. 每次 adapter 调用前创建 attempt；adapter 内没有隐藏 retry。
4. authorization 对过滤后的同协议 fallback candidates 分别调用对应 input tokenizer，
   使用保守 token 结果冻结余额；不能只按第一个 candidate 估算。

<a id="task-10-06-openai-contract-full"></a>
### TASK-10.06 OpenAI Chat Completions 全量字段契约

状态：planned

目标：

```text
按 docs/protocol/openai_chat_completion.md 补齐公开请求、非流式响应、流式响应和错误响应。
```

实现内容：

1. 顶层请求字段全部登记、typed 或明确 Reject。
2. messages 各 role、content union、多模态、audio、file、tool、deprecated function 字段显式处理。
3. response choices、message、logprobs、annotations、audio、tool_calls、usage details 完整建模。
4. stream chunk、delta、usage 尾包和 `[DONE]` 语义完整。
5. OpenAI vendor extension 只允许登记后 passthrough。
6. 图片、视频、音频、文件等能力字段只表示协议面可识别；如果当前 provider 或
   选中模型不支持，必须在调用上游前明确 Reject。

字段清单见 [OPENAI_CHAT_COMPLETIONS_MATRIX.md](OPENAI_CHAT_COMPLETIONS_MATRIX.md)。

验收：

1. 不再把 C8 作为可长期遗漏项。
2. nested 字段同样禁止 silent drop。
3. 不把多模态字段 typed 化误写成模型能力支持。

<a id="task-10-07-deepseek-openai-adapter"></a>
### TASK-10.07 DeepSeek OpenAI adapter 全量转换

状态：done（adapter 全量 + 实时黑盒回归，2026-06-02）

承接说明：adapter 核心已对真实 `https://api.deepseek.com` 端到端验证通过，五课全部收口（详见 STATUS.md TASK-10.07）：Lesson 1 请求侧 typed 全量；Lesson 2 语义 Adapt（`function_call`→`tool_choice`、`functions`→现代 `tools`、`user`→`user_id` 校验+非法 Drop）；Lesson 3 非流式响应全量字段贯通 + `created` parity 修复；Lesson 4 删除 `streamtranslate` 过渡包并把 baseline 流式翻译内联进 `openai.Adapter`，流式 chunk/delta 全量字段贯通；**Lesson 5 黑盒回归补全**——DS-OAI-03/04（reasoner reasoning/content 分离，非流式+流式）、DS-OAI-07（logprobs+top_logprobs 实时透传）、DS-OAI-09（塞满不支持字段仍 200 + 审计 dropped 列表）、DS-OAI-14（tokenizer 校准：恒 ≥ 上游 prompt_tokens，ratio≈1.8~2.75，保守达标），全部 gate 在 `DEEPSEEK_BLACKBOX=1`。残留为可选/跨任务项：DS-OAI-05（tools 多轮）/DS-OAI-06（json_object）可选回归；DS-OAI-08（cache 命中）非确定性；DS-OAI-10/11（retry/fallback、stream tail/cancel delivery audit）归 TASK-10.05/10.10；DS-OAI-12/13 已由 drop/adapt 单测 + DS-OAI-09 覆盖。

目标：

```text
在 adapter/openai/deepseek 完成 OpenAI 客户请求 ↔ DeepSeek OpenAI endpoint 的显式转换。
```

前置条件：

1. [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md) 不允许残留 `Verify`。
2. 所有 `Verify` 必须先通过 DeepSeek 黑盒请求冻结为 `Pass`、`Adapt`、`No-op`
   或 `Reject`。
3. 未冻结前不得开始 request map、response map、stream translation、usage
   mapping 和 tokenizer 的生产代码实现。

实现内容：

1. 请求 map。
2. 非流式 response map。
3. SSE stream translation。
4. usage → `ResponseFacts`。
5. provider error → 稳定 `UpstreamError`。
6. 在 `adapter/openai/deepseek/tokenizer.go` 独立实现 OpenAI 输入 tokenizer；
   按 DeepSeek OpenAI wire 估算，不新增公网 tokenizer endpoint。
7. DeepSeek 支持、no-op、ignored、unsupported 字段矩阵。
8. 承接原 TASK-10.02 item 5（done，Lesson 4）：实测旧 `DeepSeek` translator 是 `Default` 的纯透传，
   DeepSeek 流式与 OpenAI 基线无差异，因此把 baseline 流式翻译内联进 `openai.Adapter` 并**移除整个
   `streamtranslate` 包**，而不是新建空壳 `adapter/openai/deepseek/stream.go`；同时贯通流式 chunk/choice/delta
   全量字段（created/service_tier/system_fingerprint/index/logprobs/refusal/function_call）。如未来出现
   DeepSeek 专属流式 framing，再按 provider adapter 新建 `deepseek/stream.go` 收口。

完整矩阵见 [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md)。

<a id="task-10-08-anthropic-ingress-full"></a>
### TASK-10.08 Anthropic Messages 全量字段入口

状态：done（ingress 契约层，2026-06-01）

承接说明：ingress 契约层已落地并单测（`app/gatewayapi/anthropic/messages` + `app/gatewayapi/anthropic`）：双轨 decode（`mcp_servers` Reject）、顶层字段校验、content union 结构化校验（item 4 的 content/thinking/tools/tool_choice/system 结构面）、非流式 `MessageResponse` + 强类型 `MessageUsage`（item 5）、命名 SSE 事件 DTO + 帧编码器（item 6）、共享 Anthropic error shape（item 7）。item 1 的 HTTP header（`x-api-key`/`anthropic-version`/`anthropic-beta`）校验与 item 2 身份接入随 handler 装配（TASK-10.10）。item 8 的 provider 能力级 Reject 已在 adapter 侧（TASK-10.09）实现。

目标：

```text
增加 POST /v1/messages，保持 Anthropic SDK 可识别的原生协议形状。
```

实现内容：

1. 支持 `x-api-key`、`anthropic-version` 和登记后的 `anthropic-beta`。
2. 复用现有 opaque API key 身份，不新建第二套用户身份。
3. 顶层请求字段全部 typed 或明确 Reject。
4. content block union、thinking、tools、tool choice、cache control、output config 完整建模。
5. 非流式 Message response 完整建模。
6. 原生 Anthropic SSE event 类型完整建模。
7. Anthropic 原生错误响应输出。
8. image、document、container upload 等能力 block 只表示协议面可识别；如果
   DeepSeek 或选中模型不支持，必须在 tokenizer 和上游请求前明确 Reject。

字段清单见 [ANTHROPIC_MESSAGES_MATRIX.md](ANTHROPIC_MESSAGES_MATRIX.md)。

<a id="task-10-09-deepseek-anthropic-adapter"></a>
### TASK-10.09 DeepSeek Anthropic adapter 全量转换

状态：done（adapter 核心，实时黑盒通过，2026-06-01）

承接说明：前置黑盒冻结已完成——DEEPSEEK_ANTHROPIC_MAPPING.md 所有 `Verify` 清零（§8 usage、§7 thinking、§9 delta、§4 cache_control/thinking.display、§12 错误体、新增 §14 冻结结果）。adapter 已落地并对真实 `https://api.deepseek.com/anthropic/v1/messages` 端到端验证通过：契约层 `core/adapter/anthropic`（接口 + 内部 DTO + `MessageUsage.ToUsageFacts()` + `ResponseFacts` + 三能力 `Registry` + `MessagesInputTokenizer`）、通用 base `anthropic.Adapter`（item 1 请求 map / item 2 非流式 response map / item 3 SSE 翻译 / item 4 usage + `ResponseFacts` / item 5 错误 → `UpstreamError`）、`adapter/anthropic/deepseek`（item 7 reject ignored/unsupported + item 6 保守 tokenizer）。DS-ANT-01/02/09 实时黑盒回归全绿。待办（并入 10.10/10.15）：响应 content block typed 化、server_tool_use/web_search 黑盒、DS-ANT-15 tokenizer 校准、bootstrap/preflight/routing/lifecycle 接线。

目标：

```text
在 adapter/anthropic/deepseek 完成 Anthropic 客户请求 ↔ DeepSeek Anthropic endpoint 的显式转换。
```

前置条件：

1. [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md) 不允许残留 `Verify`。
2. 所有 `Verify` 必须先通过 DeepSeek Anthropic 黑盒请求冻结为 `Pass`、`Adapt`、
   `Ignored` 或 `Reject`。
3. usage、thinking、cache、server tool usage、stream delta 和 tokenizer 相关
   语义必须先冻结，再进入 adapter 生产代码实现。

实现内容：

1. 请求 map。
2. 非流式 response map。
3. Anthropic SSE stream translation。
4. usage 累积与 `ResponseFacts`。
5. provider error → 稳定 `UpstreamError`。
6. 在 `adapter/anthropic/deepseek/tokenizer.go` 独立实现 Anthropic 输入 tokenizer；
   按 DeepSeek Anthropic wire 估算，不新增公网 tokenizer endpoint。
7. 对 DeepSeek ignored 和 unsupported 字段在调用上游前明确处理。

完整矩阵见 [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md)。

<a id="task-10-10-stream-lifecycle"></a>
### TASK-10.10 双协议 stream 生命周期

状态：done（2026-06-02）

进度：

1. 【done，10.10A】双协议 stream adapter 契约统一返回 `adapter.StreamOutcome`。
2. 【done，10.10A】OpenAI adapter 截留 upstream `[DONE]`；Anthropic adapter 截留
   upstream `message_stop`，并从 `message_start` + `message_delta` 累积可靠 usage 与
   `ResponseFacts`。
3. 【done，10.10A】可靠 usage 已到达但成功终态前断尾时返回 `facts + error`，供 lifecycle
   结算后记录 delivery interrupted。
4. 【done，10.10B】抽取共享 lifecycle executor，并完成双 registry / preflight /
   routing / handler 接线。
5. 【done，10.10B-1】新增共享 `lifecycle.AdapterRegistry` facade，按
   `(protocol, adapter_key)` 查询 non-stream / stream / input tokenizer 能力并保持 SQL
   顺序过滤候选；bootstrap 同时注册 DeepSeek 双协议 adapter，preflight 校验复合绑定；
   routing 在查库前拒绝非法 ingress protocol。
6. 【done，10.10B-2a】新增共享候选准备 executor：authorization 前按 capability、只读
   熔断可用性和候选级估算 closure 生成 fallback plan；OpenAI 非流式/流式已接入，
   对可用 fallback candidates 取最大输入估算并冻结一次。
7. 【done，10.12A → 10.12B】schema 与 OpenAI settlement/recovery 生产路径已消费 immutable
   `ResponseFacts`；测试/sqlc/账务回归全绿。
8. 【done，10.10B-2b】共享 lifecycle 叶子（retry/breaker/metrics/tracing/authorization/settlement/recovery）已抽到 `lifecycle`，Anthropic Messages service (`service/gateway/anthropic/messages`) 复用这些叶子完整编排非流式/流式请求，app handler + `/v1/messages` 路由 + bootstrap `NewMessagesGateway` 已接线；service 编排单测 + handler 集成测试 + 真实 adapter↔mock 上游 parity e2e（`messages_parity_e2e_test.go`）全绿。
9. 【done，2026-06-02 durable closeout 终局】黑盒实证 + fixture 收口：
   - `RecoverableChatSettlementExecutor` 双轨语义复核：成功路径下「先写 pending recovery job → 真实 settle → mark succeeded」，settle 失败时返回 `ErrChatSettlementRecoveryScheduled`，请求向客户响应成功但 `request_records.status` 留 running 直到 worker 重放；`chat_completion.go` line 177 / `chat_stream.go` line 218/308 / `messages.go` line 167 / `message_stream.go` line 186/265 都正确区分这两条路径。
   - **item 10**「recovery facts 无法持久化时不输出成功终态」已实现：`CreatePendingChatSettlementRecoveryJob` 失败时直接返回错误（非 recovery scheduled），service 调用方按 fail 收口、`markRequestRecordFailed`，不写客户成功终态。
   - **item 11**「客户端断开后的账务收口使用有上限的内部 context」已实现：`chat_stream.go` line 151 与 `message_stream.go` 对应位置已用 `context.WithTimeout(context.WithoutCancel(ctx), 5s)` 解绑请求 ctx。
   - 黑盒实证：OAI-SDK-Mock-10 fallback 用例修复 fixture（`AddFallbackChannel` 现在同时插入同形状 `channel_cost_prices`，与生产 admin 配置必须为 enabled channel+model 配 cost_price 的硬约定一致），收紧断言为「`request_records.status=succeeded` + `final_channel_id=secondary` + `cost_snapshots` 一条且属 secondary + `settlement_recovery_jobs` 一条且 status=succeeded」。任何 running 终态或 pending recovery job 视为 durable closeout 回归。
   - 文档：fallback 的 durable closeout 语义不再作为「已知现象+延后收口」描述，而是已实证的同步终态推进契约。

目标：

```text
让 OpenAI chunk 与 Anthropic event 各自原生返回，同时统一交付、fallback 和 settlement 规则。
```

实现内容：

1. OpenAI 保持 data-only chunk + `[DONE]`。
2. Anthropic 支持 `message_start`、`content_block_start`、`content_block_delta`、`content_block_stop`、`message_delta`、`message_stop`、`ping` 和 `error`。
3. Anthropic delta 支持 `text_delta`、`input_json_delta`、`thinking_delta` 和 `signature_delta`。
4. adapter 累积最终 usage，lifecycle 只消费最终可靠 facts。
5. fallback 只允许发生在首个客户可见事件之前。
6. 有可靠 usage 的 tail error 或客户端取消仍走 settlement，并单独记录 delivery interrupted。
7. 没有可靠 usage 的风险路径写入 billing exception。
8. adapter 截留 upstream `[DONE]` 与 `message_stop`；只有 immutable recovery facts
   已持久化，并且 settlement 已完成或 durable recovery job 已接管后，gatewayapi
   writer 才输出客户可见成功终态。
9. OpenAI 上游 final usage chunk 先转成内部 facts；完成 durable closeout 后，
   `gatewayapi/openai` 再按 `include_usage` 写客户可选 usage 尾包，最后写 `[DONE]`。
10. recovery facts 无法持久化时不输出成功终态；已开始的 stream 输出协议原生 error
   event，记录 delivery interrupted。
11. 客户端断开后的账务收口使用有上限的内部 context，不依赖已取消的请求 context。

<a id="task-10-11-error-rendering"></a>
### TASK-10.11 双协议错误与安全输出

状态：done（2026-06-02）

目标：

```text
共享内部 failure，分别渲染 OpenAI 和 Anthropic 原生公开错误。
```

实现内容：

1. 【done】adapter 已把 provider 错误（HTTP status + 网络错误）解析成稳定
   `adapter.UpstreamErrorCategory`（auth/permission/rate_limit/bad_request/timeout/
   server/canceled/unknown）+ 安全 `UpstreamMetadata`，不解析 provider 原始 body。
2. 【done】两侧 handler 改为消费 `adapter.UpstreamCategoryOf(err)` 的稳定 category，
   不再把上游错误一律压成 500。HTTP status 策略两协议一致：rate_limit→429、
   timeout→504、bad_request→400、auth/permission/server/unknown→502。
3. 【done】OpenAI handler `mapUpstreamChatError` 输出 OpenAI error shape
   （429 `rate_limit_error`/504+502 `api_error`/400 `invalid_request_error`）。
4. 【done】Anthropic handler `mapUpstreamMessageError` 输出 Anthropic error shape
   （429 `rate_limit_error`/504+502 `api_error`/400 `invalid_request_error`）。
5. 【done】SSE 开始后按各协议输出 stream error：OpenAI 写 data-only error chunk、
   Anthropic 写 `event: error`，且都复用与非流式相同的分类映射（Anthropic 不再写死
   `api_error / "stream request failed"`），不回退普通 JSON。
6. 【done】原始上游 body 不直接返回客户：公开 message 全部是固定安全文案；测试断言
   upstream auth/permission 绝不渲染成 401/`authentication_error`（避免客户误以为自己的
   API key 失效），且 message 不泄漏内部诊断。
7. 【done】安全验收 4（按 DEC-013 修订）：`anthropic-beta` 一律接受，不因 provider 能力 400；
   按 DEC-012 在 provider 映射层 Drop（DeepSeek 忽略且出站不发送），handler 脱敏 Debug 审计
   `dropped_beta_headers`（仅 beta 能力名），绝不在响应里假装某 beta 生效。理由见 DEC-013：
   drop-in 兼容 + 与 DEC-012 一致 + beta 列表更新快；对照 OpenRouter 同样宽进不 400。

测试：`app/gatewayapi/openai/chat_upstream_error_test.go`（非流式 7 分类表 + 流式
tail error 映射）、`app/gatewayapi/anthropic/messages/error_security_test.go`
（非流式 7 分类表 + 流式 tail error 映射 + beta 接受/拒绝/混合列表拒绝）。

<a id="task-10-12-migration-sqlc-tests"></a>
### TASK-10.12 Migration、sqlc 与账务回归

状态：done（2026-06-02，10.12A 生产路径 + 10.12B 测试与 sqlc 回归均已收口）

目标：

```text
完成 schema 改造、sqlc 生成和账务回归，保证商业事实未被协议扩展破坏。
```

实现内容：

1. 【done，10.12A】按表修改源 migration，扩展协议无关 usage/facts、价格快照、
   成本快照与 recovery job，并新增 `usage_line_items`。
2. 【done，10.12B】本地库 `drop`→`up` 重建到新 schema，与源 migration 完全对齐。
3. 【done，10.12A】OpenAI settlement/recovery 只消费 immutable `ResponseFacts` /
   `usage.Facts`；recovery job 从持久化 facts 字段重建，不重新解析公开响应。
4. 【done，10.12B】按一张表一个 query 文件修改 SQL，`sqlc generate` 无 drift。
5. 【done，10.12B】`sqlc generate` 已执行，旧生成物已清理。
6. 【done，10.12B】settlement / write-off / risk exposure / recovery 幂等测试已对齐
   新 schema：`chat_settlement_test`（usage facts 列断言 + `ensureSettlementUsageMatches(row, facts)`
   + cost snapshot tamper 列名）、`service_test` / `openai_parity_e2e_test`
   （`fakeChatAdapter` 合成 `StreamOutcome.Facts`、`chatResponse` 产出 `ResponseFacts`、
   断言改 `Facts.Usage.*` / `Facts.Metadata`）。
7. 【done，10.12B】usage line item 与 cache write 价格测试已补；`ledger` /
   `sqlc` 测试 helper 已补 `ingress_protocol` + `operation`；usage line item 用例
   拆为独立事务避免 25P02。带 `DATABASE_URL` 的 `go test ./internal/...` 全绿。

> 残留 token 明细（audio / accepted_prediction_tokens / rejected_prediction_tokens）
> 随 TASK-10.06 billing facts 收口，不再阻塞 10.12 关闭。

<a id="task-10-13-blackbox-openai"></a>
### TASK-10.13 OpenAI 黑盒验收

状态：done（2026-06-02）

目标：

```text
使用 OpenAI SDK 作为外部客户端验收工具，不将 SDK 引入生产 adapter。
```

实现总结：

- **隔离机制**：`openai-go v1.12.0` 通过 `//go:build blackbox` build tag 完全隔离；
  生产 build (`go build ./cmd/...`) 不会引入 SDK，二进制零残留；
  `go vet ./cmd/... ./internal/...`、`go build ./cmd/...`、`git diff --check` 全绿。
- **共享 fixture**：`internal/blackbox/sdkfixture/fixture.go` 装一个完整的 unio gateway
  HTTP server（httptest）+ 真实 PostgreSQL + Redis + 一份可用的
  user/project/api_key/channel/model/price/cost/balance/credential。
  客户 SDK 把 `base_url = fixture.BaseURL`、`api_key = fixture.APIKey` 就能 drop-in。
- **OAI-SDK-Mock 用例（14 个）**：在 `internal/blackbox/openaisdk/` 用 mock 上游覆盖
  非流式（01）/流式（02）/非流式 reasoning（03）/流式 reasoning（04）/tools 多轮（05）/
  `response_format=json_object`（06）/不支持字段 Drop（07，DEC-012 对真实 SDK 客户的端到端证明）/
  错误映射 5 分类（08a~08e：429/400/上游 401 映射 502 不渲染成客户 401/500→502/timeout→504）/
  settlement DB 事实链路（09，含 request_records/request_attempts/usage_records/
  ledger_entries/price_snapshots/cost_snapshots）/fallback（10，含 durable closeout
  现状校验：要么同步推 succeeded，要么 settlement_recovery_jobs 已写入）。
- **OAI-SDK-Real smoke（2 个）**：`realupstream_test.go` 对真实 DeepSeek 上游做
  非流式 + 流式 + tail usage smoke，gate 在 `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`。
  与 DS-OAI（adapter 层）形成纵深；DS-OAI 验 adapter 与上游契合，10.13 验 SDK 与 unio HTTP 契合。
- **fallback durable closeout 发现**：fallback 路径下，settle 通过
  `RecoverableChatSettlementExecutor` 先写 pending recovery job 再 settle；如果 settle 失败
  返回 `ErrChatSettlementRecoveryScheduled`，客户端拿到成功响应，但
  `request_records.status` 留在 running 等 worker 重放推 succeeded。这是
  GAP-7-007 的设计行为（不阻塞客户响应），不是 bug；客户视角的终态推进缺口
  由 **TASK-10.10 durable closeout** 收口。

覆盖映射（原计划 7 项）：

1. 非流式与流式 → 01、02、Real-01、Real-02。
2. reasoning → 03（非流）、04（流）。
3. tools 多轮 → 05（两轮 function_call → tool_role result → 最终回复）。
4. response format → 06（`json_object` 上游透传 + SDK 解析）。
5. 多模态和完整高级字段的 Support / Reject → 07（DEC-012 Drop 高级字段，保留 Pass `logprobs`）。
6. usage、错误、fallback、settlement、delivery audit → 02/Real-02 tail usage、
   08a~08e 错误映射、09 settlement DB 链路、10 fallback。
7. DS-OAI mapping `Verify` 冻结回归 → 真实 DeepSeek smoke (Real-01/02) +
   DS-OAI-01~14（adapter 层）共同覆盖。

测试入口：

```bash
# 仅 mock 上游（不需要 DeepSeek key，需要 DATABASE_URL + Redis）：
go test -tags=blackbox ./internal/blackbox/openaisdk/...

# 含真实 DeepSeek smoke：
DEEPSEEK_BLACKBOX=1 DEEPSEEK_API_KEY=sk-... \
  go test -tags=blackbox ./internal/blackbox/openaisdk/...
```

<a id="task-10-14-blackbox-anthropic"></a>
### TASK-10.14 Anthropic 黑盒验收

状态：done（2026-06-02）

目标：

```text
使用 anthropic-sdk-go 作为外部客户端验收工具，跑通 "客户改 base_url + api_key 即可
drop-in 使用 unio gateway 的 /v1/messages" 的端到端契约；不将 SDK 引入生产 adapter。
```

实现：

- 依赖隔离：`anthropic-sdk-go v1.46.0` + `//go:build blackbox` build tag，
  仅在 `-tags=blackbox` 下编译；生产二进制 `go build ./cmd/...` 不引入 SDK。
- fixture 扩展：`sdkfixture` 增加 `Protocol="anthropic"` + `AdapterKey="deepseek"` 模式，
  channel.base_url 真实模式指向 `https://api.deepseek.com/anthropic`，mock 模式指向
  `httptest.Server`；新增 `Fixture.AnthropicBaseURL = Server.URL + "/"`，
  匹配 anthropic-sdk-go 的 base 期望（SDK 内部自己拼 `v1/messages`）。
- **ANT-SDK-Mock（12 个）** 全绿，路径 `internal/blackbox/anthropicsdk/`：
  - **非流式 + 流式基本面**：`nonstream_test.go` / `stream_test.go` 验证 SDK
    accumulator pattern 累加内容、`message_start → content_block_* → message_delta
    → message_stop` 完整事件序、usage 与 stop_reason。
  - **thinking content block**：`thinking_test.go` 验证 ContentBlockUnion.AsThinking()
    暴露 thinking + signature；与 text block 共存索引稳定。
  - **tool_use 多轮**：`tools_test.go` 端到端模拟两轮：turn-1 模型返
    `stop_reason=tool_use` + ToolUseBlock，客户端用 SDK 构造 `tool_result(tool_use_id=...)`
    续接 turn-2；断言 turn-2 upstream body 中 user content 含 `tool_result.tool_use_id`
    与 turn-1 一致。
  - **DEC-012 Drop + DEC-013 anthropic-beta**：`drop_test.go` 故意通过
    `option.WithJSONSet` 注入 `service_tier/mcp_servers/top_k/output_config.format`，
    通过 `option.WithHeader` 注入 `anthropic-beta`；断言 unio 不 400 返成功，
    mock 上游 body 不含被 Drop 字段（`output_config.effort` 保留），
    upstream headers 不含 `anthropic-beta`，含 adapter 强制写入的 `anthropic-version`
    与 `x-api-key`、不含 Authorization。
  - **错误映射**：`errors_test.go` 验五条上游故障 → 客户端最终 SDK error 的映射：
    上游 429 → 客户 429 `rate_limit_error`；上游 400 → 客户 400
    `invalid_request_error`；**上游 401（provider credential issue）→ 客户 502
    `api_error`**（关键不变量：不渲染成客户 401 误导用户检查 unio key，且上游
    `Your api key: ****abc is invalid` 类原文不得透传）；上游 500 → 客户 502 `api_error`；
    上游超时（channel timeout=100ms vs mock 阻塞 1s）→ 客户 504 `api_error`。
  - **settlement DB 事实链路**（`settlement_test.go`）：一次成功调用后断言
    `request_records.status=succeeded` + `ingress_protocol=anthropic` + `operation=messages`，
    `request_attempts.upstream_protocol=anthropic` + `upstream_status_code=200`，
    `usage_records` 把 `input_tokens=100 → uncached_input_tokens`、
    `output_tokens=50 → output_tokens_total`，`ledger_entries` 至少一条 debit，
    `price_snapshots` / `cost_snapshots` 各一条。
- **ANT-SDK-Real（2 个）** 全绿，`realupstream_test.go` 对真实 DeepSeek Anthropic endpoint
  做非流式 + 流式 smoke，gate 在 `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`。
  关键真实行为：DeepSeek 默认开 thinking mode，必须 `MaxTokens >= 256` 才能让 final text
  完整输出；测试同时接受 text 或 thinking 非空（与 DS-ANT adapter 层一致）。
  与 DS-ANT（adapter 层）形成纵深；DS-ANT 验 adapter 与上游契合，10.14 验 SDK 与
  unio HTTP 契合。

覆盖映射（原计划 7 项）：

1. 非流式与流式 → nonstream_test、stream_test、Real-01、Real-02。
2. system、content blocks、thinking → thinking_test。
3. tools 多轮和 `input_json_delta` → tools_test（DeepSeek anthropic endpoint 不输出
   `input_json_delta` 增量，统一为完整 `tool_use.input`；这是 mapping 层既有事实，
   adapter 层 DS-ANT 已覆盖）。
4. cache control 和 usage → drop_test（cache_control 走 Extensions Drop）+
   settlement_test（usage 字段 → uncached/cache_read/output 端到端）。
5. ignored / unsupported 字段 Reject → 按 DEC-012 改为 **Drop（不 Reject）**：drop_test。
6. 错误 / settlement / delivery audit → errors_test + settlement_test。
   fallback 走 OpenAI 侧 OAI-SDK-Mock-10 已验证（共享 lifecycle，行为协议无关），
   不重复在 Anthropic 侧建第二个 channel；fallback 路径 durable closeout 现状
   归 TASK-10.10 收口。
7. mapping `Verify` 冻结回归 → 由 DS-ANT 黑盒承担；10.14 不重复 mapping 表逐字段断言。

最终验收命令（全绿）：

```bash
# Anthropic SDK 全套(含真实 DeepSeek): 12 mock + 2 real
DEEPSEEK_BLACKBOX=1 DEEPSEEK_API_KEY=*** go test -tags=blackbox -count=1 \
  ./internal/blackbox/anthropicsdk/...

# OpenAI SDK 回归: 14 mock + 2 real
DEEPSEEK_BLACKBOX=1 DEEPSEEK_API_KEY=*** go test -tags=blackbox -count=1 \
  ./internal/blackbox/openaisdk/...

# 主线 vet/build/diff/非 DB 单测
go vet ./cmd/... ./internal/...
go build ./cmd/...
git diff --check
go test $(go list ./internal/... | grep -v -E '(store/sqlc|service/gateway/lifecycle|core/ledger|core/requestlog|blackbox)')
```

<a id="task-10-15-doc-review"></a>
### TASK-10.15 文档、命名与冗余复核

状态：done（2026-06-02）

目标：

```text
阶段关闭前按长期维护视角复核目录、命名、重复代码和协议矩阵。
```

检查项与结论：

1. ✅ `openai`、`anthropic` 是协议族包名（`internal/{core/adapter,app/gatewayapi,service/gateway}/openai|anthropic/`）。
2. ✅ `deepseek` 是两个协议族下的 provider 实现包名（`core/adapter/openai/deepseek`、`core/adapter/anthropic/deepseek`）。
3. ✅ `streamtranslate` 已移除（代码仅保留 `core/adapter/openai/chat.go` 一处死注释引用，无 import、无包目录）。
4. ✅ **已收口（10.15 + 10.05 终局）**：核心账务/熔断/retry/metrics/tracing 都委托共享 `lifecycle`，没有逻辑复制；`endSettlementSpan` 与 request log 协议无关 helper 已 hoist 到 `lifecycle/tracing.go` / `lifecycle/request_log.go`；**thin wrapper 复制清单**（channel_breaker / chat(message)\_authorization / chat(message)\_metrics / chat(message)\_request_record 各对的 receiver-bound wrapper）已在 TASK-10.05 终局收口——抽出 `lifecycle.RequestLifecycle`（`request_lifecycle.go`），两侧 service 改为 1-line forward，协议族 ad-hoc string code 通过构造时注入的 `SafeMessage` 闭包提供。
5. ✅ app DTO、protocol adapter DTO、provider wire DTO、DB row 没有互相泄漏：`core/adapter/**` 不引用 `app/gatewayapi/**` 也不引用 `platform/store/sqlc`；`app/gatewayapi/**` 不引用 `platform/store/sqlc`；`deepseek/*` 子包仅被 bootstrap + lifecycle/adapter_registry_test 引用，未外泄到 OpenAI/Anthropic 共享包。
6. ✅ 没有引入 `common`、`util`、`helper`（按目录名与 package 名全仓 grep 零命中）。
7. ✅ 两个字段矩阵与实际代码、测试一致：OpenAI 矩阵 §2 顶层字段（seed/top_logprobs/logit_bias/modalities/audio/prediction/verbosity/prompt_cache_key/prompt_cache_retention/safety_identifier/web_search_options 等）在 `openai/chatcompletions/dto.go`（原 `openai_dto.go`）全部 typed；Anthropic 矩阵 §4 按 DEC-012 走 typed (top_k/thinking) + Extensions（cache_control/container/inference_geo/service_tier/output_config/mcp_servers）+ adapter 出站 Drop，`deepseekDroppedExtensions` 与单独的 `output_config` 处理路径全部对应。
8. ✅ DeepSeek 两套 mapping 与黑盒测试一致（DS-OAI / DS-ANT 的 `Verify` 均已清零，见对应 mapping §8/§14 与 TASK-10.07/10.09 黑盒回归）。
9. ✅ `go build ./internal/... ./cmd/...`、`go vet ./internal/... ./cmd/...`、`git diff --check` 全绿；非 DB 包测试全绿；带 `DATABASE_URL` 的 DB 集成测试由用户本地复跑（环境凭据未跨会话注入）。

本次额外完成的目录对称重组（用户在本任务中点名）：

```text
原结构（不对称）:
  app/gatewayapi/openai/                  ← 14 个文件平铺（chat + models + 共享）
  app/gatewayapi/anthropic/messages/      ← operation 子包

重组后（与 anthropic/messages/ 对称）:
  app/gatewayapi/openai/doc.go            ← 协议族包说明（写入三条硬规则）
  app/gatewayapi/openai/chatcompletions/  ← 12 个 chat 文件（文件名同步简化，
                                            chat_completions_handler.go → handler.go、
                                            openai_dto.go → dto.go 等）
  app/gatewayapi/openai/models/           ← 2 个 models 文件

import 调整：router/bootstrap/service 共 16 处；router 改 gatewaychat/gatewaymodels 双别名。
长期规范：写入 docs/architecture/PROJECT_STRUCTURE.md，未来新 operation（如 openai/responses、
openai/embeddings）必须直接落子包，不允许在协议族根包平铺。
```

## 推荐实施顺序

```text
10.01 ADR
→ 10.02 目录迁移
→ 10.03 channel protocol + registry
→ 10.04 ResponseFacts + schema
→ 10.05 lifecycle executor
→ 10.06 OpenAI 全量字段契约
→ 10.07 DeepSeek OpenAI adapter
→ 10.08 Anthropic 全量字段入口
→ 10.09 DeepSeek Anthropic adapter
→ 10.10 stream 生命周期
→ 10.11 错误输出
→ 10.12 migration/sqlc/账务回归
→ 10.13 OpenAI 黑盒
→ 10.14 Anthropic 黑盒
→ 10.15 文档与结构复核
```

## 进入本阶段前必须检查

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n "streamtranslate|providers\\.adapter|adapter\\.ChatRequest|adapter\\.ChatResponse|adapter\\.ChatUsage" docs internal migrations sql
```

必须阅读：

```text
docs/protocol/openai_chat_completion.md
docs/protocol/anthropic_message.md
docs/chapters/phase-10-dual-protocol-gateway/ARCHITECTURE.md
docs/chapters/phase-10-dual-protocol-gateway/RESPONSE_FACTS.md
docs/chapters/phase-10-dual-protocol-gateway/OPENAI_CHAT_COMPLETIONS_MATRIX.md
docs/chapters/phase-10-dual-protocol-gateway/ANTHROPIC_MESSAGES_MATRIX.md
docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md
docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_ANTHROPIC_MAPPING.md
docs/production/TODO_REGISTER.md
docs/production/RELEASE_BLOCKERS.md
docs/production/DECISIONS.md
```
