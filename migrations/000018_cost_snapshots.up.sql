-- Cost snapshot 是一次请求结算时使用的 provider/channel 成本价副本和实际成本事实。
CREATE SEQUENCE public.cost_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.cost_snapshots (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_record_id: 对应的请求记录 ID，一次请求只能有一条成本快照。--
    request_record_id bigint NOT NULL,
    -- cost_price_id: 结算时命中的 channel 成本价配置 ID。--
    cost_price_id bigint,
    -- provider_id: 本次请求最终使用的 provider ID。--
    provider_id bigint NOT NULL,
    -- channel_id: 本次请求最终使用的 channel ID。--
    channel_id bigint NOT NULL,
    -- model_id: 本次请求使用的 Unio 模型 ID。--
    model_id bigint NOT NULL,
    -- upstream_model: 本次请求转发给上游的模型名。--
    upstream_model text NOT NULL,
    -- currency: 成本币种。--
    currency text NOT NULL,
    -- pricing_unit: 成本计价单位。--
    pricing_unit text NOT NULL,
    -- uncached_input_cost: 快照中的未缓存输入 token 成本价。--
    uncached_input_cost numeric(20,10) NOT NULL,
    -- cache_read_input_cost: 快照中的缓存读取输入 token 成本价。--
    cache_read_input_cost numeric(20,10),
    cache_write_5m_input_cost numeric(20,10),
    cache_write_1h_input_cost numeric(20,10),
    output_cost numeric(20,10) NOT NULL,
    reasoning_output_cost numeric(20,10),
    uncached_input_cost_amount numeric(20,10) NOT NULL,
    cache_read_input_cost_amount numeric(20,10) NOT NULL,
    cache_write_5m_input_cost_amount numeric(20,10) NOT NULL,
    cache_write_1h_input_cost_amount numeric(20,10) NOT NULL,
    output_cost_amount numeric(20,10) NOT NULL,
    reasoning_output_cost_amount numeric(20,10) NOT NULL,
    total_cost_amount numeric(20,10) NOT NULL,
    formula_version text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    cache_write_30m_input_cost numeric(20,10),
    cache_write_30m_input_cost_amount numeric(20,10) NOT NULL,
    model_reference_cost_id bigint,
    channel_cost_multiplier_id bigint,
    cost_multiplier numeric(20,10),
    channel_recharge_factor_id bigint,
    recharge_factor numeric(20,10),
    CONSTRAINT ck_cost_snapshots_total_amount CHECK ((total_cost_amount = ((((((uncached_input_cost_amount + cache_read_input_cost_amount) + cache_write_5m_input_cost_amount) + cache_write_1h_input_cost_amount) + cache_write_30m_input_cost_amount) + output_cost_amount) + reasoning_output_cost_amount))),
    CONSTRAINT cost_snapshots_cache_read_input_cost_amount_check CHECK ((cache_read_input_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_cache_read_input_cost_check CHECK (((cache_read_input_cost IS NULL) OR (cache_read_input_cost >= (0)::numeric))),
    CONSTRAINT cost_snapshots_cache_write_1h_input_cost_amount_check CHECK ((cache_write_1h_input_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_cache_write_1h_input_cost_check CHECK (((cache_write_1h_input_cost IS NULL) OR (cache_write_1h_input_cost >= (0)::numeric))),
    CONSTRAINT cost_snapshots_cache_write_30m_input_cost_amount_check CHECK ((cache_write_30m_input_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_cache_write_30m_input_cost_check CHECK (((cache_write_30m_input_cost IS NULL) OR (cache_write_30m_input_cost >= (0)::numeric))),
    CONSTRAINT cost_snapshots_cache_write_5m_input_cost_amount_check CHECK ((cache_write_5m_input_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_cache_write_5m_input_cost_check CHECK (((cache_write_5m_input_cost IS NULL) OR (cache_write_5m_input_cost >= (0)::numeric))),
    CONSTRAINT cost_snapshots_cost_multiplier_check CHECK (((cost_multiplier IS NULL) OR (cost_multiplier >= (0)::numeric))),
    CONSTRAINT cost_snapshots_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT cost_snapshots_formula_version_check CHECK ((formula_version <> ''::text)),
    CONSTRAINT cost_snapshots_output_cost_amount_check CHECK ((output_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_output_cost_check CHECK ((output_cost >= (0)::numeric)),
    CONSTRAINT cost_snapshots_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT cost_snapshots_reasoning_output_cost_amount_check CHECK ((reasoning_output_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_reasoning_output_cost_check CHECK (((reasoning_output_cost IS NULL) OR (reasoning_output_cost >= (0)::numeric))),
    CONSTRAINT cost_snapshots_recharge_factor_check CHECK (((recharge_factor IS NULL) OR (recharge_factor >= (0)::numeric))),
    CONSTRAINT cost_snapshots_total_cost_amount_check CHECK ((total_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_uncached_input_cost_amount_check CHECK ((uncached_input_cost_amount >= (0)::numeric)),
    CONSTRAINT cost_snapshots_uncached_input_cost_check CHECK ((uncached_input_cost >= (0)::numeric)),
    CONSTRAINT cost_snapshots_upstream_model_check CHECK ((upstream_model <> ''::text))
);

ALTER SEQUENCE public.cost_snapshots_id_seq OWNED BY public.cost_snapshots.id;

ALTER TABLE ONLY public.cost_snapshots ALTER COLUMN id SET DEFAULT nextval('public.cost_snapshots_id_seq'::regclass);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_request_record_id_key UNIQUE (request_record_id);

CREATE INDEX idx_cost_snapshots_channel_created_at ON public.cost_snapshots USING btree (channel_id, created_at DESC, id DESC);

CREATE INDEX idx_cost_snapshots_provider_created_at ON public.cost_snapshots USING btree (provider_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT cost_snapshots_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT fk_cost_snapshots_channel_model FOREIGN KEY (channel_id, model_id) REFERENCES public.channel_models(channel_id, model_id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT fk_cost_snapshots_channel_provider FOREIGN KEY (channel_id, provider_id) REFERENCES public.channels(id, provider_id);

ALTER TABLE ONLY public.cost_snapshots
    ADD CONSTRAINT fk_cost_snapshots_cost_price_channel_model FOREIGN KEY (cost_price_id, channel_id, model_id) REFERENCES public.channel_prices(id, channel_id, model_id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000035_repoint_snapshots_to_channel_prices]
-- 阶段 15：把价格 / 成本快照与补偿任务的外键从退役的 prices / channel_cost_prices 改挂到 channel_prices。
-- 开发期库可重置，迁移在空快照表上执行；生产化前若有历史快照需另设数据迁移（详见 PLAN §12）。
--
-- price_snapshots.price_id：模型级 prices(id) -> 渠道级 channel_prices(id)。
-- [000075_add_cache_write_30m]
-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。
--
-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
-- [000079_cost_snapshots_add_multiplier_source]
-- 000079: cost_snapshots 记录成本来源（DEC-027 渠道成本倍率）。
--
-- 倍率路径下没有 channel_prices 行，成本由「参考成本 × 价格倍率 × 充值倍率」算出并冻结进本表金额列；
-- 为可审计复算，额外快照来源行 id 与倍率标量。覆盖路径（channel_prices 绝对成本）仍走 cost_price_id，
-- 此时新增来源列为 NULL。故 cost_price_id 放开 NOT NULL：倍率路径写 NULL（MATCH SIMPLE 下复合 FK 自动豁免）。
--
-- 1) 放开 cost_price_id NOT NULL（倍率路径无 channel_prices 行可指）。复合 FK 保留不变。
