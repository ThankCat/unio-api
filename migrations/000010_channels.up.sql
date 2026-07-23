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
    -- provider_endpoint_id: channel 绑定的 ProviderEndpoint（唯一 API Root/公共故障域），base_url 由此派生。--
    provider_endpoint_id bigint NOT NULL,
    -- name: provider 内 channel 名称。--
    name text NOT NULL,
    -- protocol: channel 对外协议族，决定 ingress 路由与 adapter 协议族；routing 只命中同协议 channel。--
    protocol text NOT NULL,
    -- adapter_key: channel 运行时绑定的 adapter 注册键，routing 据此解析具体 adapter，不再从 provider 派生。--
    adapter_key text NOT NULL,
    -- credential: 上游 API key，明文存储，便于管理端查看/复制/编辑（产品决策：渠道凭据不加密）。--
    credential text NOT NULL,
    -- config_revision: PostgreSQL 权威单调配置版本；provider_endpoint_id/protocol/adapter_key/credential/credential_valid/timeout_ms/status 真变化时同事务 +1。--
    config_revision bigint DEFAULT 1 NOT NULL,
    -- admission_limits_revision: 四维限额（rpm/tpm/rpd/concurrency）有效值真变化时 +1，不复用 config_revision。--
    admission_limits_revision bigint DEFAULT 1 NOT NULL,
    -- status: channel 启停状态。--
    status text NOT NULL,
    -- priority: routing 选择 channel 时的优先级，数值越小越靠前。--
    priority integer NOT NULL,
    -- timeout_ms: 该 channel 的上游请求超时时间，空值表示使用默认值。--
    timeout_ms integer,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    rpm_limit integer,
    tpm_limit integer,
    rpd_limit integer,
    last_tested_at timestamp with time zone,
    last_test_ok boolean,
    last_test_latency_ms integer,
    last_test_error text,
    credential_valid boolean DEFAULT true NOT NULL,
    archived_at timestamp with time zone,
    concurrency_limit integer,
    upstream_bills_on_disconnect boolean DEFAULT false NOT NULL,
    CONSTRAINT channels_concurrency_limit_check CHECK (((concurrency_limit IS NULL) OR (concurrency_limit >= 0))),
    CONSTRAINT channels_config_revision_check CHECK ((config_revision >= 1)),
    CONSTRAINT channels_admission_limits_revision_check CHECK ((admission_limits_revision >= 1)),
    CONSTRAINT channels_credential_check CHECK ((credential <> ''::text)),
    CONSTRAINT channels_last_test_latency_ms_check CHECK (((last_test_latency_ms IS NULL) OR (last_test_latency_ms >= 0))),
    CONSTRAINT channels_priority_check CHECK ((priority >= 0)),
    CONSTRAINT channels_protocol_check CHECK ((protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
    CONSTRAINT channels_rpd_limit_check CHECK (((rpd_limit IS NULL) OR (rpd_limit >= 0))),
    CONSTRAINT channels_rpm_limit_check CHECK (((rpm_limit IS NULL) OR (rpm_limit >= 0))),
    CONSTRAINT channels_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text, 'archived'::text]))),
    CONSTRAINT channels_timeout_ms_check CHECK (((timeout_ms IS NULL) OR (timeout_ms > 0))),
    CONSTRAINT channels_tpm_limit_check CHECK (((tpm_limit IS NULL) OR (tpm_limit >= 0))),
    CONSTRAINT ck_channels_archived_at CHECK (((status = 'archived'::text) = (archived_at IS NOT NULL)))
);

ALTER SEQUENCE public.channels_id_seq OWNED BY public.channels.id;

ALTER TABLE ONLY public.channels ALTER COLUMN id SET DEFAULT nextval('public.channels_id_seq'::regclass);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_provider_id_name_key UNIQUE (provider_id, name);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT uq_channels_id_provider UNIQUE (id, provider_id);

CREATE INDEX idx_channels_credential_invalid ON public.channels USING btree (id) WHERE (credential_valid = false);

CREATE INDEX idx_channels_priority ON public.channels USING btree (priority, id);

CREATE INDEX idx_channels_protocol ON public.channels USING btree (protocol);

CREATE INDEX idx_channels_provider_id ON public.channels USING btree (provider_id);

CREATE INDEX idx_channels_provider_endpoint_id ON public.channels USING btree (provider_endpoint_id);

ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

-- 复合外键：保证所选 Endpoint 属于同一 Provider，避免供应商/账务/归档关系漂移。
ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_provider_endpoint_fkey FOREIGN KEY (provider_endpoint_id, provider_id) REFERENCES public.provider_endpoints(id, provider_id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000053_add_channels_rate_limits]
-- 为 channel 增加渠道级限流上限（P2-8）：RPM 每分钟请求数、TPM 每分钟 token 数、RPD 每日请求数。
-- 三列均可空：NULL 表示「继承渠道默认限流」，0 表示「显式不限」，>0 表示具体上限。
-- 渠道级限流在每次调用上游前生效，命中即跳过该候选 fallback 到下一渠道，不直接整盘失败。
-- [000060_add_channels_test_result]
-- 为 channel 增加「最近一次主动检测结果」四列（渠道检测 / 一键测渠道，阶段一）。
-- 主动检测 = 用渠道自己的 base_url + 凭据，挑一个绑定模型发一个最小 "hi" 请求，
-- 验证「连得上 + 凭据有效 + 模型可用」，记录延迟与可读失败原因。与被动熔断/cooldown 正交。
-- 四列均可空：从未检测过时全为 NULL；仅由检测端点写入，不参与路由/计费，不改渠道启停状态。
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
-- 与 RPM（每分钟请求数）正交：并发上限专门防「慢上游 + 客户端重试风暴」把长耗时请求堆在同一渠道上，
-- 每个在途请求都可能被上游计费（如 sub2api 断开仍扣费），RPM 无法限制这种堆积。
-- NULL 表示「继承并发默认」（gateway.concurrency_defaults.channel_limit），0 表示「显式不限」，>0 表示具体上限。
-- 命中上限时该候选被跳过（fallback 到下一渠道），不产生上游调用，也不写 attempt 记录。
-- [000073_add_channels_bills_on_disconnect]
-- upstream_bills_on_disconnect（DESIGN-bill-on-cancel 阶段一）：标记该渠道的上游在连接断开后
-- 仍会完成生成并计费（典型：sub2api 类订阅中转，断开不取消、drain 到底照扣）。
-- 打开后，gateway 在「请求已发出但本 attempt 不会产生真实结算成本」的失败/取消路径上，
-- 会向 channel_cost_exposures 记一条平台成本敞口（保守上界估算），供成本对账与渠道横向比较。
-- 不影响路由与客户计费，纯平台侧观测。
-- [P4 ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN §4.4]
-- 单故障域改造：删除 channels.base_url（地址唯一归属 provider_endpoints），新增必填 provider_endpoint_id
-- 与 (provider_endpoint_id, provider_id) -> provider_endpoints(id, provider_id) 复合外键；
-- 新增单调 config_revision（配置/凭据状态真变化 +1）与独立 admission_limits_revision（四维限额真变化 +1）。
