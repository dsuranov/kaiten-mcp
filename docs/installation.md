# Installation

## Choose an artifact

Releases provide full `kaiten` and MCP-only `kaiten-mcp` archives for:

- macOS amd64 and arm64;
- Linux amd64 and arm64;
- Windows amd64.

The `kaiten` archive contains both sibling executables so `kaiten mcp install`
can install the background MCP service. Use the `kaiten-mcp` archive when only
the MCP executable is needed.

## Verify the download

Download the selected archive and `checksums.txt` from the same release. On
Linux, verify a single file with:

```sh
sha256sum --check --ignore-missing checksums.txt
```

On macOS:

```sh
shasum -a 256 <downloaded-archive>
grep '<downloaded-archive>' checksums.txt
```

On Windows PowerShell:

```powershell
Get-FileHash .\<downloaded-archive> -Algorithm SHA256
Select-String -Path .\checksums.txt -Pattern '<downloaded-archive>'
```

Compare the complete hash, not only a prefix. Release assets also include SPDX
JSON SBOMs for inventory review.

## Install a standalone executable

Extract the archive and move the executable to a directory already on your
user `PATH`. Administrator or root privileges are not required.

Example for a user-owned Unix directory:

```sh
mkdir -p "$HOME/.local/bin"
install -m 0755 ./kaiten ./kaiten-mcp "$HOME/.local/bin/"
"$HOME/.local/bin/kaiten" --version
```

Keep `kaiten-mcp` beside `kaiten` if you plan to invoke the installer through
`kaiten mcp install`. For the MCP-only archive, install just `kaiten-mcp`. On
Windows, place the `.exe` files in a user-owned directory and add that directory
to the user `PATH`, then run:

```powershell
kaiten.exe --version
kaiten-mcp.exe version
```

## Build from source

With Go 1.23 or newer:

```sh
go build -trimpath -o ./bin/kaiten ./cmd/kaiten
go build -trimpath -o ./bin/kaiten-mcp ./cmd/kaiten-mcp
```

Development builds report version `dev` unless release metadata is injected.

## Per-user background service

Either command starts the same interactive installer:

```sh
kaiten mcp install
# or
kaiten-mcp install
```

The installer:

1. validates the tenant URL and obtains a token without displaying it (terminal
   echo is disabled for an interactive prompt; non-terminal automation reads
   one deterministic line when no environment token is set);
2. asks whether MCP write tools should be enabled; the default is read-only;
3. installs a user-owned executable and user-level service definition;
4. starts the service and waits for bounded health readiness from the exact
   version being installed;
5. optionally adds a server named `kaiten` to a supported MCP client while
   preserving unrelated configuration.

The installed endpoint is `http://127.0.0.1:8100/mcp`; health is available at
`http://127.0.0.1:8100/health`. The installer reports the platform-specific log
location. A failed activation returns nonzero and retains or restores the prior
working installation whenever possible.

An existing installation offers update, reinstall, or cancel. Update stops the
old service before replacing files, starts only the newly installed binary, and
preserves a recoverable previous executable and configuration until the new
version is confirmed. A failed write, activation, or readiness check restores
and reactivates the previous files; any restoration or reactivation failure is
included in the returned error.

## Uninstall

```sh
kaiten mcp uninstall
# or
kaiten-mcp uninstall
```

Uninstall stops and disables only this user's service, removes only files owned
by this product, and can optionally remove only the `kaiten` entry from a client
configuration. Logs are preserved by default and their location is printed.
Running uninstall again is safe. Incomplete cleanup returns nonzero and lists
remaining paths without printing secrets.

## Upgrade safety

Before upgrading, verify the new release checksum and review its release notes.
Do not replace a running installed executable manually; use the installer so
service activation and rollback are coordinated. Release qualification includes
a native install/start/health/update/uninstall cycle on every supported
operating system; see [Release testing](release-testing.md).
