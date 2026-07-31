#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

snapshot=false
if [[ "${1:-}" == "--snapshot" ]]; then
  snapshot=true
  shift
fi
if [[ "$#" -ne 0 ]]; then
  echo "usage: verify-reproducible-release.sh [--snapshot]" >&2
  exit 2
fi

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
if [[ -n "$(git status --porcelain --untracked-files=all)" ]]; then
  echo "error: reproducibility verification requires a clean committed checkout" >&2
  exit 1
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.5}"
export GORELEASER_BIN="${GORELEASER_BIN:-$(command -v goreleaser || true)}"
export SYFT_BIN="${SYFT_BIN:-$(command -v syft || true)}"
./scripts/verify-go-toolchain.sh
./scripts/verify-release-tools.sh

mkdir -p "$repo_root/bin/tmp"
scratch="$(mktemp -d "$repo_root/bin/tmp/reproducible-release.XXXXXX")"
cleanup() {
  rm -rf -- "$scratch"
}
trap cleanup EXIT

commit="$(git rev-parse HEAD)"
created="$(git show -s --format=%cI HEAD)"
release_args=(release --clean --skip=publish)
if [[ "$snapshot" == true ]]; then
  release_args+=(--snapshot)
fi

for run in run1 run2; do
  source_dir="$scratch/$run"
  git clone --quiet --no-hardlinks "$repo_root" "$source_dir"
  git -c advice.detachedHead=false -C "$source_dir" checkout --quiet --detach "$commit"
  test "$(git -C "$source_dir" rev-parse HEAD)" = "$commit"
done

# Deliberately assign different checkout mtimes. Equal archives must therefore
# come from the declared archive metadata rather than incidental filesystem
# metadata shared by the two builds.
find "$scratch/run1" -path '*/.git' -prune -o -type f -exec touch -t 202601020304.05 {} +
find "$scratch/run2" -path '*/.git' -prune -o -type f -exec touch -t 202602030405.06 {} +

for run in run1 run2; do
  (
    cd "$scratch/$run"
    export GORELEASER_BIN SYFT_BIN GOTOOLCHAIN
    ./scripts/release-build.sh "${release_args[@]}"
  )
done

raw_list() {
  local dist="$1"
  (
    cd "$dist"
    find . -mindepth 2 -type f \
      \( -name kaiten -o -name kaiten.exe -o -name kaiten-mcp -o -name kaiten-mcp.exe \) \
      -print | sort
  )
}

release_list() {
  local dist="$1"
  (
    cd "$dist"
    find . -maxdepth 1 -type f \
      \( -name '*.tar.gz' -o -name '*.zip' -o -name '*.sbom.json' -o -name checksums.txt \) \
      -print | sort
  )
}

raw_list "$scratch/run1/dist" >"$scratch/run1.raw"
raw_list "$scratch/run2/dist" >"$scratch/run2.raw"
release_list "$scratch/run1/dist" >"$scratch/run1.release"
release_list "$scratch/run2/dist" >"$scratch/run2.release"

test "$(wc -l <"$scratch/run1.raw" | tr -d ' ')" -eq 10
test "$(wc -l <"$scratch/run2.raw" | tr -d ' ')" -eq 10
test "$(wc -l <"$scratch/run1.release" | tr -d ' ')" -eq 21
test "$(wc -l <"$scratch/run2.release" | tr -d ' ')" -eq 21
cmp "$scratch/run1.raw" "$scratch/run2.raw"
cmp "$scratch/run1.release" "$scratch/run2.release"

while IFS= read -r relative; do
  cmp "$scratch/run1/dist/$relative" "$scratch/run2/dist/$relative"
done <"$scratch/run1.raw"

while IFS= read -r relative; do
  cmp "$scratch/run1/dist/$relative" "$scratch/run2/dist/$relative"
done <"$scratch/run1.release"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

verify_manifest() {
  local dist="$1"
  local count=0
  local expected name extra actual
  while read -r expected name extra; do
    if [[ ! "$expected" =~ ^[0-9a-f]{64}$ || -z "$name" || -n "${extra:-}" || "$name" == */* ]]; then
      echo "error: invalid checksum manifest line for $name" >&2
      exit 1
    fi
    test -f "$dist/$name"
    actual="$(sha256_file "$dist/$name")"
    if [[ "$actual" != "$expected" ]]; then
      echo "error: checksum mismatch for $name" >&2
      exit 1
    fi
    count=$((count + 1))
  done <"$dist/checksums.txt"
  test "$count" -eq 20
}

verify_manifest "$scratch/run1/dist"
verify_manifest "$scratch/run2/dist"

normalizer="$scratch/run2/bin/tmp/release-tools/normalize-spdx"
test -x "$normalizer"
validation_index=0
for sbom in "$scratch/run2"/dist/*.sbom.json; do
  artifact="${sbom%.sbom.json}"
  test -f "$artifact"
  "$normalizer" -check -input "$sbom" -artifact "$artifact" -created "$created"
  validation_index=$((validation_index + 1))
  converted="$scratch/validated-$validation_index.syft.json"
  "$SYFT_BIN" convert "$sbom" --quiet --output "syft-json=$converted"
  test -s "$converted"
done
test "$validation_index" -eq 10

# Leave the second independently verified build as the release candidate. Only
# publishable root artifacts are copied; raw binaries remain proven by the
# comparison above and are distributed inside the archives.
rm -rf -- "$repo_root/dist"
mkdir -p "$repo_root/dist"
while IFS= read -r relative; do
  cp "$scratch/run2/dist/$relative" "$repo_root/dist/$relative"
done <"$scratch/run2.release"

{
  printf 'commit=%s\n' "$commit"
  printf 'source_runs=2 independent clean local clones\n'
  printf 'source_mtimes=deliberately distinct\n'
  printf 'raw_binaries=10/10 byte-identical\n'
  printf 'archives=10/10 byte-identical\n'
  printf 'spdx_documents=10/10 byte-identical and validated\n'
  printf 'checksum_manifest=1/1 byte-identical; 20/20 entries verified\n'
  printf 'release_files=21/21 byte-identical\n'
  printf 'go=1.26.5\n'
  printf 'goreleaser=2.17.1\n'
  printf 'syft=1.50.0\n'
} >"$repo_root/dist/reproducibility.txt"

printf 'verified reproducible release: commit=%s raw=10/10 release=21/21\n' "$commit"
