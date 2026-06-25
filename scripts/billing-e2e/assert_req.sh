#!/usr/bin/env bash
# assert_req.sh <request_id|LATEST> [user_id]
# 打印某条请求的全账务链路 + 交付状态，供 E2E 逐条对账。仅本地测试用。
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

RID_IN="${1:-LATEST}"
USER_ID="${2:-}"
PSQL=(docker compose -f docker-compose.yml exec -T postgres psql -U unio -d unio -v ON_ERROR_STOP=1)

if [[ "$RID_IN" == "LATEST" ]]; then
  RID=$("${PSQL[@]}" -t -A -c "SELECT request_id FROM request_records ORDER BY id DESC LIMIT 1;" | tr -d '[:space:]')
else
  RID="$RID_IN"
fi
echo "================ request_id = $RID ================"

"${PSQL[@]}" <<SQL
\set rid '$RID'
\echo '--- request_records ---'
SELECT request_id, status, stream, operation, requested_model_id,
       delivery_status,
       (response_started_at IS NOT NULL) AS has_started,
       (response_completed_at IS NOT NULL) AS has_completed,
       error_code
FROM request_records WHERE request_id = :'rid';

\echo '--- request_attempts ---'
SELECT attempt_index, status, final_usage_received AS final_usage,
       upstream_finish_reason, finish_class, upstream_status_code AS http
FROM request_attempts
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid')
ORDER BY attempt_index;

\echo '--- usage_records ---'
SELECT usage_source, uncached_input_tokens AS in_tok, cache_read_input_tokens AS cached_tok,
       output_tokens_total AS out_tok, reasoning_output_tokens AS reason_tok
FROM usage_records
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid');

\echo '--- ledger_reservations ---'
SELECT status, estimated_amount AS est, authorized_amount AS auth,
       captured_amount AS captured, released_amount AS released
FROM ledger_reservations
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid');

\echo '--- ledger_entries ---'
SELECT entry_type, amount, balance_before, balance_after, reason
FROM ledger_entries
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid')
ORDER BY id;

\echo '--- ledger_billing_exceptions (expect none for A/B/C/D) ---'
SELECT event_type, reason_code, actual_amount, captured_amount, platform_amount
FROM ledger_billing_exceptions
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid');

\echo '--- price/cost snapshot presence ---'
SELECT
  (SELECT count(*) FROM price_snapshots WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid')) AS price_snaps,
  (SELECT count(*) FROM cost_snapshots  WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid')) AS cost_snaps;

\echo '--- settlement_recovery_jobs ---'
SELECT status, attempt_count, max_attempts, usage_source, last_error_code
FROM settlement_recovery_jobs
WHERE request_record_id = (SELECT id FROM request_records WHERE request_id = :'rid');
SQL

if [[ -n "$USER_ID" ]]; then
  echo "--- user_balances (user $USER_ID) ---"
  "${PSQL[@]}" -c "SELECT balance, reserved_balance FROM user_balances WHERE user_id = $USER_ID AND currency='USD';"
fi
