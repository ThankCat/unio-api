# DeepSeek 升级/新增计划

> **官方文档最新日期**:2026-04-24(DeepSeek-V4 发布)
> **官方文档地址**:<https://api-docs.deepseek.com/zh-cn/>(更新日志:<https://api-docs.deepseek.com/zh-cn/updates>)
> **本计划查阅日期**:2026-06-07

DeepSeek 已是在接上游,故本文件是**升级计划**(非新增)。下表为待升级项;关闭后移入「已完成」。

## 待升级项

### U3 · 成本价配置对齐 V4 真实价(P1)
- **现状**:成本价配置需与 v4 实际价对齐(见 [billing.md](billing.md))。
- **官方依据**:[模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)——
  v4-pro 输入 3 / 命中 0.025 / 输出 6 元;v4-flash 1 / 0.02 / 2 元(每百万 token)。
- **核对发现(2026-06-07)**:旧 seed `channel_cost_prices`(v4-pro,CNY)为占位值
  `未命中 2.0 / 命中 0.2 / 输出 3.0`,与官价 `3.0 / 0.025 / 6.0` 不一致:未命中/输出**低估**、命中**高估**;
  成本低估会让平台利润核算虚高。`seed/main.go` 已改为官价(dev 助手,非生产/未提交)。
- **影响**:配置错则平台利润核算失真。
- **方案**:生产 DB `channel_cost_prices` 由运营按官价更新(v4-pro `3.0/0.025/6.0`;若启用 v4-flash 另配 `1.0/0.02/2.0`,CNY/1M)。
- **状态**:seed 已对齐;**生产 DB 待运营更新**(运营数据,不入代码)。

> 代码侧升级 U1/U2/U4/U5/U6/U7 已全部收口(见下「已完成」);待升级项仅剩 U3 的**生产 DB 运营更新**(不入代码)。

## 已完成

### U6 · web_search server tool 确认(2026-06-07,结论:保持 Drop)
- **现状**:Anthropic 入站 `server_tool_use`/`web_search_tool_result` block 保留;`tools[]` 里客户
  主动声明的 web_search 工具定义出站 Drop(`anthropic/deepseek/drop.go` `dropServerTools`,非 custom 一律剔除)。
- **核对发现**:官方兼容表把 server_tool_use/web_search_tool_result 只列在
  [anthropic-api-reference.md](anthropic-api-reference.md) **§6 content block**(supported = 历史回传接受);
  **§5 `tools[]` 没有可声明的 web_search 工具类型**(只有 name/input_schema/description/cache_control,即纯 custom)。
- **结论**:按「未列入官方兼容表不默认支持」(AGENTS.md)原则,**保持 Drop**——放行该工具定义会形成
  "客户声明 web_search 却拿不到搜索结果"的假承诺,且官方无文档化的主动请求方式。Drop 已计入脱敏审计,非 silent。
- **未来解锁路径(可选)**:若日后真实黑盒证明 DeepSeek 在声明 web_search 工具时确实执行搜索,再放行;
  长期归 phase 12 `model_capabilities` 的 `tools.builtin.web_search`(DeepSeek 暂记 unsupported)。无需新 GAP。

### U7 · Function Calling strict 模式评估(2026-06-07,结论:已透传,不全局切 beta)
- **现状**:`function.strict` 字段**已端到端透传**——ingress typed DTO(`ChatFunctionTool.Strict`)→ contract
  → `internal/core/adapter/openai/request_wire.go`(`Tools: marshalJSONValue(req.Tools)` 原样上送),无任何 Drop;
  `openai/deepseek/drop.go` 只剔除 `type==custom` tool,function tool 整体保留。
- **官方依据**:[Function Calling·strict 模式](https://api-docs.deepseek.com/zh-cn/guides/function_calling)——
  Beta 能力,需 `base_url=https://api.deepseek.com/beta`,function 设 `strict:true`。
- **结论**:协议层无需改动。strict 是否真正生效取决于 **channel 的 `base_url` 是否指向 beta**——这是
  channel 业务数据(运营/后台配置,归 phase 13),不是 adapter 决策。**不全局切 beta**(Beta 稳定性与
  特性差异,影响全部 DeepSeek 流量);需严格结构化输出的客户由运营配置专用 beta channel。无需新 GAP。
- **残留(低优先)**:正式 base_url 下 `strict:true` 会被原样上送但上游可能忽略(非 Unio Drop);
  若需对客户暴露"该 channel 是否支持 strict",归 phase 12 capability 表的 `tools.function` 子能力,低优先。

### U4 · 输出上限核对(2026-06-07,核对结论:无代码改动)
- **无过时封顶**:客户 `max_tokens`/`max_output_tokens` 原样透传,无任何旧上限对 v4(输出 384K)做截断。
- **tokenizer 无 v4 失效**:仅估算输入;`internal/core/adapter/openai/tokenizer.go` 的 `fallbackEncoding`
  已用 `deepseek-*` 前缀兜底(含 `deepseek-v4-*`)→ `Cl100kBase`,不会因 v4 名报错;Anthropic 为字符启发式。
- **唯一假设**:全局 `lifecycle.DefaultAuthorizationMaxCompletionTokens = 4096`,仅在客户**省略**输出上限时
  作预授权兜底(非封顶)。这是**跨 provider 既有简化**,v4 的 384K 上限放大了「省略上限客户」的欠冻结风险
  (实际输出 > 4096 时预冻结偏小,settlement 仍按真实 usage 扣,极端情况下平台短冻结)。
- **正解**:按模型输出上限的精确预授权属 phase 12 `model_capabilities`(DEC-015)范畴,不在 adapter/全局魔数层改。
- **处置(2026-06-07,已确认)**:已登记 [GAP-12-010](../../production/TODO_REGISTER.md#gap-12-010)(P1,非上线阻断,锚 TASK-12.01)。
  本阶段**不**调整全局兜底魔数;正解为 authorization 改用 `models.max_output_tokens` 按模型预授权。
  代码 TODO 标记见 `internal/service/gateway/lifecycle/authorization.go`(`DefaultAuthorizationMaxCompletionTokens`)。

### U1 · Responses 跨轮 reasoning 回灌(2026-06-07)
- **入站**:紧邻 `function_call` 之前的 reasoning item 翻回该轮 `assistant.reasoning_content`
  (仅工具调用轮),避免开启思考 + 工具循环时 DeepSeek 400;非工具轮 reasoning 丢弃。
  还原优先级:`encrypted_content`(Unio 载体)→ `content.reasoning_text` → `summary.summary_text`。
- **出站**:`include:["reasoning.encrypted_content"]` 或无状态(`store:false`)时,reasoning item 附带
  可逆 `encrypted_content` 回放载体(`unio-rsn-v1:`+base64);非流式与流式(`output_item.done`)一致。
- 代码:`responses_chat_map.go`、`responses_response_map.go`、`responses_stream.go`;
  bridge/output/stream 单测 + emit↔parse round-trip 覆盖。
- **残留(GAP-11-003,未关闭)**:真实 Codex stateless 是否原样回传 reasoning item 待真实 Codex 黑盒
  确认;`reasoning.summary` 与 OpenAI 原生语义差异未对齐。

### U2 · 模型目录迁移到 V4(2026-06-07)
- 测试/夹具旧模型名统一为 v4:`deepseek-chat→deepseek-v4-flash`、`deepseek-reasoner→deepseek-v4-pro`
  (26 个文件);确认无生产代码硬编码旧名;`seed` 已用 `deepseek-v4-pro`。
- DB catalog、`channel_models.upstream_model`、`channel_cost_prices` 属运营数据,由运营按 v4 配置
  (成本价对齐见 U3)。

### U5 · Anthropic `output_config.effort` 归一(2026-06-07)
- adapter 出站显式归一 effort 为 `high`/`max`(minimal/low/medium/high→high,xhigh/max→max),
  未知值 Drop 让上游回退默认;不再依赖上游隐式兼容映射。
- 代码:`internal/core/adapter/anthropic/deepseek/drop.go`(`adaptOutputConfig`、`normalizeOutputConfigEffort`)。
