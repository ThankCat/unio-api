-- 阶段 15：把价格 / 成本快照与补偿任务的外键从退役的 prices / channel_cost_prices 改挂到 channel_prices。
-- 开发期库可重置，迁移在空快照表上执行；生产化前若有历史快照需另设数据迁移（详见 PLAN §12）。

-- price_snapshots.price_id：模型级 prices(id) -> 渠道级 channel_prices(id)。
ALTER TABLE price_snapshots DROP CONSTRAINT IF EXISTS price_snapshots_price_id_fkey;
ALTER TABLE price_snapshots
    ADD CONSTRAINT price_snapshots_price_id_fkey
        FOREIGN KEY (price_id) REFERENCES channel_prices (id);

-- cost_snapshots 复合外键：channel_cost_prices(id,channel_id,model_id) -> channel_prices(id,channel_id,model_id)。
ALTER TABLE cost_snapshots DROP CONSTRAINT IF EXISTS fk_cost_snapshots_cost_price_channel_model;
ALTER TABLE cost_snapshots
    ADD CONSTRAINT fk_cost_snapshots_cost_price_channel_model
        FOREIGN KEY (cost_price_id, channel_id, model_id)
            REFERENCES channel_prices (id, channel_id, model_id);

-- settlement_recovery_jobs.price_id：authorization/补偿命中的售价 prices(id) -> channel_prices(id)。
-- 阶段 15 起补偿任务记录的是命中渠道的渠道-模型价（PLAN §6.2 漏列，本期补齐）。
ALTER TABLE settlement_recovery_jobs DROP CONSTRAINT IF EXISTS settlement_recovery_jobs_price_id_fkey;
ALTER TABLE settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_price_id_fkey
        FOREIGN KEY (price_id) REFERENCES channel_prices (id);
