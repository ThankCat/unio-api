# DeepSeek 计费

本文件记录 DeepSeek 的**上游成本口径**与 token 计量方式。对外售价由 Unio 运营在价格表中配置
(不在本文件),计费链路事实见 [docs/chapters/phase-07-billing-ledger](../../chapters/phase-07-billing-ledger/STATUS.md)。

## 1. 上游价格(成本口径)

单位:人民币(元)/ 百万 token。

| 模型 | 输入(缓存未命中) | 输入(缓存命中) | 输出 |
| --- | --- | --- | --- |
| `deepseek-v4-pro` | 3 元 | 0.025 元 | 6 元 |
| `deepseek-v4-flash` | 1 元 | 0.02 元 | 2 元 |

来源:[官方·模型&价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)(查阅 2026-06-07)。
价格会变,引用时以官方页面为准并刷新日期。

> 提示:缓存命中价比未命中便宜约 100 倍,长前缀复用对成本影响很大。

## 2. token 计量维度

DeepSeek 在 `usage` 中按以下维度返回(OpenAI 格式):

| 维度 | usage 字段 | 计费用途 |
| --- | --- | --- |
| 缓存未命中输入 | `prompt_cache_miss_tokens` | 按「输入(未命中)」价 |
| 缓存命中输入 | `prompt_cache_hit_tokens` | 按「输入(命中)」价 |
| 输出(含 reasoning) | `completion_tokens` | 按「输出」价 |
| 其中 reasoning | `completion_tokens_details.reasoning_tokens` | reasoning 计入输出量 |

校验:`prompt_tokens = 命中 + 未命中`;`total_tokens = prompt + completion`。
来源:[官方·上下文硬盘缓存](https://api-docs.deepseek.com/zh-cn/guides/kv_cache)、
[官方·Token 用量计算](https://api-docs.deepseek.com/zh-cn/quick_start/token_usage)(查阅 2026-06-07)。

> **reasoning 计费**:思维链 token 已包含在 `completion_tokens`(输出)里,按输出价计费,DeepSeek 不单独定价。
> 上下文缓存默认开启,无需客户操作。

## 3. 成本计算公式

```text
上游成本 = 未命中输入tok × 未命中输入价/1e6
        + 命中输入tok   × 命中输入价/1e6
        + 输出tok       × 输出价/1e6
```

(输出已含 reasoning;若运营对 reasoning 单独定价,则把 reasoning 部分按其价单列,其余输出按输出价。)

### 算例(deepseek-v4-pro,CNY)

某请求 usage:未命中输入 90、命中输入 0、输出 635(其中 reasoning 343)。

```text
输入成本 = 90 × 3/1e6        = 0.000270 元
输出成本 = 635 × 6/1e6       = 0.003810 元
合计                         = 0.004080 元
```

> 说明:Unio 内部成本快照可把输出里的 reasoning 与普通输出分项记录(便于审计),
> 但若 reasoning 与普通输出同价(DeepSeek 即如此),合计不变。

## 4. 币种与结算

- DeepSeek 成本价币种为**人民币(元)**。
- Unio 对客户的扣费用**售价币种**(由运营价格表决定,可为 USD 等),与上游成本币种相互独立;
  成本仅用于平台利润核算,不直接参与客户余额扣减。
- 金额一律用定点小数(NUMERIC),不用浮点。
