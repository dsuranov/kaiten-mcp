# Release testing

## Release gate

A tag build creates a draft release, not an automatic public release. Publish
only after the frozen candidate, automated checks, native lifecycle evidence,
license/SBOM reconciliation, and clean-room audit all refer to the same commit.

Record the release tag, full commit, workflow run, Go version, GoReleaser
version, Syft version, runner image, artifact hashes, tester, and date.

## 1. Candidate preflight

From the frozen candidate:

```sh
./scripts/verify-go-toolchain.sh
go run ./scripts/verify-workflows.go
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color
go mod verify
go vet ./...
go test ./...
go test -race ./...
go test -covermode=atomic -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

The module requires Go 1.26.0 and pins release work to Go 1.26.5, selected under
the official [Go release policy](https://go.dev/doc/devel/release#policy) from
the current [stable downloads](https://go.dev/dl/). Install the official
scanner at the repository-pinned version, then require both build metadata and
binary-mode vulnerability checks for every executable:

```sh
GOBIN="$PWD/bin/tools" go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
GOVULNCHECK_BIN="$PWD/bin/tools/govulncheck" \
  ./scripts/verify-go-binaries.sh ./bin/kaiten ./bin/kaiten-mcp
```

The tag workflow repeats this check on every executable extracted from the
final archives. Source-mode scanning alone does not qualify packaged bytes.

Confirm `gofmt` reports no tracked Go file, the working tree is clean, and CI
passes on Linux, macOS, and Windows. Coverage is a measurement, not a substitute
for checking the critical configuration, transport, mutation, pagination,
resolution, cache, and installer branches.

Before tagging, build a local snapshot with the same pinned GoReleaser major and
Syft version used by the release workflow:

```sh
./scripts/verify-release-tools.sh
./scripts/verify-reproducible-release.sh --snapshot
```

This creates two independent clean clones and requires byte identity for all
ten raw binaries and all 21 publishable release files. See [Reproducible
builds](reproducible-builds.md) for the archive and SPDX normalization policy.

## 2. Artifact inventory

The draft release must contain ten executable archives: one full archive and
one MCP-only archive across five targets.

| Archive | darwin/amd64 | darwin/arm64 | linux/amd64 | linux/arm64 | windows/amd64 |
| --- | --- | --- | --- | --- | --- |
| `kaiten` | required | required | required | required | required |
| `kaiten-mcp` | required | required | required | required | required |

macOS and Linux use `.tar.gz`; Windows uses `.zip`. Every `kaiten_*` archive
must contain both `kaiten` and `kaiten-mcp`; every `kaiten-mcp_*` archive must
contain the MCP-only executable. Every archive also contains `README.md`,
`LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, and the user documentation
selected by the release configuration.

Also require:

- `checksums.txt` using SHA-256;
- one SPDX JSON SBOM for every executable archive;
- a checksum-manifest provenance attestation;
- draft release notes associated with the exact tag.

The two-build gate must report `raw=10/10 release=21/21`. Retain its
`reproducibility.txt` output with the workflow evidence; do not infer a hosted
or native run from local evidence.

Recompute every archive hash independently and compare it with the manifest.
Parse every SBOM as JSON and confirm its subject matches the corresponding
archive. Reconcile SBOM packages with `go list -m all` and
`THIRD_PARTY_NOTICES.md`.

## 3. Native binary smoke tests

Run artifacts on native hardware or a native hosted runner wherever available;
do not treat cross-compilation alone as execution evidence.

For each supported target:

1. extract into a new temporary directory;
2. run `kaiten --version`, `kaiten mcp version`, and `kaiten-mcp version` and
   verify the same release version;
3. run top-level, group, subcommand, and completion help without credentials;
4. verify an invalid local argument fails before the fake server sees a request;
5. run representative read and mutation contract tests against a newly authored
   fake Kaiten server;
6. verify stdout is one JSON value for successful data commands and secrets are
   absent from stderr.

If a Windows release chooses to provide the optional no-argument double-click
installer behavior, it must still distinguish that interactive launch from a
stdio launch by an MCP client. The normal no-argument behavior remains MCP
stdio mode.

## 4. MCP transport tests

Using a real MCP client library, run both entry points over stdio and Streamable
HTTP:

- negotiate a supported protocol version rather than assuming one version;
- assert the default `tools/list` contains exactly the 18 read names;
- assert explicit write enablement contains exactly all 33 names;
- compare field names, types, required fields, defaults, nullability, and
  non-negative pagination constraints with the sealed contract;
- exercise every tool with a success and relevant failure path against the fake
  API;
- compare structured content with the formatted JSON text result;
- cancel active work and confirm the HTTP listener can be rebound immediately;
- verify `/health` contains only `status`, `version`, and `runtime`.

Run read concurrency, cache-expiry/coalescing, ignored-pagination, bounded retry,
and at-most-once mutation cases under the race detector.

## 5. Native installer cycle

At least one full native installer cycle is mandatory on each supported
operating system before publishing:

| Operating system | Minimum native cycle | Additional architecture evidence |
| --- | --- | --- |
| macOS | amd64 or arm64 | Launch smoke on both release architectures. |
| Linux | amd64 or arm64 | Launch smoke on both release architectures. |
| Windows | amd64 | Covered by the native cycle. |

Use a disposable VM, hosted runner, or dedicated test user with a newly created
profile. The cycle must not read or modify a developer's real MCP-client
configuration. Drive the service against a new local fake API token and tenant;
never use production credentials.

For each operating system:

1. assert no product service or files exist in the test profile;
2. install in default read-only mode without elevated privileges;
3. verify secret and service-file permissions are as restrictive as the
   platform permits;
4. wait for bounded readiness and check
   `http://127.0.0.1:8100/health` and the MCP endpoint;
5. restart the user service through the native service manager and recheck
   health;
6. optionally register `kaiten` in a synthetic client JSON containing unrelated
   keys and another server, then prove those values are preserved;
7. perform a healthy update and verify version transition;
8. inject an activation or health failure during a second update and prove the
   prior executable/configuration remains usable;
9. uninstall and prove only owned service files and the optional `kaiten` client
   entry are removed while logs and unrelated settings remain;
10. run uninstall again and require a successful idempotent result;
11. search captured stdout, stderr, service definitions, and logs for the fake
    token and require no match.

Retain commands, redacted output, file modes or ACLs, service-manager status,
health JSON, before/after client configuration diffs, and remaining-path checks
as evidence.

## 6. Failure handling

Any mismatch, missing target, invalid SBOM, installer rollback failure, secret
disclosure, unreviewed dependency, or unexplained provenance finding keeps the
release **NO-GO**. Do not replace the frozen candidate silently. Fix on a new
commit, create a new candidate tag or draft, and rerun the affected gate plus
any downstream gate whose inputs changed.

## Evidence record template

```text
Release/tag:
Commit:
Workflow run:
Runner OS/image/architecture:
Go / GoReleaser / Syft versions:
Artifacts and verified SHA-256 values:
Automated test result:
MCP transport result:
Native installer cycle result:
License/SBOM reconciliation:
Clean-room audit reference:
Tester and UTC date:
Decision: GO | NO-GO
Open findings:
```
