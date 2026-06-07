# DeepSeek 升级/新增计划

> **官方文档最新日期**:2026-04-24(DeepSeek-V4 发布)
> **官方文档地址**:<https://api-docs.deepseek.com/zh-cn/>(更新日志:<https://api-docs.deepseek.com/zh-cn/updates>)
> **本计划查阅日期**:2026-06-07

DeepSeek 已是在接上游,故本文件是**升级计划**(非新增)。下表为待升级项;关闭后移入「已完成」。

## 待升级项

### U1 · Responses 跨轮 reasoning 回灌(P1)
- **现状**:Responses 入口丢弃客户回传的 reasoning item,不把 `reasoning_content` 回灌 DeepSeek。
  代码:`internal/service/gateway/openai/responses/responses_chat_map.go`。
- **官方依据**:[思考模式·工具调用](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode)——
  进行了工具调用的轮次,后续请求必须完整回传 `reasoning_content`,否则返回 **400**。
- **影响**:Codex 经 `/v1/responses` **开启 reasoning + 工具循环**时,第 2 轮起 DeepSeek 400,agent 链路中断。
  (此前 e2e 能过是因 Responses 默认关思考,未触发。)
- **方案**:出站把 DeepSeek `reasoning_content` 以可回读形式带进 Responses reasoning item;
  入站把紧邻 `function_call` 的 reasoning item 翻回 `assistant.reasoning_content`。需处理 stateless 下 Codex 的回传形态。
- **关联**:GAP-11-003(建议由 P2 升级为 P1)。
- **状态**:todo。

### U2 · 模型目录迁移到 V4(P1,有时间窗)
- **现状**:历史使用 `deepseek-chat` / `deepseek-reasoner`;部分测试仍引用 `deepseek-chat`。
- **官方依据**:[更新日志 2026-04-24](https://api-docs.deepseek.com/zh-cn/updates)——
  两个旧模型名 **2026-07-24 弃用**,新默认 `deepseek-v4-flash` / `deepseek-v4-pro`。
- **影响**:不迁移则弃用日后路由到旧名会失败。
- **方案**:更新 catalog `models`、`channel_models.upstream_model` 为 v4 名;清理测试里的 `deepseek-chat`(数据/代码层)。
- **状态**:todo。

### U3 · 成本价配置对齐 V4 真实价(P1)
- **现状**:成本价配置需与 v4 实际价对齐(见 [billing.md](billing.md))。
- **官方依据**:[模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)——
  v4-pro 输入 3 / 命中 0.025 / 输出 6 元;v4-flash 1 / 0.02 / 2 元(每百万 token)。
- **影响**:配置错则平台利润核算失真。
- **方案**:按上表更新 `channel_cost_prices`(运营数据)。
- **状态**:todo。

### U4 · 输出上限核对(P2)
- **现状**:authorization 估算/tokenizer 是否仍按旧上限假设需核对。
- **官方依据**:[模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)——v4 上下文 1M、输出最大 384K。
- **方案**:核对预授权估算与 tokenizer 是否有过时上限,必要时调整。
- **状态**:todo。

### U5 · Anthropic `output_config.effort` 归一(P2)
- **现状**:Anthropic 格式 `output_config.effort` 原样透传,靠上游做兼容映射。
- **官方依据**:[思考模式](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode)——low/medium→high、xhigh→max。
- **方案**:与 OpenAI 侧一致地显式归一,减少对上游隐式行为的依赖。
- **状态**:todo。

### U6 · web_search server tool 确认(P2)
- **现状**:Anthropic 入站 `server_tool_use`/`web_search_tool_result` 已保留,但 tools 数组里 web_search 工具定义被 Drop。
- **官方依据**:[Anthropic API 兼容性](https://api-docs.deepseek.com/zh-cn/guides/anthropic_api)——server_tool_use/web_search_tool_result 标 Supported。
- **方案**:黑盒确认 DeepSeek 是否支持**主动请求** web_search,再决定是否放行工具定义。
- **状态**:待查证 + 黑盒。

### U7 · Function Calling strict 模式评估(P3)
- **现状**:未启用 strict 模式(JSON Schema 严格校验)。
- **官方依据**:[Function Calling·strict 模式](https://api-docs.deepseek.com/zh-cn/guides/function_calling)——
  Beta 能力,需 `base_url=.../beta`,所有 function 设 `strict:true`。
- **方案**:评估是否为需要严格结构化输出的客户接入(注意 Beta 与 base_url 切换成本)。
- **状态**:todo(低优先)。

## 已完成

(暂无)
