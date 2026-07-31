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
	Schema      string  `json:"schema"`
	Result      string  `json:"result"`
	RunnerLabel string  `json:"runner_label"`
	GOOS        string  `json:"goos"`
	GOARCH      string  `json:"goarch"`
	Commit      string  `json:"commit"`
	StartedUTC  string  `json:"started_utc"`
	FinishedUTC string  `json:"finished_utc"`
	Checks      []Check `json:"checks"`
}

func newEvidence(label, commit string, now time.Time) Evidence {
	return Evidence{Schema: "kaiten-native-lifecycle/v1", Result: "failed", RunnerLabel: label, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Commit: commit, StartedUTC: now.UTC().Format(time.RFC3339)}
}

func writeEvidence(path string, evidence Evidence, forbidden string) error {
	evidence.FinishedUTC = time.Now().UTC().Format(time.RFC3339)
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if forbidden != "" && strings.Contains(string(encoded), forbidden) {
		return errors.New("refusing to persist evidence containing the synthetic token")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
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
