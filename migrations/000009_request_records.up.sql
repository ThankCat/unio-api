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
    -- endpoint: 客户调用的公开协议操作。--
    endpoint text NOT NULL,
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
    gateway_first_token_at timestamp with time zone,
    response_completed_at timestamp with time zone,
    started_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    reasoning_effort text,
    reasoning_budget_tokens integer,
    client_ip text,
    requested_service_tier text,
    actual_service_tier text,
    settled_service_tier text,
    service_tier_resolution text,
    -- client_thread_id: 客户端会话线程标识（Codex thread_id），跨轮稳定。--
    client_thread_id text,
    -- client_turn_id: 单轮标识（Codex turn_id），一轮内多次上游尝试共享。--
    client_turn_id text,
    -- client_request_kind: 客户端声明的请求种类（Codex request_kind，如 turn/compact）。--
    client_request_kind text,
    CONSTRAINT ck_request_records_actual_service_tier CHECK (((actual_service_tier IS NULL) OR (actual_service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT ck_request_records_delivery_completed_at CHECK ((((delivery_status = 'completed'::text) AND (response_completed_at IS NOT NULL)) OR ((delivery_status <> 'completed'::text) AND (response_completed_at IS NULL)))),
    CONSTRAINT ck_request_records_protocol_endpoint CHECK ((((ingress_protocol = 'openai'::text) AND (endpoint = ANY (ARRAY['chat_completions'::text, 'responses'::text]))) OR ((ingress_protocol = 'anthropic'::text) AND (endpoint = 'messages'::text)))),
    CONSTRAINT ck_request_records_requested_service_tier CHECK (((requested_service_tier IS NULL) OR (requested_service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT ck_request_records_service_tier_resolution CHECK (((service_tier_resolution IS NULL) OR (service_tier_resolution = ANY (ARRAY['upstream_response'::text, 'standard_fallback_missing'::text, 'standard_fallback_unknown'::text, 'standard_fallback_fast_price_missing'::text])))),
    CONSTRAINT ck_request_records_settled_service_tier CHECK (((settled_service_tier IS NULL) OR (settled_service_tier = ANY (ARRAY['standard'::text, 'fast'::text])))),
    CONSTRAINT request_records_delivery_status_check CHECK ((delivery_status = ANY (ARRAY['not_started'::text, 'in_progress'::text, 'completed'::text, 'interrupted'::text]))),
    CONSTRAINT request_records_endpoint_check CHECK ((endpoint = ANY (ARRAY['chat_completions'::text, 'messages'::text, 'responses'::text]))),
    CONSTRAINT request_records_ingress_protocol_check CHECK ((ingress_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
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

CREATE INDEX idx_request_records_created_at_id ON public.request_records USING btree (created_at DESC, id DESC);

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

-- 按线程回溯整段会话是主要查询形态；仅对非空值建索引，
-- 避免为其它协议的 NULL 行付出代价。
CREATE INDEX idx_request_records_client_thread_created_at ON public.request_records USING btree (client_thread_id, created_at DESC) WHERE (client_thread_id IS NOT NULL);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000046_drop_capability_gate_columns]
-- DEC-024 移除能力闸门：删除 observe/enforce 审计列。
-- 能力不再于请求热路径判定，required_capabilities 推断与 capability_check_result 审计随闸门一并删除。
-- [归属层级]
-- user → api_key 两级，没有中间的项目层：请求归属直接落到用户。
-- [请求记录富化]
-- reasoning_effort 为跨协议归一档位（none/minimal/low/medium/high/xhigh）：OpenAI 取 reasoning_effort，
--   Anthropic 由 thinking.budget_tokens 归一。
-- reasoning_budget_tokens 保留 Anthropic 原始预算（OpenAI 为 NULL）。client_ip 为客户端来源 IP（无地理）。
-- Migration renumbered after merging Provider Origin into Provider.
