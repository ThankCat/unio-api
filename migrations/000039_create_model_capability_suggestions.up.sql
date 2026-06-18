-- 能力自动校正建议：worker 产出的「建议给某模型补声明某能力」决策，供 admin 一键采纳/忽略。
-- auto 档强证据自动 upsert model_capabilities 时也写一条 status=accepted（decided_by=auto_calibrate）留痕。
-- dismissed 后 worker 不再重复打扰该 (模型, 能力)。
CREATE TABLE model_capability_suggestions (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- model_id: 建议作用的 Unio 模型。--
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,

    -- capability_key: 建议补的能力标识。--
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),

    -- suggested_level: 建议的支持级别（自动校正只产 full；limited 留人工）。--
    suggested_level TEXT NOT NULL CHECK (suggested_level IN ('full', 'limited')),

    -- evidence_kind: 证据强弱（strong=响应真用到；weak=仅带字段且成功）。--
    evidence_kind TEXT NOT NULL CHECK (evidence_kind IN ('strong', 'weak')),

    -- rationale: 判定依据（成功数/证据数/比例/窗口/渠道/样本 attempt id 等），JSON。--
    rationale JSONB NOT NULL,

    -- status: 决策状态。--
    status TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'dismissed')),

    -- created_at: 建议创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- decided_at: 人工/自动决策时间，pending 为空。--
    decided_at TIMESTAMPTZ,

    -- decided_by: 决策者标识（admin 标识 / auto_calibrate），pending 为空。--
    decided_by TEXT,

    -- 同一 (模型, 能力) 只保留一条建议（重复观测更新而非堆叠）。--
    UNIQUE (model_id, capability_key)
);

-- admin 与 worker 按状态筛选（列待采纳）。
CREATE INDEX idx_model_capability_suggestions_status ON model_capability_suggestions (status);
