-- 000075 down: 回滚 cache_write_30m 维度（恢复各表原 check 约束并删除新增列）。

-- 6) settlement_recovery_jobs
ALTER TABLE settlement_recovery_jobs DROP CONSTRAINT ck_settlement_recovery_jobs_non_known_values_zero;
ALTER TABLE settlement_recovery_jobs ADD CONSTRAINT ck_settlement_recovery_jobs_non_known_values_zero CHECK (
    (usage_uncached_input_tokens_state = 'known' OR usage_uncached_input_tokens = 0)
        AND (usage_cache_read_input_tokens_state = 'known' OR usage_cache_read_input_tokens = 0)
        AND (usage_cache_write_5m_input_tokens_state = 'known' OR usage_cache_write_5m_input_tokens = 0)
        AND (usage_cache_write_1h_input_tokens_state = 'known' OR usage_cache_write_1h_input_tokens = 0)
        AND (usage_output_tokens_total_state = 'known' OR usage_output_tokens_total = 0)
        AND (usage_reasoning_output_tokens_state = 'known' OR usage_reasoning_output_tokens = 0)
);
ALTER TABLE settlement_recovery_jobs
    DROP COLUMN usage_cache_write_30m_input_tokens,
    DROP COLUMN usage_cache_write_30m_input_tokens_state,
    DROP COLUMN cache_write_30m_input_price;

-- 5) usage_records
ALTER TABLE usage_records DROP CONSTRAINT ck_usage_records_non_known_values_zero;
ALTER TABLE usage_records ADD CONSTRAINT ck_usage_records_non_known_values_zero CHECK (
    (uncached_input_tokens_state = 'known' OR uncached_input_tokens = 0)
        AND (cache_read_input_tokens_state = 'known' OR cache_read_input_tokens = 0)
        AND (cache_write_5m_input_tokens_state = 'known' OR cache_write_5m_input_tokens = 0)
        AND (cache_write_1h_input_tokens_state = 'known' OR cache_write_1h_input_tokens = 0)
        AND (output_tokens_total_state = 'known' OR output_tokens_total = 0)
        AND (reasoning_output_tokens_state = 'known' OR reasoning_output_tokens = 0)
);
ALTER TABLE usage_records
    DROP COLUMN cache_write_30m_input_tokens,
    DROP COLUMN cache_write_30m_input_tokens_state;

-- 4) cost_snapshots
ALTER TABLE cost_snapshots DROP CONSTRAINT ck_cost_snapshots_total_amount;
ALTER TABLE cost_snapshots ADD CONSTRAINT ck_cost_snapshots_total_amount CHECK (
    total_cost_amount =
        uncached_input_cost_amount
        + cache_read_input_cost_amount
        + cache_write_5m_input_cost_amount
        + cache_write_1h_input_cost_amount
        + output_cost_amount
        + reasoning_output_cost_amount
);
ALTER TABLE cost_snapshots
    DROP COLUMN cache_write_30m_input_cost,
    DROP COLUMN cache_write_30m_input_cost_amount;

-- 3) price_snapshots
ALTER TABLE price_snapshots DROP COLUMN cache_write_30m_input_price;

-- 2) channel_prices
ALTER TABLE channel_prices DROP COLUMN cache_write_30m_input_cost;

-- 1) model_prices
ALTER TABLE model_prices DROP COLUMN cache_write_30m_input_price;
