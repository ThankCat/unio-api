-- Model catalog 是 models.dev 的独立参考目录（菜单），运行时永不读取（阶段 14 解耦）。
-- 同步只刷新本表与 model_catalog_capabilities，不再写运行时 models 表。
CREATE TABLE public.model_catalog (
    -- canonical_id: models.dev 规范模型标识（lab/model，如 openai/gpt-4o），主键。--
    canonical_id text NOT NULL,
    -- lab: 模型厂商/实验室（canonical_id 的前缀，如 openai/anthropic），目录分组/筛选用。--
    lab text NOT NULL,
    -- display_name: 模型展示名称。--
    display_name text NOT NULL,
    -- context_window_tokens: 上下文窗口 token 数（元数据/展示）。--
    context_window_tokens bigint,
    -- max_output_tokens: 模型最大输出 token 上限（元数据/展示）。--
    max_output_tokens bigint,
    -- input_price_usd_per_million_tokens: 输入价格基线（USD/百万 token），仅展示参考，绝不用于计费。--
    input_price_usd_per_million_tokens numeric(20,10),
    output_price_usd_per_million_tokens numeric(20,10),
    release_date date,
    removed_upstream_at timestamp with time zone,
    fingerprint text NOT NULL,
    synced_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_catalog_context_window_tokens_check CHECK (((context_window_tokens IS NULL) OR (context_window_tokens > 0))),
    CONSTRAINT model_catalog_fingerprint_check CHECK ((fingerprint <> ''::text)),
    CONSTRAINT model_catalog_input_price_usd_per_million_tokens_check CHECK (((input_price_usd_per_million_tokens IS NULL) OR (input_price_usd_per_million_tokens >= (0)::numeric))),
    CONSTRAINT model_catalog_max_output_tokens_check CHECK (((max_output_tokens IS NULL) OR (max_output_tokens > 0))),
    CONSTRAINT model_catalog_output_price_usd_per_million_tokens_check CHECK (((output_price_usd_per_million_tokens IS NULL) OR (output_price_usd_per_million_tokens >= (0)::numeric)))
);

ALTER TABLE ONLY public.model_catalog
    ADD CONSTRAINT model_catalog_pkey PRIMARY KEY (canonical_id);

CREATE INDEX idx_model_catalog_lab ON public.model_catalog USING btree (lab);
