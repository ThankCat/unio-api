-- Settlement recovery job 是上游成功且已有可靠 usage 后，settlement 成功确认前的持久化补偿任务事实。
CREATE TABLE settlement_recovery_jobs (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- user_id: 任务所属用户 ID，用于审计和校验 reservation 归属。--
    user_id BIGINT NOT NULL REFERENCES users (id),

    -- request_record_id: 需要补偿 settlement 的请求记录 ID，一次请求只能有一个 recovery job。--
    request_record_id BIGINT NOT NULL UNIQUE,

    -- attempt_id: 已调用上游并拿到可靠 usage 的 attempt ID。--
    attempt_id BIGINT NOT NULL,

    -- reservation_id: 本次请求对应的余额预授权 ID。--
    reservation_id BIGINT NOT NULL UNIQUE,

    -- response_model_id: 对用户响应的 Unio 模型 ID。--
    response_model_id TEXT NOT NULL CHECK (response_model_id <> ''),

    -- model_id: 本次请求使用的 Unio 模型数据库 ID。--
    model_id BIGINT NOT NULL REFERENCES models (id),

    -- provider_id: 本次请求最终使用的 provider ID。--
    provider_id BIGINT NOT NULL REFERENCES providers (id),

    -- channel_id: 本次请求最终使用的 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- upstream_response_model: 上游响应里的模型名。--
    upstream_response_model TEXT NOT NULL CHECK (upstream_response_model <> ''),

    -- upstream_status_code: 上游成功响应的 HTTP 状态码，worker 重放 settlement 时写回 attempt。--
    upstream_status_code INTEGER NOT NULL CHECK (upstream_status_code >= 100 AND upstream_status_code <= 599),

    -- upstream_request_id: 上游返回的请求 ID，NULL 表示上游未提供。--
    upstream_request_id TEXT CHECK (upstream_request_id IS NULL OR upstream_request_id <> ''),

    -- usage_prompt_tokens: 本次请求输入 token 数。--
    usage_prompt_tokens BIGINT NOT NULL CHECK (usage_prompt_tokens >= 0),

    -- usage_completion_tokens: 本次请求输出 token 数。--
    usage_completion_tokens BIGINT NOT NULL CHECK (usage_completion_tokens >= 0),

    -- usage_total_tokens: 本次请求总 token 数。--
    usage_total_tokens BIGINT NOT NULL CHECK (usage_total_tokens >= 0),

    -- usage_cached_tokens: 本次请求缓存输入 token 数。--
    usage_cached_tokens BIGINT NOT NULL CHECK (usage_cached_tokens >= 0),

    -- usage_reasoning_tokens: 本次请求 reasoning 输出 token 数。--
    usage_reasoning_tokens BIGINT NOT NULL CHECK (usage_reasoning_tokens >= 0),

    -- usage_source: usage 来源。--
    usage_source TEXT NOT NULL CHECK (usage_source IN ('upstream_response', 'upstream_stream')),

    -- price_id: authorization 时命中的客户侧价格 ID。--
    price_id BIGINT NOT NULL REFERENCES prices (id),

    -- currency: authorization 和 settlement 使用的币种。--
    currency TEXT NOT NULL CHECK (currency <> ''),

    -- pricing_unit: authorization 和 settlement 使用的计价单位。--
    pricing_unit TEXT NOT NULL CHECK (pricing_unit = 'per_1m_tokens'),

    -- input_price: authorization 时的输入 token 售价副本。--
    input_price NUMERIC(20, 10) NOT NULL CHECK (input_price >= 0),

    -- output_price: authorization 时的输出 token 售价副本。--
    output_price NUMERIC(20, 10) NOT NULL CHECK (output_price >= 0),

    -- cached_input_price: authorization 时的缓存输入 token 售价副本。--
    cached_input_price NUMERIC(20, 10) CHECK (
      cached_input_price IS NULL
          OR cached_input_price >= 0
      ),

    -- reasoning_output_price: authorization 时的 reasoning 输出 token 售价副本。--
    reasoning_output_price NUMERIC(20, 10) CHECK (
      reasoning_output_price IS NULL
          OR reasoning_output_price >= 0
      ),

    -- formula_version: authorization 时使用的计费公式版本。--
    formula_version TEXT NOT NULL CHECK (formula_version <> ''),

    -- estimated_amount: authorization 时估算的风险金额。--
    estimated_amount NUMERIC(20, 10) NOT NULL CHECK (estimated_amount > 0),

    -- authorized_amount: authorization 时实际冻结的金额。--
    authorized_amount NUMERIC(20, 10) NOT NULL CHECK (authorized_amount > 0),

    -- status: recovery job 状态。--
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'dead')),

    -- attempt_count: worker 已尝试执行 recovery 的次数。--
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),

    -- max_attempts: 最大自动重试次数。--
    max_attempts INTEGER NOT NULL DEFAULT 10 CHECK (max_attempts > 0),

    -- next_run_at: 下次允许 worker claim 的时间。--
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- locked_by: 当前 claim 该任务的 worker 标识。--
    locked_by TEXT CHECK (locked_by IS NULL OR locked_by <> ''),

    -- locked_until: 当前 worker 锁过期时间。--
    locked_until TIMESTAMPTZ,

    -- last_error_code: 最近一次 recovery 失败的稳定错误码。--
    last_error_code TEXT,

    -- last_error_message: 最近一次 recovery 失败的安全展示文案。--
    last_error_message TEXT,

    -- last_internal_error_detail: 最近一次 recovery 失败的内部诊断详情。--
    last_internal_error_detail TEXT,

    -- last_attempted_at: 最近一次 worker 尝试 recovery 的时间。--
    last_attempted_at TIMESTAMPTZ,

    -- completed_at: job 进入 succeeded 或 dead 终态的时间。--
    completed_at TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- usage 总数必须等于输入和输出 token 之和。--
    CONSTRAINT ck_settlement_recovery_jobs_usage_total CHECK (
      usage_total_tokens = usage_prompt_tokens + usage_completion_tokens
      ),

    -- cached token 不能超过输入 token。--
    CONSTRAINT ck_settlement_recovery_jobs_cached_tokens CHECK (
      usage_cached_tokens <= usage_prompt_tokens
      ),

    -- reasoning token 不能超过输出 token。--
    CONSTRAINT ck_settlement_recovery_jobs_reasoning_tokens CHECK (
      usage_reasoning_tokens <= usage_completion_tokens
      ),

    -- 实际冻结金额不能超过估算金额。--
    CONSTRAINT ck_settlement_recovery_jobs_authorized_not_above_estimated CHECK (
      authorized_amount <= estimated_amount
      ),

    -- 自动尝试次数不能超过最大次数。--
    CONSTRAINT ck_settlement_recovery_jobs_attempt_count CHECK (
      attempt_count <= max_attempts
      ),

    -- running 状态必须持有 worker 锁，其他状态不能持有锁。--
    CONSTRAINT ck_settlement_recovery_jobs_lock_state CHECK (
      (
          status = 'running'
              AND locked_by IS NOT NULL
              AND locked_until IS NOT NULL
          )
          OR
      (
          status IN ('pending', 'succeeded', 'dead')
              AND locked_by IS NULL
              AND locked_until IS NULL
          )
      ),

    -- 只有 succeeded/dead 终态允许 completed_at。--
    CONSTRAINT ck_settlement_recovery_jobs_completed_at CHECK (
      (
          status IN ('succeeded', 'dead')
              AND completed_at IS NOT NULL
          )
          OR
      (
          status IN ('pending', 'running')
              AND completed_at IS NULL
          )
      ),

    -- job 必须保证 request_record 和 user 归属一致。--
    CONSTRAINT fk_settlement_recovery_jobs_request_user
      FOREIGN KEY (request_record_id, user_id)
          REFERENCES request_records (id, user_id),

    -- job 必须保证 attempt 属于同一个 request。--
    CONSTRAINT fk_settlement_recovery_jobs_attempt_request
      FOREIGN KEY (attempt_id, request_record_id)
          REFERENCES request_attempts (id, request_record_id),

    -- job 必须指向同用户同请求的预授权。--
    CONSTRAINT fk_settlement_recovery_jobs_reservation
      FOREIGN KEY (reservation_id, user_id, request_record_id)
          REFERENCES ledger_reservations (id, user_id, request_record_id),

    -- job 必须保证最终 channel 属于最终 provider。--
    CONSTRAINT fk_settlement_recovery_jobs_channel_provider
      FOREIGN KEY (channel_id, provider_id)
          REFERENCES channels (id, provider_id),

    -- job 必须对应真实存在的 channel-model 服务能力。--
    CONSTRAINT fk_settlement_recovery_jobs_channel_model
      FOREIGN KEY (channel_id, model_id)
          REFERENCES channel_models (channel_id, model_id)
);

-- worker claim pending job 时按 next_run_at 和 id 稳定扫描。
CREATE INDEX idx_settlement_recovery_jobs_pending_next_run
    ON settlement_recovery_jobs (next_run_at, id)
    WHERE status = 'pending';

-- worker 会重新 claim 锁过期的 running job。
CREATE INDEX idx_settlement_recovery_jobs_running_locked_until
    ON settlement_recovery_jobs (locked_until, id)
    WHERE status = 'running';

-- 后台审计会按用户倒序查看 recovery job。
CREATE INDEX idx_settlement_recovery_jobs_user_created_at
    ON settlement_recovery_jobs (user_id, created_at DESC, id DESC);
