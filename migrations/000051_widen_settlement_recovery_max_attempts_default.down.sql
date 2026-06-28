-- 回滚：恢复 settlement 补偿任务默认最大重试次数为 10。
ALTER TABLE settlement_recovery_jobs
    ALTER COLUMN max_attempts SET DEFAULT 10;
