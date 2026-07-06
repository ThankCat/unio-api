-- 回滚归档生命周期：先把 archived 行降级为 disabled 并清空 archived_at（否则残留 archived 违反旧 CHECK），
-- 再收回一致性 CHECK 与 archived_at 列，恢复 status 二态约束。

-- routes
UPDATE routes SET status = 'disabled', archived_at = NULL WHERE status = 'archived';
ALTER TABLE routes DROP CONSTRAINT IF EXISTS ck_routes_archived_at;
ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_status_check;
ALTER TABLE routes ADD CONSTRAINT routes_status_check
    CHECK (status IN ('enabled', 'disabled'));
ALTER TABLE routes DROP COLUMN archived_at;

-- channels
UPDATE channels SET status = 'disabled', archived_at = NULL WHERE status = 'archived';
ALTER TABLE channels DROP CONSTRAINT IF EXISTS ck_channels_archived_at;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_status_check;
ALTER TABLE channels ADD CONSTRAINT channels_status_check
    CHECK (status IN ('enabled', 'disabled'));
ALTER TABLE channels DROP COLUMN archived_at;

-- providers
UPDATE providers SET status = 'disabled', archived_at = NULL WHERE status = 'archived';
ALTER TABLE providers DROP CONSTRAINT IF EXISTS ck_providers_archived_at;
ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_status_check;
ALTER TABLE providers ADD CONSTRAINT providers_status_check
    CHECK (status IN ('enabled', 'disabled'));
ALTER TABLE providers DROP COLUMN archived_at;
