#!/usr/bin/env bash
set -euo pipefail

output="${1:?usage: build-native-lifecycle-fixtures.sh <absolute-output-directory>}"
case "$output" in
  /* | [A-Za-z]:[\\/]*) ;;
  *) echo "output directory must be absolute" >&2; exit 2 ;;
esac

extension="$(go env GOEXE)"
export CGO_ENABLED=0
mkdir -p "$output/v1" "$output/v3"
go build -trimpath -ldflags '-X github.com/dsuranov/kaiten-mcp/internal/version.Version=native-v1' \
  -o "$output/v1/kaiten-mcp$extension" ./cmd/kaiten-mcp
go build -trimpath -ldflags '-X github.com/dsuranov/kaiten-mcp/internal/version.Version=native-v3' \
  -o "$output/v3/kaiten-mcp$extension" ./cmd/native-lifecycle-bad-service
go build -trimpath -o "$output/native-lifecycle-harness$extension" ./cmd/native-lifecycle-harness
