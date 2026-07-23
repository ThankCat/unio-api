-- ProviderEndpoint 是某个 Provider 下的「一个 API Root = 一个上游公共故障域」。
-- Provider 仍表示供应商/记账主体（不持有 base_url）；base_url 唯一归属 ProviderEndpoint，
-- 公共故障熔断按 Endpoint 执行；Channel 通过 provider_endpoint_id 引用本表。
CREATE SEQUENCE public.provider_endpoints_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.provider_endpoints (
    -- id: 主键。--
    id bigint NOT NULL,
    -- provider_id: 归属 Provider ID（供应商/记账主体）。--
    provider_id bigint NOT NULL,
    -- name: Provider 内 Endpoint 展示名称。--
    name text NOT NULL,
    -- base_url: 规范化后的唯一上游 API Root（adapter 从此派生 operation 路径）。--
    base_url text NOT NULL,
    -- base_url_revision: 规范化 base_url 的单调地址版本，仅在 base_url 真变化时同事务 +1。--
    base_url_revision bigint DEFAULT 1 NOT NULL,
    -- status: Endpoint 启停状态。--
    status text NOT NULL,
    -- status_revision: Endpoint 有效状态的单调版本，按 P4-D06 在自身或父 Provider 有效状态真变化时 +1。--
    status_revision bigint DEFAULT 1 NOT NULL,
    -- archived_at: 归档时间；与 status='archived' 一致。--
    archived_at timestamp with time zone,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_endpoints_base_url_check CHECK ((base_url <> ''::text)),
    CONSTRAINT provider_endpoints_base_url_scheme_check CHECK ((base_url ~* '^https?://'::text)),
    CONSTRAINT provider_endpoints_base_url_revision_check CHECK ((base_url_revision >= 1)),
    CONSTRAINT provider_endpoints_status_revision_check CHECK ((status_revision >= 1)),
    CONSTRAINT provider_endpoints_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'archived'::text]))),
    CONSTRAINT ck_provider_endpoints_archived_at CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL)))
);

ALTER SEQUENCE public.provider_endpoints_id_seq OWNED BY public.provider_endpoints.id;

ALTER TABLE ONLY public.provider_endpoints ALTER COLUMN id SET DEFAULT nextval('public.provider_endpoints_id_seq'::regclass);

ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT provider_endpoints_pkey PRIMARY KEY (id);

-- 规范化后的 base_url 全局唯一：避免同一故障域被拆成多个 Endpoint 绕过全局熔断。
ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT provider_endpoints_base_url_key UNIQUE (base_url);

-- (provider_id, name) 唯一。
ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT provider_endpoints_provider_id_name_key UNIQUE (provider_id, name);

-- (id, provider_id) 唯一：供 channels 复合外键保证 Provider/Endpoint 归属一致。
ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT uq_provider_endpoints_id_provider UNIQUE (id, provider_id);

CREATE INDEX idx_provider_endpoints_provider_id ON public.provider_endpoints USING btree (provider_id);

CREATE INDEX idx_provider_endpoints_status ON public.provider_endpoints USING btree (status);

ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT provider_endpoints_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);
