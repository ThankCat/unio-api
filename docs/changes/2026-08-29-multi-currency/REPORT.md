# 多货币支持实施报告

**日期**：2026-08-29
**范围**：PLAN.md v1.6 的 P0–P3 全量实施（项目未上线，无灰度阶段）
**结论**：全部交付并验证通过。两条河原则落地——客户全链路 USD、provider 全链路原币（**绝对路径与倍率路径统一**），记账零换汇；汇率只出现在守卫比较、结算钉档、展示三个边界。

> **D2 修订（v1.6，用户拍板）**：倍率路径成本按 **provider 结算币种**记账——基准价数值 × 价格倍率 × 充值倍率 = 原币金额；充值倍率只承载「1 单位上游名义额度花多少原币」（1:1 CNY 充值 = 1.0），**不再把汇率折进倍率**。此前 v1.5 曾把 starapi 归入「名义 USD + 充值倍率折汇率」路径（factor 1.0 → 1/6.742068），与 §12.C.2 决策矛盾且让汇率波动无法反映到毛利，已由迁移 000059 + data-fix-v2.sql 纠正。

---

## 一、交付清单

### 数据库（迁移 000054–000058，已应用至开发库，up/down 均验证）

| 迁移 | 内容 |
|---|---|
| 000054 | `exchange_rates` 表（多源汇率，追加不删，`ORDER BY rate_date DESC, fetched_at DESC LIMIT 1` 唯一消费口径） |
| 000055 | `providers.currency`（CHECK USD/CNY；存量按盘点结论统一置 CNY） |
| 000056 | `cost_snapshots` 钉汇率三列（`fx_rate`/`fx_rate_date`/`total_cost_amount_usd` + CHECK 四象限约束） |
| 000057 | 一渠道一币种守卫触发器（`channel_prices.currency == provider.currency`，D3） |
| 000058 | 毛利守卫 v2：违规查询抽出为视图 `margin_violations_current`（触发器与复查 worker 共用口径）；绝对成本分支跨币种按最新汇率乘法比较（`sale × rate ≥ cost`，零除法舍入）、缺汇率=违规。视图 DDL 以 DO/EXECUTE 包裹——sqlc 无法解析 LATERAL VALUES 形态的视图，美元引号对其不透明而 migrate 语义不变 |
| 000059 | 毛利守卫 v3（D2 修订）：倍率分支 B/C 币种感知——成本币种 = provider 结算币种，跨币种时 `sale_ratio × rate ≥ multiplier × factor`（B，基准价约分后纯乘法）/ 逐项 `sale × rate ≥ base × multiplier × factor`（C），缺汇率=违规；A/D0/D1/D2 与 000058 一致 |

### 后端（unio-gateway）

- **`internal/core/fx`（新增）**：汇率读取（进程内 1 分钟 TTL 缓存）、big.Rat 精确换算（单次舍入 scale 10）、多源拉取链（ExchangeRate-API → open.er-api.com → Frankfurter，json.Number 直通 big.Rat 不经 float64）、合理性校验（跳变 >5% 拒收、CNY 区间 [5,10]）。
- **billing**：`ValidateNonNegativeMarginFX` / `ProviderCostToSaleRatioFX`（跨币种乘法口径）；旧同币种函数语义不变（现有测试零改动）；新增 `ErrMissingFxRate`。
- **routing**：`WithFxRates` 注入；跨币种候选解析一次最新汇率；缺汇率候选剔除（宁可少赚不算错，D11）。
- **settlement**：`WithFxRates` 注入（gateway 三协议 + worker recovery 四处装配）；非 USD 成本快照钉 `fx_rate/fx_rate_date` 并写 `total_cost_amount_usd`（big.Rat 精确除、单次舍入）；provider 账本扣款保持原币语义不变；恢复重放读既有快照、不重解析汇率（S7-R 天然成立）。
- **倍率路径币种覆写（D2 修订）**：新增 `GetChannelProviderCurrency` 查询；routing 候选（standard/fast）、settlement 三条解析路径（pin/回退/fast）、探测扣款（providerledger probe）在倍率路径统一把成本快照币种覆写为 provider 结算币种——路由毛利、守卫、结算钉汇率、探测扣款四处口径一致。
- **三个新 worker**（注册于 worker-server）：`fx_rate_sync`（每 6h 多源拉取+校验+陈旧分级告警）、`fx_margin_recheck`（每小时读违规视图，存量负毛利写 critical 站内消息，D6 告警不硬卡）、`fx_reconciliation`（每日跑不变量 I1–I4，违规写 critical 消息）。告警全部走 admin 消息中心（dedupe 防轰炸）。
- **admin API**：`GET /exchange-rates/latest`、`GET/POST /exchange-rates`（手工录入兜底，区间校验）、`POST /exchange-rates/validate-key`（实时调主源回显汇率）；provider CRUD 必填 currency；余额调额从「只接受 USD」改为「只接受 provider 原币」（留空默认原币）；ops 查询透出原币余额+币种+USD 折算+汇率；设价面板查询（GetModelOpsChannels）返回成本币种与 USD 折算（与守卫同源汇率）。
- **报表清扫（D8）**：成本聚合 6 处改读 `total_cost_amount_usd`（model.sql×2、overview.sql×3、provider.sql×1）；provider 余额 5 处改按 provider 币种行 + 汇率折算（含低余额 USD 等值口径）；营收/客户侧 `currency='USD'` 过滤按 D9 刻意不动。
- **配置**：`EXCHANGE_RATE_API_KEY` 双层解析（app_settings `gateway.exchange_rate_api_key` 优先、环境变量兜底，热改生效）；6 个 env 文件已补；`EXCHANGE_RATE_SYNC_INTERVAL`（缺省 6h）。

### 前端（unio-admin）

- `formatMoney` / `formatMoneyPrecise` / `formatDualCurrency`（¥7.00 (≈ $0.98)）格式化工具。
- Provider 创建表单必选结算币种（创建后不可改）；余额弹窗币种只读跟随 provider、金额标签随币种；余额展示（列表/详情/驾驶舱 breakdown）原币为主 + ≈USD 辅；账本流水按原币显示。
- 设价面板（ModelSupplyMarginPanel）：成本列 USD 折算值为主（毛利比较口径，与守卫同源）、非 USD 原币括注、缺汇率标红提示、汇率脚注。
- 渠道成本录入（ChannelPricesDialog）：币种只读跟随 provider（防呆杜绝 1:1 错标复发）；非 USD 录入时实时显示 USD 等值；缺汇率时明确提示先录汇率。
- 系统设置新增「汇率」Tab：最新汇率卡（值/汇率日/来源/年龄/陈旧告警）、手工录入兜底、历史列表、「验证 API Key」按钮（实时回显主源汇率，支持验证候选 key）。
- console 零改动（D9）。

### 蓝图（unio-blueprint，12 份文档）

新增 **DEC-056** 多货币决策条目（adr-0003 登记表 + DEC-027 修订关系）；删除 DEC-021「不引汇率」禁令改为可审计换算要求（adr-0001 + dashboard/observability/management/overview/quality 五份 admin 文档）；billing-settlement 调额改原币 + 补钉汇率语义；adr-0020 加交叉引用。**规范外顺改两处正面冲突**：provider-adaptation 的「运行时没有汇率换算、币种不一致 fail closed」、routing-load-balancing 候选资格「可比较币种」表述。

### 数据修正（data-fix.sql + data-fix-v2.sql，均已提交，deferred 守卫复验通过）

盘点勘误：开发库无 channel_prices 绝对成本、无绝对售价；「售价 1.25/6.25 错标」的实体是 **sale_price_ratio**（5/25 基准 × 0.25）。

- **v1（保留）**：售价倍率 ÷ 当日汇率 6.742068：0.2 → 0.0296644887、0.25 → 0.0370806109（claude-opus-4-8 售价 0.1854/0.9270 USD，与原 CNY 数字等值）。
- **v1（已撤销）→ v2**：v1 曾把充值倍率 1.0 → 1/6.742068（汇率折入倍率）；D2 修订后撤销，**倍率回归 1.0**——成本按 CNY 原币记账（claude-opus 输入成本 = 5 × 0.12 × 1.0 = **0.6 CNY ≈ $0.0890**，按当日汇率动态折算），守卫分支 B 复验 `0.0371 × 6.742 ≈ 0.25 ≥ 0.12`、`0.0297 × 6.742 ≈ 0.20 ≥ 0.10` 通过，`margin_violations_current` 零行。
- 测试残留的 chat-settlement-* 供应商/渠道（无模型绑定）未动。

## 二、验证证据

- **测试**：gateway `go test ./...` 全绿（含 DB 触发器测试）；admin 前端 typecheck + eslint + 239 个测试全绿；fx 包 7 组单测（精确换算/单次舍入/乘除等价/合理性校验/降级链/缓存/缺 key）。
- **守卫 DB 测试（新增 9 个）**：跨币种盈利放行/亏本拒绝/缺汇率拒绝/边界相等放行/取最新汇率/币种一致性触发器/**倍率路径跨币种（B 分支盈利放行+亏本拒绝）/倍率路径缺汇率拒绝**/**Go-SQL 双守卫等价性（5 组 fixture 判定完全一致，§9.3 护栏落地）**。
- **金流不变性（8.6）**：现有测试断言除计划内四处夹具补列外零改动；USD 链路语义不变（同币种函数原样保留）。
- **端到端冒烟（真实 admin-server + 开发库）**：latest 返回 er-api 实拉 6.742068；validate-key 实时调通主源返回 6.742002；providers ops 透出 currency=CNY；设价面板成本口径（D2 修订后实库验证）：Plus分组2 0.2/0.4/0.5 CNY、特价高缓CC分组 0.6 CNY，USD 折算 0.0297/0.0593/0.0742/0.0890（≈ 原币 ÷ 6.742068，动态跟随汇率）。
- **生产级意外验证**：热加载 worker-server 在开发期间真实跑了 fx_rate_sync——主源因进程未载入 key 失败，**自动降级到 er-api 拉到真实汇率入库**，多源降级链在无人工干预下按设计工作。

## 二点五、追加修复与 e2e 验证（2026-08-29 晚）

### 请求记录费用链路修复（用户报告「费用计算全错」）

根因：请求列表/详情 API 把成本快照的**原币金额**塞进名为 `*_usd` 的字段返回，前端照 USD 渲染——CNY 成本 ¥0.095 显示成 $0.095，毛利用 USD 售价直减 CNY 数值算出假负毛利。数据层本身正确（快照钉档 fx 与 USD 折算列都对），错在展示链路的字段语义。修复：

- **API 诚实命名**：列表 `*_cost_usd`→`*_cost_amount`（原币）、`*_cost_unit_usd`→`*_cost_unit`（原币单价）；新增 `cost_currency` / `cost_fx_rate` / `total_cost_amount`（原币）；`total_cost_usd` 改为真 USD（读 `total_cost_amount_usd`）。详情 `cost_snapshot` 补 `currency` / `fx_rate` / `total_cost_amount_usd`。售价侧字段（恒 USD）不动。
- **费用明细组件**（cost-breakdown）：渠道成本区单价/分项/合计按原币符号渲染，合计带 `≈ $x` 折算辅注与钉档汇率脚注；毛利 = 用户价格（USD）− 成本 USD 折算值，两河不混算；缺折算时明示「缺 USD 折算」。

### 全站金额显示规则统一（用户决策）

两档规则，均**四舍五入 + 裁剪尾零 + 微额保护**（非零金额四舍五入后为 0 显示 `<最小刻度`）：

- **常规档（≤3 位小数）**：汇总卡片、余额、列表——`formatUSD` / `formatMoney`（0.020 → $0.02，40.00 → ¥40）。console `formatUsd` 同步此档。
- **对账档（≤6 位小数）**：请求中心费用列/费用明细/毛利、客户与服务商账本分录——`formatUSDPrecise` / `formatMoneyPrecise`（$0.03944、¥0.094656、探测 ¥0.000037），分位以下可见，方便对账。顺带清理散落的裸格式化：客户账本列/请求详情账本分录与计费异常（`formatNumeric`→币种感知 `formatMoneyPrecise`）、渠道价格弹窗手拼 `$`（→`formatUSD`）、余额调整 toast。金额页面全量核查：驾驶舱卡片+breakdown 表、实时流量、服务商列表/详情/账本/调额、渠道价格/倍率、模型设价面板、请求列表/详情、客户账本、用户列表、系统汇率——全部走统一 formatter 且币种正确（单价/倍率/汇率保留有效数字策略，非金额总额）。

### e2e 金额矩阵（脚本入档：e2e-money-check.py，真实 admin-server + 开发库）

**72/72 PASS**。覆盖：请求列表每行（CNY 行钉汇率 + `usd≈amount/fx`、USD 行 `usd==amount`、分项和==总额、API==DB 快照逐字段）；请求详情（cost_snapshot 三字段 + user_charge==USD 账本净额）；最新汇率 API==DB；服务商余额==原币账本净额、`balance_usd≈balance/最新汇率`；设价面板成本 `usd≈原币/汇率`（6 渠道×模型行）；驾驶舱 breakdown 每 provider 行 cost_usd==DB `SUM(total_cost_amount_usd)`（显式窗口下**精确一致**）；客户账本恒 USD、新 provider 分录币种==provider 币种、守卫零违规、非 USD 快照 fx/usd 完整性全量复核。实测验证：新代码触发渠道检测，探测扣款正确按 CNY 入账（分录 626）。

已知历史遗留（不修，账本不可变 + §12.C.1 拍板）：D2 修订前的 provider 账本分录保留错标时代的 USD 标签（388 条），其金额不计入现 CNY 余额行——StarAPI 实际剩余额度需人工按上游对账；两条 17:21 的 USD 探测分录为旧二进制进程所写。

### 生命周期状态机与删除护栏（2026-08-29 用户反馈）

- **操作菜单按状态显示**（启用 → 停用 → 归档 → 删除）：服务商/渠道的「归档」只在停用态出现；归档态只显示「恢复/删除」（原有）；模型的「删除」只在停用态出现。
- **模型删除后端护栏**：启用中的模型拒绝删除（409，先停用），与前端菜单一致，防 API 直调绕过。
- **服务商删除放宽**：纯手工调额分录（adjustment_credit/debit，无真实交易归因）与余额缓存行属管理员自录数据，随删除一并清理——测试服务商设过余额也能删；存在任何交易性分录（usage/probe）时账本保留原样、外键照旧拦下删除。已对用户报告的测试服务商（id 1288，仅一条 $123 手工注资）事务演练删除成功（演练已回滚，数据未动）。

## 三、偏差与未尽事项

1. **需求文档勘误已落 PLAN v1.5**：`provider_costs`/绝对售价错标均与实际数据形态不符，修正方案按倍率路径实体执行（见上）。
2. **对账运行记录表（reconciliation_runs）与 admin 对账页未做**：MVP 以站内 critical 消息 + 结构化日志为记录（§9.2 的「7 天对账矩阵」UI 后续按需补）。
3. **不变量 I5（客户侧复算抽检）与 §9.4 历史复算周任务未实现**（I1–I4 已跑在每日 worker）。
4. **§7 Prometheus 指标未单独埋点**：告警语义已由站内消息 + `alert` 结构化日志字段承载；指标面板后续接现有 metrics 体系时补。
5. **channel_prices 绝对成本路径（CNY 直接报价）目前无真实数据行使用**——全链路（守卫/结算/展示）已实现并有 DB 测试覆盖，出现第一家直接 CNY 报价供应商时按 PLAN 8.7 手工清单验收一遍即可。
6. **运行中的 dev admin-server（air）在实施期间曾因中间态编译错误退出**，需要在终端里重新 `make dev-admin`（worker/gateway 的 air 会自动重建到最新代码）。
