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

## 非目标（生产范围边界，非质量妥协）

1. 不新增上游 Responses adapter；DeepSeek 没有 Responses endpoint，本阶段不去对接任何
   provider 的 Responses 上游。
2. 不实现 **有状态会话**：`previous_response_id`、server-side `store=true` 持久化、
   `GET/DELETE /v1/responses/{id}`、`/v1/responses/{id}/input_items` 不在本阶段。生产上 Codex 对
   第三方 provider 默认 `store=false` 并每轮回传完整 `input`（与社区代理一致），因此无状态即可
   支撑完整 Codex 工作流。见 GAP-11-001。
3. 不实现 Responses 内置工具的真实执行：`web_search` / `file_search` / `code_interpreter` /
   `computer_use` / `image_generation` / `mcp` 等 server-side tool 不在本阶段真实执行。
4. 不扩展多模态模型能力。Responses `input_image` / `input_file` 等只做协议面识别；当前上游
   不支持时按 DEC-012 在 adapter 出站 Drop。
5. 不引入官方 Responses Go SDK 进生产链路（沿用 DEC-011，SDK 仅用于黑盒验收）。
6. 不改动既有 `/v1/chat/completions` 与 `/v1/messages` 的对外契约和计费事实。

“不在本阶段”不等于允许静默丢字段：本阶段对 Responses 请求、非流式响应、流式事件、错误响应、
usage 与工具/reasoning 的下转/上转必须完整设计、实现并验收；无法转换的合法协议字段按 DEC-012
在出站 Drop 并记内部审计。

## 开源参考

社区已有多个 “Codex ↔ DeepSeek/OpenAI 兼容上游” 的 Responses↔Chat 翻译代理，可直接对照
字段映射、流式事件序列、工具与 reasoning 处理。本阶段不抄实现，但用它们作为 wire 行为参考与
黑盒对照基线：

| 项目 | 语言 | 参考价值 |
| --- | --- | --- |
| [BerriAI/litellm](https://github.com/BerriAI/litellm) `responses/litellm_completion_transformation/` | Python | 最完整、生产级的 Responses↔Chat 双向转换：请求 `input→messages`、`tools` 扁平→嵌套、`tool_choice` 归一、`reasoning→reasoning_effort`、`text→response_format`、响应 `output` items（message/reasoning/function_call）、usage 字段名转换、流式 `transform_chat_completion_chunk_to_response_api_chunk` 事件状态机。 |
| [MetaFARS/codex-relay](https://github.com/MetaFARS/codex-relay) | Rust | 轻量 Codex 专用：完整 SSE 事件排序、tool_calls delta 累积成 `function_call` item、并行 tool_call 合并成单条 assistant、reasoning_content 跨轮保留。 |
| [yangfei4913438/codex-deepseek](https://github.com/yangfei4913438/codex-deepseek)（`ccswitch-deepseek` 端口） | Python | 直接面向 Codex+DeepSeek：`/responses` 入口、`input` items→`messages`、转发 `{base_url}/chat/completions`、SSE 回译。 |
| [soddygo/codex-convert-proxy](https://github.com/soddygo/codex-convert-proxy) | Rust | 多上游：`developer` role→`system`、保留 function tools 与 `tool_choice`、按 provider 映射 reasoning effort。 |
| [wujfeng712-ui/codex-bridge](https://github.com/wujfeng712-ui/codex-bridge) | TypeScript | Codex 专用双向桥：流式 SSE、tool calls、thinking 往返、按 provider 翻译 `none|minimal|low|medium|high` effort。 |

完整字段映射矩阵、Codex 实际 wire 行为与已知坑见
[RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)。

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
parallel_tool_calls               → 原样
tools[{type:function,name,...}]   → tools[{type:function,function:{name,...}}]（扁平→嵌套）
tools[{type:custom/grammar}]      → 见 GAP-11-002（convert→function 或 Drop）
tools[{type:web_search/...}]      → 内置工具，本阶段 Drop（无真实执行）
tool_choice(string / {type:...})  → 归一为 auto/none/required/具名 function
reasoning:{effort}                → reasoning_effort（上游不支持则 adapter 出站 Drop）
text:{format}                     → response_format
store/previous_response_id/include/truncation/prompt_cache_* → Responses 专属，Drop（无状态）
stream                            → 原样（Codex 恒为 true，并强制 include_usage）
```

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

状态：planned

目标：

```text
落地 docs/protocol/openai_responses.md 参考快照，并把 RESPONSES_CHAT_BRIDGE.md 的每个字段
冻结为 Pass / Adapt / Drop / Reject。
```

实现内容：

1. 整理 OpenAI Responses Create 官方字段 + Codex CLI 实际 wire（抓一份真实 Codex `/responses`
   请求体作为 fixture，识别 `instructions/input/tools/reasoning/text/store/include/...`）。
2. 对每个请求字段、每类 input item、每类 output item 和每个流式事件给出明确策略。
3. 标注 Codex 专属坑：`custom`/grammar 工具（apply_patch）、`local_shell` 内置工具、
   `reasoning.summary`、`text.verbosity`、`store=false` 无状态假设。
4. 冻结前不开始翻译层生产代码（与 Phase 10 mapping 冻结流程一致）。

验收：[RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md) 无残留 `Verify`。

<a id="task-11-02-model-spec"></a>
### TASK-11.02 模型指定与路由策略冻结

状态：planned

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

状态：planned

实现内容：

1. 在 DECISIONS.md 写入 DEC-014（responses-to-chat bridge、无状态生产范围、账务复用）。
2. `requestlog` 新增 `OperationResponses = "responses"`；确认 `operation` 列无需 migration
   （或补一条改列 migration）。
3. metrics 与 request log 能按 operation 区分 responses。

验收：Responses 请求在 `request_records.operation` 与 metrics 中可与 chat/messages 区分。

<a id="task-11-04-responses-ingress"></a>
### TASK-11.04 Responses ingress DTO / decode / validation

状态：planned

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

状态：planned

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

状态：planned

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

状态：planned

实现内容：

1. `responses_stream.go`：把内部 `openai.ChatStreamChunk` 流翻译成 Responses 命名事件序列：
   `response.created` → `response.in_progress` → `response.output_item.added` →
   `response.content_part.added` → `response.output_text.delta`（文本）/
   `response.reasoning_summary_text.delta`（reasoning_content）/
   `response.function_call_arguments.delta`（tool_calls delta 累积）→ 对应 `.done` →
   `response.output_item.done` → `response.completed`（含完整 response + usage）。
2. 每个事件带单调 `sequence_number`；item/content_part 索引稳定。
3. tool_calls 跨 chunk 增量累积成完整 `function_call` item；并行 tool_call 正确分项。
4. 复用共享 stream lifecycle：首个客户可见事件前允许 fallback、之后禁止；有可靠 usage 的 tail
   error 仍 settlement 并记 delivery interrupted；recovery facts 未持久化不写成功终态
   （`response.completed`），改写 Responses `error` / `response.failed` 事件。
5. `gatewayapi/openai/responses/stream.go`：命名事件 SSE 帧编码（`event:` + `data:`）。

验收：Codex 能消费完整事件流并正确渲染增量文本、reasoning 与工具调用；终态账务与 chat 流一致。

<a id="task-11-08-tools-reasoning"></a>
### TASK-11.08 工具、reasoning 与 text 特殊处理

状态：planned

实现内容：

1. `custom`/grammar 工具（Codex apply_patch freeform）按冻结策略 convert→function（单 string 参数）
   或 Drop；`local_shell` 内置工具处理。
2. `tool_choice` 各形态（string / `{type:auto|none|required|tool|function}`）归一。
3. `reasoning.effort` → `reasoning_effort`，上游不支持时 adapter 出站 Drop；`reasoning.summary`
   策略冻结。
4. `text.format`（json_schema / json_object）→ `response_format`；`text.verbosity` → `verbosity`。
5. 跨轮 reasoning item 透传 best-effort（GAP-11-003）。

验收：Codex 工具调用闭环（function_call → function_call_output 续接）端到端可用。

<a id="task-11-09-error-security"></a>
### TASK-11.09 错误与安全输出

状态：planned

实现内容：

1. 复用 `adapter.UpstreamCategoryOf` 稳定分类，渲染 Responses 原生 error（429/400/504/502 策略
   与 chat/messages 一致：上游 auth/permission 绝不渲染成客户 401）。
2. SSE 开始后写 Responses `error` / `response.failed` 事件，不回退普通 JSON。
3. 上游原始 body、credential、prompt、完整响应正文不进日志/审计/错误 fields。

验收：错误分类、HTTP status 与安全不泄漏行为与 Phase 10 双协议一致。

<a id="task-11-10-bootstrap-wiring"></a>
### TASK-11.10 bootstrap 装配与路由

状态：planned

实现内容：

1. `bootstrap/gateway.go` 新增 `NewResponsesGateway`（复用 `chatRouter` + `registry.OpenAI` +
   同一套 settlement/authorization/breaker/metrics 依赖）。
2. `gateway_server.go` 装配 `responsesService`；`http.go` `RouterDeps` 增加 `ResponsesService`。
3. `router.go` 在 `/v1` 下挂 `POST /v1/responses`，复用 API key auth + rate limit middleware。
4. 确认 Codex 把 `base_url` 设为 `<unio>/v1`、`api_key` 为 Unio key 后能 drop-in（含
   `GET /v1/models` 已存在）。

验收：进程启动 preflight 通过；`POST /v1/responses` 命中 OpenAI channel。

<a id="task-11-11-blackbox"></a>
### TASK-11.11 黑盒验收

状态：planned

实现内容：

1. mock 上游：在 `internal/blackbox/` 用 Responses 请求 fixture（真实 Codex 抓包）覆盖
   非流式 / 流式 / reasoning / tools 多轮 / custom 工具 / 错误映射 / settlement DB 事实链路。
2. 流式事件序列断言：事件类型、顺序、`sequence_number` 单调、usage 在 `response.completed`。
3. 真实 smoke：用真实 Codex CLI 指向 Unio + 真实 DeepSeek，gate 在
   `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`，跑通 “Codex 改 base_url 即可用 DeepSeek”。
4. 计费回归：Responses 请求与等价 chat 请求的 `usage_records` / `ledger_entries` /
   `price_snapshots` / `cost_snapshots` 一致。

验收：Codex drop-in 真实可用；账务事实与 chat 路径等价。

<a id="task-11-12-doc-review"></a>
### TASK-11.12 文档、命名与结构复核

状态：planned

实现内容：

1. PROJECT_STRUCTURE / README / PROJECT_STATUS 同步 responses 子包与阶段状态。
2. RESPONSES_CHAT_BRIDGE 与实际代码、黑盒一致，无残留 `Verify`。
3. 复核未引入 `common`/`util`/`helper`；lifecycle 不 import responses HTTP DTO；
   responses 子包不 import sqlc row。
4. 复核全局 P0/P1 GAP，关闭本阶段必须关闭的 GAP。

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
→ 11.09 错误与安全
→ 11.10 bootstrap 装配 + 路由
→ 11.11 黑盒验收
→ 11.12 文档与结构复核
```

## 依赖与排序

```text
依赖：Phase 10（done）——双协议 lifecycle、OpenAI adapter、registry、ResponseFacts、settlement。
本阶段排在后台管理（阶段 12）之前：用现有 bootstrap seed / 运营配置即可把某个模型路由到 DeepSeek
OpenAI channel，不依赖 admin CRUD。
模型指定的“后台可视化管理”归阶段 12（admin）；阶段 11 用 seed/既有 routing 即可生产可用。
```

## 关联 GAP

| GAP | 优先级 | 摘要 |
| --- | --- | --- |
| [GAP-11-001](../../production/TODO_REGISTER.md#gap-11-001) | P1 | 无状态生产范围：`previous_response_id` / server-side `store` / responses retrieve/delete/input_items 未实现。 |
| [GAP-11-002](../../production/TODO_REGISTER.md#gap-11-002) | P1 | Codex `custom`/grammar 工具（apply_patch）与 `local_shell` 在 Chat Completions 无等价，按 convert→function/Drop 处理，需登记能力矩阵。 |
| [GAP-11-003](../../production/TODO_REGISTER.md#gap-11-003) | P2 | 跨轮 reasoning item / `reasoning.summary` / encrypted reasoning 透传保真度为 best-effort。 |
| [GAP-11-004](../../production/TODO_REGISTER.md#gap-11-004) | P2 | Responses 内置工具（web_search/file_search/code_interpreter/mcp 等）与 `include`/annotations 输出项未实现。 |
| [GAP-11-005](../../production/TODO_REGISTER.md#gap-11-005) | P2 | OpenAI 协议族 operation 编排骨架（chat/responses）若镜像复制，需后续 genericize 为共享 invoker。 |
| [GAP-11-006](../../production/TODO_REGISTER.md#gap-11-006) | P2 | 模型别名（Codex 默认模型名 → Unio 上游模型）映射表未实现，第一版需用户填 Unio model_id。 |

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
docs/production/DECISIONS.md
docs/production/TODO_REGISTER.md
```
