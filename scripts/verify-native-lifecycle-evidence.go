//go:build ignore

// Command verify-native-lifecycle-evidence validates the downloaded five-runner
// artifact set before it is admitted to a release audit package.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

type target struct{ goos, goarch string }

var targets = map[string]target{
	"macos-15-intel":   {goos: "darwin", goarch: "amd64"},
	"macos-latest":     {goos: "darwin", goarch: "arm64"},
	"ubuntu-latest":    {goos: "linux", goarch: "amd64"},
	"ubuntu-24.04-arm": {goos: "linux", goarch: "arm64"},
	"windows-latest":   {goos: "windows", goarch: "amd64"},
}

var syntheticToken = regexp.MustCompile(`native-ci-[0-9a-f]{64}`)

func main() {
	if len(os.Args) != 3 {
		fail("usage: go run ./scripts/verify-native-lifecycle-evidence.go <download-directory> <40-character-commit>")
	}
	root, err := filepath.Abs(os.Args[1])
	check(err)
	commit := strings.TrimSpace(os.Args[2])
	if matched, _ := regexp.MatchString(`^[0-9a-f]{40}$`, commit); !matched {
		fail("expected commit must be a lowercase 40-character Git SHA")
	}
	var summaries []string
	check(filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "summary.json" {
			summaries = append(summaries, path)
		}
		return nil
	}))
	if len(summaries) != len(targets) {
		fail("found %d native summary files, want %d", len(summaries), len(targets))
	}
	sort.Strings(summaries)
	seen := make(map[string]bool)
	workflowRunID := ""
	workflowRunAttempt := 0
	for _, path := range summaries {
		evidence := readSummary(path)
		validateSummary(path, evidence, commit)
		if seen[evidence.RunnerLabel] {
			fail("duplicate native runner evidence for %s", evidence.RunnerLabel)
		}
		seen[evidence.RunnerLabel] = true
		if workflowRunID == "" {
			workflowRunID, workflowRunAttempt = evidence.WorkflowRunID, evidence.WorkflowRunAttempt
		} else if evidence.WorkflowRunID != workflowRunID || evidence.WorkflowRunAttempt != workflowRunAttempt {
			fail("native evidence mixes workflow runs or attempts")
		}
		validateArtifactDirectory(filepath.Dir(path), evidence)
	}
	for label := range targets {
		if !seen[label] {
			fail("missing native lifecycle evidence for %s", label)
		}
	}
	fmt.Printf("verified 5 native lifecycle artifacts: commit=%s workflow_run_id=%s attempt=%d\n", commit, workflowRunID, workflowRunAttempt)
}

func readSummary(path string) nativeci.Evidence {
	data := readSafe(path)
	var evidence nativeci.Evidence
	check(json.Unmarshal(data, &evidence))
	return evidence
}

func validateSummary(path string, evidence nativeci.Evidence, commit string) {
	want, ok := targets[evidence.RunnerLabel]
	if !ok {
		fail("%s uses unreviewed runner label %q", path, evidence.RunnerLabel)
	}
	if evidence.Schema != "kaiten-native-lifecycle/v1" || evidence.Result != "passed" {
		fail("%s is not a passing v1 evidence record", path)
	}
	if evidence.Commit != commit {
		fail("%s commit = %s, want %s", path, evidence.Commit, commit)
	}
	if evidence.GOOS != want.goos || evidence.GOARCH != want.goarch {
		fail("%s runtime = %s/%s, want %s/%s", evidence.RunnerLabel, evidence.GOOS, evidence.GOARCH, want.goos, want.goarch)
	}
	if evidence.GoVersion != "go1.26.5" {
		fail("%s Go version = %q, want go1.26.5", evidence.RunnerLabel, evidence.GoVersion)
	}
	if evidence.RunnerImageOS == "" || evidence.RunnerImageVersion == "" || evidence.WorkflowRunID == "" || evidence.WorkflowRunAttempt < 1 {
		fail("%s omits hosted image or workflow identity", evidence.RunnerLabel)
	}
	started, startErr := time.Parse(time.RFC3339, evidence.StartedUTC)
	finished, finishErr := time.Parse(time.RFC3339, evidence.FinishedUTC)
	if startErr != nil || finishErr != nil || finished.Before(started) {
		fail("%s has invalid evidence timestamps", evidence.RunnerLabel)
	}
	requiredChecks := map[string]bool{
		"install-health": false, "mcp-api-auth": false, "native-restart": false,
		"healthy-update": false, "failed-update-rollback": false,
		"double-uninstall": false, "final-owned-file-and-secret-scan": false,
		"permissions": false,
	}
	for _, checkRecord := range evidence.Checks {
		if checkRecord.Status != "passed" {
			fail("%s contains non-passing check %s=%s", evidence.RunnerLabel, checkRecord.Name, checkRecord.Status)
		}
		if _, required := requiredChecks[checkRecord.Name]; required {
			requiredChecks[checkRecord.Name] = true
		}
	}
	for name, present := range requiredChecks {
		if !present {
			fail("%s omits required check %s", evidence.RunnerLabel, name)
		}
	}
}

func validateArtifactDirectory(directory string, evidence nativeci.Evidence) {
	required := nativeci.RequiredEvidenceArtifacts()
	sort.Strings(required)
	actual := append([]string(nil), evidence.Artifacts...)
	sort.Strings(actual)
	if strings.Join(required, "\n") != strings.Join(actual, "\n") {
		fail("%s artifact inventory is not the reviewed set", evidence.RunnerLabel)
	}
	allowed := map[string]bool{"summary.json": true, "wrapper-context.txt": true}
	for _, name := range required {
		allowed[name] = true
		readSafe(filepath.Join(directory, name))
	}
	entries, err := os.ReadDir(directory)
	check(err)
	for _, entry := range entries {
		if entry.IsDir() || !allowed[entry.Name()] {
			fail("%s contains unexpected evidence entry %s", evidence.RunnerLabel, entry.Name())
		}
		readSafe(filepath.Join(directory, entry.Name()))
	}
	validateCommands(directory)
	validateHealth(directory)
	validateRollbackHashes(directory)
	validateMCPProofs(directory)
}

func validateCommands(directory string) {
	var commands []struct {
		Step     string `json:"step"`
		ExitCode int    `json:"exit_code"`
	}
	check(json.Unmarshal(readSafe(filepath.Join(directory, "commands.json")), &commands))
	want := map[string]bool{"install-v1": true, "healthy-update-v2": true, "bad-service-update-v3": true, "uninstall-first": true, "uninstall-second": true}
	if len(commands) != len(want) {
		fail("commands.json contains %d commands, want %d", len(commands), len(want))
	}
	for _, command := range commands {
		if !want[command.Step] {
			fail("commands.json contains unexpected or duplicate step %s", command.Step)
		}
		delete(want, command.Step)
		if command.Step == "bad-service-update-v3" {
			if command.ExitCode == 0 {
				fail("bad v3 update did not retain a nonzero exit")
			}
		} else if command.ExitCode != 0 {
			fail("%s did not retain exit 0", command.Step)
		}
	}
}

func validateHealth(directory string) {
	versions := map[string]string{
		"health-install-v1.json": "native-v1", "health-restart-v1.json": "native-v1",
		"health-update-v2.json": "native-v2", "health-rollback-v2.json": "native-v2",
	}
	for name, want := range versions {
		var health struct {
			Status, Version, Runtime string
		}
		check(json.Unmarshal(readSafe(filepath.Join(directory, name)), &health))
		if health.Status != "ok" || health.Version != want || health.Runtime == "" {
			fail("%s does not prove exact healthy version %s", name, want)
		}
	}
}

func validateRollbackHashes(directory string) {
	var hashes struct {
		Before map[string]string `json:"before_failed_update"`
		After  map[string]string `json:"after_rollback"`
	}
	check(json.Unmarshal(readSafe(filepath.Join(directory, "rollback-hashes.json")), &hashes))
	for _, role := range []string{"binary", "environment", "service_definition"} {
		if len(hashes.Before[role]) != 64 || hashes.Before[role] != hashes.After[role] {
			fail("rollback hash mismatch for %s", role)
		}
	}
}

func validateMCPProofs(directory string) {
	for name, minimum := range map[string]int{"mcp-auth-v1.json": 1, "mcp-auth-rollback-v2.json": 2} {
		var proof struct {
			Authorized int  `json:"mock_authorized_requests"`
			WriteTools bool `json:"write_tools_advertised"`
		}
		check(json.Unmarshal(readSafe(filepath.Join(directory, name)), &proof))
		if proof.Authorized < minimum || proof.WriteTools {
			fail("%s does not prove authenticated read-only MCP behavior", name)
		}
	}
}

func readSafe(path string) []byte {
	info, err := os.Stat(path)
	check(err)
	if !info.Mode().IsRegular() || info.Size() > 8*1024*1024 {
		fail("unsafe evidence file type or size: %s", path)
	}
	data, err := os.ReadFile(path)
	check(err)
	value := string(data)
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(strings.ToLower(value))
	if syntheticToken.Match(data) || strings.Contains(compact, `"authorization":`) || strings.Contains(compact, "authorization:bearer") || strings.Contains(compact, `"username":"native-lifecycle"`) || strings.Contains(compact, `"id":4242`) {
		fail("credential material or mock tenant response found in %s", path)
	}
	return data
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", arguments...)
	os.Exit(1)
}
