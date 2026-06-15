-- 还原快照 / 补偿任务外键到 prices / channel_cost_prices。
-- 依赖 000036.down 已先重建 prices / channel_cost_prices（迁移按逆序执行）。

ALTER TABLE settlement_recovery_jobs DROP CONSTRAINT IF EXISTS settlement_recovery_jobs_price_id_fkey;
ALTER TABLE settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_price_id_fkey
        FOREIGN KEY (price_id) REFERENCES prices (id);

ALTER TABLE cost_snapshots DROP CONSTRAINT IF EXISTS fk_cost_snapshots_cost_price_channel_model;
ALTER TABLE cost_snapshots
    ADD CONSTRAINT fk_cost_snapshots_cost_price_channel_model
        FOREIGN KEY (cost_price_id, channel_id, model_id)
            REFERENCES channel_cost_prices (id, channel_id, model_id);

ALTER TABLE price_snapshots DROP CONSTRAINT IF EXISTS price_snapshots_price_id_fkey;
ALTER TABLE price_snapshots
    ADD CONSTRAINT price_snapshots_price_id_fkey
        FOREIGN KEY (price_id) REFERENCES prices (id);
