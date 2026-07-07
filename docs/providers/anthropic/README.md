# Anthropic 官方上游适配

Anthropic 是 Unio 接入的**官方一方(1P)上游**,只提供 **Anthropic 协议**本身(Messages API,`/v1/messages`)。
因此本 provider 是单协议族,`protocol-and-params.md` 与 `adaptation.md` 直接平铺在目录根。

> **核心立场:官方上游 = 协议基线。** 官方 Anthropic 端点能接收并正确处理 Messages 协议的**全部合法字段**,
> 所以本 adapter 的目标是**零 Drop、零有损改写、忠实透传**。
>
> **好消息:Anthropic 协议族 base(`internal/core/adapter/anthropic/messages`)当前已基本是忠实官方基线**——
> 它用 `json.RawMessage` 原样透传 `system`/`content`/`thinking`/`tools`/`tool_choice`/`metadata`,只 typed 了
> `max_tokens`/`stop_sequences`/`temperature`/`top_p`/`top_k`,**无任何改名/塌缩**;DeepSeek 的全部偏差
>(`top_k` 丢弃、content block 剔除、server tool 剔除、metadata 仅留 `user_id`、`output_config` 归一等)
> 都已正确隔离在 `internal/core/adapter/anthropic/deepseek/messages` 层。
>
> 因此本次接入的"路线 C"对 Anthropic **几乎是空操作**(无需从 base 下沉方言)。真正要补的是两个**官方 1P 缺口**:
> **`anthropic-beta` 头透传**(见下)与**官方 input tokenizer**。

官方文档入口:<https://docs.anthropic.com/en/api/messages>(查阅 2026-06-12)。
协议本身的逐字段说明见 [docs/protocol/anthropic/messages/](../../protocol/anthropic/messages/params.md),本目录**不重抄**。

## 端点

| 协议格式 | Base URL | 路径 | 项目 adapter | adapter_key |
| --- | --- | --- | --- | --- |
| Anthropic | `https://api.anthropic.com` | `POST /v1/messages` | `internal/core/adapter/anthropic/messages`(base 直接复用) | `anthropic` |

来源:[官方·Messages](https://docs.anthropic.com/en/api/messages)(查阅 2026-06-12)。

## 模型清单

模型清单属**运营数据**(catalog + `channel_models`),由运营按官方在售模型配置,不在代码硬编码。
官方在售模型见 [官方·Models](https://docs.anthropic.com/en/docs/about-claude/models)(查阅 2026-06-12)。

> Unio 不依赖上游对未知模型名的隐式降级:routing 必须给出已登记的显式 `upstream_model`。

## 关键事实速查

| 维度 | 事实 |
| --- | --- |
| 协议格式 | Anthropic Messages(官方一方) |
| 认证 | `x-api-key: <key>` + `anthropic-version: 2023-06-01`(base 已设) |
| `anthropic-beta` 头 | **透传 + 小黑名单**(`beta.go`:默认转发,仅拦截有计费/解析缺口的 `code-execution` / `context-1m`)。1h 缓存(`extended-cache-ttl`)已透传——见 [passthrough-audit.md](passthrough-audit.md) |
| Drop 策略 | **原则零 Drop**:官方接受全部合法 Messages 字段,忠实透传(base 已如此) |
| Adapt 策略 | 仅 `model`(routing 选 upstream model) |
| 思考模式 | `thinking` 字段**原样透传**(`json.RawMessage`),不归一(归一 effort 是 DeepSeek 规则) |
| 工具调用 | 全量透传(client custom + 内置 server tool),**不剔除**(剔除 server tool 是 DeepSeek 规则) |
| `top_k` | **忠实透传**(丢弃 `top_k` 是 DeepSeek 规则,属 deepseek 层) |
| content block | 全量透传(image/document/redacted_thinking/MCP 等不剔除) |
| usage 计费 | 官方 `usage`(input/output、cache_creation/read、output_tokens_details.thinking、server_tool_use) |
| 计费币种 | 美元(USD)/ 百万 token(官方),平台对外售价按 channel 配置 |

详细依据见本目录各文档。

## 文档索引

- [protocol-and-params.md](protocol-and-params.md) — 相对标准协议的差异(几乎为零)+ **官方 1P 缺口清单**(权威)
- [adaptation.md](adaptation.md) — 请求/响应/流式三链路、思考、工具、usage、模型名、错误的忠实透传契约
- [billing.md](billing.md) — token 口径、cache(5m/1h)、thinking 计费维度
- [upgrade-plan.md](upgrade-plan.md) — **新增(创建)计划**:接入待办(beta 透传 + tokenizer + 注册)

标准协议本身见 [docs/protocol/](../../protocol/README.md);全局决策见 [DECISIONS.md](../../production/DECISIONS.md)
(尤其 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略))。

## 术语表

| 术语 | 全称 / 解释 |
| --- | --- |
| 1P / 官方一方 | First-Party,上游服务商就是协议发明者(Anthropic 提供 Anthropic 协议) |
| base adapter | 协议族通用实现 `internal/core/adapter/anthropic/messages`:wire 编码、HTTP、响应解析、SSE、usage,**当前已是忠实官方基线** |
| `anthropic-version` | Anthropic 必需的 API 版本头;base 当前固定 `2023-06-01` |
| `anthropic-beta` | 启用 beta 特性的头(如扩展上下文、特定工具);官方 1P 需按登记表透传 |
| thinking | Anthropic 扩展思考块;请求里 `thinking` 字段控制,响应/流里以 `thinking` content block 回传 |
| content block | Messages 的多态内容单元(text/image/tool_use/tool_result/thinking/document 等) |
| server tool | Anthropic 内置工具(如 web_search/web_fetch),区别于客户自定义 custom tool |
| cache_creation / cache_read | Anthropic prompt caching 的写入(5m/1h TTL)与命中读取 token 维度 |

通用术语(Provider / Adapter / Ingress / Upstream / Pass / Adapt / Drop / SSE / usage 等)见
[../README.md 通用术语表](../README.md#通用术语表)。
