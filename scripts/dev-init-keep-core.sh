#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$API_ROOT/.env"
SQL_FILE="$SCRIPT_DIR/dev-init-keep-core.sql"

usage() {
  cat <<'EOF'
Usage:
  scripts/dev-init-keep-core.sh

开发库初始化（保留核心身份与渠道配置）：
  - 保留：users、projects、api_keys、providers、channels
  - 保留：capability_keys（能力字典 seed）
  - 清空：models、model_catalog*、routes、channel_models、channel_prices、
          请求/用量/账本/补偿、model_capabilities、project_model_policies
  - 余额与 api_keys.spent_total 归零；相关自增序列复位
  - 可选：flush Redis 缓存

Options:
  -h, --help   显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
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

echo "==> dev init (keep core): api root $API_ROOT"
echo "==> applying dev-init-keep-core.sql"
run_psql -v ON_ERROR_STOP=1 -f - < "$SQL_FILE"

if command -v redis-cli >/dev/null 2>&1 && [[ -n "${REDIS_ADDR:-}" ]]; then
  echo "==> flushing redis ($REDIS_ADDR)"
  redis-cli -u "$REDIS_ADDR" FLUSHDB >/dev/null
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx unio-redis; then
  echo "==> flushing redis (unio-redis)"
  docker exec unio-redis redis-cli FLUSHDB >/dev/null
fi

echo "==> post-init summary"
run_psql -c "
SELECT 'users' AS item, COUNT(*)::text AS count FROM users
UNION ALL SELECT 'projects', COUNT(*)::text FROM projects
UNION ALL SELECT 'api_keys', COUNT(*)::text FROM api_keys
UNION ALL SELECT 'providers', COUNT(*)::text FROM providers
UNION ALL SELECT 'channels', COUNT(*)::text FROM channels
UNION ALL SELECT 'models', COUNT(*)::text FROM models
UNION ALL SELECT 'routes', COUNT(*)::text FROM routes
UNION ALL SELECT 'model_catalog', COUNT(*)::text FROM model_catalog
UNION ALL SELECT 'request_records', COUNT(*)::text FROM request_records
UNION ALL SELECT 'ledger_entries', COUNT(*)::text FROM ledger_entries
UNION ALL SELECT 'capability_keys', COUNT(*)::text FROM capability_keys
ORDER BY item;
SELECT balance, reserved_balance FROM user_balances LIMIT 3;
"

echo "==> dev init complete (models/routes/catalog cleared; create them in Admin)"
