#!/usr/bin/env bash
set -euo pipefail

build="${1:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
evidence="${2:?usage: run-native-lifecycle-ci.sh <build-directory> <evidence-directory>}"
runner_label="${NATIVE_CI_RUNNER_LABEL:?NATIVE_CI_RUNNER_LABEL is required}"
runner_id="${NATIVE_CI_RUNNER_ID:?NATIVE_CI_RUNNER_ID is required}"
commit="${GITHUB_SHA:-local-unpublished-candidate}"
extension="$(go env GOEXE)"

run_harness() {
  local harness="$1"
  local fixture_root="$2"
  local profile="$3"
  local evidence_root="$4"
  "$harness" \
    --v1 "$fixture_root/v1/kaiten-mcp$extension" \
    --v2 "$fixture_root/v2/kaiten-mcp$extension" \
    --v3 "$fixture_root/v3/kaiten-mcp$extension" \
    --profile "$profile" \
    --evidence "$evidence_root" \
    --runner-label "$runner_label" \
    --commit "$commit"
}

if [[ "${RUNNER_OS:-}" != "Linux" ]]; then
  profile="$RUNNER_TEMP/native-lifecycle-profile-$runner_id"
  mkdir -p "$evidence"
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
  GITHUB_SHA="$commit" \
  "$stage/build/native-lifecycle-harness" \
    --v1 "$stage/build/v1/kaiten-mcp" \
    --v2 "$stage/build/v2/kaiten-mcp" \
    --v3 "$stage/build/v3/kaiten-mcp" \
    --profile "$profile" \
    --evidence "$stage/evidence" \
    --runner-label "$runner_label" \
    --commit "$commit"
result=$?
set -e

mkdir -p "$evidence"
sudo cp -R "$stage/evidence/." "$evidence/"
sudo chown -R "$(id -u):$(id -g)" "$evidence"
exit "$result"
