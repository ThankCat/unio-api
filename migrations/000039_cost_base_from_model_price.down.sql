-- 000037 down: 回滚「成本基数改用模型基准价」（DEC-031）。
--
-- 诚实声明：本 down 只能重建 model_reference_costs 的空表结构（对齐 000028）并把 pin 列名改回
-- model_reference_cost_id，无法恢复已删除的参考成本数据行，也无法把已置 NULL 的历史 pin 值找回。
-- 生产回滚还需同步回退依赖 cost_base_model_price_id / model_prices 作成本基数的应用代码。

BEGIN;

-- 1) 列名改回（数据保持 NULL，无法还原）。
ALTER TABLE public.settlement_recovery_jobs
    RENAME COLUMN cost_base_model_price_id TO model_reference_cost_id;

ALTER TABLE public.cost_snapshots
    RENAME COLUMN cost_base_model_price_id TO model_reference_cost_id;

-- 2) 重建 model_reference_costs 空表（结构对齐 000028；含 cache_write_30m）。
CREATE SEQUENCE public.model_reference_costs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.model_reference_costs (
    id bigint NOT NULL,
    model_id bigint NOT NULL,
    currency text NOT NULL,
    pricing_unit text NOT NULL,
    uncached_input_cost numeric(20,10) NOT NULL,
    cache_read_input_cost numeric(20,10),
    cache_write_5m_input_cost numeric(20,10),
    cache_write_1h_input_cost numeric(20,10),
    cache_write_30m_input_cost numeric(20,10),
    output_cost numeric(20,10) NOT NULL,
    reasoning_output_cost numeric(20,10),
    status text NOT NULL,
    effective_from timestamp with time zone NOT NULL,
    effective_to timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_model_reference_costs_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from))),
    CONSTRAINT model_reference_costs_cache_read_input_cost_check CHECK (((cache_read_input_cost IS NULL) OR (cache_read_input_cost >= (0)::numeric))),
    CONSTRAINT model_reference_costs_cache_write_1h_input_cost_check CHECK (((cache_write_1h_input_cost IS NULL) OR (cache_write_1h_input_cost >= (0)::numeric))),
    CONSTRAINT model_reference_costs_cache_write_30m_input_cost_check CHECK (((cache_write_30m_input_cost IS NULL) OR (cache_write_30m_input_cost >= (0)::numeric))),
    CONSTRAINT model_reference_costs_cache_write_5m_input_cost_check CHECK (((cache_write_5m_input_cost IS NULL) OR (cache_write_5m_input_cost >= (0)::numeric))),
    CONSTRAINT model_reference_costs_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT model_reference_costs_output_cost_check CHECK ((output_cost >= (0)::numeric)),
    CONSTRAINT model_reference_costs_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT model_reference_costs_reasoning_output_cost_check CHECK (((reasoning_output_cost IS NULL) OR (reasoning_output_cost >= (0)::numeric))),
    CONSTRAINT model_reference_costs_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT model_reference_costs_uncached_input_cost_check CHECK ((uncached_input_cost >= (0)::numeric))
);

ALTER SEQUENCE public.model_reference_costs_id_seq OWNED BY public.model_reference_costs.id;

ALTER TABLE ONLY public.model_reference_costs ALTER COLUMN id SET DEFAULT nextval('public.model_reference_costs_id_seq'::regclass);

ALTER TABLE ONLY public.model_reference_costs
    ADD CONSTRAINT ex_model_reference_costs_enabled_window EXCLUDE USING gist (model_id WITH =, currency WITH =, pricing_unit WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

ALTER TABLE ONLY public.model_reference_costs
    ADD CONSTRAINT model_reference_costs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.model_reference_costs
    ADD CONSTRAINT uq_model_reference_costs_id_model UNIQUE (id, model_id);

CREATE INDEX idx_model_reference_costs_model_status_effective ON public.model_reference_costs USING btree (model_id, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.model_reference_costs
    ADD CONSTRAINT model_reference_costs_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);

COMMIT;
