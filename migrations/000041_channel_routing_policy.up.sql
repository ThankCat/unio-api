ALTER TABLE public.channels
    ADD COLUMN sticky_enabled boolean,
    ADD COLUMN sticky_ttl_ms bigint;

ALTER TABLE public.channels
    DROP CONSTRAINT channels_priority_check,
    ADD CONSTRAINT channels_priority_check
        CHECK (priority BETWEEN 0 AND 100 AND priority % 10 = 0),
    ADD CONSTRAINT channels_sticky_policy_check
        CHECK (
            (sticky_enabled IS NULL AND sticky_ttl_ms IS NULL)
            OR (sticky_enabled = false AND sticky_ttl_ms IS NULL)
            OR (sticky_enabled = true AND sticky_ttl_ms > 0)
        );

COMMENT ON COLUMN public.channels.sticky_enabled IS
    'NULL=inherit gateway.routing_sticky; true=enabled with channel TTL; false=disabled';
COMMENT ON COLUMN public.channels.sticky_ttl_ms IS
    'Channel sticky TTL in milliseconds; required only when sticky_enabled=true';

COMMENT ON COLUMN public.routes.sticky_enabled IS
    'Deprecated compatibility column; Sticky policy is owned by channels';
