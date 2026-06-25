-- 回滚 partial_stream_estimate 取值域。注意：若库中已存在 partial_stream_estimate 行，
-- 重新加约束会失败；回滚前需先清理或迁移这些行。

ALTER TABLE settlement_recovery_jobs
    DROP CONSTRAINT settlement_recovery_jobs_usage_source_check;
ALTER TABLE settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_usage_source_check
        CHECK (usage_source IN ('upstream_response', 'upstream_stream'));

ALTER TABLE usage_records
    DROP CONSTRAINT usage_records_usage_source_check;
ALTER TABLE usage_records
    ADD CONSTRAINT usage_records_usage_source_check
        CHECK (usage_source IN ('upstream_response', 'upstream_stream'));
