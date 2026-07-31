#!/usr/bin/env bash
set -euo pipefail

goreleaser_bin="${GORELEASER_BIN:-$(command -v goreleaser || true)}"
syft_bin="${SYFT_BIN:-$(command -v syft || true)}"

if [[ -z "$goreleaser_bin" || ! -x "$goreleaser_bin" ]]; then
  echo "error: GoReleaser executable was not found" >&2
  exit 1
fi
if [[ -z "$syft_bin" || ! -x "$syft_bin" ]]; then
  echo "error: Syft executable was not found" >&2
  exit 1
fi

has_module_version() {
  local binary="$1"
  local module="$2"
  local version="$3"
  go version -m "$binary" 2>/dev/null | awk -v module="$module" -v version="$version" '
    $1 == "mod" && $2 == module && $3 == version { found = 1 }
    END { exit found ? 0 : 1 }
  '
}

if ! has_module_version "$goreleaser_bin" github.com/goreleaser/goreleaser/v2 v2.17.1; then
  if ! "$goreleaser_bin" --version 2>&1 | grep -Eq '^GitVersion:[[:space:]]+v2\.17\.1$'; then
    echo "error: GoReleaser must be exactly v2.17.1" >&2
    exit 1
  fi
fi

if ! has_module_version "$syft_bin" github.com/anchore/syft v1.50.0; then
  if ! "$syft_bin" version -o json 2>/dev/null | grep -Eq '"version"[[:space:]]*:[[:space:]]*"1\.50\.0"'; then
    echo "error: Syft must be exactly v1.50.0" >&2
    exit 1
  fi
fi

printf 'verified release tools: goreleaser=v2.17.1 syft=v1.50.0\n'
