# 改造设计:运行时配置分域(第二批)——admin_backend / admin_frontend 域 + 健康与告警阈值迁移

> 状态:**已实施(2026-07-09)。** 落地映射:codec=`appsettings/admin_backend_settings.go`、
> `admin_frontend_settings.go`;分桶收拢=`opsutil.HealthBucket`(参数化);消费接线=channelops/
> channelhealth/dashboard/providerops 四 service + `ChannelsOpsHealthDistribution` SQL 参数化;
> 前端=`useMetricThresholds` hook + `metrics.ts` 纯函数参数化 + `RuntimeSettingsPanel.tsx` 分域 Tab。
> 建立:2026-07-09。依赖:`DESIGN-runtime-settings.md`(基础设施)、`DESIGN-env-to-runtime-settings-migration.md`(第一批 6 组 gateway 配置,已实施)。
> 用户已定:**① 配置按域分开(前端的归前端、后端的归后端,admin 与 gateway 分开);② 本批范围 = 域架构 + 前端告警阈值(admin_frontend.\*)+ 渠道健康阈值(admin_backend.\*);③ 域名用 `admin_backend`/`admin_frontend`;④ 毛利偏薄阈值纳入;⑤ 设置页按域分 Tab;⑥ 前端/后端成功率阈值不合并**。

---

## 0. TL;DR / 审核要点

把「配置域」formalize:同一套存储/分发基础设施(app_settings + Redis + 注册表)不动,**用 key 前缀 + 注册表 Category 划分四个域**,各域消费方独立、后台设置页按域分 Tab。本批迁入两个新配置项,正好各验证一个新域:

1. `admin_backend.channel_health_thresholds` —— 渠道健康分桶阈值(0.95/0.80),现散落 **4 个 Go 包 + 1 处 SQL**,admin 后端消费;
2. `admin_frontend.dashboard_thresholds` —— 仪表盘告警灯阈值(成功率 SLO、TTFT、延迟、毛利率偏薄),现硬编码在前端 `metrics.ts`,**前端消费,后端只存储+校验+下发**。

---

## 1. 参考调研:new-api / sub2api 怎么分域(源码/文档实证)

### new-api(本地源码 `~/Project/new-api`,直接读的)

- **存储不分域**:一张 `options` 表(key-value 字符串)装所有配置——运营、系统、支付、以及前端要用的 Logo/公告/面板开关。
- **分域靠三层机制**:
  1. *key 前缀*:新式配置用 `console_setting.api_info` 形式的 `域.字段` 键,`handleConfigUpdate` 按前缀分发到 `config.GlobalConfig.Register("console_setting", &struct)` 注册的域结构体(`model/option.go:519`);
  2. *后端包分域*:`setting/` 下按域分包——`operation_setting`(运营)/`system_setting`(系统)/`console_setting`(前端控制台)/`ratio_setting`(倍率)/`performance_setting`(性能),各自带类型化结构体 + 校验(`console_setting/validation.go`);
  3. *前端页面分域*:设置页按域分 Tab(运营/仪表盘/系统/聊天/绘图/支付/倍率/限流/性能/其他),每个 Tab 只读写自己域的 key(`web/src/pages/Setting/index.jsx`)。
- **前端配置的消费**:公开端点 `GET /api/status`(`controller/misc.go:42`)把前端需要的配置一次性下发,前端启动时拉取;**改配置走 admin 设置页、存 options 表**——不存在独立的「前端配置存储」。
- **传播**:内存 OptionMap + 每 60s 从 DB 轮询(`SyncOptions`,`SYNC_FREQUENCY` 默认 60)。我们的 Redis 实时源 + 5s applier 优于它,不学。

### sub2api(仅公开文档,无源码)

- `config.yaml` 只管启动期(DB/Redis/端口/管理员向导);运行时配置全在管理后台「设置」下按域分页(如 设置 → 支付设置),存 DB。与 new-api 同构。

### 结论

两家都是:**存储统一、域靠 key 前缀 + 类型化域结构 + UI 分组;前端配置也存后端,前端只是多一个读取端点**。我们第一批已经无意中走在同一条路上(`gateway.circuit_breaker` 即 `域.字段`),本批只需把「域」补成正式约定。

---

## 2. 域模型(已定案)

| 域(Category) | key 前缀 | 消费方 | 传播方式 | 设置页 Tab | 现有/本批 key |
|---|---|---|---|---|---|
| `gateway` | `gateway.*` | gateway 进程热路径 | settingsApplier 5s 推送 / 包级 atomic 现读 | 网关 | 已有 6 组 |
| `anthropic`(Provider 策略) | `anthropic.*` 等 | gateway adapter | Provider 现读(store 三层缓存) | Provider 策略 | 已有 `anthropic.beta_policy` |
| `admin_backend` **(新)** | `admin_backend.*` | **admin 进程后端**(运维聚合 service) | 每请求经 store 现读(本地 3s 缓存,admin QPS 低,无需 applier) | 运营判定 | 本批 `admin_backend.channel_health_thresholds` |
| `admin_frontend` **(新)** | `admin_frontend.*` | **admin 前端**(后端只存储+校验+下发) | 前端 react-query 拉取(与设置面板共用同一查询,保存即失效重取) | 前端展示 | 本批 `admin_frontend.dashboard_thresholds` |

约定:

- **域 = 注册表 `Definition.Category`**,同时必须与 key 前缀一致(注册表单测断言,防手误)。
- 各域消费方**互不加载对方的域**:gateway applier 只订阅 `gateway.*`;admin 后端只读 `admin_backend.*`;前端只消费 `admin_frontend.*`(设置面板除外——它按域渲染全部)。这就是「admin 与 gateway 分开」的落点。
- **个人偏好不进本系统**:分页大小、主题等属于单个管理员的浏览器 `localStorage`(new-api 同样如此),与全局运行时配置是两回事,本批不做。
- 基础设施零改动:`app_settings` 表、`SettingsStore`、启动 seed、`GET/PUT /admin/v1/settings` 全部复用。**无 DB 迁移**(新 key 由启动 seed 补行)。
- **seed 与注册表仍全局共享**:gateway 与 admin 用同一 `DefaultRegistry`,故 gateway 启动也会 seed `admin_backend.*`/`admin_frontend.*` 缺行(幂等 DO NOTHING,无害)——「域分开」指**消费与展示**分开,不为每个进程拆注册表(拆了 seed 就不完整)。gateway 的 settingsApplier 只显式拉取 6 个 `gateway.*` key,新增域对它零影响。

---

## 3. 本批两个新配置项

### 3.1 `admin_backend.channel_health_thresholds`(渠道健康分桶阈值)

- **值形状**:`{"healthy_rate": 0.95, "degraded_rate": 0.80}`(区间内成功率;`_rate` 后缀表意比率,取值 (0,1])
- **默认**:0.95 / 0.80(与现硬编码一致,迁移不改行为)
- **校验**:`0 < degraded_rate < healthy_rate <= 1`
- **语义**:按区间内 attempt 成功率把渠道分桶——`>= healthy_rate` 为 healthy,`>= degraded_rate` 为 degraded,否则 unhealthy(无样本 no_data)。纯运维展示分类,不影响路由/计费。
- **HotReload: true**(admin 每请求现读,改后下一次请求生效)

### 3.2 `admin_frontend.dashboard_thresholds`(仪表盘告警灯阈值)

- **值形状**(时长按既定单位约定用 int 毫秒 + `_ms` 后缀;比率用 `_rate`/`_slo`/`_warn` 后缀的浮点):

```json
{
  "success_rate_slo": 0.95,
  "success_rate_warn": 0.80,
  "ttft_warn_ms": 5000,
  "ttft_danger_ms": 12000,
  "latency_warn_ms": 15000,
  "latency_danger_ms": 30000,
  "profit_thin_rate": 0.10
}
```

- **默认**:同上(与现前端硬编码一致)
- **校验**:`0 < success_rate_warn < success_rate_slo <= 1`;`0 < ttft_warn_ms < ttft_danger_ms`;`0 < latency_warn_ms < latency_danger_ms`;`0 <= profit_thin_rate < 1`
- **语义**:仪表盘/请求列表的着色档位——请求成功率 SLO 参考线与红黄灯、TTFT P95 红黄灯、完成延迟 P95 红黄灯、毛利率「偏薄」警示线。
- **HotReload: true**(前端下一次拉取生效;在设置面板保存后经 react-query 失效立即重取)

> 两个 key 不合并(§11 决策 4):后端阈值给**渠道**成功率分桶,前端 `success_rate_*` 给**请求**成功率着色——指标主体不同,允许独立调档;描述里注明默认对齐。
> `profit_thin_rate` 纳入(§11 决策 2):与其余告警灯同族(运营判定档位),同卡片一起编辑,增量成本≈0。

---

## 4. 现状盘点(逐处核实,迁移时全部删除硬编码)

### 4.1 后端 0.95/0.80(5 处)

| 位置 | 形式 | 消费点 |
|---|---|---|
| `service/admin/opsutil/opsutil.go:17-18` | 导出常量 + `HealthBucket()` | **`providerops.go:183/251` 经此分桶**(providerops 自身无常量副本) |
| `service/admin/channelops/channelops.go:19-20` | 包内复制的常量 | `:463-465` 分桶 switch |
| `service/admin/query/channelhealth.go:12-13` | 包内复制的常量 | `:84-86` 分桶 switch |
| `service/admin/dashboard/dashboard.go:21-22` | 包内复制的常量 | `dashboard.go:436-439` 与 `radar.go:485-487`(同包共用) |
| `sql/queries/channels_ops.sql:71-73` | SQL 字面量 | `ChannelsOpsHealthDistribution` 三个 FILTER |

> 注:同一对阈值在 4 个 Go 包各存一份副本——本批顺手收拢到 `opsutil` 一处(消灭复制),再接运行时配置。
> 消费方 service 共 **4 个**:channelops、query(channelhealth)、dashboard、providerops。

### 4.2 前端 `unio-admin/src/components/dashboard/metrics.ts`(7 个常量)

`SUCCESS_RATE_SLO/WARN`、`TTFT_WARN_MS/DANGER_MS`、`LATENCY_WARN_MS/DANGER_MS`、`PROFIT_THIN_RATE`。
引用阈值相关导出的文件(约 10 个,grep 实测):`request-cells.tsx`、`DashboardPage.tsx`(SLO 参考线)、`TtftTip.tsx`、`LatencyTip.tsx`、`RequestSuccessTip.tsx`、`AttemptSuccessTip.tsx`、`breakdown-table/columns.tsx`、`AttemptSuccessRateCell.tsx`、`ProviderOverviewStats.tsx`、`ModelOverviewStats.tsx`(后两者经 `profitIntent`)。
(`SettlementTip/RevenueTip/CacheHitTip` 只引用无阈值函数,不受影响。)

---

## 5. 后端改造方案

### 5.1 注册与 codec(`appsettings`)

- 新文件 `admin_backend_settings.go` / `admin_frontend_settings.go`:各自定义类型化结构体 + `strictUnmarshal`(拒绝未知字段,沿用第一批口径)+ 校验 + `Definition`,注册进 `DefaultRegistry()`。
- `admin_frontend.*` 后端**没有 Go 消费方**——只有注册表(校验/默认/seed/面板下发),这正是「前端的配置是前端的」的后端形态。
- 注册表单测增加断言:**Category 与 key 前缀一致**(全量 key 扫一遍)。
- 新 typed reader 对 `store == nil` 回默认(admin service 单测可传 nil 走默认阈值,免造假 store)。

### 5.2 admin_backend 域接线(admin 后端)

1. **收拢**:删除 channelops / channelhealth / dashboard 三个包的复制常量,统一改调 `opsutil.HealthBucket(succeeded, total, healthy, degraded)`——签名加显式阈值参数(两个 float64,opsutil 不引入 appsettings 依赖,保持纯函数);
2. **注入**:`admin_server.go` 里把 `settingsStore` 的构造**提前到各运维 service 之前**,给 `channelops / query(channelhealth) / dashboard / providerops` **四个** service 的构造函数加 `*appsettings.SettingsStore` 参数;每请求开头调 `appsettings.AdminBackendChannelHealthThresholds(ctx, store)`(典型 typed reader:解码失败/nil store 回默认)取当前值传入分桶;
3. **SQL 参数化**:`ChannelsOpsHealthDistribution` 增加 `healthy_rate float8 / degraded_rate float8` 两个入参替换字面量,`sqlc generate` 重新生成,调用点传运行时值。

> admin 不新增 applier:store 本地 3s 缓存 + admin 低 QPS,现读即可,生效延迟 ≤3s。

### 5.3 只读面板

无变化(这两项本来就不在 env/只读面板里)。`.env` / `config.go` 亦零改动——本批没有任何 env 项被移除。

---

## 6. 前端改造方案

### 6.1 消费机制(核心)

- 新增 `useMetricThresholds()` hook:内部用 react-query 拉 `GET /admin/v1/settings`(**与运行时配置面板共用 queryKey `["runtime-settings"]`**),取 `admin_frontend.dashboard_thresholds` 的 value 解码为类型化对象;
- **fallback 语义**:加载中 / 请求失败 / 解码失败 → 回退前端内置默认常量(与注册表默认值同源同值,`metrics.ts` 保留一份 `DEFAULT_METRIC_THRESHOLDS`);颜色档位属展示层,短暂回退默认无风险;
- `metrics.ts` 的 `rateIntent / ttftIntent / latencyIntent / profitIntent` 改为**显式接收阈值参数的纯函数**,约 10 个引用文件改为从 hook 取阈值后传入(机械改动,清单见 §4.2);`DashboardPage` 的 SLO 参考线同样从 hook 取值;
- 共用 queryKey 的收益:在设置面板保存 → `invalidateQueries` → 仪表盘颜色**立即**换档,无需刷新页面。

### 6.2 设置页按域分 Tab(§11 决策 3)

- 「运行时配置」页从单页卡片堆改为**域内分组 Tab**:网关 / 运营判定 / 前端展示 / Provider 策略(Anthropic beta 卡片挪入),对齐 new-api 的组织方式;
- 渲染仍由注册表驱动:按 `category` 分组;**未知 category 落「其他」Tab** 用现有 RawJSON 兜底编辑器(以后新增域不改前端也能管);
- `admin_frontend.dashboard_thresholds` 提供 typed 编辑卡:比率用小数输入,`*_ms` 用既有 DurationInput(数字+单位下拉);`admin_backend.channel_health_thresholds` 两个小数输入;沿用「已偏离代码默认」徽章。

---

## 7. 明确不做(本批边界)

- **个人偏好**(分页大小/主题/表格密度):属 `localStorage`,不进全局配置(§2);
- **公开 status 端点**:new-api 的 `/api/status` 是给「客户门户」用的;我们的前端就是 admin 本身,复用带鉴权的 `/admin/v1/settings` 即可,不开新面;
- **gateway 域新增项**(非流式响应体上限、计费三项):已登记为候选,不混入本批(本批主题是域架构,混入会扩大 review 面);
- **worker 域**:worker 进程未接 Redis/SettingsStore,其参数留 env(第一批已定,不翻案)。

---

## 8. 风险

| # | 风险 | 缓解 |
|---|---|---|
| R1 | admin_backend 阈值改坏(如 healthy < degraded)导致分桶错乱 | 写入口校验 `0 < degraded < healthy <= 1`,非法值 400 拒绝 |
| R2 | SQL 参数化后 `ChannelsOpsHealthDistribution` 行为回归 | sqlc 重新生成 + 该查询已有调用方测试;再补「传参与 Go 分桶口径一致」的单测 |
| R3 | 前端阈值拉取失败 → 颜色回退默认 | 展示层无风险;fallback 常量与注册表默认同值,回退不产生「错误档位」只产生「默认档位」 |
| R4 | 前端 fallback 常量与注册表默认漂移 | 面板「已偏离代码默认」徽章可见;doc 注明两处同源,改默认须同步(接受此轻微重复,换取前端零依赖启动) |
| R5 | 四个 admin service 构造签名变化,改动面扩散 | 纯注入式改动,编译器兜底;测试传 nil store 走默认阈值 |

---

## 9. 落地步骤(顺序,每步编译/测试绿)

1. **注册表 + codec**:`admin_backend_settings.go` / `admin_frontend_settings.go` + 单测(编解码/校验/默认/Category-前缀一致性断言)。零接线,零风险。
2. **opsutil 收拢**:`HealthBucket` 加阈值参数,删 3 个包的复制常量(行为不变的纯重构)。
3. **admin_backend 接线**:admin bootstrap 注入 store → 四个 service(channelops/channelhealth/dashboard/providerops)现读阈值;SQL 参数化 + sqlc 重新生成。
4. **前端消费**:`useMetricThresholds` hook + `metrics.ts` 纯函数参数化 + 约 10 个引用文件改造 + SLO 参考线。
5. **设置页分域 Tab**:按 category 分组渲染 + 两个新 typed 编辑卡 + beta 卡片挪入 Provider Tab。
6. **文档**:更新 `DESIGN-runtime-settings.md`(域约定章节)、本文件转「已实施」。
7. **全量验证**:后端 build/vet/单测(-race)、`sqlc generate` 无 diff 外漏、前端 tsc/lint/build;真机:改 admin_backend 阈值看渠道健康分布变化、改 admin_frontend 阈值看仪表盘颜色换档、`source=redis` 探针。

---

## 10. 测试计划

- **单元**:两个 codec 的编解码/校验/默认;注册表 Category-前缀一致性;`HealthBucket` 参数化后新旧口径等价;SQL 分布查询传参与 Go 分桶一致。
- **回归**:channelops / dashboard / channelhealth 现有测试保持绿(步骤 2 是行为不变重构)。
- **手动验收**:后台把 `healthy_rate` 从 0.95 改 0.99 → 渠道健康分布立刻变化;把 `ttft_warn_ms` 从 5000 改 1000 → 仪表盘 TTFT 卡变黄;改回后复原。

---

## 11. 决策记录(2026-07-09 用户拍板)

| # | 事项 | 决策 |
|---|---|---|
| 1 | 域枚举定名 | **`admin_backend` / `admin_frontend`**(用户指定,弃 `ops`/`frontend` 备选) |
| 2 | `profit_thin_rate` 是否随本批纳入 | **纳入** |
| 3 | 设置页形态 | **按域分 Tab** |
| 4 | 前端/后端成功率阈值是否合并为一个 key | **不合并**(渠道分桶 vs 请求着色,主体不同;默认对齐) |
