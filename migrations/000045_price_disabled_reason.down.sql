-- 回滚 price_disabled 枚举值。存量因撤价而下架的模型改记 manual_delisted：
-- 它们确实是管理员操作导致的下架，只是丢失了「因为撤价」这个更精确的归因。
UPDATE public.models
SET disabled_reason = 'manual_delisted'
WHERE disabled_reason = 'price_disabled';

ALTER TABLE public.models
    DROP CONSTRAINT ck_models_disabled_reason;

ALTER TABLE public.models
    ADD CONSTRAINT ck_models_disabled_reason CHECK (
        ((disabled_reason IS NULL) OR (disabled_reason = ANY (ARRAY[
            'manual_delisted'::text,
            'binding_disabled'::text,
            'channel_disabled'::text
        ])))
    );

COMMENT ON COLUMN public.models.disabled_reason IS '停用直接原因：manual_delisted 管理员主动下架；binding_disabled 最后一条渠道绑定被停用或解除；channel_disabled 最后一条可用渠道被停用。enabled 时为空。';
