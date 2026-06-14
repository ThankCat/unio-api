# OpenAI 官方 · 适配与转换逻辑

> 与同目录 [protocol-and-params.md](protocol-and-params.md) 配对。本文件讲**三条链路的实现契约**:
> 官方 adapter 是协议族 base 的**忠实直用**,不叠加任何专属改写。

## 1. 总览:官方 adapter = base 直接复用

```text
ingress(OpenAI) → contract(ChatRequest) → base adapter(internal/core/adapter/openai/chatcompletions) → 官方 upstream
```

第三方上游(DeepSeek)的接法是「base + provider drop 层」:

```text
deepseek.Adapter{ base: openai.Adapter }  // 调上游前先 dropUnsupported
```

官方上游**不需要 drop 层**——它就是 base 本身。路线 C 完成后:

- `(protocol=openai, adapter_key=openai)` 直接注册 **base `openai.Adapter`**(不包任何 wrapper)。
- base 是忠实官方基线:零 Drop、零有损改写(见 [protocol-and-params.md §5](protocol-and-params.md) 去方言化)。

## 2. 请求链路(ingress → upstream)

- **编码**:`buildChatCompletionRequestBody`(`internal/core/adapter/openai/chatcompletions/request_wire.go`)把 typed 字段编码为
  wire,再把未建模的合法字段经 `Extensions` merge(已存在键不覆盖)。这套「typed + Extensions」机制本身是忠实的。
- **唯一注入**:流式时强制 `stream_options.include_usage=true`(L45–48),以拿计费 usage。这是**计费必需**,
  对官方语义无损(只是确保拿到 usage)。
- **路线 C 要改的两处**(当前破坏忠实性,见 protocol-and-params §5):
  - `resolveWireMaxTokens`:停止把 `max_completion_tokens` 塌缩为 `max_tokens`;base 忠实输出 `max_completion_tokens`。
  - `mapWireMessageRole`:停止把 `developer` 塌缩为 `system`;base 忠实透传 `developer`。
  - 这两条 DeepSeek 仍需要 → 下沉到 `internal/core/adapter/openai/deepseek/chatcompletions`(在其 `Messages`/`StreamMessages`
    调 base 前,或在 deepseek 专属 request map 里完成)。

## 3. 响应链路(upstream → 客户)

- 非流式:`Messages`/Chat 解析官方响应 DTO,**忠实回传**;`model` Adapt 为 catalog model(upstream model 记 facts)。
- 不裁剪官方返回的任何 choice / message / logprobs 字段。

## 4. 流式链路

- 官方 OpenAI SSE:`data: {chunk}` … `data: [DONE]`。
- adapter 截留 `[DONE]` 与终态 usage,持久化 immutable facts 并完成 settlement / durable recovery 接管后,
  再由 lifecycle 写出客户终包(与现有 base 流式契约一致)。

## 5. 思考(reasoning)处理

- 官方推理参数(`reasoning_effort` 等)**原样透传**,不做枚举归一(归一 high/max 是 DeepSeek 规则,属 deepseek 层)。
- 跨轮 reasoning 回传:官方 chat completions 的 reasoning 语义按官方文档透传;不套用 DeepSeek 的
  `reasoning_content` 回灌规则(那是 DeepSeek 专属,见 [../deepseek/openai/adaptation.md](../deepseek/openai/adaptation.md))。
- ⚠️ 待查证:官方对 `reasoning_content` / reasoning token 在 chat completions(非 Responses API)下的回传形状,
  接入时按官方文档冻结。

## 6. 工具调用处理

- `tools[]` 全量透传:`function`、`custom`、内置 server tool 均不剔除(官方支持)。
- `tool_choice`、`parallel_tool_calls` 原样透传。

## 7. usage → 计费事实

见 [protocol-and-params.md §7](protocol-and-params.md)。官方 usage 字段直接映射为内部计费 facts;
流式因强制 `include_usage` 而在尾包拿到终态 usage。

## 8. 模型名处理

- 出站:`model` = routing 选中的 upstream model。
- 入站响应:`model` Adapt 为客户 catalog model,upstream model 记入 ResponseFacts 审计。
- 不依赖上游对未知模型名的隐式降级。

## 9. 错误映射

- base adapter 负责 upstream HTTP 错误分类;官方无"因 Drop 而拒绝"的情况(本 provider 不 Drop)。
- gatewayapi/openai 负责渲染对外 OpenAI error shape。

## 10. 内部输入 tokenizer

- 注册 `(openai, openai)` 需要 `openai.ChatInputTokenizer`(authorization 预扣费用,非 settlement 事实)。
- 协议族包内已有 `internal/core/adapter/openai/chatcompletions/tokenizer.go`(`fallbackEncoding` 等)。
- ⚠️ 接入待办:确认 base `openai.Adapter` 是否直接实现 `ChatInputTokenizer`;若未实现,官方 adapter 需
  显式装配一个基于 **Drop 后 wire** 的估算器(官方无 Drop,即按忠实 wire 估算)。详见 [upgrade-plan.md](upgrade-plan.md)。

## 11. 与 DeepSeek 接法的对照(便于评审)

| 维度 | 官方 OpenAI(本 provider) | DeepSeek(openai 协议族) |
| --- | --- | --- |
| 是否包 drop 层 | 否(base 直用) | 是(`dropUnsupported`) |
| `max_completion_tokens` | Pass(同名) | Adapt → `max_tokens` |
| `developer` role | Pass | Adapt → `system` |
| `reasoning_effort` | Pass(原样) | Adapt(归一 high/max) |
| `user` | Pass(标准 `user`) | Adapt → `user_id` |
| 多模态 / custom tool / n>1 / json_schema | Pass | 多为 Drop |
