-- 订阅账号实体（docs/changes/2026-09-02-account-pool 批次一）。
--
-- 账号是池型渠道之下的供给单元，恰好归属一个渠道（1:N），不跨渠道复用。它把订阅席位建模成
-- 有状态资源：凭据、容量、健康、用量窗口各自独立，调度按账号而非按渠道决定容量与健康。
--
-- 两处刻意不加约束，理由记录在列注释里：
--   * plan_type 不加 CHECK——官方档位（free/go/plus/pro/business(team)/enterprise/edu）会增改；
--   * usage_snapshot 内的窗口允许缺失——Business Premium 无 5h 窗口、Enterprise/Edu 弹性额度无固定限。

CREATE SEQUENCE public.subscription_accounts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.subscription_accounts (
    -- id: 主键。--
    id bigint NOT NULL,
    -- channel_id: 归属渠道；账号恰好归属一个池型渠道，不跨渠道复用。--
    channel_id bigint NOT NULL,
    -- platform: 上游平台，决定 OAuth 实现与 wire。当前仅实现 openai（Codex 订阅）。--
    platform text NOT NULL,
    -- credential_type: 凭据类型。当前仅 oauth；预留后续类型时同步放宽 CHECK。--
    credential_type text NOT NULL,
    -- upstream_account_id: 上游账号标识（Codex 为 chatgpt_account_id）。与 platform 组成全局唯一键，
    -- 用于拒绝重复导入；换池需先归档原账号，不允许同一个号同时存在于两个池。--
    upstream_account_id text NOT NULL,
    -- display_name: 运营可读名（邮箱或备注）。不参与唯一性判断，可重名。--
    display_name text NOT NULL,
    -- plan_type: 订阅档位（free/go/plus/pro/business(team)/enterprise/edu 等）。
    -- 官方档位会增改，故不加 CHECK 约束，由应用层做白名单软校验；「同套餐一池」按字符串比对。--
    plan_type text,
    -- credentials: 凭据文档（access_token / refresh_token / expires_at / client_id 等）。
    -- 明文存储，与渠道凭据同口径（产品决策：凭据不加密，便于管理端查看与轮换）。--
    credentials jsonb NOT NULL,
    -- proxy_url: 账号绑定的出口代理；NULL 表示直连。导入换码、令牌刷新、正式请求三条路径共用，
    -- 保证同一个号始终从同一出口访问上游（风控一致性）。--
    proxy_url text,
    -- concurrency_limit: 账号并发上限。NULL 继承渠道 account_default_concurrency，0 不限，正数为硬上限。--
    concurrency_limit integer,
    -- priority: 池内选号优先级，数值越小越靠前；同档位由调度层随机打散，不按 ID 决胜。--
    priority integer DEFAULT 50 NOT NULL,
    -- status: 账号自身启停。绑定式语义：可调度性 = 账号 ∧ 渠道 ∧ 服务商 三层同时 enabled，
    -- 本列不表达父级状态，父级遮蔽由管理页显式标注。--
    status text NOT NULL,
    -- disabled_reason: 停用原因（受控值）。manual=管理员停用；token_revoked=令牌确认吊销；
    -- risk_control=上游风控封禁。仅 status=disabled 时有意义。--
    disabled_reason text,
    -- subscription_expires_at: 订阅到期时间（导入时采集）。与令牌过期是两回事，用于到期预警。--
    subscription_expires_at timestamp with time zone,
    -- usage_snapshot: 用量窗口快照 {primary:{used_percent,window_minutes,reset_at},secondary:{...},
    -- captured_at}。primary=5h、secondary=7d（实测口径，勿反）；任一窗口都可能缺失，消费方须容忍空值。--
    usage_snapshot jsonb,
    -- last_success_at: 最近一次完整成功时间，供 LRU 选号使用。--
    last_success_at timestamp with time zone,
    -- config_revision: 账号配置单调版本；并发、优先级、代理、状态真变化时 +1，
    -- 同事务提升所属渠道 capacity_revision，使运行态围栏立即感知（配置热更新传播）。--
    config_revision bigint DEFAULT 1 NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE public.subscription_accounts
    ALTER COLUMN id SET DEFAULT nextval('public.subscription_accounts_id_seq');

ALTER SEQUENCE public.subscription_accounts_id_seq OWNED BY public.subscription_accounts.id;

ALTER TABLE ONLY public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_channel_id_fkey
        FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE public.subscription_accounts
    ADD CONSTRAINT subscription_accounts_credential_type_check
        CHECK (credential_type IN ('oauth')),
    ADD CONSTRAINT subscription_accounts_status_check
        CHECK (status IN ('enabled', 'disabled', 'archived')),
    ADD CONSTRAINT subscription_accounts_disabled_reason_check
        CHECK (disabled_reason IS NULL OR disabled_reason IN ('manual', 'token_revoked', 'risk_control')),
    ADD CONSTRAINT subscription_accounts_concurrency_limit_check
        CHECK (concurrency_limit IS NULL OR concurrency_limit >= 0),
    ADD CONSTRAINT subscription_accounts_priority_check
        CHECK (priority >= 0),
    ADD CONSTRAINT subscription_accounts_config_revision_check
        CHECK (config_revision >= 1);

-- 同一平台下上游账号唯一：重复导入直接被数据库拒绝，不依赖应用层先查后插（并发下不可靠）。
CREATE UNIQUE INDEX subscription_accounts_platform_upstream_id_key
    ON public.subscription_accounts (platform, upstream_account_id);

-- 候选快照按渠道取可调度账号，这是每请求热路径。
CREATE INDEX subscription_accounts_channel_status_idx
    ON public.subscription_accounts (channel_id, status);

-- 到期预警扫描：只关心尚未归档的账号。
CREATE INDEX subscription_accounts_expires_idx
    ON public.subscription_accounts (subscription_expires_at)
    WHERE status <> 'archived';
