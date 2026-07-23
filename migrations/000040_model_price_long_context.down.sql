ALTER TABLE public.cost_snapshots
    DROP COLUMN IF EXISTS long_context_applied;

ALTER TABLE public.price_snapshots
    DROP COLUMN IF EXISTS long_context_applied;

ALTER TABLE public.model_prices
    DROP CONSTRAINT IF EXISTS ck_model_prices_long_context;

ALTER TABLE public.model_prices
    DROP COLUMN IF EXISTS long_context_output_multiplier,
    DROP COLUMN IF EXISTS long_context_input_multiplier,
    DROP COLUMN IF EXISTS long_context_threshold,
    DROP COLUMN IF EXISTS long_context_enabled;
