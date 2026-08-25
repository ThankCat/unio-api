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
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- status: 账号状态：active 正常，disabled 停用后不能登录也不能调用。--
    status text DEFAULT 'active'::text NOT NULL,
    -- uid: 对外公开的用户标识；自增 id 不外泄，避免暴露注册规模与顺序。--
    uid uuid NOT NULL,
    -- rpm_limit: 每分钟请求上限；NULL 继承全局默认，0 表示不限。--
    rpm_limit integer,
    -- rpd_limit: 每天请求上限；NULL 继承全局默认，0 表示不限。--
    rpd_limit integer,
    -- concurrency_limit: 同时进行中的请求上限；NULL 继承全局默认，0 表示不限。--
    concurrency_limit integer,
    CONSTRAINT ck_users_concurrency_limit_non_negative CHECK (((concurrency_limit IS NULL) OR (concurrency_limit >= 0))),
    CONSTRAINT ck_users_rpd_limit_non_negative CHECK (((rpd_limit IS NULL) OR (rpd_limit >= 0))),
    CONSTRAINT ck_users_rpm_limit_non_negative CHECK (((rpm_limit IS NULL) OR (rpm_limit >= 0))),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_uid_key UNIQUE (uid);

CREATE UNIQUE INDEX idx_users_email_lower ON public.users USING btree (lower(email));

COMMENT ON COLUMN public.users.rpm_limit IS '用户级每分钟请求上限；NULL 继承全局默认，0 表示不限。同一用户的多把 Key 共享该配额。';

-- ---------------------------------------------------------------------------
-- 限流三维（rpm/rpd/concurrency）挂在用户上而不是 API Key 上：同一用户的多把
-- Key 共享同一份配额，否则客户多建几把 Key 就能绕过限流。
--
-- 三者都区分 NULL 与 0：NULL 表示跟随全局默认（会随系统设置变动），0 表示明确
-- 不限（不随全局变动）。合并成一个值就无法表达「这个用户确定不限，别跟着全局改」。
-- ---------------------------------------------------------------------------
