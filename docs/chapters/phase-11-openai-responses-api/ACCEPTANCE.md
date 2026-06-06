# Phase 11 Acceptance

## 产品验收

1. 用户只把 Codex 的 `base_url` 指向 `<unio>/v1`、`api_key` 设为 Unio key，**不改 Codex 代码**，
   即可用 DeepSeek 驱动 Codex CLI 正常对话、编辑代码（apply_patch 工具闭环按 GAP-11-002 策略）。
2. `POST /v1/responses` 与既有 `/v1/chat/completions`、`/v1/messages` 共享同一套认证、限流、
   routing、authorization、fallback、settlement、recovery、metrics 和 tracing。
3. 本阶段不承诺有状态会话、Responses 内置工具真实执行和多模态能力扩展；无法转换的合法字段
   按 DEC-012 在 adapter 出站 Drop，ingress 不因 provider 能力 400。
4. 公开能力声明（[CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md)）准确反映"做 / 降级 / 不做"全部
   接口与字段，与 PLAN 接口范围表一致。
5. 商业承诺的诚实性：结构性不可桥接能力（`encrypted_content` / 内置工具 / 服务端 store /
   `background` / server-side prompt）一律按公开策略处理，**不模拟伪能力**。

## 协议验收

1. [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md) 无残留 `Verify`，每个请求字段、input item、
   output item 和流式事件都有明确 Pass/Adapt/Drop/Reject 策略。
2. 请求翻译：`input`（string 与 items[]）、`instructions`、`tools`（扁平→嵌套）、`tool_choice`
   归一、`reasoning→reasoning_effort`、`text→response_format`、无状态字段 Drop 全部覆盖并记审计。
3. 非流式响应：`output` items（reasoning / message / function_call）、usage 字段名转换、
   status/incomplete_details 完整。
4. 流式：`response.created`/`in_progress`/`output_item.*`/`content_part.*`/`output_text.delta`/
   `reasoning_*`/`function_call_arguments.*`/`completed` 事件序列正确，`sequence_number` 单调，
   usage 在 `response.completed`。
5. 非法 Responses 协议结构返回 Responses 原生 400；不因 provider 能力 400。
6. `POST /v1/responses/compact` 无状态降级实现可用：合法 input → 返回带 `compaction` item 的
   合法 `CompactedResponse`；下一轮 `/v1/responses` 回传该 item 能还原为 system summary message。
   `encrypted_content` 由 Unio 自编码，不模拟 OpenAI 密文。
7. `POST /v1/responses/input_tokens` 本地估算实现可用：返回 `{object:"response.input_tokens",
   input_tokens:<N>}`；不调上游、不产生账务事件。
8. 有状态 endpoint（`GET/DELETE /v1/responses/{id}`、`/input_items`、`/cancel`）返回稳定的
   501 + Responses 原生 error（`code:"unsupported_endpoint_stateless"`）；`background:true` 返回
   400 + `code:"unsupported_background"`。

## 架构与复用验收

1. 未新增上游 Responses adapter；首版具体上游 `adapter/openai/deepseek` 完全复用，仅在确有
   Responses/Codex 专属缺口且经授权时按既有出站 Drop 模式补规则。
2. Drop 职责不重叠：桥接层把有承载字段的 Responses 字段（store/prompt_cache_*/verbosity/
   response_format/parallel_tool_calls 等）Adapt 进 `ChatRequest`，provider 能力 Drop 由 DeepSeek
   adapter 出站 `dropUnsupported` 负责；桥接层只 Drop 契约无承载字段的 Responses 专属字段。
3. `service/gateway/lifecycle` 未 import `gatewayapi/openai/responses` HTTP DTO。
4. `gatewayapi/openai/responses` 与 `service/gateway/openai/responses` 为独立 operation 子包，
   未在协议族根包平铺；未引入 `common`/`util`/`helper`。
5. Responses 子包不直接引用 `platform/store/sqlc` row。
6. 账务事实零改动：Responses 请求复用 `ResponseFacts`/`usage.Facts`，未新增账务 schema。
7. `requestlog.OperationResponses` 落地，Responses 请求在审计与 metrics 可与 chat/messages 区分。

## 账务与安全验收

1. Responses 请求与等价 chat 请求的 `usage_records`/`ledger_entries`/`price_snapshots`/
   `cost_snapshots` 事实一致。
2. 客户响应只在 immutable recovery facts 持久化、settlement 完成或 durable recovery job 接管后
   写成功终态（非流式返回 / 流式 `response.completed`）。
3. 错误分类与 HTTP status 与 chat/messages 一致；上游 auth/permission 绝不渲染成客户 401。
4. 上游原始 body、credential、prompt、完整响应正文不进日志/审计/错误 fields。
5. SSE 开始后写 Responses `error`/`response.failed` 事件，不回退普通 JSON。

## 测试验收

1. 黑盒（mock 上游）覆盖：非流式、流式、reasoning、tools 多轮（function_call→function_call_output）、
   custom 工具策略、错误映射、settlement DB 事实链路、流式事件序列断言。
2. 真实 smoke：真实 Codex CLI 指向 Unio + 真实 DeepSeek，gate 在
   `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`，跑通 drop-in。
3. 请求翻译、响应翻译、流式状态机有单元测试覆盖（合并 function_call、call_id 对齐、
   tool delta 累积、usage 字段名、status 映射）。
4. compact 端到端：mock 上游覆盖摘要请求 → compaction item 合成 → 下一轮回传 → 还原为 system
   message 全链路；账务两次请求都正确入账。
5. input_tokens：合法 input 返回估算值；非法结构返回 Responses 原生 400；零账务事件、零 upstream 调用断言。
6. 有状态 endpoint 501 与 background Reject：HTTP 状态码 + Responses 原生 error code 稳定；不调用上游。
7. `reasoning_effort` 在 DeepSeek 上的行为已黑盒冻结（A/B/C 三选一），代码与
   [DEEPSEEK_OPENAI_MAPPING.md](../phase-10-dual-protocol-gateway/DEEPSEEK_OPENAI_MAPPING.md) 一致；
   GAP-11-010 已关闭。

自动化验证：

```bash
sqlc generate
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
git diff --check
rg -n '^\| [^|]+ \| [^|]+ \| `Verify` \|' docs/chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md
```

`Verify` 残留检查必须无输出。

## 文档验收

1. [PLAN.md](PLAN.md)、[STATUS.md](STATUS.md) 与实现一致。
2. [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md) 与翻译代码、黑盒一致。
3. [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md) 与实际接口/字段行为一致，可作为对客户的能力声明。
4. [docs/architecture/PROJECT_STRUCTURE.md](../../architecture/PROJECT_STRUCTURE.md) 已加入 responses 子包。
5. [docs/production/DECISIONS.md](../../production/DECISIONS.md) DEC-014 已落地（含结构性不可桥接边界）。
6. [docs/PROJECT_STATUS.md](../../PROJECT_STATUS.md) 与 [docs/chapters/README.md](../README.md) 已同步阶段 11。
7. 本阶段必须关闭的 P0/P1 GAP 已收口。
