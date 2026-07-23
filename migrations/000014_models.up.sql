-- Model 是 Unio 对外暴露和计费的模型目录。
CREATE SEQUENCE public.models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.models (
    -- id: 主键。--
    id bigint NOT NULL,
    -- model_id: OpenAI-compatible API 对外暴露的模型 ID。--
    model_id text NOT NULL,
    -- display_name: 模型展示名称。--
    display_name text NOT NULL,
    -- owned_by: 模型归属方展示字段。--
    owned_by text NOT NULL,
    -- status: 模型启停状态（对应能力架构 Layer 1 的 enabled 语义）。--
    status text NOT NULL,
    -- context_window_tokens: 上下文窗口 token 数（元数据/展示，不用于计费）。--
    context_window_tokens bigint,
    -- max_output_tokens: 模型最大输出 token 上限；预授权按模型兜底的数据源（GAP-12-010），不用于计费。--
    max_output_tokens bigint,
    -- input_price_usd_per_million_tokens: 输入价格基线（USD/百万 token），仅 catalog 展示，绝不用于计费（计费以 prices/channel_cost_prices 为准）。--
    input_price_usd_per_million_tokens numeric(20,10),
    output_price_usd_per_million_tokens numeric(20,10),
    release_date date,
    source text DEFAULT 'manual'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT models_context_window_tokens_check CHECK (((context_window_tokens IS NULL) OR (context_window_tokens > 0))),
    CONSTRAINT models_input_price_usd_per_million_tokens_check CHECK (((input_price_usd_per_million_tokens IS NULL) OR (input_price_usd_per_million_tokens >= (0)::numeric))),
    CONSTRAINT models_max_output_tokens_check CHECK (((max_output_tokens IS NULL) OR (max_output_tokens > 0))),
    CONSTRAINT models_output_price_usd_per_million_tokens_check CHECK (((output_price_usd_per_million_tokens IS NULL) OR (output_price_usd_per_million_tokens >= (0)::numeric))),
    CONSTRAINT models_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'catalog'::text]))),
    CONSTRAINT models_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text])))
);

ALTER SEQUENCE public.models_id_seq OWNED BY public.models.id;

ALTER TABLE ONLY public.models ALTER COLUMN id SET DEFAULT nextval('public.models_id_seq'::regclass);

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_model_id_key UNIQUE (model_id);

ALTER TABLE ONLY public.models
    ADD CONSTRAINT models_pkey PRIMARY KEY (id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000029_models_decouple_catalog]
-- 阶段 14：models 表与 models.dev 目录解耦。
-- 目录专属列迁出到 model_catalog / model_catalog_links；models 只保留运营事实。
--
-- 先把历史 seed/import 行的 source 收敛到新枚举，避免 CHECK 收紧时失败（开发期 models 通常为空）。
-- [000037_add_models_capability_autocalibrate]
-- 能力自动校正（被动证据式，DESIGN-capability-autocalibration）：per-model 开关。
-- off=不学习；suggest=只产生建议待人工采纳（默认）；auto=强证据自动补、弱证据仍只建议。
-- [000045_drop_capability_autocalibration]
-- 移除能力自动校正与证据 v2（DEC-024 / DESIGN-capability-manual-declaration）。
-- 自动校正与 used_capabilities/delivery_mode 证据链全部废止；能力改为人工声明。
