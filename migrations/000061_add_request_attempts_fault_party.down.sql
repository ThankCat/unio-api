DROP INDEX IF EXISTS idx_request_attempts_channel_fault;
ALTER TABLE request_attempts DROP COLUMN IF EXISTS fault_party;
