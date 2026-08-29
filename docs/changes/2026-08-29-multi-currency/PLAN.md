# 多货币支持改造落地计划

**文档版本**：v1.5（已实施，见同目录 REPORT.md）
**创建日期**：2026-08-29
**创建人**：chenhao
**关联文档**：《多货币支持需求文档》v1.0（本文档是其落地版本，术语按实际代码修正）
**状态**：待评审
**修订记录**：v1.1 新增 D11（冷启动/汇率缺失时序）与 S10 测试场景；黄金回归收敛为「现有精确断言不改全绿」，删除独立基线 diff 设施。v1.2 新增附录 D 蓝图修订清单，发现并处理与 admin DEC-021「不引汇率」禁令的正面冲突。v1.3 项目未上线，移除生产灰度（Shadow/Canary/GA）与上线签字流程，9.6 改为回滚与修正原则，P3 完成即可发布。v1.4 §12.C 七项决策全部确认落档：错标修正用当天汇率、存量 provider 全 CNY、DEC-021 删禁令、低余额按 USD 等值、缓冲线 5%、告警通道=admin 消息中心（MVP 先行）、API key 双层配置+运行时验证按钮。v1.5（2026-08-29）P0–P3 全量实施完成：迁移 000054–000058、fx 包、三 worker、汇率 admin API/UI、守卫 v2 + Go/SQL 等价性测试、结算钉汇率、余额解锁、报表清扫、前端双币、蓝图 12 份文档修订（新增 DEC-056）、开发库数据修正。实施勘误：开发库实际无 channel_prices 绝对成本与绝对售价，「错标售价」实体为 sale_price_ratio（5/25 基准 × 0.25 = 1.25/6.25 被当 USD 卖），修正 = ratio ÷ 当日汇率；starapi 属「名义 USD + 1:1 CNY 充值」类，按 D1 走倍率路径，recharge factor 1.0 → 1/6.742068（折入充值汇率，DEC-027 本义），真实成本不再高估 ~6.74 倍。详见 REPORT.md。**v1.6（2026-08-29，用户拍板修订 D1）**：v1.5 的「recharge factor 折入汇率」违背 §12.C.2「成本按渠道货币记账 + 动态汇率」决策且让汇率波动无法反映到毛利，予以撤销——倍率路径成本改按 **provider 结算币种**记账（基准价数值 × 价格倍率 × 充值倍率 = 原币金额），充值倍率回归 1.0 只承载名义额度折价；迁移 000059 使守卫分支 B/C 币种感知（跨币种 `sale_ratio × rate ≥ multiplier × factor`、缺汇率=违规）；routing/settlement/probe 三处成本快照币种覆写；随之 §2 范围表「倍率路径不动」、§5.2「分支 B/C 不动」、S3「字节级一致」的旧表述对 CNY provider 失效（USD provider 行为不变）。详见 REPORT.md D2 修订段与 data-fix-v2.sql

---

## 一、结论与范围

### 1.1 一句话方案

上游供应商按其真实计价货币记账（`providers.currency`，CNY/USD），成本数据存原始货币；新增 `exchange_rates` 汇率表由后台 worker 自动维护；汇率只在**比较**（margin 守卫）、**归一报表**（毛利/成本统计）和**展示**（双币显示）三个边界出现，**从不参与任何一笔记账**。

### 1.2 两条河原则（本方案的根基）

系统里有两条独立资金流，各自用各自的币种流动，永不互相换算：

- **客户河（USD）**：用户充值 USD → 每笔请求扣 USD → 余额 USD。全程无汇率。
- **上游河（原币）**：给 CNY 供应商充值 CNY → 每笔请求扣 CNY → 余额 CNY。全程无汇率。

汇率波动因此**不可能污染任何账本**；它只影响毛利估算值。这是所有容错设计从容的前提。

### 1.3 范围内 / 范围外

| | 说明 |
|---|---|
| ✅ 范围内 | 绝对成本路径（`channel_prices`）多货币；provider 余额多货币；汇率基础设施；margin 守卫跨币种；成本报表 USD 归一；admin 双币展示 |
| ❌ 范围外 | 客户侧（`user_balances` / `ledger_entries` / console 计价）——保持 USD 不动 |
| ✅ 范围内（v1.6 修订） | 倍率路径（`model_prices × channel_cost_multipliers × channel_recharge_factors`）——成本币种 = provider 结算币种，守卫 B/C 分支币种感知（000059），见 D1 修订 |
| ❌ 范围外 | CNY/USD 以外的货币——架构预留，本期不实现 |

---

## 二、现状事实清单（代码调查结论，2026-08-29 核实）

改造以下列事实为基线，评审时如发现事实变化需同步修订本文档。

| # | 事实 | 位置 |
|---|---|---|
| F1 | **不存在 `provider_costs` 表**。成本两条路径：绝对覆盖 `channel_prices`（自带 currency 自由文本字段）；倍率路径 `model_prices × channel_cost_multipliers × channel_recharge_factors` | `migrations/000014` `000026` `000012` `000015` |
| F2 | `channel_recharge_factors` 注释明确「已把汇率+充值优惠折进去」，即倍率路径的 CNY 问题已用**充值时刻汇率**解决（历史成本法） | `migrations/000015` 头注 |
| F3 | margin 守卫是 **Go + SQL 双实现**：Go 侧 `sale.Currency != cost.Currency` 直接报错；SQL 触发器函数分支 A 把 `mp.currency <> cp.currency` 判为违规，触发器挂在 9+ 张配置表上（DEFERRABLE INITIALLY DEFERRED） | `internal/core/billing/margin.go:45`、`migrations/000041` |
| F4 | `provider_balances` 主键已是 `(provider_id, currency)`，`provider_ledger_entries` 带 currency——schema 层天然支持多币种；但 admin 服务层写死 USD（`currency must be USD`、`BalanceUSD`） | `migrations/000035` `000037`、`internal/service/admin/providerbalance/providerbalance.go` |
| F5 | 结算扣 provider 余额用的是 **cost snapshot 的 currency**（库层不限 USD） | `internal/app/*/lifecycle/settlement.go`（DebitUsageWithQueries） |
| F6 | `cost_snapshots` / `price_snapshots` 只有单一 currency 字段，`UNIQUE(request_record_id)`，无汇率信息 | `migrations/000017` `000027` |
| F7 | 路由候选查询按时间取 `LIMIT 1` 选成本行，**不按 currency 过滤**；排他约束键含 currency，同渠道×模型的 CNY 与 USD 行可同时启用并存 | `sql/queries/gateway/channel_models.sql` |
| F8 | 报表 SQL 大面积硬编码 `currency = 'USD'`（成本聚合、营收、overview、dashboard、console 钱包等），出现非 USD 行会**静默漏数据** | `sql/queries/admin/model.sql:923,930` 等，全量以 `rg "currency = 'USD'"` 为准 |
| F9 | worker 基础设施现成：`workers.Unit{Name, RunOnce}` + `Runner` 轮询，注册在 `bootstrap/worker_server.go` | `internal/app/workers/runner.go` |
| F10 | 全链路 big.Rat + NUMERIC(20,10)，单次舍入范式（`scaleRate`） | `internal/core/billing/scale.go` `numeric.go` |
| F11 | `providers` 表无 currency 字段 | `migrations/000006` |
| F12 | admin 前端：`ProviderFormDialog` 无币种字段；`ProviderBalanceDialog` 币种 readOnly "USD"；设价面板 `ModelSupplyMarginPanel` 毛利=裸减法（无币种概念）；其数据源 `GetModelOpsChannels` 不返回币种 | `unio-admin/src/components/...`、`sql/queries/admin/model.sql` ~1008 |
| F13 | DB 触发器测试范式现成：`DATABASE_URL` 连真实 PG，事务内 `SET CONSTRAINTS ALL IMMEDIATE` 断言约束名 `ck_non_negative_margin`，测试后回滚 | `internal/platform/store/sqlc/margin_guard_test.go` |
| F14 | **存量数据事故**：部分模型售价数值实为 CNY 却标 USD（如 1.25/6.25），需求文档 §1.2 已记录 | `model_prices` 存量数据 |

---

## 三、关键设计决策（D1–D11）

> 每条决策都是评审对象；实现与决策冲突时，以修订后的决策为准。

**D1｜成本一律按 provider 结算币种记账，动态汇率覆盖两条成本路径。**（v1.6 修订，取代原「动态汇率只作用于绝对成本路径」）
绝对路径：`channel_prices` 按 provider 币种存 token 单价（D3 触发器保证一致）。倍率路径：基准价**数值** × 价格倍率 × 充值倍率 = **provider 币种金额**（如 5 × 0.12 × 1.0 = 0.6 CNY）——充值倍率只承载「1 单位上游名义额度花多少原币」的折价（1:1 CNY 充值 = 1.0），**禁止把汇率折进倍率**（原 v1.0–v1.5 方案，会冻结汇率使波动无法反映到毛利，且与账本原币记账矛盾）。两条路径的守卫比较/毛利/报表统一按当日汇率显式折算，换算三边界不变。*会计视角：provider 账本、上游名义余额（1:1 时数值相同）、成本快照三者同币种，对账零换算。*

**D2｜记账不换汇，比较/报表/展示才换汇。**
三个换汇点：① 路由与 margin 守卫比较（临时，用完即弃）；② 结算时计算毛利并把所用汇率**钉进** `cost_snapshots`（唯一落库的汇率）；③ UI 双币显示（临时，用最新汇率）。永不换汇的地方：`channel_prices` 存储值、客户全链路、provider 充值/扣款/余额。

**D3｜一渠道一币种。**
强制 `channel_prices.currency == providers.currency`（触发器 + 应用层双重校验）。由此 F7 的路由多币种并存隐患自然消除，路由查询**不需要改**。provider 的 currency 在产生任何 `channel_prices` / `provider_ledger_entries` / `provider_balances` 之后**不可修改**。

**D4｜快照钉汇率 + 冗余 USD 列。**
`cost_snapshots` 新增 `fx_rate`（quote per USD，如 7.1700）、`fx_rate_date`、`total_cost_amount_usd`。USD 行：fx 列为 NULL 且 usd 列 = 原币列（CHECK 约束强制）。冗余列是刻意的：给报表提供 O(1) 聚合口径，同时给交叉检查提供「两种表示必须对得上」的审计抓手（见 I3）。

**D5｜守卫比较用乘法，不用除法。**
`sale_usd × rate ≥ cost_cny`，纯乘法零舍入（Go 侧 big.Rat 本就精确，SQL 侧规避 NUMERIC 除法精度问题）。汇率缺失时**配置写入直接拒绝**（保守失败，出错信息说明缺哪对汇率）。

**D6｜毛利判定是时变的：写入时硬卡，汇率变动后复查告警。**
配置写入时守卫用最新可用汇率硬校验；此后汇率漂移可能让存量配置转为负毛利——由 worker 在每次汇率更新后跑全量复查，**告警而不硬卡**（不自动停用渠道，人工决策）。`exchange_rates` 表上**不挂** margin 触发器，否则汇率更新本身会被存量违规卡死。守卫与复查共用同一个违规查询（实现为视图 `margin_violations_current`），保证口径永不分叉。

**D7｜汇率源四层容错。**
①任何请求路径不同步调外部 API，只读本地表；②多源冗余（ExchangeRate-API 主源 → Frankfurter → open.er-api.com）；③入库前合理性校验：对前值跳变 >5% 拒收告警、CNY 绝对区间 [5,10] 硬校验——**防错数据优先级高于防宕机**；④陈旧分级：<24h 正常，1–3 天低优告警，>3 天高优告警要求人工介入；`source='manual'` 手工兜底随时可录。**结算永不因汇率新鲜度失败**，取最近可用值钉入快照。

**D8｜成本报表统一读 `total_cost_amount_usd`。**
所有 `SUM(cs.total_cost_amount) ... WHERE cs.currency='USD'` 形态改为 `SUM(cs.total_cost_amount_usd)`（历史口径 = 各笔发生时钉住的汇率，可审计可复算）。营收侧（`ledger_entries`，客户永远 USD）**不改**，避免过度修改。

**D9｜客户侧零改动。**
`user_balances`、`ledger_entries`、授权冻结、console 全部不动。回归测试保证字节级不变（见 §8.6）。

**D10｜精度与一致性。**
汇率 NUMERIC(20,10)；换算 big.Rat 精确运算、仅在最终落库时单次舍入到 scale 10（沿用 `scaleRate` 范式）。单笔请求内路由比较与结算钉值可能跨越一次汇率刷新——可接受，**以钉进快照的值为最终口径**。

**D11｜冷启动与汇率缺失的时序自洽。**
「请求先于汇率到达」不可能触及资金路径，且由系统自身强制而非运维纪律：无汇率 → M5 守卫拒绝任何 CNY `channel_prices` 写入 → 不存在可路由的 CNY 渠道 → 所有请求都是纯 USD 候选，全程不碰汇率表。运行期兜底（仅汇率行被人为删除等运维事故可触发）：路由候选查不到汇率即剔除并计指标，流量回落 USD 渠道；无其他候选时请求以「无可用渠道」干净失败——**宁可少赚一笔，不算错一分**。汇率表只追加不删除，结算取最近可用值（最坏是陈旧，D7/S6 覆盖），故「路由时有、结算时没了」不存在。进程冷启动缓存为空时首查直接读本地库，请求路径对外部 API 依赖为零。

---

## 四、数据库改造

迁移编号顺延（当前最新 000052），每个 migration 必须有可用的 down。

### M1｜`exchange_rates` 表（新迁移）

```sql
CREATE TABLE exchange_rates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    base_currency  text NOT NULL,             -- 'USD'（固定基准）
    quote_currency text NOT NULL,             -- 'CNY'
    rate numeric(20,10) NOT NULL,             -- 1 base 兑多少 quote（如 7.1700）
    rate_date date NOT NULL,                  -- 汇率所属日
    source text NOT NULL,                     -- 'exchangerate-api' / 'frankfurter' / 'er-api' / 'manual'
    fetched_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_exchange_rates_rate_positive CHECK (rate > 0),
    CONSTRAINT ck_exchange_rates_pair CHECK (base_currency <> quote_currency),
    CONSTRAINT uq_exchange_rates UNIQUE (base_currency, quote_currency, rate_date, source)
);
CREATE INDEX idx_exchange_rates_lookup
    ON exchange_rates (base_currency, quote_currency, rate_date DESC, fetched_at DESC);
```

消费口径唯一：`ORDER BY rate_date DESC, fetched_at DESC LIMIT 1`（手工行只要 rate_date 更新即自然生效）。数据量每天几行，永久保留，不设归档。

### M2｜`providers.currency`

```sql
ALTER TABLE providers ADD COLUMN currency text NOT NULL DEFAULT 'USD';
ALTER TABLE providers ADD CONSTRAINT ck_providers_currency CHECK (currency IN ('USD','CNY'));
```

盘点已完成（§12.C.2）：**存量 provider 全部为 CNY**，迁移回填 DEFAULT 'USD' 后由数据脚本（或迁移内 UPDATE）统一改标 'CNY'。将来扩币种 = 扩 CHECK + 补汇率对 + 应用层白名单，一次迁移即可。

### M3｜`cost_snapshots` 加汇率列

```sql
ALTER TABLE cost_snapshots
    ADD COLUMN fx_rate numeric(20,10),
    ADD COLUMN fx_rate_date date,
    ADD COLUMN total_cost_amount_usd numeric(20,10);

UPDATE cost_snapshots SET total_cost_amount_usd = total_cost_amount;  -- 存量全 USD

ALTER TABLE cost_snapshots
    ALTER COLUMN total_cost_amount_usd SET NOT NULL,
    ADD CONSTRAINT ck_cost_snapshots_fx CHECK (
        (currency = 'USD' AND fx_rate IS NULL AND fx_rate_date IS NULL
             AND total_cost_amount_usd = total_cost_amount)
     OR (currency <> 'USD' AND fx_rate IS NOT NULL AND fx_rate > 0
             AND fx_rate_date IS NOT NULL)
    );
```

分项 USD 金额不落库（可由 `分项原币 ÷ fx_rate` 复算），只落总额，控制列膨胀。

### M4｜渠道价格币种一致性触发器（D3）

```sql
CREATE FUNCTION assert_channel_price_currency_matches_provider() RETURNS trigger AS $$
DECLARE pc text;
BEGIN
    SELECT p.currency INTO pc FROM providers p
    JOIN channels c ON c.provider_id = p.id WHERE c.id = NEW.channel_id;
    IF NEW.currency <> pc THEN
        RAISE EXCEPTION USING ERRCODE='23514',
            CONSTRAINT='ck_channel_price_currency_matches_provider',
            MESSAGE=format('channel_prices.currency=%s 与 provider.currency=%s 不一致', NEW.currency, pc);
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
-- 挂 channel_prices INSERT/UPDATE；providers.currency 的不可变性在应用层拒绝
-- （存在 channel_prices/ledger/balances 任一引用即禁止改），触发器兜底可后补。
```

### M5｜margin 守卫函数改造（`assert_non_negative_margins` 新版本迁移）

- 抽出违规查询为视图 `margin_violations_current`（守卫触发器与 worker 复查共用，D6）。
- **分支 A / D（绝对成本）**：删除「币种不同即违规」，改为 LATERAL 取最新汇率，比较 `rates.sale * COALESCE(fx.rate, 1) < rates.cost`；跨币种且汇率缺失 → 视为违规（错误信息注明「缺少汇率对」）；违规信息带上所用 `fx.rate` 与 `rate_date` 方便定位。

```sql
LEFT JOIN LATERAL (
    SELECT er.rate, er.rate_date
    FROM exchange_rates er
    WHERE er.base_currency = mp.currency        -- 售价侧固定 USD
      AND er.quote_currency = cp.currency
    ORDER BY er.rate_date DESC, er.fetched_at DESC
    LIMIT 1
) fx ON cp.currency <> mp.currency
...
AND (
    mp.pricing_unit <> cp.pricing_unit
    OR (cp.currency <> mp.currency AND fx.rate IS NULL)
    OR rates.sale * COALESCE(fx.rate, 1) < rates.cost
)
```

- **分支 B / C（倍率路径）**：不动（两侧共用同一 `model_prices` 行，币种恒同，D1）。
- `exchange_rates` 表**不挂**守卫触发器（D6）。

### M6｜存量错标数据修正（数据迁移，非 schema）

口径已决策（§12.C.1）：原 CNY 数值 ÷ **执行日当天汇率** = 目标 USD 售价（开发数据、仅观测）。执行：单事务修正（守卫为 deferred 约束，提交时整体复验）；修正脚本进版本库，带前后对照输出。

---

## 五、后端改造（unio-gateway）

### 5.1 新增 `internal/core/fx` 包

| 组件 | 职责 |
|---|---|
| `fx.Service` | `LatestRate(quote string) (Rate, error)`：读库 + 进程内缓存（TTL 1–5 分钟）；`Rate{Value *big.Rat, Date, Source, FetchedAt}` |
| `fx.Convert` | 原币 → USD：`amount ÷ rate`，big.Rat 精确、最终单次舍入 scale 10；比较场景暴露 `MulRate`（乘法路径，D5） |
| `fx.Staleness` | 汇率年龄分级（正常 / >24h / >72h），供指标与告警 |
| 拉取器 | 多源客户端链（主→备→备），合理性校验（跳变 >5% 拒收、区间 [5,10]），只负责写表 |

配置（已决策，§12.C.6）：API key **双层解析**——`app_settings` 系统设置表优先（key 建议 `gateway.exchange_rate_api_key`，注册进 `appsettings.DefaultRegistry()`），设置表未配置时回退环境变量 `EXCHANGE_RATE_API_KEY`（`.env.example` / `.env.dev` / `.env.test` / `.env.prod` 四处同步）；现有 key 继续使用。admin 系统设置页增加「**验证 API Key**」按钮：即时调用汇率源并回显返回的当前汇率，支持运行时改 key 后当场验证。

### 5.2 billing 包

- `types.go`：`ProviderCostSnapshot` / `ProviderCost` 增加 `FxRate pgtype.Numeric`、`FxRateDate`（USD 时 Invalid）。
- `margin.go`：`normalizedSaleCostPairs` 放宽同币种断言 → 接受可选汇率参数；跨币种时成本侧不动、售价侧乘 rate（或成本除 rate，统一用乘法口径）；无汇率且跨币种 → 显式错误 `ErrMissingFxRate`。
- `cost_ratio.go`（路由比价）同步支持跨币种。
- 现有测试「CNY vs USD 必须报错」语义翻转为「无汇率才报错」，用例保留并改写（见 §8.1）。

### 5.3 routing

- `router.go` 构造候选时：向 `fx.Service` 解析一次汇率（每次请求解析一次，贯穿该请求所有候选），传入 margin 校验与 cost ratio。
- 防御性检查：候选 `cost_currency` 既非 USD 又查不到汇率 → 候选剔除并计数指标（不 panic、不 500）。

### 5.4 settlement（资金主链路，改动最谨慎的地方）

- 计算 `ProviderCost`：分项与总额保持**原币**（provider 账本扣原币，D2）；同时算 `total_cost_amount_usd = total ÷ rate`（单次舍入）；写 `cost_snapshots` 带 fx 三列。
- `DebitUsageWithQueries` 维持用 snapshot 的 currency/金额——**不改语义，天然扣原币**（F5）。
- 毛利/观测指标改用 usd 列。
- **恢复链路铁律**：settlement recovery 重放**只从既有 `cost_snapshots` 行取数**，绝不重新解析「今天的汇率」（否则重放日≠发生日时金额漂移，账就崩了）。审查 `settlement_recovery_jobs` 全路径确认此性质并加测试（§8.3-R）。
- probe 扣款（`providerledger/probe.go`）：随成本快照自然变为原币，纳入测试矩阵。

### 5.5 provider 余额与 admin API

- `providerbalance` 服务：删除 `currency must be USD` 硬校验，改为「必须等于该 provider.currency」；`BalanceUSD` 改为 `Balance(providerID)` 返回原币余额 + 币种（USD 等值由展示层算）。
- provider CRUD：创建时必填 currency；有账务/价格引用后禁止修改（D3）。
- 新增 admin 端点：`GET /exchange-rates`（最新与历史）、`POST /exchange-rates`（手工录入，source='manual'，校验同拉取器）。
- ops 查询（`GetModelOpsChannels`、channel 侧同类）：返回成本的**原币值 + 币种 + USD 折算值 + 所用汇率/日期**（JOIN exchange_rates，后端换算保证与守卫同源，D5/D6）。

### 5.6 worker（三个新 Unit，注册进 `worker_server.go`）

| Unit | 频率 | 职责 |
|---|---|---|
| `fx-rate-sync` | 每 6h（月均 120 请求，配额无忧） | 多源拉取 → 校验 → 入库；失败重试与降级；产出指标 |
| `fx-margin-recheck` | 每次 sync 成功后 + 每日兜底 | `SELECT * FROM margin_violations_current` → 每条违规打结构化日志 + 指标（含渠道/模型/分项/所用汇率），**只告警不改配置**（D6） |
| `fx-reconciliation` | 每日 | 跑 §9.1 全部不变量（前一日窗口），结果落 `reconciliation_runs` 表 + 违规告警 |

### 5.7 报表 SQL 清扫（D8）

执行步骤（Phase 3 第一项，先于任何 UI）：

1. `rg "currency = 'USD'" unio-gateway/sql/` 产出全量清单（已知：`admin/model.sql` 营收/成本聚合、`admin/provider.sql`、`admin/overview.sql`、`admin/requests.sql`、console usage/wallet 相关）。
2. 逐条分类：**成本聚合**（cost_snapshots）→ 改读 `total_cost_amount_usd` 去掉币种过滤；**营收/客户侧**（ledger_entries）→ 不动（D9）；**provider 余额列表**→ 改为返回原币+币种（+展示层折算）。
3. 每条改动配 §9.2 对账任务的前后一致性验证（改造前后对 USD-only 数据必须输出完全相同数字）。

---

## 六、前端改造

### 6.1 unio-admin

| 文件 | 改动 |
|---|---|
| `lib/format.ts` | 新增 `formatMoney(amount, currency)` 与 `formatDualCurrency(amount, currency, usdEquiv)` → `¥7.00 (≈ $0.98)`；现有 `formatUSD*` 保留给纯 USD 场景 |
| `components/providers/ProviderFormDialog.tsx` | 创建时必选币种（USD/CNY）；编辑态按后端规则禁用 |
| `ProviderBalanceDialog.tsx` / `lib/api/providerBalance.ts` | 币种跟随 provider（只读显示，不再写死 USD）；金额标签随币种；提交 body 用 provider 币种 |
| `ProviderBalanceDisplay.tsx` / `ProviderLedgerSection.tsx` / `lib/api/providersOps.ts` | `balance_usd` 类字段改为 `balance + currency + balance_usd_equiv`；双币显示 |
| `components/models/ModelSupplyMarginPanel.tsx` | 成本列以 **USD 折算值为主、原币为辅**；毛利用 USD 口径计算；面板脚注「CNY 成本按 {date} 汇率 {rate} 折算」；数据来自 5.5 的 ops 查询（换算在后端） |
| `ChannelPricesDialog.tsx` | 币种继承 provider 只读显示；录入 CNY 单价时**实时显示 USD 等值**（防呆，杜绝 1:1 错标再发生） |
| `ChannelCostCalculator.tsx` / `ModelPriceDetailsHoverCard.tsx` | 同口径双币显示 |
| `DashboardPage` 等 `currency !== 'USD'` 过滤点 | 随 5.7 后端字段调整 |
| 新增：汇率管理页 | 最新汇率卡片（值/日期/来源/年龄）、历史列表、手工录入弹窗、对账状态（§9.2 结果） |

### 6.2 unio-console

**零功能改动**（D9）。仅做回归验证：所有 `*_usd` 字段的数值在改造前后一致。

---

## 七、观测与告警接线

§9.5 告警矩阵所需的指标与日志，随各阶段代码一并交付。告警落地通道已决策（§12.C.3）：**admin 消息中心**（站内消息 + 顶栏未读提醒，MVP 随 P0 先行交付；fx worker 的告警直接写消息表）+ Prometheus 指标。

**指标（Prometheus 口径）**

| 指标 | 类型 | 用途 |
|---|---|---|
| `fx_rate_age_hours{pair}` | gauge | 陈旧分级告警的数据源（>24h / >72h） |
| `fx_fetch_total{source,outcome}` | counter | 拉取成功/失败/降级到备源的分布 |
| `fx_rate_rejected_total{reason}` | counter | 合理性校验拒收（jump / band / invalid） |
| `fx_settlement_stale_rate_total` | counter | 结算使用了 >24h 旧汇率的笔数（周报回顾） |
| `margin_recheck_violation_count` | gauge | 复查发现的存量负毛利配置数（>0 即 P1） |
| `margin_recheck_thin_count` | gauge | 毛利低于缓冲线的配置数（P2） |
| `reconciliation_check_status{check}` | gauge | I1–I6 各项 0/1，对账页与告警共用 |
| `routing_candidate_dropped_fx_total` | counter | 因缺汇率被剔除的路由候选（5.3 防御分支） |

**结构化日志**：recheck 与对账的每条违规输出独立日志行，字段含 `channel_id / model_id / component / sale / cost / fx_rate / fx_rate_date / check_name`，保证告警可直接定位到配置行，无需再查库。

**现有观测复用**：请求生命周期已有 `margin_guard_triggered` 观测（路由候选被守卫拒绝），跨币种改造后其标签补充 `cost_currency`，用于区分「真亏本」与「缺汇率」两类拒绝。

---

## 八、测试方案

> 原则：资金链路的每一处改动，必须同时有「新行为的正向用例」和「旧行为的不变性用例」。凡涉及金额断言，一律精确到 NUMERIC(20,10) 全部位数，禁止「约等于」断言。

### 8.1 Go 单元测试（无 DB，`go test ./...`）

**`internal/core/fx`（新增 `fx_test.go`）**

| 用例 | 断言 |
|---|---|
| 精确换算表驱动 | `42 CNY ÷ 7.0 = 6.0000000000`；`1 CNY ÷ 7.17` 落库值等于 big.Rat 精确值单次舍入 scale 10 的结果（与 float64 路径对照，证明无浮点污染） |
| 单次舍入 | 换算→舍入一次 与 分步舍入 的差异用例：结果必须等于前者 |
| 乘除等价 | `sale × rate ≥ cost` 与 `sale ≥ cost ÷ rate` 在 big.Rat 下判定一致（随机 1000 组） |
| 合理性校验 | 跳变 5.01% 拒收、4.99% 接受；rate=0/负数/NaN 拒收；CNY 越界 [5,10] 拒收 |
| 降级链 | 主源失败→备源成功；全失败→返回错误不写库；staleness 分级边界（23h59m / 24h01m / 72h01m） |

**`internal/core/billing`（扩展 `margin_test.go`、`cost_ratio_test.go`、`scale_test.go`）**

| 用例 | 断言 |
|---|---|
| 跨币种盈利 | sale 2.5/12.5 USD vs cost 7/35 CNY @7.0 → 无违规 |
| 跨币种亏本 | 同上 @2.0（极端升值）→ 逐分项报违规，Sale/Cost 字段为归一后精确值 |
| 边界相等 | `sale × rate == cost` 精确相等 → 无违规（≥ 语义） |
| 缺汇率 | 跨币种 + rate 缺失 → `ErrMissingFxRate`（旧「币种不同必报错」用例改写为此语义，**保留原用例文件位置便于评审 diff**） |
| 同币种回归 | 全部现有 USD 用例不改一字节、必须全绿 |
| 七分项覆盖 | cache_* / reasoning 分项在跨币种下的 NULL 回退语义与同币种一致 |
| Fast 档 | Fast 独立价格对跨币种校验 |
| 属性测试 | 随机 (价格向量, 汇率) 1000 组：`ValidateNonNegativeMargin` 判定 == 手写 big.Rat 真值函数判定 |

### 8.2 SQL/DB 测试（`DATABASE_URL` + 事务回滚 + `SET CONSTRAINTS ALL IMMEDIATE`，沿用 F13 范式）

**`margin_guard_test.go` 新增用例**

| 用例 | 断言 |
|---|---|
| CNY 成本 + 汇率存在 + 盈利 | 提交成功 |
| CNY 成本 + 汇率存在 + 亏本 | 拒绝，约束名 `ck_non_negative_margin`，DETAIL 含所用 rate |
| CNY 成本 + 无汇率 | 拒绝，错误信息含「缺少汇率对」 |
| 守卫取最新汇率 | 插两天汇率，断言用的是 rate_date 较新那条 |
| manual 优先 | 同 rate_date 下 fetched_at 较新的 manual 行生效 |
| 倍率路径不受影响 | 分支 B/C 现有用例全绿 |
| Fast 分支跨币种 | 同分支 A 三态 |
| 汇率更新不触发守卫 | 向 exchange_rates 插入使存量配置转负的汇率 → 插入成功（D6），复查视图能查出该违规 |

**新增 `channel_price_currency_test.go`**：币种与 provider 不一致 → 拒绝；一致 → 通过；provider 改币种被应用层拒绝（有引用时）。

**新增 `cost_snapshots_fx_test.go`**：CHECK 约束四象限（USD±fx 列、CNY±fx 列）。

**migration 循环测试**：新迁移在干净库上 up → down → up 三段全部成功。

### 8.3 结算端到端集成测试（场景矩阵）

每个场景断言四件套：①客户 `ledger_entries` 金额/币种；②provider `provider_ledger_entries` 金额/币种；③`cost_snapshots` 全列（含 fx 三列）；④`price_snapshots` 不变性。

| # | 场景 | 关键断言 |
|---|---|---|
| S1 | USD provider 绝对成本（**黄金回归**） | 与改造前基线输出**字节级一致**；fx 列为 NULL；usd 列 = 原币列 |
| S2 | CNY provider 绝对成本 | 客户扣 USD；provider 扣 CNY（数额=CNY 单价×用量，与汇率无关）；快照钉 rate/date；`usd = total ÷ rate` 精确到 10 位 |
| S3 | 倍率路径渠道 | 输出与改造前字节级一致（D1 不动性证明） |
| S4 | CNY + Fast 档 | Fast 成本对同样钉汇率 |
| S5 | CNY + 长上下文倍率 | 倍率作用于原币、换算发生在总额，两种顺序结果一致性断言 |
| S6 | 陈旧汇率结算 | 表里只有 3 天前汇率 → 结算成功，钉旧值，告警指标 +1 |
| S7-R | **恢复重放** | 结算写完快照后模拟崩溃 → recovery 重放：期间插入新汇率，重放结果必须用快照里钉住的旧 rate，产出与未崩溃路径完全一致（复用现有 `settlement_recovery_jobs_test.go` 崩溃重放测试族，仅新增用例，成本约几十行） |
| S8 | probe 扣款 | CNY 渠道探测成本扣 CNY |
| S9 | 授权冻结 | 冻结金额（USD 售价侧）在 CNY 渠道下与 USD 渠道逻辑一致 |
| S10 | **冷启动/汇率缺失**（D11） | 配置期：无汇率时创建 CNY 渠道价格被守卫拒绝；运行期：删除汇率行后 CNY 候选被剔除（`routing_candidate_dropped_fx_total`+1）、流量落 USD 渠道；仅剩 CNY 候选时请求以「无可用渠道」失败，**不产生任何账务写入** |

### 8.4 worker 测试

fetch：成功写行 / 主源挂走备源 / 全挂不写库且告警 / 垃圾数据被合理性校验拒收（rate=0、跳变 8%）。
recheck：构造「汇率变动使存量配置转负」→ 视图查出、告警产出、配置未被改动。
reconciliation：注入一条人为不一致（测试事务内改坏 usd 列）→ 对账任务标红。

### 8.5 前端测试

`formatMoney/formatDualCurrency` 单测（含 0、负数、10 位小数截断）；`ModelSupplyMarginPanel` 用 fixture 断言 USD 主值、原币辅值、汇率脚注渲染；`ChannelPricesDialog` 实时换算显示；`ProviderBalanceDialog` 币种跟随 provider。

### 8.6 回归护栏（金流不变性，合并前强制）

1. **黄金回归（收敛版）**：现有全部测试（Go + DB）的金额断言**一字不改、必须全绿**（唯一允许的改写是 §8.1 的「缺汇率」语义翻转）。金额列为 NUMERIC(20,10) 精确十进制，断言本来就是全位数相等，因此**不建**独立的「main vs 分支落库 diff」基线设施——那是重复建设。S1/S3 场景用例即 USD 链路不变性在集成层的证明。
2. console 的 `*_usd` API 响应对同一 seed 数据前后一致（快照对比）。

### 8.7 staging 手工验收清单（Phase 门槛用）

- [ ] 创建 CNY provider → 渠道 → 录入 CNY 成本（弹窗实时显示 USD 等值）
- [ ] 设价面板显示 USD 折算成本 + 汇率脚注，毛利与手算一致
- [ ] 打真实请求 → 四件套落库检查（S2 口径手工复核一笔）
- [ ] provider 余额页：CNY 余额 + ≈USD 双显；充值 CNY 成功；充 USD 被拒
- [ ] 手工录汇率 → 5 分钟内（缓存 TTL）设价面板/守卫用新值
- [ ] 断网模拟：停 worker 3 天 → 告警升级链路触发、结算不受影响
- [ ] 对账页全绿

---

## 九、交叉检查方案

> 思路：**同一笔钱至少两条独立路径可以互相验证**。守卫有 Go/SQL 双实现→互验；换算有原币/USD 双列→互验；分录与快照→互验。任何一路被改坏，另一路当天报警。

### 9.1 账务不变量（SQL，随时可跑，对账任务每日跑）

**I1｜成本分录 = 成本快照**（每笔已结算请求）

```sql
SELECT cs.request_record_id
FROM cost_snapshots cs
JOIN provider_ledger_entries ple ON ple.request_record_id = cs.request_record_id
    AND ple.entry_type = 'usage_debit'
WHERE ple.amount <> cs.total_cost_amount OR ple.currency <> cs.currency;
-- 期望 0 行
```

**I2｜快照汇率完备性**：M3 的 CHECK 约束为主防线；对账任务补跑同语义 SELECT 作为「约束被误删」的哨兵。

**I3｜换算冗余一致**（D4 的审计抓手）

```sql
SELECT id FROM cost_snapshots
WHERE currency <> 'USD'
  AND total_cost_amount_usd <> round(total_cost_amount / fx_rate, 10);
-- 期望 0 行（round 口径与 Go 侧单次舍入一致，需一次性校准验证）
```

**I4｜余额 = 分录累计**（按 provider × currency）

```sql
SELECT pb.provider_id, pb.currency
FROM provider_balances pb
LEFT JOIN (
    SELECT provider_id, currency,
           sum(CASE WHEN entry_type LIKE '%debit%' THEN -amount ELSE amount END) AS total
    FROM provider_ledger_entries GROUP BY 1, 2
) agg USING (provider_id, currency)
WHERE pb.balance <> COALESCE(agg.total, 0);
-- 期望 0 行。符号口径为草案，实施时对齐 entry_type 实际枚举（usage_debit/probe_debit/adjustment_*）；
-- 另抽样验证分录链 balance_after = balance_before ± amount 逐条衔接。
```

**I5｜客户侧复算抽检**：随机抽 N 笔，`price_snapshots` 单价 × usage tokens 重算金额 == `ledger_entries` 扣费金额（客户链路未动的持续证明）。

**I6｜汇率表健康**：每启用币种对——最新行年龄 <24h；相邻两日 rate 变动 ≤5%；值在区间内；无空洞天数超阈值。

### 9.2 每日对账任务（`fx-reconciliation` worker）

- 窗口：前一自然日（UTC）。跑 I1–I6，结果写 `reconciliation_runs(run_date, check_name, status, detail)`。
- 任何一项非 0 → 高优告警；admin 汇率管理页展示最近 7 天对账矩阵（绿/红）。
- 对账任务随服务常开、告警驱动；正式启用 CNY 渠道的初期建议每日查看一次对账页。

### 9.3 双实现等价性检查（Go 守卫 vs SQL 守卫）

CI 增加 DB 测试：同一组随机 fixture（价格向量 × 汇率 × 币种组合，含边界相等），分别送入 Go `ValidateNonNegativeMargin` 与 SQL `margin_violations_current` 视图，**判定结果必须完全一致**，不一致即构建失败。这是防「两套守卫悄悄漂移」的永久闸门（历史上 `margin.go` 与 000041 就是手工保持同步的，跨币种改造让漂移风险上升，必须自动化）。

### 9.4 历史复算抽检（每周）

随机抽 N=100 笔历史已结算请求：仅凭 `cost_snapshots`（单价、用量倍率、钉住的 fx_rate）从头重算总额与 usd 列，字节级比对落库值。保证「任何一笔历史账单可离线复算」这一审计承诺长期成立。

### 9.5 告警矩阵

| 事件 | 级别 | 动作 |
|---|---|---|
| 汇率拉取连续 2 次失败 | P3 | 值班知悉 |
| 最新汇率 >24h | P3 | 值班知悉 |
| 最新汇率 >72h | **P1** | 人工录入 manual 汇率 |
| 新汇率被合理性校验拒收 | P2 | 人工核实行情，必要时 manual 覆盖 |
| recheck 发现存量配置负毛利 | **P1** | 人工决策：调价或停渠道（系统不自动动作） |
| recheck 发现毛利 < 5% 缓冲线 | P2 | 提示调价 |
| 对账任一不变量非 0 | **P1** | 冻结相关发布，当日排查 |
| 结算使用了 >24h 旧汇率 | P3 | 计数指标，周报回顾 |

### 9.6 回滚与修正原则

项目尚未上线，不设生产灰度（Shadow/Canary）流程，staging 验收（§8.7）即最终门槛。但账务的回滚原则从第一天生效：

1. 发现账务异常：停用相关 CNY 渠道（配置操作，分钟级，流量自动回落 USD 渠道）。
2. 已落库分录**不回滚不修改**（账本只追加），修正一律走 `adjustment` 冲正分录。
3. 代码回滚不影响已落库数据（新列可空/有默认，旧代码可共存）。

---

## 十、实施阶段与验收门槛

| 阶段 | 内容 | 验收门槛（全部满足才进下一阶段） |
|---|---|---|
| **P0 决策固化** | 蓝图修订（附录 D 全清单：新增多货币 DEC、删除 DEC-021 汇率禁令、DEC-027 边界表述）；**admin 消息中心 MVP**（告警通道，§12.C.3）；API key 双层配置设计定稿（5.1） | §12.C 决策已全部确认（2026-08-29 ✅）；消息中心 MVP 可用；蓝图修订落稿 |
| **P1 基础设施** | M1/M2；fx 包；`fx-rate-sync` worker；告警接线；admin 汇率页（只读+手工录入）；运营完成 provider 币种盘点表 | 汇率连续流动 3 天；I6 绿；手工录入链路验证通过；§8.1 fx 用例全绿 |
| **P2 资金链路** | M3/M4/M5；billing/routing/settlement 改造；provider 余额解锁；`fx-margin-recheck` + `fx-reconciliation` worker；M6 数据修正 | §8 全部测试绿（含黄金回归 S1/S3 字节级）；§9.3 等价性检查进 CI；staging 手工清单 §8.7 通过 |
| **P3 报表与前端** | 5.7 报表清扫（先行）；6.1 全部 UI | 报表改造前后 USD-only 数字逐项一致；对账 I1–I5 在 staging 连续 3 天绿；§8.7 手工验收清单通过 |

**依赖关系**：P2 依赖 P1 的汇率数据与盘点表；报表清扫（P3 首项）必须先于启用任何 CNY 渠道（否则 F8 静默漏数）。项目尚未上线，**首发版本即自带全部多货币能力，P3 完成即达到可发布状态**，不设灰度流程。

---

## 十一、风险登记册

| 风险 | 等级 | 缓解 |
|---|---|---|
| 报表静默漏 CNY 数据（F8） | 高 | P3 报表清扫前置于 CNY 流量；9.2 每日对账含报表口径核对 |
| 双守卫（Go/SQL）漂移 | 高 | 9.3 等价性检查进 CI，漂移即红 |
| recovery 重放用错汇率日 | 高 | S7-R 专项测试；代码评审 checklist 单列 |
| 双重换汇（D1 边界被误用） | 高 | ADR + 录入界面按 provider 币种防呆 + recheck 复查异常毛利率告警 |
| 汇率源返回垃圾数据 | 中 | 合理性校验双闸（跳变+区间）；manual 兜底 |
| 存量错标修正改错 | 中 | 修正脚本进版本库、前后对照单、deferred 守卫整体复验、业务签字 |
| 汇率长期陈旧导致毛利误判 | 中 | 陈旧分级告警；定价预留缓冲的操作规范（面板显示毛利率辅助目测） |
| provider 币种改动破坏历史语义 | 中 | D3 不可变规则（应用层强制，触发器兜底可后补） |

---

## 十二、附录

### A. 涉及文件清单（速查）

**unio-gateway**：`migrations/0000XX_*`（M1–M5 新增）；`internal/core/fx/`（新增）；`internal/core/billing/{types,margin,cost_ratio,scale}.go`；`internal/core/routing/router.go`；`internal/app/*/lifecycle/settlement.go`；`internal/core/providerledger/`；`internal/service/admin/{providerbalance,channelprice,modelprice,modelops}/`；`internal/app/adminapi/provider/`；`internal/app/workers/` + `internal/bootstrap/worker_server.go`；`sql/queries/{gateway,admin,console}/**`（报表清扫清单以 rg 输出为准）。

**unio-admin**：`src/lib/format.ts`、`src/lib/api/{providers,providerBalance,providersOps,channelPrices,modelPrices}.ts`、`src/components/providers/*`、`src/components/models/{ModelSupplyMarginPanel,ModelPriceDetailsHoverCard,ModelPricesDialog}.tsx`、`src/components/channels/{ChannelPricesDialog,ChannelCostCalculator}.tsx`、新增汇率管理页。

**unio-console**：无功能改动（回归验证）。

**unio-blueprint**：修订清单见附录 D（P0 交付物，含与现行 DEC-021 的冲突处理）。

### B. 与需求文档的术语勘误

| 需求文档表述 | 实际 |
|---|---|
| `provider_costs` 表 | 不存在；对应 `channel_prices`（绝对路径）与 `model_prices × 倍率`（倍率路径，本期不动） |
| 「margin guard 当前逻辑」单处 | Go + SQL 双实现，均需改造且需等价性护栏 |
| 「provider 余额需要新表/字段」 | schema 已就绪（F4），只需解锁应用层 |

### C. 业务决策结果（2026-08-29 已全部确认，P0 关闭）

1. **存量错标售价修正口径**：全部按**执行日当天汇率**折算（当前为开发数据、仅观测用途，无需追溯录入当期汇率）。
2. **provider 币种盘点**：现存 provider 全部为「直接 CNY 计价」（报价数值即 CNY：上游按 1:1 兑名义 USD、充值均为 CNY）→ 全部标记 `currency='CNY'`。（v1.6 落地形态：实际数据走**倍率路径 + 原币记账**——基准价数值 × 倍率 = CNY 金额，充值倍率 1.0；无 channel_prices 绝对成本行。）
3. **告警通道**：新建 **admin 消息中心**（站内消息模块，MVP 先行；后续用户工单系统复用同一消息范式）；Prometheus 指标体系保留（§7）。
4. **毛利缓冲线**：5%。
5. **低余额阈值**：按 USD 等值口径判定。
6. **汇率源与 API key**：免费版 ExchangeRate-API 主源 + Frankfurter 备源，每 6h 拉取；现有 API key **继续使用、不作废**，配置方式见 5.1（app_settings 优先、环境变量兜底、系统设置页可运行时验证）。

### D. 蓝图（unio-blueprint）修订清单（P0 交付物）

> 核对方法：`rg "币种|货币|USD|汇率" docs/` 全量输出逐条分类（注意 `currency` 会误匹配 `concurrency_limit`）。
> 修订统一口径（已决策，§12.C）：**删除「不引汇率」类禁令**；金额展示与聚合允许换汇，但必须可审计——归一聚合基于每笔钉住落库的汇率，展示换算标注汇率与日期；不同币种金额仍不做无换算的直接相加。

| 文档 | 现状 | 修订 |
|---|---|---|
| `gateway/decisions/adr-0003-billing-settlement.md` | DEC 登记表含 DEC-027/DEC-031 取代关系；正文「Admin 手工调额只接受 USD」 | 新增多货币 DEC 条目（收录本文档 D1–D11 摘要；取代关系标注「修订 DEC-027：充值倍率不再承载新渠道汇率语义，边界见 D1」）；调额表述改为「只接受 provider 原始币种」；快照结构补 `fx_rate / fx_rate_date / total_cost_amount_usd` 语义 |
| `gateway/decisions/adr-0020-model-centric-supply-and-pricing.md` | 无实质货币内容（`currency` 命中均为 `concurrency_limit` 误匹配） | 仅加交叉引用：绝对成本路径的币种语义由新 DEC 定义 |
| `gateway/features/billing-settlement.md` | 「Admin 手工调额只接受 USD 和最终目标余额」；结算/授权表述默认单币种 | 调额改原币；新增「成本原币入账 + 结算钉汇率归一 USD」段落；授权侧（客户 USD）表述不变 |
| `admin/decisions/adr-0001-objective-operational-facts.md`（**DEC-021，正面冲突**） | 明文「金额与利润展示必须可审计，不受隐式汇率影响」「金额按币种分组，不引汇率或跨币种相加」 | **已决策（§12.C）：直接删除「不引汇率」禁令**；「可审计」要求保留——归一聚合一律基于钉住落库的汇率，展示换算标注汇率与日期 |
| `admin/pages/operations-dashboard.md` | 「金额卡按币种分开展示；不得提供隐式汇率换算或跨币种总额」「Provider breakdown 当前 USD 余额列、低余额 <10 USD」 | 同 DEC-021 修订口径；成本/毛利卡改读 USD 归一列并标注口径；Provider 余额列改「原币 + ≈USD 等值」 |
| `admin/features/operations-observability.md` | 「不引入汇率或跨币种求和」「利润率只在同币种内计算」 | 利润率口径改为「售价 USD vs 钉住汇率归一成本 USD」；「禁止隐式换算」保留 |
| `admin/pages/provider-channel-management.md` | 「当前 USD 余额」×3；低余额筛选 = 大于等于 0 且小于 10 USD 或负数 | 余额展示改「原币 + ≈USD 等值」；低余额阈值改按 USD 等值口径判定（或分币种阈值，评审定） |
| 其余命中（`request-lifecycle` `error-semantics` `admission-control` `access-control` `glossary` 等） | 多为附带提及或误匹配；客户侧「所有币种账面余额」等表述本就多币种中立 | P0 逐条核对顺改，不改变客户侧语义（D9） |
