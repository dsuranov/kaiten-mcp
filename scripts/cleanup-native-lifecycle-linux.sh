#!/usr/bin/env bash
set -uo pipefail

native_user="${1:?usage: cleanup-native-lifecycle-linux.sh <user> <uid> <stage> <evidence-directory>}"
native_uid="${2:?usage: cleanup-native-lifecycle-linux.sh <user> <uid> <stage> <evidence-directory>}"
stage="${3:?usage: cleanup-native-lifecycle-linux.sh <user> <uid> <stage> <evidence-directory>}"
evidence="${4:?usage: cleanup-native-lifecycle-linux.sh <user> <uid> <stage> <evidence-directory>}"

if [[ ! -d "$evidence" || -L "$evidence" ]]; then
  echo "evidence destination is unavailable or symbolic" >&2
  exit 1
fi
canonical_evidence="$(realpath "$evidence")"
if [[ "$canonical_evidence" != "$evidence" ]]; then
  echo "evidence destination is not an exact canonical path" >&2
  exit 1
fi

target_validated=false
user_manager_stopped=false
linger_disabled=false
processes_absent=false
port_8100_free=false
user_deleted=false
group_deleted=false
login_state_absent=false
stage_deleted=false
failed=false
evidence_uid=0

write_evidence() {
  local result="failed"
  [[ "$failed" == false ]] && result="passed"
  local temporary="$canonical_evidence/.linux-wrapper-cleanup.$$.tmp"
  {
    printf '{\n'
    printf '  "schema": "kaiten-linux-wrapper-cleanup/v1",\n'
    printf '  "result": "%s",\n' "$result"
    printf '  "user": "kaitenci",\n'
    printf '  "uid": %s,\n' "$evidence_uid"
    printf '  "target_validated": %s,\n' "$target_validated"
    printf '  "user_manager_stopped": %s,\n' "$user_manager_stopped"
    printf '  "linger_disabled": %s,\n' "$linger_disabled"
    printf '  "processes_absent": %s,\n' "$processes_absent"
    printf '  "port_8100_free": %s,\n' "$port_8100_free"
    printf '  "user_deleted": %s,\n' "$user_deleted"
    printf '  "group_deleted": %s,\n' "$group_deleted"
    printf '  "login_state_absent": %s,\n' "$login_state_absent"
    printf '  "stage_deleted": %s\n' "$stage_deleted"
    printf '}\n'
  } >"$temporary"
  chmod 0600 "$temporary"
  mv -f -- "$temporary" "$canonical_evidence/linux-wrapper-cleanup.json"
}

finish() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 ]]; then
    failed=true
  fi
  write_evidence || status=1
  if [[ "$failed" == true ]]; then
    status=1
  fi
  exit "$status"
}
trap finish EXIT

if [[ "$native_user" != "kaitenci" || ! "$native_uid" =~ ^[1-9][0-9]*$ ]]; then
  echo "invalid dedicated native lifecycle user identity" >&2
  exit 1
fi
evidence_uid="$native_uid"
for required_command in realpath ss pgrep getent; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "required cleanup probe is unavailable: $required_command" >&2
    exit 1
  fi
done
if [[ ! "${GITHUB_RUN_ID:-}" =~ ^[1-9][0-9]*$ || ! "${GITHUB_RUN_ATTEMPT:-}" =~ ^[1-9][0-9]*$ ]]; then
  echo "GitHub workflow identity is unavailable for cleanup target validation" >&2
  exit 1
fi
expected_stage="/tmp/kaiten-native-lifecycle-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
if [[ "$stage" != "$expected_stage" || ! -d "$stage" || -L "$stage" ]]; then
  echo "refusing unreviewed Linux cleanup stage $stage" >&2
  exit 1
fi
canonical_stage="$(realpath "$stage")"
if [[ "$canonical_stage" != "$expected_stage" || "$(dirname "$canonical_stage")" != "/tmp" ]]; then
  echo "Linux cleanup stage did not resolve to the exact /tmp child" >&2
  exit 1
fi
target_validated=true

if ! sudo systemctl stop "user@${native_uid}.service"; then
  failed=true
fi
manager_state="$(sudo systemctl show "user@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"
if ! sudo systemctl stop "user-runtime-dir@${native_uid}.service"; then
  failed=true
fi
runtime_dir_state="$(sudo systemctl show "user-runtime-dir@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"
runtime_dir="/run/user/$native_uid"
if [[ "$manager_state" == "inactive" && "$runtime_dir_state" == "inactive" && ! -e "$runtime_dir" && ! -L "$runtime_dir" ]]; then
  user_manager_stopped=true
else
  failed=true
fi

sudo loginctl disable-linger "$native_user" >/dev/null 2>&1 || true
linger_marker="/var/lib/systemd/linger/$native_user"
linger_record="/run/systemd/users/$native_uid"
if linger_state="$(sudo loginctl show-user "$native_user" --property=Linger --value 2>/dev/null)"; then
  if [[ "$linger_state" == "no" && ! -e "$linger_marker" && ! -L "$linger_marker" ]]; then
    linger_disabled=true
  else
    failed=true
  fi
elif [[ ! -e "$linger_record" && ! -L "$linger_record" && ! -e "$linger_marker" && ! -L "$linger_marker" ]]; then
  linger_disabled=true
else
  failed=true
fi

sudo pgrep -u "$native_uid" >/dev/null 2>&1
process_status=$?
if [[ "$process_status" -eq 1 ]]; then
  processes_absent=true
else
  failed=true
fi

listeners="$(sudo ss -H -ltn 'sport = :8100' 2>/dev/null)"
listener_status=$?
if [[ "$listener_status" -eq 0 && -z "$listeners" ]]; then
  port_8100_free=true
else
  failed=true
fi

if ! sudo userdel "$native_user"; then
  failed=true
fi
if ! id "$native_user" >/dev/null 2>&1 && ! getent passwd "$native_uid" >/dev/null 2>&1; then
  user_deleted=true
else
  failed=true
fi

if getent group "$native_user" >/dev/null 2>&1; then
  if ! sudo groupdel "$native_user"; then
    failed=true
  fi
fi
if ! getent group "$native_user" >/dev/null 2>&1; then
  group_deleted=true
else
  failed=true
fi

for _ in $(seq 1 20); do
  if ! sudo loginctl show-user "$native_user" >/dev/null 2>&1; then
    login_state_absent=true
    break
  fi
  sleep 0.25
done
if [[ "$login_state_absent" != true ]]; then
  failed=true
fi

sudo pgrep -u "$native_uid" >/dev/null 2>&1
process_status=$?
if [[ "$process_status" -ne 1 ]]; then
  processes_absent=false
  failed=true
fi

listeners="$(sudo ss -H -ltn 'sport = :8100' 2>/dev/null)"
listener_status=$?
if [[ "$listener_status" -ne 0 || -n "$listeners" ]]; then
  port_8100_free=false
  failed=true
fi

if [[ "$user_manager_stopped" == true && "$linger_disabled" == true && "$processes_absent" == true && "$port_8100_free" == true && "$user_deleted" == true && "$group_deleted" == true && "$login_state_absent" == true ]]; then
  sudo rm -rf -- "$canonical_stage"
  if [[ ! -e "$canonical_stage" ]]; then
    stage_deleted=true
  else
    failed=true
  fi
else
  echo "preserving exact stage because privileged state is not proven quiescent" >&2
  failed=true
fi

if [[ "$failed" == true ]]; then
  exit 1
fi
