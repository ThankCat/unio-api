-- 回滚：重建能力自动校正与证据 v2 的 schema（仅恢复结构，历史数据不可恢复）。
ALTER TABLE settlement_recovery_jobs
    ADD COLUMN used_capabilities TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE request_attempts
    ADD COLUMN used_capabilities TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'batch'
        CHECK (delivery_mode IN ('stream', 'batch'));

ALTER TABLE models
    ADD COLUMN capability_autocalibrate TEXT NOT NULL DEFAULT 'suggest'
        CHECK (capability_autocalibrate IN ('off', 'suggest', 'auto'));

CREATE TABLE capability_calibration_state (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    last_processed_attempt_id BIGINT NOT NULL DEFAULT 0 CHECK (last_processed_attempt_id >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_by TEXT,
    locked_until TIMESTAMPTZ
);
INSERT INTO capability_calibration_state (id, last_processed_attempt_id) VALUES (1, 0);

CREATE TABLE model_capability_observations (
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,
    channel_id BIGINT NOT NULL REFERENCES channels (id) ON DELETE CASCADE,
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),
    success_count BIGINT NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    evidence_count BIGINT NOT NULL DEFAULT 0 CHECK (evidence_count >= 0),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, channel_id, capability_key)
);
CREATE INDEX idx_model_capability_observations_model ON model_capability_observations (model_id);

CREATE TABLE model_capability_suggestions (
    id BIGSERIAL PRIMARY KEY,
    model_id BIGINT NOT NULL REFERENCES models (id) ON DELETE CASCADE,
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),
    suggested_level TEXT NOT NULL CHECK (suggested_level IN ('full', 'limited')),
    evidence_kind TEXT NOT NULL CHECK (evidence_kind IN ('strong', 'weak')),
    rationale JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'dismissed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    decided_by TEXT,
    UNIQUE (model_id, capability_key)
);
CREATE INDEX idx_model_capability_suggestions_status ON model_capability_suggestions (status);
