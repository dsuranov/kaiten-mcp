# Reproducible release builds

Release acceptance requires two independent clean checkouts of the same commit
to produce identical bytes. The gate compares ten raw cross-built binaries,
ten archives, ten SPDX JSON documents, and the SHA-256 manifest. GoReleaser
metadata files are diagnostic output and are not release artifacts.

Run the local gate with the repository-pinned Go 1.26.5, GoReleaser 2.17.1,
and Syft 1.50.0 available on `PATH`:

```sh
./scripts/verify-reproducible-release.sh --snapshot
```

The script refuses a dirty checkout, creates two local clones at the exact
commit, assigns deliberately different source-file mtimes, builds both clones,
and compares the release bytes. The second verified result is copied to
`dist/`; `dist/reproducibility.txt` records the comparison counts. A tag build
runs the same gate without snapshot mode before assets can reach attestation or
the draft-release job.

## Deterministic inputs

- Go is exactly 1.26.5, `CGO_ENABLED=0`, and builds use `-trimpath`. Version,
  full commit, commit date, and output mtime come from the exact Git commit.
- Every archive member has an explicit commit-derived mtime, `root` owner and
  group for TAR, and a fixed `0644` or `0755` mode. GoReleaser sorts configured
  files and emits stable gzip/ZIP metadata. Source checkout owners and mtimes
  are not trusted.
- GoReleaser is exactly 2.17.1 and Syft is exactly 1.50.0. Runtime checks verify
  those versions instead of relying only on workflow text.
- Syft scans only the completed archive. Mutable network enrichment and local
  module/vendor license-cache lookup are disabled. Package inventory,
  checksums, files, and relationships remain Syft output; dependency license
  notices are also carried separately in `THIRD_PARTY_NOTICES.md`.

These choices follow the official [Go reproducible-build
guidance](https://go.dev/blog/rebuild), [GoReleaser Go builder
guidance](https://goreleaser.com/customization/builds/builders/go/#reproducible-builds),
and [GoReleaser SBOM command contract](https://goreleaser.com/customization/sbom/).
Syft's supported SPDX output and conversion behavior are documented by the
[Syft project](https://github.com/anchore/syft).

## SPDX normalization policy

Syft 1.50.0 intentionally emits the wall-clock generation time and a random
UUID namespace. The repository's custom GoReleaser SBOM command runs Syft
first, then the small `spdxnormalize` helper before GoReleaser computes the
manifest. It makes only these provenance-policy changes:

1. `creationInfo.created` becomes the source commit time in UTC;
2. the existing Syft creators are preserved and
   `Tool: kaiten-mcp-spdx-normalizer-1` is appended;
3. `creationInfo.comment` explains the commit-time convention and namespace
   policy;
4. `documentNamespace` becomes an HTTPS URI containing the SHA-256 digest of
   canonical semantic document content, excluding only `creationInfo.created`
   and `documentNamespace`, plus the normalization-policy version.

The semantic digest includes package evidence, archive checksums,
relationships, creator provenance, and the policy comment. A changed analysis
therefore receives a different namespace even if it describes the same archive
bytes. This follows SPDX 2.3's requirement that each document namespace be a
unique absolute URI; see the official [document creation information
specification](https://spdx.github.io/spdx-spec/v2.3/document-creation-information/).

Before writing an SBOM, the helper verifies the archive SHA-256 recorded by the
root package, unique SPDX IDs, relationship references, the `DESCRIBES` edge,
creators, timestamp, and namespace. The pinned Syft parser then converts the
final document as an independent parse check. The two-build gate repeats those
checks and verifies all 20 manifest entries.

## Evidence interpretation

A passing local file proves byte equality for the recorded commit and pinned
local tools. It is not evidence that a hosted CI or native lifecycle job ran.
Keep the workflow run URL, runner image, artifact hashes, and native test record
with the release evidence before publication.
