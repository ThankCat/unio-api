-- 用于验证 migration 流程已经跑通，不承载业务含义。
-- TODO(阶段2/production): [GAP-2-002] 引入正式 migration runner 和 schema 版本检查后，决定保留该开发期验证表还是迁移到专门的 schema_migrations 健康检查。
CREATE SEQUENCE public.schema_health_checks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.schema_health_checks (
    -- id: 主键。--
    id bigint NOT NULL,
    -- name: 健康检查名称。--
    name text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER SEQUENCE public.schema_health_checks_id_seq OWNED BY public.schema_health_checks.id;

ALTER TABLE ONLY public.schema_health_checks ALTER COLUMN id SET DEFAULT nextval('public.schema_health_checks_id_seq'::regclass);

ALTER TABLE ONLY public.schema_health_checks
    ADD CONSTRAINT schema_health_checks_name_key UNIQUE (name);

ALTER TABLE ONLY public.schema_health_checks
    ADD CONSTRAINT schema_health_checks_pkey PRIMARY KEY (id);
