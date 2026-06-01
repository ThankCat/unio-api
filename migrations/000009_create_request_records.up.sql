-- Request record 是一次用户可见的 Unio API 请求事实。
CREATE TABLE request_records (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_id: 对外展示和日志串联的请求 ID。--
    request_id TEXT NOT NULL UNIQUE,

    -- user_id: 发起请求的用户 ID。--
    user_id BIGINT NOT NULL REFERENCES users (id),

    -- project_id: 发起请求的项目 ID。--
    project_id BIGINT NOT NULL REFERENCES projects (id),

    -- api_key_id: 发起请求的 API Key ID。--
    api_key_id BIGINT NOT NULL REFERENCES api_keys (id),

    -- requested_model_id: 用户请求的模型 ID。--
    requested_model_id TEXT NOT NULL,

    -- ingress_protocol: 客户调用 Unio 时使用的公开协议族。--
    ingress_protocol TEXT NOT NULL CHECK (ingress_protocol IN ('openai', 'anthropic')),

    -- operation: 客户调用的公开协议操作。--
    operation TEXT NOT NULL CHECK (operation IN ('chat_completions', 'messages')),

    -- response_model_id: 最终响应使用的模型 ID。--
    response_model_id TEXT,

    -- response_protocol: 返回给客户的协议族，未产生响应时为空。--
    response_protocol TEXT CHECK (
        response_protocol IS NULL
            OR response_protocol IN ('openai', 'anthropic')
    ),

    -- response_id: 返回给客户的响应 ID，未产生响应时为空。--
    response_id TEXT CHECK (response_id IS NULL OR response_id <> ''),

    -- stream: 是否为流式请求。--
    stream BOOLEAN NOT NULL,

    -- status: 请求状态机状态。--
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),

    -- final_provider_id: 最终成功或终态关联的 provider ID。--
    final_provider_id BIGINT REFERENCES providers (id),

    -- final_channel_id: 最终成功或终态关联的 channel ID。--
    final_channel_id BIGINT REFERENCES channels (id),

    -- error_code: 安全稳定的终态错误码。--
    error_code TEXT,

    -- error_message: 可安全展示的终态错误文案。--
    error_message TEXT,

    -- internal_error_detail: 仅供内部排查的截断错误详情。--
    internal_error_detail TEXT,

    -- delivery_status: 客户响应交付状态，与 settlement 状态分开记录。--
    delivery_status TEXT NOT NULL DEFAULT 'not_started' CHECK (
        delivery_status IN ('not_started', 'in_progress', 'completed', 'interrupted')
    ),

    -- response_started_at: 首个客户可见响应写出时间。--
    response_started_at TIMESTAMPTZ,

    -- response_completed_at: 客户可见响应完整交付时间。--
    response_completed_at TIMESTAMPTZ,

    -- started_at: 请求开始时间。--
    started_at TIMESTAMPTZ NOT NULL,

    -- completed_at: 请求完成时间。--
    completed_at TIMESTAMPTZ,

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- updated_at: 记录更新时间。--
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 组合唯一约束用于账本外键校验 request 和 user 归属一致。--
    CONSTRAINT uq_request_records_id_user UNIQUE (id, user_id),

    -- 公开协议族与操作必须保持匹配，禁止跨协议隐式 bridge。--
    CONSTRAINT ck_request_records_protocol_operation CHECK (
        (
            ingress_protocol = 'openai'
                AND operation = 'chat_completions'
        )
        OR
        (
            ingress_protocol = 'anthropic'
                AND operation = 'messages'
        )
    ),

    -- 完整交付时间只能在 completed 状态出现。--
    CONSTRAINT ck_request_records_delivery_completed_at CHECK (
        (
            delivery_status = 'completed'
                AND response_completed_at IS NOT NULL
        )
        OR
        (
            delivery_status <> 'completed'
                AND response_completed_at IS NULL
        )
    )
);

-- 请求日志常按用户和创建时间倒序查询。
CREATE INDEX idx_request_records_user_created_at ON request_records (user_id, created_at DESC);

-- 请求日志常按项目和创建时间倒序查询。
CREATE INDEX idx_request_records_project_created_at ON request_records (project_id, created_at DESC);

-- 请求日志常按 API Key 和创建时间倒序查询。
CREATE INDEX idx_request_records_api_key_created_at ON request_records (api_key_id, created_at DESC);

-- 补偿和后台排查会按状态和创建时间扫描请求。
CREATE INDEX idx_request_records_status_created_at ON request_records (status, created_at DESC);
