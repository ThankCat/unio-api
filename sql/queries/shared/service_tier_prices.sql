-- name: GetModelPriceServiceTier :one
SELECT *
FROM model_price_service_tiers
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: GetChannelPriceServiceTier :one
SELECT *
FROM channel_price_service_tiers
WHERE id = sqlc.arg(id)
LIMIT 1;

-- name: CreateProviderServiceTierCostRisk :one
INSERT INTO provider_service_tier_cost_risks (
    provider_id,
    request_record_id,
    request_attempt_id,
    estimated_increment_amount,
    settled_amount,
    currency,
    reason_code,
    reason,
    upstream_service_tier,
    settled_service_tier,
    service_tier_resolution
)
VALUES (
    sqlc.arg(provider_id),
    sqlc.arg(request_record_id),
    sqlc.arg(request_attempt_id),
    sqlc.narg(estimated_amount),
    sqlc.arg(settled_amount),
    sqlc.arg(currency),
    sqlc.arg(reason_code),
    sqlc.arg(reason),
    sqlc.narg(upstream_service_tier),
    sqlc.arg(settled_service_tier),
    sqlc.arg(service_tier_resolution)
)
ON CONFLICT (request_attempt_id) DO UPDATE
SET provider_id = EXCLUDED.provider_id
WHERE provider_service_tier_cost_risks.provider_id = EXCLUDED.provider_id
  AND provider_service_tier_cost_risks.request_record_id = EXCLUDED.request_record_id
  AND provider_service_tier_cost_risks.estimated_increment_amount IS NOT DISTINCT FROM EXCLUDED.estimated_increment_amount
  AND provider_service_tier_cost_risks.settled_amount = EXCLUDED.settled_amount
  AND provider_service_tier_cost_risks.currency = EXCLUDED.currency
  AND provider_service_tier_cost_risks.reason_code = EXCLUDED.reason_code
  AND provider_service_tier_cost_risks.reason = EXCLUDED.reason
  AND provider_service_tier_cost_risks.upstream_service_tier IS NOT DISTINCT FROM EXCLUDED.upstream_service_tier
  AND provider_service_tier_cost_risks.settled_service_tier = EXCLUDED.settled_service_tier
  AND provider_service_tier_cost_risks.service_tier_resolution = EXCLUDED.service_tier_resolution
RETURNING *;
