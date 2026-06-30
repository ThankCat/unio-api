#!/usr/bin/env bash
# seed-test-data.sh — 初始化本地开发测试数据，并打印一把可用的 API Key。
#
# 用法：
#   bash scripts/seed-test-data.sh            # 仅写入种子数据（幂等，可重复）
#   bash scripts/seed-test-data.sh --reset    # 先 migrate drop+up 重置干净库，再写入种子
#
# 读取 DATABASE_URL：优先 .env，缺省回退本地默认 DSN。
# 渠道 base_url / API key 为占位，需在 Admin 后台改成真实值后才能打真实上游。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
SQL_FILE="${SCRIPT_DIR}/seed-test-data.sql"

RESET=0
for arg in "$@"; do
    case "$arg" in
        --reset) RESET=1 ;;
        *) echo "未知参数：$arg" >&2; exit 2 ;;
    esac
done

# --- 解析 DATABASE_URL ---------------------------------------------------------
if [[ -f "${ROOT_DIR}/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "${ROOT_DIR}/.env"
    set +a
fi
DATABASE_URL="${DATABASE_URL:-postgres://unio:unio_dev_password@127.0.0.1:5432/unio?sslmode=disable}"

# --- psql 执行方式：优先宿主机 psql，否则走 docker compose 容器内 psql ---------
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
PG_SERVICE="postgres"
PG_USER_IN_CONTAINER="unio"
PG_DB_IN_CONTAINER="unio"

if command -v psql >/dev/null 2>&1; then
    USE_DOCKER=0
else
    PG_RUNNING=""
    if command -v docker >/dev/null 2>&1; then
        PG_RUNNING="$(docker compose -f "${COMPOSE_FILE}" ps --status running --format '{{.Service}}' 2>/dev/null || true)"
    fi
    if printf '%s\n' "${PG_RUNNING}" | grep -qx "${PG_SERVICE}"; then
        USE_DOCKER=1
        echo "==> 宿主机无 psql，改用容器内 psql（docker compose exec ${PG_SERVICE}）"
    else
        echo "缺少 psql：宿主机未安装，且 ${PG_SERVICE} 容器未运行" >&2
        exit 1
    fi
fi

# pg：统一的 psql 执行入口，SQL 从 stdin 读取（同时兼容宿主机 / 容器）。
pg() {
    if [[ "${USE_DOCKER}" == "1" ]]; then
        docker compose -f "${COMPOSE_FILE}" exec -T "${PG_SERVICE}" \
            psql -U "${PG_USER_IN_CONTAINER}" -d "${PG_DB_IN_CONTAINER}" "$@"
    else
        psql "${DATABASE_URL}" "$@"
    fi
}

if [[ "$RESET" == "1" ]]; then
    command -v migrate >/dev/null 2>&1 || { echo "缺少 golang-migrate（migrate），--reset 需要它" >&2; exit 1; }
fi

# --- 可选：重置数据库（drop 全部表 + 重新 up）---------------------------------
if [[ "$RESET" == "1" ]]; then
    echo "==> 重置数据库（migrate drop -f && migrate up）"
    migrate -path "${ROOT_DIR}/migrations" -database "${DATABASE_URL}" drop -f
    migrate -path "${ROOT_DIR}/migrations" -database "${DATABASE_URL}" up
fi

# --- 生成 API Key（格式与 internal/core/apikey 一致：unio_sk_<43 base62>）-------
# 子 shell 内关闭 pipefail：tr 读 /dev/urandom 被 head 提前关闭管道会收到 SIGPIPE。
RANDOM_PART="$(set +o pipefail; LC_ALL=C tr -dc '0-9A-Za-z' < /dev/urandom | head -c 43)"
API_KEY="unio_sk_${RANDOM_PART}"
KEY_PREFIX="${API_KEY:0:16}"                                   # unio_sk_ + 前 8 位
KEY_HASH="$(printf '%s' "${API_KEY}" | shasum -a 256 | awk '{print $1}')"

# --- 写入种子数据 --------------------------------------------------------------
echo "==> 写入测试种子数据"
pg --quiet --no-psqlrc \
    -v ON_ERROR_STOP=1 \
    -v key_prefix="${KEY_PREFIX}" \
    -v key_hash="${KEY_HASH}" \
    -v key_plaintext="${API_KEY}" \
    < "${SQL_FILE}"

# --- 概览 ----------------------------------------------------------------------
echo "==> 已写入概览"
pg --quiet --no-psqlrc -P pager=off <<'SQL'
SELECT 'provider' AS kind, slug AS name, status FROM providers WHERE slug='openai'
UNION ALL SELECT 'channel', name, status FROM channels WHERE name='OpenAI 官方渠道'
UNION ALL SELECT 'model', model_id, status FROM models WHERE model_id IN ('gpt-5.5','gpt-5.4','gpt-5.4-mini')
UNION ALL SELECT 'route', name, status FROM routes WHERE name = 'Dev Cheapest'
UNION ALL SELECT 'user', email, '' FROM users WHERE lower(email)=lower('dev@unio.local')
ORDER BY kind, name;

-- DEC-026：渠道只录成本（channel_prices）；模型基准售价在 model_prices，客户售价 = 基准 × 线路倍率。
SELECT m.model_id,
       cp.currency,
       cp.uncached_input_cost AS in_cost,
       cp.output_cost AS out_cost,
       mp.uncached_input_price AS in_base,
       mp.output_price AS out_base,
       (cp.effective_to IS NULL) AS cost_never_expires
FROM channel_prices cp
JOIN models m ON m.id = cp.model_id
JOIN channels c ON c.id = cp.channel_id
LEFT JOIN model_prices mp ON mp.model_id = m.id AND mp.status = 'enabled'
WHERE c.name='OpenAI 官方渠道'
ORDER BY m.model_id;

SELECT u.email, ub.currency, ub.balance,
       kr.name AS key_route,
       ak.name AS api_key_name, ak.key_prefix
FROM users u
JOIN user_balances ub ON ub.user_id=u.id
JOIN api_keys ak ON ak.user_id=u.id AND ak.name='seed test key'
LEFT JOIN routes kr ON kr.id=ak.route_id
WHERE lower(u.email)=lower('dev@unio.local');
SQL

# --- 输出明文 Key（只在此处可见一次）-----------------------------------------
cat <<EOF

============================================================
  测试 API Key（已明文留存，可在 Admin 多次复制查看）：

      ${API_KEY}

  调用示例（先在 Admin 把渠道 base_url / api key 改成真实值）：
      curl http://localhost:8521/v1/chat/completions \\
        -H "Authorization: Bearer ${API_KEY}" \\
        -H "Content-Type: application/json" \\
        -d '{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}'
============================================================
EOF
