-- name: EnsureProviderBalance :exec
INSERT INTO provider_balances (provider_id, currency)
VALUES (sqlc.arg(provider_id), sqlc.arg(currency))
ON CONFLICT (provider_id, currency) DO NOTHING;

-- name: GetProviderBalanceForUpdate :one
SELECT id, provider_id, currency, balance, created_at, updated_at
FROM provider_balances
WHERE provider_id = sqlc.arg(provider_id)
  AND currency = sqlc.arg(currency)
FOR UPDATE;

-- name: AddProviderBalance :one
UPDATE provider_balances
SET balance = balance + sqlc.arg(amount), updated_at = now()
WHERE provider_id = sqlc.arg(provider_id)
  AND currency = sqlc.arg(currency)
RETURNING id, provider_id, currency, balance, created_at, updated_at;

-- name: SubtractProviderBalance :one
UPDATE provider_balances
SET balance = balance - sqlc.arg(amount), updated_at = now()
WHERE provider_id = sqlc.arg(provider_id)
  AND currency = sqlc.arg(currency)
RETURNING id, provider_id, currency, balance, created_at, updated_at;

-- name: CreateProviderLedgerEntry :one
INSERT INTO provider_ledger_entries (
    provider_id,
    request_record_id,
    request_attempt_id,
    cost_snapshot_id,
    channel_id,
    request_id,
    channel_name,
    upstream_model,
    provider_probe_record_id,
    usage_source,
    entry_type,
    amount,
    currency,
    balance_before,
    balance_after,
    idempotency_key,
    reason
)
VALUES (
    sqlc.arg(provider_id),
    sqlc.narg(request_record_id),
    sqlc.narg(request_attempt_id),
    sqlc.narg(cost_snapshot_id),
    sqlc.narg(channel_id),
    sqlc.narg(request_id),
    sqlc.narg(channel_name),
    sqlc.narg(upstream_model),
    sqlc.narg(provider_probe_record_id),
    sqlc.narg(usage_source),
    sqlc.arg(entry_type),
    sqlc.arg(amount),
    sqlc.arg(currency),
    sqlc.arg(balance_before),
    sqlc.arg(balance_after),
    sqlc.arg(idempotency_key),
    sqlc.arg(reason)
)
RETURNING *;

-- name: GetProviderLedgerEntryByIdempotencyKey :one
SELECT *
FROM provider_ledger_entries
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetProviderLedgerEntryByCostSnapshotID :one
SELECT *
FROM provider_ledger_entries
WHERE cost_snapshot_id = sqlc.arg(cost_snapshot_id);

-- name: GetProviderLedgerEntryByProbeRecordID :one
SELECT *
FROM provider_ledger_entries
WHERE provider_probe_record_id = sqlc.arg(provider_probe_record_id);

-- name: GetProviderProbeRecordByIdempotencyKey :one
SELECT * FROM provider_probe_records
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: CreateProviderProbeRecord :one
INSERT INTO provider_probe_records (
    provider_id, channel_id, model_id, protocol, source, upstream_model,
    success, http_status, error_code, message, latency_ms, usage_source,
    usage_facts, usage_reliable, cost_amount, currency, formula_version, idempotency_key
)
VALUES (
    sqlc.arg(provider_id), sqlc.arg(channel_id), sqlc.narg(model_id), sqlc.arg(protocol), sqlc.arg(source),
    sqlc.arg(upstream_model), sqlc.arg(success), sqlc.arg(http_status), sqlc.narg(error_code), sqlc.narg(message),
    sqlc.narg(latency_ms), sqlc.narg(usage_source), sqlc.narg(usage_facts)::jsonb, sqlc.arg(usage_reliable),
    sqlc.narg(cost_amount), sqlc.narg(currency), sqlc.narg(formula_version), sqlc.arg(idempotency_key)
)
RETURNING *;

-- name: LockProviderLedgerIdempotencyKey :exec
SELECT pg_advisory_xact_lock(
    hashtext('provider_ledger_entries'),
    hashtext(sqlc.arg(idempotency_key)::text)
);
