-- 阶段 14 Q4：能力声明去 source。
-- 同步不再写运行时能力表（改写目录），source（models_dev/manual/adapter_seed）已无意义。
ALTER TABLE model_capabilities DROP COLUMN source;
