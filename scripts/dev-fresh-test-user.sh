#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$API_ROOT/.env"
SQL_FILE="$SCRIPT_DIR/dev-fresh-test-user.sql"
GEN_KEY="$SCRIPT_DIR/dev-gen-api-key/main.go"

usage() {
  cat <<'EOF'
Usage:
  scripts/dev-fresh-test-user.sh

开发库重置（保留模型/渠道/供应商/线路，新建测试身份）：
  - 保留：providers、channels、models、channel_models、channel_prices、
          routes、route_channels、model_capabilities、model_catalog*、capability_keys
  - 清空：请求/用量/账本、旧 users/projects/api_keys
  - 新建：test@unio.local、测试项目、API Key（$100 余额），并绑定首条线路
  - 同步 OPENAI_API_KEY → ~/.codex/auth.json（Codex 用，不写 unio-api/.env）
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

if [[ -z "${DATABASE_URL:-}" ]]; then
  echo "DATABASE_URL is not set in $ENV_FILE" >&2
  exit 1
fi

sync_codex_auth_key() {
  local key_plain="$1"
  local codex_home="${CODEX_HOME:-$HOME/.codex}"
  local auth_file="$codex_home/auth.json"

  python3 - "$key_plain" "$auth_file" <<'PY'
import json
import sys
from pathlib import Path

key, auth_path = sys.argv[1:3]
auth = Path(auth_path)
auth.parent.mkdir(parents=True, exist_ok=True)
payload = {}
if auth.is_file():
    payload = json.loads(auth.read_text())
payload["OPENAI_API_KEY"] = key
payload["auth_mode"] = payload.get("auth_mode", "apikey")
auth.write_text(json.dumps(payload, indent=2) + "\n")
PY

  echo "==> synced OPENAI_API_KEY to $auth_file"
}

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

echo "==> generating api key"
IFS=$'\t' read -r KEY_PLAIN KEY_PREFIX KEY_HASH < <(go run "$GEN_KEY")

ROUTE_ID="$(run_psql -t -A -c "SELECT id FROM routes ORDER BY id LIMIT 1")"
if [[ -z "$ROUTE_ID" ]]; then
  echo "No route found; create a route in Admin first." >&2
  exit 1
fi

echo "==> dev fresh test user: api root $API_ROOT"
echo "==> applying dev-fresh-test-user.sql"
run_psql -v ON_ERROR_STOP=1 -f - < "$SQL_FILE"

echo "==> creating api key"
run_psql -v ON_ERROR_STOP=1 -c "
INSERT INTO api_keys (project_id, name, key_prefix, key_hash, route_id)
VALUES (1, '测试 Key', '${KEY_PREFIX}', '${KEY_HASH}', ${ROUTE_ID});
"

if command -v redis-cli >/dev/null 2>&1 && [[ -n "${REDIS_ADDR:-}" ]]; then
  echo "==> flushing redis ($REDIS_ADDR)"
  redis-cli -u "$REDIS_ADDR" FLUSHDB >/dev/null
elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx unio-redis; then
  echo "==> flushing redis (unio-redis)"
  docker exec unio-redis redis-cli FLUSHDB >/dev/null
fi

sync_codex_auth_key "$KEY_PLAIN"

echo ""
echo "==> 测试身份已就绪"
echo "用户:     test@unio.local (测试用户)"
echo "项目:     测试项目 (id=1)"
echo "线路:     route_id=${ROUTE_ID}"
echo "余额:     USD 100.00"
echo ""
echo "API Key（仅显示一次，请保存）:"
echo "${KEY_PLAIN}"
echo ""
echo "Gateway:  http://127.0.0.1:${GATEWAY_HTTP_ADDR#:}"
echo ""
echo "==> post-init summary"
run_psql -c "
SELECT 'users' AS item, COUNT(*)::text AS count FROM users
UNION ALL SELECT 'projects', COUNT(*)::text FROM projects
UNION ALL SELECT 'api_keys', COUNT(*)::text FROM api_keys
UNION ALL SELECT 'providers', COUNT(*)::text FROM providers
UNION ALL SELECT 'channels', COUNT(*)::text FROM channels
UNION ALL SELECT 'models', COUNT(*)::text FROM models
UNION ALL SELECT 'routes', COUNT(*)::text FROM routes
UNION ALL SELECT 'request_records', COUNT(*)::text FROM request_records
ORDER BY item;
SELECT u.email, p.name AS project, ak.key_prefix, ak.route_id, ub.balance
FROM users u
JOIN projects p ON p.user_id = u.id
JOIN api_keys ak ON ak.project_id = p.id
JOIN user_balances ub ON ub.user_id = u.id;
"
