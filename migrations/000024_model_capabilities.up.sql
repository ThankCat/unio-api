-- Model capability 是能力架构 Layer 2，按「模型 × 协议字段/子能力」声明该模型对某能力的支持级别。
CREATE TABLE public.model_capabilities (
    -- model_id: 能力所属模型 ID。--
    model_id bigint NOT NULL,
    -- capability_key: 稳定能力标识，合法值由 app 层 capability 注册表校验（docs/protocol/CAPABILITY_KEYS.md），DB 不做枚举约束以支持只增不删。--
    capability_key text NOT NULL,
    -- support_level: 该模型对该能力的支持级别。--
    support_level text NOT NULL,
    -- limits: 能力的细化约束（如 reasoning.effort 允许值集合、tools.max_count），空表示无额外约束。--
    limits jsonb,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_by: 最后修改者标识（admin/同步任务），空表示未知。--
    updated_by text,
    CONSTRAINT model_capabilities_capability_key_check CHECK ((capability_key <> ''::text)),
    CONSTRAINT model_capabilities_support_level_check CHECK ((support_level = ANY (ARRAY['full'::text, 'limited'::text, 'unsupported'::text])))
);

ALTER TABLE ONLY public.model_capabilities
    ADD CONSTRAINT model_capabilities_pkey PRIMARY KEY (model_id, capability_key);

CREATE INDEX idx_model_capabilities_capability_key ON public.model_capabilities USING btree (capability_key);

ALTER TABLE ONLY public.model_capabilities
    ADD CONSTRAINT model_capabilities_capability_key_fkey FOREIGN KEY (capability_key) REFERENCES public.capability_keys(key) ON UPDATE CASCADE ON DELETE RESTRICT;

ALTER TABLE ONLY public.model_capabilities
    ADD CONSTRAINT model_capabilities_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000030_model_capabilities_drop_source]
-- 阶段 14 Q4：能力声明去 source。
-- 同步不再写运行时能力表（改写目录），source（models_dev/manual/adapter_seed）已无意义。
-- [000047_create_capability_keys]
-- 能力 key 字典表（DEC-024 / DESIGN-capability-manual-declaration §4）：合法能力 key 的唯一真源，
-- 取代代码内 keys.go 常量注册表。新增能力 = 往本表插一行（带中文描述供运维区分），无需改代码。
