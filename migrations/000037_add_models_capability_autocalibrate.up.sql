-- 能力自动校正（被动证据式，DESIGN-capability-autocalibration）：per-model 开关。
-- off=不学习；suggest=只产生建议待人工采纳（默认）；auto=强证据自动补、弱证据仍只建议。
ALTER TABLE models
    -- capability_autocalibrate: 该模型的能力自动校正档位。--
    ADD COLUMN capability_autocalibrate TEXT NOT NULL DEFAULT 'suggest'
        CHECK (capability_autocalibrate IN ('off', 'suggest', 'auto'));
