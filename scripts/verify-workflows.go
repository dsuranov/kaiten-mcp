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

type workflowLine struct {
	number   int
	original string
	trimmed  string
	indent   int
}

type workflowJob struct {
	name  string
	lines []workflowLine
}

type workflowStep struct {
	name      string
	startLine int
	lines     []workflowLine
}

var (
	actionUse       = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*([^@\s]+)@([0-9a-f]{40})\s+#\s+(v[0-9][A-Za-z0-9._-]*)\s*$`)
	matrixRunnerRef = regexp.MustCompile(`^\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}$`)
)

func main() {
	var paths []string
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml"} {
		matches, err := filepath.Glob(pattern)
		check(err)
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		fail("no GitHub Actions workflows found")
	}
	for _, path := range paths {
		checkActionPins(path)
		data, err := os.ReadFile(path)
		check(err)
		if violations := windowsShellViolations(string(data)); len(violations) != 0 {
			fail("%s has Windows-reachable .sh steps without explicit shell: bash: %s", path, strings.Join(violations, "; "))
		}
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
	if !strings.HasPrefix(release.raw, "name: Release\n\non:\n  push:\n    tags:\n      - \"v*\"\n\npermissions: {}\n") {
		fail("release workflow must be named Release and triggered only by v-prefixed tag pushes")
	}
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
	for name, raw := range map[string]string{"CI": ci.raw, "release": release.raw} {
		if !strings.Contains(raw, "go run ./scripts/verify-dependency-policy.go") {
			fail("%s must verify dependency notices and provenance", name)
		}
		if !strings.Contains(raw, "go test ./scripts/verify-native-lifecycle-evidence.go ./scripts/verify-native-lifecycle-evidence_test.go") {
			fail("%s must test the native evidence validator", name)
		}
		if !strings.Contains(raw, "go test ./scripts/prepare-native-lifecycle-release.go ./scripts/prepare-native-lifecycle-release_test.go") {
			fail("%s must test the native release binding helper", name)
		}
		if !strings.Contains(raw, "go test ./scripts/sanitize-native-lifecycle-evidence.go ./scripts/sanitize-native-lifecycle-evidence_test.go") {
			fail("%s must test the native evidence upload sanitizer", name)
		}
		if !strings.Contains(raw, "go test ./cleanup-native-lifecycle-linux_test.go") {
			fail("%s must test the isolated Linux cleanup helper", name)
		}
	}

	native := parseWorkflow(".github/workflows/native-lifecycle.yml")
	if !native.topExplicit || !samePermissions(native.topPermissions, permissionSet{"actions": "read", "contents": "read"}) {
		fail("native lifecycle top-level permissions must be exactly actions: read and contents: read")
	}
	if len(native.jobs) != 1 || native.jobs[0] != "native-lifecycle" {
		fail("native lifecycle workflow must contain only the reviewed matrix job, got %v", native.jobs)
	}
	if len(native.tokenJobs) != 1 || native.tokenJobs[0] != "native-lifecycle" {
		fail("native lifecycle must expose its read-only token exactly once for the authenticated artifact download, got %v", native.tokenJobs)
	}
	if !strings.Contains(native.raw, "on:\n  workflow_dispatch:\n") {
		fail("native lifecycle workflow must be manually dispatched")
	}
	for _, input := range []string{"expected_sha", "release_run_id"} {
		pattern := regexp.MustCompile(`(?m)^      ` + regexp.QuoteMeta(input) + `:\n(?:        [^\n]*\n)*?        required: true\n(?:        [^\n]*\n)*?        type: string$`)
		if !pattern.MatchString(native.raw) {
			fail("native lifecycle dispatch input %s must be a required string", input)
		}
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
		"expected_sha:",
		"release_run_id:",
		"WORKFLOW_SHA: ${{ github.sha }}",
		"WORKFLOW_REF: ${{ github.ref }}",
		`test "$WORKFLOW_SHA" = "$EXPECTED_SHA"`,
		`[[ "$WORKFLOW_REF" =~ ^refs/tags/v[0-9][0-9A-Za-z.+-]*$ ]]`,
		"runs-on: ${{ matrix.runner }}",
		"ref: ${{ inputs.expected_sha }}",
		"persist-credentials: false",
		`- name: Install Linux user-session DBus prerequisite
        if: runner.os == 'Linux'
        shell: bash
        run: |
          sudo apt-get update
          sudo apt-get install --yes --no-install-recommends dbus-user-session`,
		"run: ./scripts/build-native-lifecycle-fixtures.sh",
		"go run ./scripts/prepare-native-lifecycle-release.go",
		"go test ./scripts/prepare-native-lifecycle-release.go ./scripts/prepare-native-lifecycle-release_test.go",
		"go test ./scripts/sanitize-native-lifecycle-evidence.go ./scripts/sanitize-native-lifecycle-evidence_test.go",
		"GH_TOKEN: ${{ github.token }}",
		"run: ./scripts/run-native-lifecycle-ci.sh",
		"go run ./scripts/sanitize-native-lifecycle-evidence.go \"$RUNNER_TEMP/native-lifecycle-evidence\"",
		"if: ${{ always() && steps.sanitize.outcome == 'success' }}",
		"name: native-lifecycle-${{ matrix.id }}",
		"go run ./scripts/verify-dependency-policy.go",
		"go test ./scripts/verify-native-lifecycle-evidence.go ./scripts/verify-native-lifecycle-evidence_test.go",
	} {
		if !strings.Contains(native.raw, required) {
			fail("native lifecycle workflow is missing reviewed gate fragment %q", required)
		}
	}
	if strings.Count(native.raw, "GH_TOKEN: ${{ github.token }}") != 1 {
		fail("native lifecycle token must be exposed only to the exact release binding step")
	}
	if !strings.Contains(native.raw, "go test ./cleanup-native-lifecycle-linux_test.go") {
		fail("native lifecycle must test the reviewed Linux cleanup helper")
	}
	wrapperData, err := os.ReadFile("scripts/run-native-lifecycle-ci.sh")
	check(err)
	wrapper := string(wrapperData)
	if !strings.Contains(wrapper, `"$script_directory/cleanup-native-lifecycle-linux.sh" "$native_user" "$resolved_uid" "$stage" "$evidence"`) {
		fail("native lifecycle wrapper must invoke the reviewed Linux cleanup helper with exact targets")
	}
	for _, required := range []string{
		`if [[ -e "$evidence" || -L "$evidence" ]]; then`,
		`mkdir "$evidence"`,
		`if [[ "${RUNNER_OS:-}" != "Windows" ]]; then`,
		`chmod 0700 "$evidence"`,
		`sudo systemctl start "user-runtime-dir@$uid.service"`,
		`runtime_dir="/run/user/$uid"`,
		`if [[ ! -d "$runtime_dir" || -L "$runtime_dir" ]]; then`,
		`if [[ "$(stat -c '%u:%g' "$runtime_dir")" != "$uid:$gid" ]]; then`,
		`sudo loginctl enable-linger "$native_user"`,
		`sudo systemctl start "user@$uid.service"`,
	} {
		if !strings.Contains(wrapper, required) {
			fail("native lifecycle wrapper is missing reviewed portability or Linux isolation fragment %q", required)
		}
	}
	if strings.Contains(wrapper, `mkdir -m 0700 "$evidence"`) {
		fail("native lifecycle wrapper must not use non-portable mkdir mode flags for the cross-platform evidence directory")
	}
	previous := -1
	for _, ordered := range []string{
		`if [[ -e "$evidence" || -L "$evidence" ]]; then`,
		`mkdir "$evidence"`,
		`sudo systemctl start "user-runtime-dir@$uid.service"`,
		`runtime_dir="/run/user/$uid"`,
		`if [[ ! -d "$runtime_dir" || -L "$runtime_dir" ]]; then`,
		`if [[ "$(stat -c '%u:%g' "$runtime_dir")" != "$uid:$gid" ]]; then`,
		`sudo loginctl enable-linger "$native_user"`,
		`sudo systemctl start "user@$uid.service"`,
	} {
		index := strings.Index(wrapper, ordered)
		if index < 0 || index <= previous {
			fail("native lifecycle wrapper does not retain reviewed evidence and dedicated-runtime ordering at %q", ordered)
		}
		previous = index
	}
	cleanupData, err := os.ReadFile("scripts/cleanup-native-lifecycle-linux.sh")
	check(err)
	cleanup := string(cleanupData)
	for _, required := range []string{
		`sudo systemctl stop "user@${native_uid}.service"`,
		`manager_state="$(sudo systemctl show "user@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo systemctl stop "user-runtime-dir@${native_uid}.service"`,
		`runtime_dir_state="$(sudo systemctl show "user-runtime-dir@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`if [[ "$manager_state" == "inactive" && "$runtime_dir_state" == "inactive" && ! -e "$runtime_dir" && ! -L "$runtime_dir" ]]; then`,
		`sudo loginctl disable-linger "$native_user" >/dev/null 2>&1 || true`,
		`if linger_state="$(sudo loginctl show-user "$native_user" --property=Linger --value 2>/dev/null)"; then`,
		`elif [[ ! -e "$linger_record" && ! -L "$linger_record" && ! -e "$linger_marker" && ! -L "$linger_marker" ]]; then`,
	} {
		if !strings.Contains(cleanup, required) {
			fail("Linux cleanup helper is missing reviewed runtime or linger fragment %q", required)
		}
	}
	previous = -1
	for _, ordered := range []string{
		`sudo systemctl stop "user@${native_uid}.service"`,
		`manager_state="$(sudo systemctl show "user@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo systemctl stop "user-runtime-dir@${native_uid}.service"`,
		`runtime_dir_state="$(sudo systemctl show "user-runtime-dir@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo loginctl disable-linger "$native_user" >/dev/null 2>&1 || true`,
		`if linger_state="$(sudo loginctl show-user "$native_user" --property=Linger --value 2>/dev/null)"; then`,
	} {
		index := strings.Index(cleanup, ordered)
		if index < 0 || index <= previous {
			fail("Linux cleanup helper does not retain reviewed user-runtime cleanup ordering at %q", ordered)
		}
		previous = index
	}
	for _, required := range []string{`test "$release_run_attempt" = "1"`, `test "${GITHUB_SHA:?GITHUB_SHA is required}" = "$expected_sha"`, `test "${GITHUB_REF:?GITHUB_REF is required}" = "refs/tags/$release_tag"`, `--release-kaiten-sha256 "$release_kaiten_sha256"`, `--release-kaiten-mcp-sha256 "$release_kaiten_mcp_sha256"`} {
		if !strings.Contains(wrapper, required) {
			fail("native lifecycle wrapper is missing exact release binding %q", required)
		}
	}
	bindingHelperData, err := os.ReadFile("scripts/prepare-native-lifecycle-release.go")
	check(err)
	bindingHelper := string(bindingHelperData)
	if strings.Contains(bindingHelper, `"os/exec"`) || strings.Contains(bindingHelper, "exec.Command") {
		fail("token-bearing native release binding helper must never execute downloaded artifact code")
	}
	for _, required := range []string{"run.RunAttempt != 1", "downloadedBytes != selected.SizeInBytes", "maxTarStreamBytes", `request.Header.Del("Referer")`} {
		if !strings.Contains(bindingHelper, required) {
			fail("native release binding helper is missing fail-closed invariant %q", required)
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

func windowsShellViolations(raw string) []string {
	var violations []string
	for _, job := range parseWorkflowJobs(raw) {
		if !jobCanRunOnWindows(job) {
			continue
		}
		for _, step := range parseWorkflowSteps(job) {
			if stepRunsShellScript(step) && !stepUsesBash(step) {
				violations = append(violations, fmt.Sprintf("job %s step %q at line %d", job.name, step.name, step.startLine))
			}
		}
	}
	return violations
}

func parseWorkflowJobs(raw string) []workflowJob {
	var jobs []workflowJob
	current := -1
	inJobs := false
	for index, original := range strings.Split(raw, "\n") {
		line := newWorkflowLine(index+1, original)
		if line.trimmed == "" {
			continue
		}
		if line.indent == 0 {
			if line.trimmed == "jobs:" {
				inJobs = true
				current = -1
				continue
			}
			if inJobs {
				break
			}
		}
		if !inJobs {
			continue
		}
		if line.indent == 2 && strings.HasSuffix(line.trimmed, ":") {
			jobs = append(jobs, workflowJob{name: strings.TrimSuffix(line.trimmed, ":")})
			current = len(jobs) - 1
			continue
		}
		if current >= 0 {
			jobs[current].lines = append(jobs[current].lines, line)
		}
	}
	return jobs
}

func parseWorkflowSteps(job workflowJob) []workflowStep {
	var steps []workflowStep
	current := -1
	inSteps := false
	for _, line := range job.lines {
		if line.indent == 4 && line.trimmed == "steps:" {
			inSteps = true
			current = -1
			continue
		}
		if !inSteps {
			continue
		}
		if line.indent <= 4 {
			break
		}
		if line.indent == 6 && strings.HasPrefix(line.trimmed, "- ") {
			steps = append(steps, workflowStep{name: fmt.Sprintf("line %d", line.number), startLine: line.number})
			current = len(steps) - 1
		}
		if current < 0 {
			continue
		}
		steps[current].lines = append(steps[current].lines, line)
		if key, value, ok := stepField(line); ok && key == "name" {
			steps[current].name = value
		}
	}
	return steps
}

func jobCanRunOnWindows(job workflowJob) bool {
	for index, line := range job.lines {
		if line.indent != 4 {
			continue
		}
		key, value, ok := yamlField(line.trimmed)
		if !ok || key != "runs-on" {
			continue
		}
		if containsWindows(value) {
			return true
		}
		if value == "" {
			for _, continuation := range job.lines[index+1:] {
				if continuation.indent <= line.indent {
					break
				}
				if containsWindows(continuation.trimmed) {
					return true
				}
			}
			return false
		}
		unquoted := strings.Trim(value, `"'`)
		if match := matrixRunnerRef.FindStringSubmatch(unquoted); match != nil {
			return matrixIncludesWindows(job, match[1])
		}
		// Unknown expressions are treated as Windows-capable so a new dynamic
		// runner source cannot silently bypass this policy.
		return strings.Contains(unquoted, "${{")
	}
	return false
}

func matrixIncludesWindows(job workflowJob, runnerKey string) bool {
	matrixIndent := -1
	activeKeyIndent := -1
	for _, line := range job.lines {
		if matrixIndent < 0 {
			if line.trimmed == "matrix:" {
				matrixIndent = line.indent
			}
			continue
		}
		if line.indent <= matrixIndent {
			break
		}
		if activeKeyIndent >= 0 {
			if line.indent <= activeKeyIndent {
				activeKeyIndent = -1
			} else if containsWindows(line.trimmed) {
				return true
			}
		}
		candidate := strings.TrimPrefix(line.trimmed, "- ")
		key, value, ok := yamlField(candidate)
		if !ok || key != runnerKey {
			continue
		}
		if containsWindows(value) {
			return true
		}
		if value == "" {
			activeKeyIndent = line.indent
		}
	}
	return false
}

func stepRunsShellScript(step workflowStep) bool {
	runBlockIndent := -1
	for _, line := range step.lines {
		if runBlockIndent >= 0 {
			if line.indent > runBlockIndent {
				if strings.Contains(strings.ToLower(line.original), ".sh") {
					return true
				}
				continue
			}
			runBlockIndent = -1
		}
		key, value, ok := stepField(line)
		if !ok || key != "run" {
			continue
		}
		if strings.Contains(strings.ToLower(line.original), ".sh") {
			return true
		}
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			runBlockIndent = line.indent
		}
	}
	return false
}

func stepUsesBash(step workflowStep) bool {
	for _, line := range step.lines {
		key, value, ok := stepField(line)
		if ok && key == "shell" && strings.Trim(value, `"'`) == "bash" {
			return true
		}
	}
	return false
}

func stepField(line workflowLine) (string, string, bool) {
	if line.indent != 6 && line.indent != 8 {
		return "", "", false
	}
	value := line.trimmed
	if line.indent == 6 {
		if !strings.HasPrefix(value, "- ") {
			return "", "", false
		}
		value = strings.TrimPrefix(value, "- ")
	}
	return yamlField(value)
}

func yamlField(value string) (string, string, bool) {
	key, fieldValue, ok := strings.Cut(value, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(key), strings.TrimSpace(fieldValue), true
}

func containsWindows(value string) bool {
	return strings.Contains(strings.ToLower(value), "windows")
}

func newWorkflowLine(number int, original string) workflowLine {
	structural := original
	if index := strings.Index(structural, "#"); index >= 0 {
		structural = structural[:index]
	}
	structural = strings.TrimRight(structural, " \t")
	return workflowLine{
		number:   number,
		original: original,
		trimmed:  strings.TrimSpace(structural),
		indent:   leadingSpaces(structural),
	}
}

func parseWorkflow(path string) workflow {
	data, err := os.ReadFile(path)
	check(err)
	raw := strings.ReplaceAll(string(data), "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	result := workflow{jobPermissions: make(map[string]permissionSet), raw: raw}
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
