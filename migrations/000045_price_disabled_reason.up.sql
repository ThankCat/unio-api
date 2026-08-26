-- 模型停用原因新增 price_disabled：撤掉最后一条可解析售价导致的联动下架。
--
-- 供给不变量有两条腿：可用渠道、可解析售价。渠道那条腿断了会记
-- binding_disabled / channel_disabled，价格这条腿断了此前无处可记——
-- 因为撤价路径根本没有联动下架，模型会留在「已启用、列表里有、一调失败」的状态。
-- 补上枚举值，让价格侧联动与渠道侧使用同一套归因。

ALTER TABLE public.models
    DROP CONSTRAINT ck_models_disabled_reason;

ALTER TABLE public.models
    ADD CONSTRAINT ck_models_disabled_reason CHECK (
        ((disabled_reason IS NULL) OR (disabled_reason = ANY (ARRAY[
            'manual_delisted'::text,
            'binding_disabled'::text,
            'channel_disabled'::text,
            'price_disabled'::text
        ])))
    );

COMMENT ON COLUMN public.models.disabled_reason IS '停用直接原因：manual_delisted 管理员主动下架；binding_disabled 最后一条渠道绑定被停用或解除；channel_disabled 最后一条可用渠道被停用；price_disabled 最后一条可解析售价被撤销。enabled 时为空。';
