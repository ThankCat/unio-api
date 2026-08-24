# Codex / Claude Code CLI 协议对齐计划

## 背景

2026-08-24 用本地记录型反向代理对两个 CLI 重新抓包（上游为第三方中转 `open.codex521.cc`），
确认当前客户端形态与网关既有实现存在偏差。抓包原件（凭据已脱敏）暂存
`tmp/cli-capture-2026-08/`，不入库。

抓到的客户端版本与请求形态：

1. `codex-cli 0.147.0` → `POST /v1/responses`
   - `tools[]` 含四类：`function`（带 `strict`）、`custom`（`apply_patch`，
     `format.{type:grammar,syntax:lark,definition}`）、`tool_search`（`execution:"client"`）、
     `web_search`（`external_web_access`、`search_content_types`）。
   - 顶层含 `include:["reasoning.encrypted_content"]`、`prompt_cache_key`、
     `text.verbosity`、`reasoning.effort`、`store:false`、`client_metadata`。
   - `input[]` 元素带 `id`，`role` 使用 `developer`；多轮工具会话中还出现
     `reasoning`（跨轮携带 `encrypted_content`）、`function_call`、`function_call_output`、
     `custom_tool_call`、`custom_tool_call_output` 五类非 message item。
   - 请求头含 `x-codex-beta-features`、`x-codex-window-id`、`x-codex-turn-metadata`、
     `thread-id`、`session-id`、`originator`。
   - 第二轮抓包触发了真实 `apply_patch` 编辑，取得 custom tool 的双向格式，
     详见 P2-1。
2. `Claude Code 2.1.104`（`@anthropic-ai/sdk` 0.81.0）→ `POST /v1/messages?beta=true`
   - `anthropic-beta` 五项，其中 `prompt-caching-scope-2026-01-05` 与
     `effort-2025-11-24` 为既有实现未覆盖过的取值。
   - `system` 为三段数组，首段是无 `cache_control` 的计费文本
     `x-anthropic-billing-header: cc_version=...`。
   - `tools[]` 共 23 个，`input_schema` 带 `$schema: draft/2020-12`。
   - `metadata.user_id` 的值是内嵌 JSON 字符串
     `{"device_id":...,"account_uuid":...,"session_id":<uuid>}`，
     不再是既有实现假设的 `user_<hash>_account_<uuid>_session_<uuid>` 形态。

## 目标

使网关在两个 CLI 的当前版本上保持会话粘性正确，补齐 Codex 侧审计字段，
并为 Responses 桥接的工具能力缺口留出加固方案。按优先级排列。

## 范围

1. **P0-1　Anthropic 会话键回退解析（已于 2026-08-24 实施）**
   `internal/core/sessionhint` 的 `sessionKeyFromAnthropicMetadata` 原先只识别
   `_session_` 后缀，对新的内嵌 JSON 形态返回空串，回退路径失效。

   已改为「先试 JSON、再退下划线」两条分支：新增 `sessionIDFromEmbeddedJSON`
   （按 `{` 前缀短路后解析取 `session_id`）与 `sessionIDFromUnderscoreSuffix`
   （原逻辑原样搬迁），两者均保留既有 UUID 形状校验，任一失败即空串不粘（R9）。
   主路径 `x-claude-code-session-id` 头优先级不变；`Hint.Source` 维持
   `metadata_user_id`——该值描述字段来源而非格式变体，且只流向结构化日志字段
   `sticky_source`，非 Prometheus label，保持稳定可避免下游日志查询改动。

   失效性已独立验证：对抓包实测的 user_id 值，`strings.LastIndex(userID,
   "_session_")` 返回 `-1`，确认旧逻辑必然产出空串，新增用例可捕获该缺陷。
2. **P1　Codex 审计字段（已于 2026-08-24 实施）**
   `x-codex-turn-metadata` 与 body 内 `client_metadata` 均含 `turn_id`、`thread_id`、
   `request_kind`，原先既不读取也不留痕。

   已新增 `internal/core/clientmeta`：以 ctx 传递（与 `sessionhint` 同模式，避免逐层改签名），
   `ParseCodexTurnMetadata` 解析头值，畸形输入与超长字段静默降级为空——审计字段可缺失，
   但客户端可控输入不得影响请求主链路。两个 OpenAI 族 handler（`/responses` 与
   `/chat/completions`，后者覆盖 Codex 配 `wire_api=chat` 的情形）在 ingress 捕获，
   `RequestLifecycle.PrepareRequest` 读取并写入 `request_records`。仅本地留痕，不转发上游。

   新增 migration `000051_request_records_client_turn_audit`：给 `request_records` 加
   `client_thread_id` / `client_turn_id` / `client_request_kind` 三列，并对
   `client_thread_id` 建部分索引（仅非空行，按线程回溯整段会话是主要查询形态，
   不为其它协议的 NULL 行付出写入代价）。已 `sqlc generate` 并应用到本地 Dev 库。
3. **P2-1　Responses 桥接路径保真度（缺口 a 已于 2026-08-24 实施，其余复核为非缺口）**
   Responses 直传路径按 `RawBody` 原文透传，以下各项**全部不影响直传**，
   只在 chat 桥接路径（DEC-014）出现。

   2026-08-24 核对本地 Dev 库：全部 6 个 channel 均为 `protocol=openai` +
   `adapter_key=openai`，无 chat-only channel。但用户已确认将接入 DeepSeek 渠道，
   故缺口 a 由防御性预案升级为上线前必做项并已实施。

   触发条件：向 OpenAI 路由加入任一只注册 chat 槽的 channel（如 DeepSeek）。

   桥接缺口完整清单（按影响排序）：
   - **a. 工具类型丢弃**（最严重）：`custom`、`tool_search`、`web_search` 三类被
     Drop，其中 `apply_patch` 是 Codex 的文件编辑手段，丢弃后客户端无感知地退化为只读。
   - **b. reasoning 跨轮仅限同类渠道**（2026-08-24 复核后修正，非缺口）：
     桥接内部闭环完整——出站由 `encodeReasoningCarrier` 签发 `unio-rsn-v1:` 载体，
     客户端原样回传后 `extractReasoningText` 按「载体 → `reasoning_text` →
     `summary_text`」三级还原并回灌为 `reasoning_content`（U1，DeepSeek 工具轮次
     缺此字段会 400）。真正的限制只在**直传渠道与桥接渠道互相 fallback** 时出现：
     此时客户端持有的是上游真 `encrypted_content`，桥接侧解不开，只能退到
     `reasoning_text` / `summary_text`。sticky 会话粘性正是为此设计，
     故不列为待修缺口，仅在此记录边界。
   - **c/d. 响应新字段与 `obfuscation`（复核后：非缺口）**：`service_tier`、
     `prompt_cache_retention`、`tool_usage`、`phase`、`obfuscation` 等均为 OpenAI
     Responses 上游特有字段。桥接路径的上游是 chat-only provider，实测 DeepSeek 响应
     不产生其中任何一项，桥接侧「未建模」是因为**没有数据源**而非丢弃；伪造这些字段
     反而会给客户端错误事实。直传路径原样透传，不受影响。
   - **e. 嵌套字段丢弃（复核后：非缺口）**：`tool_search.execution`、
     `web_search.external_web_access` / `search_content_types` 描述的是上游服务端能力，
     chat-only provider 不具备；`input[].id` 在 Chat 协议中无对应承载。三者均属协议固有
     差异，按 R9「不猜第三方语义」保持 Drop + 审计，不做臆测映射。
   - **f. `client_metadata`（复核后：Drop 正确，需求转由 P1 满足）**：其内容是客户端
     审计元数据，chat 契约无承载且不应转发上游，Drop 是正确行为。真正的诉求是本地留痕，
     已由 P1 通过 `x-codex-turn-metadata` 落库实现。

   DEC-014 的桥接语义已确认为既定正确设计（让 chat-only channel 也能服务
   Responses），因此方向是**补全桥接**而非绕开它；不采用「把 chat-only channel
   移出候选」的方案，那等同于废弃 DEC-014。

   针对缺口 a，2026-08-24 抓包已取得 `apply_patch` 的双向实测格式（见
   `tmp/cli-capture-2026-08/codex-custom-tool-call-*.txt`）：
   - 上游响应：item `{"type":"custom_tool_call","call_id","name","input"}`，
     配套事件 `response.custom_tool_call_input.delta` / `.done`，`delta` 为裸文本片段；
   - 客户端回传：`{"type":"custom_tool_call","input":"*** Begin Patch…"}` 与
     `{"type":"custom_tool_call_output","call_id","output"}`，`input`/`output` 均为裸文本。

   据此可设计四向映射：出站 `custom`→function（schema `{"input":"string"}`，
   lark definition 落入 description）；入站 `function_call`→合成 `custom_tool_call`
   与对应 delta 事件；回传两类 item 反向转 `function_call` / `tool` 消息。

   实施落点：新增 `responses_custom_tool.go` 承载四向转换，出站在
   `mapResponsesToolsToChat` 增 custom 分支；回传在 `buildChatMessages` 中与
   `function_call` 合流（仅参数承载不同）；非流式在 `mapChatResponseToResponses`
   按 `req.Tools` 判定还原；流式在 `streamEncoder` 标记 `isCustom`，不逐片转发
   `function_call_arguments.delta`，改在收尾时补发成对的
   `custom_tool_call_input.delta` / `.done`（牺牲逐字粒度换取正确性——裸文本无法从
   JSON 分片增量还原）。

   兜底：上游违反降级 schema 时**不静默产出空 input**（客户端会当成"改了但没生效"），
   而是原样透出上游文本并置 `status=incomplete`，把契约偏差显式暴露。

   原先记录的「约束强度降级」风险经实测**不成立**：以真实 DeepSeek 上游验证，
   简单场景与三处修改的困难场景各 5/5 产出格式合法且语义正确的 patch，上下文行
   精确匹配。端到端回归见 `TestCustomToolBridgeAgainstRealUpstream`
   （需 `UNIO_DEEPSEEK_E2E=1` 与 `DEEPSEEK_API_KEY` 显式开关，默认跳过，只读上游）。

   接入 DeepSeek 渠道前仍需确认 Test/Production 的 `route_channels` 现状。
4. **P2-2　策略核对（已于 2026-08-24 核对，无需改动）**
   - 本地 `app_settings` 实测 `anthropic.beta_policy` =
     `{"mode":"filter","list":["context-1m-2025-08-07"]}`，黑名单模式且仅挡一项，
     Claude Code 2.1.104 的五个 beta flag（含 `prompt-caching-scope-2026-01-05` 与
     `effort-2025-11-24`）全部放行。Test/Production 需各自复核同一 key。
   - `?beta=true` query 不转发上游：上游 URL 由 `BuildUpstreamURL` 固定拼接，
     Anthropic 官方渠道以 `anthropic-beta` 头为准，该 query 不影响能力协商。

## 已核对确认无缺口

以下项已用本次抓包逐项比对现有实现，结论是当前实现正确，不列入改造范围，
记录在此避免后续重复排查：

1. **Responses 直传 usage 映射**：以抓包真实值
   `{input_tokens:8922, cached_tokens:7040, output_tokens:19, reasoning_tokens:12,
   total_tokens:8941}` 核对，`UncachedInputTokens=1882`、`CacheReadInputTokens=7040`、
   `OutputTokensTotal=19`（含 reasoning）、`ReasoningOutputTokens=12`，
   且 `input+output==total` 自洽，与 `openai.responses.v2` 口径一致。
2. **Responses 直传的请求与 SSE**：四类 tools、`include`、`prompt_cache_key`、
   `client_metadata`、超长 `instructions`，以及含 `obfuscation`、真
   `encrypted_content` 的上游事件均按 RawBody / 原文透传，无 Drop。
3. **Codex 专属请求头不转发上游**：经裸请求验证，不带 `originator`、`x-codex-*`
   时上游仍正常返回 200，故不构成功能缺口，仅为审计缺口（已收敛到 P1）。
4. **Anthropic 默认 beta 策略**：黑名单仅 `context-1m-2025-08-07`，
   抓包中五个 flag 全部放行，含两个新取值。
5. **Anthropic system 数组、逐段 `cache_control`、23 个 tools 的
   `$schema`**：均以 `json.RawMessage` 原样透传，未被结构化改写。
6. **Anthropic SSE `message_stop` 截留**：adapter 截留、结算后由 service 补发，
   客户端可见事件序列与官方一致。
7. **Anthropic 流式 usage 映射**（2026-08-24 实测补验，见
   `tmp/cli-capture-2026-08/claude-sse-cache-*.txt`）：同一请求连发两次，
   写缓存轮 `cache_creation_input_tokens=4434` /
   `cache_creation.ephemeral_5m_input_tokens=4434` / `cache_read=0`，
   命中轮 `cache_creation=0` / `cache_read_input_tokens=4434`，缓存按预期生效。
   与 `MessageUsage.ToUsageFacts` 逐项比对一致：`input_tokens`→`UncachedInputTokens`、
   `cache_read_input_tokens`→`CacheReadInputTokens`、`cache_creation.ephemeral_5m/1h`
   →`CacheWrite5m/1h`、`CacheWrite30m` 恒 `not_applicable`。
   实测事件序列 `message_start → content_block_start → ping → content_block_delta×N
   → content_block_stop → message_delta → message_stop`，全部在既有处理范围内。
8. **Anthropic 响应未建模字段不丢失**：实测响应含 `inference_geo`、
   `context_management.applied_edits`（`context-management-2025-06-27` beta 产物）、
   `stop_details` 三个 wire DTO 未建模字段；adapter 以 `cloneRaw(ev.Data)`
   整段克隆透传 SSE data，未建模字段随原文送达客户端，仅不参与内部结算。
   `service_tier` 已在 `usageWire` 建模。

## 验证

P0-1 实测结果（2026-08-24）：

1. `gofmt -l internal/core/sessionhint` 无输出。
2. `go test ./internal/core/sessionhint/... -count=1` 通过，6 个测试函数全绿。
   新增 `TestAnthropicSessionKeyEmbeddedJSONMetadata`（用抓包原值，含
   `account_uuid` 为空串这一实测特征，并断言头仍优先于 body 回退）；
   `TestAnthropicSessionKeyStrictParse` 用例表由 6 项扩到 10 项，新增
   `session_id` 缺失、非 UUID、空串、以及 `{` 开头但 JSON 畸形四个失败分支；
   原有旧格式用例全部保留并通过，构成向后兼容回归。
3. `go test ./...` 退出码 0，96 个包通过，0 个 FAIL。
4. `git diff --check` 通过。

P2-1a 与 P1 实测结果（2026-08-24）：

5. `responses` 包新增 `TestCustomTool*` 五组单测，覆盖出站降级、回传往返、非流式还原、
   四类兜底分支（裸 payload / 缺 input 键 / input 非字符串 / 空 arguments）与流式事件对，
   并断言 custom 工具不得发出 `function_call_arguments.delta`。
6. `TestCustomToolBridgeAgainstRealUpstream` 对真实 DeepSeek 上游跑通完整链路：
   出站降级 → 上游产出 → 还原为 `custom_tool_call`，patch 首尾标记完整、`status=completed`。
7. `clientmeta` 新增四组单测：抓包原值解析、五类畸形输入静默降级、超长字段丢弃而兄弟字段
   保留、ctx 往返（全空不写入）。
8. `go test ./...` 退出码 0，97 个包通过、0 个 FAIL；`gofmt` 无输出。
9. migration `000051` 已应用到本地 Dev 库并校验三列就位；`sqlc generate` 使用 v1.31.1，
   与 DEVELOPMENT.md 记录一致。

## 约束

1. P0-1、P1、P2-1a 均经用户确认后实施；P2-1 的 c/d/e/f 复核为非缺口，不改代码。
2. 不手改 `internal/platform/store/sqlc` 生成文件；若触及 `migrations/` 或
   `sql/queries/` 需重跑 `sqlc generate`。
3. 抓包原件含会话标识与设备标识，只留在 gitignore 覆盖的 `tmp/` 下，不入库、不写入测试夹具。
4. 本次不操作远程环境、不提交、不推送。

## 抓包完成度

请求侧与响应侧均已取得，无遗留待补项。

Claude 响应侧一度受阻：中转服务商的 Claude 账号池不可用，两张分属不同授权分组的
key 均返回 `503 No available accounts` 或 `502`，同期 Codex 侧 200 可排除整站故障。
服务商更换线路后 `claude-sonnet-4-6` 恢复，遂以最小成本方式补测——不跑完整
Claude Code CLI（单次约 25K input tokens），改用等价特征请求（system 数组 +
逐段 `cache_control` + 五个 beta flag + tools + `stream`，约 1.5K tokens）连发两次，
即取得写缓存与命中缓存两种 usage 形态及完整 SSE 序列，结论见「已核对确认无缺口」
第 7、8 项。

归档于 `tmp/cli-capture-2026-08/`（gitignore 覆盖，已校验无明文凭据）：

| 文件 | 内容 |
| --- | --- |
| `codex-cli-0.147.0-responses.txt` | Codex 基础请求与 SSE |
| `codex-custom-tool-call-response.txt` | `apply_patch` 上游调用事件流 |
| `codex-custom-tool-call-roundtrip.txt` | `apply_patch` 客户端回传往返 |
| `claude-code-2.1.104-messages.txt` | Claude Code 完整请求 |
| `claude-sse-cache-write.txt` | Anthropic 写缓存轮 SSE 与 usage |
| `claude-sse-cache-read.txt` | Anthropic 命中缓存轮 SSE 与 usage |
