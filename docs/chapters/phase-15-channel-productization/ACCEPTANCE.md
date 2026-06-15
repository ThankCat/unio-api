# 阶段 15 验收 - 渠道商品化 + 策略路由

## 功能验收

- [x] 价格下沉到渠道-模型级：`channel_prices` 一行同时含售价（必填）+ 成本（可空）；退役 `prices`/`channel_cost_prices`。
- [x] 录入守卫：任一分项「售价 < 成本」被拦下（DB `ck_channel_prices_margin` 硬拦 + service 可读报错 + 前端飘红禁止提交）。
- [x] 内置线路「经济」(cheapest, all) / 「稳定」(stable, all) 系统判定零配置、只读不可删（迁移种子）。
- [x] 自定义线路：mode（cheapest/stable/fixed）+ 池（all/explicit），fixed 恰好一条渠道（service 校验 + 前端校验）。
- [x] API Key 绑线路、项目设默认线路；解析优先级 key ?? project ?? 内置经济。
- [x] cheapest 按售价升序选路（单测覆盖）；stable 按熔断健康分；fixed 单候选不 fallback。
- [x] 候选过滤：未定价渠道（无当前生效 `channel_prices`）被排除，不参与计费。
- [x] 收入按实际命中渠道售价计费（结算时按 channel/model/attempt 时点重查 `channel_prices`），与成本同源。
- [x] 预授权保守上界：渠道未定时取候选池「按本次 token 估算最贵」售价冻结，命中只会更便宜（不超扣）。
- [x] 成本分项为空 → 成本快照该分项记 0（成本未知保守入账）。

## 生产/数据验收

- [x] 迁移 `up → down 6 → up` 可逆；开发库重置后无旧价污染。
- [x] `price_snapshots` / `cost_snapshots` / `settlement_recovery_jobs` 价格外键均改挂 `channel_prices`（含 PLAN 漏列的 recovery price_id）。
- [x] 渠道/模型删除级联清理 `channel_prices`；被账务/线路池引用时返回 conflict（DeleteChannelCascade/DeleteModelCascade）。
- [x] 运行时契约不变：ingress 双协议、能力闸门、ledger 双分录 / `formula_version` 不变。
- [ ] `/v1/models` 随 fixed/explicit 线路收敛模型可见性（剩余项；`all` 线路不受影响）。

## 测试验收

- [x] 后端 `go build/vet ./...` 干净；`go test ./...` 全绿（51 包，含 DB 集成）。
- [x] 渠道价 DB 集成：`channel_prices` 守卫 CHECK / EXCLUDE 窗口（沿用 model/channel 测试改造）。
- [x] 计费 DB 集成：按命中渠道售价计费、attempt 时点取价不被后续改价影响、双快照挂 `channel_prices`、幂等重放、回滚。
- [x] reservation 取候选池最贵售价做保守冻结（authorization 单测）。
- [x] cheapest 排序单测（`PrepareCandidates` Mode=cheapest 按售价升序）。
- [x] 前端 `tsc -b` / `eslint`（改动文件）/ `vite build` 全绿。
- [ ] stable 排序 / fixed 不 fallback 的专项端到端测试（剩余项）。

## 文档验收

- [x] `PLAN.md` 任务勾选与状态更新（含实现期偏差与剩余项）。
- [x] `STATUS.md` / `ACCEPTANCE.md` 建立。
- [x] chapters `README.md` 索引登记阶段 15 状态。
- [ ] `PROJECT_STATUS.md` / `DECISIONS.md` 收口时补充。
