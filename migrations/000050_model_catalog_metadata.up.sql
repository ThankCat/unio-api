-- models.dev 元数据全量采纳：目录补齐上游可用字段，运营表快照展示字段。
--
-- 此前同步只取名称/系列/日期/上下文/输入输出参考价，把 description（全量覆盖）、
-- 知识截止、缓存参考价、输入上限（长上下文阈值的数据来源）等高价值字段全部丢掉了。
-- 这些字段进目录后随同步刷新；console/admin 的展示字段按「运行时永不读目录」原则
-- 在采纳/追更时快照进 models。
ALTER TABLE public.model_catalog
    ADD COLUMN description text DEFAULT ''::text NOT NULL,
    ADD COLUMN knowledge_cutoff text DEFAULT ''::text NOT NULL,
    ADD COLUMN cache_read_price_usd_per_million_tokens numeric(20,10),
    ADD COLUMN cache_write_price_usd_per_million_tokens numeric(20,10),
    ADD COLUMN input_limit_tokens bigint,
    ADD COLUMN open_weights boolean,
    ADD COLUMN modalities_input text[] DEFAULT '{}'::text[] NOT NULL,
    ADD COLUMN modalities_output text[] DEFAULT '{}'::text[] NOT NULL,
    ADD COLUMN last_updated date,
    ADD CONSTRAINT model_catalog_cache_read_price_check
        CHECK ((cache_read_price_usd_per_million_tokens IS NULL) OR (cache_read_price_usd_per_million_tokens >= (0)::numeric)),
    ADD CONSTRAINT model_catalog_cache_write_price_check
        CHECK ((cache_write_price_usd_per_million_tokens IS NULL) OR (cache_write_price_usd_per_million_tokens >= (0)::numeric)),
    ADD CONSTRAINT model_catalog_input_limit_tokens_check
        CHECK ((input_limit_tokens IS NULL) OR (input_limit_tokens > 0));

COMMENT ON COLUMN public.model_catalog.description IS '上游一句话模型简介，仅展示。';
COMMENT ON COLUMN public.model_catalog.knowledge_cutoff IS '知识截止（上游格式不齐：可能是 2024-09-30 也可能是 2024-08），原样保存，空串表示上游未给。';
COMMENT ON COLUMN public.model_catalog.cache_read_price_usd_per_million_tokens IS '缓存读参考价基线（USD/百万 token），仅展示，绝不用于计费。';
COMMENT ON COLUMN public.model_catalog.cache_write_price_usd_per_million_tokens IS '缓存写参考价基线（USD/百万 token），仅展示，绝不用于计费。';
COMMENT ON COLUMN public.model_catalog.input_limit_tokens IS '单请求输入 token 上限（上游 limit.input），是长上下文阶梯阈值的参考来源。';
COMMENT ON COLUMN public.model_catalog.open_weights IS '是否开源权重；NULL 表示上游未标注。';
COMMENT ON COLUMN public.model_catalog.modalities_input IS '上游原始输入模态（text/image/audio/video/pdf…），能力声明之外保留原文供展示。';
COMMENT ON COLUMN public.model_catalog.modalities_output IS '上游原始输出模态。';
COMMENT ON COLUMN public.model_catalog.last_updated IS '上游条目最近更新日期。';

-- 运营表快照列：采纳时从目录复制，追更刷新可覆盖，管理员可改。
ALTER TABLE public.models
    ADD COLUMN description text DEFAULT ''::text NOT NULL,
    ADD COLUMN knowledge_cutoff text DEFAULT ''::text NOT NULL;

COMMENT ON COLUMN public.models.description IS '模型一句话简介（采纳时快照自目录，可编辑），console 模型页展示用。';
COMMENT ON COLUMN public.models.knowledge_cutoff IS '知识截止（采纳时快照自目录，可编辑），空串表示未知。';

-- 图标抓取从 /logos/{slug}.svg（provider 路径，大量 lab 只有统一占位图）切换到
-- /logos/labs/{slug}.svg（lab 专属真图标）。清空同步时间让下次目录同步全量重抓。
UPDATE public.model_labs SET logo_synced_at = NULL;
