-- 移除能力自动校正与证据 v2（DEC-024 / DESIGN-capability-manual-declaration）。
-- 自动校正与 used_capabilities/delivery_mode 证据链全部废止；能力改为人工声明。
DROP TABLE IF EXISTS model_capability_suggestions;
DROP TABLE IF EXISTS model_capability_observations;
DROP TABLE IF EXISTS capability_calibration_state;

ALTER TABLE models
    DROP COLUMN IF EXISTS capability_autocalibrate;

ALTER TABLE request_attempts
    DROP COLUMN IF EXISTS used_capabilities,
    DROP COLUMN IF EXISTS delivery_mode;

ALTER TABLE settlement_recovery_jobs
    DROP COLUMN IF EXISTS used_capabilities;
