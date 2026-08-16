#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BACKUP_DIR="${UNIO_DB_BACKUP_DIR:-$REPO_ROOT/tmp/db-snapshots}"
DB_PROFILE="dev"
COMPOSE_FILE=""
ENV_FILE=""
EXPECTED_COMPOSE_PROJECT=""
BACKUP_NAME_PREFIX=""
COMPOSE_ARGS=()
REQUESTED_FILE=""
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-}"
REDIS_CONTAINER="${REDIS_CONTAINER:-}"

usage() {
  cat <<'EOF'
备份 Unio Dev/Test PostgreSQL，或将备份恢复到本地 Dev 数据库。

用法：
  scripts/db_snapshot.sh backup [--profile dev|test] [备份文件]
  scripts/db_snapshot.sh verify [--profile dev|test] [备份文件]
  scripts/db_snapshot.sh restore [备份文件] --confirm-replace
  scripts/db_snapshot.sh list

典型流程：
  # 在测试站执行；只读取正在运行的 Test PostgreSQL，不启动或重启服务
  scripts/db_snapshot.sh backup --profile test

  # 将 .dump 和同名 .sha256 文件复制到本地后执行
  scripts/db_snapshot.sh verify /path/to/unio-test-YYYYmmdd-HHMMSS.dump
  scripts/db_snapshot.sh restore /path/to/unio-test-YYYYmmdd-HHMMSS.dump --confirm-replace

默认目录：
  tmp/db-snapshots/（已被 Git 忽略）

说明：
  - backup 使用 PostgreSQL custom format，并生成同名 .sha256 校验文件。
  - profile 默认为 dev；test 从 .env.test 读取数据库配置，并按 unio-test/postgres 标签查找容器。
  - Test backup 不停止 Gateway/Admin/Worker；pg_dump 会生成事务一致的在线备份。
  - verify 未指定文件时校验默认目录中最新的备份。
  - restore 固定使用 Dev profile，拒绝 Test 容器，会覆盖本地 Dev PostgreSQL 并清空本地 Dev Redis 当前 DB。
  - restore 前必须停止 Gateway、Admin 和 Worker；检测到活动连接时脚本会拒绝执行。
  - Test 备份包含完整业务数据，复制、保存和删除时应按敏感数据处理。
  - 快照包含 Schema；本地代码需与 Test Schema 兼容。不同凭据主密钥下的加密字段需在本地重新配置。

可选环境变量：
  UNIO_DB_BACKUP_DIR, UNIO_COMPOSE_FILE, UNIO_ENV_FILE,
  POSTGRES_CONTAINER, POSTGRES_USER, POSTGRES_DB, REDIS_CONTAINER, REDIS_DB
EOF
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

configure_profile() {
  case "$DB_PROFILE" in
    dev)
      COMPOSE_FILE="${UNIO_COMPOSE_FILE:-$REPO_ROOT/deploy/compose.dev.yml}"
      ENV_FILE="${UNIO_ENV_FILE:-$REPO_ROOT/deploy/env/.env.dev}"
      EXPECTED_COMPOSE_PROJECT="unio-dev"
      BACKUP_NAME_PREFIX="unio-dev"
      COMPOSE_ARGS=(--env-file "$ENV_FILE" -f "$COMPOSE_FILE")
      ;;
    test)
      COMPOSE_FILE="${UNIO_COMPOSE_FILE:-$REPO_ROOT/deploy/compose.test.yml}"
      ENV_FILE="${UNIO_ENV_FILE:-$REPO_ROOT/deploy/env/.env.test}"
      EXPECTED_COMPOSE_PROJECT="unio-test"
      BACKUP_NAME_PREFIX="unio-test"
      COMPOSE_ARGS=()
      ;;
    *)
      fail "profile 只能是 dev 或 test：$DB_PROFILE"
      ;;
  esac
}

configure_restore_profile() {
  if [[ -n "${UNIO_COMPOSE_FILE:-}" || -n "${UNIO_ENV_FILE:-}" ]]; then
    fail "restore 不允许覆盖 Compose 或环境文件；目标固定为仓库内的本地 Dev 配置"
  fi
  DB_PROFILE="dev"
  configure_profile
}

compose() {
  docker compose "${COMPOSE_ARGS[@]}" "$@"
}

compose_up() {
  compose up -d "$@" >/dev/null
}

compose_container() {
  local service="$1"
  compose ps -q "$service" | tail -n 1
}

running_profile_container() {
  local service="$1"
  docker ps \
    --filter "status=running" \
    --filter "label=com.docker.compose.project=$EXPECTED_COMPOSE_PROJECT" \
    --filter "label=com.docker.compose.service=$service" \
    --format '{{.ID}}' | tail -n 1
}

env_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1
}

load_env_defaults() {
  POSTGRES_USER="${POSTGRES_USER:-$(env_value POSTGRES_USER)}"
  POSTGRES_DB="${POSTGRES_DB:-$(env_value POSTGRES_DB)}"
  REDIS_PASSWORD="${REDIS_PASSWORD:-$(env_value REDIS_PASSWORD)}"
  REDIS_DB="${REDIS_DB:-$(env_value REDIS_DB)}"
}

container_compose_project() {
  local container="$1"
  docker inspect \
    --format '{{ index .Config.Labels "com.docker.compose.project" }}' \
    "$container"
}

assert_expected_container() {
  local service="$1"
  local container="$2"
  local actual_project
  actual_project="$(container_compose_project "$container")"
  [[ "$actual_project" == "$EXPECTED_COMPOSE_PROJECT" ]] || {
    fail "$service 容器 $container 属于 Compose 项目 ${actual_project:-<unknown>}，预期为 $EXPECTED_COMPOSE_PROJECT"
  }
}

redis_cli() {
  local args=(docker exec "$REDIS_CONTAINER" redis-cli --no-auth-warning)
  if [[ -n "$REDIS_PASSWORD" ]]; then
    args+=(-a "$REDIS_PASSWORD")
  fi
  "${args[@]}" -n "$REDIS_DB" "$@"
}

wait_for_postgres() {
  local attempt
  for attempt in {1..30}; do
    if docker exec "$POSTGRES_CONTAINER" pg_isready -U "$POSTGRES_USER" -d postgres >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "PostgreSQL 容器在 30 秒内没有就绪：$POSTGRES_CONTAINER"
}

wait_for_redis() {
  local attempt
  for attempt in {1..30}; do
    if redis_cli PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 1
  done
  fail "Redis 容器在 30 秒内没有就绪：$REDIS_CONTAINER"
}

ensure_postgres() {
  if [[ "$DB_PROFILE" == "dev" ]]; then
    compose_up postgres
  fi
  if [[ -z "$POSTGRES_CONTAINER" ]]; then
    if [[ "$DB_PROFILE" == "test" ]]; then
      POSTGRES_CONTAINER="$(running_profile_container postgres)"
    else
      POSTGRES_CONTAINER="$(compose_container postgres)"
    fi
  fi
  if [[ -z "$POSTGRES_CONTAINER" ]]; then
    if [[ "$DB_PROFILE" == "test" ]]; then
      fail "没有找到正在运行的 Test postgres 容器；Test backup 不会自动启动服务"
    fi
    fail "没有找到 Compose postgres 容器"
  fi
  assert_expected_container postgres "$POSTGRES_CONTAINER"
  wait_for_postgres
}

ensure_redis() {
  compose_up redis
  if [[ -z "$REDIS_CONTAINER" ]]; then
    REDIS_CONTAINER="$(compose_container redis)"
  fi
  [[ -n "$REDIS_CONTAINER" ]] || fail "没有找到 Compose redis 容器"
  assert_expected_container redis "$REDIS_CONTAINER"
  wait_for_redis
}

validate_config() {
  local require_redis="${1:-false}"
  if [[ "$DB_PROFILE" == "dev" ]]; then
    [[ -f "$COMPOSE_FILE" ]] || fail "Dev Compose 文件不存在：$COMPOSE_FILE"
  fi
  [[ -f "$ENV_FILE" ]] || fail "$DB_PROFILE 环境文件不存在：$ENV_FILE"
  load_env_defaults
  [[ "$POSTGRES_USER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_USER 不是安全的 PostgreSQL 标识符"
  [[ "$POSTGRES_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_DB 不是安全的 PostgreSQL 标识符"
  if [[ "$require_redis" == "true" ]]; then
    [[ "$REDIS_DB" =~ ^[0-9]+$ ]] || fail "REDIS_DB 必须是非负整数"
  fi
}

latest_backup() {
  local files=()
  local file latest=""
  shopt -s nullglob
  files=("$BACKUP_DIR"/unio-*.dump)
  shopt -u nullglob
  ((${#files[@]} > 0)) || fail "没有找到备份：$BACKUP_DIR/unio-*.dump"
  for file in "${files[@]}"; do
    if [[ -z "$latest" || "$file" -nt "$latest" ]]; then
      latest="$file"
    fi
  done
  printf '%s\n' "$latest"
}

resolve_backup_file() {
  local requested="${1:-}"
  if [[ -n "$requested" ]]; then
    [[ -f "$requested" ]] || fail "备份文件不存在：$requested"
    printf '%s\n' "$requested"
    return
  fi
  latest_backup
}

checksum_tool() {
  if command -v shasum >/dev/null 2>&1; then
    printf '%s\n' shasum
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s\n' sha256sum
    return
  fi
  fail "缺少 SHA-256 工具：需要 shasum 或 sha256sum"
}

write_checksum() {
  local backup_file="$1"
  local backup_dir backup_name checksum_temp tool
  backup_dir="$(cd "$(dirname "$backup_file")" && pwd)"
  backup_name="$(basename "$backup_file")"
  checksum_temp="$(mktemp "$backup_dir/.unio-db-checksum.XXXXXX")"
  tool="$(checksum_tool)"
  if [[ "$tool" == "shasum" ]]; then
    (cd "$backup_dir" && LC_ALL=C LANG=C shasum -a 256 "$backup_name" > "$checksum_temp")
  else
    (cd "$backup_dir" && LC_ALL=C LANG=C sha256sum "$backup_name" > "$checksum_temp")
  fi
  if [[ ! -s "$checksum_temp" ]]; then
    rm -f "$checksum_temp"
    fail "SHA-256 校验文件生成失败"
  fi
  mv "$checksum_temp" "$backup_file.sha256"
}

verify_checksum_if_present() {
  local backup_file="$1"
  local checksum_file="$backup_file.sha256"
  local backup_dir checksum_name tool
  if [[ ! -f "$checksum_file" ]]; then
    printf '警告：未找到 SHA-256 校验文件，仅检查 PostgreSQL 归档结构：%s\n' "$checksum_file" >&2
    return 0
  fi
  [[ -s "$checksum_file" ]] || fail "SHA-256 校验文件为空：$checksum_file"
  backup_dir="$(cd "$(dirname "$backup_file")" && pwd)"
  checksum_name="$(basename "$checksum_file")"
  tool="$(checksum_tool)"
  if [[ "$tool" == "shasum" ]]; then
    (cd "$backup_dir" && LC_ALL=C LANG=C shasum -a 256 -c "$checksum_name")
  else
    (cd "$backup_dir" && LC_ALL=C LANG=C sha256sum -c "$checksum_name")
  fi
}

verify_archive() {
  local backup_file="$1"
  verify_checksum_if_present "$backup_file"
  docker exec -i "$POSTGRES_CONTAINER" pg_restore --list < "$backup_file" >/dev/null
}

backup_database() {
  local output_file="${1:-}"
  local temp_file

  ensure_postgres
  mkdir -p "$BACKUP_DIR"
  umask 077

  if [[ -z "$output_file" ]]; then
    output_file="$BACKUP_DIR/$BACKUP_NAME_PREFIX-$(date '+%Y%m%d-%H%M%S').dump"
  elif [[ "$output_file" != /* ]]; then
    output_file="$PWD/$output_file"
  fi
  mkdir -p "$(dirname "$output_file")"
  [[ ! -e "$output_file" ]] || fail "目标文件已存在，不会覆盖：$output_file"

  temp_file="$(mktemp "$(dirname "$output_file")/.unio-db-backup.XXXXXX")"
  trap 'rm -f "$temp_file"' EXIT

  printf '正在备份 %s PostgreSQL：%s/%s\n' "$DB_PROFILE" "$POSTGRES_CONTAINER" "$POSTGRES_DB"
  docker exec "$POSTGRES_CONTAINER" pg_dump \
    -U "$POSTGRES_USER" \
    -d "$POSTGRES_DB" \
    --format=custom \
    --compress=9 \
    --no-owner \
    --no-acl > "$temp_file"

  [[ -s "$temp_file" ]] || fail "pg_dump 没有生成有效内容"
  docker exec -i "$POSTGRES_CONTAINER" pg_restore --list < "$temp_file" >/dev/null
  mv "$temp_file" "$output_file"
  trap - EXIT
  write_checksum "$output_file"

  printf '备份完成：%s\n' "$output_file"
  printf '校验文件：%s.sha256\n' "$output_file"
  du -h "$output_file" | awk '{print "文件大小：" $1}'
}

verify_backup() {
  local backup_file
  backup_file="$(resolve_backup_file "${1:-}")"
  ensure_postgres
  verify_archive "$backup_file"
  printf '备份校验通过：%s\n' "$backup_file"
}

active_database_connections() {
  docker exec "$POSTGRES_CONTAINER" psql \
    -U "$POSTGRES_USER" \
    -d postgres \
    -At \
    -v ON_ERROR_STOP=1 \
    -c "SELECT count(*) FROM pg_stat_activity WHERE datname = '$POSTGRES_DB';"
}

restore_database() {
  local backup_file="$1"
  local confirmed="$2"
  local connection_count restore_db restored_size

  [[ "$DB_PROFILE" == "dev" ]] || fail "restore 只允许使用 Dev profile"
  [[ "$confirmed" == "true" ]] || fail "restore 必须添加 --confirm-replace，确认覆盖本地开发库"
  ensure_postgres
  verify_archive "$backup_file"

  connection_count="$(active_database_connections)"
  [[ "$connection_count" =~ ^[0-9]+$ ]] || fail "无法读取数据库活动连接数：$connection_count"
  if ((connection_count > 0)); then
    fail "数据库 $POSTGRES_DB 仍有 $connection_count 个活动连接。请停止 Gateway、Admin 和 Worker 后重试"
  fi

  ensure_redis

  restore_db="${POSTGRES_DB}_restore_$(date '+%s')_$$"
  cleanup_restore_db() {
    if [[ -n "${restore_db:-}" ]]; then
      docker exec "$POSTGRES_CONTAINER" psql \
        -U "$POSTGRES_USER" \
        -d postgres \
        -v ON_ERROR_STOP=1 \
        -c "DROP DATABASE IF EXISTS \"$restore_db\" WITH (FORCE);" >/dev/null 2>&1 || true
    fi
  }
  trap cleanup_restore_db EXIT

  printf '正在恢复到临时 PostgreSQL 数据库：%s\n' "$restore_db"
  docker exec "$POSTGRES_CONTAINER" psql \
    -U "$POSTGRES_USER" \
    -d postgres \
    -v ON_ERROR_STOP=1 \
    -c "CREATE DATABASE \"$restore_db\" OWNER \"$POSTGRES_USER\";" >/dev/null

  docker exec -i "$POSTGRES_CONTAINER" pg_restore \
    -U "$POSTGRES_USER" \
    -d "$restore_db" \
    --no-owner \
    --no-privileges \
    --single-transaction \
    --exit-on-error < "$backup_file"

  restored_size="$(docker exec "$POSTGRES_CONTAINER" psql \
    -U "$POSTGRES_USER" \
    -d "$restore_db" \
    -At \
    -v ON_ERROR_STOP=1 \
    -c 'SELECT pg_size_pretty(pg_database_size(current_database()));')"

  printf '临时数据库恢复成功，正在替换：%s\n' "$POSTGRES_DB"
  docker exec "$POSTGRES_CONTAINER" psql \
    -U "$POSTGRES_USER" \
    -d postgres \
    -v ON_ERROR_STOP=1 \
    -c "DROP DATABASE IF EXISTS \"$POSTGRES_DB\";" \
    -c "ALTER DATABASE \"$restore_db\" RENAME TO \"$POSTGRES_DB\";" >/dev/null
  restore_db=""
  trap - EXIT

  printf '正在清空 Redis DB %s 的旧运行态\n' "$REDIS_DB"
  redis_cli FLUSHDB >/dev/null

  printf '恢复完成：%s\n' "$backup_file"
  printf '恢复后数据库大小：%s\n' "$restored_size"
  printf 'Redis DB %s 已清空，请再启动 Gateway、Admin 和 Worker。\n' "$REDIS_DB"
}

list_backups() {
  local files=()
  shopt -s nullglob
  files=("$BACKUP_DIR"/unio-*.dump)
  shopt -u nullglob
  if ((${#files[@]} == 0)); then
    printf '没有备份：%s\n' "$BACKUP_DIR"
    return
  fi
  printf '备份目录：%s\n' "$BACKUP_DIR"
  du -h "${files[@]}"
}

parse_profile_file_args() {
  local action="$1"
  shift
  DB_PROFILE="dev"
  REQUESTED_FILE=""
  while (($# > 0)); do
    case "$1" in
      --profile)
        shift
        (($# > 0)) || fail "--profile 后必须指定 dev 或 test"
        DB_PROFILE="$1"
        ;;
      --profile=*)
        DB_PROFILE="${1#--profile=}"
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      --*)
        fail "未知参数：$1"
        ;;
      *)
        [[ -z "$REQUESTED_FILE" ]] || fail "$action 只能指定一个备份文件"
        REQUESTED_FILE="$1"
        ;;
    esac
    shift
  done
  [[ "$DB_PROFILE" == "dev" || "$DB_PROFILE" == "test" ]] || fail "profile 只能是 dev 或 test：$DB_PROFILE"
}

parse_restore_args() {
  local backup_file=""
  local confirmed="false"
  shift
  while (($# > 0)); do
    case "$1" in
      --confirm-replace)
        confirmed="true"
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      --*)
        fail "未知参数：$1"
        ;;
      *)
        [[ -z "$backup_file" ]] || fail "restore 只能指定一个备份文件"
        backup_file="$1"
        ;;
    esac
    shift
  done
  backup_file="$(resolve_backup_file "$backup_file")"
  restore_database "$backup_file" "$confirmed"
}

main() {
  local command="${1:-help}"
  case "$command" in
    backup)
      parse_profile_file_args backup "${@:2}"
      configure_profile
      validate_config false
      require_command docker
      backup_database "$REQUESTED_FILE"
      ;;
    verify)
      parse_profile_file_args verify "${@:2}"
      configure_profile
      validate_config false
      require_command docker
      verify_backup "$REQUESTED_FILE"
      ;;
    restore)
      configure_restore_profile
      validate_config true
      require_command docker
      parse_restore_args "$@"
      ;;
    list)
      (($# == 1)) || fail "list 不接受额外参数"
      list_backups
      ;;
    help|-h|--help)
      usage
      ;;
    *)
      fail "未知命令：$command（使用 --help 查看用法）"
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
