# OpenAI 官方 · 协议与参数映射

> 本文件是 OpenAI **官方一方**端点(`POST /v1/chat/completions`)的**权威逐字段策略**。
> 适配逻辑见同目录 [adaptation.md](adaptation.md);计费见 [billing.md](billing.md);
> 接入与路线 C 改造待办见 [upgrade-plan.md](upgrade-plan.md)。
> 标准 OpenAI 协议本身(完整字段语义)见
> [docs/protocol/openai/chat-completions/](../../protocol/openai/chat-completions/params.md),本文件**不重抄**。

## 0. 总原则:官方 = 基线,默认 Pass

与第三方上游(如 DeepSeek)不同,官方 OpenAI 端点**就是协议本身**,能接收并正确处理 OpenAI 协议的全部合法字段。
因此本 provider 的策略表与第三方相反:

- **默认 `Pass`**:ingress 收到的任何合法 OpenAI 字段,原样写入 upstream wire,不改名、不改值、不丢弃。
- **极少数 `Adapt`**:只有 routing 与计费**必须**改写的两项(见 §2)。
- **`Drop` 原则上为空**:官方不存在"会被 400 的合法字段"。仅当官方文档明确某字段已删除/不再接受时才 Drop,
  且必须标官方依据。当前**无**此类字段。

全局原则见 [DEC-012 协议为先与 Provider 映射 Drop 策略](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)。

状态:

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名与语义一致,原样写入 upstream wire / 响应。 |
| `Adapt` | 显式转换后写入(本 provider 仅 routing 与计费两项)。 |
| `Drop` | ingress 可收但不写入 upstream(本 provider 原则上为空)。 |

## 1. 支持参数(Pass,忠实透传)

下表为本 provider **相对标准协议无差异**的确认(全部 Pass)。完整字段语义见 `docs/protocol`,此处只声明策略。

| 字段 | 策略 | 说明 |
| --- | --- | --- |
| `messages`(含 `developer`/`system`/`user`/`assistant`/`tool` role) | `Pass` | **role 原样透传**,含 `developer`(见 §3 与 §5)。 |
| `messages[].content`(string / 多模态 part:`text`/`image_url`/`input_audio`/`file`) | `Pass` | 多模态 part 不剔除。 |
| `max_completion_tokens` | `Pass` | **原样透传为同名字段**(官方新模型用它,见 §5)。 |
| `max_tokens` | `Pass` | 客户显式传则原样透传(官方仍接受,标记 deprecated)。 |
| `temperature` / `top_p` | `Pass` | |
| `presence_penalty` / `frequency_penalty` | `Pass` | |
| `stop` | `Pass` | string 或数组。 |
| `n` | `Pass` | 多 choice 原样透传(官方支持)。 |
| `seed` | `Pass` | |
| `logprobs` / `top_logprobs` | `Pass` | |
| `logit_bias` | `Pass` | |
| `response_format`(`text`/`json_object`/`json_schema`) | `Pass` | **含 `json_schema`**(官方支持结构化输出)。 |
| `tools[]`(`function` / `custom` / 内置 server tool) | `Pass` | **不剔除任何工具类型**(官方支持)。 |
| `tool_choice`(`none`/`auto`/`required`/named) | `Pass` | |
| `parallel_tool_calls` | `Pass` | 官方支持,原样透传(对第三方才 Drop)。 |
| `reasoning_effort` | `Pass` | **原样透传官方枚举**,不归一为 `high`/`max`(那是 DeepSeek 规则)。 |
| `modalities` | `Pass` | |
| `audio` / `prediction` | `Pass` | |
| `web_search_options` | `Pass` | |
| `store` / `metadata` | `Pass` | |
| `service_tier` | `Pass` | |
| `verbosity` | `Pass` | |
| `prompt_cache_key` / `prompt_cache_retention` | `Pass` | |
| `user` / `safety_identifier` | `Pass` | **保留标准 `user` 顶层字段**,不改名为 `user_id`(那是 DeepSeek wire)。 |
| `function_call` / `functions`(legacy) | `Pass` | 官方仍兼容 legacy,原样透传。 |
| `stream` | `Pass` | |
| 任何未显式建模的合法字段 | `Pass` | 经 `Extensions` 透传(见 [adaptation.md](adaptation.md))。 |

> 出站采用「**typed 字段 + Extensions 透传**」:base wire DTO 显式建模常用字段,其余合法字段经
> `Extensions` 原样 merge,不丢弃。

## 2. 转换参数(Adapt,仅两项)

| 客户字段 | upstream wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `model` | `model` | `Adapt` | routing 选中的 upstream model(catalog → upstream_model 映射)。 |
| `stream_options.include_usage` | 同名 | `Adapt` | 流式时 adapter **强制注入 `true`**,以获取计费所需的尾包 usage(settlement 事实来源)。客户即使没传或传 false,也注入 true。 |

## 3. `messages[]` 映射

| 客户消息 | upstream wire | 策略 |
| --- | --- | --- |
| `developer` role | **`developer` role** | `Pass`(官方原生支持;**不**塌缩为 `system`) |
| `system` role | `system` role | `Pass` |
| `user` / `assistant` / `tool` role | 同名 | `Pass` |
| `assistant.tool_calls`(`function` / `custom`) | 同名 | `Pass`(不剔除 custom) |
| `content` 多模态 part | 同名 | `Pass`(不剔除 image/audio/file) |

## 4. 废弃 / 丢弃参数(Drop)

**空。** 官方端点不丢弃任何合法 OpenAI 字段。若未来官方文档明确删除某字段,在此登记并标官方依据(链接 + 日期)。

## 5. 路线 C:去方言化下沉清单(已完成,2026-06-12)

协议族 base adapter(`internal/core/adapter/openai/chatcompletions`)曾被 DeepSeek 接入烤进两处**有损改写**。
路线 C 改造已于 2026-06-12 完成:base 回归忠实官方基线,两条方言下沉到 `openai/deepseek` 层。

| # | 原 base 行为(方言) | 路线 C 处置(已落地) |
| --- | --- | --- |
| L1 | `max_completion_tokens` 被塌缩为 wire `max_tokens`(原 `resolveWireMaxTokens`) | base wire DTO 新增 `max_completion_tokens` 字段,两字段**各自独立忠实输出**;塌缩规则下沉为 `openai/deepseek` 的 `adaptMaxCompletionTokens`(`adapt.go`,在 `dropUnsupported` 入口执行,冲突时仍优先 completion tokens,行为零回归) |
| L2 | `developer` role 被映射为 `system`(原 `mapWireMessageRole`) | base 忠实透传 `developer`;塌缩规则下沉为 `openai/deepseek` 的 `adaptDeveloperRole`(保持消息相对顺序,行为零回归) |

双向回归测试:base `request_wire_test.go`(官方路径忠实)+ deepseek `drop_test.go`(DeepSeek 塌缩零回归)。

> 黑盒冻结记录(2026-06-12,经 OpenRouter OpenAI 协议端点实测,`https://openrouter.ai/api/v1`):
>
> - `developer` role、`max_completion_tokens`、`max_tokens`+`max_completion_tokens` **双字段同传**、
>   `reasoning_effort` 原生枚举:忠实 wire 均被 200 接受(`internal/core/adapter/openai/blackbox_test.go` 三用例全过)。
> - 流式注入 `stream_options.include_usage` 的尾包 usage 形状(含 `*_tokens_details` 的 cached/reasoning 维度)正常解析。
> - ⚠️ 残留待查证:**OpenAI 官方直连端点**(`api.openai.com`)与官方在售模型(gpt-5.5 等)的实测被
>   OpenRouter 账户级 403(provider ToS,中国区限制)拦截,无法经该代理完成;待可用的官方 key / 渠道后
>   重跑同一组黑盒用例完成最终冻结(用例已就位,env 门控:`OPENAI_BLACKBOX=1` + key/base_url/model)。

下沉后,DeepSeek 侧的这两条规则与现有 [../deepseek/openai/protocol-and-params.md](../deepseek/openai/protocol-and-params.md)
§2(`max_completion_tokens`→`max_tokens` Adapt)、§3(`developer`→`system` Adapt)保持一致,行为不变。

## 6. 非流式响应映射(upstream → 公开 OpenAI)

| upstream 字段 | 客户字段 | 策略 |
| --- | --- | --- |
| `id` / `object` / `created` / `system_fingerprint` | 同名 | `Pass` |
| `model` | `model` | `Adapt`(返回 Unio catalog model;upstream model 记入 facts) |
| `choices[].*`(`message`/`logprobs`/`finish_reason`/`tool_calls`/`reasoning_content` 等) | 同名 | `Pass` |
| `usage` | 同名 | `Pass`(见 §7) |

官方响应忠实透传,不裁剪字段。

## 7. usage 映射

官方 `usage` 形状(查阅 2026-06-12,[官方·The chat completion object](https://platform.openai.com/docs/api-reference/chat/object)):

| upstream 字段 | 客户字段 | `usage.Facts` |
| --- | --- | --- |
| `usage.prompt_tokens` | 同名 | 总输入 |
| `usage.prompt_tokens_details.cached_tokens` | 同名 | `CacheReadInputTokens` |
| `usage.completion_tokens` | 同名 | `OutputTokensTotal` |
| `usage.completion_tokens_details.reasoning_tokens` | 同名 | `ReasoningOutputTokens` |
| `usage.total_tokens` | 同名 | 校验总量 |

校验:`total_tokens = prompt_tokens + completion_tokens`。

## 8. 流式响应映射

官方 OpenAI 风格 SSE(`data: {chunk}` … `data: [DONE]`):

| upstream SSE | 客户 SSE | 策略 |
| --- | --- | --- |
| `delta.*`(`role`/`content`/`tool_calls`/`reasoning_content`) | 同名 | `Pass` |
| `finish_reason` | 同名 | `Pass` |
| final `usage`(因 §2 注入 include_usage 而出现) | 尾包 + facts | `Adapt` |
| `[DONE]` | `[DONE]` | `Adapt`(adapter 截留;lifecycle 收口后写出) |
