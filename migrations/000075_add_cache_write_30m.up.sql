-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。

-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
ALTER TABLE model_prices
    ADD COLUMN cache_write_30m_input_price NUMERIC(20, 10) CHECK (
        cache_write_30m_input_price IS NULL OR cache_write_30m_input_price >= 0
    );

-- 2) channel_prices：成本价新增 30m 缓存写成本（可空；售价列与毛利约束已于 000056 退役）。
ALTER TABLE channel_prices
    ADD COLUMN cache_write_30m_input_cost NUMERIC(20, 10) CHECK (
        cache_write_30m_input_cost IS NULL OR cache_write_30m_input_cost >= 0
    );

-- 3) price_snapshots：客户售价快照新增 30m 缓存写单价副本。
ALTER TABLE price_snapshots
    ADD COLUMN cache_write_30m_input_price NUMERIC(20, 10) CHECK (
        cache_write_30m_input_price IS NULL OR cache_write_30m_input_price >= 0
    );

-- 4) cost_snapshots：成本快照新增 30m 缓存写成本价 + 实际成本金额，并把 30m 金额并入总额校验。
ALTER TABLE cost_snapshots
    ADD COLUMN cache_write_30m_input_cost NUMERIC(20, 10) CHECK (
        cache_write_30m_input_cost IS NULL OR cache_write_30m_input_cost >= 0
    ),
    ADD COLUMN cache_write_30m_input_cost_amount NUMERIC(20, 10) NOT NULL DEFAULT 0 CHECK (
        cache_write_30m_input_cost_amount >= 0
    );

-- 回填后去掉默认值：与既有 *_cost_amount 列一致，强制后续写入显式给值。
ALTER TABLE cost_snapshots ALTER COLUMN cache_write_30m_input_cost_amount DROP DEFAULT;

ALTER TABLE cost_snapshots DROP CONSTRAINT ck_cost_snapshots_total_amount;
ALTER TABLE cost_snapshots ADD CONSTRAINT ck_cost_snapshots_total_amount CHECK (
    total_cost_amount =
        uncached_input_cost_amount
        + cache_read_input_cost_amount
        + cache_write_5m_input_cost_amount
        + cache_write_1h_input_cost_amount
        + cache_write_30m_input_cost_amount
        + output_cost_amount
        + reasoning_output_cost_amount
);

-- 5) usage_records：新增 30m 缓存写 token 数 + 可信状态，并入非 known 值零校验。
ALTER TABLE usage_records
    ADD COLUMN cache_write_30m_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_write_30m_input_tokens >= 0),
    ADD COLUMN cache_write_30m_input_tokens_state TEXT NOT NULL DEFAULT 'not_applicable' CHECK (
        cache_write_30m_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    );

-- state 列与既有兄弟列一致：回填后去掉默认，强制显式写入。
ALTER TABLE usage_records ALTER COLUMN cache_write_30m_input_tokens_state DROP DEFAULT;

ALTER TABLE usage_records DROP CONSTRAINT ck_usage_records_non_known_values_zero;
ALTER TABLE usage_records ADD CONSTRAINT ck_usage_records_non_known_values_zero CHECK (
    (uncached_input_tokens_state = 'known' OR uncached_input_tokens = 0)
        AND (cache_read_input_tokens_state = 'known' OR cache_read_input_tokens = 0)
        AND (cache_write_5m_input_tokens_state = 'known' OR cache_write_5m_input_tokens = 0)
        AND (cache_write_1h_input_tokens_state = 'known' OR cache_write_1h_input_tokens = 0)
        AND (cache_write_30m_input_tokens_state = 'known' OR cache_write_30m_input_tokens = 0)
        AND (output_tokens_total_state = 'known' OR output_tokens_total = 0)
        AND (reasoning_output_tokens_state = 'known' OR reasoning_output_tokens = 0)
);

-- 6) settlement_recovery_jobs：新增 30m 缓存写 token + 状态 + authorization 售价副本，并入零校验。
ALTER TABLE settlement_recovery_jobs
    ADD COLUMN usage_cache_write_30m_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (usage_cache_write_30m_input_tokens >= 0),
    ADD COLUMN usage_cache_write_30m_input_tokens_state TEXT NOT NULL DEFAULT 'not_applicable' CHECK (
        usage_cache_write_30m_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),
    ADD COLUMN cache_write_30m_input_price NUMERIC(20, 10) CHECK (
        cache_write_30m_input_price IS NULL OR cache_write_30m_input_price >= 0
    );

-- token / state 列与既有兄弟列一致（无默认值）：回填后去掉默认。
ALTER TABLE settlement_recovery_jobs ALTER COLUMN usage_cache_write_30m_input_tokens DROP DEFAULT;
ALTER TABLE settlement_recovery_jobs ALTER COLUMN usage_cache_write_30m_input_tokens_state DROP DEFAULT;

ALTER TABLE settlement_recovery_jobs DROP CONSTRAINT ck_settlement_recovery_jobs_non_known_values_zero;
ALTER TABLE settlement_recovery_jobs ADD CONSTRAINT ck_settlement_recovery_jobs_non_known_values_zero CHECK (
    (usage_uncached_input_tokens_state = 'known' OR usage_uncached_input_tokens = 0)
        AND (usage_cache_read_input_tokens_state = 'known' OR usage_cache_read_input_tokens = 0)
        AND (usage_cache_write_5m_input_tokens_state = 'known' OR usage_cache_write_5m_input_tokens = 0)
        AND (usage_cache_write_1h_input_tokens_state = 'known' OR usage_cache_write_1h_input_tokens = 0)
        AND (usage_cache_write_30m_input_tokens_state = 'known' OR usage_cache_write_30m_input_tokens = 0)
        AND (usage_output_tokens_total_state = 'known' OR usage_output_tokens_total = 0)
        AND (usage_reasoning_output_tokens_state = 'known' OR usage_reasoning_output_tokens = 0)
);
