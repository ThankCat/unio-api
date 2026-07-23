-- Price snapshot 是一次请求结算时使用的客户售价副本。
CREATE SEQUENCE public.price_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.price_snapshots (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_record_id: 对应的请求记录 ID。--
    request_record_id bigint NOT NULL,
    -- price_id: 结算时命中的价格配置 ID。--
    price_id bigint,
    -- currency: 结算币种。--
    currency text NOT NULL,
    -- pricing_unit: 结算计价单位。--
    pricing_unit text NOT NULL,
    -- uncached_input_price: 快照中的未缓存输入 token 售价。--
    uncached_input_price numeric(20,10) NOT NULL,
    -- cache_read_input_price: 快照中的缓存读取输入 token 售价。--
    cache_read_input_price numeric(20,10),
    cache_write_5m_input_price numeric(20,10),
    cache_write_1h_input_price numeric(20,10),
    output_price numeric(20,10) NOT NULL,
    reasoning_output_price numeric(20,10),
    formula_version text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    price_ratio numeric(20,10),
    cache_write_30m_input_price numeric(20,10),
    CONSTRAINT price_snapshots_cache_read_input_price_check CHECK (((cache_read_input_price IS NULL) OR (cache_read_input_price >= (0)::numeric))),
    CONSTRAINT price_snapshots_cache_write_1h_input_price_check CHECK (((cache_write_1h_input_price IS NULL) OR (cache_write_1h_input_price >= (0)::numeric))),
    CONSTRAINT price_snapshots_cache_write_30m_input_price_check CHECK (((cache_write_30m_input_price IS NULL) OR (cache_write_30m_input_price >= (0)::numeric))),
    CONSTRAINT price_snapshots_cache_write_5m_input_price_check CHECK (((cache_write_5m_input_price IS NULL) OR (cache_write_5m_input_price >= (0)::numeric))),
    CONSTRAINT price_snapshots_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT price_snapshots_formula_version_check CHECK ((formula_version <> ''::text)),
    CONSTRAINT price_snapshots_output_price_check CHECK ((output_price >= (0)::numeric)),
    CONSTRAINT price_snapshots_price_ratio_check CHECK (((price_ratio IS NULL) OR (price_ratio >= (0)::numeric))),
    CONSTRAINT price_snapshots_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT price_snapshots_reasoning_output_price_check CHECK (((reasoning_output_price IS NULL) OR (reasoning_output_price >= (0)::numeric))),
    CONSTRAINT price_snapshots_uncached_input_price_check CHECK ((uncached_input_price >= (0)::numeric))
);

ALTER SEQUENCE public.price_snapshots_id_seq OWNED BY public.price_snapshots.id;

ALTER TABLE ONLY public.price_snapshots ALTER COLUMN id SET DEFAULT nextval('public.price_snapshots_id_seq'::regclass);

ALTER TABLE ONLY public.price_snapshots
    ADD CONSTRAINT price_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.price_snapshots
    ADD CONSTRAINT price_snapshots_request_record_id_key UNIQUE (request_record_id);

ALTER TABLE ONLY public.price_snapshots
    ADD CONSTRAINT price_snapshots_price_id_fkey FOREIGN KEY (price_id) REFERENCES public.channel_prices(id);

ALTER TABLE ONLY public.price_snapshots
    ADD CONSTRAINT price_snapshots_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000035_repoint_snapshots_to_channel_prices]
-- 阶段 15：把价格 / 成本快照与补偿任务的外键从退役的 prices / channel_cost_prices 改挂到 channel_prices。
-- 开发期库可重置，迁移在空快照表上执行；生产化前若有历史快照需另设数据迁移（详见 PLAN §12）。
--
-- price_snapshots.price_id：模型级 prices(id) -> 渠道级 channel_prices(id)。
-- [000071_add_price_ratio_snapshots]
-- 为「客户售价快照」与「结算补偿任务」补记结算当时使用的线路倍率（DEC-026：客户售价 = 模型基准价 × 线路倍率）。
--
-- 背景：此前请求列表/详情的「线路倍率」是实时读 routes.price_ratio，「模型基准价」是用 售价 ÷ 倍率 倒推。
-- 管理员改倍率后，历史请求会显示当前倍率（而非结算当时的倍率），倒推出的基准价随之失真。
-- 快照结算当时的倍率后，历史请求恒显示当时真实倍率、基准价倒推也随之稳定，不再被后续改倍率污染。
--
-- 列可空：迁移前的历史行没有该快照，展示端对 NULL 回落为「—」（当时倍率未记录，不臆造当前值）。
-- [000075_add_cache_write_30m]
-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。
--
-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
