-- Channel cost multiplier 是某个渠道在「模型基准价」之上的价格倍率（DEC-027 / DEC-031）。
-- 上游名义成本 = model_prices（模型基准价，DEC-031 成本基数）× 本倍率。语义 = 中转站在官方价上的加价倍率。
-- 变化频率高（中转调价就改），沿用「不可改 + 时间窗 + 新建一条 + 关闭旧窗口」范式。
-- model_id 可空：NULL = 渠道默认倍率（对全部模型生效）；非空 = 对该模型的覆盖（优先于默认）。
CREATE SEQUENCE public.channel_cost_multipliers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_cost_multipliers (
    -- id: 主键。--
    id bigint NOT NULL,
    -- channel_id: 倍率适用的渠道 ID。--
    channel_id bigint NOT NULL,
    -- model_id: NULL=渠道默认倍率；非空=对该模型的覆盖。--
    model_id bigint,
    -- multiplier: 相对上游参考成本的倍数（如 1.15 = 官方价的 115%）。--
    multiplier numeric(20,10) NOT NULL,
    -- status: 倍率启停状态。--
    status text NOT NULL,
    -- effective_from: 生效开始时间。--
    effective_from timestamp with time zone NOT NULL,
    -- effective_to: 生效结束时间，空值表示长期有效。--
    effective_to timestamp with time zone,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    -- updated_at: 记录更新时间。--
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    -- model_key: 把 NULL 默认与各逐模型覆盖各自当成一条时间线（NULL → 0），供窗口不重叠约束比较。--
    model_key bigint GENERATED ALWAYS AS (COALESCE(model_id, (0)::bigint)) STORED,
    CONSTRAINT channel_cost_multipliers_multiplier_check CHECK ((multiplier >= (0)::numeric)),
    CONSTRAINT channel_cost_multipliers_status_check CHECK ((status = ANY (ARRAY['enabled'::text, 'disabled'::text]))),
    CONSTRAINT ck_channel_cost_multipliers_window CHECK (((effective_to IS NULL) OR (effective_to > effective_from)))
);

ALTER SEQUENCE public.channel_cost_multipliers_id_seq OWNED BY public.channel_cost_multipliers.id;

ALTER TABLE ONLY public.channel_cost_multipliers ALTER COLUMN id SET DEFAULT nextval('public.channel_cost_multipliers_id_seq'::regclass);

ALTER TABLE ONLY public.channel_cost_multipliers
    ADD CONSTRAINT channel_cost_multipliers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.channel_cost_multipliers
    ADD CONSTRAINT ex_channel_cost_multipliers_enabled_window EXCLUDE USING gist (channel_id WITH =, model_key WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&) WHERE ((status = 'enabled'::text));

CREATE INDEX idx_channel_cost_multipliers_channel_model_status_effective ON public.channel_cost_multipliers USING btree (channel_id, model_key, status, effective_from DESC, id DESC);

ALTER TABLE ONLY public.channel_cost_multipliers
    ADD CONSTRAINT channel_cost_multipliers_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.channel_cost_multipliers
    ADD CONSTRAINT channel_cost_multipliers_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);
