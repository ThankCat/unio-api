# 改造方案：渠道成本也走倍率（成本 = 上游参考价 × 渠道成本倍率）

> 提议 **DEC-027**（pending，待审）。承接 **DEC-026**（倍率定价，售价侧）——本方案把「倍率」这套已在售价侧验证过的模式，**对称地补到成本侧**。
>
> - 撰写基准：对照当前工作区代码（`unio-api` / `unio-admin`）逐文件勘探，文件/行号来自真实代码（勘探时点）。
> - **阅读约定**：英文/专业词首次出现配「（中文解释）」；复杂逻辑用「小明发请求」实例。
> - 本文 = 设计 + 执行计划合一。先读 §1（模型）与 §2（决策 + 为什么这样选），再按 §7 分阶段实施。

---

## 1. 一句话与模型

**一句话**：上游中转站按「官方价 × 它的倍率」向你计价；所以把渠道成本也拆成 **每模型「上游参考价」（稳定、少变）× 每渠道「成本倍率」（多变，中转一调你就改）**，结算时相乘得到成本、并把结果冻结进 `cost_snapshots`。**上游改一次倍率 → 你只改 1 个数，该渠道所有模型同步生效。**

```mermaid
graph TD
    subgraph 售价侧["售价侧（DEC-026，已上线）"]
      MP["model_prices<br/>每模型基准售价（稳定）"] -->|× route.price_ratio| Sale["客户售价（结算时算 + 快照）"]
      RR["routes.price_ratio<br/>每线路倍率（多变）"] --> Sale
    end
    subgraph 成本侧["成本侧（本方案，对称新增）"]
      MRC["model_reference_costs<br/>每模型上游参考价（稳定，新表）"] -->|× channel 成本倍率| Cost["渠道成本（结算时算 + 快照）"]
      CCM["channel_cost_multipliers<br/>每渠道成本倍率（多变，新表）"] --> Cost
      OV["channel_prices（现表，退为「绝对值覆盖」）<br/>个别模型手填绝对成本，优先级最高"] -.覆盖.-> Cost
    end
    Sale --> Margin["毛利 = 售价 − 成本"]
    Cost --> Margin
```

**概念锚点**：
- **model_reference_costs（上游参考价，新表）**：每个模型一组「上游官方/参考成本向量」，versioned（`effective_from/to + status`），跨渠道共享。语义 = new-api 的「模型倍率基准」，但落成明确金额。**变化频率低**（provider 官方调价时才动）。
- **channel_cost_multipliers（渠道成本倍率，新表）**：每渠道一个标量倍率（可选逐模型覆盖），versioned。语义 = 该中转站的加价倍率。**变化频率高**（中转一调倍率就改），是本方案要「一处改」的目标。
- **channel_prices（现表）**：保留为**「绝对值覆盖」逃生通道**——个别渠道/模型如果中转不按倍率计价（自有价表），仍可手填绝对成本，**优先级最高**。现有数据零改动、继续生效。

---

## 2. 决策（DEC-027，pending）与「为什么这样选」

**决策**：渠道成本从「每 (渠道,模型) 绝对金额」升级为 **`成本 = model_reference_costs(模型) × channel_cost_multipliers(渠道[/模型])`**，在**结算时相乘**得到成本向量并冻结进 `cost_snapshots`；`channel_prices` 保留为绝对值覆盖（最高优先级）。

**为什么选「结算时算」而不是「物化成 N 行」**：

| 方案 | 做法 | 优点 | 缺点 | 结论 |
|---|---|---|---|---|
| **A 物化** | 改倍率时自动重算并写入 N 条 `channel_prices` 绝对行 | 结算/路由/快照/恢复**全不动**（风险最低） | 与售价侧不对称；行数膨胀 + 每次改倍率批量收口/新建 N 行；`channel_prices` 变「派生表」需防手改漂移（双事实源） | 备选 |
| **B 结算时算（选它）** | 保留参考价+倍率分离，命中时相乘 | **与售价侧完全对称**（团队已在 DEC-026 用同套路：runtime 算 + 快照存 source id + 倍率）；真·1 处改；单一事实源、无行churn | 触及计费热路径 + `cost_snapshots` 一个 FK；改动面较大、测试更重 | **主方案** |

选 B 的关键理由：**售价侧（DEC-026）已经做过一模一样的事**——`model_prices × routes.price_ratio` 在路由/结算时算、把「算出的售价向量 + `price_ratio` + `model_price_id/route_id`」快照进 `price_snapshots`（见 `settlement.go:423-443`、`billing/scale.go` 的 `ScaleCustomerPrice`）。成本侧照抄这套，架构一致、心智负担最小、无双事实源。B 的额外风险（热路径 + FK）可控，且下文 §7 用分阶段 + 门禁 + 真实 e2e 兜住。

**审计不变量（沿用 DEC-026 补偿思路）**：结算时 `cost_snapshots` 除了**冻结算出的绝对成本向量 + 各分项金额**（现状已做），再记 **参考价行 id + 倍率行 id + 当时倍率标量**，历史成本可按原事实独立复算，且**不随后续改倍率漂移**。

---

## 3. 计费语义（含用户实例）

### 3.1 成本解析优先级（结算 / 路由候选一致）

对某次命中的 (channel C, model M) 在时刻 t 解析成本：

1. **绝对值覆盖**：若 C/M 在 t 有启用中的 `channel_prices` 行（含路由锁定的 `ChannelPriceID`）→ 直接用它（现状路径，逃生通道）。
2. **逐模型倍率覆盖**：否则若 C 对 M 有启用中的 `channel_cost_multipliers`（`model_id=M`）→ `成本 = 参考价(M,t) × 该倍率`。
3. **渠道默认倍率**：否则若 C 有启用中的默认倍率（`model_id IS NULL`）→ `成本 = 参考价(M,t) × 默认倍率`。
4. **都没有** → 该 (C,M) 视为「未定价」：路由候选阶段就排除；结算阶段报错（与现状「无 channel_price」同语义）。

> 参考价缺失（模型没配 `model_reference_costs`）但配了倍率 → 视为未定价（路由排除 + 管理台醒目告警「模型 M 缺上游参考价」）。

### 3.2 与售价 / 冻结 / 扣费的关系（**不动**）

- 冻结（pre-authorize）/ 扣费（capture）**只用售价**（`SalePrice = 基准 × 线路倍率`），成本**不参与**授权额。本方案**不触碰**授权与扣费路径。
- 成本只在**结算**与**风险敞口估算**（`cost_exposure.go`）里用于算平台成本/毛利，写 `cost_snapshots`。
- 稳定账单不变量（DEC-026）不受影响：fallback 命中更贵成本渠道，客户售价不变、只吃平台毛利。

### 3.3 用户实例

> **实例 A（一处改倍率）**：某中转渠道 C 绑了 20 个模型，之前每个模型各录一条绝对成本。中转把倍率从 1.2 调到 1.15 → 你只新建 1 条 C 的默认成本倍率（1.15，生效=现在），**20 个模型的成本同时按新倍率生效**，历史请求成本不变（已快照冻结）。
>
> **实例 B（个别模型特殊）**：中转对 `claude-opus` 单独加价 1.4、其余 1.15 → C 默认倍率 1.15 + 对 `claude-opus` 建一条逐模型覆盖 1.4。
>
> **实例 C（中转不按倍率）**：某渠道自有价表、跟官方价无倍率关系 → 对该 (渠道,模型) 用 `channel_prices` 手填绝对成本（覆盖，优先级最高），其余照走倍率。
>
> **实例 D（历史复算）**：3 个月前的请求，其 `cost_snapshots` 冻结了当时算出的绝对成本 + 当时倍率 + 参考价行 id；你今天怎么改倍率都不影响它，审计可原样复算。

---

## 4. 数据模型改动（migrations，新号从勘探最大号 000075 之后起，**实施前以仓库实际为准**）

- [ ] **`000076_create_model_reference_costs`**：结构对齐 `model_prices`（`migrations/000054`）但语义是「上游参考**成本**」：`id, model_id FK→models, currency TEXT, pricing_unit CHECK='per_1m_tokens', <成本列: uncached_input_cost NOT NULL, output_cost NOT NULL, cache_read_input_cost, cache_write_5m/1h/30m_input_cost, reasoning_output_cost（均 NUMERIC(20,10) ≥0，主两项必填、其余可空>, status CHECK IN('enabled','disabled'), effective_from/to, created/updated_at`。约束对齐：`uq(id, model_id)`、`ck_window(effective_to>effective_from)`、`ex_model_reference_costs_enabled_window`（按 `model_id+currency+pricing_unit` 的启用窗口不重叠，`EXCLUDE USING gist` + btree_gist）；索引 `(model_id, status, effective_from DESC, id DESC)`。
- [ ] **`000077_create_channel_cost_multipliers`**：`id, channel_id FK→channels, model_id BIGINT NULL（NULL=渠道默认；非空=该模型覆盖）, multiplier NUMERIC(20,10) NOT NULL CHECK(multiplier>=0), status, effective_from/to, created/updated_at`。约束：`ck_window`；启用窗口不重叠用**生成列** `model_key BIGINT GENERATED ALWAYS AS (COALESCE(model_id,0)) STORED` + `EXCLUDE USING gist (channel_id WITH =, model_key WITH =, tstzrange(...) WITH &&) WHERE (status='enabled')`（把「默认」与各「逐模型覆盖」各自当一条时间线，互不重叠）；`model_id` 非空时应指向合法 `channel_models` 绑定——用**应用层校验**（`model_id NULL` 无法进复合 FK），可选加触发器兜底；索引 `(channel_id, model_key, status, effective_from DESC, id DESC)`。
- [ ] **`000078_cost_snapshots_add_multiplier_source`**：给 `cost_snapshots` 加 `model_reference_cost_id BIGINT`、`channel_cost_multiplier_id BIGINT`、`cost_multiplier NUMERIC(20,10)`（倍率路径下有值；绝对覆盖路径下为 NULL）；把 `cost_price_id` 改**可空**并**放开** `fk_cost_snapshots_cost_price_channel_model`（改为可空复合 FK：`cost_price_id` 非空时才校验属于同 channel/model）。**对齐售价侧** `price_snapshots`（`000057` 加 `model_price_id/route_id/price_ratio` 并让 `price_id` 可空）的既有做法。
- [ ] **`000079_settlement_recovery_jobs_add_cost_source`**：给 `settlement_recovery_jobs` 加与结算一致的成本来源列（`model_reference_cost_id/channel_cost_multiplier_id/cost_multiplier`，或至少能让 replay 按 attemptStart 时点确定性复算），与售价侧 replay 键对齐。
- [ ] 每个 up 配 down。**`channel_prices` 表结构不动**（继续作为绝对值覆盖），现有行零改动。
- [ ] （可选回填）为常用模型建 `model_reference_costs`（可用现有 `channel_prices` 成本行反推种子）；为每个纯倍率中转渠道建一条默认 `channel_cost_multipliers`。不回填也能跑（走绝对覆盖）。

---

## 5. 后端改动（文件/行号来自勘探）

### 5.1 sqlc 查询

- [ ] **新增** `sql/queries/model_reference_costs.sql`：`CreateModelReferenceCost` / `Get...` / `ListModelReferenceCostsByModel` / `ListEnabledModelReferenceCostWindows`（窗口校验）/ `UpdateModelReferenceCostWindow` / **`FindActiveModelReferenceCost(model_id, at_time)`**。仿 `sql/queries/model_prices.sql`。
- [ ] **新增** `sql/queries/channel_cost_multipliers.sql`：CRUD + 窗口校验 + **`FindActiveChannelCostMultiplier(channel_id, model_id, at_time)`**（先找 `model_id=M` 的覆盖，无则找 `model_id IS NULL` 的默认，`ORDER BY (model_id IS NULL), effective_from DESC` → 覆盖优先）。
- [ ] **改** `sql/queries/channel_models.sql` 的候选查询（`FindRouteCandidates`，勘探含 `ChannelPriceID` 列，行 241/306 一带）：候选「已定价」过滤从「有 `channel_prices` 成本行」改为「**有绝对覆盖** OR **(模型有参考价 AND 渠道有可用倍率)**」；仍带回**成本来源标识**（`ChannelPriceID` 覆盖 id，或标记走倍率），供结算解析。绝对覆盖存在时照旧回填 `ChannelPriceID`（锁价 pin 语义不变）。
- [ ] **改** `sql/queries/cost_snapshots.sql`：`CreateCostSnapshot` 增写 `model_reference_cost_id/channel_cost_multiplier_id/cost_multiplier`。
- [ ] `sqlc generate`。

### 5.2 计费核心 `internal/core/billing/`

- [ ] **新增** `ScaleProviderCost(base ProviderCostSnapshot, multiplier pgtype.Numeric) (ProviderCostSnapshot, error)`：逐分项乘倍率、四舍五入到 `NUMERIC(20,10)`，NULL 分项保持 NULL。**照抄** `scale.go` 的 `ScaleCustomerPrice`（同一套 `scaleRate`/`ratToNumeric`）。
- [ ] `CalculateProviderCost`（`service.go:34`）**不改**（仍吃 `ProviderCostSnapshot`，只是该向量现在可能是算出来的）。

### 5.3 结算 `lifecycle/settlement.go` + 恢复 + 敞口

- [ ] 把 `resolveSettlementChannelPrice`（`108-154`）升级为 **`resolveSettlementCost(...) (ProviderCostSnapshot, CostSource, error)`**，按 §3.1 优先级：
  - 有 pin `ChannelPriceID` 且行存在且属本 (channel,model) → 取该绝对行成本（现状 `channelPriceCostSnapshot`，`965-980`）。
  - 否则按 `attemptStart` 查 `FindActiveChannelPrice`（绝对覆盖）→ 有则用。
  - 否则 `FindActiveModelReferenceCost(M, attemptStart)` × `FindActiveChannelCostMultiplier(C, M, attemptStart)` 经 `ScaleProviderCost` 得成本；记录 `CostSource{referenceID, multiplierID, multiplier}`。
  - 都无 → 报 `CodeGatewayChatSettlementFailed`（未定价）。
  - **按 `attemptStart` 时点解析 = 天然防漂移**（参考价/倍率都是不可改+时间窗+新建一条），无需新 pin 即与现有 P1-3 同等安全；绝对覆盖仍走既有 pin。
- [ ] 结算写 `cost_snapshots`（`473-506`）：`cost_price_id` 走覆盖时填行 id、走倍率时为 NULL；增写 `model_reference_cost_id/channel_cost_multiplier_id/cost_multiplier`。
- [ ] 幂等复算分支（`885-916`）：从 `cost_snapshots` 已冻结的成本向量复算（现状即如此），**不受影响**；仅需校验新增来源列一致。
- [ ] `settlement_recovery.go`（`127-135, 329-333`）：持久化/透传新成本来源，replay 走同一 `resolveSettlementCost`，按 attemptStart 确定性复算。
- [ ] `cost_exposure.go`（`118` 用 `CalculateProviderCost`）：其成本解析改用同一 `resolveSettlementCost`，口径统一。
- [ ] `attempt_runner.go`（`322`）/ `attempt_runner_stream.go`（`353`）：候选透传维持 `ChannelPriceID`（覆盖场景），倍率场景无需额外 pin（attemptStart 解析）。

### 5.4 选路 `internal/core/routing/router.go`

- [ ] 候选构建：`ChannelPriceID`（`94-95, 431`）语义收窄为「绝对覆盖行 id（若有）」；候选「已定价」判定改为 §3.1 可解析即可。cheapest 排序若按成本——需要候选带**算出的成本**（参考×倍率）以比较；在候选查询/构建处解析成本向量（与售价侧 `buildChatRouteCandidate` 算 `SalePrice` 对称）。

### 5.5 Admin 后端

- [ ] **模型参考价管理（新）**：仿 `service/admin/modelprice` + `adminapi/model_prices.go`，做 `service/admin/modelreferencecost` + `adminapi/model_reference_costs.go`，路由 `GET/POST /models/{id}/reference-costs`、`PATCH /model-reference-costs/{id}`。
- [ ] **渠道成本倍率管理（新）**：`service/admin/channelcostmultiplier` + `adminapi/channel_cost_multipliers.go`，路由 `GET/POST /channels/{id}/cost-multipliers`（默认 + 逐模型覆盖）、`PATCH /channel-cost-multipliers/{id}`。窗口重叠/收口逻辑复用现有「新建一条 + 关闭旧窗口」范式（与 `channelprice` 一致）。
- [ ] **`channelprice` 保留**（绝对覆盖），仅在文案上标注「绝对成本覆盖（优先级最高）」。

---

## 6. 前端 / 运维体验（**重点**：人性化 + 合理创建价格 + 价格差异可视化，细节决定成败）

> 全部复用现有组件与约定，不另起炉灶：`ChannelPricesDialog` 的分项网格 + `CostRow`/`CostDelta`（红涨绿降带箭头）+ `PriceOverwriteDialog`（收口/停用确认）+ `findOverlappingChannelPrices`（`unio-admin/src/components/channels/ChannelPricesDialog.tsx`、`lib/api/channelPrices.ts`）、`DateTimePicker`、`Badge`、`Field/HintLabel`、`ServerDataTable`/`DetailPageHeader`、`format.ts`（`trimDecimal`/`roundPrice3`/`formatUSD`/`formatUSDPrecise`/`localToRFC3339`）。

### 6.0 心智模型（一句话讲清给运维）

三层，各司其职，UI 上就分成三块、颜色/图标固定：

| 概念 | 谁配、多久变一次 | UI 载体 | 视觉基调 |
|---|---|---|---|
| **模型参考价**（上游官方价） | 每模型 1 处，很少变 | `ModelReferenceCostDialog`（模型详情） | 中性、稳重 |
| **渠道成本倍率**（中转加价） | 每渠道 1 个数，**经常变** | 渠道详情「成本倍率」页（本次主角） | 主色强调、改动即预览 |
| **绝对成本覆盖**（例外逃生） | 极少数 (渠道,模型) | `ChannelPricesDialog`（重定位） | 弱化、加「覆盖」徽标 |

**贯穿全程的两条铁律**（下面每个界面都遵守）：
1. **合理创建**：开始时间留空 = 立即生效（已做）；结束时间留空 = 长期；创建即预览影响；命中重叠自动走「收口/停用」确认（已做）。运维不需要懂时间窗也能配对。
2. **差异可视化**：任何一次改动，在**确认前**就用「旧 → 新 + 彩色差额 + 影响面计数 + 毛利红绿」把后果摆到眼前。绝不让人「盲改后才发现」。

### 6.1 模型参考价管理 `ModelReferenceCostDialog`（新）

仿 `ChannelPricesDialog` 的 list↔create 两视图 + 分项网格 + versioned；入口挂模型详情「成本/定价」区（`lib/api/modelReferenceCosts.ts` 新增）。

- **create 视图**：分项「上游参考成本」向量（`uncached_input`* / `output`* 必填，其余可空）+ 币种 + 生效开始（留空=立即）/结束（留空=长期）。复用 `CostRow`：**当前价 | 参考价输入 | 差额（`CostDelta`）** 三列，一边填一边看和现价的差。
- **合理创建**：留空开始时间默认 `now`；命中已有参考价窗口 → 复用 `PriceOverwriteDialog` 的收口/停用确认。
- **下游影响提示**（人性化关键）：参考价被 N 个渠道引用；改它 = 影响这 N 个渠道该模型的成本。create 视图底部常驻一行：
  > 此参考价被 **5 个渠道** 引用（gpt 主力线等）。保存后，这些渠道该模型的**派生成本**将随之变化 —— 点「预览影响」看逐渠道 旧→新。
  点开是一张按渠道列的差异表（结构同 §6.3 的影响表，维度换成渠道）。

```
┌ 模型「gpt-5.5」· 上游参考价 ────────────────────────────── ✕ ┐
│ 上游官方/参考成本（每百万 token）。渠道成本 = 本参考价 × 渠道倍率。 │
│ 分项            当前价     参考价        差额                     │
│ 未缓存输入 *    0.50       [0.55]        ↑ +0.05                  │
│ 输出 *          2.00       [2.10]        ↑ +0.10                  │
│ 缓存读取        0.05       [0.05]        0                        │
│ …                                                               │
│ 币种 [USD]   生效开始[留空=立即]  生效结束[留空=长期]             │
│ ── 影响：被 5 个渠道引用，保存后派生成本随动  [预览影响 →]  ───── │
│                                    [返回]  [创建并继续]  [创建]   │
└─────────────────────────────────────────────────────────────┘
```

### 6.2 渠道成本倍率 UI（新，**运维高频主入口 —— 本次体验核心**）

入口：渠道详情新增「成本倍率」tab（或行操作「成本倍率」）。一个页面配齐**默认倍率**（`model_id=NULL`）+ 若干**逐模型覆盖**；`lib/api/channelCostMultipliers.ts` 新增。

**这一屏就是要把「改一个数、全渠道模型生效」做成一次爽快、透明、不会踩雷的操作。** 编辑倍率时，下方**实时**渲染「改动影响预览表」——不是保存后才知道，是边打字边看：

```
┌ 渠道「openai_0.16」· 成本倍率 ─────────────────────────────── ✕ ┐
│ 上游成本 = 模型参考价 × 倍率。改这里 → 该渠道所有「走倍率」模型同步生效。│
│                                                                        │
│ 默认倍率 [ 1.15 ]   生效开始[留空=立即]   生效结束[留空=长期]           │
│          当前生效 1.20（2026/7/6 起 · 长期）   [复制当前值]             │
│                                                                        │
│ ── 改动影响预览 ────────  18 模型 ·  ↑14 涨  ↓3 降  ⚠1 未定价 ──────── │
│ 模型             参考价(入/出)   旧成本 → 新成本        毛利(vs 售价)    │
│ gpt-5.5          0.55 / 2.10     0.66/2.52 → 0.63/2.42  ↓  +1.8  ● 绿  │
│ gpt-5.4          0.40 / 1.60     0.48/1.92 → 0.46/1.84  ↓  +1.1  ● 绿  │
│ gpt-5.5-mini     0.10 / 0.40     0.12/0.48 → 0.115/0.46 ↓  +0.3  ● 绿  │
│ claude-opus-4-8  0.80 / 4.00     覆盖 1.4（不随默认变）  —      ○ 覆盖  │
│ o3-pro           — 未配参考价    ⚠ 无法计价              去补参考价 →   │
│ …（滚动）                                                              │
│                                                   [取消]  [预览确认 →] │
└──────────────────────────────────────────────────────────────────────┘
```

细节：
- **默认倍率**输入即触发预览重算（防抖）。旧成本 = 当前生效倍率 × 参考价；新成本 = 输入倍率 × 参考价；差额用 `CostDelta`（红涨绿降 + 箭头），逐分项，hover 展开全 7 分项拆解。
- **逐模型覆盖**：折叠区，一行一个覆盖（模型 + 倍率 + 窗口），有覆盖的模型在预览表标「覆盖」徽标、**不随默认倍率变**（灰显）。加/删覆盖同样即时进预览。
- **未定价模型**（缺参考价）：整行灰显 + `⚠` + 「去补参考价 →」深链到 §6.1 的该模型弹窗，闭环不卡住。
- **合理创建**：开始留空=立即；点「预览确认」→ 弹 `PriceOverwriteDialog` 变体，摘要「该渠道 17 个走倍率模型的派生成本从 1.20 → 1.15，旧窗口**收口于** {now}；1 个模型有覆盖不受影响」。确认后一次事务生效（后端生成器/结算解析二选一，见 §5，前端无感）。
- **快捷**：「复制当前值」预填现行倍率；「回滚到某历史版本」= 用旧值新建一条（价格不可改，故用新建实现「撤销」）。

### 6.3 价格差异可视化（**专门抽出来做到位**）

> 你要的「差异可视化」= 一个**可复用**的展示层，三处共用：渠道倍率预览、参考价下游影响、覆盖创建对比。抽成 `PriceImpactTable` + 复用 `CostDelta`。

**A. 逐分项差额徽章（复用现成 `CostDelta`）**：`+0.05 ↑`（红=成本涨）/ `-0.02 ↓`（绿=成本降）/ `0`（灰）。金额用 `roundPrice3` 去尾零、`tabular-nums` 对齐。

**B. `PriceImpactTable`（新，跨场景复用）**：一行一个受影响对象（模型 或 渠道），列固定：
- `名称`｜`参考价(入/出)`｜`旧成本 → 新成本`（关键分项，hover 出全 7 分项）｜`差额`（`CostDelta`）｜`毛利`（红/绿圆点 + 数值）｜`来源徽标`（派生/覆盖/未定价）。
- **行 tone**：涨=红点、降=绿点、未定价=灰点 `⚠`、覆盖=空心点（复用 `ServerDataTable`/`Badge` 的 tone）。

**C. 影响面摘要条（顶部一句话，先给全局感）**：`18 模型 · ↑14 涨 ↓3 降 · ⚠1 未定价 · ⛔0 亏本`。数字点击可筛选表格（只看涨/只看亏本）。

**D. 毛利红绿（advisory 护栏）**：毛利 = 售价（`model_prices × 当前生效线路倍率`）− 新成本。
- 绿：毛利 ≥ 0；红：**亏本**（成本 > 售价）——摘要条 `⛔ 亏本 N` 高亮，确认按钮旁给非阻断警示「有 N 个模型将亏本，仍要继续？」。
- 售价随「哪条线路」而变：默认取「引用该渠道、倍率最高的线路」算最保守毛利，可切线路重算（hover 说明）。

**E. 版本时间线（轻量）**：倍率/参考价的历史版本用一条小时间线（`effective_from ~ to` 段），当前生效段高亮，hover 看当时值——「什么时候是多少」一目了然，替代干巴巴的列表。

### 6.4 `ChannelPricesDialog` 重定位为「绝对成本覆盖」（改现有组件）

现有弹窗基本保留，做三处小改，避免与倍率入口打架：
- **标题/说明改**：「绝对成本覆盖（优先级最高，仅少数特殊渠道用）」；顶部一条 `Callout`：「大多数模型用**渠道成本倍率**更省事；这里只在中转不按倍率计价时手填绝对成本。」
- **列表加来源徽标**：每条价用 `Badge` 标「覆盖」；同时把**倍率派生出来的成本**作为**只读行**混排展示（灰底 + 「派生」徽标 + 显示 `参考价 × 倍率`），让运维在一个列表看全「这个渠道每个模型最终成本从哪来」。派生行不可编辑，行尾「转为覆盖」按钮 → 预填当前派生值进 create 视图。
- **create/收口逻辑不动**：我们已做的 `findOverlappingChannelPrices` + `PriceOverwriteDialog`（收口/停用）原样服务于「覆盖」的创建。

### 6.5 交互黄金路径（两条，做顺做爽）

```mermaid
graph LR
    subgraph 改倍率["① 中转调倍率（高频）"]
      A1[渠道详情·成本倍率] --> A2[改默认倍率数字]
      A2 --> A3[实时影响预览表: 旧→新 差额 毛利 影响面]
      A3 --> A4[预览确认: 收口旧窗口摘要]
      A4 --> A5[一次生效·全渠道模型同步·toast点名渠道]
    end
    subgraph 补参考价["② 新模型补参考价（低频）"]
      B1[模型详情·参考价] --> B2[填分项+当前价差额]
      B2 --> B3[下游影响: 被N渠道引用]
      B3 --> B4[创建/收口确认]
    end
```

### 6.6 人性化细节清单（**细节决定成败**，逐条验收）

- [ ] 开始时间留空=「立即」、结束留空=「长期」，输入框 placeholder 明写（沿用已做）。
- [ ] 倍率输入：数字校验（>0）、支持小数、失焦格式化；旁标「相对上游官方价的倍数，如 1.15 = 官方价的 115%」。
- [ ] 金额显示统一走 `format.ts`：列表 `trimDecimal` 去尾零、差额 `roundPrice3`、成本 `formatUSDPrecise`（小额不被抹零）。
- [ ] 币种锁定：覆盖/倍率结果币种跟随参考价，UI 不让改错币种；不一致时禁用保存并提示。
- [ ] 状态齐全：加载 `Skeleton`、空态（「还没配参考价/倍率」+ 一键去配）、错误 `Alert`、pending 时按钮 `Spinner` + 禁用（沿用现有）。
- [ ] 确认文案具体：命中重叠时明说「收口于 {时间}」还是「停用」，并点名渠道/模型数量（复用已做的中文引导）。
- [ ] 未定价/缺参考价：灰显 + `⚠` + 深链去补，绝不静默失败或抛后端英文错误（复用 `overlapMessage` 那套友好文案思路）。
- [ ] 亏本护栏：毛利为负高亮 + 非阻断二次确认，避免手滑配出亏本。
- [ ] 倒填保护：`effective_from` 早于「现在」时黄色提示「会影响历史未结算请求成本」，默认引导用「现在」。
- [ ] 撤销心智：价格不可改，用「复制当前值/回滚到历史版本 = 新建一条」表达「撤销」，文案讲清。
- [ ] toast 点名主体：「已将渠道『openai_0.16』成本倍率更新为 1.15，17 个模型生效」。
- [ ] 无障碍：所有开关/输入 `aria-label`、`aria-invalid`；影响表可键盘滚动；焦点回落合理（沿用现有 Dialog 约定）。
- [ ] 一致性：React Query key 命名与失效沿用现有（见 §6.7），`AppLayout`/侧栏/`RangeFilter` 约定照旧。

### 6.7 前端 API 与 React Query keys（新增）

- [ ] `lib/api/modelReferenceCosts.ts`：`listModelReferenceCosts(modelId)` / `createModelReferenceCost` / `updateModelReferenceCost` / `pickCurrentModelReferenceCost` / `findOverlappingModelReferenceCosts`（照抄 `channelPrices.ts` 里我们加的重叠/取现价工具）。key：`["model-reference-costs", modelId]`。
- [ ] `lib/api/channelCostMultipliers.ts`：`listChannelCostMultipliers(channelId)`（默认 + 覆盖）/ `create` / `update` / `pickCurrentMultiplier(channelId, modelId?)`。key：`["channel-cost-multipliers", channelId]`。
- [ ] 预览用只读派生计算：前端有「参考价 + 倍率」即可本地算派生成本（`ScaleProviderCost` 的 TS 版小工具，逐分项 × 倍率 + `roundPrice3`），无需后端往返，预览即时。保存才走后端。
- [ ] 失效：改倍率成功后失效 `["channel-cost-multipliers", channelId]` + `["channel-prices", channelId]`（覆盖列表）；改参考价失效 `["model-reference-costs", modelId]` 及其下游渠道派生视图。

### 6.8 看板（小幅增强，非必须）

收入/成本/毛利本就来自 `cost_snapshots`（DEC-021/026 `radar`），快照结构兼容、口径不变。可选：渠道维度 breakdown 加「当前生效成本倍率」列；渠道详情「经营」区显示「倍率变更 → 成本/毛利」的时间标注。

---

## 7. 分阶段实施 + 验收门禁

- **阶段 0｜测试基建**：`sdkfixture`（`blackbox/sdkfixture/fixture.go`，勘探 `ChannelPriceID`/`fallbackChannelPriceIDs` 一带）加 helper：seed `model_reference_costs` + `channel_cost_multipliers`，并保留绝对覆盖 seed。✅ blackbox 能跑。
- **阶段 1｜后端计费核心（最关键）**：迁移 000076-000079 → sqlc → `ScaleProviderCost` → `resolveSettlementCost`（§3.1 优先级、attemptStart 解析）→ `cost_snapshots` 增列 → 恢复/敞口/路由候选同步。✅ 后端单测 + DB 集成 + 真实 e2e（§8）全绿；尤其**改倍率后新请求按新成本、历史请求成本不变**、**覆盖优先级**、**fallback 客价不变**。
- **阶段 2｜Admin 后端**：模型参考价 + 渠道成本倍率 CRUD（含窗口收口）。✅ 接口单测 + 校验（缺参考价告警、倍率非负、窗口不重叠）。
- **阶段 3｜前端（重点，详见 §6）**：先做 `PriceImpactTable`（差异可视化基座，§6.3）→ 渠道成本倍率 UI（§6.2，含实时影响预览 + 收口确认，主角）→ `ModelReferenceCostDialog`（§6.1，含下游影响）→ `ChannelPricesDialog` 重定位为「覆盖」+ 来源徽标（§6.4）→ §6.6 人性化细节清单逐条过。✅ `tsc`/`eslint` 绿；手测门禁：改 1 处倍率**保存前即见**逐模型 旧→新 差额 + 影响面计数 + 毛利红绿、亏本/未定价有护栏、确认文案点名收口/停用、派生/覆盖来源一眼分清。
- **阶段 4（可选）**：`上游倍率同步`（若中转暴露倍率接口，自动拉取写入 `channel_cost_multipliers`）；「把现有绝对 `channel_prices` 反推为 参考价+倍率」的迁移工具。

**回滚**：每个 up 配 down；`channel_prices` 全程不动，最坏情况关掉倍率解析、纯走绝对覆盖即回到现状。前端 git revert。

---

## 8. 测试方案（务必含真实 e2e）

类型：U=单测，I=DB 集成，E=blackbox 真实 SDK/Codex e2e。

- [ ] **U** `ScaleProviderCost`：逐分项乘倍率、NULL 分项保持 NULL、四舍五入到 (20,10) 与 `ScaleCustomerPrice` 同精度。
- [ ] **U/I** 成本解析优先级（§3.1）：绝对覆盖 > 逐模型倍率 > 渠道默认倍率 > 未定价报错。
- [ ] **I（核心）改倍率语义**：t1 建默认倍率 1.2、跑请求 R1；t2 新建倍率 1.15（收口旧窗口）；R1（settle 于 t2 之后）仍按 1.2 成本，t2 后新请求按 1.15。历史 `cost_snapshots` 不被改写。
- [ ] **I** 参考价缺失 → 该 (渠道,模型) 未定价：路由候选排除；结算报错。
- [ ] **I/E** fallback 客价不变（沿用 DEC-026 不变量）：命中更贵成本渠道，客户售价不变、毛利变薄、账单不变。
- [ ] **E（真实 Codex）** 渠道配「默认成本倍率」，发一族模型 → 各模型成本 = 各自参考价 × 该渠道倍率；改一次倍率 → 新请求全族生效。
- [ ] **I** 审计复算：`cost_snapshots` 含 `model_reference_cost_id/channel_cost_multiplier_id/cost_multiplier` + 冻结成本向量，可独立复算成本、不随后续改倍率漂移。
- [ ] **I** 恢复路径：settlement_recovery replay 成本与首次一致。

---

## 9. 边界与风险（考虑周全）

- **热路径改动风险**：`resolveSettlementCost` 在计费事务内，改动最敏感。缓解：保留绝对覆盖既有路径原样、倍率路径按 attemptStart 确定性解析、幂等复算仍以 `cost_snapshots` 冻结值为权威、§8 覆盖 U/I/E。
- **`cost_snapshots` FK 放开**：`cost_price_id` 改可空是对核心账务表的收敛式改动；对齐售价侧 `price_snapshots`（`000057`）已有先例，风险面已知。
- **回填时间戳/倒挂**：倍率/参考价的 `effective_from` 默认「现在或将来」，避免倒填重写历史未结算请求成本（与改价同风险，快照保护已结算请求）。倒填需谨慎、给管理台警示。
- **双入口混淆**：绝对覆盖（`channel_prices`）与倍率并存，靠 §3.1 优先级 + 前端来源徽标消歧；同一 (渠道,模型,时刻) 覆盖赢。
- **币种一致**：成本币种取参考价币种；倍率无量纲；覆盖行币种须与参考价一致（校验）。
- **NULL 分项**：参考价某分项 NULL → 成本该分项 NULL → 结算按 0 入账（`numericOrZero`，毛利偏保守），与现状一致。
- **精度**：参考 × 倍率四舍五入到 `NUMERIC(20,10)`，确定性、与售价侧同口径，幂等复算稳定。
- **授权/扣费不受影响**：成本不参与冻结/扣费，仅结算与敞口用，改动隔离在成本侧。

---

## 10. 受影响文件清单（对照打勾）

**后端新增**：`migrations/000076-000079`（+down）、`sql/queries/{model_reference_costs,channel_cost_multipliers}.sql`、`billing.ScaleProviderCost`、模型参考价 admin（service + `adminapi/model_reference_costs.go`）、渠道成本倍率 admin（service + `adminapi/channel_cost_multipliers.go`）。
**后端改**：`sql/queries/{channel_models,cost_snapshots}.sql`、`core/routing/router.go`、`lifecycle/{settlement,settlement_recovery,attempt_runner,attempt_runner_stream,cost_exposure}.go`、sqlc 重生成、`blackbox/sdkfixture/fixture.go` + seed、相关 `_test.go`。
**前端新增**：`components/pricing/PriceImpactTable.tsx`（差异可视化基座，§6.3，复用 `CostDelta`）、`components/channels/ChannelCostMultiplierDialog.tsx`（渠道成本倍率 + 实时影响预览，§6.2）、`components/models/ModelReferenceCostDialog.tsx`（§6.1）、`lib/api/{modelReferenceCosts,channelCostMultipliers}.ts`、`lib/billing/scaleProviderCost.ts`（前端本地派生算价，供即时预览）。
**前端改**：`components/channels/ChannelPricesDialog.tsx`（重定位「绝对覆盖」+ 派生/覆盖来源徽标 + 派生只读混排 + 「转为覆盖」，§6.4）、渠道详情页接入「成本倍率」tab/入口、模型详情页接入「参考价」入口、`lib/api/channelPrices.ts`（注释/来源字段）。已就绪可复用：`CostDelta`/`CostRow`/`PriceOverwriteDialog`/`findOverlappingChannelPrices`（本轮已实现）。

---

> 实施前请先审本文。认可后按 §7 阶段 0→1→2→3(→4) 推进；每阶段守住 ✅ 门禁。**核心不变量：渠道成本 = 上游参考价 × 渠道成本倍率，按 (渠道[/模型],时间) 锁定并快照冻结；改一次倍率全渠道模型同步生效，历史成本不漂移。**
