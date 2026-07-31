# Configuration

## Precedence and `.env` search

Configuration is resolved in this order, from highest to lowest priority:

1. MCP transport command-line flags;
2. process environment variables;
3. the first `.env` file found in the current working directory;
4. if different, the first `.env` file found beside the running executable;
5. documented defaults.

At most one `.env` file is loaded. Values already present in the process
environment are never overwritten. Within a source, a nonempty primary variable
wins over its alias.

The `.env` reader accepts blank lines, comments beginning with `#`, optional
`export `, and `KEY=value` entries. Matching single or double quotes around the
whole value are removed. Variable expansion is not performed.

## Variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `KAITEN_API_TOKEN` | required for API use | Primary bearer token. |
| `KAITEN_TOKEN` | none | Token alias, used only when the primary value is empty. |
| `KAITEN_URL` | required for API use | Absolute tenant `http` or `https` origin. HTTPS is recommended. |
| `KAITEN_BASE_URL` | none | URL alias, used only when the primary value is empty. |
| `KAITEN_API_PREFIX` | `/api/v1` | API path prefix, normalized to one leading slash and no trailing slash. |
| `KAITEN_RATE_LIMIT_RPS` | `3` | Positive finite local request rate per second. |
| `KAITEN_RATE_LIMIT` | none | Rate alias, used only when the primary variable is unset. |
| `KAITEN_CACHE_TTL_SECONDS` | `60` | Finite discovery-cache TTL at least zero; zero disables reuse. |
| `KAITEN_MAX_CONCURRENCY` | `5` | Maximum concurrent upstream requests; integer at least one. |
| `KAITEN_TIMEOUT_SECONDS` | `20` | Positive finite deadline for the whole API operation, including local concurrency/rate queues, retry waits, and HTTP. |
| `KAITEN_ENABLE_WRITE_TOOLS` | `false` | MCP write tools are registered only when the trimmed value equals `true`, case-insensitively. |
| `KAITEN_MCP_TRANSPORT` | `stdio` | `stdio` or `streamable-http`. |
| `KAITEN_MCP_HOST` | `127.0.0.1` | HTTP bind host; an empty value becomes loopback. |
| `KAITEN_MCP_PORT` | `8000` | HTTP port from 1 through 65535. |
| `KAITEN_MCP_STREAMABLE_HTTP_PATH` | `/mcp` | MCP path; an empty value becomes `/mcp` and a missing leading slash is added. |
| `KAITEN_TRACE_FLIGHT_RECORDER` | `false` | Enables optional bounded runtime diagnostics only when equal to `true`. |
| `KAITEN_TRACE_FLIGHT_RECORDER_MIN_AGE_SECONDS` | `5` | Non-negative finite diagnostic window. |
| `KAITEN_TRACE_FLIGHT_RECORDER_MAX_BYTES` | `8388608` | Positive integer diagnostic buffer limit. |

Invalid values stop startup before a listener is opened or an API request is
made.

## Tenant URL validation

The URL must be absolute, use `http` or `https`, and include a host. Embedded
credentials, non-root paths, query strings, and fragments are rejected. A
trailing slash is ignored. The API prefix is appended separately.

Use plain HTTP only for a controlled local fake server. Tokens sent over plain
HTTP are visible to the network path.

## MCP flag overrides

Both `kaiten mcp` and `kaiten-mcp` accept:

```text
--transport <stdio|streamable-http>
--host <bind-host>
--port <1..65535>
--streamable-http-path <path>
```

These flags override the corresponding MCP environment variables. An omitted
flag leaves the environment or default value unchanged.

## Example `.env`

```dotenv
KAITEN_URL=https://your-tenant.example
KAITEN_API_TOKEN=replace-with-a-real-token
KAITEN_CACHE_TTL_SECONDS=60
KAITEN_MAX_CONCURRENCY=5
KAITEN_ENABLE_WRITE_TOOLS=false
```

Keep `.env` outside version control and restrict it to the current user where
the platform supports file permissions. Prefer an operating-system credential
store or service-secret mechanism for a long-running installation.

## Installed service configuration

The interactive installer manages its own per-user configuration with narrow
permissions for both the current secret file and its recoverable backup, plus
an atomic replacement strategy. Do not copy the token into a system-wide
service definition. The installed service uses loopback port `8100` and path
`/mcp`, independent of the direct HTTP defaults above.
