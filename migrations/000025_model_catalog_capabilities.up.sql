-- Model catalog 是 models.dev 的独立参考目录（菜单），运行时永不读取（阶段 14 解耦）。
-- 同步只刷新本表与 model_catalog_capabilities，不再写运行时 models 表。
CREATE TABLE public.model_catalog_capabilities (
    -- canonical_id: models.dev 规范模型标识（lab/model，如 openai/gpt-4o），主键。--
    canonical_id text NOT NULL,
    capability_key text NOT NULL,
    support_level text NOT NULL,
    limits jsonb,
    CONSTRAINT model_catalog_capabilities_capability_key_check CHECK ((capability_key <> ''::text)),
    CONSTRAINT model_catalog_capabilities_support_level_check CHECK ((support_level = ANY (ARRAY['full'::text, 'limited'::text, 'unsupported'::text])))
);

ALTER TABLE ONLY public.model_catalog_capabilities
    ADD CONSTRAINT model_catalog_capabilities_pkey PRIMARY KEY (canonical_id, capability_key);

ALTER TABLE ONLY public.model_catalog_capabilities
    ADD CONSTRAINT model_catalog_capabilities_canonical_id_fkey FOREIGN KEY (canonical_id) REFERENCES public.model_catalog(canonical_id) ON DELETE CASCADE;
