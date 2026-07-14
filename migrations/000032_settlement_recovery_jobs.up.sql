-- Settlement recovery job 是上游成功且已有可靠 usage 后，settlement 成功确认前的持久化补偿任务事实。
CREATE SEQUENCE public.settlement_recovery_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.settlement_recovery_jobs (
    -- id: 主键。--
    id bigint NOT NULL,
    -- user_id: 任务所属用户 ID，用于审计和校验 reservation 归属。--
    user_id bigint NOT NULL,
    -- request_record_id: 需要补偿 settlement 的请求记录 ID，一次请求只能有一个 recovery job。--
    request_record_id bigint NOT NULL,
    -- attempt_id: 已调用上游并拿到可靠 usage 的 attempt ID。--
    attempt_id bigint NOT NULL,
    -- reservation_id: 本次请求对应的余额预授权 ID。--
    reservation_id bigint NOT NULL,
    -- response_protocol: 返回给客户的协议族。--
    response_protocol text NOT NULL,
    -- response_id: 返回给客户的响应 ID。--
    response_id text NOT NULL,
    -- response_model_id: 对用户响应的 Unio 模型 ID。--
    response_model_id text NOT NULL,
    -- model_id: 本次请求使用的 Unio 模型数据库 ID。--
    model_id bigint NOT NULL,
    -- provider_id: 本次请求最终使用的 provider ID。--
    provider_id bigint NOT NULL,
    -- channel_id: 本次请求最终使用的 channel ID。--
    channel_id bigint NOT NULL,
    -- upstream_protocol: 本次调用上游时使用的协议族。--
    upstream_protocol text NOT NULL,
    -- upstream_response_id: provider 返回的响应 ID。--
    upstream_response_id text NOT NULL,
    -- upstream_model: 上游响应里的模型名。--
    upstream_model text NOT NULL,
    -- finish_class: 协议无关的稳定结束分类。--
    finish_class text NOT NULL,
    upstream_finish_reason text NOT NULL,
    upstream_status_code integer NOT NULL,
    upstream_request_id text,
    usage_uncached_input_tokens bigint NOT NULL,
    usage_uncached_input_tokens_state text NOT NULL,
    usage_cache_read_input_tokens bigint NOT NULL,
    usage_cache_read_input_tokens_state text NOT NULL,
    usage_cache_write_5m_input_tokens bigint NOT NULL,
    usage_cache_write_5m_input_tokens_state text NOT NULL,
    usage_cache_write_1h_input_tokens bigint NOT NULL,
    usage_cache_write_1h_input_tokens_state text NOT NULL,
    usage_output_tokens_total bigint NOT NULL,
    usage_output_tokens_total_state text NOT NULL,
    usage_reasoning_output_tokens bigint NOT NULL,
    usage_reasoning_output_tokens_state text NOT NULL,
    usage_server_web_search_requests bigint DEFAULT 0 NOT NULL,
    usage_server_web_fetch_requests bigint DEFAULT 0 NOT NULL,
    usage_source text NOT NULL,
    usage_mapping_version text NOT NULL,
    price_id bigint,
    currency text NOT NULL,
    pricing_unit text NOT NULL,
    uncached_input_price numeric(20,10) NOT NULL,
    cache_read_input_price numeric(20,10),
    cache_write_5m_input_price numeric(20,10),
    cache_write_1h_input_price numeric(20,10),
    output_price numeric(20,10) NOT NULL,
    reasoning_output_price numeric(20,10),
    formula_version text NOT NULL,
    estimated_amount numeric(20,10) NOT NULL,
    authorized_amount numeric(20,10) NOT NULL,
    status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 20 NOT NULL,
    next_run_at timestamp with time zone DEFAULT now() NOT NULL,
    locked_by text,
    locked_until timestamp with time zone,
    last_error_code text,
    last_error_message text,
    last_internal_error_detail text,
    last_attempted_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    price_ratio numeric(20,10),
    usage_cache_write_30m_input_tokens bigint NOT NULL,
    usage_cache_write_30m_input_tokens_state text NOT NULL,
    cache_write_30m_input_price numeric(20,10),
    model_reference_cost_id bigint,
    channel_cost_multiplier_id bigint,
    channel_recharge_factor_id bigint,
    CONSTRAINT ck_settlement_recovery_jobs_attempt_count CHECK ((attempt_count <= max_attempts)),
    CONSTRAINT ck_settlement_recovery_jobs_authorized_not_above_estimated CHECK ((authorized_amount <= estimated_amount)),
    CONSTRAINT ck_settlement_recovery_jobs_completed_at CHECK ((((status = ANY (ARRAY['succeeded'::text, 'dead'::text])) AND (completed_at IS NOT NULL)) OR ((status = ANY (ARRAY['pending'::text, 'running'::text])) AND (completed_at IS NULL)))),
    CONSTRAINT ck_settlement_recovery_jobs_lock_state CHECK ((((status = 'running'::text) AND (locked_by IS NOT NULL) AND (locked_until IS NOT NULL)) OR ((status = ANY (ARRAY['pending'::text, 'succeeded'::text, 'dead'::text])) AND (locked_by IS NULL) AND (locked_until IS NULL)))),
    CONSTRAINT ck_settlement_recovery_jobs_non_known_values_zero CHECK ((((usage_uncached_input_tokens_state = 'known'::text) OR (usage_uncached_input_tokens = 0)) AND ((usage_cache_read_input_tokens_state = 'known'::text) OR (usage_cache_read_input_tokens = 0)) AND ((usage_cache_write_5m_input_tokens_state = 'known'::text) OR (usage_cache_write_5m_input_tokens = 0)) AND ((usage_cache_write_1h_input_tokens_state = 'known'::text) OR (usage_cache_write_1h_input_tokens = 0)) AND ((usage_cache_write_30m_input_tokens_state = 'known'::text) OR (usage_cache_write_30m_input_tokens = 0)) AND ((usage_output_tokens_total_state = 'known'::text) OR (usage_output_tokens_total = 0)) AND ((usage_reasoning_output_tokens_state = 'known'::text) OR (usage_reasoning_output_tokens = 0)))),
    CONSTRAINT ck_settlement_recovery_jobs_reasoning_not_above_output CHECK (((usage_reasoning_output_tokens_state <> 'known'::text) OR (usage_output_tokens_total_state <> 'known'::text) OR (usage_reasoning_output_tokens <= usage_output_tokens_total))),
    CONSTRAINT settlement_recovery_jobs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT settlement_recovery_jobs_authorized_amount_check CHECK ((authorized_amount > (0)::numeric)),
    CONSTRAINT settlement_recovery_jobs_cache_read_input_price_check CHECK (((cache_read_input_price IS NULL) OR (cache_read_input_price >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_cache_write_1h_input_price_check CHECK (((cache_write_1h_input_price IS NULL) OR (cache_write_1h_input_price >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_cache_write_30m_input_price_check CHECK (((cache_write_30m_input_price IS NULL) OR (cache_write_30m_input_price >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_cache_write_5m_input_price_check CHECK (((cache_write_5m_input_price IS NULL) OR (cache_write_5m_input_price >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_currency_check CHECK ((currency <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_estimated_amount_check CHECK ((estimated_amount > (0)::numeric)),
    CONSTRAINT settlement_recovery_jobs_finish_class_check CHECK ((finish_class = ANY (ARRAY['stop'::text, 'length'::text, 'tool_use'::text, 'content_filter'::text, 'refusal'::text, 'pause'::text, 'other'::text]))),
    CONSTRAINT settlement_recovery_jobs_formula_version_check CHECK ((formula_version <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_locked_by_check CHECK (((locked_by IS NULL) OR (locked_by <> ''::text))),
    CONSTRAINT settlement_recovery_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT settlement_recovery_jobs_output_price_check CHECK ((output_price >= (0)::numeric)),
    CONSTRAINT settlement_recovery_jobs_price_ratio_check CHECK (((price_ratio IS NULL) OR (price_ratio >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_pricing_unit_check CHECK ((pricing_unit = 'per_1m_tokens'::text)),
    CONSTRAINT settlement_recovery_jobs_reasoning_output_price_check CHECK (((reasoning_output_price IS NULL) OR (reasoning_output_price >= (0)::numeric))),
    CONSTRAINT settlement_recovery_jobs_response_id_check CHECK ((response_id <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_response_model_id_check CHECK ((response_model_id <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_response_protocol_check CHECK ((response_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
    CONSTRAINT settlement_recovery_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'dead'::text]))),
    CONSTRAINT settlement_recovery_jobs_uncached_input_price_check CHECK ((uncached_input_price >= (0)::numeric)),
    CONSTRAINT settlement_recovery_jobs_upstream_model_check CHECK ((upstream_model <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_upstream_protocol_check CHECK ((upstream_protocol = ANY (ARRAY['openai'::text, 'anthropic'::text]))),
    CONSTRAINT settlement_recovery_jobs_upstream_request_id_check CHECK (((upstream_request_id IS NULL) OR (upstream_request_id <> ''::text))),
    CONSTRAINT settlement_recovery_jobs_upstream_response_id_check CHECK ((upstream_response_id <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_upstream_status_code_check CHECK (((upstream_status_code >= 100) AND (upstream_status_code <= 599))),
    CONSTRAINT settlement_recovery_jobs_usage_cache_read_input_tokens_check CHECK ((usage_cache_read_input_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_cache_read_input_tokens_st_check CHECK ((usage_cache_read_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_1h_input_toke_check1 CHECK ((usage_cache_write_1h_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_1h_input_token_check CHECK ((usage_cache_write_1h_input_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_30m_input_tok_check1 CHECK ((usage_cache_write_30m_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_30m_input_toke_check CHECK ((usage_cache_write_30m_input_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_5m_input_toke_check1 CHECK ((usage_cache_write_5m_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_cache_write_5m_input_token_check CHECK ((usage_cache_write_5m_input_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_mapping_version_check CHECK ((usage_mapping_version <> ''::text)),
    CONSTRAINT settlement_recovery_jobs_usage_output_tokens_total_check CHECK ((usage_output_tokens_total >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_output_tokens_total_state_check CHECK ((usage_output_tokens_total_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_reasoning_output_tokens_check CHECK ((usage_reasoning_output_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_reasoning_output_tokens_st_check CHECK ((usage_reasoning_output_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_server_web_fetch_requests_check CHECK ((usage_server_web_fetch_requests >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_server_web_search_requests_check CHECK ((usage_server_web_search_requests >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_source_check CHECK ((usage_source = ANY (ARRAY['upstream_response'::text, 'upstream_stream'::text, 'partial_stream_estimate'::text]))),
    CONSTRAINT settlement_recovery_jobs_usage_uncached_input_tokens_check CHECK ((usage_uncached_input_tokens >= 0)),
    CONSTRAINT settlement_recovery_jobs_usage_uncached_input_tokens_stat_check CHECK ((usage_uncached_input_tokens_state = ANY (ARRAY['known'::text, 'not_applicable'::text, 'unknown'::text])))
);

ALTER SEQUENCE public.settlement_recovery_jobs_id_seq OWNED BY public.settlement_recovery_jobs.id;

ALTER TABLE ONLY public.settlement_recovery_jobs ALTER COLUMN id SET DEFAULT nextval('public.settlement_recovery_jobs_id_seq'::regclass);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_request_record_id_key UNIQUE (request_record_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_reservation_id_key UNIQUE (reservation_id);

CREATE INDEX idx_settlement_recovery_jobs_pending_next_run ON public.settlement_recovery_jobs USING btree (next_run_at, id) WHERE (status = 'pending'::text);

CREATE INDEX idx_settlement_recovery_jobs_running_locked_until ON public.settlement_recovery_jobs USING btree (locked_until, id) WHERE (status = 'running'::text);

CREATE INDEX idx_settlement_recovery_jobs_user_created_at ON public.settlement_recovery_jobs USING btree (user_id, created_at DESC, id DESC);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT fk_settlement_recovery_jobs_attempt_request FOREIGN KEY (attempt_id, request_record_id) REFERENCES public.request_attempts(id, request_record_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT fk_settlement_recovery_jobs_channel_model FOREIGN KEY (channel_id, model_id) REFERENCES public.channel_models(channel_id, model_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT fk_settlement_recovery_jobs_channel_provider FOREIGN KEY (channel_id, provider_id) REFERENCES public.channels(id, provider_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT fk_settlement_recovery_jobs_request_user FOREIGN KEY (request_record_id, user_id) REFERENCES public.request_records(id, user_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT fk_settlement_recovery_jobs_reservation FOREIGN KEY (reservation_id, user_id, request_record_id) REFERENCES public.ledger_reservations(id, user_id, request_record_id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.models(id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_price_id_fkey FOREIGN KEY (price_id) REFERENCES public.channel_prices(id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.providers(id);

ALTER TABLE ONLY public.settlement_recovery_jobs
    ADD CONSTRAINT settlement_recovery_jobs_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);

-- ---------------------------------------------------------------------------
-- 后续迁移补充的设计说明（列/约束演进，原 ALTER 迁移的中文注释归档）：
-- ---------------------------------------------------------------------------
-- [000035_repoint_snapshots_to_channel_prices]
-- 阶段 15：把价格 / 成本快照与补偿任务的外键从退役的 prices / channel_cost_prices 改挂到 channel_prices。
-- 开发期库可重置，迁移在空快照表上执行；生产化前若有历史快照需另设数据迁移（详见 PLAN §12）。
--
-- price_snapshots.price_id：模型级 prices(id) -> 渠道级 channel_prices(id)。
-- [000042_add_request_attempts_used_capabilities]
-- 能力自动校正按 key 精确命中埋点（DESIGN-capability-autocalibration TASK-H）。
--
-- request_attempts.used_capabilities：本次成功响应被 adapter 解析「真正用到」的能力 key
-- （如响应里出现 function_call → tools.function）。它取代 finish_class 作为 tools.* 的强证据来源：
--   - finish_class=tool_use 只能笼统证明「某个工具被调了」，无法区分 function/custom；
--   - 且 OpenAI Responses 直传时 finish_class 恒为 stop（Codex 主力流量），tools.* 永远拿不到证据。
-- 校正聚合按 key 命中归因；无埋点的旧行 / 其它 adapter 仍回退到 finish_class（粗粒度）。
-- [000045_drop_capability_autocalibration]
-- 移除能力自动校正与证据 v2（DEC-024 / DESIGN-capability-manual-declaration）。
-- 自动校正与 used_capabilities/delivery_mode 证据链全部废止；能力改为人工声明。
-- [000050_add_partial_stream_estimate_usage_source]
-- Stream partial settlement（TASK-7.23 / DEC-025）落地后，partial 路线（B/D）合成的
-- usage facts 以 usage_source='partial_stream_estimate' 写入 usage_records，并在触发 settlement
-- recovery 时随 job 持久化。原 CHECK 仅允许 upstream_response / upstream_stream，导致 partial
-- 结算 INSERT 与 recovery job 落库被拒（SQLSTATE 23514）。此迁移把 partial_stream_estimate
-- 纳入两处 usage_source 取值域。
-- [000051_widen_settlement_recovery_max_attempts_default]
-- P1-4：放宽 settlement 补偿任务的默认最大重试次数 10 -> 20。
-- 与 worker 退避上限（默认 5m）一起把「上游已成功但 settlement 反复失败」的总补偿覆盖窗口
-- 从 ~4 分钟拉长到 ~1 小时级，覆盖 DB/网络短时故障，避免过早 dead 导致请求被收口为 failed + 平台白白承担风险敞口。
-- 新 job 由应用层显式写入配置值（WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS），此默认值仅作 schema 兜底与文档对齐。
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
-- [000080_settlement_recovery_jobs_add_cost_source]
-- 000080: settlement_recovery_jobs 记录成本来源 pin（DEC-027）。
--
-- worker 重放 settlement 时，成本按「pin 的来源行 id」确定性复算（不按 attemptStart 重解析），
-- 与首次结算一致、不受后续改倍率竞态影响。覆盖路径已有 price_id（指向 channel_prices 成本行）；
-- 倍率路径新增下列 pin：参考成本行 + 价格倍率行 + 充值倍率行 id。
-- 只存 pin id（不存倍率标量）：这些行金额/倍率不可改，replay 时按 id 重取即得同值，标量存了也是冗余。
--
-- 放开 price_id NOT NULL：倍率路径无 channel_prices 行可指，写 NULL（FK 对 NULL 自动豁免）。
