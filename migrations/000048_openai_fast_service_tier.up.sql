-- OpenAI Fast 采用与 Standard 同一价格窗口下的独立精确价格向量。
CREATE SEQUENCE public.model_price_service_tiers_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.model_price_service_tiers (
    id bigint NOT NULL DEFAULT nextval('public.model_price_service_tiers_id_seq'::regclass),
    model_price_id bigint NOT NULL,
    service_tier text NOT NULL,
    uncached_input_price numeric(20,10) NOT NULL,
    cache_read_input_price numeric(20,10),
    cache_write_5m_input_price numeric(20,10),
    cache_write_1h_input_price numeric(20,10),
    cache_write_30m_input_price numeric(20,10),
    output_price numeric(20,10) NOT NULL,
    reasoning_output_price numeric(20,10),
    reference_source text,
    reference_checked_at date,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT model_price_service_tiers_pkey PRIMARY KEY (id),
    CONSTRAINT model_price_service_tiers_model_price_fkey FOREIGN KEY (model_price_id)
        REFERENCES public.model_prices(id),
    CONSTRAINT uq_model_price_service_tiers UNIQUE (model_price_id, service_tier),
    CONSTRAINT ck_model_price_service_tiers_tier CHECK (service_tier = 'fast'),
    CONSTRAINT ck_model_price_service_tiers_uncached CHECK (uncached_input_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_cache_read CHECK (cache_read_input_price IS NULL OR cache_read_input_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_cache_write_5m CHECK (cache_write_5m_input_price IS NULL OR cache_write_5m_input_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_cache_write_1h CHECK (cache_write_1h_input_price IS NULL OR cache_write_1h_input_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_cache_write_30m CHECK (cache_write_30m_input_price IS NULL OR cache_write_30m_input_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_output CHECK (output_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_reasoning CHECK (reasoning_output_price IS NULL OR reasoning_output_price >= 0),
    CONSTRAINT ck_model_price_service_tiers_reference CHECK (
        (reference_source IS NULL AND reference_checked_at IS NULL)
        OR (btrim(reference_source) <> '' AND reference_checked_at IS NOT NULL)
    )
);

ALTER SEQUENCE public.model_price_service_tiers_id_seq
    OWNED BY public.model_price_service_tiers.id;

-- 绝对渠道成本覆盖可为 Fast 保存独立成本向量；倍率路径继续复用渠道倍率与充值倍率。
CREATE SEQUENCE public.channel_price_service_tiers_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.channel_price_service_tiers (
    id bigint NOT NULL DEFAULT nextval('public.channel_price_service_tiers_id_seq'::regclass),
    channel_price_id bigint NOT NULL,
    service_tier text NOT NULL,
    uncached_input_cost numeric(20,10) NOT NULL,
    cache_read_input_cost numeric(20,10),
    cache_write_5m_input_cost numeric(20,10),
    cache_write_1h_input_cost numeric(20,10),
    cache_write_30m_input_cost numeric(20,10),
    output_cost numeric(20,10) NOT NULL,
    reasoning_output_cost numeric(20,10),
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT channel_price_service_tiers_pkey PRIMARY KEY (id),
    CONSTRAINT channel_price_service_tiers_channel_price_fkey FOREIGN KEY (channel_price_id)
        REFERENCES public.channel_prices(id),
    CONSTRAINT uq_channel_price_service_tiers UNIQUE (channel_price_id, service_tier),
    CONSTRAINT ck_channel_price_service_tiers_tier CHECK (service_tier = 'fast'),
    CONSTRAINT ck_channel_price_service_tiers_uncached CHECK (uncached_input_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_cache_read CHECK (cache_read_input_cost IS NULL OR cache_read_input_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_5m CHECK (cache_write_5m_input_cost IS NULL OR cache_write_5m_input_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_1h CHECK (cache_write_1h_input_cost IS NULL OR cache_write_1h_input_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_30m CHECK (cache_write_30m_input_cost IS NULL OR cache_write_30m_input_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_output CHECK (output_cost >= 0),
    CONSTRAINT ck_channel_price_service_tiers_reasoning CHECK (reasoning_output_cost IS NULL OR reasoning_output_cost >= 0)
);

ALTER SEQUENCE public.channel_price_service_tiers_id_seq
    OWNED BY public.channel_price_service_tiers.id;

-- 请求、attempt 与快照分别保存意图、上游事实和最终结算事实。历史行保持 NULL，不反推档位。
ALTER TABLE public.request_records
    ADD COLUMN requested_service_tier text,
    ADD COLUMN actual_service_tier text,
    ADD COLUMN settled_service_tier text,
    ADD COLUMN service_tier_resolution text,
    ADD CONSTRAINT ck_request_records_requested_service_tier CHECK (requested_service_tier IS NULL OR requested_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_request_records_actual_service_tier CHECK (actual_service_tier IS NULL OR actual_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_request_records_settled_service_tier CHECK (settled_service_tier IS NULL OR settled_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_request_records_service_tier_resolution CHECK (
        service_tier_resolution IS NULL OR service_tier_resolution IN (
            'upstream_response',
            'standard_fallback_missing',
            'standard_fallback_unknown',
            'standard_fallback_fast_price_missing'
        )
    );

ALTER TABLE public.request_attempts
    ADD COLUMN requested_service_tier text,
    ADD COLUMN upstream_service_tier text,
    ADD CONSTRAINT ck_request_attempts_requested_service_tier CHECK (requested_service_tier IS NULL OR requested_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_request_attempts_upstream_service_tier CHECK (upstream_service_tier IS NULL OR btrim(upstream_service_tier) <> '');

ALTER TABLE public.price_snapshots
    ADD COLUMN service_tier text,
    ADD COLUMN model_price_service_tier_id bigint,
    ADD CONSTRAINT ck_price_snapshots_service_tier CHECK (service_tier IS NULL OR service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT price_snapshots_model_price_service_tier_fkey FOREIGN KEY (model_price_service_tier_id)
        REFERENCES public.model_price_service_tiers(id);

ALTER TABLE public.cost_snapshots
    ADD COLUMN service_tier text,
    ADD COLUMN model_price_service_tier_id bigint,
    ADD COLUMN channel_price_service_tier_id bigint,
    ADD COLUMN tier_cost_source text,
    ADD CONSTRAINT ck_cost_snapshots_service_tier CHECK (service_tier IS NULL OR service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_cost_snapshots_tier_cost_source CHECK (tier_cost_source IS NULL OR tier_cost_source IN ('derived', 'absolute')),
    ADD CONSTRAINT cost_snapshots_model_price_service_tier_fkey FOREIGN KEY (model_price_service_tier_id)
        REFERENCES public.model_price_service_tiers(id),
    ADD CONSTRAINT cost_snapshots_channel_price_service_tier_fkey FOREIGN KEY (channel_price_service_tier_id)
        REFERENCES public.channel_price_service_tiers(id);

ALTER TABLE public.settlement_recovery_jobs
    ADD COLUMN requested_service_tier text,
    ADD COLUMN actual_service_tier text,
    ADD COLUMN settled_service_tier text,
    ADD COLUMN upstream_service_tier text,
    ADD COLUMN service_tier_resolution text,
    ADD COLUMN model_price_service_tier_id bigint,
    ADD COLUMN channel_price_service_tier_id bigint,
    ADD CONSTRAINT ck_settlement_recovery_requested_service_tier CHECK (requested_service_tier IS NULL OR requested_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_settlement_recovery_actual_service_tier CHECK (actual_service_tier IS NULL OR actual_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_settlement_recovery_settled_service_tier CHECK (settled_service_tier IS NULL OR settled_service_tier IN ('standard', 'fast')),
    ADD CONSTRAINT ck_settlement_recovery_upstream_service_tier CHECK (upstream_service_tier IS NULL OR btrim(upstream_service_tier) <> ''),
    ADD CONSTRAINT ck_settlement_recovery_service_tier_resolution CHECK (
        service_tier_resolution IS NULL OR service_tier_resolution IN (
            'upstream_response',
            'standard_fallback_missing',
            'standard_fallback_unknown',
            'standard_fallback_fast_price_missing'
        )
    );

-- 风险敞口只记录潜在 Fast Provider 增量，不进入成本快照或 Provider ledger。
CREATE SEQUENCE public.provider_service_tier_cost_risks_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.provider_service_tier_cost_risks (
    id bigint NOT NULL DEFAULT nextval('public.provider_service_tier_cost_risks_id_seq'::regclass),
    provider_id bigint NOT NULL,
    request_record_id bigint NOT NULL,
    request_attempt_id bigint NOT NULL,
    estimated_increment_amount numeric(20,10),
    settled_amount numeric(20,10) NOT NULL,
    currency text NOT NULL,
    reason_code text NOT NULL,
    reason text NOT NULL,
    upstream_service_tier text,
    settled_service_tier text NOT NULL,
    service_tier_resolution text NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT provider_service_tier_cost_risks_pkey PRIMARY KEY (id),
    CONSTRAINT uq_provider_service_tier_cost_risks_attempt UNIQUE (request_attempt_id),
    CONSTRAINT provider_service_tier_cost_risks_provider_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id),
    CONSTRAINT provider_service_tier_cost_risks_request_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id),
    CONSTRAINT provider_service_tier_cost_risks_attempt_fkey FOREIGN KEY (request_attempt_id, request_record_id)
        REFERENCES public.request_attempts(id, request_record_id),
    CONSTRAINT ck_provider_service_tier_cost_risks_increment CHECK (estimated_increment_amount IS NULL OR estimated_increment_amount >= 0),
    CONSTRAINT ck_provider_service_tier_cost_risks_settled CHECK (settled_amount >= 0),
    CONSTRAINT ck_provider_service_tier_cost_risks_currency CHECK (btrim(currency) <> ''),
    CONSTRAINT ck_provider_service_tier_cost_risks_reason CHECK (btrim(reason_code) <> '' AND btrim(reason) <> ''),
    CONSTRAINT ck_provider_service_tier_cost_risks_tier CHECK (settled_service_tier IN ('standard', 'fast'))
);

ALTER SEQUENCE public.provider_service_tier_cost_risks_id_seq
    OWNED BY public.provider_service_tier_cost_risks.id;

CREATE INDEX idx_provider_service_tier_cost_risks_provider_created
    ON public.provider_service_tier_cost_risks (provider_id, created_at DESC, id DESC);
