-- 出站代理实体（对齐 sub2api 的代理一等实体模型，扩展到渠道级）。
--
-- 代理是被复用的出口资产：多个账号/渠道共用一个住宅代理时，换地址只改实体一条。
-- 出站选择回退链：账号代理 → 渠道代理 → 直连；三条账号出站路径（导入换码、令牌刷新、
-- 正式请求）与渠道出站（正式请求、检测、模型发现/验证）都经同一解析。
-- 凭据明文存储，与渠道/账号凭据同口径（边界 22：要加密应统一做，不单独开新口径）。

CREATE SEQUENCE public.proxies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.proxies (
    -- id: 主键。--
    id bigint NOT NULL DEFAULT nextval('public.proxies_id_seq'),
    -- name: 运营可读名（唯一）：如「住宅-US-01」。--
    name text NOT NULL,
    -- protocol: 代理协议。--
    protocol text NOT NULL,
    -- host: 代理主机。--
    host text NOT NULL,
    -- port: 代理端口。--
    port integer NOT NULL,
    -- username/password: 代理认证，可空；明文（与渠道凭据同口径）。--
    username text,
    password text,
    -- url: 规范化后的完整代理 URL（service 在写入时组装并做凭据转义），运行时只读本列——
    -- 出站解析（proxyclient 按 URL 缓存 http.Client）保持字符串口径，不在热路径拼 URL。--
    url text NOT NULL,
    -- status: enabled 参与出站；disabled 后引用它的账号/渠道回退直连（管理页有显式提示）。--
    status text NOT NULL DEFAULT 'enabled',
    -- note: 备注（套餐/到期/供应商等自由记录）。--
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER SEQUENCE public.proxies_id_seq OWNED BY public.proxies.id;

ALTER TABLE ONLY public.proxies
    ADD CONSTRAINT proxies_pkey PRIMARY KEY (id);

ALTER TABLE public.proxies
    ADD CONSTRAINT proxies_name_key UNIQUE (name),
    ADD CONSTRAINT proxies_protocol_check CHECK (protocol IN ('http', 'https', 'socks5')),
    ADD CONSTRAINT proxies_port_check CHECK (port > 0 AND port <= 65535),
    ADD CONSTRAINT proxies_status_check CHECK (status IN ('enabled', 'disabled')),
    ADD CONSTRAINT proxies_url_check CHECK (btrim(url) <> '');

-- 渠道级出站代理：credential 型渠道的正式请求/检测/发现走它；
-- 池型渠道作为账号级代理缺省时的回退出口。
ALTER TABLE public.channels
    -- proxy_id: 渠道出站代理；NULL 直连。RESTRICT：被引用的代理不可物理删除（先解除引用）。--
    ADD COLUMN proxy_id bigint;

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_proxy_id_fkey
        FOREIGN KEY (proxy_id) REFERENCES public.proxies(id) ON DELETE RESTRICT;

-- 账号级出站代理（实体引用）。历史列 proxy_url 保留为回退（存量数据 + sub2api 文件导入的
-- 裸 URL 兼容）；读取口径：proxy_id 实体（enabled）优先，其次 legacy proxy_url，最后渠道代理。
ALTER TABLE public.subscription_accounts
    -- proxy_id: 账号出站代理实体；NULL 时回退 legacy proxy_url，再回退渠道代理。--
    ADD COLUMN proxy_id bigint;

ALTER TABLE ONLY public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_proxy_id_fkey
        FOREIGN KEY (proxy_id) REFERENCES public.proxies(id) ON DELETE RESTRICT;

CREATE INDEX channels_proxy_id_idx ON public.channels (proxy_id) WHERE proxy_id IS NOT NULL;
CREATE INDEX subscription_accounts_proxy_id_idx ON public.subscription_accounts (proxy_id) WHERE proxy_id IS NOT NULL;
