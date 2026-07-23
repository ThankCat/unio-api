-- Model catalog link 记录「运营模型 ← 采纳来源目录条目」的关联与更新提醒状态（阶段 14）。
-- 采纳是一次快照拷贝；目录之后变化不自动回灌，靠指纹对比检测「有更新」并提醒。
CREATE TABLE public.model_catalog_links (
    -- model_id: 关联的运营模型 ID；一个模型至多关联一条目录条目，模型删除时级联清理。--
    model_id bigint NOT NULL,
    -- canonical_id: 采纳来源目录条目；非唯一 → 一条目录可派生多个模型（Q2）。--
    canonical_id text NOT NULL,
    -- adopted_fingerprint: 采纳/上次刷新时目录条目的 fingerprint（快照基线）。--
    adopted_fingerprint text NOT NULL,
    -- reminder_muted: 永久忽略更新（true 表示不再提醒，可取消静音）。--
    reminder_muted boolean DEFAULT false NOT NULL,
    -- reminder_snooze_until: 稍后提醒：此时间之前不提醒，空表示未稍后。--
    reminder_snooze_until timestamp with time zone,
    -- dismissed_fingerprint: 忽略本次更新所对应的目录 fingerprint；目录再变到新指纹会重新提醒。--
    dismissed_fingerprint text,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_catalog_links_adopted_fingerprint_check CHECK ((adopted_fingerprint <> ''::text))
);

ALTER TABLE ONLY public.model_catalog_links
    ADD CONSTRAINT model_catalog_links_pkey PRIMARY KEY (model_id);

CREATE INDEX idx_model_catalog_links_canonical ON public.model_catalog_links USING btree (canonical_id);

ALTER TABLE ONLY public.model_catalog_links
    ADD CONSTRAINT model_catalog_links_canonical_id_fkey FOREIGN KEY (canonical_id) REFERENCES public.model_catalog(canonical_id);

ALTER TABLE ONLY public.model_catalog_links
    ADD CONSTRAINT model_catalog_links_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id) ON DELETE CASCADE;
