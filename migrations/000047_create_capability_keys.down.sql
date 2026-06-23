-- 回滚能力 key 字典表。
ALTER TABLE model_capabilities
    DROP CONSTRAINT IF EXISTS model_capabilities_capability_key_fkey;

DROP TABLE IF EXISTS capability_keys;
