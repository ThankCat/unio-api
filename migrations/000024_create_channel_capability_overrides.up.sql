-- Channel capability override 是能力架构 Layer 3，对某条 channel 的能力做收紧（只能减法）。
CREATE TABLE channel_capability_overrides (
    -- channel_id: 收紧策略所属 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,

    -- capability_key: 稳定能力标识，合法值由 app 层 capability 注册表校验，DB 不做枚举约束以支持只增不删。--
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),

    -- support_level: 仅允许 limited/unsupported；不能反向放开 Layer 2 未声明的能力。--
    support_level TEXT NOT NULL CHECK (support_level IN ('limited', 'unsupported')),

    -- limits: 比模型层更严的细化约束，空表示仅按 support_level 收紧。--
    limits JSONB,

    -- reason: 收紧原因（供应商限制说明等），供审计。--
    reason TEXT,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_by: 最后修改者标识，空表示未知。--
    updated_by TEXT,

    -- 同一 channel 对同一能力只能有一条收紧策略。--
    PRIMARY KEY (channel_id, capability_key)
);

-- 排查某能力被哪些 channel 收紧时按 capability 反查。
CREATE INDEX idx_channel_capability_overrides_capability_key ON channel_capability_overrides (capability_key);
