#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$API_ROOT/.env"

usage() {
  cat <<'EOF'
Usage:
  scripts/calibrate-capabilities.sh [--dry-run|--apply]

Options:
  --dry-run   Preview the calibration plan without writing to the database. Default.
  --apply     Run calibration and write allowed changes/suggestions to the database.
  -h, --help  Show this help.
EOF
}

mode="dry-run"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      mode="dry-run"
      shift
      ;;
    --apply)
      mode="apply"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing env file: $ENV_FILE" >&2
  echo "Create it from .env.example and fill required database/config values first." >&2
  exit 1
fi

cd "$API_ROOT"

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

args=(calibrate-capabilities)
if [[ "$mode" == "dry-run" ]]; then
  args+=(--dry-run)
fi

echo "==> capability calibration mode: $mode"
echo "==> api root: $API_ROOT"

exec go run ./cmd/worker-server "${args[@]}"
