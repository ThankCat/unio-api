-- Model capability sync job 记录 models.dev 能力同步任务的执行审计（worker 逻辑见阶段 12 cron，本表先承载状态）。
CREATE TABLE model_capability_sync_jobs (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- source: 同步来源。--
    source TEXT NOT NULL CHECK (source IN ('models_dev', 'manual')),

    -- status: 任务状态机。--
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),

    -- started_at: 任务开始执行时间，空表示尚未开始。--
    started_at TIMESTAMPTZ,

    -- finished_at: 任务结束时间，空表示未结束。--
    finished_at TIMESTAMPTZ,

    -- stats_json: 同步统计（新增/更新/标记删除计数等），结构由 worker 约定。--
    stats_json JSONB,

    -- error_text: 失败原因摘要，仅在 status=failed 时有意义。--
    error_text TEXT,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 运维与 worker 按状态和时间查找最近任务。
CREATE INDEX idx_model_capability_sync_jobs_status ON model_capability_sync_jobs (status, created_at DESC);
