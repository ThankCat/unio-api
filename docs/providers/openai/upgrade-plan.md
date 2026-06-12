# OpenAI 官方 · 新增(创建)计划

> **官方文档最新日期**:评审确认(2026-06-12)官方文档无新变更,以本查阅日期为基线;后续以 <https://platform.openai.com/docs/api-reference/chat> changelog 为准。
> **官方文档地址**:<https://platform.openai.com/docs/api-reference/chat>(查阅 2026-06-12)
> **本计划查阅日期**:2026-06-12
> **官方 key**:用户将于到家后提供**官方真实 key**;届时执行 N6 黑盒冻结与各 `⚠️ 待查证` 实测项
>(`max_tokens` vs `max_completion_tokens`、`developer` role、`reasoning_effort` 枚举、流式 usage 形状)。
> 在拿到 key 前,路线 C 去方言化(N1)为**纯重构 + 回归测试**,不依赖 key,可先行开发。

OpenAI 官方一方上游**尚未接入**,故本文件是**新增(创建)计划**。下表为接入待办;关闭后移入「已完成」。
本计划同时承载**路线 C(去方言化)改造**——这是接入官方的前置条件。

## 接入待办

### N1 · 路线 C:base 去方言化(前置,P0)
- **现状**:协议族 base `internal/core/adapter/openai` 烤进两处 DeepSeek 方言,**不是**忠实官方基线:
  - L1 `resolveWireMaxTokens` 把 `max_completion_tokens` 塌缩为 wire `max_tokens`(`request_wire.go` L15、L62–68)。
  - L2 `mapWireMessageRole` 把 `developer` 塌缩为 `system`(`request_wire.go` L74、L86–93)。
- **官方依据**:[Chat Completions](https://platform.openai.com/docs/api-reference/chat/create)(查阅 2026-06-12)——
  官方新模型用 `max_completion_tokens`、原生区分 `developer` 与 `system`。
- **影响**:base 直接给官方用会丢失/错配语义;且方言留在 base 违背"协议为先、provider 偏差在 adapter 层"原则(DEC-012)。
- **方案**:
  1. base wire DTO 新增 `max_completion_tokens` 字段,`buildChatCompletionRequestBody` 忠实输出它;移除 `resolveWireMaxTokens` 的塌缩。
  2. base `adapterMessagesToWire` 忠实透传 `developer` role;移除 `mapWireMessageRole`。
  3. 把这两条塌缩规则**下沉**到 `internal/core/adapter/openai/deepseek`(在调 base 前的 request map / drop 阶段完成),
     保证 DeepSeek 行为与现 [../deepseek/openai/protocol-and-params.md](../deepseek/openai/protocol-and-params.md) §2/§3 完全不变。
  4. base 现有单测 + deepseek wire 单测都要更新断言,保证:官方路径忠实、DeepSeek 路径不回归。
- **优先级**:P0(官方接入的前置)。
- **状态**:待开发(本计划评审通过后)。

### N2 · 注册官方 adapter(P0)
- **方案**:`internal/bootstrap/adapters.go` 增加 `(protocol=openai, adapter_key="openai")` 注册,直接复用 base
  `openai.Adapter`(去方言化后),无需 wrapper。需提供 `Chat` / `StreamChat` / `ChatInputTokenizer`。
- **依赖**:N1、N3。
- **状态**:待开发。

### N3 · 官方 input tokenizer(P1)
- **现状**:协议族包内有 `internal/core/adapter/openai/tokenizer.go`;DeepSeek 用的是其**自有**实现。
- **待办**:确认 base `openai.Adapter` 是否直接实现 `ChatInputTokenizer`;若否,为官方装配一个基于忠实 wire 的
  保守估算器(官方无 Drop,按完整 wire 估算)。返回值仅用于 authorization 预冻结,非 settlement。
- **状态**:待确认 + 按需实现。

### N5 · catalog 与价格(运营,P1)
- **待办**:运营按官方在售模型登记 catalog + `channel_models.upstream_model`;按官方 pricing 配 `channel_cost_prices`(见 [billing.md](billing.md))。
- **状态**:运营数据,不入代码。

### N6 · 黑盒冻结(P1,接入后)
- 拿到官方 key 后,用最小请求实测并冻结以下事实(写回 protocol-and-params §5 黑盒记录):
  - `max_completion_tokens` vs `max_tokens` 的实际接受/语义。
  - `developer` role 的实际处理(是否原生区分 `system`)。
  - `reasoning_effort` 官方枚举接受范围。
  - 流式 `stream_options.include_usage` 尾包 usage 形状。
  - `usage` 各 `*_tokens_details` 字段实际形状(cache / reasoning)。
- **状态**:待接入后执行。

## 已完成

### N4 · 认证与可选头透传(评审确认 2026-06-12,结论:暂不需要)
- **决策**:`OpenAI-Organization` / `OpenAI-Project` 头**暂不需要**透传。base `do()`
 (`internal/core/adapter/openai/chat.go`)只设 `Authorization: Bearer` 即可。
- **理由**:多数场景由 channel 配置(独立 key / project 维度的 base_url + key)承载;无需在 adapter 层透传客户头。
- **未来解锁路径(可选)**:若日后需按客户请求区分 org/project,再按登记表透传,届时重开本项。

> 其余接入项(N1~N3、N5、N6)待本计划评审通过后开始;N1 为 P0 前置。
