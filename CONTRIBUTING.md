# Contributing

Issues, focused pull requests, native-platform reproductions, documentation
improvements, and test cases are welcome.

The highest-priority platform work is:

- [Linux native installer qualification](https://github.com/dsuranov/kaiten-mcp/issues/2);
- [Windows native installer qualification](https://github.com/dsuranov/kaiten-mcp/issues/3).

Please keep a change narrow and explain the externally observable behavior it
fixes. For native lifecycle work, include the operating-system version and
architecture, the exact project release or commit, redacted reproduction steps,
and proof that cleanup leaves no owned service, process, listener, or temporary
profile behind.

Before opening a pull request, run:

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go run ./scripts/verify-workflows.go
go run ./scripts/verify-dependency-policy.go
```

Never submit a Kaiten tenant URL, API token, authorization header, entity body,
private configuration, or captured production response. Use a loopback mock and
synthetic credentials for tests. MCP write tools must remain opt-in, and tests
must not send writes to a real tenant.

By contributing, you agree that your contribution is provided under this
project's [Apache License 2.0](LICENSE).
