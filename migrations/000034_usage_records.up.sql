-- Usage record 是一次请求最终用于计费和审计的协议无关用量事实。
CREATE SEQUENCE public.usage_records_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.usage_records (
    -- id: 主键。--
    id bigint NOT NULL,
    -- request_record_id: 对应的请求记录 ID。--
    request_record_id bigint NOT NULL,
    -- uncached_input_tokens: 未命中上游缓存的输入 token 数。--
    uncached_input_tokens bigint DEFAULT 0 NOT NULL,
    -- uncached_input_tokens_state: 未缓存输入 token 的可信状态。--
    uncached_input_tokens_state text NOT NULL,
    cache_read_input_tokens bigint DEFAULT 0 NOT NULL,
    cache_read_input_tokens_state text NOT NULL,
    cache_write_5m_input_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_5m_input_tokens_state text NOT NULL,
    cache_write_1h_input_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_1h_input_tokens_state text NOT NULL,
    output_tokens_total bigint DEFAULT 0 NOT NULL,
    output_tokens_total_state text NOT NULL,
    reasoning_output_tokens bigint DEFAULT 0 NOT NULL,
    reasoning_output_tokens_state text NOT NULL,
    usage_source text NOT NULL,
    usage_mapping_version text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    cache_write_30m_input_tokens bigint DEFAULT 0 NOT NULL,
    cache_write_30m_input_tokens_state text NOT NULL,
    CONSTRAINT ck_usage_records_non_known_values_zero CHECK ((((uncached_input_tokens_state = 'known'::text) OR (uncached_input_tokens = 0)) AND ((cache_read_input_tokens_state = 'known'::text) OR (cache_read_input_tokens = 0)) AND ((cache_write_5m_input_tokens_state = 'known'::text) OR (cache_write_5m_input_tokens = 0)) AND ((cache_write_1h_input_tokens_state = 'known'::text) OR (cache_write_1h_input_tokens = 0)) AND ((cache_write_30m_input_tokens_state = 'known'::text) OR (cache_write_30m_input_tokens = 0)) AND ((output_tokens_total_state = 'known'::text) OR (output_tokens_total = 0)) AND ((reasoning_output_tokens_state = 'known'::text) OR (reasoning_output_tokens = 0)))),
    CONSTRAINT ck_usage_records_reasoning_not_above_output CHECK (((reasoning_output_tokens_state <> 'known'::text) OR (output_tokens_total_state <> 'known'::text) OR (reasoning_output_tokens <= output_tokens_total))),
    CONSTRAINT usage_records_cache_read_input_tokens_check CHECK ((cache_read_input_tokens >= 0)),
    CONSTRAINT usage_records_cache_read_input_tokens_state_check CHECK ((cache_read_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_cache_write_1h_input_tokens_check CHECK ((cache_write_1h_input_tokens >= 0)),
    CONSTRAINT usage_records_cache_write_1h_input_tokens_state_check CHECK ((cache_write_1h_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_cache_write_30m_input_tokens_check CHECK ((cache_write_30m_input_tokens >= 0)),
    CONSTRAINT usage_records_cache_write_30m_input_tokens_state_check CHECK ((cache_write_30m_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_cache_write_5m_input_tokens_check CHECK ((cache_write_5m_input_tokens >= 0)),
    CONSTRAINT usage_records_cache_write_5m_input_tokens_state_check CHECK ((cache_write_5m_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_output_tokens_total_check CHECK ((output_tokens_total >= 0)),
    CONSTRAINT usage_records_output_tokens_total_state_check CHECK ((output_tokens_total_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_reasoning_output_tokens_check CHECK ((reasoning_output_tokens >= 0)),
    CONSTRAINT usage_records_reasoning_output_tokens_state_check CHECK ((reasoning_output_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_uncached_input_tokens_check CHECK ((uncached_input_tokens >= 0)),
    CONSTRAINT usage_records_uncached_input_tokens_state_check CHECK ((uncached_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT usage_records_usage_mapping_version_check CHECK ((usage_mapping_version <> ''::text)),
    CONSTRAINT usage_records_usage_source_check CHECK ((usage_source = ANY (ARRAY['upstream_response'::text, 'upstream_stream'::text, 'partial_stream_estimate'::text])))
);

ALTER SEQUENCE public.usage_records_id_seq OWNED BY public.usage_records.id;

ALTER TABLE ONLY public.usage_records ALTER COLUMN id SET DEFAULT nextval('public.usage_records_id_seq'::regclass);

ALTER TABLE ONLY public.usage_records
    ADD CONSTRAINT usage_records_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.usage_records
    ADD CONSTRAINT usage_records_request_record_id_key UNIQUE (request_record_id);

ALTER TABLE ONLY public.usage_records
    ADD CONSTRAINT usage_records_request_record_id_fkey FOREIGN KEY (request_record_id) REFERENCES public.request_records(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000050_add_partial_stream_estimate_usage_source]
-- Stream partial settlement（TASK-7.23 / DEC-025）落地后，partial 路线（B/D）合成的
-- usage facts 以 usage_source='partial_stream_estimate' 写入 usage_records，并在触发 settlement
-- recovery 时随 job 持久化。原 CHECK 仅允许 upstream_response / upstream_stream，导致 partial
-- 结算 INSERT 与 recovery job 落库被拒（SQLSTATE 23514）。此迁移把 partial_stream_estimate
-- 纳入两处 usage_source 取值域。
-- [000075_add_cache_write_30m]
-- 000075: 新增 cache_write_30m 缓存写入维度。
--
-- 背景：OpenAI GPT-5.6 起引入「30 分钟单档」缓存写入（cache_write_tokens，按未缓存输入价 1.25x 计费），
-- 与 Anthropic 的 5m / 1h 双档并列但语义不同。为保证账目按 TTL 语义精确区分、便于对账与未来分档定价，
-- 显式新增 cache_write_30m 维度，而非塞进既有 5m 桶。历史行回填为 0 / not_applicable，token_v1 公式对其
-- 恒为 0，历史复算结果不变（故 formula_version 不升级）。
--
-- 1) model_prices：基准售价新增 30m 缓存写单价（可空，缺省计费时回退 uncached）。
