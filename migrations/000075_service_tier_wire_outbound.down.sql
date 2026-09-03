ALTER TABLE public.request_records
    DROP CONSTRAINT ck_request_records_service_tier_resolution;
ALTER TABLE public.request_records
    ADD CONSTRAINT ck_request_records_service_tier_resolution CHECK (
        service_tier_resolution IS NULL OR service_tier_resolution = ANY (ARRAY[
            'upstream_response'::text,
            'standard_fallback_missing'::text,
            'standard_fallback_unknown'::text,
            'standard_fallback_fast_price_missing'::text
        ])
    );

ALTER TABLE public.settlement_recovery_jobs
    DROP CONSTRAINT ck_settlement_recovery_service_tier_resolution;
ALTER TABLE public.settlement_recovery_jobs
    ADD CONSTRAINT ck_settlement_recovery_service_tier_resolution CHECK (
        service_tier_resolution IS NULL OR service_tier_resolution = ANY (ARRAY[
            'upstream_response'::text,
            'standard_fallback_missing'::text,
            'standard_fallback_unknown'::text,
            'standard_fallback_fast_price_missing'::text
        ])
    );
