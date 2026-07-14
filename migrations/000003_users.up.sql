-- 用户账号主体，承载登录身份和用户归属边界。
CREATE SEQUENCE public.users_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.users (
    -- id: 主键。--
    id bigint NOT NULL,
    -- email: 用户登录邮箱。--
    email text NOT NULL,
    -- password_hash: 用户密码哈希。--
    password_hash text NOT NULL,
    -- display_name: 用户展示名称。--
    display_name text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX idx_users_email_lower ON public.users USING btree (lower(email));
