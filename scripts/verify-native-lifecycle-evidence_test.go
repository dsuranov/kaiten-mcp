//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

func TestReviewedNativeEvidenceFixturePassesAggregateChecks(t *testing.T) {
	directory := t.TempDir()
	required := nativeci.RequiredEvidenceArtifacts()
	evidence := nativeci.Evidence{
		Schema: "kaiten-native-lifecycle/v1", Result: "passed", RunnerLabel: "ubuntu-latest",
		RunnerImageOS: "ubuntu24", RunnerImageVersion: "test", GOOS: "linux", GOARCH: "amd64",
		GoVersion: "go1.26.5", Commit: strings.Repeat("a", 40), WorkflowRunID: "123", WorkflowRunAttempt: 1,
		StartedUTC: time.Unix(0, 0).UTC().Format(time.RFC3339), FinishedUTC: time.Unix(1, 0).UTC().Format(time.RFC3339),
		Artifacts: required,
	}
	for _, name := range []string{"install-health", "mcp-api-auth", "native-restart", "healthy-update", "failed-update-rollback", "double-uninstall", "final-owned-file-and-secret-scan", "permissions"} {
		evidence.Checks = append(evidence.Checks, nativeci.Check{Name: name, Status: "passed"})
	}
	writeTestJSON(t, filepath.Join(directory, "summary.json"), evidence)
	for _, name := range required {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTestJSON(t, filepath.Join(directory, "commands.json"), []map[string]any{
		{"step": "install-v1", "exit_code": 0},
		{"step": "healthy-update-v2", "exit_code": 0},
		{"step": "bad-service-update-v3", "exit_code": 1},
		{"step": "uninstall-first", "exit_code": 0},
		{"step": "uninstall-second", "exit_code": 0},
	})
	for name, version := range map[string]string{"health-install-v1.json": "native-v1", "health-restart-v1.json": "native-v1", "health-update-v2.json": "native-v2", "health-rollback-v2.json": "native-v2"} {
		writeTestJSON(t, filepath.Join(directory, name), map[string]any{"status": "ok", "version": version, "runtime": "go1.26.5"})
	}
	hash := strings.Repeat("b", 64)
	writeTestJSON(t, filepath.Join(directory, "rollback-hashes.json"), map[string]any{
		"before_failed_update": map[string]string{"binary": hash, "environment": hash, "service_definition": hash},
		"after_rollback":       map[string]string{"binary": hash, "environment": hash, "service_definition": hash},
	})
	writeTestJSON(t, filepath.Join(directory, "mcp-auth-v1.json"), map[string]any{"mock_authorized_requests": 1, "write_tools_advertised": false})
	writeTestJSON(t, filepath.Join(directory, "mcp-auth-rollback-v2.json"), map[string]any{"mock_authorized_requests": 2, "write_tools_advertised": false})

	validateSummary(filepath.Join(directory, "summary.json"), evidence, evidence.Commit)
	validateArtifactDirectory(directory, evidence)
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
