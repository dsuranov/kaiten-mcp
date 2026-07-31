#!/usr/bin/env bash
set -euo pipefail

build="${1:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
evidence="${2:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
runner_label="${NATIVE_CI_RUNNER_LABEL:?NATIVE_CI_RUNNER_LABEL is required}"
runner_id="${NATIVE_CI_RUNNER_ID:?NATIVE_CI_RUNNER_ID is required}"
expected_sha="${NATIVE_CI_EXPECTED_SHA:?NATIVE_CI_EXPECTED_SHA is required}"
expected_release_run_id="${NATIVE_CI_RELEASE_RUN_ID:?NATIVE_CI_RELEASE_RUN_ID is required}"
extension="$(go env GOEXE)"
binding="$build/release-binding.txt"

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
test "$(read_binding schema)" = "kaiten-native-release-binding/v1"
release_run_id="$(read_binding release_run_id)"
release_run_attempt="$(read_binding release_run_attempt)"
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
release_artifact_api_digest="$(read_binding release_artifact_api_digest)"
release_artifact_zip_sha256="$(read_binding release_artifact_zip_sha256)"
release_kaiten_sha256="$(read_binding release_kaiten_sha256)"
release_kaiten_mcp_sha256="$(read_binding release_kaiten_mcp_sha256)"

[[ "$expected_sha" =~ ^[0-9a-f]{40}$ ]]
[[ "$expected_release_run_id" =~ ^[1-9][0-9]*$ ]]
[[ "$release_run_attempt" =~ ^[1-9][0-9]*$ ]]
[[ "$release_tag" =~ ^v[0-9][0-9A-Za-z.+-]*$ ]]
[[ "$release_version" =~ ^[0-9][0-9A-Za-z.+-]*$ ]]
[[ "$release_archive" =~ ^kaiten_[0-9A-Za-z.+-]+_(darwin|linux|windows)_(amd64|arm64)(\.tar\.gz|\.zip)$ ]]
[[ "$release_goos" =~ ^(darwin|linux|windows)$ ]]
[[ "$release_goarch" =~ ^(amd64|arm64)$ ]]
[[ "$release_artifact_id" =~ ^[1-9][0-9]*$ ]]
[[ "$release_artifact_api_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
[[ "$release_head_sha" =~ ^[0-9a-f]{40}$ ]]
for digest in "$release_manifest_sha256" "$release_archive_sha256" "$release_artifact_zip_sha256" "$release_kaiten_sha256" "$release_kaiten_mcp_sha256"; do
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]]
done
test "$release_head_sha" = "$expected_sha"
test "$release_run_id" = "$expected_release_run_id"
test "$release_tag" = "v$release_version"
test "$release_artifact_api_digest" = "sha256:$release_artifact_zip_sha256"
test "$release_goos" = "$(go env GOOS)"
test "$release_goarch" = "$(go env GOARCH)"
test "$release_go_version" = "$(go env GOVERSION)"
commit="$release_head_sha"
mkdir -p "$evidence"
{
  echo "runner_label=$runner_label"
  echo "runner_id=$runner_id"
  echo "runner_os=${RUNNER_OS:-local}"
  echo "candidate_commit=$commit"
  echo "workflow_run_id=${GITHUB_RUN_ID:-local}"
  echo "workflow_run_attempt=${GITHUB_RUN_ATTEMPT:-local}"
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
    --release-tag "$release_tag" \
    --release-head-sha "$release_head_sha" \
    --release-manifest-sha256 "$release_manifest_sha256" \
    --release-archive "$release_archive" \
    --release-archive-sha256 "$release_archive_sha256" \
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

stage="/tmp/kaiten-native-lifecycle-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
profile="$stage/native-lifecycle-profile-$runner_id"
created_user=false
cleanup() {
  set +e
  if [[ "$created_user" == true ]]; then
    uid="$(id -u "$native_user" 2>/dev/null)"
    if [[ -n "$uid" ]]; then
      sudo systemctl stop "user@$uid.service" >/dev/null 2>&1
      sudo loginctl disable-linger "$native_user" >/dev/null 2>&1
    fi
    sudo userdel --remove "$native_user" >/dev/null 2>&1
  fi
  if [[ "$stage" == /tmp/kaiten-native-lifecycle-* ]]; then
    sudo rm -rf -- "$stage"
  fi
}
trap cleanup EXIT

if [[ -e "$stage" ]]; then
  echo "refusing to reuse existing Linux lifecycle stage $stage" >&2
  exit 1
fi
sudo install -d -m 0755 "$stage"
sudo useradd --home-dir "$profile" --no-create-home --shell /bin/bash "$native_user"
created_user=true
uid="$(id -u "$native_user")"
sudo install -d -m 0700 -o "$native_user" -g "$native_user" "$profile" "$stage/build" "$stage/evidence"
sudo loginctl enable-linger "$native_user"
sudo systemctl start "user@$uid.service"

bus="/run/user/$uid/bus"
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
    --release-tag "$release_tag" \
    --release-head-sha "$release_head_sha" \
    --release-manifest-sha256 "$release_manifest_sha256" \
    --release-archive "$release_archive" \
    --release-archive-sha256 "$release_archive_sha256" \
    --profile "$profile" \
    --evidence "$stage/evidence" \
    --runner-label "$runner_label" \
    --commit "$commit"
result=$?
set -e

sudo cp -R "$stage/evidence/." "$evidence/"
sudo chown -R "$(id -u):$(id -g)" "$evidence"
exit "$result"
