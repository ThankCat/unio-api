-- 能力自动校正 rollup：按「模型 × 渠道 × 能力」增量聚合真实成功流量的观测计数。
-- worker 据此判定哪些能力被成功用过（且带强证据）但尚未声明。按渠道粒度学习，避免多渠道分档时
-- 把强渠道能力误套到弱渠道（超声明）。决策只读这张小表，不碰原始大表 request_attempts。
CREATE TABLE model_capability_observations (
    -- model_id: 观测所属 Unio 模型（由 request_attempts 的 channel_id+upstream_model 经 channel_models 反查）。--
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,

    -- channel_id: 产生该观测的渠道（同模型不同渠道分别累计）。--
    channel_id BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,

    -- capability_key: 稳定能力标识，合法值由 app 层 capability 注册表校验（docs/protocol/CAPABILITY_KEYS.md）。--
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),

    -- success_count: 成功且 required 含该能力的尝试累计数。--
    success_count BIGINT NOT NULL DEFAULT 0 CHECK (success_count >= 0),

    -- evidence_count: 其中带强证据（响应真用到该能力，如 finish_class=tool_use / cache 命中 / reasoning token）的尝试数。--
    evidence_count BIGINT NOT NULL DEFAULT 0 CHECK (evidence_count >= 0),

    -- first_seen_at: 首次观测到的时间。--
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- last_seen_at: 最近一次观测到的时间（窗口判定用）。--
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 同一 (模型, 渠道, 能力) 只累计一行。--
    PRIMARY KEY (model_id, channel_id, capability_key)
);

-- 决策按模型聚合其全部渠道的观测。
CREATE INDEX idx_model_capability_observations_model ON model_capability_observations (model_id);
