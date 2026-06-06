# Phase 11 Status

状态：in_progress

## 当前判断

阶段 11 为新增 `POST /v1/responses`（OpenAI Responses API ingress，Codex 兼容），是商业级、
生产级实现，排在能力架构（阶段 12 Capability Architecture，DEC-015）与后台管理（阶段 13）之前。
核心策略是 responses-to-chat 桥接：在 gateway 内部把 Responses 请求下转到既有 `openai.ChatRequest`
契约，复用 Phase 10 的 OpenAI adapter、routing、lifecycle、settlement 与 `ResponseFacts`，不新增
上游 Responses adapter，不改账务 schema。

`POST /v1/responses`（非流式 + 流式 SSE）主路径已落地：TASK-11.04~11.07 done，并采纳方案 B 抽出共享
`lifecycle.AttemptRunner`，让 OpenAI chat 与 responses 复用同一份资金关键候选 fallback 循环
（Anthropic Messages 保留自身循环）。`go build ./...` 与 `go test ./internal/... ./cmd/...` 全过、0 失败。
剩余：其余 endpoint（compact/input_tokens/有状态 501，TASK-11.11~11.13，路由未挂）、DeepSeek
reasoning_effort 复核（TASK-11.09）、错误/工具补齐（TASK-11.08/11.10）、黑盒验收与 fixture 脱敏（TASK-11.15）。

阶段 11 的 `CAPABILITY_MATRIX.md` 是静态公开能力声明；阶段 12 完成后会迁入运行时
`model_capabilities` 表并加 ingress capability 闸门，本阶段公开 API 表面不会被改动。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-11.01 | done | 协议快照已补齐：主文件 `POST /responses` + 子文件 [streaming events](../../protocol/openai_responses_streaming_events.md) + [其余 endpoint/error schema](../../protocol/openai_responses_other_endpoints.md)。**首份真实 Codex v0.130.0 `/responses` 已抓**（`internal/blackbox/fixtures/codex/20260605_151709.845_POST_v1_responses.json`，76 KB）+ **codex-rs 源码交叉确认**，BRIDGE §1~§9 全部残留清零：① reasoning 事件名冻结为 `reasoning_text.*`（content_index/reasoning_text，开源模型语义）；② `/compact` 请求 `CompactionInput`、响应 `{output:[ResponseItem]}`；③ `/input_tokens` 响应 `{input_tokens,object:"response.input_tokens"}`；④ 字段子集 + 新发现（`client_metadata` Drop、tools `type:"namespace"` 拍平+namespace 回译、工具全 `type:function`、Codex 仅消费事件子集、`output_item.done` 为权威载体）。⚠️ raw fixture 含个人信息，提交前需脱敏（TASK-11.15）。 |
| TASK-11.02 | done | 方案 A（客户 model = Unio model_id，复用 Phase 6 routing）已冻结在 [DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接)；5 个决策点结论见 PLAN「模型指定策略」；routing 用 `IngressProtocol=openai` 无 responses 特例；seed 一个 Codex 可用模型并入 TASK-11.09 item 5。 |
| TASK-11.03 | done | [DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接) accepted；`requestlog.OperationResponses="responses"` 已加；migration `000009` 放开 `operation` CHECK + 联合约束加 `(openai, responses)` 分支，scratch 库全量 up 验证通过、`sqlc generate` 无 drift、`go build` 通过。**待办**：dev 本地库需 drop→up 重建后才接受 `responses`（DATABASE_URL 门控测试前）。 |
| TASK-11.04 | done | `internal/app/gatewayapi/openai/responses/` 落地：`dto.go`（请求/响应/usage/output item/input union/reasoning/text）+ `tools.go`（function/namespace union）+ `stream_dto.go`（事件信封 + 事件名常量）+ `decode.go`（双轨 typed+Extensions，input string/array union）+ `content.go`（content part 结构校验）+ `validation.go`（协议结构校验，含 item/tool union）+ `response.go`（Responses 原生 error 渲染）。测试：decode（含 client_metadata Extensions、namespace 工具、reasoning:null）+ validation（18 例错误 param + 未知类型放行）+ error 渲染全过；`go vet` / `go build ./internal/... ./cmd/...` / lint 干净。 |
| TASK-11.05 | done | `internal/service/gateway/openai/responses/` 落地请求翻译：`responses_chat_map.go`（顶层字段 Adapt 进 `openai.ChatRequest`；instructions→system；input string/items→messages，连续 function_call 合并进单条 assistant、function_call_output 按 call_id 对齐 tool message、MCP function_call 用 namespace 回填名；tools function 扁平→嵌套、namespace 拍平为 `<ns><name>`、内置/custom/local_shell→Drop；tool_choice 归一；reasoning.effort→reasoning_effort；text.format→response_format（json_schema 提 schema）+verbosity；契约无承载字段 Drop 记 `dropped_request_fields`）+ `responses_content_map.go`（content union：纯文本合并为 string、多模态 best-effort content parts、tool output 归一）。测试：13 组单测 + 真实 v0.130 fixture 端到端翻译全过；vet/build/gofmt/lint 干净。 |
| TASK-11.06 | done | 方案 B 落地：抽共享 `lifecycle.AttemptRunner`（`attempt_runner.go` 非流式 + `attempt_runner_stream.go` 流式），chat 与 responses 复用同一份资金关键候选循环（Anthropic Messages 不接）。`responses/service.go` 装配 `ResponsesService`；`create_response.go` 经 routing→authorization→`RunNonStream`(复用 `openai.ChatAdapter`，Invoke 内做 responses→chat 请求翻译)→settlement；`responses_response_map.go` 把 `ChatResponse` 翻译回 Responses `response`。build/test 全过。 |
| TASK-11.07 | done | `stream_response.go` 走 `AttemptRunner.RunStream`（与 chat 流式同一份链路：emitted 后禁 fallback、final usage 缺失处理、tail-error 仍结算）；`responses_stream.go` `streamEncoder` 把 `ChatStreamChunk` 翻成 codex-rs 实测消费子集（created→output_item.added→output_text/reasoning_text/function_call_arguments.delta→output_item.done 权威载体→completed），单调 sequence_number、并行 tool_call 分项、namespace 回填。HTTP handler SSE 帧 + 真 SSE 测试。SDK 完整性事件补齐留 TASK-11.08。 |
| TASK-11.08 | planned | 工具（custom/grammar/local_shell）、reasoning、text 特殊处理。 |
| TASK-11.09 | planned | DeepSeek 上游适配复核：`reasoning_effort` 首要验证 + Phase 10 doc/code drift 同步修正。 |
| TASK-11.10 | planned | Responses 原生错误与安全输出。 |
| TASK-11.11 | planned | `POST /v1/responses/compact` 无状态降级实现（DeepSeek 摘要 + 合成 compaction item）。 |
| TASK-11.12 | planned | `POST /v1/responses/input_tokens` 本地估算实现（复用 `ChatInputTokenizer`）。 |
| TASK-11.13 | planned | 有状态 endpoint（retrieve/delete/input_items/cancel）501 + 异步任务 Reject。 |
| TASK-11.14 | partial | `bootstrap/gateway.go` 已装配 `ResponsesService`、`router.go` 已挂 `POST /v1/responses`（非流式 + 流式 SSE）。剩余 `/responses/compact`、`/responses/input_tokens`、有状态 endpoint 路由随 TASK-11.11~11.13 补挂。 |
| TASK-11.15 | planned | 黑盒验收（mock + 真实 Codex/DeepSeek smoke）。 |
| TASK-11.16 | planned | 文档、命名与结构复核。 |

## 进入阶段 11 前置条件

1. 阶段 10 双协议 gateway 链路稳定（done）。
2. 已能用现有 bootstrap seed / 运营配置把某个客户模型名路由到 DeepSeek OpenAI channel。
3. 协议快照 [docs/protocol/openai_responses.md](../../protocol/openai_responses.md) 已覆盖
   `POST /responses` 主路径；剩余 endpoint 与 streaming events 目录在 TASK-11.01 补齐。
4. 已抓到一份真实 Codex `/responses` 请求体作为字段冻结依据（TASK-11.01）。
5. 模型指定方式已冻结（TASK-11.02），运营可声明哪些模型可用于 Codex。

## 关联 GAP

GAP-11-001 ~ GAP-11-006 见 [TODO_REGISTER.md](../../production/TODO_REGISTER.md)。
