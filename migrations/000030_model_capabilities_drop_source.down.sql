-- 还原 source 列（默认 manual 以兼容已有行；随后去掉默认值，回到「显式写入」语义）。
ALTER TABLE model_capabilities
    ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('models_dev', 'manual', 'adapter_seed'));
ALTER TABLE model_capabilities ALTER COLUMN source DROP DEFAULT;
