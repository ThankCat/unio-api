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
    sqlc.arg(output_tokens_total),
    sqlc.arg(output_tokens_total_state),
    sqlc.arg(reasoning_output_tokens),
    sqlc.arg(reasoning_output_tokens_state),
    sqlc.arg(usage_source),
    sqlc.arg(usage_mapping_version)
)
RETURNING *;

-- name: GetUsageRecordByRequest :one
-- GetUsageRecordByRequest 按请求 ID 读取协议无关 usage 记录。
SELECT *
FROM usage_records
WHERE request_record_id = sqlc.arg(request_record_id);

-- name: ListUsageRecordsPage :many
-- ListUsageRecordsPage 供 admin 只读查询台（M6）按用户/项目/模型/时间过滤分页倒序列出用量。
-- JOIN request_records 取请求归属维度，便于后台按 user/project/model 检索。
SELECT
    u.id,
    u.request_record_id,
    r.request_id,
    r.user_id,
    r.project_id,
    r.api_key_id,
    r.requested_model_id,
    r.response_model_id,
    r.status,
    u.uncached_input_tokens,
    u.cache_read_input_tokens,
    u.cache_write_5m_input_tokens,
    u.cache_write_1h_input_tokens,
    u.output_tokens_total,
    u.reasoning_output_tokens,
    u.usage_source,
    u.usage_mapping_version,
    u.created_at
FROM usage_records u
JOIN request_records r ON r.id = u.request_record_id
WHERE (sqlc.narg('user_id')::bigint IS NULL OR r.user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('project_id')::bigint IS NULL OR r.project_id = sqlc.narg('project_id')::bigint)
  AND (sqlc.narg('model')::text IS NULL OR r.requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR u.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR u.created_at < sqlc.narg('to_time')::timestamptz)
ORDER BY
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg('sort_desc')::bool, true) THEN u.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg('sort_field')::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg('sort_desc')::bool, true) THEN u.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'model' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'model' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.requested_model_id END ASC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg('sort_field')::text = 'user_id' AND NOT COALESCE(sqlc.narg('sort_desc')::bool, false) THEN r.user_id END ASC NULLS LAST,
  u.id DESC
LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: CountUsageRecords :one
-- CountUsageRecords 返回与 ListUsageRecordsPage 相同过滤条件下的总条数。
SELECT COUNT(*) AS total
FROM usage_records u
JOIN request_records r ON r.id = u.request_record_id
WHERE (sqlc.narg('user_id')::bigint IS NULL OR r.user_id = sqlc.narg('user_id')::bigint)
  AND (sqlc.narg('project_id')::bigint IS NULL OR r.project_id = sqlc.narg('project_id')::bigint)
  AND (sqlc.narg('model')::text IS NULL OR r.requested_model_id ILIKE '%' || sqlc.narg('model')::text || '%')
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR u.created_at >= sqlc.narg('from_time')::timestamptz)
  AND (sqlc.narg('to_time')::timestamptz IS NULL OR u.created_at < sqlc.narg('to_time')::timestamptz);
