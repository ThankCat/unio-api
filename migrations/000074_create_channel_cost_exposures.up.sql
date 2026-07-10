-- channel_cost_exposures 是 bill-on-cancel 渠道的平台成本敞口事实（DESIGN-bill-on-cancel 阶段一）。
-- 一行 = 一次「请求已发到上游、但本 attempt 不会产生真实结算成本」的失败/取消，
-- 上游（断开不取消、照常计费）大概率仍收了钱；金额为保守上界估算（输入保守估 + 输出按上限假定）。
-- 与 ledger/结算完全隔离：不动客户余额、不进 usage_records，纯平台侧成本观测，出错最多是估算偏差。
CREATE TABLE channel_cost_exposures (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_record_id: 所属请求记录。--
    request_record_id BIGINT NOT NULL REFERENCES request_records (id),

    -- attempt_id: 产生敞口的具体上游尝试。--
    attempt_id BIGINT NOT NULL REFERENCES request_attempts (id),

    -- channel_id: 产生敞口的渠道（bill-on-disconnect 渠道）。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- provider_id: 渠道所属 provider（冗余便于聚合）。--
    provider_id BIGINT NOT NULL REFERENCES providers (id),

    -- reason: 敞口成因。upstream_timeout=等首字节超时；upstream_error=上游 5xx/传输层失败；
    -- client_canceled=客户端在上游生成期间断开。--
    reason TEXT NOT NULL CHECK (reason IN ('upstream_timeout', 'upstream_error', 'client_canceled')),

    -- estimated_input_tokens: 输入 token 保守估算（复用预授权阶段 ConservativeInputTokens）。--
    estimated_input_tokens BIGINT NOT NULL CHECK (estimated_input_tokens >= 0),

    -- assumed_output_tokens: 假定输出 token（上界：模型 max_output_tokens，缺省进程级兜底）。--
    assumed_output_tokens BIGINT NOT NULL CHECK (assumed_output_tokens >= 0),

    -- estimated_cost_amount: 按渠道成本价折算的敞口金额上界（NUMERIC，不用 float）。--
    estimated_cost_amount NUMERIC NOT NULL CHECK (estimated_cost_amount >= 0),

    -- currency: 金额币种（随渠道成本价快照）。--
    currency TEXT NOT NULL,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 渠道成本对账按 (channel, 时间) 聚合。
CREATE INDEX idx_channel_cost_exposures_channel_created_at
    ON channel_cost_exposures (channel_id, created_at DESC);

-- 请求详情页按请求查敞口。
CREATE INDEX idx_channel_cost_exposures_request
    ON channel_cost_exposures (request_record_id);
