-- origin_routing_operations 是 Origin BaseURL/status 围栏与 Provider 批量状态围栏的
-- PostgreSQL 持久操作表，作为 Redis op record 的恢复依据（P4-D06 / 计划 §4.3）。
-- 状态机：preparing -> prepared -> db_committed -> committed；仅 preparing|prepared 可 -> aborted。
CREATE SEQUENCE public.origin_routing_operations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.origin_routing_operations (
    -- id: 主键。--
    id bigint NOT NULL,
    -- token: 全局唯一安全随机操作令牌（幂等/重试/恢复关联）。--
    token text NOT NULL,
    -- kind: 操作类型。--
    kind text NOT NULL,
    -- provider_id: 归属 Provider（批量围栏与锁序权威）；永久删除前置空但保留 transitions 摘要。--
    provider_id bigint,
    -- origin_id: 单 Origin 操作的目标；批量操作为 NULL，目标集合记于 transitions。--
    origin_id bigint,
    -- transitions: 严格校验的 JSONB，保存排序后的 Provider/Origin ID 与 current/next 版本/状态摘要。--
    transitions jsonb NOT NULL,
    -- payload_hash: 完整 canonical operation 的小写 SHA-256，防同 token 被不同参数重放。--
    payload_hash text NOT NULL,
    -- state: 操作状态机当前值。--
    state text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- completed_at: 仅在 committed|aborted 终态非空。--
    completed_at timestamp with time zone,
    CONSTRAINT origin_routing_operations_token_check CHECK ((token <> ''::text)),
    CONSTRAINT origin_routing_operations_payload_hash_check CHECK ((payload_hash <> ''::text)),
    CONSTRAINT origin_routing_operations_kind_check CHECK ((kind = ANY (ARRAY['origin_create'::text, 'base_url'::text, 'status'::text, 'base_url_status'::text, 'provider_status_batch'::text]))),
    CONSTRAINT origin_routing_operations_state_check CHECK ((state = ANY (ARRAY['preparing'::text, 'prepared'::text, 'db_committed'::text, 'committed'::text, 'aborted'::text]))),
    CONSTRAINT ck_origin_routing_operations_completed_at CHECK (((state = ANY (ARRAY['committed'::text, 'aborted'::text])) = (completed_at IS NOT NULL)))
);

ALTER SEQUENCE public.origin_routing_operations_id_seq OWNED BY public.origin_routing_operations.id;

ALTER TABLE ONLY public.origin_routing_operations ALTER COLUMN id SET DEFAULT nextval('public.origin_routing_operations_id_seq'::regclass);

ALTER TABLE ONLY public.origin_routing_operations
    ADD CONSTRAINT origin_routing_operations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.origin_routing_operations
    ADD CONSTRAINT origin_routing_operations_token_key UNIQUE (token);

-- 单 Origin 同时最多一条非终态 operation。
CREATE UNIQUE INDEX uq_origin_routing_operations_active_origin
    ON public.origin_routing_operations USING btree (origin_id)
    WHERE ((origin_id IS NOT NULL) AND (state <> ALL (ARRAY['committed'::text, 'aborted'::text])));

-- 同一 Provider 批量状态围栏同时最多一条非终态 operation。
CREATE UNIQUE INDEX uq_origin_routing_operations_active_provider_batch
    ON public.origin_routing_operations USING btree (provider_id)
    WHERE ((kind = 'provider_status_batch'::text) AND (state <> ALL (ARRAY['committed'::text, 'aborted'::text])));

CREATE INDEX idx_origin_routing_operations_state ON public.origin_routing_operations USING btree (state) WHERE (state <> ALL (ARRAY['committed'::text, 'aborted'::text]));

-- 未终结 operation 阻止级联删除目标业务实体（RESTRICT）；终态历史由应用置 NULL 后清理。
ALTER TABLE ONLY public.origin_routing_operations
    ADD CONSTRAINT origin_routing_operations_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.origin_routing_operations
    ADD CONSTRAINT origin_routing_operations_origin_id_fkey FOREIGN KEY (origin_id) REFERENCES public.provider_origins(id) ON DELETE RESTRICT;
