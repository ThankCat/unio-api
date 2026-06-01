# Phase 10 ResponseFacts - 响应、审计与计费事实

## 1. 为什么需要 ResponseFacts

OpenAI 与 Anthropic 的客户响应不能强行统一：

```text
OpenAI    → ChatCompletionResponse / ChatCompletionChunk
Anthropic → MessageResponse / MessageStreamEvent
```

但 settlement、recovery 和审计必须消费稳定事实。否则 billing 会被迫理解两套公开 DTO，后续 Gemini 接入时还会继续复制分支。

本阶段固定双轨输出：

```text
协议原生响应
→ 返回给对应 SDK

统一 ResponseFacts
→ request log
→ usage record
→ settlement
→ recovery
→ metrics / tracing
```

两条轨道必须在 adapter 同一次解析中产生。

## 2. 建议结构

```go
package adapter

type ResponseFacts struct {
    UpstreamProtocol   string
    UpstreamResponseID string
    UpstreamModel      string
    Finish             FinishFacts
    Usage              usage.Facts
    UsageSource        usage.Source
    UsageMappingVersion string
    Metadata           UpstreamMetadata
}

type FinishFacts struct {
    Class     string
    RawReason string
}

type StreamOutcome struct {
    Facts *ResponseFacts
}
```

`FinishFacts.Class` 使用稳定有限集合：

```text
stop
length
tool_use
content_filter
refusal
pause
other
```

`RawReason` 保存 provider 原始 finish reason，用于审计，不直接驱动跨模块业务逻辑。

## 3. UsageFacts

### TokenCount

商业计费不能混淆：

```text
已知为 0
不适用于此协议
上游没有返回
解析失败
```

建议使用显式状态：

```go
package usage

type CountState string

const (
    CountKnown         CountState = "known"
    CountNotApplicable CountState = "not_applicable"
    CountUnknown       CountState = "unknown"
)

type TokenCount struct {
    Value int64
    State CountState
}

type Facts struct {
    UncachedInputTokens     TokenCount
    CacheReadInputTokens    TokenCount
    CacheWrite5mInputTokens TokenCount
    CacheWrite1hInputTokens TokenCount
    OutputTokensTotal       TokenCount
    ReasoningOutputTokens   TokenCount
    ServerToolUsage         []MeteredItem
}

type MeteredItem struct {
    Kind     string
    Quantity int64
}
```

规则：

1. `unknown` 不能在 settlement 中偷偷按 `0` 处理。
2. `not_applicable` 可以按 `0` 处理，但必须由 adapter 显式给出。
3. `OutputTokensTotal` 是包含 reasoning 的 authoritative output 总量。
4. `ReasoningOutputTokens` 是 output 的可选分解项，不是额外生成量。
5. 如果某 provider 只按总 output 计价，billing 不拆 reasoning。
6. 如果某价格策略要单独计算 reasoning，必须先确认 reasoning count 可靠且不会与总 output 重复收费。

### MeteredItem

Anthropic usage 还可能包含 server tool 次数：

```text
server_web_search_request
server_web_fetch_request
```

未来出现新的可计费附加项时，必须新增受控 `Kind` 常量、价格维度和测试。禁止直接把任意 JSON key 当作账务维度。

本阶段只为 Anthropic/DeepSeek 返回的 server tool usage 建立可审计账务事实，
不提前实现完整模型能力系统。server tool 是否允许调用仍由协议字段 mapping、
provider 官方文档、后续模型契约和黑盒验收共同决定；ResponseFacts 只记录已经
发生且 adapter 能可靠解析的计量事实。

## 4. OpenAI usage 映射

OpenAI Chat Completions usage：

```text
prompt_tokens
prompt_tokens_details.cached_tokens
completion_tokens
completion_tokens_details.reasoning_tokens
completion_tokens_details.audio_tokens
completion_tokens_details.accepted_prediction_tokens
completion_tokens_details.rejected_prediction_tokens
```

统一映射：

| OpenAI 字段 | UsageFacts |
| --- | --- |
| `prompt_tokens - prompt_tokens_details.cached_tokens` | `UncachedInputTokens` |
| `prompt_tokens_details.cached_tokens` | `CacheReadInputTokens` |
| 协议没有 cache write 事实 | `CacheWrite5mInputTokens = not_applicable` |
| 协议没有 cache write 事实 | `CacheWrite1hInputTokens = not_applicable` |
| `completion_tokens` | `OutputTokensTotal` |
| `completion_tokens_details.reasoning_tokens` | `ReasoningOutputTokens` |

DeepSeek OpenAI endpoint 可能返回更明确的：

```text
prompt_cache_hit_tokens
prompt_cache_miss_tokens
```

`adapter/openai/deepseek` 必须优先使用 provider 明确字段，并校验与 total 一致。映射规则见 [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md)。

## 5. Anthropic usage 映射

Anthropic Messages usage：

```text
input_tokens
cache_read_input_tokens
cache_creation_input_tokens
cache_creation.ephemeral_5m_input_tokens
cache_creation.ephemeral_1h_input_tokens
output_tokens
output_tokens_details.thinking_tokens
server_tool_use.web_search_requests
server_tool_use.web_fetch_requests
service_tier
inference_geo
```

统一映射：

| Anthropic 字段 | UsageFacts |
| --- | --- |
| `input_tokens` | `UncachedInputTokens` |
| `cache_read_input_tokens` | `CacheReadInputTokens` |
| `cache_creation.ephemeral_5m_input_tokens` | `CacheWrite5mInputTokens` |
| `cache_creation.ephemeral_1h_input_tokens` | `CacheWrite1hInputTokens` |
| `output_tokens` | `OutputTokensTotal` |
| `output_tokens_details.thinking_tokens` | `ReasoningOutputTokens` |
| `server_tool_use.web_search_requests` | `MeteredItem{Kind: server_web_search_request}` |
| `server_tool_use.web_fetch_requests` | `MeteredItem{Kind: server_web_fetch_request}` |

输入总量：

```text
input_tokens
+ cache_read_input_tokens
+ cache_creation_input_tokens
```

`cache_creation_input_tokens` 应与 5m、1h breakdown 一致。若 breakdown 缺失但总 cache creation 存在，必须记录 `unknown` 或单独设计兼容维度，不能擅自把所有 cache write 归入 5m。

`output_tokens_details.thinking_tokens` 是 output 的分解项。Anthropic 参考文档说明该值是通过重新 tokenize raw reasoning 得到的观测值，可能与精确生成计数有小幅差异。因此：

1. 默认把 `output_tokens` 作为 authoritative output 成本事实。
2. thinking tokens 可用于审计和观测。
3. 若未来要用 thinking 单独定价，必须增加明确商业决策和价格策略。

## 6. Response ID 与 model

必须分开记录：

```text
response_id
= 返回给客户的 ID

upstream_response_id
= provider 返回的 ID

response_model
= 返回给客户的 Unio catalog model

upstream_model
= channel_models 映射后的 provider model
```

当前 DeepSeek adapter 可以保留 upstream ID 作为客户 response ID，但数据库仍需分列。未来 bridge、重放或本地生成 ID 时，两者可能不同。

## 7. 非流式 settlement

```text
adapter 解析完整 upstream response
→ 同时生成 protocol response + ResponseFacts
→ lifecycle 持久化 recovery job
→ settlement
→ settlement 成功或 durable recovery job 接管后返回 protocol response
```

如果 settlement 首次失败：

1. 不 release reservation。
2. recovery job 保留 immutable facts。
3. worker 复用幂等 settlement。
4. recovery job 已持久化时，客户仍收到协议原生成功响应；账务状态记录为 pending recovery。

如果 recovery job 自身无法持久化：

1. 不向客户返回协议成功。
2. 返回安全 5xx。
3. 写告警与 best-effort 风险审计。
4. 不允许产生“客户看到成功，但平台没有可重放账务事实”的窗口。

## 8. 流式 settlement

OpenAI：

```text
通常从最终 usage chunk 得到可靠 usage
```

Anthropic：

```text
message_start 可能带初始 usage
message_delta 带最终 usage 增量或最终事实
message_stop 表示流结束
```

adapter 负责累积协议细节，lifecycle 只接收最终 `StreamOutcome`。

客户可见终态：

```text
OpenAI    → [DONE]
Anthropic → message_stop
```

adapter 必须截留 upstream 终态，不直接 emit 给客户。lifecycle 先持久化 immutable
recovery facts，再执行 settlement。settlement 成功或 durable recovery job 接管后，
对应 gatewayapi writer 才输出客户可见终态。

若 recovery facts 无法持久化：

```text
不写 [DONE] / message_stop
→ 写协议原生 stream error
→ delivery_status=interrupted
→ 告警 + best-effort 风险审计
```

客户端断开后，结算使用有上限的内部 context 继续收口，不依赖已取消的 HTTP context。

规则：

| 场景 | settlement | delivery_status |
| --- | --- | --- |
| 正常结束、usage 可靠且 durable closeout 成功 | settle 或 pending recovery | `completed` |
| 客户端取消，但 usage 已可靠 | settle | `interrupted` |
| upstream tail error，但 usage 已可靠 | settle | `interrupted` |
| usage 可靠但 recovery facts 无法持久化 | 告警 + best-effort 风险审计 | `interrupted` |
| 已产生可见输出，但无可靠 usage | `risk_exposure` | `interrupted` |
| 首包前失败且无上游成本事实 | release，可 fallback | `not_started` |

## 9. Schema 改造范围

### `usage_records`

至少增加：

```text
uncached_input_tokens
cache_read_input_tokens
cache_write_5m_input_tokens
cache_write_1h_input_tokens
output_tokens_total
reasoning_output_tokens
usage_source
usage_mapping_version
```

对于需要区分 unknown 的字段，数据库使用 nullable 或显式 state 列，不用魔法数。

### `usage_line_items`

建议新增：

```text
usage_record_id
kind
quantity
```

用于 server tool 等附加计量项。`kind` 必须受控，不接受任意 provider key。
初始 `kind` 只允许 Phase 10 明确登记并测试过的 server tool usage，例如
`server_web_search_request`、`server_web_fetch_request`。未登记 key 必须进入
mapping 黑盒冻结，不能自动入账。

### 价格与快照

`prices`、`price_snapshots`、`channel_cost_prices`、`cost_snapshots` 至少增加：

```text
uncached_input_price
cache_read_input_price
cache_write_5m_input_price
cache_write_1h_input_price
output_price
reasoning_output_price
```

如果 reasoning output 不参与独立计价，价格策略必须明确使用 output total，不做重复扣费。

### `settlement_recovery_jobs`

必须持久化：

```text
ResponseFacts
price snapshot facts
cost snapshot facts
reservation facts
```

worker 只重放 immutable facts，不读取易变价格，不重新调用 adapter，不重新解析 response body。

## 10. 审计隐私

默认不保存：

```text
完整 prompt
完整响应正文
原始 provider error body
credential
API key 明文
```

允许保存：

```text
协议
operation
response ID
model
finish reason
usage
provider/channel
安全错误码
截断后的内部诊断详情
有限安全 metadata
```

如果未来需要 payload debug log，必须单独设计：

```text
显式开关
字段脱敏
加密
TTL
权限
审计
```
