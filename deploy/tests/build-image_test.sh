#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_line() {
  local expected="$1"
  local file="$2"
  grep -Fqx "$expected" "$file" || fail "missing '$expected' in $file"
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source_script="$script_dir/../build-image.sh"
source_compose="$script_dir/../compose.test.yml"
temp_root="$(mktemp -d "${TMPDIR:-/tmp}/unio-build-image-test.XXXXXX")"
trap 'rm -rf "$temp_root"' EXIT

repo="$temp_root/repo"
fake_bin="$temp_root/bin"
fake_state="$temp_root/docker-state"
fake_log="$temp_root/docker-log"
mkdir -p "$repo/deploy/env" "$repo/deploy/tests" "$fake_bin"
cp "$source_script" "$repo/deploy/build-image.sh"
cp "$source_compose" "$repo/deploy/compose.test.yml"

cat >"$repo/.gitignore" <<'EOF'
/deploy/env/.env.*
!/deploy/env/.env.*.example
EOF

cat >"$repo/deploy/env/.env.docker" <<'EOF'
GATEWAY_IMAGE_REPOSITORY=unio-gateway
GATEWAY_IMAGE_TAG=<current-gateway-tag>
ADMIN_IMAGE_REPOSITORY=unio-admin
ADMIN_IMAGE_TAG=0.0.3
WORKER_IMAGE_REPOSITORY=unio-worker
WORKER_IMAGE_TAG=<current-worker-tag>
MIGRATION_IMAGE_REPOSITORY=unio-migration
MIGRATION_IMAGE_TAG=<current-migration-tag>
GO_BUILD_IMAGE=golang:test
APP_RUNTIME_IMAGE=alpine:test
MIGRATE_TOOL_IMAGE=migrate:test
EOF

cat >"$repo/deploy/env/.env.test" <<'EOF'
COMPOSE_PROJECT_NAME=unio_test
EOF

cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

echo "$*" >>"$FAKE_DOCKER_LOG"

if [[ "$1" == "compose" ]]; then
  [[ "${FAKE_DOCKER_FAIL_BUILD:-0}" != "1" ]] || exit 42
  printf '%s|%s|%s\n' "$IMAGE_VERSION" "$IMAGE_REVISION" "$IMAGE_CREATED" >"$FAKE_DOCKER_STATE"
  printf '%s\n' "${ADMIN_IMAGE_TAG:-}" >"${FAKE_DOCKER_STATE}.admin-tag"
  printf '%s\n' "${GATEWAY_IMAGE_TAG:-}" >"${FAKE_DOCKER_STATE}.gateway-tag"
  exit 0
fi

if [[ "$1" == "image" && "$2" == "inspect" ]]; then
  cat "$FAKE_DOCKER_STATE"
  exit 0
fi

exit 1
EOF
chmod +x "$fake_bin/docker" "$repo/deploy/build-image.sh"

git -C "$repo" init -b develop >/dev/null
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.com
git -C "$repo" add .gitignore deploy/build-image.sh deploy/compose.test.yml
git -C "$repo" commit -m fixture >/dev/null

PATH="$fake_bin:$PATH" \
FAKE_DOCKER_STATE="$fake_state" \
FAKE_DOCKER_LOG="$fake_log" \
  "$repo/deploy/build-image.sh" admin 0.0.4 >/dev/null

env_file="$repo/deploy/env/.env.docker"
assert_line "GATEWAY_IMAGE_TAG=<current-gateway-tag>" "$env_file"
assert_line "ADMIN_IMAGE_TAG=0.0.4" "$env_file"
assert_line "WORKER_IMAGE_TAG=<current-worker-tag>" "$env_file"
assert_line "MIGRATION_IMAGE_TAG=<current-migration-tag>" "$env_file"
assert_line "0.0.4" "${fake_state}.admin-tag"
assert_line "unconfigured" "${fake_state}.gateway-tag"
grep -Fq "build admin" "$fake_log" || fail "admin build was not selected"
grep -Fq "image inspect" "$fake_log" || fail "built image metadata was not inspected"
grep -Fq "unio-admin:0.0.4" "$fake_log" || fail "unexpected image reference"

revision="$(git -C "$repo" rev-parse HEAD)"
IFS='|' read -r built_version built_revision built_created <"$fake_state"
[[ "$built_version" == "0.0.4" ]] || fail "unexpected build version $built_version"
[[ "$built_revision" == "$revision" ]] || fail "unexpected build revision $built_revision"
[[ "$built_created" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || fail "unexpected build time $built_created"

if PATH="$fake_bin:$PATH" \
  FAKE_DOCKER_STATE="$fake_state" \
  FAKE_DOCKER_LOG="$fake_log" \
  FAKE_DOCKER_FAIL_BUILD=1 \
  "$repo/deploy/build-image.sh" gateway 0.0.3 >/dev/null 2>&1; then
  fail "failed build unexpectedly succeeded"
fi
assert_line "GATEWAY_IMAGE_TAG=<current-gateway-tag>" "$env_file"

if PATH="$fake_bin:$PATH" \
  FAKE_DOCKER_STATE="$fake_state" \
  FAKE_DOCKER_LOG="$fake_log" \
  "$repo/deploy/build-image.sh" unknown 0.0.1 >/dev/null 2>&1; then
  fail "unsupported service unexpectedly succeeded"
fi

echo "build-image tests passed"
