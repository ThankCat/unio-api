-- 渠道上游模型发现与逐模型验证事实。发现不修改模型、绑定、渠道状态或 credential_valid。
CREATE SEQUENCE public.channel_model_discovery_runs_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.channel_model_discovery_runs (
    id bigint NOT NULL,
    channel_id bigint NOT NULL,
    source text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    channel_config_revision bigint NOT NULL,
    provider_origin_revision bigint NOT NULL,
    provider_status_revision bigint NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    model_count integer NOT NULL DEFAULT 0,
    warning_code text,
    error_code text,
    message text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    CONSTRAINT channel_model_discovery_runs_pkey PRIMARY KEY (id),
    CONSTRAINT channel_model_discovery_runs_source_check CHECK (source IN ('manual', 'setup', 'scheduled')),
    CONSTRAINT channel_model_discovery_runs_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'stale')),
    CONSTRAINT channel_model_discovery_runs_revision_check CHECK (
        channel_config_revision > 0 AND provider_origin_revision > 0 AND provider_status_revision > 0
    ),
    CONSTRAINT channel_model_discovery_runs_attempt_check CHECK (attempt_count >= 0),
    CONSTRAINT channel_model_discovery_runs_count_check CHECK (model_count >= 0)
);

ALTER SEQUENCE public.channel_model_discovery_runs_id_seq OWNED BY public.channel_model_discovery_runs.id;
ALTER TABLE ONLY public.channel_model_discovery_runs
    ALTER COLUMN id SET DEFAULT nextval('public.channel_model_discovery_runs_id_seq'::regclass);
ALTER TABLE ONLY public.channel_model_discovery_runs
    ADD CONSTRAINT channel_model_discovery_runs_channel_id_fkey FOREIGN KEY (channel_id)
    REFERENCES public.channels(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX uq_channel_model_discovery_runs_active
    ON public.channel_model_discovery_runs (channel_id)
    WHERE status IN ('queued', 'running');
CREATE INDEX idx_channel_model_discovery_runs_channel_created
    ON public.channel_model_discovery_runs (channel_id, created_at DESC, id DESC);
CREATE INDEX idx_channel_model_discovery_runs_claim
    ON public.channel_model_discovery_runs (next_attempt_at, id)
    WHERE status = 'queued';

CREATE TABLE public.channel_model_discovery_items (
    run_id bigint NOT NULL,
    upstream_model text NOT NULL,
    owned_by text,
    upstream_created_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_model_discovery_items_pkey PRIMARY KEY (run_id, upstream_model),
    CONSTRAINT channel_model_discovery_items_model_check CHECK (btrim(upstream_model) <> ''),
    CONSTRAINT channel_model_discovery_items_run_id_fkey FOREIGN KEY (run_id)
    REFERENCES public.channel_model_discovery_runs(id) ON DELETE CASCADE
);

CREATE SEQUENCE public.channel_model_verification_runs_id_seq
    START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.channel_model_verification_runs (
    id bigint NOT NULL,
    channel_id bigint NOT NULL,
    source text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    channel_config_revision bigint NOT NULL,
    provider_origin_revision bigint NOT NULL,
    provider_status_revision bigint NOT NULL,
    total_count integer NOT NULL DEFAULT 0,
    succeeded_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    error_code text,
    message text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    CONSTRAINT channel_model_verification_runs_pkey PRIMARY KEY (id),
    CONSTRAINT channel_model_verification_runs_source_check CHECK (source IN ('manual', 'setup')),
    CONSTRAINT channel_model_verification_runs_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'stale')),
    CONSTRAINT channel_model_verification_runs_revision_check CHECK (
        channel_config_revision > 0 AND provider_origin_revision > 0 AND provider_status_revision > 0
    ),
    CONSTRAINT channel_model_verification_runs_count_check CHECK (
        total_count >= 0 AND succeeded_count >= 0 AND failed_count >= 0
        AND succeeded_count + failed_count <= total_count
    )
);

ALTER SEQUENCE public.channel_model_verification_runs_id_seq OWNED BY public.channel_model_verification_runs.id;
ALTER TABLE ONLY public.channel_model_verification_runs
    ALTER COLUMN id SET DEFAULT nextval('public.channel_model_verification_runs_id_seq'::regclass);
ALTER TABLE ONLY public.channel_model_verification_runs
    ADD CONSTRAINT channel_model_verification_runs_channel_id_fkey FOREIGN KEY (channel_id)
    REFERENCES public.channels(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX uq_channel_model_verification_runs_active
    ON public.channel_model_verification_runs (channel_id)
    WHERE status IN ('queued', 'running');
CREATE INDEX idx_channel_model_verification_runs_channel_created
    ON public.channel_model_verification_runs (channel_id, created_at DESC, id DESC);
CREATE INDEX idx_channel_model_verification_runs_claim
    ON public.channel_model_verification_runs (created_at, id)
    WHERE status = 'queued';

CREATE TABLE public.channel_model_verification_items (
    id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    run_id bigint NOT NULL,
    model_id bigint NOT NULL,
    upstream_model text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    success boolean,
    http_status integer NOT NULL DEFAULT 0,
    error_code text,
    message text,
    latency_ms bigint,
    provider_probe_record_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT channel_model_verification_items_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'skipped', 'stale')),
    CONSTRAINT channel_model_verification_items_model_check CHECK (btrim(upstream_model) <> ''),
    CONSTRAINT channel_model_verification_items_http_check CHECK (http_status >= 0 AND http_status <= 599),
    CONSTRAINT channel_model_verification_items_latency_check CHECK (latency_ms IS NULL OR latency_ms >= 0),
    CONSTRAINT channel_model_verification_items_run_id_fkey FOREIGN KEY (run_id)
    REFERENCES public.channel_model_verification_runs(id) ON DELETE CASCADE,
    CONSTRAINT channel_model_verification_items_model_id_fkey FOREIGN KEY (model_id)
    REFERENCES public.models(id),
    CONSTRAINT channel_model_verification_items_probe_id_fkey FOREIGN KEY (provider_probe_record_id)
    REFERENCES public.provider_probe_records(id),
    CONSTRAINT channel_model_verification_items_run_model_key UNIQUE (run_id, model_id)
);

CREATE INDEX idx_channel_model_verification_items_binding
    ON public.channel_model_verification_items (model_id, upstream_model, created_at DESC, id DESC);

