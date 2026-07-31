//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

var aggregateTestCommit = strings.Repeat("a", 40)

func TestRealisticFiveDirectoryBundlePassesAggregateChecks(t *testing.T) {
	root := writeValidBundle(t)
	identity, err := validateBundle(root, aggregateTestCommit)
	if err != nil {
		t.Fatalf("valid five-directory evidence bundle failed: %v", err)
	}
	if identity.workflowRunID != "456" || identity.workflowRunAttempt != 2 || identity.releaseCommon == "" {
		t.Fatalf("unexpected aggregate identity: %+v", identity)
	}
}

func TestReviewedReleaseWorkflowPathFormsPass(t *testing.T) {
	for _, workflowPath := range []string{
		".github/workflows/release.yml",
		".github/workflows/release.yml@v1.2.3",
		".github/workflows/release.yml@refs/tags/v1.2.3",
	} {
		workflowPath := workflowPath
		t.Run(workflowPath, func(t *testing.T) {
			root := writeValidBundle(t)
			for _, reviewed := range reviewedTargets {
				path := filepath.Join(root, reviewed.directory, "wrapper-context.txt")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				updated := strings.Replace(string(data), "binding_release_workflow_path=.github/workflows/release.yml\n", "binding_release_workflow_path="+workflowPath+"\n", 1)
				writeTestText(t, path, updated)
			}
			if _, err := validateBundle(root, aggregateTestCommit); err != nil {
				t.Fatalf("reviewed workflow path form failed: %v", err)
			}
		})
	}
}

func TestReleaseAttemptMustBeFirstAttempt(t *testing.T) {
	root := writeValidBundle(t)
	path := linuxArtifact(root, "wrapper-context.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTestText(t, path, strings.Replace(string(data), "binding_release_run_attempt=1\n", "binding_release_run_attempt=2\n", 1))
	if _, err := validateBundle(root, aggregateTestCommit); err == nil {
		t.Fatal("aggregate verifier accepted a rerun Release workflow attempt")
	}
}

func TestEverySemanticArtifactIsRequiredToRemainMeaningful(t *testing.T) {
	names := []string{"summary.json", "wrapper-context.txt"}
	names = append(names, nativeci.RequiredEvidenceArtifacts()...)
	names = append(names, "linux-wrapper-cleanup.json")
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			root := writeValidBundle(t)
			path := filepath.Join(root, "native-lifecycle-linux-amd64", name)
			switch name {
			case "service-log.txt":
				writeTestText(t, path, "native-ci-"+strings.Repeat("f", 64)+"\n")
			case "service-definition.txt", "manager-install-v1.txt", "manager-restart-v1.txt", "manager-update-v2.txt", "manager-rollback-v2.txt", "manager-final.txt", "wrapper-context.txt":
				writeTestText(t, path, "semantically-empty\n")
			default:
				writeTestText(t, path, "{}\n")
			}
			if _, err := validateBundle(root, aggregateTestCommit); err == nil {
				t.Fatalf("aggregate verifier accepted mutated %s", name)
			}
		})
	}
}

func TestAggregateRejectsSemanticFalsePositiveMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "client unrelated sentinel changed",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "clients-registered-v2.json")
				var state map[string]any
				readTestJSON(t, path, &state)
				state["claude_code"].(map[string]any)["unrelated"].(map[string]any)["preserve"] = false
				writeTestJSON(t, path, state)
			},
		},
		{
			name: "v3 arbitrary failure",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "commands.json")
				var commands []map[string]any
				readTestJSON(t, path, &commands)
				commands[2]["stderr"] = "error: filesystem unavailable"
				writeTestJSON(t, path, commands)
			},
		},
		{
			name: "health endpoint changed",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "health-rollback-v2.json")
				var health map[string]any
				readTestJSON(t, path, &health)
				health["endpoint"] = "http://127.0.0.1:9999/health"
				writeTestJSON(t, path, health)
			},
		},
		{
			name: "restart PID unchanged",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "manager-states.json")
				var states []map[string]any
				readTestJSON(t, path, &states)
				states[1]["pid"] = states[0]["pid"]
				writeTestJSON(t, path, states)
			},
		},
		{
			name: "write tool advertised",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "mcp-auth-v1.json")
				var proof map[string]any
				readTestJSON(t, path, &proof)
				proof["tool_names"] = append(proof["tool_names"].([]any), "create_card")
				proof["read_only_tool_count"] = float64(19)
				writeTestJSON(t, path, proof)
			},
		},
		{
			name: "Unix permission broadened",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "permissions.json")
				var permissions []map[string]any
				readTestJSON(t, path, &permissions)
				permissions[0]["mode"] = "0755"
				writeTestJSON(t, path, permissions)
			},
		},
		{
			name: "rollback binary detached from release",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "rollback-hashes.json")
				var hashes map[string]map[string]string
				readTestJSON(t, path, &hashes)
				hashes["before_failed_update"]["binary"] = strings.Repeat("e", 64)
				hashes["after_rollback"]["binary"] = strings.Repeat("e", 64)
				writeTestJSON(t, path, hashes)
			},
		},
		{
			name: "remaining owned binary added",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "remaining-files.json")
				var remaining []string
				readTestJSON(t, path, &remaining)
				remaining = append(remaining, ".local/share/kaiten-mcp/bin/kaiten-mcp")
				writeTestJSON(t, path, remaining)
			},
		},
		{
			name: "release binary did not launch",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "release-binaries.json")
				var proof map[string]any
				readTestJSON(t, path, &proof)
				proof["binaries"].([]any)[1].(map[string]any)["launched"] = false
				writeTestJSON(t, path, proof)
			},
		},
		{
			name: "artifact API digest detached from downloaded ZIP",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "wrapper-context.txt")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				writeTestText(t, path, strings.Replace(string(data), "binding_release_artifact_api_digest=sha256:"+strings.Repeat("b", 64), "binding_release_artifact_api_digest=sha256:"+strings.Repeat("c", 64), 1))
			},
		},
		{
			name: "post cleanup incomplete",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "post-harness-cleanup.json")
				var proof map[string]any
				readTestJSON(t, path, &proof)
				proof["port_8100_free"] = false
				writeTestJSON(t, path, proof)
			},
		},
		{
			name: "Linux wrapper cleanup incomplete",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "linux-wrapper-cleanup.json")
				var proof map[string]any
				readTestJSON(t, path, &proof)
				proof["user_deleted"] = false
				writeTestJSON(t, path, proof)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := writeValidBundle(t)
			test.mutate(t, root)
			if _, err := validateBundle(root, aggregateTestCommit); err == nil {
				t.Fatal("aggregate verifier accepted semantic mutation")
			}
		})
	}
}

func TestAggregateRejectsExactRootExtras(t *testing.T) {
	root := writeValidBundle(t)
	writeTestText(t, filepath.Join(root, "unreviewed.txt"), "stale evidence\n")
	if _, err := validateBundle(root, aggregateTestCommit); err == nil {
		t.Fatal("aggregate verifier accepted an extra root file")
	}
}

func TestAggregateRejectsArtifactSymlink(t *testing.T) {
	root := writeValidBundle(t)
	external := filepath.Join(t.TempDir(), "commands.json")
	writeTestJSON(t, external, []any{})
	linked := linuxArtifact(root, "commands.json")
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, linked); err != nil {
		t.Skipf("symlink unavailable on this runner: %v", err)
	}
	if _, err := validateBundle(root, aggregateTestCommit); err == nil {
		t.Fatal("aggregate verifier followed an artifact symlink")
	}
}

func TestStrictJSONRejectsUnknownTrailingAndDuplicateFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "unknown",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "health-install-v1.json")
				var health map[string]any
				readTestJSON(t, path, &health)
				health["unknown"] = true
				writeTestJSON(t, path, health)
			},
		},
		{
			name: "trailing",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "health-install-v1.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				writeTestText(t, path, string(data)+"{}\n")
			},
		},
		{
			name: "duplicate",
			mutate: func(t *testing.T, root string) {
				path := linuxArtifact(root, "health-install-v1.json")
				writeTestText(t, path, `{"endpoint":"http://127.0.0.1:8100/health","status":"ok","status":"ok","version":"native-v1","runtime":"go1.26.5"}`+"\n")
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := writeValidBundle(t)
			test.mutate(t, root)
			if _, err := validateBundle(root, aggregateTestCommit); err == nil {
				t.Fatal("aggregate verifier accepted non-strict JSON")
			}
		})
	}
}

func writeValidBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for index, reviewed := range reviewedTargets {
		writeValidArtifactDirectory(t, root, reviewed, index)
	}
	return root
}

func writeValidArtifactDirectory(t *testing.T, root string, reviewed target, index int) {
	t.Helper()
	directory := filepath.Join(root, reviewed.directory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	mcpHash := repeatedHex(index + 1)
	kaitenHash := repeatedHex(index + 6)
	archiveHash := repeatedHex(index + 11)
	version := "1.2.3"
	archiveExtension := ".tar.gz"
	kaitenFile, mcpFile := "kaiten", "kaiten-mcp"
	if reviewed.goos == "windows" {
		archiveExtension, kaitenFile, mcpFile = ".zip", "kaiten.exe", "kaiten-mcp.exe"
	}
	archive := fmt.Sprintf("kaiten_%s_%s_%s%s", version, reviewed.goos, reviewed.goarch, archiveExtension)

	required := nativeci.RequiredEvidenceArtifacts()
	sort.Strings(required)
	checks := make([]checkRecord, 0, len(expectedCheckNames))
	for _, name := range expectedCheckNames {
		detail := "reviewed check passed"
		if name == "fixture-sha256" {
			detail = fmt.Sprintf("v1=%s; v2=%s; v3-no-health=%s", strings.Repeat("0", 64), mcpHash, strings.Repeat("f", 64))
		}
		checks = append(checks, checkRecord{Name: name, Status: "passed", Detail: detail})
	}
	summary := summaryRecord{
		Schema: "kaiten-native-lifecycle/v2", Result: "passed", RunnerLabel: reviewed.runnerLabel,
		RunnerImageOS: "hosted-image", RunnerImageVersion: "20260731.1", GOOS: reviewed.goos, GOARCH: reviewed.goarch,
		GoVersion: goVersion, Commit: aggregateTestCommit, WorkflowRunID: "456", WorkflowRunAttempt: 2,
		StartedUTC: time.Unix(1, 0).UTC().Format(time.RFC3339), FinishedUTC: time.Unix(2, 0).UTC().Format(time.RFC3339),
		Artifacts: required, Checks: checks,
	}
	writeTestJSON(t, filepath.Join(directory, "summary.json"), summary)

	contextValues := map[string]string{
		"runner_label": reviewed.runnerLabel, "runner_id": reviewed.runnerID, "runner_os": reviewed.runnerOS,
		"candidate_commit": aggregateTestCommit, "workflow_run_id": "456", "workflow_run_attempt": "2",
		"binding_schema": "kaiten-native-release-binding/v1", "binding_release_repository": "dsuranov/kaiten-mcp",
		"binding_release_repository_id": "987", "binding_release_run_id": "123", "binding_release_run_attempt": "1",
		"binding_release_workflow": "Release", "binding_release_workflow_path": ".github/workflows/release.yml",
		"binding_release_event": "push", "binding_release_conclusion": "success", "binding_release_tag": "v" + version,
		"binding_release_head_sha": aggregateTestCommit, "binding_release_artifact_id": "789",
		"binding_release_artifact_name": "release-assets", "binding_release_artifact_size": "123456",
		"binding_release_artifact_api_digest": "sha256:" + strings.Repeat("b", 64),
		"binding_release_artifact_zip_sha256": strings.Repeat("b", 64),
		"binding_release_manifest_sha256":     strings.Repeat("a", 64), "binding_release_archive": archive,
		"binding_release_archive_sha256": archiveHash, "binding_release_version": version,
		"binding_release_goos": reviewed.goos, "binding_release_goarch": reviewed.goarch, "binding_release_go_version": goVersion,
		"binding_release_kaiten": kaitenFile, "binding_release_kaiten_sha256": kaitenHash,
		"binding_release_kaiten_mcp": mcpFile, "binding_release_kaiten_mcp_sha256": mcpHash,
	}
	var wrapper strings.Builder
	for _, key := range wrapperKeys {
		fmt.Fprintf(&wrapper, "%s=%s\n", key, contextValues[key])
	}
	writeTestText(t, filepath.Join(directory, "wrapper-context.txt"), wrapper.String())

	before := testClientState(false)
	registered := testClientState(true)
	writeTestJSON(t, filepath.Join(directory, "clients-before-install.json"), before)
	writeTestJSON(t, filepath.Join(directory, "clients-registered-v1.json"), registered)
	writeTestJSON(t, filepath.Join(directory, "clients-registered-v2.json"), registered)
	writeTestJSON(t, filepath.Join(directory, "clients-after-rollback.json"), registered)
	writeTestJSON(t, filepath.Join(directory, "clients-after-uninstall.json"), before)

	binaryCommand := "kaiten-mcp"
	if reviewed.goos == "windows" {
		binaryCommand += ".exe"
	}
	writeTestJSON(t, filepath.Join(directory, "commands.json"), []commandEvidence{
		{Step: "install-v1", Command: binaryCommand + " install", ExitCode: 0, DurationNS: int64(time.Second), Stdout: "installed native-v1"},
		{Step: "healthy-update-v2", Command: binaryCommand + " install", ExitCode: 0, DurationNS: int64(time.Second), Stdout: "installed shipped v2"},
		{Step: "bad-service-update-v3", Command: binaryCommand + " install", ExitCode: 1, DurationNS: int64(10 * time.Second), Stderr: `error: installation failed: verify installed service version "native-v3": service did not become healthy before the readiness deadline`},
		{Step: "uninstall-first", Command: binaryCommand + " uninstall", ExitCode: 0, DurationNS: int64(time.Second), Stdout: "uninstalled"},
		{Step: "uninstall-second", Command: binaryCommand + " uninstall", ExitCode: 0, DurationNS: int64(time.Second), Stdout: "already absent"},
	})
	for _, health := range []struct{ name, version string }{
		{"health-install-v1.json", "native-v1"}, {"health-restart-v1.json", "native-v1"},
		{"health-update-v2.json", version}, {"health-rollback-v2.json", version},
	} {
		writeTestJSON(t, filepath.Join(directory, health.name), healthEvidence{Endpoint: healthEndpoint, Status: "ok", Version: health.version, Runtime: goVersion})
	}

	pids := []int{1001, 1002, 1003, 1004}
	rawNames := []string{"manager-install-v1.txt", "manager-restart-v1.txt", "manager-update-v2.txt", "manager-rollback-v2.txt"}
	stages := []string{"install-v1", "restart-v1", "update-v2", "rollback-v2"}
	states := make([]map[string]any, 0, 5)
	for stateIndex, stage := range stages {
		writeTestText(t, filepath.Join(directory, rawNames[stateIndex]), testManagerRaw(reviewed, pids[stateIndex]))
		states = append(states, map[string]any{"stage": stage, "manager": reviewed.manager, "identity": reviewed.identity, "active": true, "pid": pids[stateIndex]})
	}
	states = append(states, map[string]any{"stage": "final", "manager": reviewed.manager, "identity": reviewed.identity, "active": false, "pid": 0})
	writeTestJSON(t, filepath.Join(directory, "manager-states.json"), states)
	writeTestText(t, filepath.Join(directory, "manager-final.txt"), fmt.Sprintf("manager=%s\nidentity=%s\nstate=absent\nexecutable=%s\nprocess=absent\nport_8100=free\n", reviewed.manager, reviewed.identity, expectedProfileBinary(reviewed.goos)))

	writeTestJSON(t, filepath.Join(directory, "mcp-auth-v1.json"), testMCPProof("install-v1", "native-v1", 1))
	writeTestJSON(t, filepath.Join(directory, "mcp-auth-rollback-v2.json"), testMCPProof("rollback-v2", version, 2))
	permissions := testPermissions(reviewed.goos)
	writeTestJSON(t, filepath.Join(directory, "permissions.json"), permissions)
	writeTestJSON(t, filepath.Join(directory, "rollback-backup-permissions.json"), permissions)
	writeTestJSON(t, filepath.Join(directory, "rollback-hashes.json"), map[string]any{
		"before_failed_update": map[string]string{"binary": mcpHash, "environment": strings.Repeat("c", 64), "service_definition": strings.Repeat("d", 64)},
		"after_rollback":       map[string]string{"binary": mcpHash, "environment": strings.Repeat("c", 64), "service_definition": strings.Repeat("d", 64)},
	})
	writeTestJSON(t, filepath.Join(directory, "release-binaries.json"), map[string]any{
		"schema": "kaiten-native-release-binaries/v1", "release_run_id": "123", "release_run_attempt": "1",
		"release_tag": "v" + version, "release_head_sha": aggregateTestCommit,
		"release_manifest_sha256": strings.Repeat("a", 64), "release_archive": archive,
		"release_archive_sha256": archiveHash, "release_artifact_name": "release-assets",
		"binaries": []map[string]any{
			{"name": "kaiten", "sha256": kaitenHash, "version_output": "kaiten " + version, "go_version": goVersion, "goos": reviewed.goos, "goarch": reviewed.goarch, "launched": true, "exit_code": 0},
			{"name": "kaiten-mcp", "sha256": mcpHash, "version_output": "kaiten-mcp " + version, "go_version": goVersion, "goos": reviewed.goos, "goarch": reviewed.goarch, "launched": true, "exit_code": 0},
		},
	})
	writeTestJSON(t, filepath.Join(directory, "remaining-files.json"), testRemainingFiles(reviewed.goos))
	writeTestText(t, filepath.Join(directory, "service-definition.txt"), expectedServiceDefinition(reviewed.goos))
	writeTestText(t, filepath.Join(directory, "service-log.txt"), "")
	writeTestJSON(t, filepath.Join(directory, "post-harness-cleanup.json"), map[string]any{"profile_absent": true, "service_absent": true, "process_absent": true, "port_8100_free": true})
	if reviewed.goos == "linux" {
		writeTestJSON(t, filepath.Join(directory, "linux-wrapper-cleanup.json"), map[string]any{
			"schema": "kaiten-linux-wrapper-cleanup/v1", "result": "passed", "user": "kaitenci", "uid": 1001,
			"target_validated": true, "user_manager_stopped": true, "linger_disabled": true, "processes_absent": true,
			"port_8100_free": true, "user_deleted": true, "group_deleted": true, "login_state_absent": true, "stage_deleted": true,
		})
	}
}

func testClientState(registered bool) map[string]any {
	document := func() map[string]any {
		servers := map[string]any{"other": map[string]any{"type": "http", "url": "http://127.0.0.1:65534/mcp"}}
		if registered {
			servers["kaiten"] = map[string]any{"type": "http", "url": mcpEndpoint}
		}
		return map[string]any{"unrelated": map[string]any{"preserve": true}, "mcpServers": servers}
	}
	return map[string]any{"claude_code": document(), "claude_desktop": document()}
}

func testManagerRaw(reviewed target, pid int) string {
	binary := expectedProfileBinary(reviewed.goos)
	arguments := binary + " --transport streamable-http --host 127.0.0.1 --port 8100 --streamable-http-path /mcp"
	switch reviewed.goos {
	case "darwin":
		return fmt.Sprintf("gui/501/%s = {\n\tstate = running\n\tprogram = %s\n\targuments = %s\n\tpid = %d\n}\n", serviceLabel, binary, arguments, pid)
	case "linux":
		return fmt.Sprintf("● %s - Kaiten MCP\n     Loaded: loaded ($PROFILE/.config/systemd/user/%s; enabled)\n     Active: active (running)\n   Main PID: %d (%s)\n      Tasks: 1\n     CGroup: /user.slice/%d\n             └─%d %s\n", serviceUnit, serviceUnit, pid, filepath.Base(binary), pid, pid, arguments)
	case "windows":
		encoded, _ := json.MarshalIndent(map[string]any{"ProcessId": pid, "ExecutablePath": binary, "CommandLine": arguments}, "", "  ")
		return string(encoded) + "\n"
	default:
		return ""
	}
}

func testMCPProof(stage, version string, authorized int) map[string]any {
	return map[string]any{
		"stage": stage, "endpoint": mcpEndpoint, "server_version": version, "protocol_version": "2025-06-18",
		"session_established": true, "tool_names": expectedReadOnlyToolNames, "read_only_tool_count": 18, "write_tool_count": 0,
		"representative_tool": "get_current_user", "representative_read_succeeded": true,
		"authorized_request_count": authorized, "unauthorized_request_count": 0,
		"mock_method": "GET", "mock_path": "/api/v1/users/current", "auth_header_valid": true,
	}
}

func testPermissions(goos string) []map[string]any {
	roles := []string{"binary", "environment", "service_definition", "claude_code", "claude_desktop"}
	result := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		permission := map[string]any{"role": role, "owner_current_user": true, "unexpected_allow_count": 0}
		if goos == "windows" {
			permission["acl_current_user"] = true
			permission["acl_system"] = true
		} else {
			permission["mode"] = "0600"
			if role == "binary" {
				permission["mode"] = "0700"
			}
		}
		result = append(result, permission)
	}
	return result
}

func testRemainingFiles(goos string) []string {
	switch goos {
	case "darwin":
		return []string{".claude.json", ".claude.json.bak", "Library/Application Support/Claude/claude_desktop_config.json", "Library/Application Support/Claude/claude_desktop_config.json.bak", "Library/Application Support/kaiten-mcp/logs/service.log"}
	case "linux":
		return []string{".claude.json", ".claude.json.bak", ".config/Claude/claude_desktop_config.json", ".config/Claude/claude_desktop_config.json.bak", ".local/state/kaiten-mcp/service.log"}
	case "windows":
		return []string{".claude.json", ".claude.json.bak", "AppData/Local/KaitenMCP/logs/service.log", "AppData/Roaming/Claude/claude_desktop_config.json", "AppData/Roaming/Claude/claude_desktop_config.json.bak"}
	default:
		return nil
	}
}

func repeatedHex(value int) string {
	return strings.Repeat(fmt.Sprintf("%x", value), 64)
}

func linuxArtifact(root, name string) string {
	return filepath.Join(root, "native-lifecycle-linux-amd64", name)
}

func readTestJSON(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatal(err)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestText(t, path, string(data)+"\n")
}

func writeTestText(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
