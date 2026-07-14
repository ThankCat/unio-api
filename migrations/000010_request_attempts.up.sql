-- Request attempt 是一次 logical request 下的一次上游 channel 尝试事实。
CREATE SEQUENCE public.request_attempts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.request_attempts (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_record_id: 所属请求记录 ID。--
    request_record_id bigint NOT NULL,
    -- attempt_index: 同一请求内的尝试序号。--
    attempt_index integer NOT NULL,
    -- provider_id: 本次尝试使用的 provider ID。--
    provider_id bigint NOT NULL,
    -- channel_id: 本次尝试使用的 channel ID。--
    channel_id bigint NOT NULL,
    -- adapter_key: 本次尝试使用的 adapter 注册键。--
    adapter_key text NOT NULL,
    -- upstream_model: 本次尝试发送给上游的模型名。--
    upstream_model text NOT NULL,
    -- upstream_protocol: 本次尝试调用上游时使用的协议族。--
    upstream_protocol text NOT NULL,
    -- upstream_response_id: provider 返回的响应 ID，与客户可见 response_id 分列。--
    upstream_response_id text,
    -- upstream_response_model: 上游响应里的模型名。--
    upstream_response_model text,
    -- upstream_finish_reason: provider 返回的原始结束原因，仅用于审计。--
    upstream_finish_reason text,
    -- finish_class: 协议无关的稳定结束分类。--
    finish_class text,
    status text NOT NULL,
    upstream_status_code integer,
    upstream_request_id text,
    error_code text,
    error_message text,
    internal_error_detail text,
    response_started_at timestamp with time zone,
    final_usage_received boolean DEFAULT false NOT NULL,
    usage_mapping_version text,
    started_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    fault_party text GENERATED ALWAYS AS (
CASE
    WHEN (status = 'canceled'::text) THEN 'client'::text
    WHEN (status <> 'failed'::text) THEN NULL::text
    WHEN ((error_code ~~ 'gateway_%'::text) OR (error_code ~~ 'routing_%'::text) OR (error_code ~~ 'ledger_%'::text) OR (error_code ~~ 'config_%'::text) OR (error_code = 'adapter_not_registered'::text)) THEN 'platform'::text
    WHEN (error_code = 'client_canceled'::text) THEN 'client'::text
    WHEN (upstream_status_code BETWEEN 400 AND 499 AND upstream_status_code NOT IN (401, 403, 408, 429)) THEN 'client'::text
    ELSE 'upstream'::text
END) STORED,
    CONSTRAINT request_attempts_attempt_index_check CHECK ((attempt_index >= 0)),
    CONSTRAINT request_attempts_finish_class_check CHECK (((finish_class IS NULL) OR (finish_class = ANY (ARRAY['stop'::text, 'length'::text, 'tool_use'::text, 'content_filter'::text, 'refusal'::text, 'pause'::text, 'other'::text])))),
    CONSTRAINT request_attempts_status_check CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text, 'canceled'::text]))),
    CONSTRAINT request_attempts_upstream_protocol_check CHECK ((upstream_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
    CONSTRAINT request_attempts_upstream_response_id_check CHECK (((upstream_response_id IS NULL) OR (upstream_response_id <> ''::text))),
    CONSTRAINT request_attempts_upstream_status_code_check CHECK (((upstream_status_code IS NULL) OR ((upstream_status_code >= 100) AND (upstream_status_code <= 599)))),
    CONSTRAINT request_attempts_usage_mapping_version_check CHECK (((usage_mapping_version IS NULL) OR (usage_mapping_version <> ''::text)))
);

ALTER SEQUENCE public.request_attempts_id_seq OWNED BY public.request_attempts.id;

ALTER TABLE ONLY public.request_attempts ALTER COLUMN id SET DEFAULT nextval('public.request_attempts_id_seq'::regclass);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT request_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT request_attempts_request_record_id_attempt_index_key UNIQUE (request_record_id, attempt_index);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT uq_request_attempts_id_request UNIQUE (id, request_record_id);

CREATE INDEX idx_request_attempts_channel_created_at ON public.request_attempts USING btree (channel_id, created_at DESC);

CREATE INDEX idx_request_attempts_channel_fault ON public.request_attempts USING btree (channel_id, fault_party) WHERE (status = 'failed'::text);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT request_attempts_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT request_attempts_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.request_attempts
    ADD CONSTRAINT request_attempts_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000042_add_request_attempts_used_capabilities]
-- 能力自动校正按 key 精确命中埋点（DESIGN-capability-autocalibration TASK-H）。
--
-- request_attempts.used_capabilities：本次成功响应被 adapter 解析「真正用到」的能力 key
-- （如响应里出现 function_call → tools.function）。它取代 finish_class 作为 tools.* 的强证据来源：
--   - finish_class=tool_use 只能笼统证明「某个工具被调了」，无法区分 function/custom；
--   - 且 OpenAI Responses 直传时 finish_class 恒为 stop（Codex 主力流量），tools.* 永远拿不到证据。
-- 校正聚合按 key 命中归因；无埋点的旧行 / 其它 adapter 仍回退到 finish_class（粗粒度）。
-- [000043_add_request_attempts_delivery_mode]
-- 能力证据 v2（DESIGN-capability-evidence-v2 Phase 3 / G3）。
--
-- request_attempts.delivery_mode：本次尝试的分发方式（stream 流式 / batch 一次性）。
-- 仅作 Admin 审计与 stream 的二级佐证；不作为能力自动校正的强证据来源（一级 stream 证据走
-- used_capabilities 含 'stream'，见 DESIGN §4.2 / Q1 / Q6）。NOT NULL DEFAULT 'batch' 兼容历史行。
-- [000045_drop_capability_autocalibration]
-- 移除能力自动校正与证据 v2（DEC-024 / DESIGN-capability-manual-declaration）。
-- 自动校正与 used_capabilities/delivery_mode 证据链全部废止；能力改为人工声明。
-- [000046_drop_capability_gate_columns]
-- 移除能力闸门（DEC-024 / DESIGN-capability-manual-declaration）：删除 observe/enforce 审计列。
-- 能力不再于请求热路径判定，required_capabilities 推断与 capability_check_result 审计随闸门一并删除。
-- [000061_add_request_attempts_fault_party]
-- 归因维度 fault_party：把每次 attempt 的失败/取消归到「上游 / 客户端 / 平台」，
-- 使运维口径（渠道健康、成功率、最近错误）只在「上游故障」时归咎渠道，与运行时熔断器
-- IsChannelFaultError 一致（timeout/server/rate_limit/auth/permission=上游；bad_request/canceled=非渠道）。
--
-- 采用 STORED 生成列：由 status/error_code/upstream_status_code 派生，无需改写网关热路径，
-- 且新增 STORED 列会在迁移时对历史行一次性回填。仅影响只读运维聚合，不参与计费。
--   - canceled            → client（客户端取消）
--   - failed + 平台错误码  → platform（gateway_/routing_/ledger_/config_/adapter_not_registered）
--   - failed + client_canceled 码 → client
--   - failed + 上游 4xx（401/403/408/429 除外）→ client（请求本身问题，渠道正常拒绝）
--   - failed 其余（含超时/5xx/鉴权/限流/上游通信错误）→ upstream
--   - succeeded / running → NULL（无归因）
