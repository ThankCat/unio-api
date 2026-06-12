# Anthropic 官方 · 计费

> 价格有时效,本文件只固化**计费口径(token 维度)**这类协议事实;具体单价属**运营数据**,
> 由运营按官方 pricing 页配置到 `channel_cost_prices`,不在代码/本文档硬编码权威单价。

## 1. 成本口径与售价口径(分开)

- **上游成本**:Anthropic 官方按 token 计费,币种**美元(USD)/ 百万 token**。
  来源:[官方·Pricing](https://docs.anthropic.com/en/docs/about-claude/pricing)(查阅 2026-06-12,具体单价以官方页为准)。
- **对外售价**:由平台按 channel / 模型配置(`prices`),与上游成本解耦。

## 2. token 计量维度

| 维度 | usage 字段 | 计费含义 |
| --- | --- | --- |
| 输入 | `usage.input_tokens` | 非缓存输入 token |
| 缓存写入(5m) | `usage.cache_creation.ephemeral_5m_input_tokens` | 写入 5m TTL 缓存,官方有**写入溢价** |
| 缓存写入(1h) | `usage.cache_creation.ephemeral_1h_input_tokens` | 写入 1h TTL 缓存,溢价更高 |
| 缓存命中读取 | `usage.cache_read_input_tokens` | 命中缓存读取,官方**大幅降价** |
| 输出 | `usage.output_tokens` | 输出 token(含 thinking) |
| thinking 输出 | `usage.output_tokens_details.thinking_tokens` | 思考 token,计入输出 |
| 内置工具 | `usage.server_tool_use.web_search_requests` / `web_fetch_requests` | 按次计费的内置工具调用 |

> Anthropic 缓存计费比 OpenAI 复杂:**写入有溢价、命中有折扣,且按 5m / 1h 两档 TTL 分别计价**。
> base `usageWire` 已分维度解析这些字段(`wire.go` L47–70),计费实现需按官方各档单价分别计算。

## 3. 计费公式(口径示意)

```text
cost = input_tokens × 单价_input
     + cache_creation_5m × 单价_cache_write_5m
     + cache_creation_1h × 单价_cache_write_1h
     + cache_read_input_tokens × 单价_cache_read
     + output_tokens × 单价_output
     + server_tool_use.* × 单价_per_tool_call
```

> 算例与具体单价待运营按官方 pricing 页填入对应模型后补充(避免在本文档固化会过期的单价)。

## 4. 待办

- 接入后核对官方 pricing 页,把在售模型单价(含 cache 5m/1h 写入溢价、cache read 折扣、内置工具单价)配置进
  `channel_cost_prices`(运营数据)。
- ⚠️ 待查证:官方内置工具(web_search/web_fetch)的按次单价口径,刷新本文档 §2/§3 并标查阅日期。
