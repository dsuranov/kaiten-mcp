# Clean-room provenance

Status: candidate provenance record; release approval still requires independent
verification

Handoff date: 2026-07-31

## Scope

This repository is a new implementation of externally observable CLI, REST,
MCP, configuration, installation, and release contracts. Clean-room authorship
is a release condition, not a claim of endorsement by Kaiten or of access to
non-public Kaiten material.

This file is the canonical in-repository provenance record. Operational release
checks remain documented separately in `docs/release-testing.md`.

## Sealed input ledger

The implementation team received four neutral handoff artifacts. Their names
and verified SHA-256 digests are:

| Artifact | SHA-256 |
| --- | --- |
| `CLEANROOM_HANDOFF.md` | `7bdfcff1849e6eb445a798c6e06cfbfbf5f19fa6c97c00b9579694fcb6be3508` |
| `CLI_CONTRACT.md` | `ff2fa19127989724ebd1d92489805ebdf2965299d61fbe8675cbd4b887f9a2a2` |
| `MCP_CONTRACT.json` | `126a0d4929064eb215738828ae4b607333fc53cdd693e2daf5f259292dc85d85` |
| `PRODUCT_SPEC.md` | `e6c0634d45e75e7107409219bb6bea58cfaf31ebb37c5a2f7e2926c10cbf17ea` |

The sealed corpus describes public names, input and output shapes, observable
state transitions, error behavior, safety outcomes, and acceptance criteria. It
does not prescribe source code, package layout, dependencies, internal
identifiers, algorithms, fixtures, or implementation architecture. The sealed
artifacts are not distributed in this repository.

## Allowed sources

Implementation and documentation were limited to:

- the sealed input ledger above;
- [Kaiten public developer documentation](https://developers.kaiten.ru/);
- the public [Model Context Protocol specification](https://modelcontextprotocol.io/specification/);
- public Go, operating-system, GitHub Actions, GoReleaser, and Syft
  documentation;
- public license texts and dependency license metadata;
- ordinary toolchain and dependency caches;
- newly authored fake servers, fixtures, tests, and audit checks derived from
  the neutral contract.

The Go standard library was independently selected as the only runtime module.
GoReleaser and Syft are pinned release-time tools and are not runtime
dependencies.

## Excluded sources

The implementation team did not inspect or use a legacy or reference
implementation's source, Git objects or history, tests, fixtures, binaries,
artifacts, generated graphs, package entries, internal documentation, CI
results, issues, cached snapshots, endpoints, or implementation-derived
guidance.

A compatibility discrepancy may return to implementation only as a neutral
description of observable inputs, outputs, status, or side effects. It must not
include source locations, code excerpts, internal identifiers, or advice based
on a previous implementation.

## Independent implementation record

The implementation team independently chose:

- Go with a standard-library-only runtime;
- two small executable entry points over shared internal packages;
- explicit boundaries for configuration, REST transport, domain resolution,
  pagination, caching, CLI rendering, MCP protocol handling, and per-user
  installation;
- deterministic fake HTTP services and temporary user profiles for tests;
- read-only MCP tools by default with explicit write-tool opt-in;
- bounded retries for reads and no automatic retries for writes;
- loopback HTTP defaults and token-free service definitions;
- GoReleaser archives, Syft SPDX JSON SBOMs, SHA-256 manifests, and draft
  releases.

These are authorship facts, not assertions that another implementation uses the
same design.

## History and freeze discipline

The repository began empty and was developed as an additive sequence of focused
commits. Candidate freezes use annotated local tags. A finding against a frozen
candidate is addressed with new commits and a new tag; existing commits and
candidate tags are not rewritten or moved.

External parity checks begin only after a candidate is frozen. The first
black-box remediation supplied to the implementation team contained only the
observable requirement that command-specific help exit successfully, remain
offline and side-effect free, and print usage instead of executing the command.
No source location, prior implementation detail, or suggested implementation
was supplied.

During post-freeze audit orchestration, a failed audit-tool invocation briefly
created one untracked cache directory. The orchestrator attributed and moved it
intact without changing the candidate commit or tag. The implementation team
did not inspect or use its contents. This event did not alter tracked material,
but it remains part of the retained audit evidence.

## Dependency and license record

`go list -m all` is expected to list only
`github.com/dsuranov/kaiten-mcp`. Project-authored material is offered under
Apache-2.0. Release archives carry `LICENSE`, `NOTICE`, and
`THIRD_PARTY_NOTICES.md`. Generated SBOMs and the module graph must agree before
release approval.

Public product and protocol names are used only to describe interoperability.
They do not imply ownership, sponsorship, or endorsement.

## Verification gates

Every frozen candidate must pass, at one commit:

- formatting, module verification, vet, unit tests, race tests, and measured
  coverage;
- all supported cross-builds for both executables;
- successful and schema-error paths for the complete MCP tool inventory;
- stdio and Streamable HTTP protocol smoke tests;
- offline CLI help, version, and completion behavior;
- API timeout, retry, cancellation, pagination, cache, and ambiguity tests;
- installer lifecycle, rollback, permission, merge, and scoped-cleanup tests;
- GoReleaser validation and snapshot packaging;
- archive composition, SHA-256 checksum, SPDX SBOM, and version-parity checks;
- clean Git status, tag-to-commit equality, zero remotes, repository integrity,
  and tracked-content path, secret, and provenance scans.

The detailed native lifecycle procedure is in `docs/release-testing.md`.
Automated tests and cross-builds do not replace real install, start, health,
update, rollback, and uninstall checks on macOS, Linux, and Windows.

## Release decision

The project remains **NO-GO** for publication until an independent verifier
records black-box contract results, a provenance and license auditor signs off,
CI passes at the frozen commit, and native lifecycle evidence exists for every
supported operating system. Release automation therefore creates drafts only;
publishing is a separate human decision.
