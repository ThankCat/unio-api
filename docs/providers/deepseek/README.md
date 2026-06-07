# DeepSeek 上游适配

DeepSeek 是 Unio 当前的主力上游服务商。它同时提供 **OpenAI 兼容**与 **Anthropic 兼容**两套 API 格式,
Unio 据此用两个独立 adapter 接入。

官方文档入口:<https://api-docs.deepseek.com/zh-cn/>(查阅 2026-06-07,官方最新模型更新 2026-04-24 DeepSeek-V4)。

## 端点

| 协议格式 | Base URL | 路径 | 项目 adapter |
| --- | --- | --- | --- |
| OpenAI | `https://api.deepseek.com` | `POST /chat/completions` | `internal/core/adapter/openai/deepseek` |
| Anthropic | `https://api.deepseek.com/anthropic` | `POST /v1/messages` | `internal/core/adapter/anthropic/deepseek` |

来源:[官方·首次调用](https://api-docs.deepseek.com/zh-cn/)、[官方·模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)(查阅 2026-06-07)。

## 模型清单

| 模型 | 说明 | 思考模式 | 状态 |
| --- | --- | --- | --- |
| `deepseek-v4-pro` | DeepSeek-V4-Pro,能力更强 | 支持非思考/思考切换(默认开) | 在售 |
| `deepseek-v4-flash` | DeepSeek-V4-Flash,更快更便宜 | 支持非思考/思考切换(默认开) | 在售 |
| `deepseek-chat` | 旧模型名,指向 v4-flash 非思考模式 | — | **2026-07-24 弃用** |
| `deepseek-reasoner` | 旧模型名,指向 v4-flash 思考模式 | — | **2026-07-24 弃用** |

来源:[官方·更新日志 2026-04-24](https://api-docs.deepseek.com/zh-cn/updates)、[官方·模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)(查阅 2026-06-07)。

> Unio 不依赖上游对未知模型名的隐式降级:routing 必须给出已登记的显式 `upstream_model`。

## 关键事实速查

| 维度 | 事实 |
| --- | --- |
| 协议格式 | OpenAI 兼容 + Anthropic 兼容 |
| 认证 | OpenAI 格式 `Authorization: Bearer`;Anthropic 格式 `x-api-key` |
| 上下文长度 | 1M token(v4) |
| 最大输出 | 384K token(v4) |
| 思考模式 | 默认**开启**;用 `thinking` 字段开关,`reasoning_effort` 控强度(仅 high/max) |
| 思考下失效参数 | `temperature`/`top_p`/`presence_penalty`/`frequency_penalty`(不报错但不生效) |
| 工具调用 | 支持(function);思考模式也支持,且工具轮次必须回传 `reasoning_content` |
| 上下文缓存 | 默认开启,`usage` 返回命中/未命中 token 数 |
| 计费币种 | 人民币(元)/ 百万 token |

详细依据见本目录各文档。

## 文档索引

协议适配按协议族分目录(权威逐字段映射 + 适配逻辑):

- **OpenAI 格式**(`/chat/completions`):
  - [openai/protocol-and-params.md](openai/protocol-and-params.md) — 支持/废弃/转换参数(权威映射)
  - [openai/adaptation.md](openai/adaptation.md) — 思考、reasoning 跨轮、工具、usage、模型名、错误、流式
- **Anthropic 格式**(`/anthropic/v1/messages`):
  - [anthropic/protocol-and-params.md](anthropic/protocol-and-params.md) — 支持/废弃/转换参数(权威映射)
  - [anthropic/adaptation.md](anthropic/adaptation.md) — 思考、工具、usage、模型名、错误、流式

跨协议共用:

- [billing.md](billing.md) — 价格、token 口径、缓存、reasoning 计费
- [upgrade-plan.md](upgrade-plan.md) — 升级/新增计划(含官方文档最新日期)
- [anthropic-api-reference.md](anthropic-api-reference.md) — DeepSeek 官方 Anthropic 兼容性参考摘要

标准协议本身见 [docs/protocol/](../../protocol/README.md);全局决策见 [DECISIONS.md](../../production/DECISIONS.md)。

## 术语表

| 术语 | 全称 / 解释 |
| --- | --- |
| 思考模式 / thinking | DeepSeek 的深度思考:开启后先产出思维链(reasoning)再给答案 |
| reasoning_content | 思考模式下返回的思维链文本,与最终答案 `content` 同级 |
| reasoning_effort | 思考强度参数;DeepSeek 仅认 `high`/`max` |
| CoT | Chain-of-Thought,思维链,即思考过程文本 |
| 硬盘缓存 / context cache | DeepSeek 对重复前缀的输入做缓存,命中部分大幅降价 |
| prompt_cache_hit/miss_tokens | usage 中的缓存命中/未命中输入 token 数 |
| FIM | Fill-In-the-Middle,中间填充补全(代码补全场景) |
| strict 模式 | Function Calling 严格遵循 JSON Schema 的 Beta 能力 |
