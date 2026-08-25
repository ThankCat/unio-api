DROP TABLE IF EXISTS public.provider_service_tier_cost_risks;
DROP SEQUENCE IF EXISTS public.provider_service_tier_cost_risks_id_seq;

ALTER TABLE public.settlement_recovery_jobs
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_service_tier_resolution,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_upstream_service_tier,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_settled_service_tier,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_actual_service_tier,
    DROP CONSTRAINT IF EXISTS ck_settlement_recovery_requested_service_tier,
    DROP COLUMN IF EXISTS channel_price_service_tier_id,
    DROP COLUMN IF EXISTS model_price_service_tier_id,
    DROP COLUMN IF EXISTS service_tier_resolution,
    DROP COLUMN IF EXISTS upstream_service_tier,
    DROP COLUMN IF EXISTS settled_service_tier,
    DROP COLUMN IF EXISTS actual_service_tier,
    DROP COLUMN IF EXISTS requested_service_tier;

ALTER TABLE public.cost_snapshots
    DROP CONSTRAINT IF EXISTS cost_snapshots_channel_price_service_tier_fkey,
    DROP CONSTRAINT IF EXISTS cost_snapshots_model_price_service_tier_fkey,
    DROP CONSTRAINT IF EXISTS ck_cost_snapshots_tier_cost_source,
    DROP CONSTRAINT IF EXISTS ck_cost_snapshots_service_tier,
    DROP COLUMN IF EXISTS tier_cost_source,
    DROP COLUMN IF EXISTS channel_price_service_tier_id,
    DROP COLUMN IF EXISTS model_price_service_tier_id,
    DROP COLUMN IF EXISTS service_tier;

ALTER TABLE public.price_snapshots
    DROP CONSTRAINT IF EXISTS price_snapshots_model_price_service_tier_fkey,
    DROP CONSTRAINT IF EXISTS ck_price_snapshots_service_tier,
    DROP COLUMN IF EXISTS model_price_service_tier_id,
    DROP COLUMN IF EXISTS service_tier;

ALTER TABLE public.request_attempts
    DROP CONSTRAINT IF EXISTS ck_request_attempts_upstream_service_tier,
    DROP CONSTRAINT IF EXISTS ck_request_attempts_requested_service_tier,
    DROP COLUMN IF EXISTS upstream_service_tier,
    DROP COLUMN IF EXISTS requested_service_tier;

ALTER TABLE public.request_records
    DROP CONSTRAINT IF EXISTS ck_request_records_service_tier_resolution,
    DROP CONSTRAINT IF EXISTS ck_request_records_settled_service_tier,
    DROP CONSTRAINT IF EXISTS ck_request_records_actual_service_tier,
    DROP CONSTRAINT IF EXISTS ck_request_records_requested_service_tier,
    DROP COLUMN IF EXISTS service_tier_resolution,
    DROP COLUMN IF EXISTS settled_service_tier,
    DROP COLUMN IF EXISTS actual_service_tier,
    DROP COLUMN IF EXISTS requested_service_tier;

DROP TABLE IF EXISTS public.channel_price_service_tiers;
DROP SEQUENCE IF EXISTS public.channel_price_service_tiers_id_seq;
DROP TABLE IF EXISTS public.model_price_service_tiers;
DROP SEQUENCE IF EXISTS public.model_price_service_tiers_id_seq;
