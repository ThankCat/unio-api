# 阶段 15 计划 - 渠道商品化 + 策略路由（Channel Productization & Routing Strategy）

> 状态：planned（规划中，尚未进入实现）。本文件是「本次整改」的落地设计文档；
> `STATUS.md` / `ACCEPTANCE.md` 在正式进入实现时再补齐。
>
> 关系：本阶段与 [phase-14 模型目录解耦](../phase-14-model-catalog-decoupling/PLAN.md) 正交但相邻——
> phase-14 解决「模型从哪来 / 怎么采纳」，本阶段解决「同一个模型怎么按渠道定价、怎么把渠道差异
> 包装成可售商品、客户怎么选线路」。两者都会动到「创建/管理流程」与前端，建议落地时统一收口。
>
> 设计来源：参考 one-api/new-api 的 `group + ability + 倍率` 机制，**砍掉倍率、改用绝对价**，
> 并把「分组」演进为面向客户的「线路（route）」。详见 §16 决策记录。

## 1. 背景与动机

当前一个模型可绑定多条渠道，但存在四个产品/架构痛点（用户原话归纳）：

1. **黑盒赔钱**：路由在选渠道那一刻**完全不读价**（只看能力/熔断/协议 + priority），成本只在
   事后结算才出现。运营无法在配置期就确定每条请求赚不赚。
2. **没有可选性**：客户只看到模型，模型只有**一个模型级售价**（`prices`，与渠道无关），
   产品没有档位差异，客户比价后易流失。
3. **想买「又快又稳」无门**：客户愿意为稳定/低延迟的贵渠道付费，但系统不暴露这种选择。
4. **创建链路绕**：建渠道 → 建模型 → 回头绑定 → 再分别配「模型售价」「渠道成本价」，四段断头路。

## 2. 现状（代码事实，整改前）

| 维度 | 现状 |
| --- | --- |
| 渠道-模型绑定 | `channel_models`（000008）：`(channel_id, model_id, upstream_model, status)`，唯一 `(channel_id, model_id)`。 |
| 客户售价 | `prices`（000012）：**模型级**（`model_id` + currency + unit + 生效窗口），EXCLUDE 约束保证同一 `(model, currency, unit)` 同一时刻只有一条 enabled。金额不可改、改价新建一条。 |
| 成本价 | `channel_cost_prices`（000019）：**渠道-模型级**（`(channel_id, model_id)` + 生效窗口），复合外键引用 `channel_models(channel_id, model_id)`。 |
| 计费解析 | 收入：`FindActivePriceForModel(model_id, at_time)`（**按模型，与最终渠道无关**）。成本：`FindActiveChannelCostPrice(channel_id, model_id, at_time)`（按实际渠道）。 |
| 计费快照 | `price_snapshots`（000013，`price_id → prices(id)`）；`cost_snapshots`（000020，`cost_price_id → channel_cost_prices(id,channel_id,model_id)`，含实际成本金额分项 + 总额 CHECK）。 |
| 路由候选 | `FindRouteCandidates(project_id, requested_model_id, ingress_protocol)`：过滤 model/channel/provider/cm 全 enabled + 协议匹配 + 项目可见性（allow/deny），`ORDER BY c.priority ASC, c.id ASC`。**不读任何价格**。 |
| 候选执行 | `lifecycle.PrepareCandidates`（能力过滤 + 熔断可用性 + 保守 token 估算）→ `AttemptRunner.RunNonStream/RunStreamGeneric` 顺序走候选，遇**可重试**上游错误（限流/超时/5xx）`fallback` 到下一条同模型渠道（`retry.go`）。 |
| 认证 | `GetAPIKeyByHash(key_hash)` → 带出 `project_id`、`user_id`、`spend_limit_reached`。Key 归属 `projects`（000003），项目归属 `users`。 |
| 可见性 | `project_model_policies`（000016）：项目级模型 allow/denied。 |
| 预授权 | `ledger_reservations`（000017）+ 候选级保守估算（`candidates.go` `ConservativeInputTokens`）。 |

## 3. 问题（要解决的）

1. 路由不读价 → 无法在选路时保证毛利（痛点 1）。
2. 售价只在模型级 → 无法按渠道差异化定价、无法做档位商品（痛点 2/3）。
3. 客户无法选择「走哪条线 / 经济还是稳定」（痛点 3）。
4. 售价（模型级）与成本价（渠道级）两套表、两段配置，创建链路绕（痛点 4）。

## 4. 目标 / 非目标

### 目标
- **价格下沉到渠道-模型级**：合并 `prices` + `channel_cost_prices` 为单表 `channel_prices`，
  一行同时含**成本价 + 售价**（成本可空、售价必填），毛利一眼可见。
- **录入守卫**：配置时 `售价 < 成本` 直接拦下（DB CHECK + service 校验），把「不赔钱」前移到录入期。
- **线路（route）= 渠道商品**：
  - 内置 **经济**（按售价升序）/ **稳定**（按健康/低延迟）两条线路，**系统自动判定、零配置**，
    候选池 = 该模型当前所有可见/启用/已定价渠道（动态）。
  - **自定义线路**（可选）：运营手挑渠道 + 选策略，用于**固定单渠道**或**把某几条打包成套餐**。
- **API Key 绑线路**：客户/运营建 key 时选线路（key 为准，project 提供默认，未选回落内置经济）。
- **按实际命中渠道的售价计费**：收入解析从「模型级」改为「实际渠道-模型级」。
- **一站式创建**：绑定渠道-模型时一次填齐 `upstream_model + 成本价 + 售价`。
- 含前端改动。

### 非目标
- 不引入 one-api 式**倍率**（model_ratio/group_ratio）——用绝对价。
- 不做**请求级**临时指定渠道（仅 key 级，§16 Q5）；one-api `SpecificChannelId` 式后续再议。
- 不改账务事实记账口径（ledger/usage 双分录、formula_version 语义不变），只改「价从哪来」。
- 不改能力闸门 observe/enforce 策略、不改双协议 ingress 契约。
- 不做跨 provider 能力 sub-routing（延续 DEC-015 非目标）。

## 5. 总体设计（目标态）

```text
            API Key ──(route_id)──▶ Route（线路/商品）
                                      │  mode: cheapest | stable | fixed
                                      │  pool: all(动态) | explicit(手挑渠道)
                                      ▼
   请求 model=gpt-5.5 ─▶ 候选集 = 该模型可路由渠道 ∩ 线路池 ∩ 项目可见 ∩ 已定价(售≥成)
                                      │
                          按 mode 排序：cheapest=售价升序 / stable=健康优先 / fixed=锁一条
                                      ▼
                          能力过滤 + 熔断可用性 → 顺序 fallback（fixed 不 fallback）
                                      ▼
                 命中渠道 c → 用 channel_prices(c, model) 同时取「售价(收入)+成本(成本)」
                                      ▼
                 price_snapshot(售价) + cost_snapshot(成本) 双快照 → ledger 双分录
```

关键边界：
- **线路只决定「候选池 + 排序」**，不改变能力闸门、熔断、协议过滤等既有约束（叠加在其之上）。
- **收入与成本同源**：都来自命中渠道的 `channel_prices` 行；毛利在录入期已被守卫保证非负。

## 6. 数据模型变更（草案，DDL 为示意）

### 6.1 新增 `channel_prices`（合并售价 + 成本价，渠道-模型级）
```sql
CREATE TABLE channel_prices (
    id BIGSERIAL PRIMARY KEY,
    channel_id BIGINT NOT NULL,
    model_id   BIGINT NOT NULL,
    currency   TEXT NOT NULL CHECK (currency <> ''),
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- 售价（客户侧，必填）
    uncached_input_price        NUMERIC(20,10) NOT NULL CHECK (uncached_input_price >= 0),
    cache_read_input_price      NUMERIC(20,10) CHECK (cache_read_input_price IS NULL OR cache_read_input_price >= 0),
    cache_write_5m_input_price  NUMERIC(20,10) CHECK (cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0),
    cache_write_1h_input_price  NUMERIC(20,10) CHECK (cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0),
    output_price                NUMERIC(20,10) NOT NULL CHECK (output_price >= 0),
    reasoning_output_price      NUMERIC(20,10) CHECK (reasoning_output_price IS NULL OR reasoning_output_price >= 0),

    -- 成本价（上游侧，可空：不一定知道成本）
    uncached_input_cost         NUMERIC(20,10) CHECK (uncached_input_cost IS NULL OR uncached_input_cost >= 0),
    cache_read_input_cost       NUMERIC(20,10) CHECK (cache_read_input_cost IS NULL OR cache_read_input_cost >= 0),
    cache_write_5m_input_cost   NUMERIC(20,10) CHECK (cache_write_5m_input_cost IS NULL OR cache_write_5m_input_cost >= 0),
    cache_write_1h_input_cost   NUMERIC(20,10) CHECK (cache_write_1h_input_cost IS NULL OR cache_write_1h_input_cost >= 0),
    output_cost                 NUMERIC(20,10) CHECK (output_cost IS NULL OR output_cost >= 0),
    reasoning_output_cost       NUMERIC(20,10) CHECK (reasoning_output_cost IS NULL OR reasoning_output_cost >= 0),

    status TEXT NOT NULL CHECK (status IN ('enabled','disabled')),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 售价/成本必须对应真实存在的渠道-模型绑定（沿用 channel_cost_prices 既有约束口径）
    CONSTRAINT fk_channel_prices_channel_model
        FOREIGN KEY (channel_id, model_id) REFERENCES channel_models (channel_id, model_id),

    -- 供快照复合外键引用（替代 cost_snapshots 现有 (id,channel_id,model_id) 复合 FK）
    CONSTRAINT uq_channel_prices_id_channel_model UNIQUE (id, channel_id, model_id),

    CONSTRAINT ck_channel_prices_window CHECK (effective_to IS NULL OR effective_to > effective_from),

    -- 录入守卫（每个分项：售价 >= 成本，成本为空则跳过）
    CONSTRAINT ck_channel_prices_margin CHECK (
        (uncached_input_cost       IS NULL OR uncached_input_price       >= uncached_input_cost) AND
        (output_cost               IS NULL OR output_price               >= output_cost) AND
        (cache_read_input_cost     IS NULL OR cache_read_input_price     IS NULL OR cache_read_input_price     >= cache_read_input_cost) AND
        (cache_write_5m_input_cost IS NULL OR cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= cache_write_5m_input_cost) AND
        (cache_write_1h_input_cost IS NULL OR cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= cache_write_1h_input_cost) AND
        (reasoning_output_cost     IS NULL OR reasoning_output_price     IS NULL OR reasoning_output_price     >= reasoning_output_cost)
    ),

    -- 同一 (channel, model, currency, unit) 同一时刻只能有一条 enabled（沿用 prices 的 EXCLUDE 口径）
    CONSTRAINT ex_channel_prices_enabled_window
        EXCLUDE USING gist (
            channel_id WITH =, model_id WITH =, currency WITH =, pricing_unit WITH =,
            tstzrange(effective_from, COALESCE(effective_to,'infinity'::timestamptz), '[)') WITH &&
        ) WHERE (status = 'enabled')
);
CREATE INDEX idx_channel_prices_channel_model_status_effective
    ON channel_prices (channel_id, model_id, status, effective_from DESC, id DESC);
```
> 守卫双保险：DB `ck_channel_prices_margin` 硬拦 + service 层返回**可读错误**（哪个分项亏、差多少）。
> 若以后要允许促销性亏本，再把硬 CHECK 降级为 service 软提示（本期按用户要求做**硬拦**）。

### 6.2 退役 `prices` 与 `channel_cost_prices`
- **`prices`（模型级售价）彻底退役**（§16 Q1=a）：计费一律走 `channel_prices`；某渠道-模型未定价 = 该渠道不可计费（路由跳过，见 §7）。
- **`channel_cost_prices` 退役**：成本并入 `channel_prices`。
- 关联调整：
  - `price_snapshots.price_id` 改为引用 `channel_prices(id)`（语义从「模型售价」变「渠道售价」）。
  - `cost_snapshots` 的 `cost_price_id` 复合外键改为引用 `channel_prices(id, channel_id, model_id)`。
  - `DeleteChannelModel` / `DeleteModelCascade` 引用检查从 `channel_cost_prices` 换成 `channel_prices`。

### 6.3 新增 `routes`（线路 / 渠道商品）
```sql
CREATE TABLE routes (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,                       -- 对外商品名：经济 / 稳定 / C-专线 ...
    mode TEXT NOT NULL CHECK (mode IN ('cheapest','stable','fixed')),
    pool_kind TEXT NOT NULL CHECK (pool_kind IN ('all','explicit')),  -- all=动态全量；explicit=手挑
    is_builtin BOOLEAN NOT NULL DEFAULT false,       -- 内置经济/稳定，不可删
    status TEXT NOT NULL CHECK (status IN ('enabled','disabled')),
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 种子：内置「经济」(cheapest, all) 与「稳定」(stable, all)，is_builtin=true，pool_kind='all'。
```

### 6.4 新增 `route_channels`（自定义线路的渠道池）
```sql
CREATE TABLE route_channels (
    route_id   BIGINT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    channel_id BIGINT NOT NULL REFERENCES channels(id),
    PRIMARY KEY (route_id, channel_id)
);
CREATE INDEX idx_route_channels_channel ON route_channels (channel_id);
```
> 约束（service 层强校验）：`pool_kind='all'` 的线路不得有 `route_channels` 行；
> `mode='fixed'` 的线路必须**恰好一条** `route_channels`。

### 6.5 `api_keys` / `projects` 绑线路
```sql
ALTER TABLE api_keys ADD COLUMN route_id BIGINT REFERENCES routes(id);   -- NULL = 回落项目默认/内置经济
ALTER TABLE projects ADD COLUMN default_route_id BIGINT REFERENCES routes(id); -- 项目级默认线路（可空）
```
> 线路解析优先级：`api_keys.route_id` ?? `projects.default_route_id` ?? 内置「经济」。

## 7. 路由改造（`internal/core/routing` + `internal/service/gateway/lifecycle`）

候选生成新增「线路过滤 + 策略排序」，叠加在既有过滤之上：

1. **线路解析**：认证已知 `project_id`；新增解析 key 的 `route`（含 mode/pool）。
2. **候选过滤**（扩展 `FindRouteCandidates`）：
   - 沿用现有过滤（model/channel/provider/cm enabled + 协议 + 项目 allow/deny）。
   - **新增**：`pool_kind='explicit'` 时，候选 ∩ `route_channels`（`fixed` 即只剩一条）。
   - **新增**：候选必须在 `channel_prices` 有「当前生效、售价齐全」的行（未定价渠道不参与计费 → 直接排除）。
   - **新增**：`SELECT` 带出当前生效 `channel_prices` 的售价（供 cheapest 排序）。
3. **策略排序**（在 Go 侧，`lifecycle.PrepareCandidates` 之前或之中）：
   - `cheapest`：按**售价**升序（§16 Q6：按售价不按毛利；毛利非负已由守卫保证）。
     售价是多分项，排序口径用「代表价」（建议 `output_price` 为主键，`uncached_input_price` 次之；具体口径在实现期定并写入 ACCEPTANCE）。
   - `stable`：按渠道健康排序——熔断 closed 优先，再按近窗口错误率/延迟（复用 `lifecycle/breaker.go` + `RecordChannelHealth` 的内存统计），平手回落 `priority ASC`。
   - `fixed`：单候选，**禁用 fallback**（候选只有一条，`AttemptRunner` 自然不切换）。
4. **能力过滤 + 熔断可用性 + fallback**：完全不变（叠加在排序结果之上）。

> 运行时新增读取：路由首次需要读 `channel_prices` 售价（cheapest 排序 + 计费）。
> 走 routing 的缓存层（与渠道/能力同款缓存），避免每请求打 DB。

## 8. 计费改造（settlement）

核心变化：**收入解析从「模型级」改为「实际命中渠道-模型级」，与成本同源。**

- 退役 `FindActivePriceForModel`；新增 `FindActiveChannelPrice(channel_id, model_id, at_time)`，
  **一次取回售价 + 成本**（命中渠道已知，来自 cost/route 决策结果）。
- 结算：
  - 收入分项用 `channel_prices` 售价列；写 `price_snapshots`（`price_id → channel_prices(id)`）。
  - 成本分项用同行成本列；写 `cost_snapshots`（`cost_price_id → channel_prices(id,channel_id,model_id)`）。
  - 成本列为空 → 成本快照该分项记 0（成本未知按 0 入账，毛利偏保守；ACCEPTANCE 固化此约定）。
  - ledger 双分录、`formula_version`、usage 口径**不变**。
- **预授权/reservation 估算**（`ledger_reservations` + `candidates.go`）：
  下单时尚未确定最终渠道（可能 fallback），预扣额必须**保守**：
  取**候选池内售价最高**的渠道做上界估算（`cheapest` 模式实际命中只会更便宜 → 不会超扣）。
  实现期补一条 `MaxActiveSalePriceAmongCandidates` 查询/计算。

## 9. 接口契约（admin / 客户侧）

### 价格（合并后）
- `POST /admin/v1/channels/{channelID}/models/{modelID}/prices` — 创建一条 `channel_prices`（售价必填、成本可选；违反守卫返回 422 + 明确分项）。
- `GET  /admin/v1/channels/{channelID}/prices` — 列某渠道全部渠道-模型价（含历史/停用 + 毛利展示）。
- `PATCH /admin/v1/channel-prices/{id}` — 调整生效窗口/启停（金额不可改，改价新建一条；窗口重叠 → 409）。
- 退役模型级 `prices` 的创建/查询接口。

### 线路
- `GET/POST /admin/v1/routes`、`GET/PATCH/DELETE /admin/v1/routes/{id}` — 自定义线路 CRUD（内置经济/稳定只读不可删）。
- `PUT /admin/v1/routes/{id}/channels` — 设置 explicit 线路的渠道池（fixed 校验恰好一条）。

### API Key 绑线路
- `POST /admin/v1/projects/{projectID}/api-keys` body 增加 `route_id`（可空）。
- `PATCH /admin/v1/api-keys/{id}` 支持改 `route_id`。
- `PATCH /admin/v1/projects/{projectID}` 支持设 `default_route_id`。
- 客户自助侧（若有）：建 key 时可选「线路」（展示为商品名 + 简介，如「经济 / 稳定 / 套餐X」）。

### 一站式绑定
- `POST /admin/v1/channels/{channelID}/models` — 在建「渠道-模型绑定」同一请求内可携带 `upstream_model + 售价 + 成本价`，单事务建 `channel_models` + `channel_prices`。

### 运行时
- `GET /v1/models`（`ListAvailableModelsForProject`）：受 key 线路影响——`fixed`/`explicit` 线路只暴露其池内渠道能服务的模型；`all` 线路维持现有可见性口径。

## 10. 前端改动（`unio-admin`）

- **价格管理**：渠道详情下「渠道-模型价」一张表，**同行填售价 + 成本**，实时显示毛利、`售价<成本` 飘红并禁止提交。退役独立的「模型售价」入口。
- **线路管理页**：列内置（经济/稳定，只读）+ 自定义线路；自定义线路编辑器（选 mode + 选渠道池，fixed 限一条）。
- **API Key 表单**：新增「线路」选择（展示商品名 + 简介）；项目设置页可设默认线路。
- **一站式绑定**：渠道详情「添加模型」弹窗一次填 `upstream_model + 售价 + 成本`。
- **请求详情/账务**：成本/收入快照展示沿用，价来源标注「渠道-模型价」。

## 11. 运行时影响与边界

- 新增运行时读：路由读 `channel_prices` 售价（cheapest 排序 + 计费），走缓存。
- `stable` 排序复用既有熔断/健康内存统计，无新外部依赖。
- 不改 ingress 协议契约、不改能力闸门、不改 ledger 记账事实口径。
- 爆炸半径：路由候选排序 + 计费价来源 + 价格/线路 admin + 前端；其余 gateway 链路不动。

## 12. 数据迁移（开发期，可随意重建）

- 开发阶段 DB 可 down/up，**无需保数据迁移脚本**。
- 新增迁移（编号在 phase-14 之后**按落地顺序顺延**，示意 `000028+`）：
  - 建 `channel_prices`（含守卫 CHECK + EXCLUDE + 复合唯一）。
  - 建 `routes` + 种子内置经济/稳定；建 `route_channels`。
  - `api_keys` 加 `route_id`、`projects` 加 `default_route_id`。
  - `price_snapshots.price_id` 改引用 `channel_prices`；`cost_snapshots` 复合 FK 改引用 `channel_prices`。
  - 删 `prices`、`channel_cost_prices`（含其查询）。
- 与 phase-14 的迁移**互不冲突**（phase-14 动 models/目录三表，本阶段动价/线路/快照外键），落地先后皆可，仅编号顺延。

## 13. 任务拆分（实现期再逐项打勾）

- [ ] T1 迁移：`channel_prices`（守卫 CHECK/EXCLUDE）+ `routes`(+种子) + `route_channels`；`api_keys.route_id`/`projects.default_route_id`；快照外键改挂 `channel_prices`；删 `prices`/`channel_cost_prices`。
- [ ] T2 sqlc：`channel_prices` CRUD + `FindActiveChannelPrice` + 候选售价上界（reservation）；`routes`/`route_channels` CRUD；`api_keys`/`projects` 线路读写；`FindRouteCandidates` 扩展（线路池过滤 + 已定价过滤 + 带出售价）。
- [ ] T3 routing：线路解析 + 候选池过滤 + 策略排序（cheapest/stable/fixed）；价缓存接入。
- [ ] T4 settlement：收入解析改 `FindActiveChannelPrice`；双快照改挂 `channel_prices`；成本空值按 0；reservation 保守上界。
- [ ] T5 service：价格守卫（可读错误）；线路 CRUD + fixed/all 校验；一站式绑定事务；key/project 线路绑定。
- [ ] T6 adminapi：渠道价/线路/绑定/key 线路 路由 + DTO + 错误渲染；`/v1/models` 随线路收敛。
- [ ] T7 前端：渠道价表（毛利+守卫）、线路管理页、key 线路选择、一站式绑定弹窗、退役模型售价入口。
- [ ] T8 测试：守卫拦截矩阵、cheapest/stable/fixed 排序、fixed 不 fallback、按实际渠道计费、reservation 不超扣、迁移 down/up。
- [ ] T9 文档：补 `STATUS.md`/`ACCEPTANCE.md`，更新 chapters `README.md` 索引、`PROJECT_STATUS.md`，必要时 `DECISIONS.md` 追加（倍率取舍 / 收入按渠道）。

## 14. 测试策略

- 单元：守卫真值矩阵（各分项售<成、成本空值跳过）；cheapest 排序口径；stable 排序在熔断/健康组合下的次序；fixed 单候选无 fallback。
- DB 集成：`channel_prices` 守卫 CHECK 与 EXCLUDE 生效；一站式绑定事务原子；快照复合外键约束；迁移 down/up。
- 计费：按实际命中渠道售价记收入、成本同源；成本空值记 0；reservation 取候选最高售价不超扣；ledger 双分录平衡。
- 路由：线路池过滤（all/explicit/fixed）+ 项目可见性叠加正确；未定价渠道被排除。
- 回归：能力闸门、双协议 ingress、熔断 fallback 行为不变。
- 前端：build + lint；守卫飘红拦截；线路选择落库正确。

## 15. 风险与取舍

1. **收入口径变更（模型级 → 渠道级）**：动到账务读取路径，是本阶段最敏感处；
   靠「双快照同源 + reservation 保守上界 + formula_version 不变」控制风险，需重点测试。
2. **cheapest 多分项排序口径**：售价是向量，需定一个稳定的「代表价」口径，否则排序抖动；实现期固化进 ACCEPTANCE。
3. **stable 依赖运行时健康统计**：冷启动/无样本时退化为 `priority`，需明确退化策略。
4. **守卫硬 CHECK**：杜绝亏本但牺牲促销灵活性；本期按用户要求硬拦，未来可降级为软提示。
5. **中等偏上重构**：动迁移（建 3 表 + 改 2 快照外键 + 删 2 表）+ 路由 + 计费 + admin + 前端；运行时协议不动，可分段交付。
6. **与 phase-14 协同**：两阶段都改创建/前端流程，建议合并收口避免来回改 UI。

## 16. 决策记录

> 已敲定（2026-06-15）：

- **Q1 价格表合并 = `channel_prices`，老 `prices` 彻底退役（方案 a）**：售价+成本同表同行（成本可空、售价必填）；计费一律走渠道-模型级，无模型级兜底。
- **Q2 选择单位 = 命名线路（route），采用「内置经济/稳定（自动）+ 自定义线路（可选，含 fixed/套餐）」**：
  key 绑 `route_id`；内置两条系统判定零配置（池=全量动态），自定义手挑渠道（fixed 限一条）。
  否决「裸渠道 id」（渠道增删/换会废 key）与「key 内联策略」（无法一处改全量生效）。
- **Q3 计费 = 按实际命中渠道的售价**（收入随命中渠道走，毛利恒定）。
- **Q4 线路挂 `api_keys`**（key 为准，project 给默认 `default_route_id`，未选回落内置经济）。
- **Q5 仅 key 级选择**，不做请求级临时指定渠道（one-api `SpecificChannelId` 式后续再议）。
- **Q6 cheapest = 按售价升序**（不按毛利；毛利非负由录入守卫保证）。
- **守卫**：配置 `channel_prices` 时任一分项 `售价 < 成本` → DB CHECK 硬拦 + service 可读报错。
- **倍率**：不引入（绝对价更直观、可审计），区别于 one-api/new-api。

## 17. 参考
- one-api：`model/ability.go`（group×model×channel 路由索引）、`middleware/distributor.go`（`SpecificChannelId` 锁渠道 / 否则按 group 随机）、倍率计费。
- new-api：`model/ability.go`（加 `weight` 加权 + `tag`）、渠道级 setting 覆盖价。
- [phase-14 模型目录解耦 PLAN](../phase-14-model-catalog-decoupling/PLAN.md)
- [DEC-015 能力架构与 models.dev 定位](../../production/DECISIONS.md#dec-015-能力架构三层模型与-modelsdev-定位)
- 代码锚点：`sql/queries/{prices,channel_cost_prices,channel_models,api_keys}.sql`、`internal/core/routing/router.go`、`internal/service/gateway/lifecycle/{candidates,attempt_runner,retry,breaker}.go`、`migrations/{000012,000013,000019,000020,000016,000004,000003}_*.sql`、`unio-admin/src/pages/*`。
