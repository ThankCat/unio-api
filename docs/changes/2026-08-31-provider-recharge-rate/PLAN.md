# 服务商充值汇率（provider_recharge_rates）改造 — Gateway 侧

> 主计划：`unio-admin/docs/PLAN-provider-currency-and-unified-usd-2026-08.md`（已批准，阶段 0 决策已确认）。
> 前提：开发环境，改造完成后删库重建；不做历史数据迁移或兼容期双读。

## 范围（本仓库）

充值汇率从渠道级（`channel_recharge_factors`）迁移到服务商级（`provider_recharge_rates`），
账本分录落事件时 USD 折算快照，D-02 严格拦截（渠道启用校验 + 路由候选过滤 + 结算兜底告警）。

## 实施状态

- [x] 迁移 000065：建 `provider_recharge_rates`（EXCLUDE 窗口 + rate>0 + 审计字段 + 毛利守卫触发器）；
      重写 `margin_violations_current` B/C 分支（crf → prr）；`cost_snapshots` 充值列替换为
      `provider_recharge_rate_id`/`provider_recharge_rate`；`settlement_recovery_jobs` pin 列改名换 FK；
      `provider_ledger_entries` 加 `amount_usd`/`fx_rate`/`fx_rate_date`（三态 CHECK）；drop 旧表。
      已在临时 Postgres 验证 up/down/up 循环。
- [x] SQL：新增 gateway/shared `provider_recharge_rates.sql`；`FindModelCandidates` 充值 LATERAL 改
      provider 级并加 `recharge.id IS NOT NULL`（D-02 候选过滤）；`ModelRuntimePool`、admin `model.sql`
      供给面板、`ChannelsOpsTable`（移除 recharge_factor 列）、requests（`provider_recharge_rate` +
      `cost_fx_rate_date`）、provider ops（current_recharge_rate* + balance fx）、账本查询/写入同步。
- [x] Go：settlement pin/解析改 provider 级（缺失兜底 1.0 + zap 告警）；routing 候选/pin 改名；
      probe 成本按 provider 解析；providerledger 全部分录落 USD 快照（usage debit 复用结算钉档值）；
      渠道启用校验（Create/Update → enabled 前置 FindActiveProviderRechargeRate）；
      新增 providerrechargerate service + provider recharge-rates handler（`GET/POST
      /providers/{id}/recharge-rates`、`PATCH /provider-recharge-rates/{id}`）；删除渠道充值倍率
      service/handler/路由；providerbalance 调额响应带 USD 快照。
- [x] 测试：`go build ./...`、`go vet`、`go test ./...` 全部通过；新增 D-02 渠道启用闸门单测；
      settlement 集成测试 fixture 迁移到 provider 级。

## 收尾

Blueprint 更新与本文件删除待 Admin 前端联调完成后一并执行。
