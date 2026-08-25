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
    user_id bigint NOT NULL,
    CONSTRAINT api_keys_spend_limit_check CHECK (((spend_limit IS NULL) OR (spend_limit >= (0)::numeric))),
    CONSTRAINT api_keys_spent_total_check CHECK ((spent_total >= (0)::numeric))
);

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_key_hash_key UNIQUE (key_hash);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

CREATE INDEX idx_api_keys_key_prefix ON public.api_keys USING btree (key_prefix);

CREATE INDEX idx_api_keys_user_id ON public.api_keys USING btree (user_id);

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000026_add_api_keys_spend_limit]
-- API Key 费用上限：生命周期累计封顶（M7）。
-- 口径同 OpenRouter：每把 Key 设一个累计花费上限，spent_total 达到 spend_limit 即停用该 Key。
-- 假设单币种部署（与计费币种一致，实践为 USD）；spent_total 在 settlement capture 时累加客户实扣金额。
-- [限流不在 Key 上]
-- 曾为 API Key 设过 rpm_limit / tpm_limit / rpd_limit 三列，现已移除：
-- 配额挂在用户上（users 表三列），同一用户的多把 Key 共享额度，
-- 否则客户多建几把 Key 就能绕过限流。
-- [归属层级]
-- user → api_key 两级，没有中间的项目层：API Key、请求归属都直接挂用户。
