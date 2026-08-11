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
	e.usage_source,
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

-- name: CountLowBalanceProviders :one
SELECT COUNT(*) AS total
FROM providers p
JOIN provider_balances pb ON pb.provider_id = p.id AND pb.currency = 'USD'
WHERE p.status <> 'archived'
  AND pb.balance < 10;
