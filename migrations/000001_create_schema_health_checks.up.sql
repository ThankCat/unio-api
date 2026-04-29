-- 用于验证 migration 流程已经跑通，不承载业务含义。
CREATE TABLE schema_health_checks (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);