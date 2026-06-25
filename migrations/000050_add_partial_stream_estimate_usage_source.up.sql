-- Stream partial settlement（TASK-7.23 / DEC-025）落地后，partial 路线（B/D）合成的
-- usage facts 以 usage_source='partial_stream_estimate' 写入 usage_records，并在触发 settlement
-- recovery 时随 job 持久化。原 CHECK 仅允许 upstream_response / upstream_stream，导致 partial
-- 结算 INSERT 与 recovery job 落库被拒（SQLSTATE 23514）。此迁移把 partial_stream_estimate
-- 纳入两处 usage_source 取值域。

ALTER TABLE usage_records
    DROP CONSTRAINT usage_records_usage_source_check;
ALTER TABLE usage_records
    ADD CONSTRAINT usage_records_usage_source_check
        CHECK (usage_source IN ('upstream_response', 'upstream_stream', 'partial_stream_estimate'));

ALTER TABLE settlement_recovery_jobs
    DROP CONSTRAINT settlement_recovery_jobs_usage_source_check;
ALTER TABLE settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_usage_source_check
        CHECK (usage_source IN ('upstream_response', 'upstream_stream', 'partial_stream_estimate'));
