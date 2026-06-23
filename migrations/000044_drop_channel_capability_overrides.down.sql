-- 回滚 DEC-023：重建能力架构 Layer 3「渠道能力收紧」表（结构同 000024）。
CREATE TABLE channel_capability_overrides (
    channel_id BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),
    support_level TEXT NOT NULL CHECK (support_level IN ('limited', 'unsupported')),
    limits JSONB,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT,
    PRIMARY KEY (channel_id, capability_key)
);

CREATE INDEX idx_channel_capability_overrides_capability_key ON channel_capability_overrides (capability_key);
