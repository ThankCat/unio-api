-- 000038: model_prices 长上下文阶梯定价（对齐 OpenAI GPT-5.4+ / sub2api）。
--
-- 背景：GPT-5.6 等模型在「输入合计（未缓存 + cache_read + cache_write）> 272K」时，
-- 整单按输入侧 ×2、输出侧 ×1.5 计价（非用量阶梯打折）。此前 Unio 只记短上下文牌价，
-- 导致渠道成本约少一半。阶梯绑定价格窗口（model_prices），与基准价同生同灭、可审计。
--
-- 字段：long_context_enabled 开关；启用时 threshold / input_multiplier / output_multiplier 必填。
-- 快照：price_snapshots / cost_snapshots 记 long_context_applied，便于对账解释单价为何翻倍。

ALTER TABLE public.model_prices
    ADD COLUMN long_context_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN long_context_threshold bigint,
    ADD COLUMN long_context_input_multiplier numeric(20,10),
    ADD COLUMN long_context_output_multiplier numeric(20,10);

ALTER TABLE public.model_prices
    ADD CONSTRAINT ck_model_prices_long_context CHECK (
        (NOT long_context_enabled)
        OR (
            long_context_threshold IS NOT NULL
            AND long_context_threshold > 0
            AND long_context_input_multiplier IS NOT NULL
            AND long_context_input_multiplier > (0)::numeric
            AND long_context_output_multiplier IS NOT NULL
            AND long_context_output_multiplier > (0)::numeric
        )
    );

ALTER TABLE public.price_snapshots
    ADD COLUMN long_context_applied boolean NOT NULL DEFAULT false;

ALTER TABLE public.cost_snapshots
    ADD COLUMN long_context_applied boolean NOT NULL DEFAULT false;

-- 现有 gpt-5.6-* 启用中的基准价窗口默认打开官方阶梯，避免运营逐条补配。
UPDATE public.model_prices mp
SET
    long_context_enabled = true,
    long_context_threshold = 272000,
    long_context_input_multiplier = 2,
    long_context_output_multiplier = 1.5
FROM public.models m
WHERE mp.model_id = m.id
  AND m.model_id LIKE 'gpt-5.6%'
  AND mp.status = 'enabled';
