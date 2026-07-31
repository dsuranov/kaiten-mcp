# Security

## Security posture

This is a local bearer-token integration. Anyone who obtains the token can act
with its Kaiten permissions, and any client allowed to call enabled write tools
can mutate the configured tenant. Keep the process, configuration, transport,
and client trust boundaries narrow.

## Credentials

- Supply the token through the process environment, a protected `.env` file,
  or the per-user installer. Never pass it as a command argument.
- Give the token only the permissions required for the intended workflow and
  rotate it after suspected disclosure.
- Use HTTPS for tenant traffic. Plain HTTP is suitable only for an isolated
  local fake server because bearer tokens are otherwise exposed in transit.
- Do not paste tokens, authorization headers, real comments, descriptions, or
  captured API responses into issues or test fixtures.
- Treat process inspection, crash dumps, shell tracing, and environment export
  tools as possible disclosure paths.

The tenant URL controls the network destination. Configure only a trusted URL;
the program is not a general-purpose URL sandbox.

## MCP writes

MCP starts read-only. `KAITEN_ENABLE_WRITE_TOOLS=true` changes the advertised
tool set and should be enabled only for a client and session that need writes.
Permanent delete, unlink, and membership removal are destructive operations.
Review tool arguments and the client's approval behavior before enabling them.

Writes are not retried automatically. A timeout can leave the remote outcome
uncertain; inspect the resource before deciding whether to repeat an operation.

## HTTP transport

The default bind address is `127.0.0.1`. Keep it loopback unless you have added
an appropriate external authentication, encryption, and network policy layer.
A non-loopback bind exposes a process that holds a bearer credential and emits
a security warning; it should never be treated as safe merely because it is on
a private network.

Loopback also does not isolate mutually untrusted processes running as the same
user. Prefer stdio when a single local MCP client can own the server process.

The health endpoint intentionally returns only `status`, `version`, and
`runtime`. It must not return configuration or credential state.

## Logs and diagnostics

Normal diagnostics go to stderr in CLI and stdio modes. Logs must not contain
tokens, authorization headers, card descriptions, comment bodies, installer
secrets, or full sensitive upstream responses.

Flight-recorder configuration is disabled by default and reserves age and byte
limits for builds that emit those diagnostics. Before enabling diagnostics in
such a build, verify where they are stored and who can read them. Diagnostic
output follows the same redaction requirements as normal logs.

## Installer

Installation is per-user and must not require administrator or root access.
Service and secret files use the narrowest user-only permissions available.
Updates are staged and activated only after validation; failed activation must
retain or restore the prior working installation whenever possible.

Third-party MCP client JSON is external state. Installer changes must be
optional, atomic, scoped to the `kaiten` entry, and preserve unrelated keys and
servers. Use release testing with an isolated temporary user profile, never a
maintainer's real client configuration.

## Supply chain

Release archives include `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, SHA-256
checksums, and SPDX JSON SBOMs. The release workflow produces a draft and
attests the checksum manifest. Verify the checksum before executing a download
and review the SBOM when policy requires it.

The runtime uses the Go standard library plus `golang.org/x/term` for hidden
terminal input and its `golang.org/x/sys` platform support dependency. Their
versions and BSD 3-Clause notice are recorded in the module files, SBOMs, and
`THIRD_PARTY_NOTICES.md`. CI runs static analysis, tests, race detection, and
cross-builds; release review must reconcile dependency licenses and notices.

The release toolchain is fixed at Go 1.26.5 and checked before every build.
CI scans both executables for every target, and the release gate repeats
binary-mode vulnerability scans on the exact executable bytes extracted from
the final archives. `go version -m` must identify Go 1.26.5 for every scanned
binary; a source-only vulnerability scan is not sufficient evidence.

Every external GitHub Action is pinned to a full immutable commit SHA with its
human-readable major version retained in a comment. Verification and build
jobs are read-only; attestation and draft publication use separate jobs with
only their required write scopes. A repository policy check and pinned
actionlint run validate these rules, while weekly Dependabot updates propose
reviewable action-pin changes.

The release build also runs twice from independent clean clones. Commit-derived
metadata is fixed for every archive member, and the gate requires identical
bytes for ten binaries, ten archives, ten normalized SPDX documents, and the
checksum manifest. Syft package evidence is validated before checksums; only
its volatile creation metadata is normalized under the documented
[reproducibility policy](reproducible-builds.md).

## Reporting a vulnerability

Use the repository's private security-advisory channel when available. Include
the affected version, operating system, reproduction steps, and impact, but no
real token or tenant data. Do not open a public issue for an unpatched secret
exposure or remote exploit.
