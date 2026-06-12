# OpenAI 官方上游适配

OpenAI 是 Unio 接入的**官方一方(1P)上游**,只提供 **OpenAI 协议**本身(`/v1/chat/completions`)。
因此本 provider 是单协议族,`protocol-and-params.md` 与 `adaptation.md` 直接平铺在目录根。

> **核心立场:官方上游 = 协议基线。** 官方 OpenAI 端点能接收并正确处理 OpenAI 协议的**全部合法字段**,
> 所以本 adapter 的目标是**零 Drop、零有损改写、忠实透传**。任何"为某个第三方上游做的归一/塌缩"都
> **不允许**出现在本 provider 与其依赖的协议族 base adapter(`internal/core/adapter/openai`)里——
> 那些只能存在于具体第三方 provider 的 adapter 层(如 `internal/core/adapter/openai/deepseek`)。
> 这正是本次接入(路线 C)要修正的现状:base 当前烤进了两处 DeepSeek 方言,见
> [protocol-and-params.md §5](protocol-and-params.md) 与 [upgrade-plan.md](upgrade-plan.md)。

官方文档入口:<https://platform.openai.com/docs/api-reference/chat>(查阅 2026-06-12)。
协议本身的逐字段说明见 [docs/protocol/openai/chat-completions/](../../protocol/openai/chat-completions/params.md),本目录**不重抄**。

## 端点

| 协议格式 | Base URL | 路径 | 项目 adapter | adapter_key |
| --- | --- | --- | --- | --- |
| OpenAI | `https://api.openai.com/v1` | `POST /chat/completions` | `internal/core/adapter/openai`(base 直接复用) | `openai` |

来源:[官方·Chat Completions](https://platform.openai.com/docs/api-reference/chat/create)(查阅 2026-06-12)。

> 实际生产中 channel 的 `base_url` 由运营配置,可指向官方 `https://api.openai.com/v1`,也可指向
> 任意**声称完全兼容 OpenAI 官方**的代理。adapter 只认协议,不认域名;能否对接取决于该端点是否真的
> 兼容官方语义。

## 模型清单

模型清单属**运营数据**(catalog + `channel_models`),由运营按官方在售模型配置,不在代码硬编码,也不在本文档维护权威列表。
官方在售模型见 [官方·Models](https://platform.openai.com/docs/models)(查阅 2026-06-12)。

> Unio 不依赖上游对未知模型名的隐式降级:routing 必须给出已登记的显式 `upstream_model`。

## 关键事实速查

| 维度 | 事实 |
| --- | --- |
| 协议格式 | OpenAI 原生(官方一方) |
| 认证 | `Authorization: Bearer <key>`(`OpenAI-Organization` / `OpenAI-Project` 头暂不需要,评审确认,见 upgrade-plan) |
| Drop 策略 | **原则零 Drop**:官方接受全部合法 OpenAI 字段,忠实透传 |
| Adapt 策略 | 仅两类:`model`(routing 选 upstream model)、流式 `stream_options.include_usage`(adapter 强制注入以拿计费 usage) |
| 思考模式 | 由模型能力决定;`reasoning_effort` 等推理参数**原样透传**,不归一 |
| 工具调用 | 全量透传(function / custom / 内置 server tool),不剔除 |
| `developer` role | **忠实透传**(官方原生支持),不塌缩为 `system`(当前 base 有此方言,待修) |
| `max_completion_tokens` | **忠实透传**为同名字段(官方新模型用它),不塌缩为 `max_tokens`(当前 base 有此方言,待修) |
| usage 计费 | 走官方 `usage`(`prompt_tokens` / `completion_tokens` / `*_tokens_details`) |
| 计费币种 | 美元(USD)/ 百万 token(官方),平台对外售价按 channel 配置 |

详细依据见本目录各文档。

## 文档索引

- [protocol-and-params.md](protocol-and-params.md) — 相对标准协议的差异(几乎为零)+ **路线 C 去方言化下沉清单**(权威)
- [adaptation.md](adaptation.md) — 请求/响应/流式三链路、思考、工具、usage、模型名、错误的忠实透传契约
- [billing.md](billing.md) — token 口径、cache、reasoning 计费维度
- [upgrade-plan.md](upgrade-plan.md) — **新增(创建)计划**:接入待办 + 路线 C 改造清单(含官方文档最新日期)

标准协议本身见 [docs/protocol/](../../protocol/README.md);全局决策见 [DECISIONS.md](../../production/DECISIONS.md)
(尤其 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略))。

## 术语表

| 术语 | 全称 / 解释 |
| --- | --- |
| 1P / 官方一方 | First-Party,指上游服务商就是协议的发明者(OpenAI 提供 OpenAI 协议) |
| base adapter | 协议族通用实现 `internal/core/adapter/openai`:wire 编码、HTTP、响应解析、SSE、usage,**应是忠实的官方基线** |
| 去方言化 | 把第三方上游专属的归一/塌缩从协议族 base 中移除,使 base 回归忠实官方语义 |
| 路线 C | 本次接入策略:重构 base 为忠实 OpenAI 基线,把 DeepSeek 专属改写下沉到 `openai/deepseek` 层 |
| `developer` role | OpenAI 新模型(o 系列等)的系统级指令角色,官方原生区分于 `system` |
| `max_completion_tokens` | 官方新参数,替代已弃用的 `max_tokens` 限制输出(含 reasoning)token 上限 |
| reasoning_effort | 官方推理强度参数(`minimal`/`low`/`medium`/`high` 等),官方原生枚举 |

通用术语(Provider / Adapter / Ingress / Upstream / Pass / Adapt / Drop / SSE / usage 等)见
[../README.md 通用术语表](../README.md#通用术语表)。
