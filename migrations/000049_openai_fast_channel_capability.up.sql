-- OpenAI Fast 是渠道级显式能力：默认关闭，只有确认支持且账单契约可信的 OpenAI Channel 才开启。
ALTER TABLE public.channels
    ADD COLUMN supports_openai_fast boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT ck_channels_openai_fast_protocol CHECK (
        NOT supports_openai_fast OR protocol = 'openai'
    );

-- requested_service_tier 保存客户意图；forwarded_service_tier 保存该真实 attempt 的出站档位。
ALTER TABLE public.request_attempts
    ADD COLUMN forwarded_service_tier text,
    ADD CONSTRAINT ck_request_attempts_forwarded_service_tier CHECK (
        forwarded_service_tier IS NULL OR forwarded_service_tier IN ('standard', 'fast')
    );
