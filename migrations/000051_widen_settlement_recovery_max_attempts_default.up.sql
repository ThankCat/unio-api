-- P1-4：放宽 settlement 补偿任务的默认最大重试次数 10 -> 20。
-- 与 worker 退避上限（默认 5m）一起把「上游已成功但 settlement 反复失败」的总补偿覆盖窗口
-- 从 ~4 分钟拉长到 ~1 小时级，覆盖 DB/网络短时故障，避免过早 dead 导致请求被收口为 failed + 平台白白承担风险敞口。
-- 新 job 由应用层显式写入配置值（WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS），此默认值仅作 schema 兜底与文档对齐。
ALTER TABLE settlement_recovery_jobs
    ALTER COLUMN max_attempts SET DEFAULT 20;
