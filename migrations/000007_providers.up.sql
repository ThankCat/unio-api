-- Provider 是业务服务商，例如 OpenAI、Anthropic，不等于 Go adapter 接口。
CREATE SEQUENCE public.providers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.providers (
    -- id: 主键。--
    id bigint NOT NULL,
    -- slug: provider 稳定业务标识。--
    slug text NOT NULL,
    -- name: provider 展示名称。--
    name text NOT NULL,
    -- status: provider 启停状态。--
    status text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT ck_providers_archived_at CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL))),
    CONSTRAINT providers_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'archived'::text])))
);

ALTER SEQUENCE public.providers_id_seq OWNED BY public.providers.id;

ALTER TABLE ONLY public.providers ALTER COLUMN id SET DEFAULT nextval('public.providers_id_seq'::regclass);

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_slug_key UNIQUE (slug);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000066_add_archived_status]
-- 实体归档生命周期：providers / channels / routes 三表 status 增第三态 archived，
-- 并加 archived_at 时间列 + 一致性不变量（archived_at 有值 ⟺ status='archived'）。
-- 归档 = 只改状态、不删数据、完全可逆；路由候选已按 status='enabled' 过滤，archived 天然被排除。
--
-- providers
