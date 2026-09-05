-- 回滚：显式 0 无法满足旧约束，先归为 NULL（继承默认）再恢复 (> 0) 约束。
UPDATE public.channels SET response_timeout_ms = NULL WHERE response_timeout_ms = 0;
UPDATE public.channels SET first_token_timeout_ms = NULL WHERE first_token_timeout_ms = 0;

ALTER TABLE public.channels
    DROP CONSTRAINT channels_first_token_timeout_ms_check,
    DROP CONSTRAINT channels_response_timeout_ms_check;

ALTER TABLE public.channels
    ADD CONSTRAINT channels_first_token_timeout_ms_check
        CHECK (first_token_timeout_ms IS NULL OR first_token_timeout_ms > 0),
    ADD CONSTRAINT channels_response_timeout_ms_check
        CHECK (response_timeout_ms IS NULL OR response_timeout_ms > 0);

COMMENT ON COLUMN public.channels.response_timeout_ms IS NULL;
COMMENT ON COLUMN public.channels.first_token_timeout_ms IS NULL;
