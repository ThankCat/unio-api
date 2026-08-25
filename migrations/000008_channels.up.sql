-- Channel 是某个 provider 下的一条具体上游渠道。
CREATE SEQUENCE public.channels_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channels (
    -- id: 主键。--
    id bigint NOT NULL,
    -- provider_id: channel 所属 provider ID（供应商/记账主体）。--
    provider_id bigint NOT NULL,
    -- name: provider 内 channel 名称。--
    name text NOT NULL,
    -- adapter_key: channel 运行时绑定的 adapter 注册键，routing 据此解析具体 adapter，不再从 provider 派生。--
    adapter_key text NOT NULL,
    -- credential: 上游 API key，明文存储，便于管理端查看/复制/编辑（产品决策：渠道凭据不加密）。--
    credential text NOT NULL,
    -- config_revision: PostgreSQL 权威单调配置版本；协议、凭据、超时、状态等配置真变化时同事务 +1。--
    config_revision bigint DEFAULT 1 NOT NULL,
    -- capacity_revision: 渠道并发容量真变化时 +1，不复用 config_revision。--
    capacity_revision bigint DEFAULT 1 NOT NULL,
    -- status: channel 启停状态。--
    status text NOT NULL,
    -- priority: routing 选择 channel 时的优先级，数值越小越靠前。--
    priority integer NOT NULL,
    -- sticky_enabled: NULL 继承全局设置；true 使用渠道 TTL；false 禁用。--
    sticky_enabled boolean,
    -- sticky_ttl_ms: 渠道 Sticky TTL，仅 sticky_enabled=true 时必填。--
    sticky_ttl_ms bigint,
    -- response_timeout_ms: NULL 继承全局默认；非流式限制完整响应，流式限制收到上游响应头。--
    response_timeout_ms integer,
    -- first_token_timeout_ms: NULL 继承全局默认；限制流式首个有效生成 Token。--
    first_token_timeout_ms integer,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_tested_at timestamp with time zone,
    last_test_ok boolean,
    last_test_latency_ms integer,
    last_test_error text,
    credential_valid boolean DEFAULT true NOT NULL,
    archived_at timestamp with time zone,
    concurrency_limit integer,
    -- supports_openai_fast: 是否支持 OpenAI Fast 档；默认关闭，只有确认支持且账单契约可信的渠道才开。--
    supports_openai_fast boolean DEFAULT false NOT NULL,
    -- protocols: 本渠道可服务的入口协议族集合；与 adapter_key 组合后必须在代码 adapter 注册表中存在。--
    protocols text[] NOT NULL,
    CONSTRAINT channels_capacity_revision_check CHECK ((capacity_revision >= 1)),
    CONSTRAINT channels_concurrency_limit_check CHECK (((concurrency_limit IS NULL) OR (concurrency_limit >= 0))),
    CONSTRAINT channels_config_revision_check CHECK ((config_revision >= 1)),
    CONSTRAINT channels_credential_check CHECK ((credential <> ''::text)),
    CONSTRAINT channels_first_token_timeout_ms_check CHECK (((first_token_timeout_ms IS NULL) OR (first_token_timeout_ms > 0))),
    CONSTRAINT channels_last_test_latency_ms_check CHECK (((last_test_latency_ms IS NULL) OR (last_test_latency_ms >= 0))),
    CONSTRAINT channels_priority_check CHECK (((priority >= 0) AND (priority <= 100) AND ((priority % 10) = 0))),
    CONSTRAINT channels_response_timeout_ms_check CHECK (((response_timeout_ms IS NULL) OR (response_timeout_ms > 0))),
    CONSTRAINT channels_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'archived'::text]))),
    CONSTRAINT channels_sticky_policy_check CHECK ((((sticky_enabled IS NULL) AND (sticky_ttl_ms IS NULL)) OR ((sticky_enabled = false) AND (sticky_ttl_ms IS NULL)) OR ((sticky_enabled = true) AND (sticky_ttl_ms > 0)))),
    CONSTRAINT ck_channels_archived_at CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL))),
    CONSTRAINT ck_channels_protocols_known CHECK ((protocols <@ ARRAY['openai'::text, 'anthropic'::text])),
    CONSTRAINT ck_channels_protocols_non_empty CHECK ((cardinality(protocols) > 0))
);

ALTER SEQUENCE public.channels_id_seq OWNED BY public.channels.id;

ALTER TABLE ONLY public.channels ALTER COLUMN id SET DEFAULT nextval('public.channels_id_seq'::regclass);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_provider_id_name_key UNIQUE (provider_id, name);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT uq_channels_id_provider UNIQUE (id, provider_id);

COMMENT ON COLUMN public.channels.protocols IS '本渠道可服务的入口协议族集合；与 adapter_key 组合后必须在代码 adapter 注册表中存在。';

COMMENT ON COLUMN public.channels.sticky_enabled IS
    'NULL=inherit gateway.routing_sticky; true=enabled with channel TTL; false=disabled';
COMMENT ON COLUMN public.channels.sticky_ttl_ms IS
    'Channel sticky TTL in milliseconds; required only when sticky_enabled=true';

CREATE INDEX idx_channels_credential_invalid ON public.channels USING btree (id) WHERE (credential_valid = false);

CREATE INDEX idx_channels_priority ON public.channels USING btree (priority, id);

CREATE INDEX idx_channels_provider_id ON public.channels USING btree (provider_id);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000060_add_channels_test_result]
-- 为 channel 增加「最近一次主动检测结果」四列（渠道检测 / 一键测渠道，阶段一）。
-- 主动检测 = 用 Provider origin + 渠道凭据，挑一个绑定模型发一个最小 "hi" 请求，
-- 验证「连得上 + 凭据有效 + 模型可用」，记录延迟与可读失败原因。与被动熔断/cooldown 正交。
-- 四列均可空：从未检测过时全为 NULL；仅由检测上游源站写入，不参与路由/计费，不改渠道启停状态。
-- [000062_add_channels_credential_valid]
-- 为 channel 增加「凭据是否有效」闸门列（阶段二：真摘除 + 检测通过才恢复）。
-- credential_valid=false 表示系统判定该渠道凭据失效（连续 401 或检测判定 credential_invalid），
-- 与 status（管理员启停意图）正交：即使 status='enabled'，credential_valid=false 也不参与路由候选。
-- 翻失效/翻有效的「何时/为何/每次检测结果」历史记入 channel_test_logs（000063），此列只存当前布尔。
-- [000066_add_archived_status]
-- 实体归档生命周期：providers / channels / routes 三表 status 增第三态 archived，
-- 并加 archived_at 时间列 + 一致性不变量（archived_at 有值 ⟺ status='archived'）。
-- 归档 = 只改状态、不删数据、完全可逆；路由候选已按 status='enabled' 过滤，archived 天然被排除。
--
-- providers
-- [000072_add_channels_concurrency_limit]
-- 渠道在途并发上限（DEC-029）：同一渠道「同时进行中」的上游调用数上限（in-flight，含整段流式传输）。
-- NULL 表示「继承并发默认」（gateway.concurrency_defaults.channel_limit），0 表示「显式不限」，>0 表示具体上限。
-- 命中上限时该候选被跳过（fallback 到下一渠道），不产生上游调用，也不写 attempt 记录。
-- [supports_openai_fast]
-- OpenAI Fast 是渠道级显式能力：默认关闭，只有确认支持且账单契约可信的 OpenAI 渠道才开启。
-- 关闭时该渠道不会被选为 Fast 档候选，避免把加价档位路由到并不真正提速的上游。
-- [protocols]
-- protocols 决定这条渠道能接哪些入口协议族，adapter_key 决定用哪套上游方言，二者正交：
-- (协议, adapter_key) 的每个组合都必须在代码注册表里存在，由 admin 写入路径校验。
-- DB 只约束协议取值与非空，不校验组合——注册表是代码事实，DB 无法及时跟随。
-- 一把上游凭据往往能同时以 OpenAI 与 Anthropic 形态服务，所以是数组而非单值。
-- Provider 与 Channel 运行态围栏：
-- 单故障域改造：地址、公共故障域与双 revision 唯一归属 Provider；
-- 新增单调 config_revision（配置/凭据状态真变化 +1）与独立 capacity_revision（并发容量真变化 +1）。
-- Migration renumbered after merging Provider Origin into Provider.
