# Anthropic 官方 · 新增(创建)计划

> **官方文档最新日期**:评审确认(2026-06-12)官方文档无新变更,以本查阅日期为基线;后续以 <https://docs.anthropic.com/en/api/messages> changelog 为准。
> **官方文档地址**:<https://docs.anthropic.com/en/api/messages>(查阅 2026-06-12)
> **本计划查阅日期**:2026-06-12
> **官方 key**:用户将于到家后提供**官方真实 key**;届时执行 N6 黑盒冻结与各 `⚠️ 待查证` 实测项
>(`anthropic-beta` 白名单生效、`anthropic-version` 版本策略、content block / server tool / metadata 官方接受性、cache 5m/1h 与 thinking usage 形状)。

Anthropic 官方一方上游**尚未接入**,故本文件是**新增(创建)计划**。

> **与 OpenAI 的关键差异**:Anthropic 协议族 base(`internal/core/adapter/anthropic`)**已是忠实官方基线**
>(无方言需下沉,见 [protocol-and-params.md §3/§4](protocol-and-params.md))。因此本计划**不含"路线 C 去方言化"**,
> 只含三件接入工作:补 beta 透传(G1)、补 tokenizer(G2)、注册官方 adapter。

## 接入待办

### N1 · `anthropic-beta` 头透传(P0,G1)
- **现状**:base `do()`(`internal/core/adapter/anthropic/adapter.go` L230–235)只设 `x-api-key` +
  `anthropic-version`;`anthropic-beta` 头按 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)
  第 4 点在 gatewayapi 层 Drop,不到上游。
- **官方依据**:DEC-012 明确"未来接入真实 Anthropic 1P adapter 时,应改为按登记表把支持的 beta Pass 转发到
  upstream `anthropic-beta`";官方 beta 头见 [官方·Beta headers](https://docs.anthropic.com/en/api/beta-headers)(查阅 2026-06-12)。
- **影响**:不透传则 beta 特性(扩展上下文、特定工具等)对官方失效,客户体验受损。
- **方案**:官方 adapter 维护**支持 beta 白名单登记表**;请求里命中白名单的 beta 值透传到 upstream `anthropic-beta`,
  未登记的不透传(不做假承诺)。需贯通 gatewayapi → channel runtime → adapter 的 beta 传递路径。
- ⚠️ **待查证**:首批要支持的 beta 集合(接入时定)。
- **优先级**:P0。**状态**:待开发(评审通过后)。

### N2 · 官方 input tokenizer(P0,G2)
- **现状**:base `anthropic.Adapter` **未实现** `MessagesInputTokenizer`(`adapter.go` L345–348 只断言
  `MessagesAdapter`/`StreamMessagesAdapter`);DeepSeek 用其自有 tokenizer。
- **待办**:为官方 adapter 提供 tokenizer,二选一:
  1. 保守字符启发式估算(独立实现,不复用 DeepSeek)。
  2. 调官方 [Count Message tokens](https://docs.anthropic.com/en/api/messages-count-tokens) 端点精确预估(有往返/限频成本)。
- **影响**:注册 `(anthropic, anthropic)` 强依赖此接口(authorization 预扣费)。
- **优先级**:P0。**状态**:待开发。

### N3 · 注册官方 adapter(P0)
- **方案**:`internal/bootstrap/adapters.go` 增加 `(protocol=anthropic, adapter_key="anthropic")` 注册。
  - `Messages` / `StreamMessages` 直接用 base `anthropic.Adapter`(可能需薄封装以挂 beta 透传与 tokenizer)。
  - `MessagesInputTokenizer` 用 N2 的实现。
- **依赖**:N1、N2。
- **状态**:待开发。

### N4 · `anthropic-version` 策略(P2,G3)
- **现状**:base 硬编码 `2023-06-01`(`adapter.go` L20–22)。
- **待办**:确认官方当前推荐基线版本;如需更高版本支持新特性,评估是否做成可配置(channel 级或全局)。
- ⚠️ **待查证**:官方版本头当前推荐值。**状态**:待查证。

### N5 · catalog 与价格(运营,P1)
- **待办**:运营按官方在售模型登记 catalog + `channel_models.upstream_model`;按官方 pricing 配 `channel_cost_prices`
 (含 cache 5m/1h 写入溢价、cache read 折扣、内置工具单价,见 [billing.md](billing.md))。
- **状态**:运营数据,不入代码。

### N6 · 黑盒冻结(P1,接入后)
- 拿到官方 key 后,用最小请求实测并冻结(写回 protocol-and-params 黑盒记录):
  - 基础非流式 / 流式 messages(`message_start`→`message_delta`→`message_stop` 流程、终态 usage 形状)。
  - `top_k`、全量 content block、内置 server tool、完整 `metadata` 的官方接受性(确认 base Pass 无误)。
  - `thinking` 各形态、`output_tokens_details.thinking_tokens` 计费维度。
  - cache_creation(5m/1h)/ cache_read usage 实际形状。
  - 命中白名单的 `anthropic-beta` 头是否生效(N1 验证)。
- **状态**:待接入后执行。

## 已完成

(暂无。本计划评审通过后开始。但注意:base 去方言化对 Anthropic **本就不需要**——架构已正确隔离 DeepSeek 偏差。)
