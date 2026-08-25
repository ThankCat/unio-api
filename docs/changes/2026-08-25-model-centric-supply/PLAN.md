# 以模型为中心的供给与定价改造

## 背景

当前供给链路是 `API Key → Route → route_channels → Channel → channel_models → Model`，
Route 同时承载六件事：渠道池、模型售卖资格（含 ingress 协议）、售价倍率、调度模式、
入口限流、（已废弃的）会话粘性开关。

这个结构在只有一种上游形态时够用，但接入 DeepSeek（chat-only、需 Responses→Chat 桥接）
之后暴露了两个问题：

1. **概念负担**：一次「接入新供应商」要同时理解 provider / channel / adapter_key /
   protocol / route / offering 六个实体，以及直传与桥接两条执行路径。
2. **Route 是运营视角的容器，却泄漏到了产品设计中**。它承载的六件事里，只有
   「能用哪些模型、哪些协议」是最终用户关心的，其余五件都是内部编排。

线上现状（2026-08-25 核对本地 Dev 库）：

- `routes` 仅 1 行（id=1，`balanced`，`price_ratio=0.2`），2 个用户 2 把 Key 全绑它；
- `route_channels` 2 行，`route_model_offerings` 5 行（全部 `ingress_protocol=openai`）；
- `channels` 6 行全部 `(protocol=openai, adapter_key=openai)`，2 启用 4 停用；
- `user_model_policies` **0 行**（过滤逻辑在空转）。

即：Route 当前没有产生任何区分，是一个只有一个选项的选择。此刻是改造成本最低的时刻。

## 目标

把供给的根从 Route 换成 Model：

```
api_key ──▶ user ──▶ 限流额度
model ──channel_models──▶ 候选渠道（enabled + 协议匹配 + 成本可解析）
      └──model_prices/售价──▶ 计费
```

对最终用户，console 上只剩「密钥」和「可用模型」两个概念；
Route、Channel、Provider、adapter_key 全部退回运营后台。

## 已确认的决策

以下均为 2026-08-25 与用户逐条确认的结论，实施时不再重开：

| # | 决策 | 备注 |
| --- | --- | --- |
| D1 | 彻底删除 route：表、字段、代码、admin 页面、console 筛选 | 开发/测试环境，历史数据不保留 |
| D2 | 删除 `fixed` 调度模式，只保留 `balanced`，配置收归全局 | 模型维度下 fixed 无语义 |
| D3 | 入口限流迁到 **user 级**（rpm / rpd / concurrency） | 后续可作为商品售卖；不迁 api_key 级（DEC-027 曾删除该列，不反悔） |
| D4 | `/v1/models` 返回所有 enabled 模型，不做用户级过滤 | 由 D6 的不变量保证「列出即可调用」 |
| D5 | 删除 `user_model_policies` 表及其全部过滤逻辑 | 表为空，且与 D4 冲突；将来做企业分权时重建 |
| D6 | 模型 enable 需前置供给检查；渠道解绑/停用需警告失去供给的模型 | 双向保证「enabled 的模型必定可调用」 |
| D7 | 定价放模型级：**绝对售价优先，缺省回退「基准价 × 全局倍率」** | 两种形式都要 |
| D8 | 毛利守卫重写为逐项比较，覆盖 Standard + **Fast**；长上下文不需校验 | 见「毛利守卫」一节的证明 |
| D9 | 企业差异化定价暂不做，所有用户一视同仁 | 留 users 表扩展位 |
| D10 | `channels.protocol` 改为 `protocols` 数组，一条渠道可服务多协议 | 要求供应商多协议共享同一 origin |
| D11 | `adapter_key` 必填、无默认值 | 现有「留空则取 protocol」的逻辑删除 |
| D12 | `(protocol, adapter_key)` 组合逐个校验，未注册组合硬拦 | 防止配出永远选不中的渠道 |
| D13 | 不做「模型开放协议」第三层 | 模型可用协议 = 其可用渠道的 protocols 并集，自动推导 |
| D14 | 落库 models.dev 的 `family` 字段做系列分组 | 原以为需人工维护故打算跳过；核实后发现 feed 本就提供（343 个模型分 76 个 family），且 `feed.go` 的 `modelsJSONEntry.Family` 已在解析、只是未落库。成本仅「加一列 + 一次赋值」 |
| D15 | 新建厂商表（对应 models.dev 的 lab），存 name 与 logo SVG **内容** | 不存 URL：图标属于展示不能挂的东西，且避免用户浏览器直连第三方。SVG 用 `<img src="data:image/svg+xml;base64,…">` 渲染，不给执行环境 |
| D16 | 厂商表与 `models.owned_by` 用 **slug 关联**，不改外键 | `owned_by` 是 OpenAI 兼容契约字段（`/v1/models` 响应），必须保持字符串；models.dev 的 `lab` 值天然是 slug，两边直接对得上。代价是无引用完整性约束——同步时对「找不到对应厂商」的行记日志即可，图标缺失只退化为显示厂商名 |

## 改造后的关键机制

### 候选筛选

现有 `FindRouteCandidates`（`sql/queries/gateway/channel_models.sql`）的过滤条件中，
**只有一条依赖 route**：

```sql
AND EXISTS (SELECT 1 FROM route_channels rc
            WHERE rc.route_id = sqlc.arg(route_id) AND rc.channel_id = c.id)
```

其余条件（模型匹配、协议匹配、model/channel/provider/binding 四级 enabled、
`credential_valid`、成本可解析）全部与 route 无关，原样保留。改造即：删除该 EXISTS、
删除 `route_id` 参数、把 `c.protocol = $x` 改为 `$x = ANY(c.protocols)`、
删除 `user_model_policies` 两段子查询，查询更名为 `FindModelCandidates`。

### 定价

保持与成本侧对称的两级解析：

```
客户售价 = 模型绝对售价（若配置）
         ↓ 否则
         model_prices 基准价 × 全局售价倍率

渠道成本 = channel_prices 绝对覆盖（若配置）
         ↓ 否则
         model_prices 基准价 × channel_cost_multipliers × channel_recharge_factors
```

迁移时全局售价倍率取 `0.2`（现 `routes.price_ratio` 值），保证改造前后计费结果一致。

### 毛利守卫

现有 DB 触发器 `assert_non_negative_route_margins`
（`migrations/000037_routing_margin_guard.up.sql`）挂在 6 张表上，配置期硬拦亏本组合。
它对倍率路径用了约分简化：售价与成本都含「基准价」这个公因子，于是退化为
`price_ratio >= multiplier × recharge` 的标量比较。

**引入模型绝对售价后该简化在绝对售价路径上失效**（售价侧不再含基准价），
该路径必须逐项比较七个价格分项；倍率路径可继续保留简化式。

校验矩阵：

| 档位 | 是否校验 | 依据 |
| --- | --- | --- |
| Standard | 是 | 基础 |
| Fast | **是（新增）** | 售价来自 `model_price_service_tiers`、成本来自 `channel_price_service_tiers`，是独立价格对，与 Standard 无约分关系。现有守卫不覆盖，属既有缺口 |
| 长上下文 | **否** | 售价与成本使用同一个 `LongContextPolicy`、同一对倍率（`settlement.go:883` 与 `:936`）。逐项校验下 `售价×k ≥ 成本×k ⟺ 售价 ≥ 成本`，倍率约掉，结论等同 Standard |

### 模型的可用协议（D13 的落地形式）

删除 `route_model_offerings` 后，「某模型支持哪些 ingress 协议」不再有存储，改为从供给推导：

```
model ──channel_models（enabled）──▶ channel（enabled）──▶ unnest(protocols) → 取并集
                                     且 provider enabled
```

需要新增一个 console 侧查询做这个聚合，供「可用模型」页展示。这样得到的协议列表
恒等于实际能力，不存在「配置声明支持但调不通」的状态。模型数量在几十量级，
必要时在应用层缓存，不做物化。

`models.owned_by` 作为厂商分组维度直接可用（当前 6 行均为 `openai`，
接入第二家供应商后自然分化）。

### 模型目录元数据补齐（D14 / D15 / D16）

现有 models.dev 每日同步（`ModelCatalogSyncConfig`，消费 `/models.json` 与 `/api.json`）
再补两项：

- **family**：`feed.go` 已解析未落库，`models` 加一列存下即可，用于列表按系列折叠。
- **厂商图标**：feed **不含** logo，实测在站点静态资源
  `https://models.dev/logos/{lab}.svg`（openai / anthropic / deepseek / google /
  meta / mistral / xai 抽查均 200，SVG 约 3.7KB）。同步时按 lab 逐个拉取并存入
  厂商表；单次新增约 199 个 provider 的请求，需限流与失败容忍（拉不到只是没图标，
  不能让整次同步失败）。

厂商表以 slug 为键，与 `models.owned_by` 值域一致。

### 供给不变量

要保证 D4（列出即可调用），需要双向检查：

- **加法方向（新增）**：模型 `enabled` 前置检查——至少存在一条 enabled 的
  `channel_models` 绑定，且其 channel、provider 均 enabled、成本可解析。
- **减法方向（现有，降维）**：`sql/queries/admin/supply.sql` 已有整套
  （`LockModelsForSupplyChange`、`ListOfferingsLosingSupport`、
  `CountOtherEnabledBindingsForModel`、`DisableModelSupply` 等），
  现以 offering（route × model × 协议）为单位，改造后降为 model × 协议。
  409 确认响应的 `affected_routes[]` 改为 `affected_models[]`，语义为
  「这些模型将失去全部供给」。

## 分阶段实施

阶段按**可独立验证**切分，不按模块。每个阶段结束时系统都处于可用状态，
route 到最后一阶段才真正删除，前面各阶段只是「绕过」它。任何阶段可作为停止点。

### 阶段 1　候选筛选脱离 route

**改动**：`FindRouteCandidates` → `FindModelCandidates`，删除 `route_channels` EXISTS
与 `route_id` 参数；同步改 `internal/core/routing/router.go` 的 `PlanChat` 与
`ValidateChat`（后者删除 `route_model_offerings` 检查）。`routes` 表、
`api_keys.route_id`、admin 路由页全部保持不动。

**行为变化**：候选池从「route 池内的渠道」变为「所有满足条件的渠道」。
当前 6 条渠道中 4 条 `disabled`、2 条既 enabled 又在池内，故**实际候选集不变**。

**验证**：`go test ./...` 全绿；本地起 gateway，用真实 Key 打
`/v1/chat/completions` 与 `/v1/responses`，确认命中渠道与改造前一致
（比对 `request_records.channel_id`）。

**可停止**：是。此时系统行为与改造前完全相同。

### 阶段 2　限流迁移到 user 级

**改动**：
- migration：`users` 加 `rpm_limit` / `rpd_limit` / `concurrency_limit`（均可空，
  语义沿用 `nil`=继承全局 / `0`=不限 / `>0`=上限），把 `routes` 上的值复制给全部用户。
- `sql/queries/gateway/api_keys.sql` 的 `GetAPIKeyByHash` 改为 JOIN `users` 取限流值。
- `internal/core/auth/apikey.go` 的 `APIKeyPrincipal` 去掉 `RouteID` 依赖。
- Redis key 去掉 route 维度：
  `admission:v1:ru-rpm:{route}:{user}:{bucket}` → `...:u-rpm:{user}:{bucket}`，
  `ru-rpd`、`ru-conc` 同理（`internal/platform/breakerstore/keys.go`）。
- `RequestAdmission` middleware 与 `requestadmission.Manager.Acquire` 的
  Identity / fingerprint / Lua argv 去掉 `route_id`。
- 全局默认配置键 `gateway.route_rate_limit_defaults` 更名。
- admin 的 `AggregateRouteUsage`（按 route SCAN）改为按 user。

**注意**：RPM/RPD 计数只增不减，改维度后旧桶会占额度至 TTL 过期（RPD ≈ 24h）。
开发环境直接 `FLUSHDB` 对应 namespace。

**验证**：单测覆盖新 key 格式；手动连打超过 rpm 上限，确认 429；
确认并发租约在请求结束后释放（ZREM）。

**可停止**：是。

### 阶段 3　定价迁移到模型级 + 毛利守卫重写

**改动**：
- migration：新增模型绝对售价存储（挂在 `model_prices` 上加售价列，或新建
  `model_sale_prices` 时间窗表——实施时按现有 `model_prices` 的时间窗结构对齐）；
  新增全局售价倍率配置项，初值 `0.2`。
- `internal/core/billing/scale.go` 的 `ScaleCustomerPrice` 改为两级解析。
- 候选装配（`router.go:537-563`）与结算（`settlement.go:879-912`）改为从模型取售价。
- `price_snapshots.price_ratio` 语义改为「结算时生效的售价倍率」；绝对售价路径写 `1.0`
  或置空（二选一，实施时定，需同步改 console/admin 的「售价 ÷ 倍率」倒推逻辑：
  `sql/queries/console/usage.sql:284`、`console/requests.sql:378`、
  `sql/queries/admin/requests.sql:138-140`）。
- 重写触发器 `assert_non_negative_route_margins` → `assert_non_negative_margins`：
  挂载点去掉 `routes`/`route_channels`、加上模型售价表；倍率路径保留标量简化，
  绝对售价路径逐项比较；**新增 Fast 档校验**。
- `settlement_recovery_jobs.price_ratio` 跟随。

**验证**：改造前后对同一组 usage 计算客户扣费，金额必须逐位相同（倍率仍为 0.2）；
构造亏本配置，确认写库被触发器拒绝（Standard 与 Fast 各一例）；
`price_snapshots` 历史行不受影响（老请求读老快照）。

**可停止**：是。

### 阶段 4　渠道多协议

**改动**：
- migration：`channels.protocol TEXT` → `protocols TEXT[]`，迁移 `protocols = ARRAY[protocol]`。
- 候选筛选 `c.protocol = $x` → `$x = ANY(c.protocols)`。
- `internal/service/admin/channel/channel.go`：删除 `adapterKey == "" → adapterKey = protocol`
  的默认逻辑（D11）；`HasAny(protocol, adapterKey)` 改为对 `protocols` 中**每一个**协议
  逐个校验，任一未注册即拒绝（D12）。
- admin 渠道表单：协议改多选，adapter_key 改必填。

**验证**：现有 6 条渠道迁移后 `protocols = {openai}`，行为不变；
新建一条 `protocols = {openai,anthropic}` + `adapter_key = deepseek` 的渠道能通过校验；
新建 `protocols = {openai,anthropic}` + `adapter_key = openai` 被拒绝（anthropic 侧未注册）。

**可停止**：是。

### 阶段 5　供给不变量

**改动**：
- 模型 enable 前置检查（新增，见「供给不变量」）。
- `sql/queries/admin/supply.sql` 全套降维：去掉 route 维度，
  `Offering*` 系列改为 `ModelSupply*`。
- 409 确认响应 `affected_routes[]` → `affected_models[]`。
- admin 前端 `SupplyImpactConfirmDialog`、`RestoreOfferingsDialog` 跟随改造。

**验证**：尝试 enable 一个无渠道供给的模型，应被拒绝并说明原因；
解绑某模型在某渠道上的唯一绑定，应返回 409 并列出该模型；
确认后模型被同步 disable。

**可停止**：是。

### 阶段 6　删除 route 与 user_model_policies

**改动**（此阶段后不可回退）：

*Gateway*
- migration：drop `route_channels`、`route_model_offerings`、`routes`、
  `user_model_policies`；drop `api_keys.route_id`、`request_records.route_id`、
  `routing_decision_traces.route_id`。
- 删除 `sql/queries/admin/route.sql`、`shared/routes.sql`、
  `gateway/user_model_policies.sql`；清理 `admin/supply.sql`、`admin/channel.sql`、
  `admin/provider.sql`、`admin/overview.sql`（`DashboardBreakdownRoute`）、
  `admin/requests.sql`、`admin/user.sql`、`console/requests.sql`、`console/usage.sql`
  中的 route 引用。
- 删除 `internal/service/admin/route`、`internal/service/admin/routeruntime`、
  `internal/app/adminapi/route`（18 个端点，含 10 个 ops）。
- 删除 `routes.mode` 相关的 fixed 分支（D2）、`routes.sticky_enabled`（已废弃列）。
- sticky key 去掉 `route_id` 段（`internal/core/routing/sticky.go:475-486`）。

*Admin 前端（unio-admin）*
- 删除 `pages/RoutesPage.tsx`、`pages/RouteDetailPage.tsx`、`components/routes/` 整目录、
  `lib/api/routes.ts`、`lib/api/routesOps.ts`、导航项与 `/routes*` 路由。
- `ApiKeyFormDialog` 去掉线路选择；`ChannelDetailContent` 去掉关联线路；
  Dashboard 去掉 `dimension=route`；Requests 列表去掉线路列与「线路倍率」。
- 系统设置去掉「线路默认限流」，改为「用户默认限流」。

*Console 前端（unio-console）*
- 请求记录：去掉线路筛选与 `route_name` 列
  （`pages/requests/hooks/use-request-list-filters.ts`、`components/columns.tsx`）。
- 用量统计：去掉线路筛选、去掉趋势拆分与分组维度中的 `route`
  （`pages/usage/hooks/use-usage-filters.ts`、`components/usage-trend.tsx`、
  `components/usage-groups.tsx`）。
- `lib/api/requests.ts`、`lib/api/usage.ts` 去掉 `route_id` / `routes` / `UsageGroupBy="route"`。
- i18n 清理 `filters.route`、`groups.by.route`、`splitBy.route`。
- 侧栏「模型线路」占位页改为「可用模型」（新页面另行规划，不在本次范围）。

**验证**：`go test ./...` 全绿；三端手动回归——创建密钥、发起请求、查看请求记录与用量统计；
`rg -i "route" --glob '*.go'` 结果中不应再有领域 route（react-router 除外）。

**不可停止**：本阶段一旦开始须做完，中途停留会导致 schema 与代码不一致。

## 风险

1. **阶段 3 的计费一致性**是最高风险项。改造前后必须用同一组 usage 做逐位比对，
   任何差异都要归因到「倍率取值」而非「公式变形」。
2. **触发器重写**若逻辑有误，会在配置期放行亏本组合，且不会立即暴露。
   需要构造正反例各若干，覆盖绝对售价 / 倍率两条路径 × Standard / Fast 两档。
3. **限流维度切换**期间，Redis 新旧 key 并存会导致额度计算错乱。
   开发环境用 FLUSHDB 规避；将来上生产需要停机窗口或双写过渡。
4. **阶段 6 涉及三个仓库**，任一仓库漏改会导致接口 404 或字段缺失。
   建议三仓库同时改、同时验证，不分批发布。
5. **Blueprint 的 ADR-0002 / 0012 / 0019** 整套建立在 route 之上，
   本次代码改造完成后需要一并重写（按 AGENTS.md，改造完成再更新 Blueprint）。

## 约束

1. 未经用户明确批准不修改生产代码；本计划本身不含代码改动。
2. 修改 `migrations/` 或 `sql/queries/` 后必须运行 `sqlc generate`，不手改生成文件。
3. Go 代码运行 `gofmt`，每阶段结束跑 `go test ./...`。
4. 本次不操作远程环境、不提交、不推送。
5. Blueprint 文档在代码与测试证明行为之后再更新，不把计划写成事实。

## 待定项

以下在实施对应阶段前需要最终确认，不阻塞前序阶段：

1. 模型绝对售价的存储形式：`model_prices` 加列 vs 新建时间窗表。
2. `price_snapshots.price_ratio` 在绝对售价路径下写 `1.0` 还是置空。
3. 全局售价倍率的配置位置：`app_settings` 还是新表。
