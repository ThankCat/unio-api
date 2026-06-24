#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$API_ROOT/.env"

usage() {
  cat <<'EOF'
Usage:
  scripts/dev-seed.sh

开发库 seed（幂等，不含 provider/channel）：
  - 模型 gpt-5.5 / gpt-5.4 / gpt-5.4-mini（能力按 OpenAI 官方文档声明）
  - 测试用户 test@unio.local / 测试项目 / API Key / 经济线路
  - flush Redis

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

echo "==> dev seed: api root $API_ROOT"
go run ./scripts/dev-seed/main.go

if command -v redis-cli >/dev/null 2>&1 && [[ -n "${REDIS_ADDR:-}" ]]; then
  echo "==> flushing redis ($REDIS_ADDR)"
  redis-cli -u "$REDIS_ADDR" FLUSHDB >/dev/null
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx unio-redis; then
  echo "==> flushing redis (unio-redis)"
  docker exec unio-redis redis-cli FLUSHDB >/dev/null
fi

echo "==> post-seed summary"
if command -v psql >/dev/null 2>&1; then
  psql "$DATABASE_URL" -c "
SELECT 'users' AS item, COUNT(*)::text AS count FROM users
UNION ALL SELECT 'projects', COUNT(*)::text FROM projects
UNION ALL SELECT 'api_keys', COUNT(*)::text FROM api_keys
UNION ALL SELECT 'providers', COUNT(*)::text FROM providers
UNION ALL SELECT 'channels', COUNT(*)::text FROM channels
UNION ALL SELECT 'models', COUNT(*)::text FROM models
UNION ALL SELECT 'routes', COUNT(*)::text FROM routes
UNION ALL SELECT 'channel_prices', COUNT(*)::text FROM channel_prices
ORDER BY item;
SELECT m.model_id, COUNT(mc.capability_key) AS caps
FROM models m LEFT JOIN model_capabilities mc ON mc.model_id = m.id
GROUP BY m.id, m.model_id ORDER BY m.model_id;
"
fi
