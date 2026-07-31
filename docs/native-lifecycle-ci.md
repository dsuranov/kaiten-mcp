# Native lifecycle CI

The manual `Native lifecycle` workflow is the release gate for executing the
per-user installer through the operating system's real activation mechanism.
It is deliberately `workflow_dispatch` only: each run creates services,
processes, and disposable user profiles and therefore must be tied to a reviewed
candidate commit.

## Hosted matrix

The workflow uses only these GitHub-hosted labels and never `self-hosted`:

| Evidence target | Runner label | Native activation |
| --- | --- | --- |
| macOS amd64 | `macos-15-intel` | `launchctl` in the current GUI domain |
| macOS arm64 | `macos-latest` | `launchctl` in the current GUI domain |
| Linux amd64 | `ubuntu-latest` | `systemd --user` over a real user DBus |
| Linux arm64 | `ubuntu-24.04-arm` | `systemd --user` over a real user DBus |
| Windows amd64 | `windows-latest` | the isolated profile's Windows Startup entry |

The Linux wrapper creates a dedicated user, starts that user's `user@.service`,
and runs the harness and installed service without root privileges. `sudo` is
used only by the runner wrapper to create and later remove that disposable user.

## Lifecycle proved by each job

Before changing state, the harness requires the fixed native service identity
to be absent and proves that `127.0.0.1:8100` is free. A collision fails the job;
the harness never stops an unknown service or listener.

The job then:

1. creates a mode-restricted synthetic profile and two client JSON fixtures
   containing unrelated keys and another MCP server;
2. starts a newly authored loopback mock API with a generated, non-production
   bearer value;
3. installs a healthy `native-v1` binary in default read-only mode and registers
   the `kaiten` client entry;
4. verifies exact-version health, performs MCP initialization and
   `get_current_user`, and proves the mock received the expected bearer header;
5. restarts through the native activation mechanism and rechecks health;
6. performs a healthy update to `native-v2`;
7. attempts an update to the repository's intentional `native-v3` no-health
   fixture, requires the installer to fail, and proves the executable and
   running health endpoint rolled back to `native-v2`;
8. uninstalls twice, verifies client JSON preservation, checks permissions and
   owned-file scope, and requires the synthetic bearer to be absent everywhere
   except the restricted environment file before uninstall and everywhere
   afterward.

The no-health fixture is built only for this test. It is not a release artifact.

## Running and retaining evidence

Dispatch the workflow at the exact candidate commit or reviewed branch. With
GitHub CLI, for example:

```sh
gh workflow run native-lifecycle.yml --ref <candidate-ref>
gh run watch <run-id> --exit-status
gh run download <run-id> --pattern 'native-lifecycle-*' --dir native-evidence
```

Every matrix job uploads one `native-lifecycle-<target>` artifact containing a
mode-restricted `summary.json`. It records the commit, exact runner label,
architecture, version transitions, native-manager checks, permission result,
client and restricted-backup preservation, authorization proof, and final file inventory. Captured
installer output is checked for the generated bearer and ephemeral paths are
redacted before evidence is written. The artifact itself is rejected if it
contains the bearer.

Match all five passing summaries to the frozen commit and GitHub run before
qualifying a release. A workflow file, cross-build, local unit test, or preflight
failure is not native execution evidence.

## Current execution status

As of 2026-07-31, authorship of this gate produced no clean private-repository
GitHub-hosted matrix run. That remote run and its five retained artifacts remain
pending; the workflow configuration alone must not be cited as a passed native
lifecycle gate.
