-- Request record 是一次用户可见的 Unio API 请求事实。
CREATE SEQUENCE public.request_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.request_records (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_id: 对外展示和日志串联的请求 ID。--
    request_id text NOT NULL,
    -- user_id: 发起请求的用户 ID。--
    user_id bigint NOT NULL,
    -- api_key_id: 发起请求的 API Key ID。--
    api_key_id bigint NOT NULL,
    -- requested_model_id: 用户请求的模型 ID。--
    requested_model_id text NOT NULL,
    -- ingress_protocol: 客户调用 Unio 时使用的公开协议族。--
    ingress_protocol text NOT NULL,
    -- operation: 客户调用的公开协议操作。--
    operation text NOT NULL,
    -- response_model_id: 最终响应使用的模型 ID。--
    response_model_id text,
    -- response_protocol: 返回给客户的协议族，未产生响应时为空。--
    response_protocol text,
    response_id text,
    stream boolean NOT NULL,
    status text NOT NULL,
    final_provider_id bigint,
    final_channel_id bigint,
    error_code text,
    error_message text,
    internal_error_detail text,
    delivery_status text DEFAULT 'not_started'::text NOT NULL,
    response_started_at timestamp with time zone,
    response_completed_at timestamp with time zone,
    started_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    route_id bigint,
    reasoning_effort text,
    reasoning_budget_tokens integer,
    client_ip text,
    CONSTRAINT ck_request_records_delivery_completed_at CHECK ((((delivery_status = 'completed'::text) AND (response_completed_at IS NOT NULL)) OR ((delivery_status <> 'completed'::text) AND (response_completed_at IS NULL)))),
    CONSTRAINT ck_request_records_protocol_operation CHECK ((((ingress_protocol = 'openai'::text) AND (operation = ANY (ARRAY['chat_completions'::text, 'responses'::text]))) OR ((ingress_protocol = 'anthropic'::text) AND (operation = 'messages'::text)))),
    CONSTRAINT request_records_delivery_status_check CHECK ((delivery_status = ANY (ARRAY['not_started'::text, 'in_progress'::text, 'completed'::text, 'interrupted'::text]))),
    CONSTRAINT request_records_ingress_protocol_check CHECK ((ingress_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
    CONSTRAINT request_records_operation_check CHECK ((operation = ANY (ARRAY['chat_completions'::text, 'messages'::text, 'responses'::text]))),
    CONSTRAINT request_records_reasoning_budget_tokens_check CHECK (((reasoning_budget_tokens IS NULL) OR (reasoning_budget_tokens >= 0))),
    CONSTRAINT request_records_response_id_check CHECK (((response_id IS NULL) OR (response_id <> ''::text))),
    CONSTRAINT request_records_response_protocol_check CHECK (((response_protocol IS NULL) OR (response_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text])))),
    CONSTRAINT request_records_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text, 'canceled'::text])))
);

ALTER SEQUENCE public.request_records_id_seq OWNED BY public.request_records.id;

ALTER TABLE ONLY public.request_records ALTER COLUMN id SET DEFAULT nextval('public.request_records_id_seq'::regclass);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_request_id_key UNIQUE (request_id);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT uq_request_records_id_user UNIQUE (id, user_id);

CREATE INDEX idx_request_records_api_key_created_at ON public.request_records USING btree (api_key_id, created_at DESC);

CREATE INDEX idx_request_records_status_created_at ON public.request_records USING btree (status, created_at DESC);

CREATE INDEX idx_request_records_user_created_at ON public.request_records USING btree (user_id, created_at DESC);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_api_key_id_fkey FOREIGN KEY (api_key_id) REFERENCES public.api_keys(id);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_final_channel_id_fkey FOREIGN KEY (final_channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_final_provider_id_fkey FOREIGN KEY (final_provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.request_records
    ADD CONSTRAINT request_records_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000046_drop_capability_gate_columns]
-- 移除能力闸门（DEC-024 / DESIGN-capability-manual-declaration）：删除 observe/enforce 审计列。
-- 能力不再于请求热路径判定，required_capabilities 推断与 capability_check_result 审计随闸门一并删除。
-- [000058_collapse_projects_into_users]
-- 折叠 user → project → api_key 三级为 user → api_key 两级，彻底移除 projects 概念。
-- API Key、模型策略与请求归属全部直接挂在用户上。
-- 同时把线路改为 API Key 必填：彻底移除「用户/项目默认线路」回落，线路只认 api_keys.route_id。
-- 数据无需保留，但仍写正确回填，保证存量库平滑迁移。
--
-- 1. api_keys.project_id → api_keys.user_id（API Key 直接归属用户）。
-- [000064_add_request_records_route_reasoning_ip]
-- 请求记录富化（批二）：线路快照 + 推理强度归一 + 客户端 IP。
-- route_id 为请求创建时 API Key 绑定线路的快照：即使之后 Key 换绑线路，历史请求仍据此显示当时线路
--   （列表按 route_id JOIN routes 取名；历史行 NULL 时回落到 Key 当前绑定）。
-- reasoning_effort 为跨协议归一档位（none/minimal/low/medium/high/xhigh）：OpenAI 取 reasoning_effort，
--   Anthropic 由 thinking.budget_tokens 归一（映射见 PLAN-request-records-redesign）。
-- reasoning_budget_tokens 保留 Anthropic 原始预算（OpenAI 为 NULL）。client_ip 为客户端来源 IP（无地理）。
