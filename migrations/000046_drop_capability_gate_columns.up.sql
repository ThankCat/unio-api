-- 移除能力闸门（DEC-024 / DESIGN-capability-manual-declaration）：删除 observe/enforce 审计列。
-- 能力不再于请求热路径判定，required_capabilities 推断与 capability_check_result 审计随闸门一并删除。
ALTER TABLE request_attempts
    DROP COLUMN IF EXISTS required_capabilities;

ALTER TABLE request_records
    DROP COLUMN IF EXISTS capability_check_result;
