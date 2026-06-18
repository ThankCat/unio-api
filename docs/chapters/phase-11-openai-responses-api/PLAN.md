# Phase 11 Plan - OpenAI Responses API ingress（Codex 兼容，生产级）

## 目标

为 Unio Gateway 新增第三个公开操作：

```text
POST /v1/responses          OpenAI Responses API（Codex CLI 主入口）
```

让客户在 **不修改 Codex** 的前提下，只把 Codex 的 `base_url` / `api_key` 指向 Unio，
即可用 DeepSeek（以及任何只提供 Chat Completions 的 OpenAI 兼容上游）驱动 Codex。

本阶段是 **商业级、生产级实现**，不是 MVP、不是 demo、不是 “先跑通再补”。收口标准等同
Phase 10 双协议网关：计费事实、审计、settlement/recovery、错误安全、限流、鉴权、可观测性、
fallback、流式可靠交付、tokenizer 预授权和黑盒验收必须全部到位（见 [ACCEPTANCE.md](ACCEPTANCE.md)）。
“无状态会话” 是经过权衡的生产范围决策（见下文与 GAP-11-001），不是质量妥协。

## 端到端流程（对齐业务诉求）

```text
1. Codex 配置：base_url = <unio>/v1，api_key = <unio key>，wire_api = "responses"。
2. Codex → POST <unio>/v1/responses（Responses 协议请求）。
3. Unio 内部选定要使用的上游模型（模型指定策略见“## 模型指定策略”）。
4. Unio 把 Responses 参数转换成内部 openai.ChatRequest，交给上游 adapter。
5. 模型上游（DeepSeek /chat/completions）返回（非流式 JSON 或流式 SSE）。
6. Unio 把上游返回转换成 Responses 响应格式（非流式 response / 命名事件流）返回 Codex。
```

对应内部链路：

```text
Codex（Responses 请求）
→ gatewayapi/openai/responses           解析 Responses DTO
→ service/gateway/openai/responses      Responses → 内部 openai.ChatRequest 翻译
→ 共享 lifecycle                         routing(protocol=openai) / authorization / attempt / settlement / recovery
→ adapter/openai/deepseek               复用现有 Chat Completions adapter（不新增 Responses adapter）
→ DeepSeek /chat/completions
→ adapter/openai/deepseek 响应翻译        upstream wire → 内部 openai.ChatResponse / ChatStreamChunk + ResponseFacts
→ service/gateway/openai/responses      ChatResponse/Chunk → Responses 响应 / 命名 SSE 事件翻译
→ gatewayapi/openai/responses           写出 Responses JSON / SSE
→ Codex
```

## 背景：Codex 为什么需要 `/v1/responses`

1. OpenAI Codex CLI（0.118+）固定使用 **Responses API**（`wire_api = "responses"`），
   请求体用 `input: [...]` 而非 `messages: [...]`，并带 Responses 专属字段
   （`instructions` / `reasoning` / `text` / `store` / `previous_response_id` /
   `include` / `truncation` / `prompt_cache_*`），工具用扁平 `{"type":"function","name":...}`。
2. DeepSeek 以及绝大多数第三方上游 **只提供 Chat Completions**，没有 Responses endpoint。
3. 因此社区做法是中间放一个 **协议翻译代理**：把 Responses 请求实时下转成 Chat Completions，
   再把 Chat Completions 响应（含 SSE）实时上转回 Responses 事件。Unio 把这层翻译收进自己的
   gateway，复用既有计费、审计、fallback 和 settlement，并把它做成商业级网关能力，而不是让用户
   额外跑一个本地代理。

> 上游侧本阶段先锁定 DeepSeek（沿用 Phase 10 已验收的 `adapter/openai/deepseek`）。任何
> 已实现 `openai.ChatAdapter` 的上游都能自动获得 `/v1/responses` 能力，因为桥接发生在 Unio
> 内部协议契约层，不依赖具体 provider。

## 核心架构决策

详见 [DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接)。要点：

1. **Responses 是 ingress-only 协议**：在 gateway 内部下转到既有 `openai.ChatRequest` 契约，
   复用现有 OpenAI Chat 上游、registry capability（`Chat` / `StreamChat` / `ChatInputTokenizer`）、
   routing（`IngressProtocol=openai`）、authorization、settlement、recovery 与 `ResponseFacts`。
2. **不强行统一公开 DTO**：沿用 Phase 10 边界——请求协议分离、响应协议分离、账务事实统一、
   商业生命周期统一。Responses 公开 DTO 独立维护，不污染 Chat DTO，也不进 billing。
3. **账务零改动**：上游仍是一次 Chat Completions 调用，`ResponseFacts` / `usage.Facts` /
   价格快照 / 成本快照 / recovery 全部复用，不新增账务 schema。唯一新增是
   `requestlog.Operation = "responses"`，用于把 Responses 请求与 chat/messages 在审计与
   metrics 上区分。
4. **目录对称**：按 PROJECT_STRUCTURE 的硬规则，新增 operation 必须落独立子包
   `gatewayapi/openai/responses` 与 `service/gateway/openai/responses`，不在协议族根包平铺；
   协议无关共享能力仍只落 `service/gateway/lifecycle`。

桥接翻译代码落点（与 `chatcompletions` 的 `chat_dto_map.go` 对称）：

```text
service/gateway/openai/responses/responses_chat_map.go     Responses 请求 → 内部 openai.ChatRequest
service/gateway/openai/responses/responses_response_map.go 内部 openai.ChatResponse → Responses 响应
service/gateway/openai/responses/responses_stream.go       内部 openai.ChatStreamChunk → Responses 命名事件状态机
```

## 上游与 Drop 职责划分（DeepSeek 为首版具体上游）

首版具体上游就是 **DeepSeek**，复用 Phase 10 已实现并验收的
[`internal/core/adapter/openai/deepseek`](../../../internal/core/adapter/openai/deepseek)
（`adapter_key="deepseek"`，已注册 `Chat` / `StreamChat` / `ChatInputTokenizer`，含
[DEEPSEEK_OPENAI_MAPPING.md](../phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md)
与黑盒测试）。本阶段不重写 DeepSeek adapter，但必须把它纳入计划并复核（见 TASK-11.09），
明确两层职责，避免桥接层与 adapter 重复或漏 Drop：

```text
桥接层（responses_chat_map）：只做“协议结构翻译”，把 Responses 字段映射到 openai.ChatRequest
  契约字段——能映射就映射进契约，不在桥接层做 provider 能力裁剪。
DeepSeek adapter（已实现）：做“provider 能力 Drop”，在出站前 dropUnsupported 清掉 DeepSeek
  无法转换的字段并记审计。
```

关键后果：`openai.ChatRequest` 已含 `Store` / `Verbosity` / `PromptCacheKey` / `PromptCacheRetention` /
`ResponseFormat` / `ParallelToolCalls` / `ReasoningEffort` 等全部 OpenAI 规范字段，DeepSeek adapter
也已对它们做出站 Drop（`store` / `verbosity` / `prompt_cache_*` / `json_schema` 形态的
`response_format` / `parallel_tool_calls` / `type=custom` 工具 / 多模态 part / 非白名单 extension）。
因此桥接层 **不应** 自己硬 Drop 这些字段，而应：

```text
有 ChatRequest 承载字段的 Responses 字段（store / prompt_cache_* / text.verbosity / text.format /
  parallel_tool_calls / reasoning.effort）→ Adapt 进 ChatRequest 对应字段，由 DeepSeek adapter 出站 Drop。
  好处：provider 无关，未来换上能支持这些字段的上游可直接 Pass，不丢能力。
无 ChatRequest 承载字段的 Responses 专属字段（previous_response_id / include / truncation /
  item_reference）→ 桥接层 Drop（契约里无处安放，且无状态），记内部审计。
```

Codex `apply_patch`（`type=custom` grammar 工具）特别说明：若桥接层原样映射为
`ChatTool{Type:"custom"}`，DeepSeek adapter 的 `dropCustomTools` 会把它整条 Drop，Codex 编辑能力失效；
若要在 DeepSeek 上保留编辑能力，必须在桥接层 convert→function（见 GAP-11-002，TASK-11.08 冻结）。

## 模型指定策略

> 业务诉求第 3 步 “unio 内部使用支持的模型，具体怎么指定还需要讨论” 在此收口。
> **首版已采纳方案 A**（客户 model = Unio 模型目录 model_id，复用现有 routing）；模型别名作为增强
> 留 GAP-11-006。TASK-11.02 负责把方案 A 落为可运营配置并冻结剩余决策点。

### 问题

Codex 在请求体里发送 `model`（来自 Codex 配置 `model = "..."`）。Unio 必须把这个 `model`
映射到一个 **受支持的上游模型**（当前是 DeepSeek 的某个 `upstream_model`）。问题是：这个映射
由谁、在哪里、以什么粒度指定。

### 现有可复用机制（Phase 6 routing + model catalog）

Unio 已经具备完整的“模型名 → channel + upstream_model”路由机制，不需要为 Responses 另造一套：

```text
models            模型目录（model_id, enabled, owned_by），由 /v1/models 公布。
channels          上游渠道（protocol=openai, adapter_key=deepseek, base_url, priority, ...）。
channel_models    某 channel 下 model → upstream_model 映射（真实上游模型名）。
routing           客户 model + ingress protocol → 同协议候选 → 按优先级/熔断选 channel。
project_model_policies  project 级 allow-list/deny-list（已存在）。
```

Responses 请求复用同一条 routing：`IngressProtocol=openai` + 客户 `model` → 选中 OpenAI channel
→ adapter 用该 channel 的 `upstream_model` 调 DeepSeek。

### 首版决策（已采纳：方案 A）

```text
方案 A：客户 model = Unio 模型目录里的 model_id（运营配置驱动）
- 运营在后台/seed 配置一个 Unio 模型（如 deepseek-chat、deepseek-reasoner，或品牌别名 unio-codex），
  并把它映射到 DeepSeek channel 的 upstream_model。
- 用户把 Codex 的 model 配成这个 model_id。
- /v1/models 公布可选模型，Codex 的模型选择/校验可用。
- routing、project_model_policies、计费价格全部按既有事实走，零特例。
```

方案 A 的好处：不引入 Responses 专属模型逻辑，路由/计费/审计与 chat 完全一致，最可控、可审计。

### 决策点结论（首版冻结，TASK-11.02 落地）

| # | 决策点 | 选项 | 首版结论 |
| --- | --- | --- | --- |
| 1 | 是否支持 **模型别名**（Codex 默认名如 `gpt-5-codex` → DeepSeek 上游） | A) 不支持，用户必须填 Unio model_id；B) 支持 catalog 级 alias 表 | **首版不支持**（方案 A）；别名作为增强（GAP-11-006） |
| 2 | 是否需要 **Codex 专属路由组/默认模型** | A) 复用通用 routing；B) 为 responses 配独立默认模型 | **复用通用 routing**（A） |
| 3 | reasoning 模型选择（`deepseek-reasoner` vs `deepseek-chat`） | 由所选 model_id 决定；reasoning 能力差异在 adapter 出站 Drop/保留 | **由运营按 model 配置** |
| 4 | 客户 `model` 在 Unio 不存在时的行为 | 返回 Responses `model_not_found` 400/404，不静默替换 | **明确报错**（与 chat 一致） |
| 5 | 是否暴露 Responses 在 `/v1/models` 的可用性 | 复用现有 `/v1/models`（已含可见性策略） | **复用** |

> 红线：Unio **不静默替换** 客户请求的模型；模型映射只来自运营配置的 catalog/channel 事实，
> 不在 responses 翻译层硬编码 provider 模型名。

## Responses API 接口范围

OpenAI Responses API 不止 `POST /v1/responses` 一个 endpoint。下表是本阶段对全部官方接口的
生产范围决策（决策依据：Codex CLI 对第三方 provider 的实际调用面 + DeepSeek 能力边界 + Unio
无状态商业承诺）。完整能力声明见 [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md)。

| Endpoint | Codex 是否调用 | 本阶段处理 | 任务 / GAP |
| --- | --- | --- | --- |
| `POST /v1/responses` | ✅ 主路径 | **完整实现**（非流式 + 流式 + tools + reasoning + 错误安全） | TASK-11.04 ~ 11.10 |
| `POST /v1/responses/compact` | ⚠️ 长会话超阈值时调用 | **降级实现**：无状态翻译为一次 DeepSeek 摘要请求 + 合成可回传 `compaction` item（不等价于 OpenAI 加密语义） | TASK-11.11 / GAP-11-007 |
| `POST /v1/responses/input_tokens` | ⚠️ 偶尔预检 | **本地估算实现**：复用 `ChatInputTokenizer`，先把 Responses input 翻译为 Chat messages 再估算 | TASK-11.12 / GAP-11-008 |
| `GET /v1/responses/{id}` | ❌ Codex 不发 | **501 stateless**，返回 Responses 原生 error，提示 stateless 边界 | TASK-11.13 / GAP-11-009 |
| `DELETE /v1/responses/{id}` | ❌ | **501 stateless** | TASK-11.13 / GAP-11-009 |
| `GET /v1/responses/{id}/input_items` | ❌ | **501 stateless** | TASK-11.13 / GAP-11-009 |
| `POST /v1/responses/{id}/cancel` | ❌（仅对 `background=true` 异步任务有效） | **501 stateless** | TASK-11.13 / GAP-11-009 |

“501 stateless” 不是静默拒绝：返回 Responses 原生 error shape，`code` 显式标注本阶段为
无状态商业承诺，引导客户用每轮回传完整 `input` 的方式，与 Codex 默认行为一致。

## 结构性不可桥接能力（公开能力边界）

下列能力是 **OpenAI 专属能力**，DeepSeek 等任何第三方上游结构上无法等价提供。**本阶段不假装实现**，
统一收口为公开能力边界，由 [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md) 声明，并按以下策略处理：

| 能力 | 不可桥接原因 | 本阶段策略 |
| --- | --- | --- |
| `encrypted_content`（reasoning 跨轮加密透传） | 需 OpenAI 服务端密钥加解密 | 桥接层不生成；ingress 收到 Drop 并记审计（GAP-11-003） |
| 内置工具 `web_search` / `file_search` / `code_interpreter` / `computer_use` / `image_generation` / `mcp` | 需 OpenAI 服务端真实执行 | ingress 收到 Drop，不调用任何外部能力（GAP-11-004） |
| Server-side 存储（`store=true` 持久化、retrieve/delete/input_items） | 需服务端会话存储 | 进 ChatRequest 由 DeepSeek adapter 出站 Drop；retrieve 系列 endpoint 501（GAP-11-001 / 11-009） |
| `background=true` 异步任务 | 需服务端任务队列 | ingress 收到 Reject（合法协议但本阶段不支持，明确报错而非静默成同步） |
| `prompt`（server-side prompt template 引用） | 需服务端模板存储 | ingress 收到 Drop 并记审计，建议客户内联 prompt |
| `include:["reasoning.encrypted_content"]` 等输出控制 | 与上同 | ingress 收到 Drop（GAP-11-004） |

这与社区做法一致：**没有任何一个开源 Codex↔Chat 桥实现了这些能力**（LiteLLM/codex-relay/
codex-deepseek/codex-bridge 全都不做）。Unio 把它做成显式商业承诺，而不是隐性缺陷。

## 非目标（生产范围边界，非质量妥协）

1. 不新增上游 Responses adapter；DeepSeek 没有 Responses endpoint，本阶段不去对接任何
   provider 的 Responses 上游。
2. 不扩展多模态模型能力。Responses `input_image` / `input_file` 等只做协议面识别；当前上游
   不支持时按 DEC-012 在 adapter 出站 Drop。
3. 不引入官方 Responses Go SDK 进生产链路（沿用 DEC-011，SDK 仅用于黑盒验收）。
4. 不改动既有 `/v1/chat/completions` 与 `/v1/messages` 的对外契约和计费事实。
5. 不模拟"结构性不可桥接能力"。`encrypted_content` 不自己生成假密文、内置工具不挂任何
   externally-executed 替代物——保持公开能力边界的诚实性。

“不在本阶段”不等于允许静默丢字段：本阶段对 Responses 请求、非流式响应、流式事件、错误响应、
usage 与工具/reasoning 的下转/上转必须完整设计、实现并验收；无法转换的合法协议字段按 DEC-012
在出站 Drop 并记内部审计；不可桥接能力按上表显式策略处理。

## 开源参考（角色分工与不参考清单）

社区里没有任何一个项目做到了 Responses 在第三方 provider 上的完美适配——这是结构性不可能。
但它们各自的取舍、字段映射、事件序列对本阶段有不同的参考价值。**本阶段不复制代码**（许可证 +
架构都不匹配），只把它们当对应角色的参考材料：

### 主参考（行为照它来）

**[MetaFARS/codex-relay](https://github.com/MetaFARS/codex-relay)（Rust）**

社区里**唯一一个把 Codex 在第三方 provider 上的行为做到生产质量**的项目。本阶段 SSE 状态机、
tool_call delta 累积、并行 tool 分项、`apply_patch` 处理的设计选择 1:1 学它的思路。

具体借鉴：

- 流式事件先后顺序（`response.created` → `output_item.added` → `content_part.added` →
  `output_text.delta` → `...done` → `response.completed`）。
- `sequence_number` 单调与 `item_index` / `content_index` 稳定。
- Chat `tool_calls[i].function.arguments` 增量合并成 Responses 单个 `function_call` item。
- `apply_patch`(`type=custom` grammar) → function 单 string 参数。
- 强制无状态，不碰 `previous_response_id` / `encrypted_content`。

代码用 Go 自然风格重写，不直译 Rust enum + match 形态。

### 辅参考 1（字段映射百科全书）

**[BerriAI/litellm](https://github.com/BerriAI/litellm) `responses/litellm_completion_transformation/`（Python）**

字段映射最全、边角 case 最多的实现。专门用来对照填 [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)
的矩阵，确保不漏字段。重点函数：

- `transform_responses_api_input_to_messages`（input items → messages）
- `_transform_response_input_param_to_chat_completion_message`（单 item → message）
- `transform_responses_api_tools_to_chat_completion_tools`（tools 扁平→嵌套）
- `transform_chat_completion_response_to_responses_api_response`（响应回译）
- `_transform_chat_completion_choices_to_responses_output`（choices → output items）

**只参考字段语义和边角处理，不参考其架构**（LiteLLM 是 provider 无关库，与 Unio 分层完全不同）。

### 辅参考 2（DeepSeek wire 行为 sanity check）

**[yangfei4913438/codex-deepseek](https://github.com/yangfei4913438/codex-deepseek)** /
**[wujfeng712-ui/codex-bridge](https://github.com/wujfeng712-ui/codex-bridge)**（Python / TypeScript）

社区里**真的对着 DeepSeek 跑通过 Codex** 的代理。只用于黑盒验收阶段，遇到"Codex 抓包出来这字段
长这样，DeepSeek 实际接受/拒绝什么"时，对照它们的 wire 处理确认我们的翻译没偏。

**代码质量都不高（单文件代理），只参考 wire 细节，不参考代码组织**。

### 备用参考

**[soddygo/codex-convert-proxy](https://github.com/soddygo/codex-convert-proxy)（Rust）**

多上游适配：`developer` role→`system`、按 provider 映射 reasoning effort。如果未来引入第二个
非 OpenAI 上游（如 Gemini），可对照其 provider 切换策略。本阶段只看一次，不进决策依赖。

### 反缝合怪护栏（明确不参考的部分）

为避免读多了变成"东拼西凑"，下列内容明确**不参考**任何一个开源项目，以 Unio 既有 Phase 9/10
约定为准：

| 不参考的 | 必须遵循 |
| --- | --- |
| 任何项目的错误处理 / 重试 / fallback | Unio 已验收的 `failure.Code` + `UpstreamCategoryOf` + lifecycle |
| 任何项目的日志 / 审计 / metrics | Unio 已验收的 structured log + Prometheus + requestlog |
| 任何项目的架构分层 / 文件组织 | 与 `chatcompletions/` 完全对称（已验收的 operation 子包模式） |
| LiteLLM 的 provider 抽象 / 模型注册 | Unio 已验收的 `adapter/openai` registry + Phase 6 routing |
| Rust 风格的 enum + match 状态机 | Go 自然的 struct + 方法 + 状态变量 |
| 任何项目的命名（snake_case 变量等） | Go 标准命名 + Unio 现有命名风格 |

**代码评审一票否决标准**：把作者名遮起来，这段代码看起来像 Unio 模块、像 codex-relay 翻译版，
还是像 LiteLLM 翻译版？答案必须恒等于"Unio 模块"。

字段映射处可在 `RESPONSES_CHAT_BRIDGE.md` 标注参考来源（如 "对照 LiteLLM transformation.py L###"），
便于审计与责任追溯，但**不在 `.go` 文件代码里留这些标注**。

完整字段映射矩阵、Codex 实际 wire 行为与已知坑见
[RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)。完整能力声明见
[CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md)。OpenAI Responses 官方协议字段树与 SSE 事件示例
见 [docs/protocol/openai/responses/official.md](../../protocol/openai/responses/official.md)（本阶段权威字段来源）。

## 目标目录

`cmd/gateway-server` 仍是一个进程。Responses 与 Chat/Messages 的分类发生在
`gatewayapi`、`service/gateway` 内部。

```text
internal/
├── app/gatewayapi/
│   ├── router.go                        # 新增 POST /v1/responses 挂载
│   └── openai/
│       ├── chatcompletions/             # 不动
│       ├── models/                      # 不动
│       └── responses/                   # 新增 operation 子包
│           ├── handler.go               # ServeHTTP：decode → service → JSON / SSE
│           ├── dto.go                   # Responses 请求/响应/事件 DTO
│           ├── decode.go                # 双轨 decode（typed + 保留未知合法字段）
│           ├── validation.go            # Responses 协议结构校验
│           ├── stream.go                # 命名 SSE 事件帧编码
│           └── response.go              # Responses 原生 error shape 渲染
│
├── service/gateway/
│   ├── lifecycle/                       # 复用；如需 genericize skeleton 见 TASK-11.06
│   └── openai/
│       ├── chatcompletions/             # 不动
│       └── responses/                   # 新增 operation 子包
│           ├── service.go               # ResponsesService（operation=responses）
│           ├── responses.go             # 非流式编排
│           ├── responses_stream.go      # 流式编排 + Chat chunk → Responses 事件状态机
│           ├── responses_chat_map.go    # Responses 请求 → openai.ChatRequest
│           └── responses_response_map.go# openai.ChatResponse → Responses 响应
│
└── core/
    ├── adapter/openai/                  # 完全复用，不新增 responses adapter
    └── requestlog/service.go            # 新增 OperationResponses = "responses"
```

bootstrap：

```text
internal/bootstrap/gateway.go           新增 NewResponsesGateway（复用 chatRouter + registry.OpenAI + 同一套 lifecycle 依赖）
internal/bootstrap/gateway_server.go    装配 responsesService
internal/bootstrap/http.go              RouterDeps 增加 ResponsesService
```

## 数据模型改造

本阶段 **不改账务/usage/价格 schema**。唯一改动：

```text
internal/core/requestlog/service.go
  新增 Operation 常量 OperationResponses = "responses"
```

`request_records.operation` 是字符串列，已能容纳新值（无需 migration）。Responses 请求写入：

```text
ingress_protocol = openai
operation        = responses
response_protocol= openai   # 上游仍是 chat completions
```

如果 `operation` 列存在 CHECK 约束或 enum，则需要一条改列 migration（实施前用
`rg -n "operation" migrations sql` 确认；当前为自由字符串则免改）。

## 参数转换概览

完整矩阵见 [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)。这里给出骨架：

请求（Responses → 内部 ChatRequest）：

```text
input(string)                     → messages[user]
input(items[])                    → messages[]，其中：
  message(role,content[input_text/output_text]) → 对应 role 的 message（content 扁平化为文本/parts）
  function_call(call_id,name,arguments)         → assistant.tool_calls[{id=call_id,function}]
  function_call_output(call_id,output)          → tool message(tool_call_id=call_id, content=output)
  reasoning(...)                                → 跨轮 reasoning_content（best-effort，见 GAP-11-003）
instructions                      → 顶部 system/developer message
max_output_tokens                 → max_tokens / max_completion_tokens
temperature / top_p / user        → 原样
parallel_tool_calls               → ChatRequest.ParallelToolCalls（DeepSeek adapter 出站 Drop）
tools[{type:function,name,...}]   → tools[{type:function,function:{name,...}}]（扁平→嵌套）
tools[{type:custom/grammar}]      → 见 GAP-11-002（convert→function 才能在 DeepSeek 存活，否则 adapter dropCustomTools 整条 Drop）
tools[{type:web_search/...}]      → 内置工具，本阶段桥接层 Drop（无真实执行）
tool_choice(string / {type:...})  → 归一为 auto/none/required/具名 function
reasoning:{effort}                → ChatRequest.ReasoningEffort（DeepSeek 行为见 TASK-11.09）
text:{format}                     → ChatRequest.ResponseFormat（json_schema 由 DeepSeek adapter 出站 Drop）
text:{verbosity}                  → ChatRequest.Verbosity（DeepSeek adapter 出站 Drop）
store / prompt_cache_*            → ChatRequest.Store / PromptCache*（DeepSeek adapter 出站 Drop，不在桥接层硬 Drop）
previous_response_id / include / truncation / item_reference → 无 ChatRequest 承载字段，桥接层 Drop（无状态）
stream                            → 原样（Codex 恒为 true，并强制 include_usage）
```

> 红线：桥接层只做协议结构翻译，能映射进 `ChatRequest` 契约的字段一律 Adapt 进契约，provider 能力
> 裁剪交给 DeepSeek adapter 出站 `dropUnsupported`（见「## 上游与 Drop 职责划分」）。只有契约里无处
> 安放的 Responses 专属字段才在桥接层 Drop。

响应（内部 ChatResponse/Chunk → Responses）：

```text
非流式 ChatResponse → response{ id:resp_*, object:"response", status, model, output:[...], usage }
  reasoning_content → output 项 {type:"reasoning", summary/content:[...]}
  content           → output 项 {type:"message", role:"assistant", content:[{type:"output_text",text}]}
  tool_calls        → output 项 {type:"function_call", call_id, name, arguments}
usage 字段名         prompt_tokens→input_tokens / completion_tokens→output_tokens / total_tokens
                    cached→input_tokens_details.cached_tokens / reasoning→output_tokens_details.reasoning_tokens
finish_reason        → status(completed/incomplete) + incomplete_details
流式 ChatStreamChunk → 命名事件状态机（response.created → output_item.added → output_text.delta
                       / reasoning_summary_text.delta / function_call_arguments.delta → ...done → response.completed）
```

## 分节实施计划

每节只完成一个稳定边界。协议参考冻结、模型策略、ingress、请求翻译、响应翻译、流式状态机、
工具/reasoning、错误、装配、黑盒分开推进，不混成一次大改。

<a id="task-11-01-protocol-freeze"></a>
### TASK-11.01 Responses 协议参考与字段冻结

状态：done（协议快照补齐 + 桥接全字段冻结；真实 Codex v0.130 抓包 + codex-rs 源码交叉确认，残留全清零）

目标：

```text
完整冻结 docs/protocol/openai/responses/official.md 参考快照，并把 RESPONSES_CHAT_BRIDGE.md 的每个字段
冻结为 Pass / Adapt / Drop / Reject。
```

已落地（协议快照三文件）：

- ✅ `POST /responses` 完整 body parameters + Returns + 9 段示例：[docs/protocol/openai/responses/official.md](../../protocol/openai/responses/official.md)
- ✅ 完整 streaming events 目录（53 种 + 字段 + lifecycle recipes + Unio×DeepSeek 桥接相关性分级）：[docs/protocol/openai/responses/official-streaming-events.md](../../protocol/openai/responses/official-streaming-events.md)
- ✅ 其余 5 个 endpoint（`compact` / `input_tokens` / retrieve / delete / input_items / cancel）语义 + Unio 处理 + error schema + `compaction` item 形状：[docs/protocol/openai/responses/official-other-endpoints.md](../../protocol/openai/responses/official-other-endpoints.md)
- ✅ BRIDGE 矩阵 §1~§7 结构可定字段全部冻结（§6 `sequence_number` 已冻结为 Adapt）

> 来源说明：官方 `developers.openai.com` API reference 抓取超时，streaming events / error code 用
> OpenAI Developer Community 整理 + Alibaba Cloud 真实 wire dump + 官方 streaming 指南三源交叉印证，
> provenance 与残留 `Verify` 在各协议子文件顶部标注。

残留项 —— **已全部清零（真实抓包 + codex-rs 源码，见 [BRIDGE §9](RESPONSES_CHAT_BRIDGE.md)）**：

- ✅ §6 reasoning 流式事件名：冻结为 `response.reasoning_text.*`（`content_index` / `content:[{type:"reasoning_text"}]`，开源模型语义）。
- ✅ `compact` 请求体 `CompactionInput` / 响应 `{output:[ResponseItem]}`；`input_tokens` 请求子集已确认。
- ✅ `input_tokens` 返回 `object` 字段名 = `response.input_tokens`（SDK 类型确认）。
- ✅ Codex 实际请求字段子集 + 新发现（`client_metadata`/`namespace`/全 function 工具/事件消费子集），fixture 已存档。

实现内容：

1. ✅ **本任务以协议快照为权威字段来源**（不再 page-fetch 官方文档）；缺失的 5 个 endpoint 与完整
   streaming events 目录已拆同目录子文件补齐。
2. ✅ 已抓真实 Codex v0.130.0 `/responses` 请求体 fixture（`internal/blackbox/fixtures/codex/`），
   对照协议快照识别 Codex 实际字段子集 + 专属坑；输出侧细节用 codex-rs 源码交叉确认。
3. ✅ 对请求字段、input/output item 与流式事件给出 Pass/Adapt/Drop/Reject 策略（结构可定部分），
   填进 [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)。
4. ✅ 标注 Codex 专属坑（`custom`/grammar、`local_shell`、`namespace`、`client_metadata`、
   `reasoning.summary`、`text.verbosity`、`store=false` 无状态假设）于 BRIDGE §8。
5. ✅ 字段映射在 BRIDGE 与协议子文件标注参考来源/provenance（含 codex-rs 源码路径），便于审计。
6. ✅ 冻结完成，可开始翻译层生产代码（TASK-11.04 起）。

验收：

- ✅ [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md) 无残留结构性 `Verify`（§9 残留全部清零）。
- ✅ 协议快照覆盖本阶段 5 个 endpoint 字段 + 全部流式事件类型。
- ✅ Codex 真实抓包 fixture 存档可复跑（脱敏待 TASK-11.15）。

<a id="task-11-02-model-spec"></a>
### TASK-11.02 模型指定与路由策略冻结

状态：done（方案 A 冻结在 DEC-014；5 决策点结论见上「模型指定策略」；routing 无 responses 特例；seed 落地并入 TASK-11.09）

目标：

```text
冻结“客户 model → 受支持上游模型”的指定方式，落为 DEC，并确认 routing/catalog 无需 Responses 特例。
```

实现内容：

1. 按“## 模型指定策略”确认 5 个决策点（别名、默认模型、reasoning 模型、未知模型行为、/v1/models 暴露）。
2. 冻结推荐方案 A（客户 model = Unio 模型目录 model_id），并在 seed/后台配好可路由到 DeepSeek 的
   Responses 可用模型（如 `deepseek-chat` / `deepseek-reasoner` 或品牌别名）。
3. 校验 routing 在 `IngressProtocol=openai` 下对 Responses 请求与 chat 请求行为一致；未知模型返回
   Responses `model_not_found`，不静默替换。
4. 决定是否引入模型别名表（若引入则登记 GAP-11-006 与对应能力）。

验收：运营可声明“哪些模型可用于 Codex”，Responses 请求按既有 routing/计费事实命中正确上游。

<a id="task-11-03-decision-operation"></a>
### TASK-11.03 DEC-014 决策与 operation 审计枚举

状态：done（DEC-014 accepted；`requestlog.OperationResponses` 已加；migration 000009 放开 operation CHECK 并 scratch 库验证通过，sqlc 无 drift。dev 本地库待 drop→up 重建）

实现内容：

1. 在 DECISIONS.md 写入 DEC-014（responses-to-chat bridge、无状态生产范围、账务复用）。
2. `requestlog` 新增 `OperationResponses = "responses"`；确认 `operation` 列无需 migration
   （或补一条改列 migration）。
3. metrics 与 request log 能按 operation 区分 responses。

验收：Responses 请求在 `request_records.operation` 与 metrics 中可与 chat/messages 区分。

<a id="task-11-04-responses-ingress"></a>
### TASK-11.04 Responses ingress DTO / decode / validation

状态：done（`internal/app/gatewayapi/openai/responses/` 子包落地：dto/tools/stream_dto/decode/content/validation/response + decode/validation/error 单测全过，vet/build/lint 干净）

实现内容：

1. `gatewayapi/openai/responses/dto.go`：Responses 请求顶层字段、`input` union（string |
   items[]）、各 input item 类型、tools（含 function / 内置 / custom）、`reasoning` / `text`
   对象、以及非流式 `response` 响应与流式事件 DTO。
2. `decode.go`：双轨 decode，合法但本阶段不消费的字段进入 extension 桶（DEC-012：decode 不丢字段）。
3. `validation.go`：只校验 Responses 协议结构合法性（`input` 必填、item union 合法、tool union
   合法），非法返回 Responses 原生 400；不因 provider 能力 400。
4. `response.go`：Responses 原生 error shape（`{"error":{"type","code","message","param"}}`，
   `object` 语义与 Responses 一致）。

验收：合法 Codex 请求体能完整 decode 进 typed DTO；非法结构返回 Responses 原生 400。

<a id="task-11-05-request-bridge"></a>
### TASK-11.05 Responses → Chat 请求翻译

状态：done（`internal/service/gateway/openai/responses/responses_chat_map.go` + `responses_content_map.go`：请求映射、messages 组装、tools 扁平/拍平、tool_choice 归一、reasoning/text 映射、契约无承载字段 Drop；13 组单测 + 真实 v0.130 fixture 端到端翻译通过，vet/build/gofmt/lint 干净）

实现内容：

1. `responses_chat_map.go`：实现请求概览中的全部请求映射，产出内部 `openai.ChatRequest`。
2. `input` items → messages：连续 `function_call` 合并进单条 assistant；`function_call_output`
   生成 tool message 并按 `call_id` 对齐；`instructions` 注入顶部 system。
3. tools 扁平→嵌套；`tool_choice` 归一；`reasoning.effort→reasoning_effort`；`text.format→response_format`。
4. Responses 专属无状态字段 Drop 并记内部审计（dropped_request_fields）。
5. Codex `custom`/grammar 工具与 `local_shell` 按 TASK-11.01 冻结策略处理（见 GAP-11-002）。

验收：翻译产物与 `chatcompletions` 既有 adapter 契约一致，可直接喂给 `openai.ChatAdapter`。

<a id="task-11-06-nonstream-orchestration"></a>
### TASK-11.06 非流式编排与响应翻译

状态：done（采纳方案 B：抽出共享 `lifecycle.AttemptRunner`，OpenAI chat 与 responses 复用同一份资金关键候选 fallback 循环；Anthropic Messages 保留自身循环。`internal/service/gateway/openai/responses/service.go` 装配 `ResponsesService`（router/registry/candidates/retryClassifier/requestLog/chatSettlement/chatAuthorizer/metrics/breaker，lifecycle 用 `Operation=OperationResponses`、`IngressProtocol=ProtocolOpenAI`）；`create_response.go` 经 routing→authorization→`AttemptRunner.RunNonStream`(ResolveAdapter 复用 `openai.ChatAdapter`、Invoke 内做 responses→chat 请求翻译并发起一次上游调用)→settlement；`responses_response_map.go` 把 `openai.ChatResponse` 翻译回 Responses `response` 对象。`go build ./...` 与 `go test ./internal/... ./cmd/...` 全过，0 失败。）

实现内容：

1. `service.go` / `responses.go`：构造 `ResponsesService`，复用与 `ChatCompletionService`
   相同的依赖（router / registry.OpenAI / candidates / retryClassifier / requestLog /
   chatSettlement / chatAuthorizer / metrics / breaker），lifecycle bundle 使用
   `Operation=OperationResponses`、`IngressProtocol=ProtocolOpenAI`。
2. 编排走 routing(protocol=openai) → authorization → attempt → `openai.ChatAdapter` →
   settlement → recovery，与 `chat_completion.go` 同构。
3. `responses_response_map.go`：`openai.ChatResponse` → Responses `response` 对象
   （output items + usage 字段名转换 + status 映射）。

> 编排骨架与 `chatcompletions` 高度同构（项目已接受 chat/messages 各有一份 operation 骨架）。
> 为避免出现第三份近似复制的计费关键循环，本任务先评估把 OpenAI 协议族的候选循环抽成可复用
> invoker（ARCHITECTURE §7 的 `AttemptInvoker`/`StreamAttemptInvoker` 模式）；若抽取成本过高，
> 允许先镜像骨架并登记后续 genericize（GAP-11-005）。

验收：非流式 Responses 请求端到端成功，`ResponseFacts`/settlement 与 chat 路径事实一致。

<a id="task-11-07-stream-statemachine"></a>
### TASK-11.07 流式事件状态机

状态：done（`stream_response.go` 走共享 `AttemptRunner.RunStream`，与 chat 流式同一份资金关键链路（emitted 后禁止 fallback、final usage 缺失处理、tail-error 仍尽力结算）；`responses_stream.go` 的 `streamEncoder` 把内部 `openai.ChatStreamChunk` 翻译成 codex-rs 实测消费子集：`response.created`→`output_item.added`→`output_text.delta`/`reasoning_text.delta`/`function_call_arguments.delta`→`output_item.done`（权威载体）→`response.completed`，单调 `sequence_number`、稳定 output_index、并行 tool_call 分项、MCP namespace 回填。SSE 帧编码在 `gatewayapi/openai/responses` handler；HTTP handler stream 用例已改为真 SSE 断言。测试全过。规范完整事件（`in_progress` / `content_part.*` / `output_text.done` / `*.done`(reasoning) / `function_call_arguments.done`）当前未发，Codex 不依赖，标准 SDK 完整性补齐登记 GAP-11-011。）

实现内容：

1. `responses_stream.go`：把内部 `openai.ChatStreamChunk` 流翻译成 Responses 命名事件序列：
   `response.created` → `response.output_item.added` → `response.output_text.delta`（文本）/
   `response.reasoning_text.delta`（reasoning_content，带 `content_index`）/
   tool_calls delta 累积 → **`response.output_item.done`（权威载体：完整 message / reasoning
   `content:[{type:"reasoning_text"}]` / function_call `{call_id,name,arguments}`）** →
   `response.completed`（含完整 response + usage）。
   - codex-rs 源码确认 Codex 仅消费上述事件子集；`in_progress` / `content_part.*` / `output_text.done` /
     `*.done`(reasoning) / `function_call_arguments.done` 对 Codex 可选，**当前未发**（标准 SDK 完整性补齐留 GAP-11-011）。
2. 每个事件带单调 `sequence_number`；item/content_part 索引稳定。
3. tool_calls 跨 chunk 增量累积成完整 `function_call` item（**最终 args 必须落在 `output_item.done`**）；
   并行 tool_call 正确分项；MCP `namespace` 工具回填 `namespace` 字段（BRIDGE §3.3）。
4. 复用共享 stream lifecycle：首个客户可见事件前允许 fallback、之后禁止；有可靠 usage 的 tail
   error 仍 settlement 并记 delivery interrupted；recovery facts 未持久化不写成功终态
   （`response.completed`），改写 Responses `error` / `response.failed` 事件。
5. `gatewayapi/openai/responses/stream.go`：命名事件 SSE 帧编码（`event:` + `data:`）。

验收：Codex 能消费完整事件流并正确渲染增量文本、reasoning 与工具调用；终态账务与 chat 流一致。

<a id="task-11-08-tools-reasoning"></a>
### TASK-11.08 工具、reasoning 与 text 特殊处理

状态：done（核心结构翻译完成；残留 fidelity 按 GAP 跟踪，见下）

落地：`responses_chat_map.go` 已实现 function 扁平→嵌套 function tool、namespace 拍平 `<ns><name>`、
内置/custom/local_shell Drop 记审计、`tool_choice` 各形态归一、`reasoning.effort`→`reasoning_effort`、
`text.format`→`response_format`（json_schema 提 schema）+`text.verbosity`→`verbosity`；
`responses_response_map.go` 出站把 MCP 拍平名回译为 namespace+name。残留：namespace 回译保真度
（GAP-11-002）、跨轮 reasoning 回灌 DeepSeek（GAP-11-003，第一版不回灌）、json_schema strict 细节，
均不阻断链路，留 TASK-11.09 真实负载复核时确认。

实现内容：

1. `custom`/grammar 工具（Codex apply_patch freeform）按冻结策略 convert→function（单 string 参数）
   或 Drop；`local_shell` 内置工具处理。
2. `tool_choice` 各形态（string / `{type:auto|none|required|tool|function}`）归一。
3. `reasoning.effort` → `reasoning_effort`，上游不支持时 adapter 出站 Drop；`reasoning.summary`
   策略冻结。
4. `text.format`（json_schema / json_object）→ `response_format`；`text.verbosity` → `verbosity`。
5. 跨轮 reasoning item 透传 best-effort（GAP-11-003）。

验收：Codex 工具调用闭环（function_call → function_call_output 续接）端到端可用。

<a id="task-11-09-deepseek-upstream"></a>
### TASK-11.09 DeepSeek 上游适配复核与 Codex 工作负载验证

状态：partial（首要项 reasoning_effort drift 已修正并关闭 GAP-11-010；真实 smoke 与模型 seed 待办）

已完成：
- **item 1 reasoning_effort（首要项，已闭环）**：以 DeepSeek 官方 API 文档为权威依据——`reasoning_effort`
  possible values 仅 `high`/`max`，DeepSeek 自带 `low`/`medium`→`high`、`xhigh`→`max` 兼容映射；Codex（gpt-5
  家族）另发 `minimal`，不在 DeepSeek 枚举内。决策取 Adapt（非裸 pass-through）：在 `deepseek/adapt.go`
  `normalizeReasoningEffort` + `drop.go` 出站归一为 `high`/`max`（minimal/low/medium/high→high，
  xhigh/max→max，未知枚举 Drop），不依赖上游隐式兼容、杜绝 minimal 触发 422。adapt_test 覆盖。
- **item 2 职责边界复核**：桥接层 `responses_chat_map.go` 只把 `reasoning.effort` Adapt 进
  `openai.ChatRequest` 契约，不做 provider 归一；归一只在 DeepSeek adapter 出站。两层不重不漏，边界正确。
- **item 6 Phase 10 doc drift 同步修正**：Phase 9 `DEEPSEEK_UPSTREAM.md`（从 pass-through 移到 adapted）
  与 Phase 10 `DEEPSEEK_OPENAI_MAPPING.md`（补 minimal→high、未知 Drop、标注已实现）已与代码一致，
  GAP-11-010 关闭。
- **thinking opt-in 闸门（DEC-016）**：DeepSeek thinking 默认 enabled，Codex 发 `reasoning:null`
  （effort none）的非 reasoning run 会触发 DeepSeek CoT（额外成本）。已实现 opt-in 语义：桥接层
  reasoning 缺省/null → `ChatRequest.ReasoningDisabled`（provider 无关内部意图标志，永不序列化进 wire）；
  DeepSeek adapter 据此出站注入 `thinking:{type:"disabled"}` 并 Drop 矛盾 reasoning_effort，客户显式
  thinking 不覆盖、chat ingress 不受影响（不回归 DS-04 默认 enabled）。桥接层不直塞私有 thinking 以防
  泄漏到真 OpenAI 上游（base adapter 无 extension 白名单）。bridge + adapter 均有单测。

待办：
- **item 5 模型落地**：seed/运营配置一个可用于 Codex 的 DeepSeek 模型，经 routing(IngressProtocol=openai)
  命中 `upstream_model`。
- **真实 smoke**：真实 Codex CLI → Unio → 真实 DeepSeek 端到端（含 reasoning_effort 归一与 thinking
  on/off 行为，需密钥，gate `DEEPSEEK_BLACKBOX=1`），归 TASK-11.15 执行。

首版具体上游是 DeepSeek，复用已实现并经 Phase 10 验收的 `adapter/openai/deepseek`，不重写。
本任务把它显式纳入计划并复核其在 Responses/Codex 负载下的行为。

实现内容：

1. **首要验证项：`reasoning_effort` 在 DeepSeek 上的真实行为**（高风险，Codex 100% 命中）。
   背景：Phase 10 `DEEPSEEK_OPENAI_MAPPING.md` 标 `reasoning_effort` 为 Adapt（"low/medium→high,
   xhigh→max 枚举映射"），但实际代码 `request_wire.go` 是裸 pass-through，`drop.go` 也没列入
   drop 清单。Phase 10 chat/completions 流量几乎不带这个字段，所以没真正触发；Phase 11 Codex
   每个请求都带 `reasoning.effort`，**这是 Phase 10 doc/code drift 的首次实战触发**（GAP-11-010）。
   动作：黑盒打 DeepSeek 验证三种结果——
     A) DeepSeek 默默接受并忽略 → 现状 pass-through 即可，仅修 mapping doc 与代码一致；
     B) DeepSeek 报 400 → 在 `dropUnsupported` 增加 `reasoning_effort`（约 1 行）+ 测试；
     C) 部分值接受部分 400 → 在 `adapt.go` 加 enum 归一（约 10~20 行）+ 测试。
2. 职责边界复核：确认桥接层只做协议结构翻译、把字段映射进 `openai.ChatRequest`；provider 能力
   Drop 全部由 DeepSeek adapter 出站 `dropUnsupported` 负责，两层不重复 Drop、不漏 Drop
   （对照「## 上游与 Drop 职责划分」与 [DEEPSEEK_OPENAI_MAPPING.md](../phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md)）。
3. reasoning 输出复核：`deepseek-reasoner` 的 `reasoning_content` 透传（已是白名单 extension
   `thinking`，Phase 9/10 已实现），在 Responses 流式 `reasoning_text.delta`（content_index）路径下
   复跑一次确认无回归。
4. 工具复核：function tool 在 DeepSeek 上的真实调用闭环。v0.130 实测 `apply_patch` 经 `exec_command`
   function 走、不发 custom 工具；重点复核 MCP `namespace` 工具拍平 + `namespace` 回填闭环（GAP-11-002）。
5. 模型落地：seed/运营配置 DeepSeek channel + 可用于 Codex 的 model（`deepseek-chat` /
   `deepseek-reasoner`），经 routing(`IngressProtocol=openai`) 命中对应 `upstream_model`（衔接 TASK-11.02）。
6. **Phase 10 doc drift 同步修正**：本任务收尾时把 `DEEPSEEK_OPENAI_MAPPING.md` 里的
   `reasoning_effort` 一行改成与代码一致的最终状态（Pass / Drop / Adapt 其中之一），关闭 GAP-11-010。
7. 不重写 DeepSeek adapter；仅在确有 Responses/Codex 专属缺口时，按既有出站 Drop 模式补规则与测试
   （改动生产代码须用户授权）。

**真实预期**：`reasoning_effort` 大概率 1~20 行小补丁；其余 0 行改动。即"DeepSeek adapter 大体不动"，
但不是完全不动——前面 PLAN 把它一笔带过会藏起这个 100% 命中风险，本任务的价值就是把它暴露出来。

验收：Responses/Codex 流量经桥接层 → DeepSeek adapter 出站后 wire 合法、能力 Drop 正确、reasoning 与
tool 行为符合预期；真实 Codex + DeepSeek smoke 跑通。

<a id="task-11-10-error-security"></a>
### TASK-11.10 错误与安全输出

状态：done

落地：`handler.go` `mapResponsesServiceError` 复用 `adapter.UpstreamCategoryOf`，把 insufficient_quota /
unsupported_parameter / model_not_found / model_unavailable / 上游 rate_limit/timeout/bad_request/其它
分别渲染为 Responses 原生 error（429/400/404/503/504/502 等），上游 auth/permission 绝不渲染成客户 401，
不透传上游原始 body；SSE 已开始后写带外 `event:error`（`writeResponsesStreamError`），不回退 JSON；
`response.go` 统一 decode/validation 400 与 501/400 专属拒绝码渲染。

实现内容：

1. 复用 `adapter.UpstreamCategoryOf` 稳定分类，渲染 Responses 原生 error（429/400/504/502 策略
   与 chat/messages 一致：上游 auth/permission 绝不渲染成客户 401）。
2. SSE 开始后写 Responses `error` / `response.failed` 事件，不回退普通 JSON。
3. 上游原始 body、credential、prompt、完整响应正文不进日志/审计/错误 fields。

验收：错误分类、HTTP status 与安全不泄漏行为与 Phase 10 双协议一致。

<a id="task-11-11-compact"></a>
### TASK-11.11 `POST /v1/responses/compact` 无状态降级实现

状态：done（第一版降级；不含第三方上游专属调优，GAP-11-007）

落地：ingress `endpoints_handler.go` `NewResponsesCompactHandler`（复用 `ResponsesRequest`
decode/validation，CompactionInput 是其子集）+ service `compact_response.go` `CompactHistory`。
实现把 `input[]`(历史)+`instructions`(缺省注入 `defaultCompactionInstruction`) 当作一次可计费非流式
chat，复用 `executeNonStreamChat` 走 routing/authorization/`AttemptRunner`/settlement；摘要包成
`{"output":[{type:"message",role:"assistant",content:[{type:"output_text",text:<摘要>}]}]}`。
第一版用 message item 承载（Codex 回传即按普通 message 解析，透明往返），不签发 compaction 密文 item。
测试：`compact_input_tokens_test.go`（happy + 默认指令注入）+ `endpoints_handler_test.go`（200 / 校验 400）。

背景：Codex 长会话超 `auto_compact_limit` 时会主动调 `/responses/compact`。OpenAI 官方做法依赖
服务端 `encrypted_content`（用 OpenAI 密钥加解密），DeepSeek 结构上做不到等价实现。社区大多数
代理直接不做这个 endpoint，导致 Codex 长会话压缩点报错。Unio 第一版用**无状态翻译降级实现**：
压缩点本质是 "context 摘要 + 一个可回传的 opaque item"，用 DeepSeek 做一次摘要请求即可。

**请求/响应形状（codex-rs 源码确认）**：请求体 `CompactionInput {model, input:[ResponseItem],
instructions, tools:[], parallel_tool_calls, reasoning?, text?}`；响应是
`{"output":[ResponseItem,...]}`（`CompactHistoryResponse`，**非完整 response 对象、非 SSE**，
Codex 把 `output` 直接当作压缩后的新历史）。

实现内容：

1. 落点 `gatewayapi/openai/responses/compact_handler.go` + `service/gateway/openai/responses/compact.go`。
2. 翻译：解析 `CompactionInput`，把 `input[]` + `instructions` 拼成单条 Chat 请求
   （system = instructions，如 "总结以下上下文..."），调用既有 OpenAI ChatAdapter
   （与 `/v1/responses` 主路径共用 routing/lifecycle/计费）。
3. 合成响应 `{"output":[ResponseItem]}`：第一版返回单条 message item 承载摘要
   `{type:"message", role:"assistant", content:[{type:"output_text", text:<摘要>}]}`；
   或自编码 `{type:"compaction", id:"cmp_<gen>", encrypted_content:"<unio-opaque-token>"}`
   （仅 Unio 可解，不模拟 OpenAI 密文）。
4. 下一轮 `/v1/responses` 请求里如果出现回传的 `type:"compaction"` item，桥接层在
   `responses_chat_map.go` 还原成一条 summary system message（透明往返）。
5. 计费：compact 调用是一次真实 DeepSeek chat 请求，走完整 lifecycle / settlement / 价格快照 /
   成本快照，不走免费 fast path。
6. 错误：DeepSeek 摘要失败时返回 Responses 原生 error；不静默 fallback。

边界与限制（GAP-11-007）：

- 不等价于 OpenAI 原生 `compact`（无加密、无服务端状态、压缩质量取决于 DeepSeek 摘要）。
- 多轮反复压缩可能累积信息损失，CAPABILITY_MATRIX 需明示。
- 不支持 `context_management.compact_threshold` 服务端自动压缩（仅响应客户显式调用）。

验收：Codex 长会话触发 compact → Unio 返回合法 `{"output":[ResponseItem]}` → Codex 用返回的
压缩历史继续会话 → 内容连续可对话；账务两次请求都正确入账。

<a id="task-11-12-input-tokens"></a>
### TASK-11.12 `POST /v1/responses/input_tokens` 本地估算实现

状态：done（本地估算，GAP-11-008）

落地：ingress `endpoints_handler.go` `NewResponsesInputTokensHandler` + service `input_tokens.go`
`CountInputTokens`。用 `router.PlanChat`(IngressProtocol=openai) 解析模型→候选只为选取 tokenizer，
取候选 `AdapterKey` 的 `ChatInputTokenizer`，把请求经 `mapResponsesRequestToChat` 翻成 ChatRequest
后估算，返回 `{"input_tokens":<N>,"object":"response.input_tokens"}`。不走候选 fallback、不调上游、
不计费、不写请求审计。测试：`compact_input_tokens_test.go`（估算值 + 无上游/无计费/无审计 + routing 错误传播）
+ `endpoints_handler_test.go`（200）。

背景：Codex 偶尔预检 token 数（用于 `auto_compact_limit` 时机判断）。官方做法是服务端精确计数；
Unio 用本地 tokenizer 估算即可——精度跟 Codex 自身本地估算同量级，绝对不够时通过 `/responses`
的真实 usage 校正。**也可以选择不实现返回 501**（Codex 会回退本地估算）；第一版采纳"本地估算"
是为商业体验，避免客户撞到 501 觉得"网关有缺陷"。

实现内容：

1. 落点 `gatewayapi/openai/responses/input_tokens_handler.go` +
   `service/gateway/openai/responses/input_tokens.go`。
2. 复用 `responses_chat_map.go` 把请求 `input` 翻译为内部 `openai.ChatRequest`。
3. 路由到 OpenAI registry 取对应 channel 的 `ChatInputTokenizer`（DeepSeek adapter 已实现），
   估算 prompt token 数。
4. 返回 `{object:"response.input_tokens", input_tokens: <N>}`。
5. **不走 routing 候选选择、不调上游、不计费**：只用 tokenizer 估算，是协议无关的本地能力。

边界与限制（GAP-11-008）：

- 与 OpenAI 服务端精确计数有偏差（与 Codex 本地估算同量级）。
- 不反映上游 prompt cache 命中带来的实际 prompt token 折扣。

验收：合法 Responses input 进入，返回估算 token 数；非法结构返回 Responses 原生 400；不产生
账务事件、不产生 upstream 调用。

如未来决定改回 501（更"诚实"），只需把 handler 改为返回 `unsupported_endpoint` error，1 处改动。

<a id="task-11-13-unsupported-endpoints"></a>
### TASK-11.13 有状态 endpoint 501 与异步任务 Reject

状态：done（metrics counter 见说明）

落地：ingress `endpoints_handler.go` `NewResponsesStatelessUnsupportedHandler`（serviceless 静态
handler）对 `GET/DELETE /responses/{response_id}`、`GET /responses/{response_id}/input_items`、
`POST /responses/{response_id}/cancel` 统一返回 501 `unsupported_endpoint_stateless`；`validation.go`
对 `POST /responses` 带 `background:true` 返回 400 `unsupported_background`（`response.go` 支持专属
拒绝码）。测试：`endpoints_handler_test.go` + `router_test.go`（4 个有状态路由 501 + background 400）。
注：第 4 点独立 metrics counter 暂未加（501/400 已计入 HTTP 层通用指标），留 TASK-11.16 评估是否补。

实现内容：

1. `gatewayapi/openai/responses/router.go`（或在主 router.go）挂以下 endpoint，全部返回 Responses
   原生 error（HTTP 501 + `code:"unsupported_endpoint_stateless"`）：
   - `GET /v1/responses/{response_id}`
   - `DELETE /v1/responses/{response_id}`
   - `GET /v1/responses/{response_id}/input_items`
   - `POST /v1/responses/{response_id}/cancel`
2. `/v1/responses` 收到 `background:true` 时 Reject（HTTP 400 + `code:"unsupported_background"`,
   明确报错而非静默转同步）。
3. error message 引导客户：本阶段无状态商业承诺，请每轮回传完整 `input`（与 Codex 默认行为一致）。
4. metrics：501 / Reject 计入独立 counter，方便观测真实调用面。

验收：endpoint 都返回稳定的协议原生错误；不调用上游；不产生账务。

<a id="task-11-14-bootstrap-wiring"></a>
### TASK-11.14 bootstrap 装配与路由

状态：done（`GET /v1/models` 的 `models` 形状兼容留 TASK-11.09，开发期抓包已验证，见下）

落地：`bootstrap/gateway.go` `NewResponsesGateway` 复用 `chatRouter`+`registry.OpenAI`+同套
settlement/authorization/breaker/metrics；`router.go` 在 `/v1` 下挂全部 `/responses*` 路由：
`POST /responses`、`POST /responses/compact`、`POST /responses/input_tokens`、
`GET/DELETE /responses/{response_id}`、`GET /responses/{response_id}/input_items`、
`POST /responses/{response_id}/cancel`，复用 API key auth + rate limit middleware。`router_test.go`
验证 chi 静态/参数路由共存无冲突、各路由可达。第 4 点 `GET /v1/models` 的 `{"models":[...]}` 兼容
已在开发期抓包验证为非致命，正式 endpoint 的兼容改造留 TASK-11.09 衔接（不破坏标准 OpenAI 契约）。

实现内容：

1. `bootstrap/gateway.go` 新增 `NewResponsesGateway`（复用 `chatRouter` + `registry.OpenAI` +
   同一套 settlement/authorization/breaker/metrics 依赖）。
2. `gateway_server.go` 装配 `responsesService`；`http.go` `RouterDeps` 增加 `ResponsesService`。
3. `router.go` 在 `/v1` 下挂 `POST /v1/responses`，复用 API key auth + rate limit middleware。
4. 确认 Codex 把 `base_url` 设为 `<unio>/v1`、`api_key` 为 Unio key 后能 drop-in（含
   `GET /v1/models` 已存在）。
   - ⚠️ 真实抓包发现（v0.130.0）：Codex 刷新模型时期望 `GET /v1/models` 返回 `{"models":[...]}`，
     而标准 OpenAI / Unio 现有实现返回 `{"object":"list","data":[...]}`，导致 Codex 日志
     `failed to decode models response: missing field "models"`。本次为**非致命**（Codex 报错后
     仍继续发 `/responses`），但影响 drop-in 体验。落点：评估为 Codex 兼容补 `models` 形状
     （或单独 endpoint / UA 嗅探），不破坏标准 OpenAI `/v1/models` 契约。

验收：进程启动 preflight 通过；`POST /v1/responses` 命中 OpenAI channel。

<a id="task-11-15-blackbox"></a>
### TASK-11.15 黑盒验收

状态：partial（mock 黑盒 + gated 真实 smoke 已落地 `internal/blackbox/openaisdk/responses_*_test.go`，`-tags=blackbox` 编译通过、无环境正确 Skip；待真实 DB/Redis 跑 mock 黑盒、`DEEPSEEK_BLACKBOX=1` 跑真实 DeepSeek smoke 与 Codex CLI 手测）

实现内容：

1. mock 上游：在 `internal/blackbox/` 用 Responses 请求 fixture（真实 Codex 抓包）覆盖
   非流式 / 流式 / reasoning / tools 多轮 / custom 工具 / 错误映射 / settlement DB 事实链路。
2. 流式事件序列断言：事件类型、顺序、`sequence_number` 单调、usage 在 `response.completed`。
3. 真实 smoke：用真实 Codex CLI 指向 Unio + 真实 DeepSeek，gate 在
   `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`，跑通 “Codex 改 base_url 即可用 DeepSeek”。
4. 计费回归：Responses 请求与等价 chat 请求的 `usage_records` / `ledger_entries` /
   `price_snapshots` / `cost_snapshots` 一致。

验收：Codex drop-in 真实可用；账务事实与 chat 路径等价。

已落地用例（`internal/blackbox/openaisdk/responses_*_test.go`，复用 `sdkfixture` + chat mock 上游）：
非流式成功 + thinking:disabled 闸门（DEC-016）、`operation=responses` 账务事实（usage/ledger/snapshots）、
background 400、有状态 endpoint 501、compact 降级摘要、input_tokens 本地估算（不触达上游）、流式事件
序列 + `sequence_number` 从 0 单调 + usage 落在 `response.completed`、`reasoning_text.delta`、真实 Codex
v0.130 `/responses` fixture 端到端回放（改 model 后整体回放，验证多 input item / 工具 / reasoning:null
翻译不报错）；gated 真实 DeepSeek 非流式/流式 smoke（`DEEPSEEK_BLACKBOX=1`）。
**待执行**：真实 DB/Redis 环境跑 mock 黑盒全绿、真实 key 跑 smoke、真实 Codex CLI 指向 Unio 手测。

<a id="task-11-16-doc-review"></a>
### TASK-11.16 文档、命名与结构复核

状态：done（结构/依赖/命名/文档/GAP 复核通过，0 代码命名/结构问题；与黑盒一致随 TASK-11.15 回归）

实现内容：

1. PROJECT_STRUCTURE / README / PROJECT_STATUS 同步 responses 子包与阶段状态。
2. RESPONSES_CHAT_BRIDGE 与实际代码、黑盒一致，无残留 `Verify`。
3. 复核未引入 `common`/`util`/`helper`；lifecycle 不 import responses HTTP DTO；
   responses 子包不 import sqlc row。
4. 复核全局 P0/P1 GAP，关闭本阶段必须关闭的 GAP。

复核结论：

1. PROJECT_STRUCTURE 已补 `gatewayapi/openai/responses`、`service/gateway/openai/responses`
   子包、lifecycle `attempt_runner*`、端点清单 `/v1/responses*`，并顺带修正 lifecycle 树过时
   文件名（Phase 10 遗留 executor/attempt/fallback/recovery/delivery）；PROJECT_STATUS 阶段状态
   已同步；项目无根 README，跳过该项。
2. RESPONSES_CHAT_BRIDGE 无残留 `Verify`；§6 流式 recipe 已与实际 emit 子集对齐，规范完整事件
   未发登记 GAP-11-011；其余字段映射与代码同源（codex-rs + 真实 fixture 冻结）。
3. 依赖方向复核通过：无 `common`/`util`/`helper` 包；lifecycle 不 import 任何 ingress DTO
   （AttemptRunner 为泛型）；responses 子包不 import sqlc row；service 用 app DTO 的方式与
   chatcompletions 一致。
4. GAP 复核：关闭 GAP-11-005（共享 invoker 已用 `lifecycle.AttemptRunner` 落地）；新增
   GAP-11-011（标准 SDK 完整性流式事件未发，P2）；GAP-11-001/002/007/009 为已接受的无状态/降级
   商业边界，按各自触发时机保留，非本阶段必须关闭。

## 整改增量：上下文压缩与请求体上限（TASK-11.18~11.25）

来源 [REMEDIATION-context-compaction-and-payload-limit.md](../../production/REMEDIATION-context-compaction-and-payload-limit.md)（已审核：Q1=128MB，Q2~Q7 按建议默认）。
决策见 [DEC-019](../../production/DECISIONS.md#dec-019-可配置请求体上限--compact-双路径native-透传--synthetic-降级)；GAP [GAP-11-013](../../production/TODO_REGISTER.md#gap-11-013) / [GAP-11-014](../../production/TODO_REGISTER.md#gap-11-014)。

<a id="task-11-18-json-body-limit"></a>
### TASK-11.18 可配置请求体上限（消除入口 413，GAP-11-013）

状态：done

落地：`platform/config` 增 `HTTPConfig.MaxJSONBodyBytes`（env `HTTP_MAX_JSON_BODY_MB`，默认 **128**，MB→字节）；
`platform/httpx/json.go` 的 `DecodeJSON` 改读进程级可配置上限 `MaxJSONBodyBytes()`，新增 `SetMaxJSONBodyBytes`，
保留 `DefaultMaxJSONBodyBytes`(1MB) 作未配置 fallback；`bootstrap` gateway/admin server 启动期各调一次
`httpx.SetMaxJSONBodyBytes(cfg.HTTP.MaxJSONBodyBytes)`，对全部经 `DecodeJSON` 的 ingress（chat/responses/compact/
input_tokens/admin）生效。解压后大小仍受 `MaxBytesReader` 约束，超限稳定 413（OpenAI-compatible）。
测试：`config_test.go`（默认/显式 env/非法）+ `json_test.go`（默认回退/抬高/收紧/负值回退）。

<a id="task-11-19-proxy-body-doc"></a>
### TASK-11.19 前置代理 body 限制与 env 示例文档

状态：done

落地：`.env.example` 增 `HTTP_MAX_JSON_BODY_MB=128` 并注明「默认 128（对齐 new-api 方向）；前置代理
`client_max_body_size` 须 ≥ 此值，否则请求仍在代理层 413」。能力契约同步见 CAPABILITY_MATRIX/CAPABILITY_KEYS。

<a id="task-11-20-compact-dual-path"></a>
### TASK-11.20 CompactOrchestrator + compact 能力 key（GAP-11-014）

状态：done

落地：`capability/keys.go` 注册 `responses.compact.native` / `responses.compact.synthetic`（CAPABILITY_KEYS v1.1）；
`service/gateway/openai/responses/compact_orchestrator.go` 的 `CompactHistory`→`executeCompact` 按候选 adapter
代码能力 `HasResponsesCompact` 选路 Native vs Synthetic（与 DEC-018 直传分流一致；能力 key 仅作契约/矩阵声明）。
两路径共用 `runNonStream` 资金关键 scaffold（自 `create_response.go` 抽出，CreateResponse 与 CompactHistory 同源）。
ingress `endpoints_handler.go` 不感知路径差异。

<a id="task-11-21-native-compact"></a>
### TASK-11.21 NativeCompact：原生 /responses/compact 透传 + lifecycle

状态：done

落地：`core/adapter/openai/responses` 增 `ResponsesCompactAdapter` 契约 + `Adapter.CompactResponse`（`compact.go`，
POST `<base>/responses/compact`，原文透传 + 同次解析 facts；404/405、无 usage、无法解析收敛为 sentinel
`ErrCompactUnsupported`）；`openai.Registry` 增 `responsesCompact` 槽 + `ResponsesCompact`/`HasResponsesCompact`，
bootstrap 为 `adapter_key=openai` 注册。ingress `CompactHistoryResponse` 增 `RawCompactHistoryResponse` + 自定义
`MarshalJSON` 原文透传（仅改写顶层 model 回显）。settlement 落上游 compact usage facts。
测试：adapter `adapter_test.go`（透传/404→unsupported/缺 usage→unsupported）+ service `compact_native_test.go`（透传）。

<a id="task-11-22-synthetic-compact"></a>
### TASK-11.22 SyntheticCompact：迁出现有 compact + 策略化

状态：done

落地：原 `compact_response.go` 迁为 `compact_synthetic.go`（`invokeSyntheticCompact` + `mapChatResponseToCompaction`
+ `defaultCompactionInstruction`），行为对 chat-only 第三方（DeepSeek）逐字保留（回归 `compact_input_tokens_test.go`
绿）。Synthetic 仍不签发加密 compaction item（永久限制 GAP-11-007）。`compact_response.go` 删除。

<a id="task-11-23-native-fallback"></a>
### TASK-11.23 Native 失败回落 Synthetic（可配置）+ audit 日志（整改 Q2）

状态：done

落地：`ResponsesService.compactNativeFallback`（默认 true）；NativeCompact 命中 `isNativeCompactUnsupported`
（`ErrCompactUnsupported` 或上游 404/405，`compact_native.go`）时回落 SyntheticCompact 并打 `slog.Warn`
（adapter_key/channel_id/upstream_model），避免 Codex 断链；其余上游错误按正常上游错误处理。
测试：`compact_native_test.go`（回落成功 + 关闭回落时上抛失败、synthetic 不触达）。

<a id="task-11-24-remediation-blackbox"></a>
### TASK-11.24 整改回归与黑盒

状态：done（单测/适配器级黑盒）

落地：`httpx`/`config` 上限单测、adapter `CompactResponse` httptest 黑盒、service compact 双路径 + 回落单测均绿；
`go build ./... && go vet ./...` 通过。真实上游端到端 smoke（OpenAI 原生 compact 透传 / 大 body 不 413）随
TASK-11.15 既有黑盒口径在接入真实渠道时复跑。

<a id="task-11-25-gzip-ingress"></a>
### TASK-11.25 （可选）gateway gzip 解压 + 解压后 MaxBytesReader

状态：deferred（本阶段不做，单独 follow-up）

说明：本整改只做 JSON 明文 + 可配置上限；若未来 gateway 接收 `Content-Encoding: gzip`，在中间件解压后再
`MaxBytesReader`（对齐 new-api 防 zip bomb）。非 Codex 现场必需，按需排期。

## 推荐实施顺序

```text
11.01 协议冻结
→ 11.02 模型指定与路由策略
→ 11.03 DEC-014 + operation 枚举
→ 11.04 Responses ingress DTO/decode/validation
→ 11.05 请求翻译
→ 11.06 非流式编排 + 响应翻译
→ 11.07 流式事件状态机
→ 11.08 工具/reasoning/text
→ 11.09 DeepSeek 上游适配复核与 Codex 工作负载验证（含 reasoning_effort 验证 + Phase 10 drift 修正）
→ 11.10 错误与安全
→ 11.11 compact 无状态降级实现
→ 11.12 input_tokens 本地估算实现
→ 11.13 有状态 endpoint 501 + 异步任务 Reject
→ 11.14 bootstrap 装配 + 路由
→ 11.15 黑盒验收
→ 11.16 文档与结构复核

(整改增量：上下文压缩与请求体上限)
→ 11.18 可配置请求体上限 → 11.19 代理 body 文档
→ 11.20 CompactOrchestrator + compact 能力 key → 11.21 NativeCompact 透传 → 11.22 SyntheticCompact 迁出
→ 11.23 Native 回落 Synthetic（可配置）→ 11.24 整改回归/黑盒 →（11.25 gzip deferred）
```

## 依赖与排序

```text
依赖：Phase 10（done）——双协议 lifecycle、OpenAI adapter（含首版具体上游 adapter/openai/deepseek）、
registry、ResponseFacts、settlement。
本阶段排在能力架构（阶段 12 Capability Architecture，DEC-015）与后台管理（阶段 13）之前：用现有
bootstrap seed / 运营配置即可把某个模型路由到 DeepSeek OpenAI channel，不依赖运行时 capability
闸门或 admin CRUD。
本阶段公开 API 表面与 `CAPABILITY_MATRIX.md` 静态文档冻结之后，阶段 12 会把它迁入运行时
`model_capabilities` 表并加 ingress capability 闸门；模型指定的“后台可视化管理”归阶段 13 admin。
阶段 11 用 seed/既有 routing 即可生产可用，不会被阶段 12 的灰度迁移影响。
```

## 关联 GAP

| GAP | 优先级 | 摘要 |
| --- | --- | --- |
| [GAP-11-001](../../production/TODO_REGISTER.md#gap-11-001) | P1 | 无状态生产范围：`previous_response_id` / server-side `store` 真实持久化未实现。 |
| [GAP-11-002](../../production/TODO_REGISTER.md#gap-11-002) | P1 | Codex `custom`/grammar 工具（apply_patch）与 `local_shell` 在 Chat Completions 无等价，按 convert→function/Drop 处理，需登记能力矩阵。 |
| [GAP-11-003](../../production/TODO_REGISTER.md#gap-11-003) | P2 | 跨轮 reasoning item / `reasoning.summary` / encrypted reasoning 透传保真度为 best-effort。 |
| [GAP-11-004](../../production/TODO_REGISTER.md#gap-11-004) | P2 | Responses 内置工具（web_search/file_search/code_interpreter/mcp 等）与 `include`/annotations 输出项未实现。 |
| [GAP-11-005](../../production/TODO_REGISTER.md#gap-11-005) | P2 | OpenAI 协议族 operation 编排骨架（chat/responses）若镜像复制，需后续 genericize 为共享 invoker。 |
| [GAP-11-006](../../production/TODO_REGISTER.md#gap-11-006) | P2 | 模型别名（Codex 默认模型名 → Unio 上游模型）映射表未实现，第一版需用户填 Unio model_id。 |
| [GAP-11-007](../../production/TODO_REGISTER.md#gap-11-007) | P1 | `/v1/responses/compact` 的 **SyntheticCompact**（chat-only 第三方）用无状态摘要降级；不签发加密 compaction item，多轮压缩累积信息损失。NativeCompact 透传见 GAP-11-014。 |
| [GAP-11-013](../../production/TODO_REGISTER.md#gap-11-013) | P1 | **已关闭**：请求体上限可配置（`HTTP_MAX_JSON_BODY_MB`，默认 128MB），消除 Codex 长会话入口 413（TASK-11.18 / DEC-019）。 |
| [GAP-11-014](../../production/TODO_REGISTER.md#gap-11-014) | P1 | **已关闭**：compact 双路径（NativeCompact 原文透传 vs SyntheticCompact 摘要降级），Native 不支持回落 Synthetic（TASK-11.20~11.24 / DEC-019）。 |
| [GAP-11-008](../../production/TODO_REGISTER.md#gap-11-008) | P2 | `/v1/responses/input_tokens` 用本地 tokenizer 估算；与 OpenAI 服务端精确计数有偏差，不反映 prompt cache 折扣。 |
| [GAP-11-009](../../production/TODO_REGISTER.md#gap-11-009) | P1 | Responses 有状态 endpoint（retrieve/delete/input_items/cancel）返回 501 stateless；`background:true` Reject——商业承诺无状态。 |
| [GAP-11-010](../../production/TODO_REGISTER.md#gap-11-010) | P2 | Phase 10 `reasoning_effort` doc/code drift（mapping doc 标 Adapt，代码 Pass-through），在 TASK-11.09 同步修正。 |

## 阶段关闭前必须检查

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n '^\| [^|]+ \| [^|]+ \| `Verify` \|' docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md
sqlc generate
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
git diff --check
```

必须阅读：

```text
docs/chapters/phase-10-dual-protocol-gateway/ARCHITECTURE.md
docs/chapters/phase-10-dual-protocol-gateway/RESPONSE_FACTS.md
docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md
docs/chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md
docs/protocol/openai/responses/official.md             # 本阶段权威字段来源
docs/production/DECISIONS.md
docs/production/TODO_REGISTER.md
```
