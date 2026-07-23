-- Model capability sync job 记录 models.dev 能力同步任务的执行审计（worker 逻辑见阶段 12 cron，本表先承载状态）。
CREATE SEQUENCE public.model_capability_sync_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.model_capability_sync_jobs (
    -- id: 主键。--
    id bigint NOT NULL,
    -- source: 同步来源。--
    source text NOT NULL,
    -- status: 任务状态机。--
    status text NOT NULL,
    -- started_at: 任务开始执行时间，空表示尚未开始。--
    started_at timestamp with time zone,
    -- finished_at: 任务结束时间，空表示未结束。--
    finished_at timestamp with time zone,
    -- stats_json: 同步统计（新增/更新/标记删除计数等），结构由 worker 约定。--
    stats_json jsonb,
    -- error_text: 失败原因摘要，仅在 status=failed 时有意义。--
    error_text text,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_capability_sync_jobs_source_check CHECK ((source = ANY (ARRAY['models_dev'::text, 'manual'::text]))),
    CONSTRAINT model_capability_sync_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text])))
);

ALTER SEQUENCE public.model_capability_sync_jobs_id_seq OWNED BY public.model_capability_sync_jobs.id;

ALTER TABLE ONLY public.model_capability_sync_jobs ALTER COLUMN id SET DEFAULT nextval('public.model_capability_sync_jobs_id_seq'::regclass);

ALTER TABLE ONLY public.model_capability_sync_jobs
    ADD CONSTRAINT model_capability_sync_jobs_pkey PRIMARY KEY (id);

CREATE INDEX idx_model_capability_sync_jobs_status ON public.model_capability_sync_jobs USING btree (status, created_at DESC);
