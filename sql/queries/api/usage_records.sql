-- name: CreateUsageRecord :one
-- CreateUsageRecord 创建一次请求最终用于计费和审计的协议无关 usage 记录。
INSERT INTO usage_records (
    request_record_id,
    uncached_input_tokens,
    uncached_input_tokens_state,
    cache_read_input_tokens,
    cache_read_input_tokens_state,
    cache_write_5m_input_tokens,
    cache_write_5m_input_tokens_state,
    cache_write_1h_input_tokens,
    cache_write_1h_input_tokens_state,
    cache_write_30m_input_tokens,
    cache_write_30m_input_tokens_state,
    output_tokens_total,
    output_tokens_total_state,
    reasoning_output_tokens,
    reasoning_output_tokens_state,
    usage_source,
    usage_mapping_version
)
VALUES (
    sqlc.arg(request_record_id),
    sqlc.arg(uncached_input_tokens),
    sqlc.arg(uncached_input_tokens_state),
    sqlc.arg(cache_read_input_tokens),
    sqlc.arg(cache_read_input_tokens_state),
    sqlc.arg(cache_write_5m_input_tokens),
    sqlc.arg(cache_write_5m_input_tokens_state),
    sqlc.arg(cache_write_1h_input_tokens),
    sqlc.arg(cache_write_1h_input_tokens_state),
    sqlc.arg(cache_write_30m_input_tokens),
    sqlc.arg(cache_write_30m_input_tokens_state),
    sqlc.arg(output_tokens_total),
    sqlc.arg(output_tokens_total_state),
    sqlc.arg(reasoning_output_tokens),
    sqlc.arg(reasoning_output_tokens_state),
    sqlc.arg(usage_source),
    sqlc.arg(usage_mapping_version)
)
RETURNING *;
