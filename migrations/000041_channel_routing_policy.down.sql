COMMENT ON COLUMN public.routes.sticky_enabled IS NULL;

ALTER TABLE public.channels
    DROP CONSTRAINT channels_sticky_policy_check,
    DROP CONSTRAINT channels_priority_check,
    ADD CONSTRAINT channels_priority_check CHECK (priority >= 0),
    DROP COLUMN sticky_ttl_ms,
    DROP COLUMN sticky_enabled;
