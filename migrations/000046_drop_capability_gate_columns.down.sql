-- 回滚：重建能力闸门审计列（仅恢复结构，历史数据不可恢复）。
ALTER TABLE request_records
    ADD COLUMN capability_check_result TEXT;

ALTER TABLE request_attempts
    ADD COLUMN required_capabilities TEXT[] NOT NULL DEFAULT '{}';
