-- Model price 是某个 Unio 模型的「基准客户售价」（DEC-026 倍率定价）。
-- 客户最终售价 = 本表基准售价 × routes.price_ratio（线路倍率）；售价不再挂渠道，渠道只记成本。
-- 结算审计：price snapshot 记录命中的 model_prices.id + 当时线路倍率，历史账单可按原事实复算。
-- btree_gist 让 exclusion constraint 可同时比较等值列与时间范围重叠（000031 已建，幂等保证）。
CREATE SEQUENCE public.model_prices_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.model_prices (
    -- id: 主键。--
    id bigint NOT NULL,
    -- model_id: 基准售价适用的 Unio 模型 ID。--
    model_id bigint NOT NULL,
    -- currency: 计价币种。--
    currency text NOT NULL,
    -- pricing_unit: 计价单位。--
    pricing_unit text NOT NULL,
    -- uncached_input_price: 每计价单位未缓存输入 token 基准售价（必填）。--
    uncached_input_price numeric(20,10) NOT NULL,
    -- cache_read_input_price: 每计价单位缓存读取输入 token 基准售价。--
    cache_read_input_price numeric(20,10),
    cache_write_5m_input_price numeric(20,10),
    cache_write_1h_input_price numeric(20,10),
    output_price numeric(20,10) NOT NULL,
    reasoning_output_price numeric(20,10),
    status text NOT NULL,
    effective_from timestamp with time zone NOT NULL,
    effective_to timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    cache_write_30m_input_price numeric(20,10),
    CONSTRAINT ck_model_prices_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from))),
    CONSTRAINT model_prices_cache_read_input_price_check CHECK (((cache_read_input_price IS NULL) OR (cache_read_input_price >= (0)::numeric))),
    CONSTRAINT model_prices_cache_write_1h_input_price_check CHECK (((cache_write_1h_input_price IS NULL) OR (cache_write_1h_input_price >= (0)::numeric))),
    CONSTRAINT model_prices_cache_write_30m_input_price_check CHECK (((cache_write_30m_input_price IS NULL) OR (cache_write_30m_input_price >= (0)::numeric))),
    CONSTRAINT model_prices_cache_write_5m_input_price_check CHECK (((cache_write_5m_input_price IS NULL) OR (cache_write_5m_input_price >= (0)::numeric))),
    CONSTRAINT model_prices_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT model_prices_output_price_check CHECK ((output_price >= (0)::numeric)),
    CONSTRAINT model_prices_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT model_prices_reasoning_output_price_check CHECK (((reasoning_output_price IS NULL) OR (reasoning_output_price >= (0)::numeric))),
    CONSTRAINT model_prices_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT model_prices_uncached_input_price_check CHECK ((uncached_input_price >= (0)::numeric))
);

ALTER SEQUENCE public.model_prices_id_seq OWNED BY public.model_prices.id;

ALTER TABLE ONLY public.model_prices ALTER COLUMN id SET DEFAULT nextval('public.model_prices_id_seq'::regclass);

ALTER TABLE ONLY public.model_prices
    ADD CONSTRAINT ex_model_prices_enabled_window EXCLUDE USING gist (model_id WITH =, currency WITH =, pricing_unit WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

ALTER TABLE ONLY public.model_prices
    ADD CONSTRAINT model_prices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.model_prices
    ADD CONSTRAINT uq_model_prices_id_model UNIQUE (id, model_id);

CREATE INDEX idx_model_prices_model_status_effective ON public.model_prices USING btree (model_id, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.model_prices
    ADD CONSTRAINT model_prices_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000075_add_cache_write_30m]
-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。
--
-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
