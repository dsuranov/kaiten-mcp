#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.5}"
export TMPDIR="$repo_root/bin/tmp"
mkdir -p "$TMPDIR" "$repo_root/bin/tmp/release-tools"

goreleaser_bin="${GORELEASER_BIN:-$(command -v goreleaser || true)}"
syft_bin="${SYFT_BIN:-$(command -v syft || true)}"
if [[ -z "$goreleaser_bin" || -z "$syft_bin" ]]; then
  echo "error: goreleaser and syft must be available" >&2
  exit 1
fi
goreleaser_bin="$(cd "$(dirname "$goreleaser_bin")" && pwd)/$(basename "$goreleaser_bin")"
syft_bin="$(cd "$(dirname "$syft_bin")" && pwd)/$(basename "$syft_bin")"
export GORELEASER_BIN="$goreleaser_bin"
export SYFT_BIN="$syft_bin"

./scripts/verify-go-toolchain.sh
./scripts/verify-release-tools.sh

export KAITEN_RELEASE_COMMIT="$(git rev-parse HEAD)"
export KAITEN_RELEASE_EPOCH="$(git show -s --format=%ct HEAD)"
export KAITEN_RELEASE_COMMIT_DATE="$(git show -s --format=%cI HEAD)"
export KAITEN_SPDX_NORMALIZER="$repo_root/bin/tmp/release-tools/normalize-spdx"
export SYFT_CHECK_FOR_APP_UPDATE=false
export SYFT_GOLANG_SEARCH_LOCAL_MOD_CACHE_LICENSES=false
export SYFT_GOLANG_SEARCH_LOCAL_VENDOR_LICENSES=false
export SYFT_GOLANG_SEARCH_REMOTE_LICENSES=false

go build -trimpath -o "$KAITEN_SPDX_NORMALIZER" ./internal/release/cmd/normalize-spdx
./scripts/verify-release-environment.sh

exec "$goreleaser_bin" "$@"
