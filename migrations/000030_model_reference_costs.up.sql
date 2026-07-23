-- Model reference cost 是某个 Unio 模型的「上游参考成本」（DEC-027 渠道成本倍率）。
-- 渠道真实成本 = 本表参考成本 × channel_cost_multipliers（价格倍率）× channel_recharge_factors（充值倍率）。
-- 语义 = 上游官方/参考成本（名义币种），跨渠道共享、变化频率低（provider 官方调价才动）。
-- 结算审计：cost snapshot 记录命中的 model_reference_costs.id + 当时倍率，历史成本可按原事实复算。
-- btree_gist 让 exclusion constraint 可同时比较等值列与时间范围重叠（000031/000054 已建，幂等保证）。
CREATE SEQUENCE public.model_reference_costs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.model_reference_costs (
    -- id: 主键。--
    id bigint NOT NULL,
    -- model_id: 参考成本适用的 Unio 模型 ID。--
    model_id bigint NOT NULL,
    -- currency: 计价币种（名义，上游报价单位）。--
    currency text NOT NULL,
    -- pricing_unit: 计价单位。--
    pricing_unit text NOT NULL,
    -- uncached_input_cost: 每计价单位未缓存输入 token 参考成本（必填）。--
    uncached_input_cost numeric(20,10) NOT NULL,
    -- cache_read_input_cost: 每计价单位缓存读取输入 token 参考成本。--
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
