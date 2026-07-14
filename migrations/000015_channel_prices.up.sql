-- btree_gist 让 exclusion constraint 可以同时比较 BIGINT/TEXT 等值和时间范围重叠（000012 已建，幂等保证）。
-- Channel price 是某条 channel 服务某个 Unio model 时的「售价 + 成本价」合并配置（阶段 15）。
-- 一行同时含客户售价（必填）与上游成本价（可空），毛利在录入期即被守卫保证非负。
-- 退役 prices（模型级售价）与 channel_cost_prices（渠道级成本价），计费一律走渠道-模型级。
CREATE SEQUENCE public.channel_prices_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_prices (
    -- id: 主键。--
    id bigint NOT NULL,
    -- channel_id: 价格适用的 channel ID。--
    channel_id bigint NOT NULL,
    -- model_id: 价格适用的 Unio 模型 ID。--
    model_id bigint NOT NULL,
    -- currency: 计价币种。--
    currency text NOT NULL,
    -- pricing_unit: 计价单位。--
    pricing_unit text NOT NULL,
    uncached_input_cost numeric(20,10),
    cache_read_input_cost numeric(20,10),
    cache_write_5m_input_cost numeric(20,10),
    cache_write_1h_input_cost numeric(20,10),
    output_cost numeric(20,10),
    reasoning_output_cost numeric(20,10),
    status text NOT NULL,
    effective_from timestamp with time zone NOT NULL,
    effective_to timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    cache_write_30m_input_cost numeric(20,10),
    CONSTRAINT channel_prices_cache_read_input_cost_check CHECK (((cache_read_input_cost IS NULL) OR (cache_read_input_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_cache_write_1h_input_cost_check CHECK (((cache_write_1h_input_cost IS NULL) OR (cache_write_1h_input_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_cache_write_30m_input_cost_check CHECK (((cache_write_30m_input_cost IS NULL) OR (cache_write_30m_input_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_cache_write_5m_input_cost_check CHECK (((cache_write_5m_input_cost IS NULL) OR (cache_write_5m_input_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT channel_prices_output_cost_check CHECK (((output_cost IS NULL) OR (output_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT channel_prices_reasoning_output_cost_check CHECK (((reasoning_output_cost IS NULL) OR (reasoning_output_cost >= (0)::numeric))),
    CONSTRAINT channel_prices_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT channel_prices_uncached_input_cost_check CHECK (((uncached_input_cost IS NULL) OR (uncached_input_cost >= (0)::numeric))),
    CONSTRAINT ck_channel_prices_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from)))
);

ALTER SEQUENCE public.channel_prices_id_seq OWNED BY public.channel_prices.id;

ALTER TABLE ONLY public.channel_prices ALTER COLUMN id SET DEFAULT nextval('public.channel_prices_id_seq'::regclass);

ALTER TABLE ONLY public.channel_prices
    ADD CONSTRAINT channel_prices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channel_prices
    ADD CONSTRAINT ex_channel_prices_enabled_window EXCLUDE USING gist (channel_id WITH =, model_id WITH =, currency WITH =, pricing_unit WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

ALTER TABLE ONLY public.channel_prices
    ADD CONSTRAINT uq_channel_prices_id_channel_model UNIQUE (id, channel_id, model_id);

CREATE INDEX idx_channel_prices_channel_model_status_effective ON public.channel_prices USING btree (channel_id, model_id, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.channel_prices
    ADD CONSTRAINT fk_channel_prices_channel_model FOREIGN KEY (channel_id, model_id) REFERENCES public.channel_models(channel_id, model_id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000056_drop_channel_prices_sale_columns]
-- DEC-026：客户售价 = 模型基准价（model_prices）× 线路倍率（routes.price_ratio），
-- 渠道侧只承载「成本」。channel_prices 的售价列与录入毛利守卫自此退役（售价快照取 model_prices×ratio）。
-- 先删依赖售价列的毛利 CHECK，再删 6 个售价列；成本列保留。
-- [000075_add_cache_write_30m]
-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。
--
-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
