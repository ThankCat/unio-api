#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BACKUP_DIR="${UNIO_DB_BACKUP_DIR:-$REPO_ROOT/tmp/db-snapshots}"
COMPOSE_FILE="${UNIO_COMPOSE_FILE:-$REPO_ROOT/docker-compose.yml}"
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-unio-postgres}"
POSTGRES_USER="${POSTGRES_USER:-unio}"
POSTGRES_DB="${POSTGRES_DB:-unio}"
REDIS_CONTAINER="${REDIS_CONTAINER:-unio-redis}"
REDIS_DB="${REDIS_DB:-0}"

usage() {
  cat <<'EOF'
备份或恢复 Unio 本地开发数据库。

用法：
  scripts/dev_db_snapshot.sh backup [备份文件]
  scripts/dev_db_snapshot.sh verify [备份文件]
  scripts/dev_db_snapshot.sh restore [备份文件] --confirm-replace
  scripts/dev_db_snapshot.sh list

默认目录：
  tmp/db-snapshots/（已被 Git 忽略）

说明：
  - backup 使用 PostgreSQL custom format，并生成同名 .sha256 校验文件。
  - verify 未指定文件时校验默认目录中最新的备份。
  - restore 会覆盖本地 unio 数据库，并清空 Redis 当前 DB 的运行态。
  - restore 前必须停止 Gateway、Admin 和 Worker；检测到活动连接时脚本会拒绝执行。

可选环境变量：
  UNIO_DB_BACKUP_DIR, UNIO_COMPOSE_FILE,
  POSTGRES_CONTAINER, POSTGRES_USER, POSTGRES_DB,
  REDIS_CONTAINER, REDIS_DB
EOF
}

fail() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

compose_up() {
  docker compose -f "$COMPOSE_FILE" up -d "$@" >/dev/null
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
    if docker exec "$REDIS_CONTAINER" redis-cli -n "$REDIS_DB" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 1
  done
  fail "Redis 容器在 30 秒内没有就绪：$REDIS_CONTAINER"
}

ensure_postgres() {
  compose_up postgres
  wait_for_postgres
}

validate_config() {
  [[ "$POSTGRES_USER" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_USER 不是安全的 PostgreSQL 标识符"
  [[ "$POSTGRES_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_DB 不是安全的 PostgreSQL 标识符"
  [[ "$REDIS_DB" =~ ^[0-9]+$ ]] || fail "REDIS_DB 必须是非负整数"
}

latest_backup() {
  local files=()
  shopt -s nullglob
  files=("$BACKUP_DIR"/unio-dev-*.dump)
  shopt -u nullglob
  ((${#files[@]} > 0)) || fail "没有找到备份：$BACKUP_DIR/unio-dev-*.dump"
  printf '%s\n' "${files[${#files[@]} - 1]}"
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
    output_file="$BACKUP_DIR/unio-dev-$(date '+%Y%m%d-%H%M%S').dump"
  elif [[ "$output_file" != /* ]]; then
    output_file="$PWD/$output_file"
  fi
  mkdir -p "$(dirname "$output_file")"
  [[ ! -e "$output_file" ]] || fail "目标文件已存在，不会覆盖：$output_file"

  temp_file="$(mktemp "$(dirname "$output_file")/.unio-db-backup.XXXXXX")"
  trap 'rm -f "$temp_file"' EXIT

  printf '正在备份 PostgreSQL：%s/%s\n' "$POSTGRES_CONTAINER" "$POSTGRES_DB"
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

  [[ "$confirmed" == "true" ]] || fail "restore 必须添加 --confirm-replace，确认覆盖本地开发库"
  ensure_postgres
  verify_archive "$backup_file"

  connection_count="$(active_database_connections)"
  [[ "$connection_count" =~ ^[0-9]+$ ]] || fail "无法读取数据库活动连接数：$connection_count"
  if ((connection_count > 0)); then
    fail "数据库 $POSTGRES_DB 仍有 $connection_count 个活动连接。请停止 Gateway、Admin 和 Worker 后重试"
  fi

  compose_up redis
  wait_for_redis

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
  docker exec "$REDIS_CONTAINER" redis-cli -n "$REDIS_DB" FLUSHDB >/dev/null

  printf '恢复完成：%s\n' "$backup_file"
  printf '恢复后数据库大小：%s\n' "$restored_size"
  printf 'Redis DB %s 已清空，请再启动 Gateway、Admin 和 Worker。\n' "$REDIS_DB"
}

list_backups() {
  local files=()
  shopt -s nullglob
  files=("$BACKUP_DIR"/unio-dev-*.dump)
  shopt -u nullglob
  if ((${#files[@]} == 0)); then
    printf '没有备份：%s\n' "$BACKUP_DIR"
    return
  fi
  printf '备份目录：%s\n' "$BACKUP_DIR"
  du -h "${files[@]}"
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
  require_command docker
  validate_config
  local command="${1:-help}"
  case "$command" in
    backup)
      (($# <= 2)) || fail "backup 只能指定一个输出文件"
      backup_database "${2:-}"
      ;;
    verify)
      (($# <= 2)) || fail "verify 只能指定一个备份文件"
      verify_backup "${2:-}"
      ;;
    restore)
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

main "$@"
