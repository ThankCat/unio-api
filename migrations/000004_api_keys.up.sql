-- API Key 是客户调用 /v1/* 的 opaque 凭证。
CREATE SEQUENCE public.api_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.api_keys (
    -- id: 主键。--
    id bigint NOT NULL,
    -- name: 用户侧 API Key 名称。--
    name text NOT NULL,
    -- key_prefix: API Key 明文前缀，用于定位和展示。--
    key_prefix text NOT NULL,
    -- key_hash: API Key 哈希值，认证按它定位（不参与明文展示）。--
    key_hash text NOT NULL,
    -- key_plaintext: 完整明文 key，供用户在控制台多次复制查看（产品决策：用户 key 明文留存）。NULL=历史/不可回显。--
    key_plaintext text,
    -- last_used_at: 最近一次成功认证时间。--
    last_used_at timestamp with time zone,
    -- expires_at: API Key 过期时间。--
    expires_at timestamp with time zone,
    -- disabled_at: API Key 被禁用时间。--
    disabled_at timestamp with time zone,
    -- revoked_at: API Key 被吊销时间。--
    revoked_at timestamp with time zone,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    spend_limit numeric(20,10),
    spent_total numeric(20,10) DEFAULT 0 NOT NULL,
    route_id bigint NOT NULL,
    rpm_limit integer,
    tpm_limit integer,
    rpd_limit integer,
    user_id bigint NOT NULL,
    CONSTRAINT api_keys_rpd_limit_check CHECK (((rpd_limit IS NULL) OR (rpd_limit >= 0))),
    CONSTRAINT api_keys_rpm_limit_check CHECK (((rpm_limit IS NULL) OR (rpm_limit >= 0))),
    CONSTRAINT api_keys_spend_limit_check CHECK (((spend_limit IS NULL) OR (spend_limit >= (0)::numeric))),
    CONSTRAINT api_keys_spent_total_check CHECK ((spent_total >= (0)::numeric)),
    CONSTRAINT api_keys_tpm_limit_check CHECK (((tpm_limit IS NULL) OR (tpm_limit >= 0)))
);

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_key_hash_key UNIQUE (key_hash);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

CREATE INDEX idx_api_keys_key_prefix ON public.api_keys USING btree (key_prefix);

CREATE INDEX idx_api_keys_route_id ON public.api_keys USING btree (route_id) WHERE (route_id IS NOT NULL);

CREATE INDEX idx_api_keys_user_id ON public.api_keys USING btree (user_id);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_route_id_fkey FOREIGN KEY (route_id) REFERENCES public.routes(id);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000026_add_api_keys_spend_limit]
-- API Key 费用上限：生命周期累计封顶（M7）。
-- 口径同 OpenRouter：每把 Key 设一个累计花费上限，spent_total 达到 spend_limit 即停用该 Key。
-- 假设单币种部署（与计费币种一致，实践为 USD）；spent_total 在 settlement capture 时累加客户实扣金额。
-- [000034_add_route_bindings]
-- 阶段 15：API Key 与项目绑定线路。
-- 线路解析优先级：api_keys.route_id ?? projects.default_route_id ?? 内置「经济」。
--
-- api_keys.route_id：该 Key 选定的线路，NULL 表示回落项目默认 / 内置经济。
-- [000052_add_api_keys_rate_limits]
-- 为 API Key 增加令牌级限流上限（P2-8）：RPM 每分钟请求数、TPM 每分钟 token 数、RPD 每日请求数。
-- 三列均可空：NULL 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
-- 限流计数在 Redis 滑动窗口完成，这里只持久化每把 Key 的策略上限。
-- [000058_collapse_projects_into_users]
-- 折叠 user → project → api_key 三级为 user → api_key 两级，彻底移除 projects 概念。
-- API Key、模型策略与请求归属全部直接挂在用户上。
-- 同时把线路改为 API Key 必填：彻底移除「用户/项目默认线路」回落，线路只认 api_keys.route_id。
-- 数据无需保留，但仍写正确回填，保证存量库平滑迁移。
--
-- 1. api_keys.project_id → api_keys.user_id（API Key 直接归属用户）。
