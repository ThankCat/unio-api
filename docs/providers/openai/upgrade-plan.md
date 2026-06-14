# OpenAI 官方 · 新增(创建)计划

> **官方文档最新日期**:评审确认(2026-06-12)官方文档无新变更,以本查阅日期为基线;后续以 <https://platform.openai.com/docs/api-reference/chat> changelog 为准。
> **官方文档地址**:<https://platform.openai.com/docs/api-reference/chat>(查阅 2026-06-12)
> **本计划查阅日期**:2026-06-12
> **代码接入状态(2026-06-12)**:路线 C 去方言化(N1)、官方 adapter 注册(N2)、tokenizer 确认(N3)**已全部完成**;
> 黑盒实测已经 OpenRouter OpenAI 协议端点跑通(详见 N6 与 [protocol-and-params.md §5](protocol-and-params.md)),
> 官方直连端点 + 官方在售模型的最终冻结因 OpenRouter 账户级 403(中国区 ToS 限制)受阻,待可用官方 key/渠道补测。

OpenAI 官方一方上游接入的代码部分已交付;本文件保留接入待办(运营项 + 黑盒残留)与已完成记录。

## 接入待办

### N5 · catalog 与价格(运营,P1)
- **待办**:运营按官方在售模型登记 catalog + `channel_models.upstream_model`;按官方 pricing 配 `channel_cost_prices`(见 [billing.md](billing.md))。
- **状态**:运营数据,不入代码。

### N6 · 黑盒冻结(P1,部分完成 2026-06-12)
- **已冻结(经 OpenRouter OpenAI 协议端点,`https://openrouter.ai/api/v1`,用例
  `internal/core/adapter/openai/blackbox_test.go`,env 门控 `OPENAI_BLACKBOX=1`)**:
  - `max_completion_tokens` 忠实同名透传:200 接受。
  - `max_tokens` + `max_completion_tokens` **双字段同传**(忠实不塌缩):200 接受。
  - `developer` role 忠实透传:200 接受。
  - `reasoning_effort` 原生枚举(`low`)原样透传:200 接受。
  - 流式 `stream_options.include_usage` 尾包 usage 形状:正常,含 `prompt_tokens_details.cached_tokens`
    / `completion_tokens_details.reasoning_tokens` 维度,被 base usage 解析与 facts 正确消费。
- **残留 ⚠️ 待查证(官方直连)**:OpenAI 官方端点(`api.openai.com`)与官方在售模型(gpt-5.5 等)实测被
  OpenRouter 账户级 403(provider ToS,中国区限制)拦截(裸 curl / Azure 承载 / 本地代理均 403,
  `provider_name:null`,请求未出 OpenRouter 边缘,非 wire 问题)。待可用官方 key 或不受限渠道后重跑同组用例,
  冻结官方对 `developer` 指令优先级、`reasoning_effort` 完整枚举、双 max 字段语义裁决的官方事实。
- **状态**:协议接受性已冻结;官方直连语义待补测。

## 已完成

### N1 · 路线 C:base 去方言化(完成 2026-06-12,P0)
- **交付**:
  1. base wire DTO(`dto.go`)新增 `max_completion_tokens` 字段,`buildChatCompletionRequestBody`
     两字段各自独立忠实输出;删除 `resolveWireMaxTokens` 塌缩。
  2. base 忠实透传 `developer` role;删除 `mapWireMessageRole`(`request_wire.go` / `message_wire.go`)。
  3. 两条塌缩下沉到 `internal/core/adapter/openai/deepseek/chatcompletions`:`adaptMaxCompletionTokens` +
     `adaptDeveloperRole`(`adapt.go`),在 `dropUnsupported` 入口执行(Chat / Stream / tokenizer 三路共用),
     与 [../deepseek/openai/protocol-and-params.md](../deepseek/openai/protocol-and-params.md) §2/§3 行为完全一致。
  4. 双向回归测试:base `request_wire_test.go`(忠实输出断言)+ deepseek `drop_test.go`
    (塌缩零回归断言,含调用方无副作用);`go test ./internal/... ./cmd/...` 全绿。

### N2 · 注册官方 adapter(完成 2026-06-12,P0)
- **交付**:`internal/bootstrap/adapters.go` 注册 `(protocol=openai, adapter_key="openai")`,直接复用
  去方言化后的 base `openai.Adapter`(`Chat` / `StreamChat` / `ChatInputTokenizer` 三能力),无 wrapper。
  preflight 按 `(protocol, adapter_key)` 复合键自动放行;官方零 Drop,无需 reduce 型 capability 画像。

### N3 · 官方 input tokenizer(完成 2026-06-12,P1)
- **结论**:base `openai.Adapter` **已直接实现** `ChatInputTokenizer`(`tokenizer.go`,按完整忠实 wire
  以 tiktoken 保守估算,官方无 Drop 故无需清理步骤),已加接口断言(`chat.go`),无需另行实现。
  返回值仅用于 authorization 预冻结,非 settlement。

### N4 · 认证与可选头透传(评审确认 2026-06-12,结论:暂不需要)
- **决策**:`OpenAI-Organization` / `OpenAI-Project` 头**暂不需要**透传。base `do()`
 (`internal/core/adapter/openai/chatcompletions/chat.go`)只设 `Authorization: Bearer` 即可。
- **理由**:多数场景由 channel 配置(独立 key / project 维度的 base_url + key)承载;无需在 adapter 层透传客户头。
- **未来解锁路径(可选)**:若日后需按客户请求区分 org/project,再按登记表透传,届时重开本项。
