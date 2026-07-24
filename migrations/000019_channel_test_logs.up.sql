-- 渠道检测日志：记录渠道凭据有效性相关的「事件历史」。
-- channels 表只保留当前布尔 credential_valid；本表存 when/why 与每次检测明细，供详情页回放。
-- 写入口径（R1(b)，节流防刷屏）：
--   - worker 巡检：仅在「检测失败」或「credential_valid 发生跳变」时写；健康且状态未变的成功探测不写。
--   - 手动检测：管理员显式操作，总写一条（留痕）。
--   - 运行时连续 401 翻失效：写一条 source='runtime_401'。
-- 保留：按渠道保留最近 N 条（CHANNEL_TEST_LOG_RETENTION_PER_CHANNEL，默认 200），worker 每轮清理。
CREATE SEQUENCE public.channel_test_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_test_logs (
    id bigint NOT NULL,
    channel_id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- 事件来源：worker（自动巡检）/ manual（管理员检测）/ runtime_401（网关连续 401 翻失效）。
    source text NOT NULL,
    success boolean NOT NULL,
    -- 失败稳定错误码（credential_invalid / model_unavailable / timeout / unreachable / rate_limited / ...）；成功为 NULL。
    error_code text,
    http_status integer,
    latency_ms integer,
    tested_model text,
    -- 本次事件后的 credential_valid 状态，便于回放跳变。
    credential_valid_after boolean NOT NULL,
    message text,
    upstream_error text,
    -- [P4 §4.4] 检测冻结的三类 expected revision 与是否真的应用了状态跳变；stale 结果只留历史日志。
    tested_origin_base_url_revision bigint,
    tested_origin_status_revision bigint,
    tested_config_revision bigint,
    state_change_applied boolean DEFAULT false NOT NULL,
    CONSTRAINT channel_test_logs_latency_ms_check CHECK (((latency_ms IS NULL) OR (latency_ms >= 0))),
    CONSTRAINT channel_test_logs_tested_base_url_revision_check CHECK (((tested_origin_base_url_revision IS NULL) OR (tested_origin_base_url_revision >= 1))),
    CONSTRAINT channel_test_logs_tested_status_revision_check CHECK (((tested_origin_status_revision IS NULL) OR (tested_origin_status_revision >= 1))),
    CONSTRAINT channel_test_logs_tested_config_revision_check CHECK (((tested_config_revision IS NULL) OR (tested_config_revision >= 1))),
    CONSTRAINT channel_test_logs_source_check CHECK ((source = ANY (ARRAY['worker'::text, 'manual'::text, 'runtime_401'::text, 'credential_rotate'::text, 'permission_recheck'::text])))
);

ALTER SEQUENCE public.channel_test_logs_id_seq OWNED BY public.channel_test_logs.id;

ALTER TABLE ONLY public.channel_test_logs ALTER COLUMN id SET DEFAULT nextval('public.channel_test_logs_id_seq'::regclass);

ALTER TABLE ONLY public.channel_test_logs
    ADD CONSTRAINT channel_test_logs_pkey PRIMARY KEY (id);

CREATE INDEX idx_channel_test_logs_channel_created ON public.channel_test_logs USING btree (channel_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.channel_test_logs
    ADD CONSTRAINT channel_test_logs_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000067_add_channel_test_logs_upstream_error]
-- 渠道检测日志新增「上游原始错误」列：失败时记录上游返回的错误响应体截断快照（约 2KB 上限）。
-- error_code/message 是归类后的稳定码与可读中文原因；upstream_error 保留上游原文，供排障时看到完整错误。
-- 可空：成功、无响应体（连不上/超时）或未捕获时为 NULL。
