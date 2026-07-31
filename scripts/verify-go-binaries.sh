#!/usr/bin/env bash
set -euo pipefail

readonly expected_go_version="${EXPECTED_GO_VERSION:-go1.26.5}"
readonly expected_scanner_version="${GOVULNCHECK_VERSION:-v1.6.0}"
readonly scanner="${GOVULNCHECK_BIN:-govulncheck}"

if [[ "$#" -eq 0 ]]; then
  echo "usage: verify-go-binaries.sh <binary> [binary ...]" >&2
  exit 2
fi

scanner_path="$scanner"
if [[ "$scanner" == */* ]]; then
  if [[ ! -x "$scanner" ]]; then
    echo "error: govulncheck is not executable: $scanner" >&2
    exit 1
  fi
else
  scanner_path="$(command -v "$scanner" || true)"
  if [[ -z "$scanner_path" ]]; then
    echo "error: govulncheck is not available on PATH" >&2
    exit 1
  fi
fi

scanner_metadata="$(go version -m "$scanner_path")"
if ! grep -Fq $'\tmod\tgolang.org/x/vuln\t'"$expected_scanner_version"$'\t' <<<"$scanner_metadata"; then
  echo "error: govulncheck must come from golang.org/x/vuln $expected_scanner_version" >&2
  printf '%s\n' "$scanner_metadata" >&2
  exit 1
fi

for binary in "$@"; do
  if [[ ! -f "$binary" ]]; then
    echo "error: binary does not exist: $binary" >&2
    exit 1
  fi
  metadata="$(go version -m "$binary")"
  first_line="${metadata%%$'\n'*}"
  if [[ "$first_line" != *": $expected_go_version" ]]; then
    echo "error: $binary was not built with $expected_go_version" >&2
    printf '%s\n' "$metadata" >&2
    exit 1
  fi
  echo "verified binary toolchain: $first_line"
  "$scanner_path" -mode=binary "$binary"
done
