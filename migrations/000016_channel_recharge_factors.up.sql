-- Channel recharge factor 是某个渠道的「充值倍率」（DEC-027）：把 1 单位上游名义额度换算成
-- 你真金白银付出的结算币种金额（已把汇率 + 充值优惠折进去）。
-- 渠道真实成本 = 上游名义成本 × 本充值倍率。语义 = 你充值时的汇率/优惠（如充 33RMB 得 500 名义USD
-- → 按结算币种 USD 折 ≈ 0.0092 真实USD/名义USD）。账户级、无 model 维度（充值不分模型）。
-- 变化频率高（每次充值/换档就改），沿用「不可改 + 时间窗 + 新建一条 + 关闭旧窗口」范式。
CREATE SEQUENCE public.channel_recharge_factors_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_recharge_factors (
    -- id: 主键。--
    id bigint NOT NULL,
    -- channel_id: 充值倍率适用的渠道 ID。--
    channel_id bigint NOT NULL,
    -- factor: 每 1 单位上游名义额度折合多少结算币种真实钱（含汇率 + 充值优惠）。--
    factor numeric(20,10) NOT NULL,
    -- status: 充值倍率启停状态。--
    status text NOT NULL,
    -- effective_from: 生效开始时间。--
    effective_from timestamp with time zone NOT NULL,
    -- effective_to: 生效结束时间，空值表示长期有效。--
    effective_to timestamp with time zone,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_recharge_factors_factor_check CHECK ((factor >= (0)::numeric)),
    CONSTRAINT channel_recharge_factors_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT ck_channel_recharge_factors_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from)))
);

ALTER SEQUENCE public.channel_recharge_factors_id_seq OWNED BY public.channel_recharge_factors.id;

ALTER TABLE ONLY public.channel_recharge_factors ALTER COLUMN id SET DEFAULT nextval('public.channel_recharge_factors_id_seq'::regclass);

ALTER TABLE ONLY public.channel_recharge_factors
    ADD CONSTRAINT channel_recharge_factors_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channel_recharge_factors
    ADD CONSTRAINT ex_channel_recharge_factors_enabled_window EXCLUDE USING gist (channel_id WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

CREATE INDEX idx_channel_recharge_factors_channel_status_effective ON public.channel_recharge_factors USING btree (channel_id, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.channel_recharge_factors
    ADD CONSTRAINT channel_recharge_factors_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);
