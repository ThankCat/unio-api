-- 渠道超时列允许显式 0（2026-09-05 流式超时对齐 Sub2API）。
--
-- 语义：NULL = 继承全局默认；显式 0 = 不限制；正数 = 覆写。此前 CHECK (> 0) 把「不限制」挡在库外，
-- 只能靠把数字调大来放宽——对 reasoning 长思考这是无效的（前导缓冲仍会溢出）。
-- 全局默认（gateway.default_response_timeout_ms / gateway.default_first_token_timeout_ms）同样以 0 表示不限制。

ALTER TABLE public.channels
    DROP CONSTRAINT channels_first_token_timeout_ms_check,
    DROP CONSTRAINT channels_response_timeout_ms_check;

ALTER TABLE public.channels
    ADD CONSTRAINT channels_first_token_timeout_ms_check
        CHECK (first_token_timeout_ms IS NULL OR first_token_timeout_ms >= 0),
    ADD CONSTRAINT channels_response_timeout_ms_check
        CHECK (response_timeout_ms IS NULL OR response_timeout_ms >= 0);

COMMENT ON COLUMN public.channels.response_timeout_ms IS
    'NULL 继承全局默认；0 不限制；正数覆写（号池账号可再覆写一层）。非流式限制完整响应；流式限制收到上游响应头。';
COMMENT ON COLUMN public.channels.first_token_timeout_ms IS
    'NULL 继承全局默认；0 不限制；正数覆写（号池账号可再覆写一层）。限制流式首个上游进展。';
