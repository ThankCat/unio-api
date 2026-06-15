# 阶段 15 状态 - 渠道商品化 + 策略路由

> 状态：in_progress（核心实现已落地并通过自动化验证；剩余少量运行时收敛/优化/文档收口）。

## 已完成（done）

- **迁移（000031–000036）**：`channel_prices`（售价+成本同表，毛利守卫 `ck_channel_prices_margin` +
  enabled 窗口 `EXCLUDE` + 复合唯一 `(id,channel_id,model_id)`）；`routes`（+内置「经济」cheapest/「稳定」stable 种子）；
  `route_channels`；`api_keys.route_id` / `projects.default_route_id`；`price_snapshots` / `cost_snapshots` /
  `settlement_recovery_jobs` 三处价格外键改挂 `channel_prices`；删 `prices` / `channel_cost_prices`。
  `migrate up → down 6 → up` 可逆验证通过。
- **sqlc 查询**：`channel_prices.sql`（CRUD + `FindActiveChannelPrice` 一次取售价+成本 + 窗口重叠校验）、
  `routes.sql`、`route_channels.sql` 新增；`api_keys.sql`/`projects.sql` 加线路读写、`GetAPIKeyByHash` 带出
  `route_id`+`default_route_id`；`channel_models.sql::FindRouteCandidates` 扩展（线路池过滤 + 已定价 LATERAL join +
  带出每候选售价向量）；级联删除查询改挂 `channel_prices`；退役 `prices.sql`/`channel_cost_prices.sql`。`sqlc generate` 干净。
- **routing（`internal/core/routing`）**：线路解析（`api_keys.route_id ?? projects.default_route_id ?? 内置经济`，
  停用/不存在回落）；`FindRouteCandidates` 带线路池 + at_time；候选携带 `SalePrice` + `ChannelPriceID`。
- **策略排序（`lifecycle.PrepareCandidates`）**：cheapest=按代表售价（output→uncached_input）升序；
  stable=按熔断健康分（窗口失败率，`ChannelCircuitBreaker.HealthScore`）升序；fixed=单候选（池=1，天然不 fallback）。
  排序叠加在能力过滤/熔断可用性之前，最终 fallback 顺序即策略顺序。
- **计费切换（`lifecycle`）**：authorization 冻结改取候选池「按本次 token 估算最贵」售价做保守上界（Go 侧计算，不超扣）；
  settlement 收入改按命中渠道 `FindActiveChannelPrice(channel, model, attempt.StartedAt)`（与成本同源同时点），
  `price_snapshots.price_id` 指向 `channel_prices`；成本分项空值按 0 入账；`settlement_recovery_jobs` 记录命中渠道售价；
  幂等校验去掉「快照 vs 授权价」比对，改为按存储快照重算并与 ledger 实扣对账。ledger 双分录 / `formula_version` 不变。
- **admin service**：`admin/channelprice`（售价必填+成本可选，分项「售价<成本」可读报错 + DB CHECK 兜底，窗口不重叠）；
  `admin/route`（CRUD + fixed/all/explicit 数量校验 + 事务建线路与渠道池 + 内置只读/不可删）；
  `customer` 加 key/project 线路绑定。
- **admin API**：`GET /channels/{id}/prices`、`POST /channels/{id}/models/{modelID}/prices`、`PATCH /channel-prices/{id}`；
  `GET/POST /routes`、`GET/PATCH/DELETE /routes/{id}`、`PUT /routes/{id}/channels`；API Key DTO/create/update 加 `route_id`；
  `PATCH /projects/{id}` 设默认线路；bootstrap 装配。
- **前端 `unio-admin`**：渠道「定价（售价/成本）」弹窗（同行售价+成本+实时毛利、售价<成本飘红禁止提交）替代独立成本价/模型售价；
  「线路」管理页（内置只读 + 自定义 CRUD + mode/池选择 + fixed 限一条）+ 侧栏入口；新建 API Key 选线路；
  `channelPrices.ts`/`routes.ts` API；退役 `prices.ts`/`costPrices.ts` 与对应弹窗。
- **验证**：后端 `go build/vet ./...` 干净、`go test ./...` 51 包全绿（含 DB 集成：渠道价守卫/窗口、按命中渠道计费、
  attempt 时点取价、reservation 取最贵候选、迁移 down/up）；前端 `tsc -b` / `eslint`（改动文件）/ `vite build` 全绿。

## 剩余项（todo）

- [ ] **`/v1/models` 随线路收敛**（PLAN §9 运行时）：`ListAvailableModelsForProject` 尚未对 fixed/explicit 线路
  只暴露其池内渠道能服务的模型；`all`（默认）线路不受影响。属可见性精修，不影响计费正确性。
- [ ] **缓存层**（PLAN §7）：线路解析与渠道价当前每请求直读 DB（routes 表极小、`channel_prices` 走索引点查）；
  后续接入与渠道/能力同款缓存。
- [ ] **一站式绑定**（PLAN §9）：建 `channel_models` 与 `channel_prices` 当前为两步；可合并为单事务接口 + 前端弹窗。
- [ ] **更多排序/计费集成测试**：stable 健康排序、fixed 不 fallback、reservation 不超扣的端到端覆盖（cheapest 排序已有单测；
  按命中渠道计费 + attempt 取价已有 DB 集成测试）。
- [ ] `ACCEPTANCE.md` 勾选到全绿并补 `PROJECT_STATUS.md`、必要时 `DECISIONS.md` 追加（倍率取舍 / 收入按渠道 / 授权保守上界）。

## 运行时影响与回归基线

- 新增运行时读：路由解析读 `routes`；`FindRouteCandidates` LATERAL join `channel_prices`（已定价过滤 + 带价）；
  settlement/recovery 按命中渠道读 `channel_prices`。
- 不改 ingress 协议契约、不改能力闸门 observe/enforce、不改 ledger 记账事实口径（双分录 / `formula_version` 不变）。
- 双协议 ingress（OpenAI Chat/Responses、Anthropic Messages）与熔断 fallback 行为不变；相关 service/DB 集成测试已适配新签名并通过。
