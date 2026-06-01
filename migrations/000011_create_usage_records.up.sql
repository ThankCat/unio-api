-- Usage record 是一次请求最终用于计费和审计的协议无关用量事实。
CREATE TABLE usage_records (
    -- id: 主键。--
    id BIGSERIAL PRIMARY KEY,

    -- request_record_id: 对应的请求记录 ID。--
    request_record_id BIGINT NOT NULL UNIQUE REFERENCES request_records (id),

    -- uncached_input_tokens: 未命中上游缓存的输入 token 数。--
    uncached_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (uncached_input_tokens >= 0),

    -- uncached_input_tokens_state: 未缓存输入 token 的可信状态。--
    uncached_input_tokens_state TEXT NOT NULL CHECK (
        uncached_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- cache_read_input_tokens: 命中上游缓存读取的输入 token 数。--
    cache_read_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_read_input_tokens >= 0),

    -- cache_read_input_tokens_state: 缓存读取 token 的可信状态。--
    cache_read_input_tokens_state TEXT NOT NULL CHECK (
        cache_read_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- cache_write_5m_input_tokens: 写入 5 分钟缓存的输入 token 数。--
    cache_write_5m_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_write_5m_input_tokens >= 0),

    -- cache_write_5m_input_tokens_state: 5 分钟缓存写入 token 的可信状态。--
    cache_write_5m_input_tokens_state TEXT NOT NULL CHECK (
        cache_write_5m_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- cache_write_1h_input_tokens: 写入 1 小时缓存的输入 token 数。--
    cache_write_1h_input_tokens BIGINT NOT NULL DEFAULT 0 CHECK (cache_write_1h_input_tokens >= 0),

    -- cache_write_1h_input_tokens_state: 1 小时缓存写入 token 的可信状态。--
    cache_write_1h_input_tokens_state TEXT NOT NULL CHECK (
        cache_write_1h_input_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- output_tokens_total: 包含 reasoning 的权威输出 token 总数。--
    output_tokens_total BIGINT NOT NULL DEFAULT 0 CHECK (output_tokens_total >= 0),

    -- output_tokens_total_state: 权威输出 token 总数的可信状态。--
    output_tokens_total_state TEXT NOT NULL CHECK (
        output_tokens_total_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- reasoning_output_tokens: 输出中 reasoning token 的可选分解数。--
    reasoning_output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (reasoning_output_tokens >= 0),

    -- reasoning_output_tokens_state: reasoning token 分解项的可信状态。--
    reasoning_output_tokens_state TEXT NOT NULL CHECK (
        reasoning_output_tokens_state IN ('known', 'not_applicable', 'unknown')
    ),

    -- usage_source: usage 来源轨道。--
    usage_source TEXT NOT NULL CHECK (usage_source IN ('upstream_response', 'upstream_stream')),

    -- usage_mapping_version: 将协议 usage 映射成统一 facts 的规则版本。--
    usage_mapping_version TEXT NOT NULL CHECK (usage_mapping_version <> ''),

    -- created_at: 记录创建时间。--
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 非 known 状态不能携带伪造的 token 数值。--
    CONSTRAINT ck_usage_records_non_known_values_zero CHECK (
        (uncached_input_tokens_state = 'known' OR uncached_input_tokens = 0)
            AND (cache_read_input_tokens_state = 'known' OR cache_read_input_tokens = 0)
            AND (cache_write_5m_input_tokens_state = 'known' OR cache_write_5m_input_tokens = 0)
            AND (cache_write_1h_input_tokens_state = 'known' OR cache_write_1h_input_tokens = 0)
            AND (output_tokens_total_state = 'known' OR output_tokens_total = 0)
            AND (reasoning_output_tokens_state = 'known' OR reasoning_output_tokens = 0)
    ),

    -- reasoning 分解项与输出总量都已知时，reasoning 不能超过总输出。--
    CONSTRAINT ck_usage_records_reasoning_not_above_output CHECK (
        reasoning_output_tokens_state <> 'known'
            OR output_tokens_total_state <> 'known'
            OR reasoning_output_tokens <= output_tokens_total
    )
);
