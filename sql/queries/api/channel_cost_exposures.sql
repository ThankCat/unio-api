-- name: CreateChannelCostExposure :one
-- CreateChannelCostExposure 写入一条 bill-on-disconnect 渠道的平台成本敞口（DESIGN-bill-on-cancel 阶段一）。
INSERT INTO channel_cost_exposures (
    request_record_id,
    attempt_id,
    channel_id,
    provider_id,
    reason,
    estimated_input_tokens,
    assumed_output_tokens,
    estimated_cost_amount,
    currency
)
VALUES (
    sqlc.arg(request_record_id),
    sqlc.arg(attempt_id),
    sqlc.arg(channel_id),
    sqlc.arg(provider_id),
    sqlc.arg(reason),
    sqlc.arg(estimated_input_tokens),
    sqlc.arg(assumed_output_tokens),
    sqlc.arg(estimated_cost_amount),
    sqlc.arg(currency)
)
RETURNING id, request_record_id, attempt_id, channel_id, provider_id, reason, estimated_input_tokens, assumed_output_tokens, estimated_cost_amount, currency, created_at;
