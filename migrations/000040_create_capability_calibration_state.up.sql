-- 能力自动校正增量游标：记录上次已处理到的 request_attempts.id，使 worker 增量扫描（只看新行），
-- 成本与 request_attempts 历史总量解耦。单行表（id 恒为 1）。
CREATE TABLE capability_calibration_state (
    -- id: 单行约束键，恒为 1。--
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),

    -- last_processed_attempt_id: 上次已聚合到的 request_attempts.id 上界（含）。--
    last_processed_attempt_id BIGINT NOT NULL DEFAULT 0 CHECK (last_processed_attempt_id >= 0),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 预置单行，GetWatermark 始终可读。
INSERT INTO capability_calibration_state (id, last_processed_attempt_id) VALUES (1, 0);
