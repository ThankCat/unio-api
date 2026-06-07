-- Model capability 是能力架构 Layer 2，按「模型 × 协议字段/子能力」声明该模型对某能力的支持级别。
CREATE TABLE model_capabilities (
    -- model_id: 能力所属模型 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,

    -- capability_key: 稳定能力标识，合法值由 app 层 capability 注册表校验（docs/protocol/CAPABILITY_KEYS.md），DB 不做枚举约束以支持只增不删。--
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),

    -- support_level: 该模型对该能力的支持级别。--
    support_level TEXT NOT NULL CHECK (support_level IN ('full', 'limited', 'unsupported')),

    -- limits: 能力的细化约束（如 reasoning.effort 允许值集合、tools.max_count），空表示无额外约束。--
    limits JSONB,

    -- source: 能力声明来源。--
    source TEXT NOT NULL CHECK (source IN ('models_dev', 'manual', 'adapter_seed')),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_by: 最后修改者标识（admin/同步任务），空表示未知。--
    updated_by TEXT,

    -- 同一模型对同一能力只能有一条声明。--
    PRIMARY KEY (model_id, capability_key)
);

-- cap-tags 公开面与闸门需要按能力反查「哪些模型支持该能力」。
CREATE INDEX idx_model_capabilities_capability_key ON model_capabilities (capability_key);
