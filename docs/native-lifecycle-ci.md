# Native lifecycle CI

The manual `Native lifecycle` workflow is the release gate for executing the
per-user installer through the operating system's real activation mechanism.
It is deliberately `workflow_dispatch` only: each run creates services,
processes, and disposable user profiles and therefore must be tied to a reviewed
candidate commit. Dispatch requires both the exact lowercase 40-character head
SHA and the numeric ID of a successful, first-attempt `Release` workflow run.
The dispatch itself must select the same immutable release tag: the workflow
rejects any control-plane `github.sha` or `github.ref` that differs from the
requested SHA and the tag reported by the Release API.

Before any lifecycle state change, every matrix job authenticates to the GitHub
API with the job's read-only token and fails closed unless that run belongs to
this repository, is the `Release` tag-push workflow, completed successfully,
has the expected head SHA, and its live `refs/tags/v*` ref still resolves to that
commit. The only downloaded artifact is the run's unexpired `release-assets`.
Only `native-v1`, the bad `native-v3` rollback fixture, and the harness are
built locally; the healthy update candidate is always the downloaded release
`kaiten-mcp` beside its matching `kaiten` binary.

The authenticated download helper never executes artifact code. It requires
the downloaded ZIP size and SHA-256 to equal the GitHub API record, verifies
all manifest entries, enforces bounded link-free extraction, and binds each
binary to its exact main package, canonical module, clean candidate VCS
revision, Go 1.26.5, target platform, and archive-derived SHA-256. Execution
happens later in the token-free lifecycle step: the harness checks both hashes
before launch, bounds version output and time, and rechecks both files after
each smoke so one sibling cannot replace the other unnoticed.

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
Its exit trap fails closed unless the exact user manager and lingering state are
stopped, the dedicated UID has no process, port 8100 is free, the user/group and
login state are absent, and only the validated per-run `/tmp` stage is removed.
Those facts are retained in `linux-wrapper-cleanup.json`.
The harness rejects a runner label whose resolved `GOOS` or `GOARCH` differs
from this table, so a moving hosted alias cannot silently remove architecture
coverage.

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
6. validates every release checksum, safely extracts the exact full `kaiten`
   archive for the runner, smokes the sibling `kaiten` and `kaiten-mcp` version
   and Go platform identities, and performs a healthy update with those exact
   released `kaiten-mcp` bytes;
7. attempts an update to the repository's intentional `native-v3` no-health
   fixture, requires the installer to fail, and proves the executable and
   running health endpoint rolled back to the exact release version; executable,
   environment, and service-definition hashes must match the pre-failure state,
   and a new MCP call must authenticate successfully after rollback;
8. uninstalls twice, verifies client JSON preservation, checks permissions and
   owned-file scope, and requires the synthetic bearer to be absent everywhere
   except the restricted environment file before uninstall and everywhere
   afterward.

The no-health fixture is built only for this test. It is not a release artifact.

## Running and retaining evidence

Dispatch while the source `release-assets` artifact is still retained (seven
days in the Release workflow). With GitHub CLI, for example:

```sh
release_run_id=<successful-release-workflow-run-id>
expected_sha=<exact-lowercase-40-character-release-head-sha>
gh workflow run native-lifecycle.yml --ref <reviewed-workflow-ref> \
  -f expected_sha="$expected_sha" \
  -f release_run_id="$release_run_id"
native_run_id=<dispatched-native-lifecycle-run-id>
gh run watch "$native_run_id" --exit-status
gh run download "$native_run_id" --pattern 'native-lifecycle-*' --dir native-evidence
go run ./scripts/verify-native-lifecycle-evidence.go native-evidence <40-character-commit> \
  | tee native-evidence-verification.txt
```

`<reviewed-workflow-ref>` must be the exact Release tag, not a branch. A failed
or rerun Release workflow is not eligible because the artifact REST record does
not attest which rerun attempt produced it; create a new candidate instead.

Every matrix job uploads one `native-lifecycle-<target>` artifact. Its
mode-restricted `summary.json` records the commit, workflow run, runner image,
exact runner label and runtime architecture, Go version, fixture hashes,
version transitions, native-manager checks, permission result, client and
restricted-backup preservation, authorization proof, and final file inventory.
Companion files retain redacted installer commands, exact health responses,
native-manager status, before/after client JSON, permission results, rollback
hashes, the token-free service definition and log, MCP authorization results,
and the remaining-file list. Captured output is checked for the generated
bearer before redaction; every evidence file is also rejected if it contains
the bearer. A final upload-boundary sanitizer scans every regular evidence file,
rejects links, directories, unreviewed names, oversize files, credentials, and
mock response bodies, and prevents artifact upload if any check fails.

`wrapper-context.txt` retains a secret-free binding record for later audit
aggregation: source Release run and attempt, tag and head SHA, artifact ID,
artifact ZIP/API digest, checksum-manifest hash, selected full-archive name and
hash, exact release version/platform, and both extracted binary hashes. The API
token and signed artifact URL are never written to evidence.

Match all five passing summaries to the frozen commit and GitHub run before
qualifying a release. The aggregate verifier requires one exact artifact set
per runner, one workflow run/attempt, Go 1.26.5, complete companion evidence,
the reviewed version transitions, and no synthetic token, authorization header,
or mock tenant body. It also binds the API artifact digest, manifest/archive
hashes, and both helper-recorded binary hashes to the hashes independently
observed by the harness. A workflow file, cross-build, local unit test, or
preflight failure is not native execution evidence.

## Execution records

This repository defines the gate but does not self-certify a particular run.
The Release workflow creates only a draft; it does not make native success a
circular prerequisite of the artifact build. Candidate source and that draft
must remain in non-public staging until an external approval verifies the exact
Release run plus all five native artifacts for the same SHA. Only that approval
may qualify or publish the candidate.
The exact workflow URL, run ID, commit, five downloaded artifacts, verification
result, tester, and UTC decision belong in the dated external audit package for
each release. The workflow configuration alone must never be cited as passed
native lifecycle evidence.
