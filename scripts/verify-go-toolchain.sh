#!/usr/bin/env bash
set -euo pipefail

readonly expected_go_version="go1.26.5"
actual_go_version="$(go env GOVERSION)"

if [[ "$actual_go_version" != "$expected_go_version" ]]; then
  echo "error: required Go toolchain is $expected_go_version; found $actual_go_version" >&2
  exit 1
fi

go_version_line="$(go version)"
if [[ "$go_version_line" != "go version $expected_go_version "* ]]; then
  echo "error: go version output does not identify $expected_go_version: $go_version_line" >&2
  exit 1
fi

echo "verified release toolchain: $go_version_line"
