#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: generate-reproducible-sbom.sh ARTIFACT DOCUMENT" >&2
  exit 2
fi

artifact="$1"
document="$2"
: "${TMPDIR:?TMPDIR is required}"
: "${SYFT_BIN:?SYFT_BIN is required}"
: "${KAITEN_SPDX_NORMALIZER:?KAITEN_SPDX_NORMALIZER is required}"
: "${KAITEN_RELEASE_COMMIT_DATE:?KAITEN_RELEASE_COMMIT_DATE is required}"

raw_document="$(mktemp "$TMPDIR/syft-spdx.XXXXXX.json")"
converted_document="$(mktemp "$TMPDIR/syft-validate.XXXXXX.json")"
cleanup() {
  rm -f -- "$raw_document" "$converted_document"
}
trap cleanup EXIT

"$SYFT_BIN" scan "file:$artifact" \
  --quiet \
  --output "spdx-json=$raw_document"

"$KAITEN_SPDX_NORMALIZER" \
  -input "$raw_document" \
  -output "$document" \
  -artifact "$artifact" \
  -created "$KAITEN_RELEASE_COMMIT_DATE"

# A successful conversion proves the final, normalized document is still
# accepted by the same pinned Syft SPDX parser before checksums are computed.
"$SYFT_BIN" convert "$document" \
  --quiet \
  --output "syft-json=$converted_document"
test -s "$converted_document"
