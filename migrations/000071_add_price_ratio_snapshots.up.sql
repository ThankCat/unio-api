-- 为「客户售价快照」与「结算补偿任务」补记结算当时使用的线路倍率（DEC-026：客户售价 = 模型基准价 × 线路倍率）。
--
-- 背景：此前请求列表/详情的「线路倍率」是实时读 routes.price_ratio，「模型基准价」是用 售价 ÷ 倍率 倒推。
-- 管理员改倍率后，历史请求会显示当前倍率（而非结算当时的倍率），倒推出的基准价随之失真。
-- 快照结算当时的倍率后，历史请求恒显示当时真实倍率、基准价倒推也随之稳定，不再被后续改倍率污染。
--
-- 列可空：迁移前的历史行没有该快照，展示端对 NULL 回落为「—」（当时倍率未记录，不臆造当前值）。
ALTER TABLE price_snapshots
    ADD COLUMN price_ratio NUMERIC(20, 10) CHECK (price_ratio IS NULL OR price_ratio >= 0);

-- 补偿任务重放 settlement 时用它写入 price_snapshots.price_ratio，保证重放账单与首次一致。
ALTER TABLE settlement_recovery_jobs
    ADD COLUMN price_ratio NUMERIC(20, 10) CHECK (price_ratio IS NULL OR price_ratio >= 0);
