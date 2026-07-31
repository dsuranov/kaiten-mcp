package nativeci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitializationFailureRetainsStructuredEvidence(t *testing.T) {
	root := t.TempDir()
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	makeCandidate := func(directory, name string) string {
		t.Helper()
		path := filepath.Join(root, directory, name+extension)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(directory), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	commit := strings.Repeat("a", 40)
	archiveExtension := ".tar.gz"
	if runtime.GOOS == "windows" {
		archiveExtension = ".zip"
	}
	releaseArchive := "kaiten_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + archiveExtension
	evidenceDir := filepath.Join(root, "native-lifecycle-evidence")
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{
		V1: makeCandidate("v1", "kaiten-mcp"), V2: makeCandidate("v2", "kaiten-mcp"),
		V3: makeCandidate("v3", "kaiten-mcp"), ReleaseKaiten: makeCandidate("release", "kaiten"),
		Profile: filepath.Join(root, "native-lifecycle-profile"), EvidenceDir: evidenceDir,
		RunnerLabel: "unreviewed-runner", Commit: commit, V2Version: "1.2.3",
		ReleaseRunID: "123", ReleaseRunAttempt: "1", ReleaseTag: "v1.2.3", ReleaseHeadSHA: commit,
		ReleaseManifestSHA256: strings.Repeat("b", 64), ReleaseArchive: releaseArchive,
		ReleaseArchiveSHA256: strings.Repeat("c", 64), ReleaseKaitenSHA256: strings.Repeat("d", 64),
		ReleaseKaitenMCPSHA256: strings.Repeat("e", 64),
	}
	if err := Run(context.Background(), config); err == nil || !strings.Contains(err.Error(), "unreviewed native lifecycle runner") {
		t.Fatalf("initialization result = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(evidenceDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence Evidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Schema != "kaiten-native-lifecycle/v2" || evidence.Result != "failed" || evidence.Commit != commit || evidence.RunnerLabel != "unreviewed-runner" {
		t.Fatalf("failure evidence = %#v", evidence)
	}
	if len(evidence.Checks) != 1 || evidence.Checks[0].Name != "gate" || evidence.Checks[0].Status != "failed" || !strings.Contains(evidence.Checks[0].Detail, "unreviewed native lifecycle runner") {
		t.Fatalf("failure checks = %#v", evidence.Checks)
	}
}

func TestReleaseBindingRejectsAnotherCommit(t *testing.T) {
	config := Config{
		Commit: strings.Repeat("a", 40), ReleaseHeadSHA: strings.Repeat("b", 40),
		ReleaseRunID: "1", ReleaseRunAttempt: "1", ReleaseTag: "v1.0.0", V2Version: "1.0.0",
		ReleaseManifestSHA256: strings.Repeat("c", 64), ReleaseArchive: "kaiten_1.0.0_linux_amd64.tar.gz",
		ReleaseArchiveSHA256: strings.Repeat("d", 64), ReleaseKaitenSHA256: strings.Repeat("e", 64),
		ReleaseKaitenMCPSHA256: strings.Repeat("f", 64),
	}
	if err := validateReleaseBinding(config); err == nil {
		t.Fatal("release artifact from another commit was accepted")
	}
}

func TestReleaseBindingRequiresExactTagVersionAndNativeArchive(t *testing.T) {
	commit := strings.Repeat("a", 40)
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	config := Config{
		Commit: commit, ReleaseHeadSHA: commit, ReleaseRunID: "42", ReleaseRunAttempt: "1", ReleaseTag: "v1.2.3", V2Version: "1.2.3",
		ReleaseManifestSHA256:  strings.Repeat("b", 64),
		ReleaseArchive:         "kaiten_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + extension,
		ReleaseArchiveSHA256:   strings.Repeat("c", 64),
		ReleaseKaitenSHA256:    strings.Repeat("d", 64),
		ReleaseKaitenMCPSHA256: strings.Repeat("e", 64),
	}
	if err := validateReleaseBinding(config); err != nil {
		t.Fatal(err)
	}
	config.ReleaseRunAttempt = "0"
	if err := validateReleaseBinding(config); err == nil {
		t.Fatal("invalid release run attempt was accepted")
	}
	config.ReleaseRunAttempt = "1"
	config.V2Version = "1.2.4"
	if err := validateReleaseBinding(config); err == nil {
		t.Fatal("release tag/version drift was accepted")
	}
}

func TestReviewedRollbackTriggerRequiresExactExitAndStderr(t *testing.T) {
	tests := []struct {
		name        string
		exitCode    int
		stderr      string
		wantTrigger string
		wantOK      bool
	}{
		{name: "health timeout", exitCode: 1, stderr: v3HealthTimeoutFailure, wantTrigger: rollbackTriggerHealth, wantOK: true},
		{name: "activation failure", exitCode: 1, stderr: v3ActivationFailure, wantTrigger: rollbackTriggerActivation, wantOK: true},
		{name: "wrong exit", exitCode: 2, stderr: v3ActivationFailure},
		{name: "arbitrary failure", exitCode: 1, stderr: "error: filesystem unavailable"},
		{name: "near miss", exitCode: 1, stderr: "error: installation failed: activate service: exit status 2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trigger, ok := reviewedRollbackTrigger(test.exitCode, test.stderr)
			if trigger != test.wantTrigger || ok != test.wantOK {
				t.Fatalf("reviewedRollbackTrigger() = %q, %t; want %q, %t", trigger, ok, test.wantTrigger, test.wantOK)
			}
		})
	}
}
