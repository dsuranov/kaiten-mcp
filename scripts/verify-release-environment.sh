#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
expected_commit="$(git rev-parse HEAD)"
expected_epoch="$(git show -s --format=%ct HEAD)"
expected_date="$(git show -s --format=%cI HEAD)"

require_equal() {
  local name="$1"
  local expected="$2"
  local actual="${!name:-}"
  if [[ "$actual" != "$expected" ]]; then
    printf 'error: %s=%q, want %q\n' "$name" "$actual" "$expected" >&2
    exit 1
  fi
}

require_equal KAITEN_RELEASE_COMMIT "$expected_commit"
require_equal KAITEN_RELEASE_EPOCH "$expected_epoch"
require_equal KAITEN_RELEASE_COMMIT_DATE "$expected_date"
require_equal TMPDIR "$repo_root/bin/tmp"
if [[ "${GOTOOLCHAIN:-}" != "local" && "${GOTOOLCHAIN:-}" != "go1.26.5" ]]; then
  echo "error: GOTOOLCHAIN must be local or go1.26.5 after exact toolchain verification" >&2
  exit 1
fi

if [[ -z "${KAITEN_SPDX_NORMALIZER:-}" || ! -x "$KAITEN_SPDX_NORMALIZER" ]]; then
  echo "error: KAITEN_SPDX_NORMALIZER must name the prepared executable" >&2
  exit 1
fi
if [[ -z "${KAITEN_ARCHIVE_CANONICALIZER:-}" || ! -x "$KAITEN_ARCHIVE_CANONICALIZER" ]]; then
  echo "error: KAITEN_ARCHIVE_CANONICALIZER must name the prepared executable" >&2
  exit 1
fi
if [[ -z "${SYFT_BIN:-}" || ! -x "$SYFT_BIN" ]]; then
  echo "error: SYFT_BIN must name the verified Syft executable" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "error: release source has tracked modifications" >&2
  exit 1
fi

printf 'verified deterministic release environment: commit=%s epoch=%s\n' "$expected_commit" "$expected_epoch"
