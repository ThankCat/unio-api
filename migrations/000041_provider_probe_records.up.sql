-- Provider 模型探测的不可变事实。探测不是客户请求，不写 request_records。
CREATE SEQUENCE public.provider_probe_records_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.provider_probe_records (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    channel_id bigint NOT NULL,
    model_id bigint,
    protocol text NOT NULL,
    source text NOT NULL,
    upstream_model text NOT NULL,
    success boolean NOT NULL,
    http_status integer NOT NULL DEFAULT 0,
    error_code text,
    message text,
    latency_ms bigint,
    usage_source text,
    usage_facts jsonb,
    usage_reliable boolean NOT NULL DEFAULT false,
    cost_amount numeric(20,10),
    currency text,
    formula_version text,
    idempotency_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_probe_records_id_pkey PRIMARY KEY (id),
    CONSTRAINT provider_probe_records_source_check CHECK (btrim(source) <> ''),
    CONSTRAINT provider_probe_records_model_check CHECK (btrim(upstream_model) <> ''),
    CONSTRAINT provider_probe_records_status_check CHECK (http_status >= 0 AND http_status <= 599),
    CONSTRAINT provider_probe_records_latency_check CHECK (latency_ms IS NULL OR latency_ms >= 0),
    CONSTRAINT provider_probe_records_usage_check CHECK (
        (usage_reliable = false)
        OR (usage_reliable = true AND usage_source IS NOT NULL AND usage_facts IS NOT NULL AND cost_amount IS NOT NULL AND currency IS NOT NULL)
    ),
    CONSTRAINT provider_probe_records_cost_check CHECK (cost_amount IS NULL OR cost_amount >= 0),
    CONSTRAINT provider_probe_records_idempotency_key_check CHECK (btrim(idempotency_key) <> '')
);

ALTER SEQUENCE public.provider_probe_records_id_seq OWNED BY public.provider_probe_records.id;
ALTER TABLE ONLY public.provider_probe_records
    ALTER COLUMN id SET DEFAULT nextval('public.provider_probe_records_id_seq'::regclass);
ALTER TABLE ONLY public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_idempotency_key_key UNIQUE (idempotency_key);
ALTER TABLE ONLY public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);
ALTER TABLE ONLY public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);
ALTER TABLE ONLY public.provider_probe_records
    ADD CONSTRAINT provider_probe_records_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);

CREATE INDEX idx_provider_probe_records_provider_created
    ON public.provider_probe_records (provider_id, created_at DESC, id DESC);
CREATE INDEX idx_provider_probe_records_channel_created
    ON public.provider_probe_records (channel_id, created_at DESC, id DESC);
