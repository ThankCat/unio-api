-- 每个进入路由规划的请求对应一条完整、结构化的路由决策 trace。
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
    algorithm_version text DEFAULT 'objective_v1' NOT NULL,
    trace_status text DEFAULT 'partial' NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL,
    eligible_count integer DEFAULT 0 NOT NULL,
    baseline_order bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    actual_scan_order bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    attempted_channel_ids bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    selected_channel_id bigint,
    fallback_count integer DEFAULT 0 NOT NULL,
    final_result text,
    sticky_key_present boolean DEFAULT false NOT NULL,
    sticky_before_channel_id bigint,
    sticky_before_version bigint,
    sticky_action text,
    sticky_reason text,
    sticky_after_channel_id bigint,
    sticky_after_version bigint,
    capacity_wait_ms integer,
    capacity_wait_result text,
    trace_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamptz DEFAULT now() NOT NULL,
    updated_at timestamptz DEFAULT now() NOT NULL,
    CONSTRAINT routing_decision_traces_pkey PRIMARY KEY (id),
    CONSTRAINT routing_decision_traces_request_key UNIQUE (request_record_id),
    CONSTRAINT routing_decision_traces_pool_size_check CHECK (pool_size >= 0),
    CONSTRAINT routing_decision_traces_status_check CHECK (trace_status = ANY (ARRAY['partial'::text, 'complete'::text, 'legacy_sampled'::text])),
    CONSTRAINT routing_decision_traces_schema_version_check CHECK (schema_version >= 0),
    CONSTRAINT routing_decision_traces_eligible_count_check CHECK (eligible_count >= 0),
    CONSTRAINT routing_decision_traces_fallback_count_check CHECK (fallback_count >= 0),
    CONSTRAINT routing_decision_traces_capacity_wait_ms_check CHECK ((capacity_wait_ms IS NULL) OR (capacity_wait_ms >= 0)),
    CONSTRAINT routing_decision_traces_payload_object CHECK (jsonb_typeof(trace_payload) = 'object'),
    CONSTRAINT routing_decision_traces_sticky_action_check CHECK ((sticky_action IS NULL) OR (sticky_action = ANY (ARRAY[
        'disabled'::text, 'miss'::text, 'hit'::text, 'bind_if_absent'::text,
        'refresh_if_current'::text, 'clear_if_current'::text,
        'preserve_on_temporary_bypass'::text, 'cas_conflict'::text, 'store_unavailable'::text
    ]))),
    CONSTRAINT routing_decision_traces_final_result_check CHECK ((final_result IS NULL) OR (final_result = ANY (ARRAY[
        'success'::text, 'client_canceled'::text, 'capacity_exhausted'::text,
        'rate_limited'::text, 'no_available_channel'::text, 'upstream_failed'::text,
        'gateway_error'::text
    ]))),
    CONSTRAINT routing_decision_traces_capacity_wait_result_check CHECK ((capacity_wait_result IS NULL) OR (capacity_wait_result = ANY (ARRAY[
        'acquired'::text, 'capacity_exhausted'::text, 'rate_limited'::text,
        'canceled'::text, 'not_waited'::text
    ])))
);

ALTER SEQUENCE public.routing_decision_traces_id_seq OWNED BY public.routing_decision_traces.id;
ALTER TABLE public.routing_decision_traces
    ADD CONSTRAINT routing_decision_traces_request_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id) ON DELETE CASCADE;
ALTER TABLE public.routing_decision_traces
    ADD CONSTRAINT routing_decision_traces_route_fkey FOREIGN KEY (route_id) REFERENCES public.routes(id);

CREATE INDEX idx_routing_decision_traces_route_created ON public.routing_decision_traces (route_id, created_at DESC);
CREATE INDEX idx_routing_decision_traces_created ON public.routing_decision_traces (created_at);
CREATE INDEX idx_routing_decision_traces_status ON public.routing_decision_traces (trace_status);

COMMENT ON COLUMN public.routing_decision_traces.trace_status IS
    'partial=已开始规划；complete=已收口；legacy_sampled=改造前的旧采样行（非完整 trace）。';
COMMENT ON COLUMN public.routing_decision_traces.trace_payload IS
    '完整结构化路由过程，禁止只存人读拼接文本。';
COMMENT ON COLUMN public.routing_decision_traces.baseline_order IS
    '按 objective_v1 总分与稳定 tie-break 排出的基准 channel_id 顺序。';
COMMENT ON COLUMN public.routing_decision_traces.actual_scan_order IS
    'Sticky 置顶或临时绕行后，本请求真实使用的候选扫描顺序。';
COMMENT ON COLUMN public.routing_decision_traces.attempted_channel_ids IS
    '本请求真实发起过上游调用的 channel_id 顺序，用于证明单请求不重复尝试同一渠道。';
COMMENT ON COLUMN public.routing_decision_traces.sticky_action IS
    '本请求对 Sticky 绑定采取的稳定动作。';
COMMENT ON COLUMN public.routing_decision_traces.capacity_wait_result IS
    '全池并发满后的有界短等结果。';
-- Migration renumbered after merging Provider Origin into Provider.
