# kaiten-mcp

`kaiten-mcp` is an independent, local integration for the public Kaiten API. It
ships two native executables:

- `kaiten`, an automation-friendly command-line client that writes data results
  as JSON;
- `kaiten-mcp`, an MCP server executable also available through `kaiten mcp`.

MCP starts with read tools only. Write tools are not advertised unless the user
explicitly enables them.

## Supported releases

| Operating system | Architectures | Archive |
| --- | --- | --- |
| macOS | amd64, arm64 | `.tar.gz` |
| Linux | amd64, arm64 | `.tar.gz` |
| Windows | amd64 | `.zip` |

Each release contains a full `kaiten` archive with both executables and an
MCP-only `kaiten-mcp` archive. Releases also include SHA-256 checksums, SPDX JSON
SBOMs, license text, and third-party notices.

## Quick start

Download the archive for your operating system and architecture from the
project's Releases page, verify it against `checksums.txt`, and place the needed
executables on your `PATH`. See [Installation](docs/installation.md) for exact
commands and background-service setup.

Configure credentials in the process environment. Do not put the token in a
command argument or shell history.

```sh
export KAITEN_URL="https://your-tenant.example"
export KAITEN_API_TOKEN="your-token"

kaiten spaces list
kaiten cards get 123
```

Successful API commands print exactly one indented JSON value followed by a
newline. Errors go to stderr and return a nonzero status.

Start an MCP server over stdio:

```sh
kaiten-mcp
```

Or start Streamable HTTP on the loopback interface:

```sh
kaiten-mcp --transport streamable-http --host 127.0.0.1 --port 8000
```

The MCP endpoint is `http://127.0.0.1:8000/mcp` by default. In HTTP mode,
`GET /health` returns runtime and version information.

## Safe defaults

- MCP write tools are absent by default. Set
  `KAITEN_ENABLE_WRITE_TOOLS=true` only when writes are required.
- HTTP binds to loopback by default.
- Read requests may be retried within bounded limits; writes are never retried
  automatically.
- Tokens, authorization headers, card descriptions, and comment bodies are not
  intended for logs.
- Per-user installation does not require administrator or root privileges.

See [Security](docs/security.md) before enabling write tools or exposing the
HTTP transport beyond loopback.

## Commands

Use generated help as the source of truth for the installed version:

```sh
kaiten --help
kaiten cards create --help
kaiten completion zsh
kaiten --version
kaiten-mcp version
```

The CLI covers spaces, boards, columns, lanes, cards, comments, blockers,
members, tags, checklists, and MCP lifecycle commands. See [Usage](docs/usage.md)
for examples.

## Build and test

Go 1.26.0 or newer is required by the module. Release and acceptance builds
must use the pinned patched toolchain, Go 1.26.5.

```sh
go test ./...
go test -race ./...
go vet ./...
./scripts/verify-go-toolchain.sh
go build ./cmd/kaiten
go build ./cmd/kaiten-mcp
```

CI additionally checks formatting, coverage, all supported cross-builds,
release configuration, and byte reproducibility across two independent clean
builds. A separate manually dispatched [native lifecycle gate](docs/native-lifecycle-ci.md)
executes install, restart, update rollback, and uninstall through GitHub-hosted
operating-system service managers. Tagged builds use GoReleaser and are created
as draft releases until the release audit is complete.

## Documentation

- [Architecture](docs/architecture.md)
- [Installation](docs/installation.md)
- [Configuration](docs/configuration.md)
- [Usage](docs/usage.md)
- [Security](docs/security.md)
- [Release testing](docs/release-testing.md)
- [Native lifecycle CI](docs/native-lifecycle-ci.md)
- [Reproducible builds](docs/reproducible-builds.md)
- [Clean-room provenance](PROVENANCE.md)
- [Provenance audit checklist](docs/clean-room-provenance.md)

Public protocol references:

- [Kaiten developer documentation](https://developers.kaiten.ru/)
- [Model Context Protocol specification](https://modelcontextprotocol.io/specification/)

## License and independence

The independently authored project is licensed under the Apache License 2.0.
See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

This project is not an official Kaiten distribution. The product name is used
only to describe interoperability with its public API.
