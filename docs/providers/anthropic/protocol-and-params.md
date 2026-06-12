# Anthropic 官方 · 协议与参数映射

> 本文件是 Anthropic **官方一方**端点(`POST /v1/messages`)的**权威逐字段策略**。
> 适配逻辑见同目录 [adaptation.md](adaptation.md);计费见 [billing.md](billing.md);
> 接入待办见 [upgrade-plan.md](upgrade-plan.md)。
> 标准 Anthropic 协议本身见 [docs/protocol/anthropic/messages/](../../protocol/anthropic/messages/params.md),本文件**不重抄**。

## 0. 总原则:官方 = 基线,默认 Pass

官方 Anthropic 端点**就是协议本身**,能接收并正确处理 Messages 协议的全部合法字段。因此:

- **默认 `Pass`**:ingress 收到的任何合法 Messages 字段,原样写入 upstream wire,不改名、不改值、不丢弃。
- **极少数 `Adapt`**:仅 `model`(routing)。
- **`Drop` 原则上为空**:官方不存在"会被 400 的合法字段"。

全局原则见 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)。

状态:

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名与语义一致,原样写入 upstream wire / 响应。 |
| `Adapt` | 显式转换后写入(本 provider 仅 `model`)。 |
| `Drop` | ingress 可收但不写入 upstream(本 provider 原则上为空)。 |

## 1. 支持参数(Pass,忠实透传)

base wire DTO(`internal/core/adapter/anthropic/wire.go` `messagesRequest`)已对复杂 union 用
`json.RawMessage` 原样透传,以下全部为 `Pass`:

| 字段 | 策略 | 说明 |
| --- | --- | --- |
| `system`(string / block 数组) | `Pass` | `json.RawMessage` 原样透传。 |
| `messages[]`(role + content) | `Pass` | content union 原样透传。 |
| `messages[].content` 各 block(`text`/`image`/`document`/`tool_use`/`tool_result`/`thinking`/`redacted_thinking`/`server_tool_use`/`web_search_tool_result`/MCP/container 等) | `Pass` | **不剔除任何 block 类型**(剔除是 DeepSeek 规则)。 |
| `max_tokens` | `Pass` | typed,原样透传。 |
| `stop_sequences` | `Pass` | typed。 |
| `temperature` / `top_p` | `Pass` | typed。 |
| `top_k` | `Pass` | typed,**忠实透传**(丢弃 `top_k` 是 DeepSeek 规则)。 |
| `thinking`(`enabled`/`disabled` + budget_tokens) | `Pass` | `json.RawMessage` 原样透传,**不归一**。 |
| `tools[]`(client custom + 内置 server tool) | `Pass` | `json.RawMessage` 原样透传,**不剔除 server tool**。 |
| `tool_choice` | `Pass` | `json.RawMessage` 原样透传。 |
| `metadata`(含 `user_id` 及其它) | `Pass` | 原样透传,**不裁剪非 `user_id` 字段**(裁剪是 DeepSeek 规则)。 |
| `stream` | `Pass` | typed。 |
| 顶层扩展(`container` / `service_tier` / `mcp_servers` / `output_config` 等合法字段) | `Pass` | 经 `Extensions` 原样 merge(已存在键不覆盖),官方接受 → 不 Drop。 |

> 出站机制:`buildMessagesRequestBody`(`wire.go` L87–114)先编码 typed 字段,再把 `Extensions` 经
> `mergeJSONObjects` 原样合并。合法字段不丢弃。

## 2. 转换参数(Adapt,仅一项)

| 客户字段 | upstream wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `model` | `model` | `Adapt` | routing 选中的 upstream model(catalog → upstream_model)。 |

> 注:Anthropic 流式 usage 不需要像 OpenAI 那样注入 `stream_options`——`message_delta` 原生携带终态
> usage 与 `stop_reason`(见 [adaptation.md](adaptation.md) §4),base 已据此收口计费,无需改写请求。

## 3. 废弃 / 丢弃参数(Drop)

**空。** 官方端点不丢弃任何合法 Messages 字段。base 当前也无任何请求侧 Drop(所有 Drop 都在 deepseek 层)。
若未来官方文档明确删除某字段,在此登记并标官方依据。

## 4. 官方 1P 缺口清单(本次接入要补)

与 OpenAI 不同,Anthropic base **不需要**从 base 下沉方言(本来就干净)。但接官方 1P 有两个**新增能力**缺口:

| # | 缺口 | 现状 / 代码位置 | 官方依据 | 处置 |
| --- | --- | --- | --- | --- |
| G1 | `anthropic-beta` 头**未透传到上游** | 按 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略) 第 4 点,beta 头当前在 gatewayapi 层 Drop;base `do()`(`adapter.go` L230–235)只设 `x-api-key` + `anthropic-version`,**不转发 `anthropic-beta`** | DEC-012 明确:"未来接入真实 Anthropic 1P adapter 时,应改为按登记表把支持的 beta Pass 转发到 upstream `anthropic-beta`" | 官方 adapter 需按**支持登记表**透传 `anthropic-beta`(白名单),否则 beta 特性(扩展上下文、特定工具等)收不到。见 upgrade-plan N1。⚠️ 待查证:接入时确定要支持的 beta 集合。 |
| G2 | 缺官方 input tokenizer | base `anthropic.Adapter` 只实现 `MessagesAdapter`/`StreamMessagesAdapter`(`adapter.go` L345–348),**未实现** `MessagesInputTokenizer`;DeepSeek 用的是其自有 tokenizer | 注册 `(anthropic, anthropic)` 需要 `MessagesInputTokenizer`(authorization 预扣费) | 官方 adapter 需提供 tokenizer:保守字符启发式估算,或调官方 [Count Message tokens](https://docs.anthropic.com/en/api/messages-count-tokens) 端点。见 upgrade-plan N2。 |
| G3 | `anthropic-version` 固定 `2023-06-01` | base 硬编码(`adapter.go` L20–22) | 官方版本头策略 | ⚠️ 待查证:确认官方当前推荐基线版本;如需更高版本以支持新特性,评估是否可配置。 |

## 5. 非流式响应映射(upstream → 公开 Anthropic)

| upstream 字段 | 客户字段 | 策略 |
| --- | --- | --- |
| `id` / `type` / `role` | 同名 | `Pass` |
| `model` | `model` | `Adapt`(返回 catalog model;upstream model 记 facts) |
| `content[]`(各 block) | 同名 | `Pass`(忠实透传) |
| `stop_reason` / `stop_sequence` | 同名 | `Pass` |
| `usage` | 同名 | `Pass`(见 §6) |

base `Messages`(`adapter.go` L66–99)已忠实解析并回传。

## 6. usage 映射

官方 `usage` 形状(base `usageWire`,`wire.go` L47–70;查阅 2026-06-12 [官方·Messages](https://docs.anthropic.com/en/api/messages)):

| upstream 字段 | `MessageUsage` / Facts |
| --- | --- |
| `input_tokens` | 输入 token |
| `cache_creation_input_tokens` | 缓存写入 token(总) |
| `cache_creation.ephemeral_5m_input_tokens` / `ephemeral_1h_input_tokens` | 5m / 1h TTL 缓存写入(分维度) |
| `cache_read_input_tokens` | 缓存命中读取 token |
| `output_tokens` | 输出 token |
| `output_tokens_details.thinking_tokens` | thinking(reasoning)输出 token |
| `server_tool_use.web_search_requests` / `web_fetch_requests` | 内置工具调用次数(计费维度) |
| `service_tier` | 服务档位(透传) |

## 7. 流式响应映射

官方 Anthropic SSE(原生事件:`message_start` / `content_block_*` / `message_delta` / `message_stop` 等):

| upstream 事件 | 对外 | 策略 |
| --- | --- | --- |
| `message_start` | 同名 | `Pass`(base 截取 id/model/初始 usage) |
| `content_block_start/delta/stop` | 同名 | `Pass` |
| `message_delta`(携终态 usage + stop_reason) | 同名 + 内挂合并 usage | `Adapt`(对外透出原生事件,内部挂 usage 供 settlement) |
| `message_stop` | `message_stop` | `Adapt`(adapter 截留为成功终态;lifecycle 收口后写出) |

base `StreamMessages`(`adapter.go` L107–207)已实现:`message_stop` 截留、`message_delta` 终态 usage 收口。
