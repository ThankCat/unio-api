-- 汇率表（多货币支持 Phase 1，设计见 docs/changes/2026-08-29-multi-currency/PLAN.md M1/D7）：
-- worker 定时从外部源拉取 USD 对各币种的汇率落表，margin 守卫 / 结算 / 展示只读本表，
-- 任何请求路径都不同步调用外部 API。行只追加不删除（数据量每天几行，永久保留），
-- 消费口径唯一：ORDER BY rate_date DESC, fetched_at DESC LIMIT 1——手工行（source='manual'）
-- 只要 rate_date / fetched_at 更新即自然生效，是外部源全挂时的最终兜底。
CREATE SEQUENCE public.exchange_rates_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.exchange_rates (
    -- id: 主键。--
    id bigint NOT NULL,
    -- base_currency: 基准货币，当前固定 'USD'（平台结算币）。--
    base_currency text NOT NULL,
    -- quote_currency: 目标货币，如 'CNY'。--
    quote_currency text NOT NULL,
    -- rate: 1 单位基准货币兑多少目标货币（如 7.1700 = 1 USD 兑 7.17 CNY）。--
    rate numeric(20,10) NOT NULL,
    -- rate_date: 汇率所属日（外部源的行情日期，不是拉取时间）。--
    rate_date date NOT NULL,
    -- source: 来源标识（'exchangerate-api' / 'er-api' / 'frankfurter' / 'manual'），可追溯。--
    source text NOT NULL,
    -- fetched_at: 入库/更新时间；同一 (pair, rate_date, source) 重复拉取时刷新。--
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT exchange_rates_rate_check CHECK ((rate > (0)::numeric)),
    CONSTRAINT exchange_rates_pair_check CHECK ((base_currency <> quote_currency)),
    CONSTRAINT exchange_rates_base_check CHECK ((btrim(base_currency) <> ''::text)),
    CONSTRAINT exchange_rates_quote_check CHECK ((btrim(quote_currency) <> ''::text)),
    CONSTRAINT exchange_rates_source_check CHECK ((btrim(source) <> ''::text))
);

ALTER SEQUENCE public.exchange_rates_id_seq OWNED BY public.exchange_rates.id;

ALTER TABLE ONLY public.exchange_rates
    ALTER COLUMN id SET DEFAULT nextval('public.exchange_rates_id_seq'::regclass);

ALTER TABLE ONLY public.exchange_rates
    ADD CONSTRAINT exchange_rates_pkey PRIMARY KEY (id);

-- 同一 (币种对, 汇率日, 来源) 一行：worker 同日重复拉取走 ON CONFLICT 刷新 rate/fetched_at。
ALTER TABLE ONLY public.exchange_rates
    ADD CONSTRAINT uq_exchange_rates UNIQUE (base_currency, quote_currency, rate_date, source);

-- 最新汇率查询（守卫/结算/展示高频路径）。
CREATE INDEX idx_exchange_rates_lookup
    ON public.exchange_rates USING btree (base_currency, quote_currency, rate_date DESC, fetched_at DESC);
