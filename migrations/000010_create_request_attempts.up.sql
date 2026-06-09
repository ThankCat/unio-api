-- Request attempt 是一次 logical request 下的一次上游 channel 尝试事实。
CREATE TABLE request_attempts (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_record_id: 所属请求记录 ID。--
    request_record_id BIGINT NOT NULL REFERENCES request_records (id),

    -- attempt_index: 同一请求内的尝试序号。--
    attempt_index INTEGER NOT NULL CHECK (attempt_index >= 0),

    -- provider_id: 本次尝试使用的 provider ID。--
    provider_id BIGINT NOT NULL REFERENCES providers (id),

    -- channel_id: 本次尝试使用的 channel ID。--
    channel_id BIGINT NOT NULL REFERENCES channels (id),

    -- adapter_key: 本次尝试使用的 adapter 注册键。--
    adapter_key TEXT NOT NULL,

    -- upstream_model: 本次尝试发送给上游的模型名。--
    upstream_model TEXT NOT NULL,

    -- upstream_protocol: 本次尝试调用上游时使用的协议族。--
    upstream_protocol TEXT NOT NULL CHECK (upstream_protocol IN ('openai', 'anthropic')),

    -- upstream_response_id: provider 返回的响应 ID，与客户可见 response_id 分列。--
    upstream_response_id TEXT CHECK (upstream_response_id IS NULL OR upstream_response_id <> ''),

    -- upstream_response_model: 上游响应里的模型名。--
    upstream_response_model TEXT,

    -- upstream_finish_reason: provider 返回的原始结束原因，仅用于审计。--
    upstream_finish_reason TEXT,

    -- finish_class: 协议无关的稳定结束分类。--
    finish_class TEXT CHECK (
        finish_class IS NULL
            OR finish_class IN ('stop', 'length', 'tool_use', 'content_filter', 'refusal', 'pause', 'other')
    ),

    -- status: attempt 状态机状态。--
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'canceled')),

    -- upstream_status_code: 上游 HTTP 状态码。--
    upstream_status_code INTEGER CHECK (upstream_status_code IS NULL OR (upstream_status_code >= 100 AND upstream_status_code <= 599)),

    -- upstream_request_id: 上游返回的请求 ID。--
    upstream_request_id TEXT,

    -- error_code: 安全稳定的 attempt 错误码。--
    error_code TEXT,

    -- error_message: 可安全展示的 attempt 错误文案。--
    error_message TEXT,

    -- internal_error_detail: 仅供内部排查的截断错误详情。--
    internal_error_detail TEXT,

    -- response_started_at: 上游开始返回响应的时间，未知时为空。--
    response_started_at TIMESTAMPTZ,

    -- final_usage_received: 是否已经收到可用于 settlement 的最终 usage。--
    final_usage_received BOOLEAN NOT NULL DEFAULT FALSE,

    -- usage_mapping_version: 将协议 usage 映射成统一 facts 的规则版本。--
    usage_mapping_version TEXT CHECK (usage_mapping_version IS NULL OR usage_mapping_version <> ''),

    -- required_capabilities: 本次尝试快照的 ingress 推断所需能力 key（capability 闸门 observe 审计，per-attempt 同值）。--
    required_capabilities TEXT[] NOT NULL DEFAULT '{}',

    -- started_at: attempt 开始时间。--
    started_at TIMESTAMPTZ NOT NULL,

    -- completed_at: attempt 完成时间。--
    completed_at TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 同一请求下 attempt_index 不能重复。--
    UNIQUE (request_record_id, attempt_index),

    -- recovery job 需要校验 attempt 属于同一个 request。--
    CONSTRAINT uq_request_attempts_id_request
        UNIQUE (id, request_record_id)
);

-- channel 健康、审计和 fallback 排查会按 channel 倒序查看尝试记录。
CREATE INDEX idx_request_attempts_channel_created_at ON request_attempts (channel_id, created_at DESC);
