-- CDKEY core queries shared by admin generation and console redemption.

-- name: CreateCDKey :one
INSERT INTO cdkeys (
    batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
    amount, currency, status
)
VALUES (
    sqlc.arg(batch_id), sqlc.arg(code_plaintext), sqlc.arg(code_hash),
    sqlc.arg(code_prefix), sqlc.arg(code_suffix), sqlc.arg(amount),
    sqlc.arg(currency), sqlc.arg(status)
)
RETURNING id, batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
          amount, currency, status, created_at, redeemed_at, revoked_at;

-- name: GetCDKeyByHashForUpdate :one
SELECT id, batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
       amount, currency, status, created_at, redeemed_at, revoked_at
FROM cdkeys
WHERE code_hash = sqlc.arg(code_hash)
FOR UPDATE;

-- name: GetCDKeyForRedemptionByHashForUpdate :one
-- Console redemption never needs the plaintext or display fragments. Keep this
-- query separate from the Admin lock/export queries so the sensitive column is
-- not read into the Console service process.
SELECT id, batch_id, code_hash, amount, currency, status,
       created_at, redeemed_at, revoked_at
FROM cdkeys
WHERE code_hash = sqlc.arg(code_hash)
FOR UPDATE;

-- name: GetCDKeyByIDForUpdate :one
SELECT id, batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
       amount, currency, status, created_at, redeemed_at, revoked_at
FROM cdkeys
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: GetCDKeysForUpdateByIDs :many
SELECT id, batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
       amount, currency, status, created_at, redeemed_at, revoked_at
FROM cdkeys
WHERE id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id
FOR UPDATE;

-- name: UpdateCDKeyRedeemed :one
UPDATE cdkeys
SET status = 'redeemed', redeemed_at = sqlc.arg(redeemed_at), revoked_at = NULL
WHERE id = sqlc.arg(id) AND status = 'unused'
RETURNING id, batch_id, code_plaintext, code_hash, code_prefix, code_suffix,
          amount, currency, status, created_at, redeemed_at, revoked_at;

-- name: CreateCDKeyRedemption :one
INSERT INTO cdkey_redemptions (
    cdkey_id, user_id, amount, currency, ledger_entry_id, idempotency_key, redeemed_at
)
VALUES (
    sqlc.arg(cdkey_id), sqlc.arg(user_id), sqlc.arg(amount), sqlc.arg(currency),
    sqlc.arg(ledger_entry_id), sqlc.arg(idempotency_key), sqlc.arg(redeemed_at)
)
RETURNING id, cdkey_id, user_id, amount, currency, ledger_entry_id, idempotency_key, redeemed_at;

-- name: GetCDKeyRedemptionByCDKeyID :one
SELECT r.id, r.cdkey_id, r.user_id, r.amount, r.currency, r.ledger_entry_id,
       r.idempotency_key, r.redeemed_at,
       l.balance_after
FROM cdkey_redemptions r
JOIN ledger_entries l ON l.id = r.ledger_entry_id
WHERE r.cdkey_id = sqlc.arg(cdkey_id);

-- name: GetCDKeyRedemptionByIdempotencyKey :one
SELECT id, cdkey_id, user_id, amount, currency, ledger_entry_id, idempotency_key, redeemed_at
FROM cdkey_redemptions
WHERE idempotency_key = sqlc.arg(idempotency_key);
