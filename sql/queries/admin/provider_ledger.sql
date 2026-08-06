-- name: GetProviderBalance :one
SELECT id, provider_id, currency, balance, created_at, updated_at
FROM provider_balances
WHERE provider_id = sqlc.arg(provider_id)
  AND currency = sqlc.arg(currency);

-- name: ListProviderLedgerEntriesPage :many
SELECT
    e.id,
    e.provider_id,
    e.request_record_id,
    e.request_attempt_id,
    e.cost_snapshot_id,
    e.channel_id,
    e.request_id,
    e.channel_name,
    e.upstream_model,
    e.provider_probe_record_id,
    probe.channel_id AS probe_channel_id,
    probe_channel.name AS probe_channel_name,
    probe.upstream_model AS probe_upstream_model,
    e.entry_type,
    e.amount,
    e.currency,
    e.balance_before,
    e.balance_after,
    e.idempotency_key,
    e.reason,
    e.created_at
FROM provider_ledger_entries e
LEFT JOIN provider_probe_records probe ON probe.id = e.provider_probe_record_id
LEFT JOIN channels probe_channel ON probe_channel.id = probe.channel_id
WHERE e.provider_id = sqlc.arg(provider_id)
  AND (sqlc.narg(entry_type)::text IS NULL OR e.entry_type = sqlc.narg(entry_type)::text)
  AND (sqlc.narg(request_id)::text IS NULL OR e.request_id = sqlc.narg(request_id)::text)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR e.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR e.created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountProviderLedgerEntries :one
SELECT COUNT(*) AS total
FROM provider_ledger_entries e
WHERE e.provider_id = sqlc.arg(provider_id)
  AND (sqlc.narg(entry_type)::text IS NULL OR e.entry_type = sqlc.narg(entry_type)::text)
  AND (sqlc.narg(request_id)::text IS NULL OR e.request_id = sqlc.narg(request_id)::text)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR e.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR e.created_at < sqlc.narg(to_time)::timestamptz);

-- name: ListProviderCostRisksPage :many
SELECT
    r.id,
    r.provider_id,
    r.request_record_id,
    r.request_attempt_id,
    r.provider_probe_record_id,
    r.source_type,
    r.estimated_amount,
    r.currency,
    r.reason_code,
    r.reason,
    r.status,
    r.reconciliation_ledger_entry_id,
    r.created_at,
    r.reconciled_at,
    rr.request_id,
    rpr.upstream_model AS probe_upstream_model,
    ra.upstream_model AS request_upstream_model,
    ch.name AS channel_name
FROM provider_cost_risks r
LEFT JOIN request_records rr ON rr.id = r.request_record_id
LEFT JOIN request_attempts ra ON ra.id = r.request_attempt_id AND ra.request_record_id = r.request_record_id
LEFT JOIN provider_probe_records rpr ON rpr.id = r.provider_probe_record_id
LEFT JOIN channels ch ON ch.id = COALESCE(rpr.channel_id, ra.channel_id)
WHERE r.provider_id = sqlc.arg(provider_id)
  AND (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status)::text)
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountProviderCostRisks :one
SELECT COUNT(*) AS total
FROM provider_cost_risks r
WHERE r.provider_id = sqlc.arg(provider_id)
  AND (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status)::text);

-- name: GetProviderCostRiskSummary :one
SELECT
    COUNT(*) AS unresolved_count,
    COALESCE(SUM(estimated_amount) FILTER (WHERE currency = 'USD'), 0)::numeric AS estimated_amount_usd,
    COUNT(*) FILTER (WHERE estimated_amount IS NULL) AS unknown_amount_count
FROM provider_cost_risks
WHERE provider_id = sqlc.arg(provider_id)
  AND status = 'unresolved';

-- name: CountLowBalanceProviders :one
SELECT COUNT(*) AS total
FROM providers p
JOIN provider_balances pb ON pb.provider_id = p.id AND pb.currency = 'USD'
WHERE p.status <> 'archived'
  AND pb.balance < 10;
