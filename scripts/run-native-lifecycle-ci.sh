#!/usr/bin/env bash
set -euo pipefail

build="${1:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
evidence="${2:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
runner_label="${NATIVE_CI_RUNNER_LABEL:?NATIVE_CI_RUNNER_LABEL is required}"
runner_id="${NATIVE_CI_RUNNER_ID:?NATIVE_CI_RUNNER_ID is required}"
expected_sha="${NATIVE_CI_EXPECTED_SHA:?NATIVE_CI_EXPECTED_SHA is required}"
expected_release_run_id="${NATIVE_CI_RELEASE_RUN_ID:?NATIVE_CI_RELEASE_RUN_ID is required}"
workflow_run_id="${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
workflow_run_attempt="${GITHUB_RUN_ATTEMPT:?GITHUB_RUN_ATTEMPT is required}"
extension="$(go env GOEXE)"
binding="$build/release-binding.txt"
script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

read_binding() {
  local key="$1"
  local value
  value="$(awk -F= -v wanted="$key" '
    $1 == wanted { count++; value = substr($0, length(wanted) + 2) }
    END { if (count != 1 || value == "") exit 1; print value }
  ' "$binding")" || {
    echo "release binding is missing exact field $key" >&2
    exit 1
  }
  printf '%s' "$value"
}

test -f "$binding"
test ! -L "$binding"
expected_binding_keys=(
  schema
  release_repository
  release_repository_id
  release_run_id
  release_run_attempt
  release_workflow
  release_workflow_path
  release_event
  release_conclusion
  release_tag
  release_head_sha
  release_artifact_id
  release_artifact_name
  release_artifact_size
  release_artifact_api_digest
  release_artifact_zip_sha256
  release_manifest_sha256
  release_archive
  release_archive_sha256
  release_version
  release_goos
  release_goarch
  release_go_version
  release_kaiten
  release_kaiten_sha256
  release_kaiten_mcp
  release_kaiten_mcp_sha256
)
binding_index=0
while IFS= read -r binding_line; do
  test "$binding_index" -lt "${#expected_binding_keys[@]}"
  binding_key="${binding_line%%=*}"
  binding_value="${binding_line#*=}"
  test "$binding_line" = "$binding_key=$binding_value"
  test "$binding_key" = "${expected_binding_keys[$binding_index]}"
  test -n "$binding_value"
  [[ "$binding_value" != *"="* ]]
  binding_index=$((binding_index + 1))
done <"$binding"
test "$binding_index" -eq "${#expected_binding_keys[@]}"
test "$(read_binding schema)" = "kaiten-native-release-binding/v1"
release_repository="$(read_binding release_repository)"
release_repository_id="$(read_binding release_repository_id)"
release_run_id="$(read_binding release_run_id)"
release_run_attempt="$(read_binding release_run_attempt)"
release_workflow="$(read_binding release_workflow)"
release_workflow_path="$(read_binding release_workflow_path)"
release_event="$(read_binding release_event)"
release_conclusion="$(read_binding release_conclusion)"
release_tag="$(read_binding release_tag)"
release_head_sha="$(read_binding release_head_sha)"
release_manifest_sha256="$(read_binding release_manifest_sha256)"
release_archive="$(read_binding release_archive)"
release_archive_sha256="$(read_binding release_archive_sha256)"
release_version="$(read_binding release_version)"
release_goos="$(read_binding release_goos)"
release_goarch="$(read_binding release_goarch)"
release_go_version="$(read_binding release_go_version)"
release_artifact_id="$(read_binding release_artifact_id)"
release_artifact_name="$(read_binding release_artifact_name)"
release_artifact_size="$(read_binding release_artifact_size)"
release_artifact_api_digest="$(read_binding release_artifact_api_digest)"
release_artifact_zip_sha256="$(read_binding release_artifact_zip_sha256)"
release_kaiten="$(read_binding release_kaiten)"
release_kaiten_sha256="$(read_binding release_kaiten_sha256)"
release_kaiten_mcp="$(read_binding release_kaiten_mcp)"
release_kaiten_mcp_sha256="$(read_binding release_kaiten_mcp_sha256)"

[[ "$expected_sha" =~ ^[0-9a-f]{40}$ ]]
[[ "$expected_release_run_id" =~ ^[1-9][0-9]*$ ]]
[[ "$workflow_run_id" =~ ^[1-9][0-9]*$ ]]
[[ "$workflow_run_attempt" =~ ^[1-9][0-9]*$ ]]
[[ "$release_repository_id" =~ ^[1-9][0-9]*$ ]]
[[ "$release_run_attempt" =~ ^[1-9][0-9]*$ ]]
[[ "$release_tag" =~ ^v[0-9][0-9A-Za-z.+-]*$ ]]
[[ "$release_version" =~ ^[0-9][0-9A-Za-z.+-]*$ ]]
[[ "$release_archive" =~ ^kaiten_[0-9A-Za-z.+-]+_(darwin|linux|windows)_(amd64|arm64)(\.tar\.gz|\.zip)$ ]]
[[ "$release_goos" =~ ^(darwin|linux|windows)$ ]]
[[ "$release_goarch" =~ ^(amd64|arm64)$ ]]
[[ "$release_artifact_id" =~ ^[1-9][0-9]*$ ]]
[[ "$release_artifact_size" =~ ^[1-9][0-9]*$ ]]
[[ "$release_artifact_api_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
[[ "$release_head_sha" =~ ^[0-9a-f]{40}$ ]]
for digest in "$release_manifest_sha256" "$release_archive_sha256" "$release_artifact_zip_sha256" "$release_kaiten_sha256" "$release_kaiten_mcp_sha256"; do
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]]
done
test "$release_head_sha" = "$expected_sha"
test "$release_run_id" = "$expected_release_run_id"
test "$release_run_attempt" = "1"
test "$release_repository" = "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
test "${GITHUB_SHA:?GITHUB_SHA is required}" = "$expected_sha"
test "${GITHUB_REF:?GITHUB_REF is required}" = "refs/tags/$release_tag"
test "${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}" = "$release_tag"
test "$release_workflow" = "Release"
if [[ "$release_workflow_path" != ".github/workflows/release.yml" && "$release_workflow_path" != ".github/workflows/release.yml@$release_tag" && "$release_workflow_path" != ".github/workflows/release.yml@refs/tags/$release_tag" ]]; then
  echo "release binding has an unexpected workflow path" >&2
  exit 1
fi
test "$release_event" = "push"
test "$release_conclusion" = "success"
test "$release_tag" = "v$release_version"
test "$release_artifact_name" = "release-assets"
test "$release_artifact_api_digest" = "sha256:$release_artifact_zip_sha256"
test "$release_goos" = "$(go env GOOS)"
test "$release_goarch" = "$(go env GOARCH)"
test "$release_go_version" = "$(go env GOVERSION)"
test "$release_kaiten" = "kaiten$extension"
test "$release_kaiten_mcp" = "kaiten-mcp$extension"
commit="$release_head_sha"
if [[ -e "$evidence" || -L "$evidence" ]]; then
  echo "refusing to reuse native lifecycle evidence destination $evidence" >&2
  exit 1
fi
mkdir "$evidence"
if [[ "${RUNNER_OS:-}" != "Windows" ]]; then
  chmod 0700 "$evidence"
fi
{
  echo "runner_label=$runner_label"
  echo "runner_id=$runner_id"
  echo "runner_os=${RUNNER_OS:-local}"
  echo "candidate_commit=$commit"
  echo "workflow_run_id=$workflow_run_id"
  echo "workflow_run_attempt=$workflow_run_attempt"
  while IFS= read -r line; do
    printf 'binding_%s\n' "$line"
  done <"$binding"
} >"$evidence/wrapper-context.txt"
chmod 0600 "$evidence/wrapper-context.txt"

run_harness() {
  local harness="$1"
  local fixture_root="$2"
  local profile="$3"
  local evidence_root="$4"
  "$harness" \
    --v1 "$fixture_root/v1/kaiten-mcp$extension" \
    --v2 "$fixture_root/release/kaiten-mcp$extension" \
    --v3 "$fixture_root/v3/kaiten-mcp$extension" \
    --release-kaiten "$fixture_root/release/kaiten$extension" \
    --v2-version "$release_version" \
    --release-run-id "$release_run_id" \
    --release-run-attempt "$release_run_attempt" \
    --release-tag "$release_tag" \
    --release-head-sha "$release_head_sha" \
    --release-manifest-sha256 "$release_manifest_sha256" \
    --release-archive "$release_archive" \
    --release-archive-sha256 "$release_archive_sha256" \
    --release-kaiten-sha256 "$release_kaiten_sha256" \
    --release-kaiten-mcp-sha256 "$release_kaiten_mcp_sha256" \
    --profile "$profile" \
    --evidence "$evidence_root" \
    --runner-label "$runner_label" \
    --commit "$commit"
}

if [[ "${RUNNER_OS:-}" != "Linux" ]]; then
  profile="$RUNNER_TEMP/native-lifecycle-profile-$runner_id"
  run_harness "$build/native-lifecycle-harness$extension" "$build" "$profile" "$evidence"
  exit
fi

# GitHub's Linux runner account is intentionally not used for the lifecycle.
# A fresh user plus user@.service supplies a real DBus-backed systemd --user
# manager, while the Go harness itself and every installed process remain
# unprivileged.
native_user="kaitenci"
if id "$native_user" >/dev/null 2>&1; then
  echo "refusing to reuse existing Linux lifecycle user $native_user" >&2
  exit 1
fi

stage="/tmp/kaiten-native-lifecycle-${workflow_run_id}-${workflow_run_attempt}"
profile="$stage/native-lifecycle-profile-$runner_id"
created_user=false
stage_created=false
cleanup_complete=false
uid=""
gid=""
finish_linux() {
  local original_status=$?
  local copy_status=0
  local cleanup_status=0
  local resolved_uid="$uid"
  trap - EXIT
  set +e
  if [[ "$created_user" == true ]]; then
    if [[ -z "$resolved_uid" ]]; then
      resolved_uid="$(id -u "$native_user" 2>/dev/null)"
    fi
    if [[ ! "$resolved_uid" =~ ^[1-9][0-9]*$ ]]; then
      sudo userdel "$native_user"
      cleanup_status=$?
      if id "$native_user" >/dev/null 2>&1 || ! sudo rmdir -- "$stage"; then
        cleanup_status=1
      fi
    else
      if [[ -d "$stage/evidence" && ! -L "$stage/evidence" ]]; then
        sudo cp -R "$stage/evidence/." "$evidence/"
        copy_status=$?
        if [[ "$copy_status" -eq 0 ]]; then
          sudo chown -R "$(id -u):$(id -g)" "$evidence"
          copy_status=$?
        fi
      else
        copy_status=1
      fi
      "$script_directory/cleanup-native-lifecycle-linux.sh" "$native_user" "$resolved_uid" "$stage" "$evidence"
      cleanup_status=$?
      if [[ "$cleanup_status" -eq 0 ]]; then
        cleanup_complete=true
      fi
    fi
  elif [[ "$stage_created" == true ]]; then
    sudo rmdir -- "$stage"
    cleanup_status=$?
  fi
  if [[ "$original_status" -ne 0 || "$copy_status" -ne 0 || "$cleanup_status" -ne 0 || "$cleanup_complete" != true ]]; then
    exit 1
  fi
  exit 0
}
trap finish_linux EXIT

if [[ -e "$stage" ]]; then
  echo "refusing to reuse existing Linux lifecycle stage $stage" >&2
  exit 1
fi
test -x "$script_directory/cleanup-native-lifecycle-linux.sh"
sudo install -d -m 0755 "$stage"
stage_created=true
sudo useradd --home-dir "$profile" --no-create-home --shell /bin/bash "$native_user"
created_user=true
uid="$(id -u "$native_user")"
gid="$(id -g "$native_user")"
if [[ ! "$uid" =~ ^[1-9][0-9]*$ || ! "$gid" =~ ^[1-9][0-9]*$ ]]; then
  echo "dedicated Linux lifecycle user has an invalid numeric identity" >&2
  exit 1
fi
sudo install -d -m 0700 -o "$native_user" -g "$native_user" "$profile" "$stage/build" "$stage/evidence"
sudo systemctl start "user-runtime-dir@$uid.service"
runtime_dir="/run/user/$uid"
if [[ ! -d "$runtime_dir" || -L "$runtime_dir" ]]; then
  echo "dedicated user's runtime directory is unavailable or symbolic" >&2
  exit 1
fi
if [[ "$(stat -c '%u:%g' "$runtime_dir")" != "$uid:$gid" ]]; then
  echo "dedicated user's runtime directory has unexpected ownership" >&2
  exit 1
fi
sudo loginctl enable-linger "$native_user"
sudo systemctl start "user@$uid.service"

bus="$runtime_dir/bus"
for _ in $(seq 1 30); do
  [[ -S "$bus" ]] && break
  sleep 1
done
if [[ ! -S "$bus" ]]; then
  echo "dedicated user's DBus socket did not become available" >&2
  exit 1
fi

sudo cp -R "$build/." "$stage/build/"
sudo chown -R "$native_user:$native_user" "$stage/build"

set +e
sudo -u "$native_user" env \
  HOME="$profile" \
  USERPROFILE="$profile" \
  XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$bus" \
  NATIVE_CI_RUNNER_LABEL="$runner_label" \
  NATIVE_CI_RUNNER_ID="$runner_id" \
  "$stage/build/native-lifecycle-harness" \
    --v1 "$stage/build/v1/kaiten-mcp" \
    --v2 "$stage/build/release/kaiten-mcp" \
    --v3 "$stage/build/v3/kaiten-mcp" \
    --release-kaiten "$stage/build/release/kaiten" \
    --v2-version "$release_version" \
    --release-run-id "$release_run_id" \
    --release-run-attempt "$release_run_attempt" \
    --release-tag "$release_tag" \
    --release-head-sha "$release_head_sha" \
    --release-manifest-sha256 "$release_manifest_sha256" \
    --release-archive "$release_archive" \
    --release-archive-sha256 "$release_archive_sha256" \
    --release-kaiten-sha256 "$release_kaiten_sha256" \
    --release-kaiten-mcp-sha256 "$release_kaiten_mcp_sha256" \
    --profile "$profile" \
    --evidence "$stage/evidence" \
    --runner-label "$runner_label" \
    --commit "$commit"
result=$?
set -e

exit "$result"
