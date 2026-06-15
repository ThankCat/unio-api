-- Model catalog 是 models.dev 的独立参考目录（菜单），运行时永不读取（阶段 14 解耦）。
-- 同步只刷新本表与 model_catalog_capabilities，不再写运行时 models 表。
CREATE TABLE model_catalog (
    -- canonical_id: models.dev 规范模型标识（lab/model，如 openai/gpt-4o），主键。--
    canonical_id TEXT PRIMARY KEY,

    -- lab: 模型厂商/实验室（canonical_id 的前缀，如 openai/anthropic），目录分组/筛选用。--
    lab TEXT NOT NULL,

    -- display_name: 模型展示名称。--
    display_name TEXT NOT NULL,

    -- context_window_tokens: 上下文窗口 token 数（元数据/展示）。--
    context_window_tokens BIGINT CHECK (context_window_tokens IS NULL OR context_window_tokens > 0),

    -- max_output_tokens: 模型最大输出 token 上限（元数据/展示）。--
    max_output_tokens BIGINT CHECK (max_output_tokens IS NULL OR max_output_tokens > 0),

    -- input_price_usd_per_million_tokens: 输入价格基线（USD/百万 token），仅展示参考，绝不用于计费。--
    input_price_usd_per_million_tokens NUMERIC(20, 10) CHECK (
        input_price_usd_per_million_tokens IS NULL OR input_price_usd_per_million_tokens >= 0
    ),

    -- output_price_usd_per_million_tokens: 输出价格基线（USD/百万 token），仅展示参考，绝不用于计费。--
    output_price_usd_per_million_tokens NUMERIC(20, 10) CHECK (
        output_price_usd_per_million_tokens IS NULL OR output_price_usd_per_million_tokens >= 0
    ),

    -- release_date: 模型发布日期（元数据）。--
    release_date DATE,

    -- removed_upstream_at: models.dev 上游下架标记时间，空表示上游仍存在（不删本地目录行）。--
    removed_upstream_at TIMESTAMPTZ,

    -- fingerprint: 本条目内容指纹（元数据 + 排序后能力提示规范化 hash），用于采纳追更对比。--
    fingerprint TEXT NOT NULL CHECK (fingerprint <> ''),

    -- synced_at: 最近一次同步刷新本条目的时间。--
    synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 目录浏览按 lab 分组/筛选。
CREATE INDEX idx_model_catalog_lab ON model_catalog (lab);

-- Model catalog capability 是目录条目的粗能力提示（来自 models.dev 布尔位/模态映射），采纳时供预填。
-- 无 source 字段（阶段 14 Q4：能力来源已无意义）。
CREATE TABLE model_catalog_capabilities (
    -- canonical_id: 所属目录条目，目录条目删除时级联清理。--
    canonical_id TEXT NOT NULL REFERENCES model_catalog (canonical_id) ON DELETE CASCADE,

    -- capability_key: 稳定能力标识，合法值由 app 层 capability 注册表校验，DB 不做枚举约束以支持只增不删。--
    capability_key TEXT NOT NULL CHECK (capability_key <> ''),

    -- support_level: 目录提示的支持级别。--
    support_level TEXT NOT NULL CHECK (support_level IN ('full', 'limited', 'unsupported')),

    -- limits: 能力的细化约束（空表示无额外约束）。--
    limits JSONB,

    -- 同一目录条目对同一能力只能有一条提示。--
    PRIMARY KEY (canonical_id, capability_key)
);
