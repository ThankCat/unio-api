# Phase 11 Status

状态：done

## 当前判断

阶段 11 为新增 `POST /v1/responses`（OpenAI Responses API ingress，Codex 兼容），是商业级、
生产级实现，排在能力架构（阶段 12 Capability Architecture，DEC-015）与后台管理（阶段 13）之前。
核心策略是 responses-to-chat 桥接：在 gateway 内部把 Responses 请求下转到既有 `openai.ChatRequest`
契约，复用 Phase 10 的 OpenAI adapter、routing、lifecycle、settlement 与 `ResponseFacts`，不新增
上游 Responses adapter，不改账务 schema。

Responses 整条对外链路（不含第三方上游）已补齐：`POST /v1/responses`（非流式 + 流式 SSE）、
`POST /v1/responses/compact`（无状态降级摘要）、`POST /v1/responses/input_tokens`（本地估算）、
有状态 endpoint（retrieve/delete/cancel/input_items）统一 501、`background:true` 400 拒绝，全部
路由已挂（TASK-11.04~11.14 done）。采纳方案 B 抽出共享 `lifecycle.AttemptRunner`，OpenAI chat 与
responses 复用同一份资金关键候选 fallback 循环（Anthropic Messages 保留自身循环）；compact 复用
非流式 runner（可计费），input_tokens 仅用 routing 选 tokenizer 本地估算（不调上游、不计费）。
`go build ./...` 与 `go test ./...` 全过、0 失败。
剩余：TASK-11.09 核心已闭环（reasoning_effort 归一 + thinking 闸门，DEC-016 / GAP-11-010），TASK-11.09 剩余项（真实 key 端到端
smoke + Codex 可用 DeepSeek 模型 seed）已于 2026-06-06 完成（本地 dev 库幂等 seed 一套 Codex 可用 DeepSeek 运营配置 + 真实 Codex CLI v0.130 端到端手测，见下「端到端验收记录」）；TASK-11.16 文档/命名/结构/GAP 复核已完成
（PROJECT_STRUCTURE/PROJECT_STATUS 同步、BRIDGE §6 recipe 对齐实际 emit、依赖方向 0 问题、关闭
GAP-11-005、新增 GAP-11-011）；黑盒验收（TASK-11.15）mock + gated 真实 smoke 共 11 个用例已写齐并
`-tags=blackbox` 编译通过（无环境正确 Skip），真实 DB/Redis mock 黑盒 + gated 真实 DeepSeek smoke 已执行通过、真实 Codex CLI v0.130 端到端手测 2026-06-06 通过（见下「端到端验收记录」）。桥接 fidelity
残留（namespace 回译 GAP-11-002、跨轮 reasoning 回灌 GAP-11-003、json_schema strict）按 GAP 跟踪。

阶段 11 的 `CAPABILITY_MATRIX.md` 是静态公开能力声明；阶段 12 完成后会迁入运行时
`model_capabilities` 表并加 ingress capability 闸门，本阶段公开 API 表面不会被改动。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-11.01 | done | 协议快照已补齐：主文件 `POST /responses` + 子文件 [streaming events](../../protocol/openai/responses/official-streaming-events.md) + [其余 endpoint/error schema](../../protocol/openai/responses/official-other-endpoints.md)。**首份真实 Codex v0.130.0 `/responses` 已抓**（`internal/blackbox/fixtures/codex/20260605_151709.845_POST_v1_responses.json`，76 KB）+ **codex-rs 源码交叉确认**，BRIDGE §1~§9 全部残留清零：① reasoning 事件名冻结为 `reasoning_text.*`（content_index/reasoning_text，开源模型语义）；② `/compact` 请求 `CompactionInput`、响应 `{output:[ResponseItem]}`；③ `/input_tokens` 响应 `{input_tokens,object:"response.input_tokens"}`；④ 字段子集 + 新发现（`client_metadata` Drop、tools `type:"namespace"` 拍平+namespace 回译、工具全 `type:function`、Codex 仅消费事件子集、`output_item.done` 为权威载体）。⚠️ raw fixture 含个人信息，提交前需脱敏（TASK-11.15）。 |
| TASK-11.02 | done | 方案 A（客户 model = Unio model_id，复用 Phase 6 routing）已冻结在 [DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接)；5 个决策点结论见 PLAN「模型指定策略」；routing 用 `IngressProtocol=openai` 无 responses 特例；seed 一个 Codex 可用模型并入 TASK-11.09 item 5。 |
| TASK-11.03 | done | [DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接) accepted；`requestlog.OperationResponses="responses"` 已加；migration `000009` 放开 `operation` CHECK + 联合约束加 `(openai, responses)` 分支，scratch 库全量 up 验证通过、`sqlc generate` 无 drift、`go build` 通过。**待办**：dev 本地库需 drop→up 重建后才接受 `responses`（DATABASE_URL 门控测试前）。 |
| TASK-11.04 | done | `internal/app/gatewayapi/openai/responses/` 落地：`dto.go`（请求/响应/usage/output item/input union/reasoning/text）+ `tools.go`（function/namespace union）+ `stream_dto.go`（事件信封 + 事件名常量）+ `decode.go`（双轨 typed+Extensions，input string/array union）+ `content.go`（content part 结构校验）+ `validation.go`（协议结构校验，含 item/tool union）+ `response.go`（Responses 原生 error 渲染）。测试：decode（含 client_metadata Extensions、namespace 工具、reasoning:null）+ validation（18 例错误 param + 未知类型放行）+ error 渲染全过；`go vet` / `go build ./internal/... ./cmd/...` / lint 干净。 |
| TASK-11.05 | done | `internal/service/gateway/openai/responses/` 落地请求翻译：`responses_chat_map.go`（顶层字段 Adapt 进 `openai.ChatRequest`；instructions→system；input string/items→messages，连续 function_call 合并进单条 assistant、function_call_output 按 call_id 对齐 tool message、MCP function_call 用 namespace 回填名；tools function 扁平→嵌套、namespace 拍平为 `<ns><name>`、内置/custom/local_shell→Drop；tool_choice 归一；reasoning.effort→reasoning_effort；text.format→response_format（json_schema 提 schema）+verbosity；契约无承载字段 Drop 记 `dropped_request_fields`）+ `responses_content_map.go`（content union：纯文本合并为 string、多模态 best-effort content parts、tool output 归一）。测试：13 组单测 + 真实 v0.130 fixture 端到端翻译全过；vet/build/gofmt/lint 干净。 |
| TASK-11.06 | done | 方案 B 落地：抽共享 `lifecycle.AttemptRunner`（`attempt_runner.go` 非流式 + `attempt_runner_stream.go` 流式），chat 与 responses 复用同一份资金关键候选循环（Anthropic Messages 不接）。`responses/service.go` 装配 `ResponsesService`；`create_response.go` 经 routing→authorization→`RunNonStream`(复用 `openai.ChatAdapter`，Invoke 内做 responses→chat 请求翻译)→settlement；`responses_response_map.go` 把 `ChatResponse` 翻译回 Responses `response`。build/test 全过。 |
| TASK-11.07 | done | `stream_response.go` 走 `AttemptRunner.RunStream`（与 chat 流式同一份链路：emitted 后禁 fallback、final usage 缺失处理、tail-error 仍结算）；`responses_stream.go` `streamEncoder` 把 `ChatStreamChunk` 翻成 codex-rs 实测消费子集（created→output_item.added→output_text/reasoning_text/function_call_arguments.delta→output_item.done 权威载体→completed），单调 sequence_number、并行 tool_call 分项、namespace 回填。HTTP handler SSE 帧 + 真 SSE 测试。SDK 完整性事件补齐留 TASK-11.08。 |
| TASK-11.08 | done | 工具/reasoning/text 结构翻译已在 `responses_chat_map.go` 落地：function 扁平→嵌套、namespace 拍平 `<ns><name>`、tool_choice 归一、reasoning.effort→reasoning_effort、text.format→response_format（json_schema 提 schema）+verbosity、内置/custom/local_shell Drop 记审计。残留 fidelity（namespace 回译 GAP-11-002、跨轮 reasoning 回灌 GAP-11-003、json_schema strict 细节）按 GAP 跟踪，不阻断链路。 |
| TASK-11.09 | done | **`reasoning_effort` drift + thinking opt-in 闸门已闭环**（GAP-11-010 关闭，DEC-016）：以官方 API 文档为依据，`deepseek/adapt.go`+`drop.go` 出站归一 reasoning_effort 为 high/max（minimal/low/medium/high→high，xhigh/max→max，未知 Drop）；Responses reasoning 缺省/null → 内部意图标志 `ChatRequest.ReasoningDisabled`（永不序列化）→ DeepSeek 出站注入 `thinking:disabled`，桥接层不直塞私有字段防泄漏到真 OpenAI；桥接/adapter 职责边界复核通过；Phase 9/10/11 三处 doc 已对齐；bridge+adapter 均有单测，全量 go test 通过。**已完成**：本地 dev 库幂等 seed 一套 Codex 可用 DeepSeek 运营配置（`gpt-5.5`→`channel_models.upstream_model=deepseek-v4-pro`，含加密凭据/price/cost_price/user/project/api_key/balance）；真实 Codex CLI v0.130 端到端手测 2026-06-06 通过（见「端到端验收记录」）。 |
| TASK-11.10 | done | Responses 原生错误与安全输出已就位：decode/validation 400、service 错误经 `mapResponsesServiceError`（insufficient_quota / unsupported_parameter / model_not_found / model_unavailable / 上游分类经 `adapter.UpstreamCategoryOf`，绝不渲染成客户 401、不透传上游 body）、流尾 `event:error`、`background` 400 `unsupported_background`、有状态 501 `unsupported_endpoint_stateless`。 |
| TASK-11.11 | done | `POST /v1/responses/compact` 无状态降级：`compact_response.go` 把待压缩历史(input)+instructions(缺省注入默认压缩指令)当作一次可计费非流式 chat（复用 `executeNonStreamChat` 走 routing/authorization/AttemptRunner/settlement），摘要包成 `{"output":[message(output_text)]}`，第一版不签发 compaction 密文 item（GAP-11-007）。service+ingress 测试覆盖。 |
| TASK-11.12 | done | `POST /v1/responses/input_tokens` 本地估算：`input_tokens.go` 用 routing 解析模型→候选选 tokenizer，把请求翻成 ChatRequest 后用 `ChatInputTokenizer` 估算，返回 `{"input_tokens",object:"response.input_tokens"}`；不走候选 fallback、不调上游、不计费、不写审计（GAP-11-008）。service+ingress 测试覆盖。 |
| TASK-11.13 | done | 有状态 endpoint（retrieve/delete/cancel/input_items）统一 501 `unsupported_endpoint_stateless`（serviceless 静态 handler）；`POST /v1/responses` 带 `background:true` → 400 `unsupported_background`（validation 拒绝，不静默转同步）。ingress+router 测试覆盖。 |
| TASK-11.14 | done | `bootstrap/gateway.go` 装配 `ResponsesService`；`router.go` 挂全部 `/v1/responses*` 路由：`POST /responses`、`POST /responses/compact`、`POST /responses/input_tokens`、`GET/DELETE /responses/{response_id}`、`GET /responses/{response_id}/input_items`、`POST /responses/{response_id}/cancel`。router_test 验证 chi 静态/参数路由无冲突、501/400/路由可达。 |
| TASK-11.15 | done | mock 黑盒 + gated 真实 smoke 已落地 `internal/blackbox/openaisdk/responses_*_test.go`（非流式 + thinking:disabled 闸门、`operation=responses` 账务、background 400、有状态 501、compact、input_tokens、流式事件序列 + sequence_number 单调 + usage、reasoning_text.delta、真实 Codex v0.130 fixture 端到端、gated 真实 DeepSeek 非流式/流式）。`-tags=blackbox` 编译通过、无环境正确 Skip（11 用例）。**已执行**：真实 DB/Redis mock 黑盒 + `DEEPSEEK_BLACKBOX=1` 真实 DeepSeek smoke + 真实 Codex CLI v0.130 端到端手测全部通过（端到端结果见「端到端验收记录」）；raw fixture 已脱敏。 |
| TASK-11.16 | done | 结构/依赖/命名/文档/GAP 复核通过：PROJECT_STRUCTURE/PROJECT_STATUS 同步 responses 子包 + lifecycle `attempt_runner*` + 端点清单（项目无根 README，跳过）；BRIDGE §6 recipe 对齐实际 emit 子集、无残留 `Verify`；依赖方向 0 问题（无 common/util/helper、lifecycle 不依赖 ingress DTO、responses 不依赖 sqlc row）；关闭 GAP-11-005（共享 invoker→AttemptRunner），新增 GAP-11-011（标准 SDK 完整性流式事件未发）。与黑盒一致随 TASK-11.15 回归。 |

## 端到端验收记录

### 2026-06-07 阶段 11 acceptance sign-off（通过，状态 → done）

- 本次未修改生产代码，仅执行验收命令组并据结果收口状态标记。
- 验收命令组全绿：

```bash
sqlc generate                                   # 通过，无 drift
go build ./internal/... ./cmd/...               # 通过
go vet ./internal/... ./cmd/...                 # 通过
go test ./internal/... ./cmd/...                # 全绿（DB 门控测试无 DATABASE_URL 正确 Skip）
git diff --check                                # 通过
# RESPONSES_CHAT_BRIDGE.md `Verify` 残留检查：无输出
```

- TASK-11.01 ~ TASK-11.16 全部 done；2026-06-06 真实 Codex CLI v0.130 端到端手测 + 资金三方对账闭合（见下）。
- 本阶段必须关闭的 P0/P1 GAP 已收口：GAP-11-005、GAP-11-010 done。剩余 P1（GAP-11-001 无状态、
  GAP-11-002 工具保真度、GAP-11-007 compact 永久降级、GAP-11-009 有状态 501 永久边界）均为已接受范围
  边界或永久限制，release_blocker 全为 no，非本阶段必须关闭项。
- 结论：阶段 11 OpenAI Responses API ingress 实现完成、验证通过、文档同步，状态由 in_progress 改为 done。

### 2026-06-06 真实 Codex CLI 端到端手测（通过）

- 环境：Codex CLI v0.130，`~/.codex/config.toml` 配 `model="gpt-5.5"`、`model_provider=Unio`、
  `base_url=http://localhost:8520/v1`、`wire_api="responses"`、流式；本地 `gateway-server`，上游真实 DeepSeek。
- seed：本地 dev 库幂等写入 provider/channel(加密 DeepSeek 凭据)/model `gpt-5.5` →
  `channel_models.upstream_model=deepseek-v4-pro`/price/cost_price/user/project/api_key/balance=100 USD
  （seed 脚本为开发期一次性工具，不入库）。
- 用例：多轮真实对话（普通问答、拒答有害请求、本地工具 `curl` 取天气、agent `tool_use` 循环）。
- 结果：14 个 Responses 请求全部 `succeeded`、`stream=true`；路由门面一致
  `requested=gpt-5.5 → response=gpt-5.5`（上游实际 `deepseek-v4-pro`）；14 次 upstream attempt 全 `200`
  （finish_class `stop`×8 + `tool_use`×6，请求数多于可见轮数源于 Codex 的工具回灌循环）。
- 账务三方对账闭合：authorized `1.2416660000` = captured `0.0924627175` + released `1.1492032825`；
  `ledger_entries` debit 合计 = reservation captured = `0.0924627175`；余额
  `100 − 0.0924627175 = 99.9075372825`；`reserved_balance=0`，无挂起/泄漏 hold；
  `usage_records`/`price_snapshots`/`cost_snapshots`/`ledger_entries`/`ledger_reservations` 计数与请求数 1:1（均 14）。
- 结论：Responses→Chat 桥接 + DeepSeek 上游 + 预授权/结算/释放 资金关键链路端到端干净通过。
- 附带：DeepSeek adapter 丢弃字段日志由 WARN 降为 DEBUG（DEC-012 既定正常行为，避免正常流量刷屏）。

## 进入阶段 11 前置条件

1. 阶段 10 双协议 gateway 链路稳定（done）。
2. 已能用现有 bootstrap seed / 运营配置把某个客户模型名路由到 DeepSeek OpenAI channel。
3. 协议快照 [docs/protocol/openai/responses/official.md](../../protocol/openai/responses/official.md) 已覆盖
   `POST /responses` 主路径；剩余 endpoint 与 streaming events 目录在 TASK-11.01 补齐。
4. 已抓到一份真实 Codex `/responses` 请求体作为字段冻结依据（TASK-11.01）。
5. 模型指定方式已冻结（TASK-11.02），运营可声明哪些模型可用于 Codex。

## 关联 GAP

GAP-11-001 ~ GAP-11-011 见 [TODO_REGISTER.md](../../production/TODO_REGISTER.md)。

- 已关闭：GAP-11-005（共享 invoker → `lifecycle.AttemptRunner`）、GAP-11-010（reasoning_effort drift）。
- 已实现降级/边界（永久保留）：GAP-11-007（compact 无状态摘要）、GAP-11-008（input_tokens 本地估算）、GAP-11-009（有状态 501 + background 400）。
- 新增：GAP-11-011（Responses 流式只发 Codex 消费子集，标准 SDK 完整性事件未发，P2）。
