# Anthropic 官方 · 适配与转换逻辑

> 与同目录 [protocol-and-params.md](protocol-and-params.md) 配对。本文件讲**三条链路的实现契约**:
> 官方 adapter 是协议族 base 的**忠实直用**,只补两个 1P 缺口(beta 透传、tokenizer),不叠加任何专属改写。

## 1. 总览:官方 adapter = base 直接复用

```text
ingress(Anthropic) → contract(MessageRequest) → base adapter(internal/core/adapter/anthropic/messages) → 官方 upstream
```

DeepSeek 的接法是「base + drop 层」(`anthropic/deepseek/adapter.go`:`Messages`/`StreamMessages` 调 base 前先
`dropUnsupported`)。官方上游**不需要 drop 层**——base 本身已是忠实官方基线。

路线 C 完成后:`(protocol=anthropic, adapter_key=anthropic)` 直接注册 **base `anthropic.Adapter`**(去方言化无需做,
base 已干净),只需补 beta 头透传与 tokenizer(见 §10、§11)。

## 2. 请求链路(ingress → upstream)

- **编码**:`buildMessagesRequestBody`(`internal/core/adapter/anthropic/messages/wire.go` L87–114)把 typed 字段编码为 wire,
  复杂 union(`system`/`content`/`thinking`/`tools`/`tool_choice`/`metadata`)以 `json.RawMessage` 原样透传,
  再把 `Extensions` 经 `mergeJSONObjects` 合并(已存在键不覆盖)。**忠实,无改写。**
- **HTTP**:`do()`(`adapter.go` L209–247)拼 `<base>/v1/messages`,设 `Content-Type` + `x-api-key` +
  `anthropic-version: 2023-06-01`;流式加 `Accept: text/event-stream`。
- **1P 要补(G1)**:按支持登记表透传 `anthropic-beta` 头(当前不转发,见 protocol-and-params §4)。

## 3. 响应链路(upstream → 客户)

- 非流式 `Messages`(`adapter.go` L66–99):解析官方 `messagesResponse`,校验 `id` 与 `content` 非空,
  **忠实回传** content blocks / stop_reason / usage;`model` Adapt 为 catalog model(upstream model 记 facts)。
- 不裁剪官方返回的任何 content block。

## 4. 流式链路

- `StreamMessages`(`adapter.go` L107–207)用 SSE reader 逐事件解析:
  - `message_start`:取 `id` / `model` / 初始 usage(`consume` L261–287)。
  - `message_delta`:取终态 `stop_reason` 与终态 usage;`input_tokens` + `output_tokens` 齐备即标记
    `reliableUsageSeen` 并作为流式结算事实(L288–306)。
  - `message_stop`:截留为成功终态(L162–165),不直接 emit;lifecycle 持久化 facts + 完成 settlement /
    durable recovery 接管后再写出客户终包。
- Anthropic 流式 usage 分散在 `message_start`(输入计量)与 `message_delta`(输出计量),`mergeUsageWire`
 (`wire.go` L203–252)合并为完整最终 facts。**官方与 DeepSeek 两种形状都生成同一份完整 facts**(base 已兼容)。

## 5. 思考(thinking)处理

- `thinking` 字段(`enabled`/`disabled` + `budget_tokens`)以 `json.RawMessage` **原样透传**,不归一。
- 归一 `output_config.effort` 为 high/max、剔除 `format` 是 **DeepSeek 专属规则**(`anthropic/deepseek/drop.go`
  `adaptOutputConfig`),**不属于**官方 adapter。
- 响应/流式的 `thinking` content block 与 `output_tokens_details.thinking_tokens` 忠实透传/计费。

## 6. 工具调用处理

- `tools[]`、`tool_choice` 以 `json.RawMessage` **原样透传**:client custom tool 与内置 server tool
 (web_search/web_fetch 等)**都不剔除**(剔除 server tool 是 DeepSeek 规则,`dropServerTools`)。
- `server_tool_use` / `web_search_tool_result` content block 忠实透传;`usage.server_tool_use` 计入计费维度。

## 7. usage → 计费事实

见 [protocol-and-params.md §6](protocol-and-params.md)。`messageUsageFromWire`(`wire.go` L163–189)已映射
input/output、cache_creation(5m/1h)、cache_read、thinking、server_tool_use 各维度。

## 8. 模型名处理

- 出站 `model` = routing 选中的 upstream model;入站响应 `model` Adapt 为客户 catalog model,upstream model 记 facts。
- 不依赖上游对未知模型名隐式降级。

## 9. 错误映射

- base `newUpstreamStatusError` / `newUpstreamSendError` 负责 upstream 错误分类;官方无"因 Drop 而拒绝"(本 provider 不 Drop)。
- gatewayapi/anthropic 负责渲染对外 Anthropic error shape。

## 10. `anthropic-beta` 头透传(G1,1P 必补)

- **现状**:base `do()` 只设 `x-api-key` + `anthropic-version`;beta 头按 DEC-012 在 gatewayapi 层 Drop,不到上游。
- **方案**:官方 adapter 维护**支持 beta 白名单登记表**,把客户请求里命中白名单的 `anthropic-beta` 值透传到 upstream
  `anthropic-beta` 头;未登记的不透传(避免对客户做假承诺)。
- ⚠️ 待查证:接入时确定要支持的 beta 集合(参考 [官方·Beta headers](https://docs.anthropic.com/en/api/beta-headers),查阅 2026-06-12)。

## 11. 内部输入 tokenizer(G2,1P 必补)

- 注册 `(anthropic, anthropic)` 需要 `MessagesInputTokenizer`(authorization 预扣费,非 settlement 事实)。
- base **未实现**该接口(只有 `MessagesAdapter`/`StreamMessagesAdapter`)。
- **方案二选一**:
  1. 保守字符启发式估算(参考 DeepSeek anthropic tokenizer 的实现思路,但官方 adapter 独立持有,不复用 DeepSeek)。
  2. 调官方 [Count Message tokens](https://docs.anthropic.com/en/api/messages-count-tokens) 端点做精确预估(有额外往返与限频成本)。
- 返回值用于 authorization 保守冻结。

## 12. 与 DeepSeek 接法的对照(便于评审)

| 维度 | 官方 Anthropic(本 provider) | DeepSeek(anthropic 协议族) |
| --- | --- | --- |
| 是否包 drop 层 | 否(base 直用) | 是(`dropUnsupported`) |
| `top_k` | Pass | Drop |
| content block(image/document/MCP 等) | Pass(全量) | Drop(仅留 6 类) |
| 内置 server tool | Pass | Drop(仅留 custom) |
| `metadata` | Pass(全量) | Drop(仅留 `user_id`) |
| `output_config.effort` / `format` | Pass(原样) | Adapt(归一 high/max)/ Drop(format) |
| `anthropic-beta` 透传 | **要补(G1)** | 不适用(DeepSeek 不需要) |
| input tokenizer | **要补(G2)** | 已自有 |
