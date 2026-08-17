ALTER TABLE public.request_attempts
    DROP CONSTRAINT IF EXISTS ck_request_attempts_forwarded_service_tier,
    DROP COLUMN IF EXISTS forwarded_service_tier;

ALTER TABLE public.channels
    DROP CONSTRAINT IF EXISTS ck_channels_openai_fast_protocol,
    DROP COLUMN IF EXISTS supports_openai_fast;
