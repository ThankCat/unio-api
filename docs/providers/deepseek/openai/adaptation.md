# DeepSeek · OpenAI 格式 适配与转换逻辑

本文件记录 Unio 在 **OpenAI 兼容格式**(`POST /chat/completions`)上接 DeepSeek 的关键转换逻辑与
行为决策(思考、reasoning 跨轮、工具、usage、模型名、错误、流式)。逐字段映射表见同目录
[protocol-and-params.md](protocol-and-params.md);计费金额口径见 [../billing.md](../billing.md)。

## 1. 思考模式(reasoning / thinking)

DeepSeek v4 单个模型即可在「非思考 / 思考」间切换,默认**思考开启**。OpenAI 格式下的控制:

| 控制 | 字段 | 说明 |
| --- | --- | --- |
| 开关 | `thinking: {type: enabled/disabled}` | 默认 enabled |
| 强度 | `reasoning_effort: high/max` | 仅这两档生效 |

来源:[官方·思考模式](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode)(查阅 2026-06-07)。

强度归一(Unio 出站显式归一,不依赖上游兼容):`minimal/low/medium/high → high`,`xhigh/max → max`,
未知值丢弃让上游回退默认。代码:`internal/core/adapter/openai/deepseek/chatcompletions/adapt.go`(`deepseekReasoningEfforts`)。

Unio 的开关策略(DEC-016):
- **Chat Completions 入口**:不干预,保持 DeepSeek 默认(思考开)。
- **Responses 入口**:思考是 **opt-in**。客户未带 `reasoning` 字段时,Unio 置内部标志 `ReasoningDisabled`,
  由 DeepSeek adapter 出站注入 `thinking:{type:"disabled"}`,避免非 reasoning 请求白白产生思维链(多花钱)。
  客户带 `reasoning.effort` 才开思考。代码:`internal/service/gateway/openai/responses/responses_chat_map.go`、
  `internal/core/adapter/openai/deepseek/chatcompletions/drop.go`(`adaptThinkingDisabled`)。

> 思考模式下 `temperature`/`top_p`/`presence_penalty`/`frequency_penalty` 不报错但不生效(官方·思考模式)。

## 2. reasoning_content 跨轮回传(重要)

官方规则([思考模式·工具调用](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode),查阅 2026-06-07):

- 两个 user 之间**没有**工具调用:中间 assistant 的 `reasoning_content` 无需回传(回传也被忽略)。
- 两个 user 之间**有**工具调用:该轮 assistant 的 `reasoning_content` **必须**在后续所有请求中完整回传;
  **不回传会返回 400**。

Unio 现状:
- **Chat Completions**:入口 DTO 保留 `messages[].reasoning_content`,adapter 原样透传上游(Pass)。
  即只要客户端回传,链路支持。代码:`internal/app/gatewayapi/openai/chatcompletions/dto.go`、
  `internal/core/adapter/openai/chatcompletions/request_wire.go`。
- **Responses**(U1 已落地跨轮回灌):
  - **入站**:紧邻 `function_call` 之前的 reasoning item 翻回该轮 `assistant.reasoning_content`
    (仅工具调用轮需要,非工具轮丢弃);还原优先级 `encrypted_content`(Unio 载体)→
    `content.reasoning_text` → `summary.summary_text`。
  - **出站**:reasoning item 始终带 `content:[{reasoning_text}]`;客户请求
    `include:["reasoning.encrypted_content"]` 或无状态(`store:false`)时,额外附带可逆
    `encrypted_content` 回放载体(`unio-rsn-v1:` + base64,非加密,原文已在 content 暴露)。
    流式与非流式两路形态一致(流式以 `output_item.done` 为权威)。
  - 代码:`responses_chat_map.go`(`extractReasoningText`/`encodeReasoningCarrier`)、
    `responses_response_map.go`、`responses_stream.go`。
  - **残留**:真实 Codex stateless 是否原样回传 reasoning item 待真实 Codex 黑盒确认;
    `reasoning.summary` 与 OpenAI 原生语义差异未对齐(GAP-11-003)。

## 3. 工具调用(Function Calling)

- 标准 OpenAI function tool,DeepSeek v4 思考模式也支持工具调用。
- legacy `functions`/`function_call` 转新式 `tools`/`tool_choice`(见 protocol-and-params 转换表)。
- **`strict` 模式(U7 已收口)**:`function.strict` 字段已**端到端透传**(ingress typed DTO → contract →
  `request_wire.go` 原样 marshal 上游,无任何 Drop)。但 DeepSeek strict 是 **Beta 能力,需 `base_url=.../beta`**;
  是否真正生效取决于该 channel 的 `base_url` 是否指向 beta。Unio **不全局切 beta**(Beta 稳定性/特性差异),
  需严格结构化输出的客户由运营配置专用 beta channel(channel 业务数据,归 phase 13)。见 upgrade-plan U7。

来源:[官方·Function Calling](https://api-docs.deepseek.com/zh-cn/guides/function_calling)(查阅 2026-06-07)。

## 4. 模型名解耦

- 客户请求 Unio catalog model;routing 选 channel-model 并给出显式 `upstream_model`。
- 不依赖 DeepSeek「未知模型名静默降级到 v4-flash」的行为。
- 响应里把 `model` 恢复为客户的 Unio catalog model;审计记录真实 upstream model。

代码:`internal/core/adapter/openai/chatcompletions` response map。

## 5. usage 映射(上游 → 内部计费事实)

| DeepSeek usage 字段 | 内部 `usage.Facts` |
| --- | --- |
| `prompt_cache_hit_tokens` | `CacheReadInputTokens`(缓存命中输入) |
| `prompt_cache_miss_tokens` | `UncachedInputTokens`(未命中输入) |
| `completion_tokens` | `OutputTokensTotal`(含 reasoning) |
| `completion_tokens_details.reasoning_tokens` | `ReasoningOutputTokens` |

校验:`prompt_tokens = hit + miss`;`total_tokens = prompt + completion`。
来源:[官方·上下文硬盘缓存](https://api-docs.deepseek.com/zh-cn/guides/kv_cache)(查阅 2026-06-07);
代码 `internal/core/adapter/openai/chatcompletions`。计费金额口径见 [../billing.md](../billing.md)。

## 6. 错误映射

DeepSeek 错误码:400 格式错误 / 401 认证失败 / 402 余额不足 / 422 参数错误 / 429 速率上限 / 500 服务器故障 / 503 繁忙。
来源:[官方·错误码](https://api-docs.deepseek.com/zh-cn/quick_start/error_codes)(查阅 2026-06-07)。

adapter 按 OpenAI error 信封解析、以 HTTP status 为主映射 category,再由 gatewayapi 渲染成 OpenAI 原生 error。
上游 auth/permission 绝不渲染成客户 401(对客户屏蔽上游凭据问题)。代码:`internal/core/adapter/openai/deepseek/chatcompletions`。

## 7. 流式

DeepSeek OpenAI endpoint 是 OpenAI 风格 SSE(`data: {chunk}` + `data: [DONE]`),`reasoning_content` 走
`delta.reasoning_content`,与 OpenAI 基线一致,无 DeepSeek 专属 framing。adapter 截留上游终态,
由 lifecycle 结算后再由 gatewayapi 写出客户可见终态。
