# CDKEYS implementation plan

## Scope

- Add the `cdkeys` and `cdkey_redemptions` schema and generated sqlc access.
- Add fixed-amount, cryptographically random key generation with plaintext retained only for authenticated export and a hash for redemption lookup.
- Add atomic admin generation/list/summary/revoke/delete/bulk operations and status-filtered CSV export.
- Add atomic, idempotent console redemption and the independent `cdkey_credit` ledger entry type.
- Wire admin and console routes/bootstrap dependencies and focused tests.

## Invariants

- Amounts are exactly 5, 10, 30, 50, 100, 200, or 500 USD; quantity is 1..1000.
- Normal JSON responses never contain plaintext key material. Export is an authenticated CSV stream only.
- A redeemed key cannot be deleted or revoked. Bulk delete is all-or-nothing when any selected key is redeemed.
- A redemption creates one ledger entry and one redemption row while changing the key state in one transaction; retries are idempotent.

## Verification

- Run `sqlc generate`, `gofmt`, focused package tests, `go test ./...` where feasible, and `git diff --check`.
- Do not modify generated sqlc files by hand; do not alter unrelated worktree changes.
