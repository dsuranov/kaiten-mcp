# Clean-room provenance

The canonical project provenance record is [PROVENANCE.md](../PROVENANCE.md).
This companion document retains the pre-publication audit checklist.

Status: durable audit procedure; release status is recorded in dated external
evidence for the exact tagged commit

Handoff date: 2026-07-31

## Purpose

This project is a new implementation of externally observable CLI, API, MCP,
configuration, and installer contracts. Independent authorship is a release
condition. This record identifies the implementation inputs and the audit gate;
it does not by itself certify that the gate has passed.

## Sealed input corpus

The implementation team received only four neutral handoff artifacts. The
sealed files supplied to the team have these verified SHA-256 digests:

| Artifact | SHA-256 |
| --- | --- |
| `CLEANROOM_HANDOFF.md` | `7bdfcff1849e6eb445a798c6e06cfbfbf5f19fa6c97c00b9579694fcb6be3508` |
| `CLI_CONTRACT.md` | `ff2fa19127989724ebd1d92489805ebdf2965299d61fbe8675cbd4b887f9a2a2` |
| `MCP_CONTRACT.json` | `126a0d4929064eb215738828ae4b607333fc53cdd693e2daf5f259292dc85d85` |
| `PRODUCT_SPEC.md` | `e6c0634d45e75e7107409219bb6bea58cfaf31ebb37c5a2f7e2926c10cbf17ea` |

The handoff specifies public names, schemas, result shapes, state transitions,
errors, safety outcomes, and acceptance criteria. It does not prescribe source
code, dependencies, package layout, identifiers, fixtures, or internal
architecture.

Any clarification after those hashes must be a dated, neutral amendment with a
new digest. Silent replacement of an input invalidates this record.

## Allowed references

Implementation and documentation may use only:

- the sealed corpus above;
- [Kaiten's public developer documentation](https://developers.kaiten.ru/);
- the public [Model Context Protocol specification](https://modelcontextprotocol.io/specification/);
- public Go, operating-system, packaging, GitHub Actions, GoReleaser, and Syft
  documentation selected independently;
- public license texts and dependency license metadata;
- newly authored fake servers, fixtures, and tests derived from neutral
  contracts.

The Go standard library and public `golang.org/x/term` v0.45.0 module were
independently selected for the runtime; `golang.org/x/sys` v0.47.0 is its
platform support dependency. The external module is used only for non-echoing
terminal input. GoReleaser and Syft are release-time tools, not runtime
services.

Go 1.26.5 was independently selected from the official stable releases as the
patched release toolchain. Repository checks reject another compiler version,
and release acceptance inspects and vulnerability-scans the executable bytes
extracted from every final archive.

Release reproducibility is checked from two independent clean local clones.
All archive metadata is fixed from the source commit; Syft remains the package
evidence producer, while a separately identified normalizer replaces only
volatile SPDX creation metadata under a documented versioned policy before the
checksum manifest is created.

## Excluded material

The implementation team must not access any previous implementation source,
Git history, tests, fixtures, artifacts, package registry entries, generated
graphs, documentation, CI results, issue history, cached snapshots, or
implementation-informed advice. It also must not execute or query a previous
binary or endpoint before the candidate is frozen.

If accidental exposure occurs, affected work must stop, the event must be
recorded, and that portion must be discarded and restarted in a clean workspace
by an uncontaminated implementer.

## Independent design decisions

Choices made without implementation precedent include:

- Go with a small runtime dependency set and `golang.org/x/term` for hidden
  interactive secret input;
- two thin executable entry points around shared local behavior;
- explicit boundaries for configuration, CLI, MCP, API requests, domain
  resolution, caching, pagination, and installer state;
- newly authored deterministic fake API tests;
- GoReleaser archives, Syft SPDX JSON SBOMs, SHA-256 manifests, and draft GitHub
  releases.

These choices are implementation details, not compatibility claims.

## Pre-publication audit

An auditor who was not the primary implementer must retain evidence that:

- [ ] the repository began empty and the development history is genuine;
- [ ] every source, test, document, workflow, and asset is independently
      authored or comes from a clearly licensed public source;
- [ ] no forbidden remote, object, tag, internal reference, private domain,
      tenant data, secret, local absolute path, or prior branding is present;
- [ ] text similarity findings beyond unavoidable public identifiers are
      investigated and resolved;
- [ ] `go list -m all`, third-party notices, dependency licenses, and generated
      SBOMs agree;
- [ ] CLI, MCP, API robustness, installer, and release acceptance gates pass at
      one frozen commit;
- [ ] native release testing evidence exists for every target claimed stable,
      while beta targets are disclosed and linked to public tracking issues;
- [ ] Apache-2.0 is applied only to independently authored project material.

External parity verification may begin only after the candidate commit is
frozen. The verifier may compare public process boundaries, but discrepancies
must return to implementation only as neutral, dated input/output amendments.

## Release decision

Publication is permitted only when dated external evidence for the exact tagged
commit records the access-boundary certification, external contract results,
provenance and license sign-off, CI success, and native tests for every target
claimed stable. Beta targets require explicit release-note disclosure and a
public issue. This procedure does not self-certify any run. The automated
workflow creates a draft release; publishing remains a separate human decision
recorded with the release-specific evidence package.
