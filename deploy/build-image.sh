#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: ./deploy/build-image.sh <gateway|admin|worker|migration> <image-tag>
EOF
}

fail() {
  echo "error: $*" >&2
  exit 1
}

if [[ $# -ne 2 ]]; then
  usage
  fail "service and image tag are required"
fi

service="$1"
image_tag="$2"

case "$service" in
  gateway)
    env_prefix="GATEWAY"
    compose_service="gateway"
    ;;
  admin)
    env_prefix="ADMIN"
    compose_service="admin"
    ;;
  worker)
    env_prefix="WORKER"
    compose_service="worker"
    ;;
  migration | migrate)
    service="migration"
    env_prefix="MIGRATION"
    compose_service="migrate"
    ;;
  *)
    usage
    fail "unsupported service $service"
    ;;
esac

if [[ ! "$image_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  fail "image tag $image_tag is not a valid Docker image tag"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null)" || fail "not inside a Git repository"
env_file="$repo_root/deploy/env/.env.docker"
runtime_env_file="$repo_root/deploy/env/.env.test"
compose_file="$repo_root/deploy/compose.test.yml"

current_branch="$(git -C "$repo_root" symbolic-ref --quiet --short HEAD 2>/dev/null)" || fail "detached HEAD is not allowed"
case "$current_branch" in
  develop)
    environment_name="test"
    ;;
  main)
    environment_name="prod"
    ;;
  *)
    fail "current branch $current_branch is not supported; use develop for test or main for prod"
    ;;
esac

if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=normal)" ]]; then
  fail "working tree must be clean"
fi

[[ -f "$env_file" ]] || fail "missing $env_file"
[[ -f "$runtime_env_file" ]] || fail "missing $runtime_env_file"
[[ -f "$compose_file" ]] || fail "missing $compose_file"
command -v docker >/dev/null 2>&1 || fail "docker is required"

required_keys="
GATEWAY_IMAGE_REPOSITORY
GATEWAY_IMAGE_TAG
ADMIN_IMAGE_REPOSITORY
ADMIN_IMAGE_TAG
WORKER_IMAGE_REPOSITORY
WORKER_IMAGE_TAG
MIGRATION_IMAGE_REPOSITORY
MIGRATION_IMAGE_TAG
GO_BUILD_IMAGE
APP_RUNTIME_IMAGE
MIGRATE_TOOL_IMAGE
"

while IFS= read -r required_key; do
  [[ -n "$required_key" ]] || continue
  key_count="$(grep -c "^${required_key}=" "$env_file" || true)"
  [[ "$key_count" -eq 1 ]] || fail "$env_file must contain exactly one $required_key"
  required_value="$(sed -n "s/^${required_key}=//p" "$env_file")"
  [[ -n "$required_value" ]] || fail "$required_key must not be empty"
  if [[ "$required_key" == *_IMAGE_TAG && "$required_value" =~ ^\<[^\<\>]+\>$ ]]; then
    export "$required_key=unconfigured"
  else
    export "$required_key=$required_value"
  fi
done <<<"$required_keys"

repository_key="${env_prefix}_IMAGE_REPOSITORY"
tag_key="${env_prefix}_IMAGE_TAG"
repository="$(sed -n "s/^${repository_key}=//p" "$env_file")"
current_tag="$(sed -n "s/^${tag_key}=//p" "$env_file")"

[[ -n "$repository" ]] || fail "$repository_key must not be empty"
[[ -n "$current_tag" ]] || fail "$tag_key must not be empty"
[[ "$current_tag" != "$image_tag" ]] || fail "$service already uses image tag $image_tag; choose a new tag"

revision="$(git -C "$repo_root" rev-parse HEAD)"
created="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
image_ref="${repository}:${image_tag}"

export "$tag_key=$image_tag"
export IMAGE_VERSION="$image_tag"
export IMAGE_REVISION="$revision"
export IMAGE_CREATED="$created"

docker compose \
  --env-file "$env_file" \
  --env-file "$runtime_env_file" \
  -f "$compose_file" \
  build "$compose_service"

actual_metadata="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}|{{index .Config.Labels "org.opencontainers.image.revision"}}|{{index .Config.Labels "org.opencontainers.image.created"}}' "$image_ref")" || fail "cannot inspect built image $image_ref"
expected_metadata="${image_tag}|${revision}|${created}"
[[ "$actual_metadata" == "$expected_metadata" ]] || fail "built image metadata mismatch: expected $expected_metadata, got $actual_metadata"

temp_file="$(mktemp "${env_file}.tmp.XXXXXX")"
trap 'rm -f "$temp_file"' EXIT

awk -v key="$tag_key" -v value="$image_tag" '
  index($0, key "=") == 1 {
    print key "=" value
    next
  }
  { print }
' "$env_file" >"$temp_file"

chmod 600 "$temp_file"
mv "$temp_file" "$env_file"
trap - EXIT

printf 'Built %s\n' "$image_ref"
printf '  environment: %s\n' "$environment_name"
printf '  service:     %s\n' "$service"
printf '  version:     %s\n' "$image_tag"
printf '  revision:    %s\n' "$revision"
printf '  created:     %s\n' "$created"
printf 'Updated %s: %s=%s\n' "$env_file" "$tag_key" "$image_tag"
