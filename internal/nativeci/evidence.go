// Package nativeci drives the disposable native installer acceptance gate.
// It is intentionally standard-library-only so the same harness runs on every
// release architecture.
package nativeci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Check is one secret-free assertion retained in the hosted-run artifact.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Evidence is the machine-readable native lifecycle record.
type Evidence struct {
	Schema             string   `json:"schema"`
	Result             string   `json:"result"`
	RunnerLabel        string   `json:"runner_label"`
	RunnerImageOS      string   `json:"runner_image_os,omitempty"`
	RunnerImageVersion string   `json:"runner_image_version,omitempty"`
	GOOS               string   `json:"goos"`
	GOARCH             string   `json:"goarch"`
	GoVersion          string   `json:"go_version"`
	Commit             string   `json:"commit"`
	WorkflowRunID      string   `json:"workflow_run_id,omitempty"`
	WorkflowRunAttempt int      `json:"workflow_run_attempt,omitempty"`
	StartedUTC         string   `json:"started_utc"`
	FinishedUTC        string   `json:"finished_utc"`
	Artifacts          []string `json:"artifacts,omitempty"`
	Checks             []Check  `json:"checks"`
}

func newEvidence(label, commit string, now time.Time) Evidence {
	attempt, _ := strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	return Evidence{
		Schema: "kaiten-native-lifecycle/v2", Result: "failed", RunnerLabel: label,
		RunnerImageOS: os.Getenv("ImageOS"), RunnerImageVersion: os.Getenv("ImageVersion"),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), Commit: commit,
		WorkflowRunID: os.Getenv("GITHUB_RUN_ID"), WorkflowRunAttempt: attempt,
		StartedUTC: now.UTC().Format(time.RFC3339),
	}
}

func writeEvidence(path string, evidence Evidence, forbidden string) error {
	evidence.FinishedUTC = time.Now().UTC().Format(time.RFC3339)
	return writeJSONArtifact(path, evidence, forbidden)
}

func writeJSONArtifact(path string, value any, forbidden string) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := validateEvidencePayload(encoded, forbidden); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func writeTextArtifact(path, value, forbidden string) error {
	if err := validateEvidencePayload([]byte(value), forbidden); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if !strings.HasSuffix(value, "\n") {
		value += "\n"
	}
	return os.WriteFile(path, []byte(value), 0o600)
}

func validateEvidencePayload(payload []byte, forbidden string) error {
	value := string(payload)
	if forbidden != "" && strings.Contains(value, forbidden) {
		return errors.New("refusing to persist evidence containing the synthetic token")
	}
	lower := strings.ToLower(value)
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(lower)
	for _, marker := range []string{`"authorization":`, "authorization:bearer", `"username":"native-lifecycle"`, `"id":4242`} {
		if strings.Contains(compact, marker) {
			return errors.New("refusing to persist an authorization header or mock tenant response")
		}
	}
	return nil
}

type commandEvidence struct {
	Step       string `json:"step"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	DurationNS int64  `json:"duration_ns"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
}

func writeCommandEvidence(path string, captures []capture, token string, replacements map[string]string) error {
	commands := make([]commandEvidence, 0, len(captures))
	for _, captured := range captures {
		command, err := redact(captured.command, token, replacements)
		if err != nil {
			return err
		}
		stdout, err := redact(strings.TrimSpace(captured.stdout), token, replacements)
		if err != nil {
			return err
		}
		stderr, err := redact(strings.TrimSpace(captured.stderr), token, replacements)
		if err != nil {
			return err
		}
		commands = append(commands, commandEvidence{Step: captured.label, Command: command, ExitCode: captured.exitCode, DurationNS: captured.duration.Nanoseconds(), Stdout: stdout, Stderr: stderr})
	}
	return writeJSONArtifact(path, commands, token)
}

func syntheticToken(seed []byte) (string, error) {
	if len(seed) < 16 {
		return "", errors.New("synthetic token seed is too short")
	}
	digest := sha256.Sum256(seed)
	return "native-ci-" + hex.EncodeToString(digest[:]), nil
}

func redact(value, token string, replacements map[string]string) (string, error) {
	if token != "" && strings.Contains(value, token) {
		return "", errors.New("captured output contains the synthetic token")
	}
	for original, replacement := range replacements {
		value = strings.ReplaceAll(value, original, replacement)
	}
	if len(value) > 4096 {
		value = value[:4096] + "...[truncated]"
	}
	return value, nil
}
