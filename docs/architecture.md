# Architecture

## Purpose

The project provides one Kaiten API boundary to two local interfaces: a JSON
CLI and an MCP server. The design keeps protocol adaptation separate from
configuration, request control, domain operations, and per-user installation.

```text
CLI caller                    MCP client
    |                             |
    v                             v
argument parser             MCP transport + schemas
    |                             |
    +----------> domain operations <----------+
                     |
             resolution / pagination
                     |
             cache / rate / concurrency
                     |
             authenticated HTTP client
                     |
                     v
              public Kaiten REST API

per-user installer --> background kaiten-mcp --> same MCP transport path
```

## Entry points

`cmd/kaiten` builds the general CLI. It validates command syntax, distinguishes
omitted update fields from explicit values, prints API results as indented JSON,
and delegates `kaiten mcp` to the shared MCP lifecycle.

`cmd/kaiten-mcp` builds the MCP-only compatibility executable. Its server,
install, uninstall, and version behavior is shared with `kaiten mcp`; it does
not expose the other CLI command groups.

Both binaries read the same version variables injected by the release build.

## Runtime boundaries

### Configuration

Configuration is loaded and fully validated before opening an HTTP listener or
sending an API request. Process variables take precedence over one optional
`.env` file, and MCP transport flags take precedence over both. Configuration
contains secrets and is not exposed through public result objects.

### CLI adapter

The CLI adapter owns argument count, flag spelling, mutually exclusive
selectors, numeric ID, enumeration, and date validation. Validation that does
not require discovery completes before configuration makes a remote call.
Stdout is reserved for the successful JSON value; diagnostics use stderr.

### MCP adapter

The MCP adapter owns protocol negotiation, stdio and Streamable HTTP framing,
tool registration, JSON Schemas, annotations, and result envelopes. Read-only
startup registers 18 tools. Explicit write enablement registers 15 more.
Domain failures are returned as data-level tool results; malformed protocol
requests remain protocol errors.

### Domain operations

Domain operations express cards, boards, users, checklists, relations, and
other public resources without coupling callers to raw URL assembly. Resource
resolution is deterministic: numeric ID, case-insensitive exact match, then a
unique case-insensitive substring. Ambiguity stops before mutation.

Pagination has bounded exit conditions. A requested limit of zero returns an
empty collection without a request. Discovery caching coalesces concurrent
misses and a zero TTL disables reuse.

### HTTP boundary

The HTTP client is the only component that attaches the bearer token. It sets
JSON headers and applies whole-operation deadlines, a local rate gate, and a
concurrency limit. Canceled rate waiters do not reserve future capacity. Only
idempotent reads are eligible for bounded retries. Mutations are sent at most
once.

### Installer boundary

The interactive installer plans and applies per-user filesystem and service
changes. It validates inputs first, writes configuration atomically, preserves
a recoverable prior installation during update, waits for bounded health
readiness, and changes third-party client configuration only as an optional,
scoped step.

## Trust boundaries

- The Kaiten token crosses only from local configuration to the HTTPS
  `Authorization` header.
- CLI and MCP input is untrusted and validated before it reaches mutation code.
- Kaiten responses are untrusted JSON; status, decoding, and shape failures are
  mapped to sanitized errors.
- A loopback listener is local by default but is not an authentication boundary
  against every process running as the same user.
- Client configuration and user service definitions are external state;
  updates must preserve unrelated content and support rollback.

## Concurrency and cancellation

One cancellation context flows from process signals or transport shutdown to
pagination, rate waits, cache loads, and HTTP requests. The server must stop
without leaving its listener bound. Mutable shared state is limited to bounded
request coordination and cache entries, and is exercised with the Go race
detector in CI.

## Release architecture

GoReleaser cross-builds both entry points with `CGO_ENABLED=0`, trimmed source
paths, embedded version/commit/date values, and commit-derived timestamps.
Archives contain user documentation and legal notices with fixed member
metadata. Syft package evidence receives deterministic SPDX creation metadata
before SHA-256 checksums are generated. Two independent builds must match byte
for byte before a draft release is eligible for the native installer and
provenance gates.
