-- 实体归档生命周期：providers / channels / routes 三表 status 增第三态 archived，
-- 并加 archived_at 时间列 + 一致性不变量（archived_at 有值 ⟺ status='archived'）。
-- 归档 = 只改状态、不删数据、完全可逆；路由候选已按 status='enabled' 过滤，archived 天然被排除。

-- providers
ALTER TABLE providers ADD COLUMN archived_at timestamptz;
ALTER TABLE providers DROP CONSTRAINT IF EXISTS providers_status_check;
ALTER TABLE providers ADD CONSTRAINT providers_status_check
    CHECK (status IN ('enabled', 'disabled', 'archived'));
ALTER TABLE providers ADD CONSTRAINT ck_providers_archived_at
    CHECK ((status = 'archived') = (archived_at IS NOT NULL));

-- channels
ALTER TABLE channels ADD COLUMN archived_at timestamptz;
ALTER TABLE channels DROP CONSTRAINT IF EXISTS channels_status_check;
ALTER TABLE channels ADD CONSTRAINT channels_status_check
    CHECK (status IN ('enabled', 'disabled', 'archived'));
ALTER TABLE channels ADD CONSTRAINT ck_channels_archived_at
    CHECK ((status = 'archived') = (archived_at IS NOT NULL));

-- routes
ALTER TABLE routes ADD COLUMN archived_at timestamptz;
ALTER TABLE routes DROP CONSTRAINT IF EXISTS routes_status_check;
ALTER TABLE routes ADD CONSTRAINT routes_status_check
    CHECK (status IN ('enabled', 'disabled', 'archived'));
ALTER TABLE routes ADD CONSTRAINT ck_routes_archived_at
    CHECK ((status = 'archived') = (archived_at IS NOT NULL));
