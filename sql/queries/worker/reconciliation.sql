-- 每日对账不变量（PLAN §9.1，fx-reconciliation worker 专用）。
-- 思路：同一笔钱的两条独立表示互相验证，任何一路被改坏当天报警。
-- 各查询 LIMIT 20：对账只需要「有没有 + 例子」，不需要全量清单。

-- name: ReconcileCostLedgerMismatch :many
-- I1｜成本分录 = 成本快照：每笔已结算请求的 provider 扣款金额/币种必须与成本快照一致。
SELECT cs.request_record_id, cs.total_cost_amount, cs.currency, ple.amount AS ledger_amount, ple.currency AS ledger_currency
FROM cost_snapshots cs
JOIN provider_ledger_entries ple
  ON ple.request_record_id = cs.request_record_id AND ple.entry_type = 'usage_debit'
WHERE cs.created_at >= sqlc.arg(from_time) AND cs.created_at < sqlc.arg(to_time)
  AND (ple.amount <> cs.total_cost_amount OR ple.currency <> cs.currency)
LIMIT 20;

-- name: ReconcileFxCompleteness :many
-- I2｜快照汇率完备性哨兵：CHECK 约束是主防线，此查询防「约束被误删」。
SELECT id
FROM cost_snapshots
WHERE created_at >= sqlc.arg(from_time) AND created_at < sqlc.arg(to_time)
  AND (
      (currency = 'USD' AND (fx_rate IS NOT NULL OR total_cost_amount_usd <> total_cost_amount))
   OR (currency <> 'USD' AND (fx_rate IS NULL OR fx_rate_date IS NULL))
  )
LIMIT 20;

-- name: ReconcileFxConversion :many
-- I3｜换算冗余一致：usd 归一列必须等于 原币总额 ÷ 钉住汇率 的单次舍入值（D4 审计抓手）。
-- round(numeric, 10) 为四舍五入，与 Go 侧 big.Rat half-up 单次舍入同口径（金额恒非负）。
SELECT id, total_cost_amount, fx_rate, total_cost_amount_usd
FROM cost_snapshots
WHERE created_at >= sqlc.arg(from_time) AND created_at < sqlc.arg(to_time)
  AND currency <> 'USD'
  AND total_cost_amount_usd <> round(total_cost_amount / fx_rate, 10)
LIMIT 20;

-- name: ReconcileProviderBalance :many
-- I4｜余额 = 分录累计（按 provider × currency）：余额行永远等于账本签名和（账本只追加）。
SELECT pb.provider_id, pb.currency, pb.balance, COALESCE(agg.total, 0)::numeric AS ledger_total
FROM provider_balances pb
LEFT JOIN (
    SELECT provider_id, currency,
        SUM(CASE WHEN entry_type IN ('usage_debit', 'probe_debit', 'adjustment_debit') THEN -amount ELSE amount END) AS total
    FROM provider_ledger_entries
    GROUP BY provider_id, currency
) agg ON agg.provider_id = pb.provider_id AND agg.currency = pb.currency
WHERE pb.balance <> COALESCE(agg.total, 0)
LIMIT 20;
