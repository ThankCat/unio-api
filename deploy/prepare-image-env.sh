#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0" >&2
}

fail() {
  echo "error: $*" >&2
  exit 1
}

if [[ $# -ne 0 ]]; then
  usage
  fail "this script does not accept arguments; use it from the develop or main branch"
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null)" || fail "not inside a Git repository"
env_file="$repo_root/deploy/env/.env.docker"

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

revision="$(git -C "$repo_root" rev-parse HEAD)"
tags="$(git -C "$repo_root" tag --points-at "$revision" --sort=-version:refname)"
tag_count="$(printf '%s\n' "$tags" | awk 'NF { count++ } END { print count + 0 }')"

if [[ "$tag_count" -eq 0 ]]; then
  fail "HEAD $revision has no Git tag"
fi
if [[ "$tag_count" -ne 1 ]]; then
  fail "HEAD $revision must have exactly one Git tag"
fi

git_tag="$(printf '%s\n' "$tags" | awk 'NF { print; exit }')"
if [[ ! "$git_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  fail "Git tag $git_tag is not a valid Docker image tag"
fi

[[ -f "$env_file" ]] || fail "missing $env_file"
for required_key in IMAGE_TAG IMAGE_VERSION IMAGE_REVISION IMAGE_CREATED; do
  grep -q "^${required_key}=" "$env_file" || fail "missing $required_key in $env_file"
done

created="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
temp_file="$(mktemp "${env_file}.tmp.XXXXXX")"
trap 'rm -f "$temp_file"' EXIT

awk \
  -v image_tag="$git_tag" \
  -v image_version="$git_tag" \
  -v image_revision="$revision" \
  -v image_created="$created" '
    /^IMAGE_TAG=/ {
      print "IMAGE_TAG=" image_tag
      next
    }
    /^IMAGE_VERSION=/ {
      print "IMAGE_VERSION=" image_version
      next
    }
    /^IMAGE_REVISION=/ {
      print "IMAGE_REVISION=" image_revision
      next
    }
    /^IMAGE_CREATED=/ {
      print "IMAGE_CREATED=" image_created
      next
    }
    { print }
  ' "$env_file" >"$temp_file"

chmod 600 "$temp_file"
mv "$temp_file" "$env_file"
trap - EXIT

printf 'Updated %s\n' "$env_file"
printf '  environment: %s\n' "$environment_name"
printf '  branch:      %s\n' "$current_branch"
printf '  tag/version: %s\n' "$git_tag"
printf '  revision:    %s\n' "$revision"
printf '  created:     %s\n' "$created"
