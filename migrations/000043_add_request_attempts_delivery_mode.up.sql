-- 能力证据 v2（DESIGN-capability-evidence-v2 Phase 3 / G3）。
--
-- request_attempts.delivery_mode：本次尝试的分发方式（stream 流式 / batch 一次性）。
-- 仅作 Admin 审计与 stream 的二级佐证；不作为能力自动校正的强证据来源（一级 stream 证据走
-- used_capabilities 含 'stream'，见 DESIGN §4.2 / Q1 / Q6）。NOT NULL DEFAULT 'batch' 兼容历史行。
ALTER TABLE request_attempts
    ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'batch'
        CHECK (delivery_mode IN ('stream', 'batch'));
