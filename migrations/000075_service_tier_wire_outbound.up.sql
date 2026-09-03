-- Fast 结算例外（docs/changes/2026-09-02-account-pool 边界 15）：
-- Codex 订阅 wire 的响应档位不可信（priority 请求仍回 auto/default），结算档位以出站请求档位为准，
-- 新增 resolution 取值 wire_outbound_authoritative。两张带 CHECK 的表同步放宽：
-- request_records（结算落库）与 settlement_recovery_jobs（补偿快照）。
-- provider_service_tier_cost_risks.service_tier_resolution 无取值 CHECK，不需改。

ALTER TABLE public.request_records
    DROP CONSTRAINT ck_request_records_service_tier_resolution;
ALTER TABLE public.request_records
    ADD CONSTRAINT ck_request_records_service_tier_resolution CHECK (
        service_tier_resolution IS NULL OR service_tier_resolution = ANY (ARRAY[
            'upstream_response'::text,
            'standard_fallback_missing'::text,
            'standard_fallback_unknown'::text,
            'standard_fallback_fast_price_missing'::text,
            'wire_outbound_authoritative'::text
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
            'standard_fallback_fast_price_missing'::text,
            'wire_outbound_authoritative'::text
        ])
    );
