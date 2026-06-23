#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$API_ROOT/.env"
SQL_FILE="$SCRIPT_DIR/dev-reset-test-state.sql"

usage() {
  cat <<'EOF'
Usage:
  scripts/dev-reset-test-state.sh [--skip-sync]

本地测试态重置：
  - 清空请求/用量/快照/账本/校正观测与建议
  - 余额与 api_keys.spent_total 归零
  - 已采纳模型的 model_capabilities 恢复为 model_catalog（models.dev 粗能力）
  - 保留 channel_prices、渠道/模型配置与用户/API Key

Options:
  --skip-sync  跳过 sync-models（目录已是最新时用）
  -h, --help   显示帮助
EOF
}

skip_sync=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-sync) skip_sync=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi

cd "$API_ROOT"
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is not set in $ENV_FILE" >&2
  exit 1
fi

run_psql() {
  if command -v psql >/dev/null 2>&1; then
    psql "$DATABASE_URL" "$@"
    return
  fi
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx unio-postgres; then
    docker exec -i unio-postgres psql -U unio -d unio "$@"
    return
  fi
  echo "Neither psql nor unio-postgres container found." >&2
  exit 1
}

echo "==> dev reset: api root $API_ROOT"

if [[ "$skip_sync" == "false" ]]; then
  echo "==> refreshing models.dev catalog (sync-models)"
  go run ./cmd/worker-server sync-models
else
  echo "==> skipping sync-models"
fi

echo "==> applying dev-reset-test-state.sql"
run_psql -v ON_ERROR_STOP=1 -f - < "$SQL_FILE"

if command -v redis-cli >/dev/null 2>&1 && [[ -n "${REDIS_ADDR:-}" ]]; then
  echo "==> flushing redis ($REDIS_ADDR)"
  redis-cli -u "$REDIS_ADDR" FLUSHDB >/dev/null
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx unio-redis; then
  echo "==> flushing redis (unio-redis)"
  docker exec unio-redis redis-cli FLUSHDB >/dev/null
fi

echo "==> post-reset summary"
run_psql -c "
SELECT 'request_records' AS item, COUNT(*)::text AS count FROM request_records
UNION ALL SELECT 'ledger_entries', COUNT(*)::text FROM ledger_entries
UNION ALL SELECT 'model_capability_suggestions', COUNT(*)::text FROM model_capability_suggestions
UNION ALL SELECT 'model_capabilities', COUNT(*)::text FROM model_capabilities
UNION ALL SELECT 'channel_prices', COUNT(*)::text FROM channel_prices;
SELECT m.model_id, COUNT(mc.capability_key) AS cap_count
FROM models m
LEFT JOIN model_capabilities mc ON mc.model_id = m.id
GROUP BY m.id, m.model_id
ORDER BY m.model_id;
SELECT balance, reserved_balance FROM user_balances LIMIT 1;
"

echo "==> dev reset complete"
