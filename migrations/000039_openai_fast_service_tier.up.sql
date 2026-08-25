-- OpenAI Fast 采用与 Standard 同一价格窗口下的独立精确价格向量。
CREATE SEQUENCE public.model_price_service_tiers_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.model_price_service_tiers (
    id bigint NOT NULL,
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
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sale_uncached_input_price numeric(20,10),
    sale_cache_read_input_price numeric(20,10),
    sale_cache_write_5m_input_price numeric(20,10),
    sale_cache_write_1h_input_price numeric(20,10),
    sale_cache_write_30m_input_price numeric(20,10),
    sale_output_price numeric(20,10),
    sale_reasoning_output_price numeric(20,10),
    CONSTRAINT ck_model_price_service_tiers_cache_read CHECK (((cache_read_input_price IS NULL) OR (cache_read_input_price >= (0)::numeric))),
    CONSTRAINT ck_model_price_service_tiers_cache_write_1h CHECK (((cache_write_1h_input_price IS NULL) OR (cache_write_1h_input_price >= (0)::numeric))),
    CONSTRAINT ck_model_price_service_tiers_cache_write_30m CHECK (((cache_write_30m_input_price IS NULL) OR (cache_write_30m_input_price >= (0)::numeric))),
    CONSTRAINT ck_model_price_service_tiers_cache_write_5m CHECK (((cache_write_5m_input_price IS NULL) OR (cache_write_5m_input_price >= (0)::numeric))),
    CONSTRAINT ck_model_price_service_tiers_output CHECK ((output_price >= (0)::numeric)),
    CONSTRAINT ck_model_price_service_tiers_reasoning CHECK (((reasoning_output_price IS NULL) OR (reasoning_output_price >= (0)::numeric))),
    CONSTRAINT ck_model_price_service_tiers_reference CHECK ((((reference_source IS NULL) AND (reference_checked_at IS NULL)) OR ((btrim(reference_source) <> ''::text) AND (reference_checked_at IS NOT NULL)))),
    CONSTRAINT ck_model_price_service_tiers_tier CHECK ((service_tier = 'fast'::text)),
    CONSTRAINT ck_model_price_service_tiers_uncached CHECK ((uncached_input_price >= (0)::numeric)),
    CONSTRAINT ck_model_price_tiers_sale_all_or_none CHECK ((((sale_uncached_input_price IS NULL) AND (sale_cache_read_input_price IS NULL) AND (sale_cache_write_5m_input_price IS NULL) AND (sale_cache_write_1h_input_price IS NULL) AND (sale_cache_write_30m_input_price IS NULL) AND (sale_output_price IS NULL) AND (sale_reasoning_output_price IS NULL)) OR ((sale_uncached_input_price IS NOT NULL) AND (sale_output_price IS NOT NULL)))),
    CONSTRAINT ck_model_price_tiers_sale_non_negative CHECK ((((sale_uncached_input_price IS NULL) OR (sale_uncached_input_price >= (0)::numeric)) AND ((sale_cache_read_input_price IS NULL) OR (sale_cache_read_input_price >= (0)::numeric)) AND ((sale_cache_write_5m_input_price IS NULL) OR (sale_cache_write_5m_input_price >= (0)::numeric)) AND ((sale_cache_write_1h_input_price IS NULL) OR (sale_cache_write_1h_input_price >= (0)::numeric)) AND ((sale_cache_write_30m_input_price IS NULL) OR (sale_cache_write_30m_input_price >= (0)::numeric)) AND ((sale_output_price IS NULL) OR (sale_output_price >= (0)::numeric)) AND ((sale_reasoning_output_price IS NULL) OR (sale_reasoning_output_price >= (0)::numeric))))
);

ALTER TABLE ONLY public.model_price_service_tiers ALTER COLUMN id SET DEFAULT nextval('public.model_price_service_tiers_id_seq'::regclass);

ALTER TABLE ONLY public.model_price_service_tiers
    ADD CONSTRAINT model_price_service_tiers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.model_price_service_tiers
    ADD CONSTRAINT uq_model_price_service_tiers UNIQUE (model_price_id, service_tier);

ALTER TABLE ONLY public.model_price_service_tiers
    ADD CONSTRAINT model_price_service_tiers_model_price_fkey FOREIGN KEY (model_price_id) REFERENCES public.model_prices(id);

ALTER SEQUENCE public.model_price_service_tiers_id_seq
    OWNED BY public.model_price_service_tiers.id;

-- 绝对渠道成本覆盖可为 Fast 保存独立成本向量；倍率路径继续复用渠道倍率与充值倍率。
CREATE SEQUENCE public.channel_price_service_tiers_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.channel_price_service_tiers (
    id bigint NOT NULL,
    channel_price_id bigint NOT NULL,
    service_tier text NOT NULL,
    uncached_input_cost numeric(20,10) NOT NULL,
    cache_read_input_cost numeric(20,10),
    cache_write_5m_input_cost numeric(20,10),
    cache_write_1h_input_cost numeric(20,10),
    cache_write_30m_input_cost numeric(20,10),
    output_cost numeric(20,10) NOT NULL,
    reasoning_output_cost numeric(20,10),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_channel_price_service_tiers_cache_read CHECK (((cache_read_input_cost IS NULL) OR (cache_read_input_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_1h CHECK (((cache_write_1h_input_cost IS NULL) OR (cache_write_1h_input_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_30m CHECK (((cache_write_30m_input_cost IS NULL) OR (cache_write_30m_input_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_price_service_tiers_cache_write_5m CHECK (((cache_write_5m_input_cost IS NULL) OR (cache_write_5m_input_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_price_service_tiers_output CHECK ((output_cost >= (0)::numeric)),
    CONSTRAINT ck_channel_price_service_tiers_reasoning CHECK (((reasoning_output_cost IS NULL) OR (reasoning_output_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_price_service_tiers_tier CHECK ((service_tier = 'fast'::text)),
    CONSTRAINT ck_channel_price_service_tiers_uncached CHECK ((uncached_input_cost >= (0)::numeric))
);

ALTER TABLE ONLY public.channel_price_service_tiers ALTER COLUMN id SET DEFAULT nextval('public.channel_price_service_tiers_id_seq'::regclass);

ALTER TABLE ONLY public.channel_price_service_tiers
    ADD CONSTRAINT channel_price_service_tiers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channel_price_service_tiers
    ADD CONSTRAINT uq_channel_price_service_tiers UNIQUE (channel_price_id, service_tier);

ALTER TABLE ONLY public.channel_price_service_tiers
    ADD CONSTRAINT channel_price_service_tiers_channel_price_fkey FOREIGN KEY (channel_price_id) REFERENCES public.channel_prices(id);

ALTER SEQUENCE public.channel_price_service_tiers_id_seq
    OWNED BY public.channel_price_service_tiers.id;

-- 快照指向档位价行的外键留在本文件：tier 表在这里才创建，
-- 挪到 price_snapshots / cost_snapshots 的建表文件会形成向前依赖。
ALTER TABLE ONLY public.price_snapshots
    ADD CONSTRAINT price_snapshots_model_price_service_tier_fkey FOREIGN KEY (model_price_service_tier_id) REFERENCES public.model_price_service_tiers(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_model_price_service_tier_fkey FOREIGN KEY (model_price_service_tier_id) REFERENCES public.model_price_service_tiers(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_channel_price_service_tier_fkey FOREIGN KEY (channel_price_service_tier_id) REFERENCES public.channel_price_service_tiers(id);

-- 风险敞口只记录潜在 Fast Provider 增量，不进入成本快照或 Provider ledger。
CREATE SEQUENCE public.provider_service_tier_cost_risks_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.provider_service_tier_cost_risks (
    id bigint NOT NULL,
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
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_provider_service_tier_cost_risks_currency CHECK ((btrim(currency) <> ''::text)),
    CONSTRAINT ck_provider_service_tier_cost_risks_increment CHECK (((estimated_increment_amount IS NULL) OR (estimated_increment_amount >= (0)::numeric))),
    CONSTRAINT ck_provider_service_tier_cost_risks_reason CHECK (((btrim(reason_code) <> ''::text) AND (btrim(reason) <> ''::text))),
    CONSTRAINT ck_provider_service_tier_cost_risks_settled CHECK ((settled_amount >= (0)::numeric)),
    CONSTRAINT ck_provider_service_tier_cost_risks_tier CHECK ((settled_service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))
);

ALTER TABLE ONLY public.provider_service_tier_cost_risks ALTER COLUMN id SET DEFAULT nextval('public.provider_service_tier_cost_risks_id_seq'::regclass);

ALTER TABLE ONLY public.provider_service_tier_cost_risks
    ADD CONSTRAINT provider_service_tier_cost_risks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.provider_service_tier_cost_risks
    ADD CONSTRAINT uq_provider_service_tier_cost_risks_attempt UNIQUE (request_attempt_id);

ALTER TABLE ONLY public.provider_service_tier_cost_risks
    ADD CONSTRAINT provider_service_tier_cost_risks_attempt_fkey FOREIGN KEY (request_attempt_id, request_record_id) REFERENCES public.request_attempts(id, request_record_id);

ALTER TABLE ONLY public.provider_service_tier_cost_risks
    ADD CONSTRAINT provider_service_tier_cost_risks_provider_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.provider_service_tier_cost_risks
    ADD CONSTRAINT provider_service_tier_cost_risks_request_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

ALTER SEQUENCE public.provider_service_tier_cost_risks_id_seq
    OWNED BY public.provider_service_tier_cost_risks.id;

CREATE INDEX idx_provider_service_tier_cost_risks_provider_created
    ON public.provider_service_tier_cost_risks (provider_id, created_at DESC, id DESC);

COMMENT ON COLUMN public.model_price_service_tiers.sale_uncached_input_price IS 'Fast 档对外绝对售价；整组为空时回退「该档基准价 × 全局售价倍率」。';
