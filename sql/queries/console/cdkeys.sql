-- Console redemption reads/writes are deliberately narrow; no plaintext is returned.

-- name: UpdateConsoleCDKeyRedeemed :execrows
-- Keep the Console state transition write-only. In particular, do not use the
-- shared Admin-shaped UPDATE ... RETURNING query here: that result includes
-- code_plaintext and would unnecessarily bring the redeemable credential into
-- the Console process.
UPDATE cdkeys
SET status = 'redeemed', redeemed_at = sqlc.arg(redeemed_at), revoked_at = NULL
WHERE id = sqlc.arg(id) AND status = 'unused';

-- name: GetConsoleCDKeyRedemption :one
SELECT r.id, r.cdkey_id, r.user_id, r.amount, r.currency, r.ledger_entry_id,
       r.idempotency_key, r.redeemed_at, l.balance_after
FROM cdkey_redemptions r
JOIN ledger_entries l ON l.id = r.ledger_entry_id
WHERE r.cdkey_id = sqlc.arg(cdkey_id);
