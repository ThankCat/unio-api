# OpenAI 官方 · 计费

> 价格有时效,本文件只固化**计费口径(token 维度)**这类协议事实;具体单价属**运营数据**,
> 由运营按官方 pricing 页配置到 `channel_cost_prices`,不在代码/本文档硬编码权威单价。

## 1. 成本口径与售价口径(分开)

- **上游成本**:OpenAI 官方按 token 计费,币种**美元(USD)/ 百万 token**。
  来源:[官方·Pricing](https://platform.openai.com/docs/pricing)(查阅 2026-06-12,具体单价以官方页为准)。
- **对外售价**:由平台按 channel / 模型配置(`prices`),与上游成本解耦。

## 2. token 计量维度

| 维度 | usage 字段 | 计费含义 |
| --- | --- | --- |
| 总输入 | `usage.prompt_tokens` | 输入 token 总数(含缓存命中) |
| 缓存命中输入 | `usage.prompt_tokens_details.cached_tokens` | 命中部分通常**降价**(官方对 cached input 折扣) |
| 输出 | `usage.completion_tokens` | 输出 token 总数(含 reasoning) |
| reasoning 输出 | `usage.completion_tokens_details.reasoning_tokens` | reasoning token,按输出价计费(官方计入 completion) |
| 总量 | `usage.total_tokens` | 校验:`= prompt_tokens + completion_tokens` |

## 3. 计费公式(口径示意)

```text
cost = (prompt_tokens - cached_tokens) × 单价_uncached_input
     + cached_tokens × 单价_cached_input
     + completion_tokens × 单价_output
```

> 算例与具体单价待运营按官方 pricing 页填入对应模型后补充(避免在本文档固化会过期的单价)。

## 4. 缓存与 reasoning 计费要点

- **prompt caching**:官方对命中缓存的输入 token 给折扣;命中数见 `prompt_tokens_details.cached_tokens`。
  ⚠️ 待查证:官方 chat completions 是否回传 cache **write** TTL 维度(若无,`CacheWrite*InputTokens = not_applicable`)。
- **reasoning**:reasoning token 计入 `completion_tokens`,按输出价计费(官方口径,查阅 2026-06-12)。

## 5. 待办

- 接入后核对官方 pricing 页,把在售模型单价配置进 `channel_cost_prices`(运营数据)。
- 确认官方 cached input 折扣比例与 reasoning 计费细则,刷新本文档 §4 并标查阅日期。
