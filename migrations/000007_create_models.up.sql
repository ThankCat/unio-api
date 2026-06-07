-- Model 是 Unio 对外暴露和计费的模型目录。
CREATE TABLE models (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- model_id: OpenAI-compatible API 对外暴露的模型 ID。--
    model_id TEXT NOT NULL UNIQUE,

    -- display_name: 模型展示名称。--
    display_name TEXT NOT NULL,

    -- owned_by: 模型归属方展示字段。--
    owned_by TEXT NOT NULL,

    -- status: 模型启停状态（对应能力架构 Layer 1 的 enabled 语义）。--
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),

    -- canonical_id: 能力架构 Layer 1 规范模型标识（如 deepseek/deepseek-v4-pro），models.dev 同步 join key；可空唯一。--
    canonical_id TEXT UNIQUE,

    -- lab: 模型厂商/实验室（如 deepseek、openai、anthropic），元数据展示用。--
    lab TEXT,

    -- context_window_tokens: 上下文窗口 token 数（元数据/展示，不用于计费）。--
    context_window_tokens BIGINT CHECK (context_window_tokens IS NULL OR context_window_tokens > 0),

    -- max_output_tokens: 模型最大输出 token 上限；预授权按模型兜底的数据源（GAP-12-010），不用于计费。--
    max_output_tokens BIGINT CHECK (max_output_tokens IS NULL OR max_output_tokens > 0),

    -- input_price_usd_per_million_tokens: 输入价格基线（USD/百万 token），仅 catalog 展示，绝不用于计费（计费以 prices/channel_cost_prices 为准）。--
    input_price_usd_per_million_tokens NUMERIC(20, 10) CHECK (
        input_price_usd_per_million_tokens IS NULL OR input_price_usd_per_million_tokens >= 0
    ),

    -- output_price_usd_per_million_tokens: 输出价格基线（USD/百万 token），仅 catalog 展示，绝不用于计费。--
    output_price_usd_per_million_tokens NUMERIC(20, 10) CHECK (
        output_price_usd_per_million_tokens IS NULL OR output_price_usd_per_million_tokens >= 0
    ),

    -- release_date: 模型发布日期（元数据）。--
    release_date DATE,

    -- source: 元数据来源；models.dev 同步只覆盖 seed_models_dev 行，manual 行永不被同步覆盖。--
    source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('seed_models_dev', 'manual', 'import')),

    -- removed_upstream_at: models.dev 上游删除标记时间，空值表示上游仍存在（同步逻辑见阶段 12 cron）。--
    removed_upstream_at TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- models.dev 同步按 canonical_id 反查已入库模型，仅在存在 canonical_id 的行上建索引。
CREATE INDEX idx_models_canonical_id ON models (canonical_id) WHERE canonical_id IS NOT NULL;
