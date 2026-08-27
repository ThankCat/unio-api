ALTER TABLE public.model_catalog
    DROP COLUMN description,
    DROP COLUMN knowledge_cutoff,
    DROP COLUMN cache_read_price_usd_per_million_tokens,
    DROP COLUMN cache_write_price_usd_per_million_tokens,
    DROP COLUMN input_limit_tokens,
    DROP COLUMN open_weights,
    DROP COLUMN modalities_input,
    DROP COLUMN modalities_output,
    DROP COLUMN last_updated;

ALTER TABLE public.models
    DROP COLUMN description,
    DROP COLUMN knowledge_cutoff;
