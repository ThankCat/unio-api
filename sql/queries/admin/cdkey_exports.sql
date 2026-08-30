-- Export is the only Admin query that intentionally selects code_plaintext.
-- The HTTP layer streams this result after AdminAuth; it never wraps it in JSON.

-- name: ExportCDKeysByIDs :many
SELECT
    c.id,
    c.batch_id,
    c.code_plaintext,
    c.amount,
    c.currency,
    c.status,
    c.created_at,
    c.redeemed_at,
    c.revoked_at,
    r.user_id AS redemption_user_id,
    u.email AS redemption_user_email,
    r.ledger_entry_id AS redemption_ledger_entry_id,
    r.redeemed_at AS redemption_redeemed_at
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE c.id = ANY(sqlc.arg(ids)::bigint[])
  AND c.status = ANY(sqlc.arg(statuses)::text[])
  AND NOT (c.id = ANY(sqlc.arg(exclude_ids)::bigint[]))
ORDER BY c.id;

-- name: ExportCDKeysByFilter :many
SELECT
    c.id,
    c.batch_id,
    c.code_plaintext,
    c.amount,
    c.currency,
    c.status,
    c.created_at,
    c.redeemed_at,
    c.revoked_at,
    r.user_id AS redemption_user_id,
    u.email AS redemption_user_email,
    r.ledger_entry_id AS redemption_ledger_entry_id,
    r.redeemed_at AS redemption_redeemed_at
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE c.status = ANY(sqlc.arg(statuses)::text[])
  AND NOT (c.id = ANY(sqlc.arg(exclude_ids)::bigint[]))
  AND (sqlc.narg(amount)::numeric IS NULL OR c.amount = sqlc.narg(amount)::numeric)
  AND (sqlc.narg(batch_id)::uuid IS NULL OR c.batch_id = sqlc.narg(batch_id)::uuid)
  AND (sqlc.narg(search)::text IS NULL OR (
      c.code_prefix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.code_suffix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.batch_id::text ILIKE '%' || sqlc.narg(search)::text || '%'
      OR COALESCE(u.email, '') ILIKE '%' || sqlc.narg(search)::text || '%'
      OR COALESCE(r.user_id::text, '') ILIKE '%' || sqlc.narg(search)::text || '%'
  ))
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR c.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR c.created_at < sqlc.narg(to_time)::timestamptz)
ORDER BY c.id;
