CREATE SEQUENCE public.routing_decision_traces_id_seq START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;

CREATE TABLE public.routing_decision_traces (
    id bigint DEFAULT nextval('public.routing_decision_traces_id_seq'::regclass) NOT NULL,
    request_record_id bigint NOT NULL,
    route_id bigint NOT NULL,
    mode text NOT NULL,
    requested_model_id text NOT NULL,
    protocol text NOT NULL,
    endpoint text NOT NULL,
    pool_size integer DEFAULT 0 NOT NULL,
    candidate_count integer DEFAULT 0 NOT NULL,
    sticky_channel_id bigint,
    sticky_pinned boolean DEFAULT false NOT NULL,
    sticky_invalid boolean DEFAULT false NOT NULL,
    all_capacity_zero boolean DEFAULT false NOT NULL,
    margin_guard_triggered boolean DEFAULT false NOT NULL,
    abnormal boolean DEFAULT false NOT NULL,
    abnormal_reasons text[] DEFAULT '{}'::text[] NOT NULL,
    candidate_scores jsonb DEFAULT '[]'::jsonb NOT NULL,
    selected_order bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    fallback_chain jsonb DEFAULT '[]'::jsonb NOT NULL,
    algorithm_version text DEFAULT 'balanced_v1' NOT NULL,
    sampled boolean DEFAULT false NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT routing_decision_traces_pkey PRIMARY KEY (id),
    CONSTRAINT routing_decision_traces_request_key UNIQUE (request_record_id),
    CONSTRAINT routing_decision_traces_pool_size_check CHECK (pool_size >= 0),
    CONSTRAINT routing_decision_traces_candidate_count_check CHECK (candidate_count >= 0),
    CONSTRAINT routing_decision_traces_scores_array CHECK (jsonb_typeof(candidate_scores) = 'array'),
    CONSTRAINT routing_decision_traces_fallback_array CHECK (jsonb_typeof(fallback_chain) = 'array')
);

ALTER SEQUENCE public.routing_decision_traces_id_seq OWNED BY public.routing_decision_traces.id;
ALTER TABLE public.routing_decision_traces
    ADD CONSTRAINT routing_decision_traces_request_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id) ON DELETE CASCADE;
ALTER TABLE public.routing_decision_traces
    ADD CONSTRAINT routing_decision_traces_route_fkey FOREIGN KEY (route_id) REFERENCES public.routes(id);

CREATE INDEX idx_routing_decision_traces_route_created ON public.routing_decision_traces (route_id, created_at DESC);
CREATE INDEX idx_routing_decision_traces_created ON public.routing_decision_traces (created_at);
