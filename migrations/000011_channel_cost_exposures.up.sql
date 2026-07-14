-- channel_cost_exposures 是 bill-on-cancel 渠道的平台成本敞口事实（DESIGN-bill-on-cancel 阶段一）。
-- 一行 = 一次「请求已发到上游、但本 attempt 不会产生真实结算成本」的失败/取消，
-- 上游（断开不取消、照常计费）大概率仍收了钱；金额为保守上界估算（输入保守估 + 输出按上限假定）。
-- 与 ledger/结算完全隔离：不动客户余额、不进 usage_records，纯平台侧成本观测，出错最多是估算偏差。
CREATE SEQUENCE public.channel_cost_exposures_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.channel_cost_exposures (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_record_id: 所属请求记录。--
    request_record_id bigint NOT NULL,
    -- attempt_id: 产生敞口的具体上游尝试。--
    attempt_id bigint NOT NULL,
    -- channel_id: 产生敞口的渠道（bill-on-disconnect 渠道）。--
    channel_id bigint NOT NULL,
    -- provider_id: 渠道所属 provider（冗余便于聚合）。--
    provider_id bigint NOT NULL,
    -- reason: 敞口成因。upstream_timeout=等首字节超时；upstream_error=上游 5xx/传输层失败；
    -- client_canceled=客户端在上游生成期间断开。--
    reason text NOT NULL,
    -- estimated_input_tokens: 输入 token 保守估算（复用预授权阶段 ConservativeInputTokens）。--
    estimated_input_tokens bigint NOT NULL,
    -- assumed_output_tokens: 假定输出 token（上界：模型 max_output_tokens，缺省进程级兜底）。--
    assumed_output_tokens bigint NOT NULL,
    -- estimated_cost_amount: 按渠道成本价折算的敞口金额上界（NUMERIC，不用 float）。--
    estimated_cost_amount numeric NOT NULL,
    -- currency: 金额币种（随渠道成本价快照）。--
    currency text NOT NULL,
    -- created_at: 记录创建时间。--
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_cost_exposures_assumed_output_tokens_check CHECK ((assumed_output_tokens >= 0)),
    CONSTRAINT channel_cost_exposures_estimated_cost_amount_check CHECK ((estimated_cost_amount >= (0)::numeric)),
    CONSTRAINT channel_cost_exposures_estimated_input_tokens_check CHECK ((estimated_input_tokens >= 0)),
    CONSTRAINT channel_cost_exposures_reason_check CHECK ((reason = ANY (ARRAY['upstream_timeout'::text, 'upstream_error'::text, 'client_canceled'::text])))
);

ALTER SEQUENCE public.channel_cost_exposures_id_seq OWNED BY public.channel_cost_exposures.id;

ALTER TABLE ONLY public.channel_cost_exposures ALTER COLUMN id SET DEFAULT nextval('public.channel_cost_exposures_id_seq'::regclass);

ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_pkey PRIMARY KEY (id);

CREATE INDEX idx_channel_cost_exposures_channel_created_at ON public.channel_cost_exposures USING btree (channel_id, created_at DESC);

CREATE INDEX idx_channel_cost_exposures_request ON public.channel_cost_exposures USING btree (request_record_id);

ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_attempt_id_fkey FOREIGN KEY (attempt_id) REFERENCES public.request_attempts(id);

ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.channel_cost_exposures
    ADD CONSTRAINT channel_cost_exposures_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);
