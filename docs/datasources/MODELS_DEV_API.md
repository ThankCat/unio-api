# models.dev 数据源接口参考

来源：[models.dev](https://models.dev)（查阅 2026-06-07）。

models.dev 是社区维护的模型元数据数据库,Unio 把它当作能力架构 **Layer 1 元数据的种子源**
（[DEC-015](../production/DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位)），由 [TASK-12.04](../chapters/phase-12-capability-architecture/PLAN.md#task-12-04-models-dev-sync) 的每日 cron 消费。

> 放置说明：models.dev 既不是 Unio 对外协议（[docs/protocol/](../protocol/README.md)），也不是被代理的上游 provider
> （[docs/providers/](../providers/README.md)），而是元数据**数据源**，故归 [docs/datasources/](README.md)；
> license 与 attribution 见同目录 `MODELS_DEV_LICENSE.md`（GAP-12-005）。
> 它**不是运行时事实源**：能力闸门、计费、路由的事实只以 Unio 自己的库为准,models.dev 仅作种子。

## 1. 四个接口总览

| 接口 | 大小（查阅日） | 顶层结构 | 用途 |
| --- | --- | --- | --- |
| `GET https://models.dev/api.json` | ≈2.2 MB | object,按 **provider id** 键控（139 个） | provider × model 全量目录,**含每 provider 价格** |
| `GET https://models.dev/models.json` | ≈122 KB | object,按 **canonical id `lab/model`** 键控（202 个） | 模型为中心的元数据（无 provider、无价格）,**含 weights/benchmarks** |
| `GET https://models.dev/catalog.json` | ≈2.3 MB | object `{ models, providers }` | api.json + models.json 的**合并一把梭**（models=202 canonical,providers=139） |
| `GET https://models.dev/logos/<id>.svg` | 每个数百~数千 B | `image/svg+xml` | 各 provider/lab 的 logo 资产（如 `anthropic.svg`、`deepseek.svg`） |

数量会随上游变化,以实际拉取为准。

## 2. `api.json`（provider × model + 价格）

顶层按 provider id 键控。每个 provider 对象：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | string | provider 标识（如 `deepseek`） |
| `name` | string | 展示名 |
| `api` | string | 上游 base URL（如 `https://api.deepseek.com`） |
| `doc` | string | 官方文档/定价页 URL |
| `env` | string[] | 该 provider 期望的 API key 环境变量名（如 `["DEEPSEEK_API_KEY"]`） |
| `npm` | string | 社区 SDK 包名（如 `@ai-sdk/openai-compatible`） |
| `models` | object | 该 provider 下的模型,按 **provider 内模型 id** 键控（如 `deepseek-v4-pro`） |

`models[*]` 在第 4 节模型字段基础上**额外带每 provider 价格** `cost`：

```json
"cost": { "input": 0.435, "output": 0.87, "cache_read": 0.003625 }
```

`cost.*` 为 **USD / 百万 token（models.dev 口径）**；不同 provider 同一模型价格不同。

## 3. `models.json`（canonical 模型元数据）

顶层按 **canonical id**（`lab/model`,如 `deepseek/deepseek-v4-pro`）键控,值是模型为中心的元数据,
**不含 provider 与 cost**,但比 api.json 的 model 多带 `weights[]` 与 `benchmarks[]`。
这个 key 正是 Unio `models.canonical_id` 的 join key。

## 4. canonical 模型字段（models.json 值 / catalog.json.models 值 / api.json.models 值的公共部分）

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | string | 模型 id（models.json 为 `lab/model`,api.json 为 provider 内 id） |
| `name` | string | 展示名 |
| `family` | string | 模型家族（如 `deepseek-thinking`） |
| `attachment` | bool | 是否支持附件输入 |
| `reasoning` | bool | 是否推理模型 |
| `tool_call` | bool | 是否支持工具调用 |
| `structured_output` | bool（可选） | 是否支持结构化输出 |
| `temperature` | bool | 是否支持 temperature |
| `knowledge` | string | 知识截止（`YYYY-MM`） |
| `release_date` | string | 发布日期（`YYYY-MM-DD`） |
| `last_updated` | string | 元数据更新日期 |
| `modalities` | object | `{ input: string[], output: string[] }`（如 `text`/`image`/`audio`） |
| `open_weights` | bool | 是否开放权重 |
| `limit` | object | `{ context: int, output: int }` 上下文窗口与最大输出 token |
| `weights` | array（仅 models/catalog） | 权重下载来源 `[{label,url}]` |
| `benchmarks` | array（仅 models/catalog） | 跑分 `[{name,score,metric,source,...}]` |

`cost` 仅出现在 **api.json**（per provider）与 **catalog.json.providers** 内,不在 canonical 模型对象里。

## 5. `catalog.json`（合并）

顶层 `{ models, providers }`：

- `catalog.models`：与 `models.json` 同构（canonical 模型,202 个）。
- `catalog.providers`：与 `api.json` 同构（provider 含 `models`+`cost`,139 个）。

一次请求拿到模型 + provider 全量,代价是 ≈2.3 MB。

## 6. `logos/<id>.svg`

按 provider/lab id 取 SVG（`image/svg+xml`），如 `https://models.dev/logos/anthropic.svg`、
`https://models.dev/logos/deepseek.svg`。属 **UI 资产**,供 console/admin 展示,**不进 gateway/计费/路由**。

## 7. Unio 消费映射（与 Layer 1 对齐）

| Unio 落点 | 取自 | 说明 |
| --- | --- | --- |
| `models.canonical_id` | models.json / catalog.json.models 的 key（`lab/model`） | 同步 join key |
| `models.lab` / `display_name` / `release_date` | canonical 模型 `family`/`name`/`release_date` | 元数据 |
| `models.context_window_tokens` / `max_output_tokens` | canonical `limit.context` / `limit.output` | 元数据；`max_output_tokens` 也是 GAP-12-010 预授权兜底数据源 |
| `models.input_price_/output_price_usd_per_million_tokens` | api.json / catalog.json.providers 的 `cost.input`/`cost.output` | **价格基线,仅展示,绝不用于计费** |
| `model_capabilities`（粗种子,`source=models_dev`） | canonical 的 `reasoning`/`tool_call`/`attachment`/`structured_output`/`modalities` | 粒度太粗,仅作首次入库默认值；精化由 Unio Layer 2 维护 |
| logo 资产 | `logos/<id>.svg` | console/admin UI,非运行时事实 |

## 8. 同步取数建议（TASK-12.04）

- **canonical 元数据**优先用 `models.json`：体量小（≈122 KB）、按 canonical_id 键控、与我方 join key 直接对齐。
- **价格基线**用 `api.json`（或 `catalog.json.providers`）的 per-provider `cost`；注意同模型多 provider 价格不一,需选定口径（如该模型 lab 官方 provider）。
- 想一把拉全（模型 + provider）可用 `catalog.json`,但要权衡 ≈2.3 MB 体量。
- logos 走 UI 侧按需取,不进同步主流程。
- license 与 attribution 见同目录 `MODELS_DEV_LICENSE.md`（GAP-12-005,首次同步前必须确认）。
