-- provider_routing_operations 持久化 Provider origin/status 围栏操作，作为 Redis 操作记录的恢复依据。
-- 状态机：preparing -> prepared -> db_committed -> committed；仅 preparing|prepared 可 -> aborted。
CREATE SEQUENCE public.provider_routing_operations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.provider_routing_operations (
    id bigint NOT NULL,
    token text NOT NULL,
    kind text NOT NULL,
    provider_id bigint NOT NULL,
    transitions jsonb NOT NULL,
    payload_hash text NOT NULL,
    state text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT provider_routing_operations_token_check CHECK ((token <> ''::text)),
    CONSTRAINT provider_routing_operations_payload_hash_check CHECK ((payload_hash <> ''::text)),
    CONSTRAINT provider_routing_operations_kind_check CHECK ((kind = ANY (ARRAY['origin'::text, 'status'::text]))),
    CONSTRAINT provider_routing_operations_state_check CHECK ((state = ANY (ARRAY['preparing'::text, 'prepared'::text, 'db_committed'::text, 'committed'::text, 'aborted'::text]))),
    CONSTRAINT ck_provider_routing_operations_completed_at CHECK (((state = ANY (ARRAY['committed'::text, 'aborted'::text])) = (completed_at IS NOT NULL)))
);

ALTER SEQUENCE public.provider_routing_operations_id_seq OWNED BY public.provider_routing_operations.id;

ALTER TABLE ONLY public.provider_routing_operations ALTER COLUMN id SET DEFAULT nextval('public.provider_routing_operations_id_seq'::regclass);

ALTER TABLE ONLY public.provider_routing_operations
    ADD CONSTRAINT provider_routing_operations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.provider_routing_operations
    ADD CONSTRAINT provider_routing_operations_token_key UNIQUE (token);

CREATE UNIQUE INDEX uq_provider_routing_operations_active_provider
    ON public.provider_routing_operations USING btree (provider_id)
    WHERE (state <> ALL (ARRAY['committed'::text, 'aborted'::text]));

CREATE INDEX idx_provider_routing_operations_state
    ON public.provider_routing_operations USING btree (state)
    WHERE (state <> ALL (ARRAY['committed'::text, 'aborted'::text]));

ALTER TABLE ONLY public.provider_routing_operations
    ADD CONSTRAINT provider_routing_operations_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE RESTRICT;
