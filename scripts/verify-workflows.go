//go:build ignore

// Command verify-workflows enforces the repository's GitHub Actions trust
// boundary without requiring a third-party YAML parser.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type permissionSet map[string]string

type workflow struct {
	topPermissions permissionSet
	topExplicit    bool
	jobPermissions map[string]permissionSet
	jobs           []string
	tokenJobs      []string
	raw            string
}

var actionUse = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*([^@\s]+)@([0-9a-f]{40})\s+#\s+(v[0-9][A-Za-z0-9._-]*)\s*$`)

func main() {
	paths, err := filepath.Glob(".github/workflows/*.yml")
	check(err)
	if len(paths) == 0 {
		fail("no GitHub Actions workflows found")
	}
	for _, path := range paths {
		checkActionPins(path)
	}

	ci := parseWorkflow(".github/workflows/ci.yml")
	if !ci.topExplicit || !samePermissions(ci.topPermissions, permissionSet{"contents": "read"}) {
		fail("CI top-level permissions must be exactly contents: read")
	}
	for _, job := range ci.jobs {
		effective := ci.topPermissions
		if explicit, exists := ci.jobPermissions[job]; exists {
			effective = explicit
		}
		if !samePermissions(effective, permissionSet{"contents": "read"}) {
			fail("CI job %s is not read-only: %v", job, effective)
		}
	}
	if len(ci.tokenJobs) != 0 {
		fail("CI must not explicitly expose GITHUB_TOKEN: %v", ci.tokenJobs)
	}

	release := parseWorkflow(".github/workflows/release.yml")
	if !release.topExplicit || len(release.topPermissions) != 0 {
		fail("release top-level permissions must be empty")
	}
	wantRelease := map[string]permissionSet{
		"verify":  {"contents": "read"},
		"build":   {"contents": "read"},
		"attest":  {"contents": "read", "id-token": "write", "attestations": "write"},
		"publish": {"contents": "write"},
	}
	if len(release.jobs) != len(wantRelease) {
		fail("release jobs changed without a permission-policy update: %v", release.jobs)
	}
	for _, job := range release.jobs {
		want, ok := wantRelease[job]
		if !ok {
			fail("release job %s has no reviewed permission policy", job)
		}
		got, explicit := release.jobPermissions[job]
		if !explicit || !samePermissions(got, want) {
			fail("release job %s permissions = %v, want %v", job, got, want)
		}
	}
	if len(release.tokenJobs) != 1 || release.tokenJobs[0] != "publish" {
		fail("GITHUB_TOKEN must be exposed exactly once in publish, got %v", release.tokenJobs)
	}
	if !strings.Contains(release.raw, "install-only: true") || !strings.Contains(release.raw, "run: ./scripts/verify-reproducible-release.sh") {
		fail("read-only release build must install pinned GoReleaser and run the two-build reproducibility gate")
	}
	if !strings.Contains(ci.raw, "run: ./scripts/verify-reproducible-release.sh --snapshot") {
		fail("CI must run the two-build snapshot reproducibility gate")
	}

	native := parseWorkflow(".github/workflows/native-lifecycle.yml")
	if !native.topExplicit || !samePermissions(native.topPermissions, permissionSet{"contents": "read"}) {
		fail("native lifecycle top-level permissions must be exactly contents: read")
	}
	if len(native.jobs) != 1 || native.jobs[0] != "native-lifecycle" {
		fail("native lifecycle workflow must contain only the reviewed matrix job, got %v", native.jobs)
	}
	if len(native.tokenJobs) != 0 {
		fail("native lifecycle workflow must not explicitly expose GITHUB_TOKEN: %v", native.tokenJobs)
	}
	if !strings.Contains(native.raw, "on:\n  workflow_dispatch:\n") {
		fail("native lifecycle workflow must be manually dispatched")
	}
	for _, forbiddenTrigger := range []string{"\n  push:", "\n  pull_request:", "\n  schedule:", "\n  workflow_call:"} {
		if strings.Contains(native.raw, forbiddenTrigger) {
			fail("native lifecycle workflow must not use automatic trigger %s", strings.TrimSpace(forbiddenTrigger))
		}
	}
	if strings.Contains(strings.ToLower(native.raw), "self-hosted") {
		fail("native lifecycle workflow must never select a self-hosted runner")
	}
	runnerPattern := regexp.MustCompile(`(?m)^\s+runner:\s*([^\s#]+)\s*$`)
	var nativeRunners []string
	for _, match := range runnerPattern.FindAllStringSubmatch(native.raw, -1) {
		nativeRunners = append(nativeRunners, match[1])
	}
	sort.Strings(nativeRunners)
	wantNativeRunners := []string{"macos-15-intel", "macos-latest", "ubuntu-24.04-arm", "ubuntu-latest", "windows-latest"}
	if strings.Join(nativeRunners, ",") != strings.Join(wantNativeRunners, ",") {
		fail("native lifecycle runner labels = %v, want exact GitHub-hosted matrix %v", nativeRunners, wantNativeRunners)
	}
	for _, required := range []string{
		"runs-on: ${{ matrix.runner }}",
		"run: ./scripts/build-native-lifecycle-fixtures.sh",
		"run: ./scripts/run-native-lifecycle-ci.sh",
		"if: always()",
		"name: native-lifecycle-${{ matrix.id }}",
	} {
		if !strings.Contains(native.raw, required) {
			fail("native lifecycle workflow is missing reviewed gate fragment %q", required)
		}
	}

	allRaw := ci.raw + "\n" + release.raw + "\n" + native.raw
	for _, line := range strings.Split(allRaw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "go-version:") && trimmed != `go-version: "1.26.5"` {
			fail("workflow Go version is not pinned to 1.26.5: %s", trimmed)
		}
		if strings.Contains(trimmed, "golang.org/x/vuln/cmd/govulncheck@") && !strings.Contains(trimmed, "govulncheck@v1.6.0") {
			fail("govulncheck installer is not pinned to v1.6.0: %s", trimmed)
		}
		if strings.Contains(trimmed, "github.com/rhysd/actionlint/cmd/actionlint@") && !strings.Contains(trimmed, "actionlint@v1.7.12") {
			fail("actionlint runner is not pinned to v1.7.12: %s", trimmed)
		}
		if strings.HasPrefix(trimmed, "syft-version:") && trimmed != "syft-version: v1.50.0" {
			fail("Syft is not pinned to v1.50.0: %s", trimmed)
		}
	}
	if strings.Count(allRaw, "version: v2.17.1") < 2 {
		fail("CI and release must pin GoReleaser v2.17.1")
	}

	dependabot, err := os.ReadFile(".github/dependabot.yml")
	check(err)
	dependabotText := string(dependabot)
	if !strings.Contains(dependabotText, "package-ecosystem: github-actions") || !strings.Contains(dependabotText, "interval: weekly") {
		fail("Dependabot must perform weekly github-actions updates")
	}
	fmt.Printf("verified %d workflow(s): immutable action pins and least-privilege permissions\n", len(paths))
}

func checkActionPins(path string) {
	file, err := os.Open(path)
	check(err)
	defer file.Close()
	uses := 0
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !strings.Contains(line, "uses:") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "uses: ./") || strings.HasPrefix(trimmed, "- uses: ./") {
			continue
		}
		uses++
		if !actionUse.MatchString(line) {
			fail("%s:%d action must use a full 40-character SHA and version comment: %s", path, lineNumber, trimmed)
		}
	}
	check(scanner.Err())
	if uses == 0 {
		fail("%s contains no external action", path)
	}
}

func parseWorkflow(path string) workflow {
	data, err := os.ReadFile(path)
	check(err)
	result := workflow{jobPermissions: make(map[string]permissionSet), raw: string(data)}
	lines := strings.Split(result.raw, "\n")
	inJobs := false
	currentJob := ""
	permissionIndent := -1
	var currentPermissions permissionSet
	for _, original := range lines {
		if strings.TrimSpace(original) == "" {
			continue
		}
		structural := original
		if index := strings.Index(structural, "#"); index >= 0 {
			structural = structural[:index]
		}
		structural = strings.TrimRight(structural, " \t")
		trimmed := strings.TrimSpace(structural)
		indent := leadingSpaces(structural)

		if currentPermissions != nil {
			if indent == permissionIndent+2 {
				key, value, ok := strings.Cut(trimmed, ":")
				if !ok || strings.TrimSpace(value) == "" {
					fail("%s has malformed permissions entry: %s", path, trimmed)
				}
				currentPermissions[strings.TrimSpace(key)] = strings.TrimSpace(value)
				continue
			}
			if indent <= permissionIndent {
				currentPermissions = nil
				permissionIndent = -1
			}
		}

		if indent == 0 && trimmed == "jobs:" {
			inJobs = true
			currentJob = ""
			continue
		}
		if !inJobs && indent == 0 && trimmed == "permissions: {}" {
			result.topPermissions = permissionSet{}
			result.topExplicit = true
			continue
		}
		if !inJobs && indent == 0 && trimmed == "permissions:" {
			result.topPermissions = permissionSet{}
			result.topExplicit = true
			currentPermissions = result.topPermissions
			permissionIndent = indent
			continue
		}
		if inJobs && indent == 2 && strings.HasSuffix(trimmed, ":") {
			currentJob = strings.TrimSuffix(trimmed, ":")
			result.jobs = append(result.jobs, currentJob)
			continue
		}
		if inJobs && currentJob != "" && indent == 4 && trimmed == "permissions:" {
			set := permissionSet{}
			result.jobPermissions[currentJob] = set
			currentPermissions = set
			permissionIndent = indent
			continue
		}
		if strings.Contains(original, "secrets.GITHUB_TOKEN") || strings.Contains(original, "github.token") {
			result.tokenJobs = append(result.tokenJobs, currentJob)
		}
	}
	sort.Strings(result.tokenJobs)
	return result
}

func leadingSpaces(value string) int {
	return len(value) - len(strings.TrimLeft(value, " "))
}

func samePermissions(first, second permissionSet) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
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
