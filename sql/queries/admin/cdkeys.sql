-- Admin CDKEY list, summary and state mutation queries. Plaintext is intentionally absent.

-- name: ListCDKeyIDs :many
-- Resolve a server-side selection without loading plaintext or an unbounded page.
SELECT c.id
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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

-- name: ListCDKeysPage :many
SELECT
    c.id,
    c.batch_id,
    c.code_prefix,
    c.code_suffix,
    c.amount,
    c.currency,
    c.status,
    c.created_at,
    c.redeemed_at,
    c.revoked_at,
    r.id AS redemption_id,
    r.user_id AS redemption_user_id,
    u.email AS redemption_user_email,
    u.display_name AS redemption_user_display_name,
    r.ledger_entry_id AS redemption_ledger_entry_id,
    r.redeemed_at AS redemption_redeemed_at,
    COUNT(*) OVER () AS total_count
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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
ORDER BY
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN c.created_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN c.created_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'amount' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN c.amount END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'amount' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN c.amount END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'status' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN c.status END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'status' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN c.status END ASC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN c.id END DESC,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'created_at') IN ('', 'created_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN c.id END ASC,
  c.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountCDKeys :one
SELECT COUNT(*) AS total
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR c.created_at < sqlc.narg(to_time)::timestamptz);

-- name: ListCDKeyIDsByFilter :many
-- Resolve select-all mutations on the server; no plaintext is selected.
SELECT c.id
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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

-- name: GetCDKeySummary :many
SELECT c.amount,
       c.status,
       COUNT(*)::bigint AS quantity,
       COALESCE(SUM(c.amount), 0)::numeric AS total_value,
       COUNT(DISTINCT c.batch_id)::bigint AS batch_count
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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
GROUP BY c.amount, c.status
ORDER BY c.amount, c.status;

-- name: CountCDKeyBatches :one
SELECT COUNT(DISTINCT c.batch_id)::bigint AS total
FROM cdkeys c
LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
LEFT JOIN users u ON u.id = r.user_id
WHERE c.status <> 'revoked'
  AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR c.created_at < sqlc.narg(to_time)::timestamptz);

-- name: GetCDKeyBatchSummary :many
-- Batch-level card details.  A batch is considered "fully redeemed" only
-- when every non-revoked key in the batch is redeemed.  The row filters are
-- applied before grouping so the detail remains consistent with the page
-- filters, while revoked keys never make a batch look partially redeemed.
WITH filtered AS (
    SELECT c.batch_id, c.amount, c.status
    FROM cdkeys c
    LEFT JOIN cdkey_redemptions r ON r.cdkey_id = c.id
    LEFT JOIN users u ON u.id = r.user_id
    WHERE c.status <> 'revoked'
      AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR c.status = ANY(sqlc.arg(statuses)::text[]))
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
), batches AS (
    SELECT batch_id,
           amount,
           COUNT(*)::bigint AS key_count,
           COUNT(*) FILTER (WHERE status = 'unused')::bigint AS unused_count,
           COUNT(*) FILTER (WHERE status = 'redeemed')::bigint AS redeemed_count
    FROM filtered
    GROUP BY batch_id, amount
)
SELECT amount,
       COUNT(*)::bigint AS batch_count,
       COUNT(*) FILTER (WHERE unused_count > 0)::bigint AS batches_with_unused,
       COUNT(*) FILTER (WHERE key_count > 0 AND redeemed_count = key_count)::bigint AS fully_redeemed_batch_count
FROM batches
GROUP BY amount
ORDER BY amount;

-- name: ListCDKeyRedemptionsPage :many
SELECT
    r.id,
    r.cdkey_id,
    c.batch_id,
    c.code_prefix,
    c.code_suffix,
    r.user_id,
    u.email AS user_email,
    u.display_name AS user_display_name,
    r.amount,
    r.currency,
    r.ledger_entry_id,
    r.idempotency_key,
    r.redeemed_at,
    COUNT(*) OVER () AS total_count
FROM cdkey_redemptions r
JOIN cdkeys c ON c.id = r.cdkey_id
JOIN users u ON u.id = r.user_id
WHERE (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id)::bigint)
  AND (sqlc.narg(search)::text IS NULL OR (
      c.code_prefix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.code_suffix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.batch_id::text ILIKE '%' || sqlc.narg(search)::text || '%'
      OR u.email ILIKE '%' || sqlc.narg(search)::text || '%'
      OR r.user_id::text ILIKE '%' || sqlc.narg(search)::text || '%'
  ))
  AND (sqlc.narg(amount)::numeric IS NULL OR r.amount = sqlc.narg(amount)::numeric)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.redeemed_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.redeemed_at < sqlc.narg(to_time)::timestamptz)
ORDER BY
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'redeemed_at') IN ('', 'redeemed_at') AND COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.redeemed_at END DESC NULLS LAST,
  CASE WHEN COALESCE(sqlc.narg(sort_field)::text, 'redeemed_at') IN ('', 'redeemed_at') AND NOT COALESCE(sqlc.narg(sort_desc)::bool, true) THEN r.redeemed_at END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'amount' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.amount END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'amount' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.amount END ASC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'user_id' AND COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.user_id END DESC NULLS LAST,
  CASE WHEN sqlc.narg(sort_field)::text = 'user_id' AND NOT COALESCE(sqlc.narg(sort_desc)::bool, false) THEN r.user_id END ASC NULLS LAST,
  r.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountCDKeyRedemptions :one
SELECT COUNT(*) AS total
FROM cdkey_redemptions r
JOIN cdkeys c ON c.id = r.cdkey_id
JOIN users u ON u.id = r.user_id
WHERE (sqlc.narg(user_id)::bigint IS NULL OR r.user_id = sqlc.narg(user_id)::bigint)
  AND (sqlc.narg(search)::text IS NULL OR (
      c.code_prefix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.code_suffix ILIKE '%' || sqlc.narg(search)::text || '%'
      OR c.batch_id::text ILIKE '%' || sqlc.narg(search)::text || '%'
      OR u.email ILIKE '%' || sqlc.narg(search)::text || '%'
      OR r.user_id::text ILIKE '%' || sqlc.narg(search)::text || '%'
  ))
  AND (sqlc.narg(amount)::numeric IS NULL OR r.amount = sqlc.narg(amount)::numeric)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR r.redeemed_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR r.redeemed_at < sqlc.narg(to_time)::timestamptz);

-- name: RevokeCDKeyIfUnused :execrows
UPDATE cdkeys
SET status = 'revoked', revoked_at = now()
WHERE id = sqlc.arg(id) AND status = 'unused';

-- name: DeleteCDKey :execrows
DELETE FROM cdkeys
WHERE id = sqlc.arg(id);
