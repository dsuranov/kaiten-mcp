//go:build ignore

// Command verify-native-lifecycle-evidence validates the downloaded five-runner
// artifact set before it is admitted to a release audit package.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

const (
	maxEvidenceSize = 8 * 1024 * 1024
	healthEndpoint  = "http://127.0.0.1:8100/health"
	mcpEndpoint     = "http://127.0.0.1:8100/mcp"
	goVersion       = "go1.26.5"
	serviceLabel    = "io.github.dsuranov.kaiten-mcp"
	serviceUnit     = "kaiten-mcp.service"
)

type target struct {
	directory, runnerID, runnerLabel string
	goos, goarch, runnerOS           string
	manager, identity                string
}

var reviewedTargets = []target{
	{directory: "native-lifecycle-macos-amd64", runnerID: "macos-amd64", runnerLabel: "macos-15-intel", goos: "darwin", goarch: "amd64", runnerOS: "macOS", manager: "launchctl", identity: serviceLabel},
	{directory: "native-lifecycle-macos-arm64", runnerID: "macos-arm64", runnerLabel: "macos-latest", goos: "darwin", goarch: "arm64", runnerOS: "macOS", manager: "launchctl", identity: serviceLabel},
	{directory: "native-lifecycle-linux-amd64", runnerID: "linux-amd64", runnerLabel: "ubuntu-latest", goos: "linux", goarch: "amd64", runnerOS: "Linux", manager: "systemd --user over DBus", identity: serviceUnit},
	{directory: "native-lifecycle-linux-arm64", runnerID: "linux-arm64", runnerLabel: "ubuntu-24.04-arm", goos: "linux", goarch: "arm64", runnerOS: "Linux", manager: "systemd --user over DBus", identity: serviceUnit},
	{directory: "native-lifecycle-windows-amd64", runnerID: "windows-amd64", runnerLabel: "windows-latest", goos: "windows", goarch: "amd64", runnerOS: "Windows", manager: "Windows Startup", identity: "kaiten-mcp.cmd"},
}

var (
	lowerSHA256    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	lowerGitSHA    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releaseVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+_-]*$`)
	syntheticToken = regexp.MustCompile(`native-ci-[0-9a-f]{64}`)
	authorization  = regexp.MustCompile(`(?i)authorization\s*:\s*bearer`)
)

var expectedReadOnlyToolNames = []string{
	"get_board", "get_board_structure", "get_card", "get_card_checklists", "get_card_children",
	"get_current_user", "get_member_cards", "get_my_cards", "get_responsible_cards", "get_server_info",
	"get_space", "list_boards", "list_card_types", "list_custom_properties", "list_spaces", "list_tags",
	"list_users", "search_cards",
}

var expectedCheckNames = []string{
	"release-binary-binding",
	"fixture-sha256",
	"preflight-port",
	"preflight-service-identity",
	"mock-api",
	"install-v1",
	"install-health",
	"permissions",
	"mcp-api-auth",
	"secret-containment-before-uninstall",
	"native-restart",
	"healthy-update-v2",
	"healthy-update",
	"bad-service-update-v3",
	"failed-update-rollback",
	"uninstall-first",
	"uninstall-second",
	"double-uninstall",
	"final-owned-file-and-secret-scan",
}

var wrapperKeys = []string{
	"runner_label",
	"runner_id",
	"runner_os",
	"candidate_commit",
	"workflow_run_id",
	"workflow_run_attempt",
	"binding_schema",
	"binding_release_repository",
	"binding_release_repository_id",
	"binding_release_run_id",
	"binding_release_run_attempt",
	"binding_release_workflow",
	"binding_release_workflow_path",
	"binding_release_event",
	"binding_release_conclusion",
	"binding_release_tag",
	"binding_release_head_sha",
	"binding_release_artifact_id",
	"binding_release_artifact_name",
	"binding_release_artifact_size",
	"binding_release_artifact_api_digest",
	"binding_release_artifact_zip_sha256",
	"binding_release_manifest_sha256",
	"binding_release_archive",
	"binding_release_archive_sha256",
	"binding_release_version",
	"binding_release_goos",
	"binding_release_goarch",
	"binding_release_go_version",
	"binding_release_kaiten",
	"binding_release_kaiten_sha256",
	"binding_release_kaiten_mcp",
	"binding_release_kaiten_mcp_sha256",
}

type checkRecord struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type summaryRecord struct {
	Schema             string        `json:"schema"`
	Result             string        `json:"result"`
	RunnerLabel        string        `json:"runner_label"`
	RunnerImageOS      string        `json:"runner_image_os"`
	RunnerImageVersion string        `json:"runner_image_version"`
	GOOS               string        `json:"goos"`
	GOARCH             string        `json:"goarch"`
	GoVersion          string        `json:"go_version"`
	Commit             string        `json:"commit"`
	WorkflowRunID      string        `json:"workflow_run_id"`
	WorkflowRunAttempt int           `json:"workflow_run_attempt"`
	StartedUTC         string        `json:"started_utc"`
	FinishedUTC        string        `json:"finished_utc"`
	Artifacts          []string      `json:"artifacts"`
	Checks             []checkRecord `json:"checks"`
}

type fixtureHashes struct {
	v1, v2, v3 string
}

type releaseBinding struct {
	repository, repositoryID, runID, runAttempt string
	workflow, workflowPath, event, conclusion   string
	tag, headSHA                                string
	artifactID, artifactName, artifactSize      string
	artifactAPIDigest, artifactZipSHA256        string
	manifestSHA256, archive, archiveSHA256      string
	version, goos, goarch, goVersion            string
	kaiten, kaitenSHA256                        string
	kaitenMCP, kaitenMCPSHA256                  string
}

func (binding releaseBinding) commonIdentity() string {
	return strings.Join([]string{
		binding.repository, binding.repositoryID, binding.runID, binding.runAttempt,
		binding.workflow, binding.workflowPath, binding.event, binding.conclusion,
		binding.tag, binding.headSHA, binding.artifactID, binding.artifactName,
		binding.artifactSize, binding.artifactAPIDigest, binding.artifactZipSHA256,
		binding.manifestSHA256, binding.version, binding.goVersion,
	}, "\x00")
}

type bundleIdentity struct {
	workflowRunID      string
	workflowRunAttempt int
	releaseCommon      string
}

type artifactDirectory struct {
	path  string
	files map[string][]byte
}

func main() {
	if len(os.Args) != 3 {
		fail("usage: go run ./scripts/verify-native-lifecycle-evidence.go <download-directory> <40-character-commit>")
	}
	identity, err := validateBundle(os.Args[1], strings.TrimSpace(os.Args[2]))
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("verified 5 native lifecycle artifacts: commit=%s workflow_run_id=%s attempt=%d\n", strings.TrimSpace(os.Args[2]), identity.workflowRunID, identity.workflowRunAttempt)
}

func validateBundle(rootInput, commit string) (bundleIdentity, error) {
	var common bundleIdentity
	if !lowerGitSHA.MatchString(commit) {
		return common, errors.New("expected commit must be a lowercase 40-character Git SHA")
	}
	root, err := filepath.Abs(rootInput)
	if err != nil {
		return common, err
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return common, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return common, fmt.Errorf("evidence root is not a real directory: %s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return common, err
	}
	wanted := make(map[string]target, len(reviewedTargets))
	for _, reviewed := range reviewedTargets {
		wanted[reviewed.directory] = reviewed
	}
	if len(entries) != len(wanted) {
		return common, fmt.Errorf("evidence root contains %d entries, want exactly %d reviewed artifact directories", len(entries), len(wanted))
	}
	seen := make(map[string]bool, len(wanted))
	for _, entry := range entries {
		reviewed, ok := wanted[entry.Name()]
		if !ok {
			return common, fmt.Errorf("evidence root contains unexpected entry %s", entry.Name())
		}
		path := filepath.Join(root, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return common, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return common, fmt.Errorf("reviewed artifact entry is not a real directory: %s", entry.Name())
		}
		if seen[entry.Name()] {
			return common, fmt.Errorf("duplicate reviewed artifact directory %s", entry.Name())
		}
		seen[entry.Name()] = true
		directory, loadErr := loadArtifactDirectory(path, reviewed)
		if loadErr != nil {
			return common, fmt.Errorf("%s: %w", entry.Name(), loadErr)
		}
		identity, validateErr := validateArtifactDirectory(directory, reviewed, commit)
		if validateErr != nil {
			return common, fmt.Errorf("%s: %w", entry.Name(), validateErr)
		}
		if common.workflowRunID == "" {
			common = identity
		} else if identity != common {
			return common, errors.New("native evidence mixes workflow runs, attempts, or release bindings")
		}
	}
	for name := range wanted {
		if !seen[name] {
			return common, fmt.Errorf("missing reviewed artifact directory %s", name)
		}
	}
	return common, nil
}

func loadArtifactDirectory(path string, reviewed target) (artifactDirectory, error) {
	result := artifactDirectory{path: path, files: make(map[string][]byte)}
	allowed := map[string]bool{"summary.json": true, "wrapper-context.txt": true}
	for _, name := range nativeci.RequiredEvidenceArtifacts() {
		allowed[name] = true
	}
	if reviewed.goos == "linux" {
		allowed["linux-wrapper-cleanup.json"] = true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return result, err
	}
	if len(entries) != len(allowed) {
		return result, fmt.Errorf("artifact directory contains %d entries, want exactly %d", len(entries), len(allowed))
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return result, fmt.Errorf("unexpected evidence entry %s", entry.Name())
		}
		fullPath := filepath.Join(path, entry.Name())
		data, readErr := readSafe(fullPath)
		if readErr != nil {
			return result, readErr
		}
		result.files[entry.Name()] = data
	}
	for name := range allowed {
		if _, ok := result.files[name]; !ok {
			return result, fmt.Errorf("missing evidence entry %s", name)
		}
	}
	return result, nil
}

func validateArtifactDirectory(directory artifactDirectory, reviewed target, commit string) (bundleIdentity, error) {
	var identity bundleIdentity
	summary, fixtures, err := validateSummary(directory.files["summary.json"], reviewed, commit)
	if err != nil {
		return identity, err
	}
	binding, err := validateWrapperContext(directory.files["wrapper-context.txt"], reviewed, summary, commit)
	if err != nil {
		return identity, err
	}
	if err := validateReleaseBinaries(directory.files["release-binaries.json"], reviewed, binding); err != nil {
		return identity, err
	}
	if fixtures.v2 != binding.kaitenMCPSHA256 {
		return identity, errors.New("fixture v2 SHA-256 is not the shipped kaiten-mcp SHA-256")
	}
	if err := validateClients(directory); err != nil {
		return identity, err
	}
	if err := validateCommands(directory.files["commands.json"], reviewed); err != nil {
		return identity, err
	}
	if err := validateHealth(directory, binding.version); err != nil {
		return identity, err
	}
	if err := validateManagers(directory, reviewed); err != nil {
		return identity, err
	}
	if err := validateMCPProofs(directory, binding.version); err != nil {
		return identity, err
	}
	if err := validatePermissionArtifact("permissions.json", directory.files["permissions.json"], reviewed.goos); err != nil {
		return identity, err
	}
	if err := validatePermissionArtifact("rollback-backup-permissions.json", directory.files["rollback-backup-permissions.json"], reviewed.goos); err != nil {
		return identity, err
	}
	if err := validateRollbackHashes(directory.files["rollback-hashes.json"], binding.kaitenMCPSHA256); err != nil {
		return identity, err
	}
	if err := validateRemainingFiles(directory.files["remaining-files.json"], reviewed.goos); err != nil {
		return identity, err
	}
	if string(directory.files["service-definition.txt"]) != expectedServiceDefinition(reviewed.goos) {
		return identity, errors.New("service-definition.txt is not the exact reviewed profile-local service definition")
	}
	if err := validatePostCleanup(directory.files["post-harness-cleanup.json"]); err != nil {
		return identity, err
	}
	if reviewed.goos == "linux" {
		if err := validateLinuxWrapperCleanup(directory.files["linux-wrapper-cleanup.json"]); err != nil {
			return identity, err
		}
	}
	return bundleIdentity{workflowRunID: summary.WorkflowRunID, workflowRunAttempt: summary.WorkflowRunAttempt, releaseCommon: binding.commonIdentity()}, nil
}

func validateSummary(data []byte, reviewed target, commit string) (summaryRecord, fixtureHashes, error) {
	var summary summaryRecord
	var fixtures fixtureHashes
	if err := decodeStrictJSON(data, &summary); err != nil {
		return summary, fixtures, fmt.Errorf("summary.json: %w", err)
	}
	if summary.Schema != "kaiten-native-lifecycle/v2" || summary.Result != "passed" {
		return summary, fixtures, errors.New("summary.json is not an exact passing v2 evidence record")
	}
	if summary.RunnerLabel != reviewed.runnerLabel || summary.GOOS != reviewed.goos || summary.GOARCH != reviewed.goarch {
		return summary, fixtures, errors.New("summary.json runner identity does not match its reviewed matrix directory")
	}
	if summary.RunnerImageOS == "" || summary.RunnerImageVersion == "" {
		return summary, fixtures, errors.New("summary.json omits hosted runner image identity")
	}
	if summary.GoVersion != goVersion || summary.Commit != commit {
		return summary, fixtures, errors.New("summary.json toolchain or candidate commit does not match the reviewed identity")
	}
	if !positiveDecimal(summary.WorkflowRunID) || summary.WorkflowRunAttempt < 1 {
		return summary, fixtures, errors.New("summary.json has an invalid workflow run identity")
	}
	started, startErr := time.Parse(time.RFC3339, summary.StartedUTC)
	finished, finishErr := time.Parse(time.RFC3339, summary.FinishedUTC)
	if startErr != nil || finishErr != nil || !strings.HasSuffix(summary.StartedUTC, "Z") || !strings.HasSuffix(summary.FinishedUTC, "Z") || finished.Before(started) {
		return summary, fixtures, errors.New("summary.json has invalid ordered UTC timestamps")
	}
	wantArtifacts := nativeci.RequiredEvidenceArtifacts()
	sort.Strings(wantArtifacts)
	if !equalExactStrings(summary.Artifacts, wantArtifacts) {
		return summary, fixtures, errors.New("summary.json artifact inventory is not the exact reviewed set")
	}
	if len(summary.Checks) != len(expectedCheckNames) {
		return summary, fixtures, fmt.Errorf("summary.json contains %d checks, want %d", len(summary.Checks), len(expectedCheckNames))
	}
	for index, expected := range expectedCheckNames {
		check := summary.Checks[index]
		if check.Name != expected || check.Status != "passed" || strings.TrimSpace(check.Detail) == "" || len(check.Detail) > 8192 {
			return summary, fixtures, fmt.Errorf("summary.json check %d is not exact passing check %s", index, expected)
		}
		if check.Name == "fixture-sha256" {
			match := regexp.MustCompile(`^v1=([0-9a-f]{64}); v2=([0-9a-f]{64}); v3-no-health=([0-9a-f]{64})$`).FindStringSubmatch(check.Detail)
			if len(match) != 4 || match[1] == match[2] || match[1] == match[3] || match[2] == match[3] {
				return summary, fixtures, errors.New("summary.json fixture-sha256 check is invalid or not distinct")
			}
			fixtures = fixtureHashes{v1: match[1], v2: match[2], v3: match[3]}
		}
	}
	if fixtures.v2 == "" {
		return summary, fixtures, errors.New("summary.json omits structured fixture SHA-256 identities")
	}
	return summary, fixtures, nil
}

func validateWrapperContext(data []byte, reviewed target, summary summaryRecord, commit string) (releaseBinding, error) {
	var binding releaseBinding
	values, err := parseExactKeyValues(data, wrapperKeys)
	if err != nil {
		return binding, fmt.Errorf("wrapper-context.txt: %w", err)
	}
	if values["runner_label"] != reviewed.runnerLabel || values["runner_id"] != reviewed.runnerID || values["runner_os"] != reviewed.runnerOS {
		return binding, errors.New("wrapper-context.txt runner identity does not match the reviewed matrix")
	}
	if values["candidate_commit"] != commit || values["workflow_run_id"] != summary.WorkflowRunID || values["workflow_run_attempt"] != strconv.Itoa(summary.WorkflowRunAttempt) {
		return binding, errors.New("wrapper-context.txt does not match summary.json candidate and workflow identity")
	}
	binding = releaseBinding{
		repository: values["binding_release_repository"], repositoryID: values["binding_release_repository_id"],
		runID: values["binding_release_run_id"], runAttempt: values["binding_release_run_attempt"],
		workflow: values["binding_release_workflow"], workflowPath: values["binding_release_workflow_path"],
		event: values["binding_release_event"], conclusion: values["binding_release_conclusion"],
		tag: values["binding_release_tag"], headSHA: values["binding_release_head_sha"],
		artifactID: values["binding_release_artifact_id"], artifactName: values["binding_release_artifact_name"],
		artifactSize: values["binding_release_artifact_size"], artifactAPIDigest: values["binding_release_artifact_api_digest"],
		artifactZipSHA256: values["binding_release_artifact_zip_sha256"], manifestSHA256: values["binding_release_manifest_sha256"],
		archive: values["binding_release_archive"], archiveSHA256: values["binding_release_archive_sha256"],
		version: values["binding_release_version"], goos: values["binding_release_goos"], goarch: values["binding_release_goarch"],
		goVersion: values["binding_release_go_version"], kaiten: values["binding_release_kaiten"],
		kaitenSHA256: values["binding_release_kaiten_sha256"], kaitenMCP: values["binding_release_kaiten_mcp"],
		kaitenMCPSHA256: values["binding_release_kaiten_mcp_sha256"],
	}
	if values["binding_schema"] != "kaiten-native-release-binding/v1" {
		return binding, errors.New("wrapper context release binding schema is not v1")
	}
	if binding.repository != "dsuranov/kaiten-mcp" || !positiveDecimal(binding.repositoryID) {
		return binding, errors.New("wrapper context has an invalid release repository identity")
	}
	workflowPathValid := binding.workflowPath == ".github/workflows/release.yml" ||
		binding.workflowPath == ".github/workflows/release.yml@"+binding.tag ||
		binding.workflowPath == ".github/workflows/release.yml@refs/tags/"+binding.tag
	if !positiveDecimal(binding.runID) || binding.runAttempt != "1" || binding.workflow != "Release" || !workflowPathValid || binding.event != "push" || binding.conclusion != "success" {
		return binding, errors.New("wrapper context has an invalid successful Release workflow identity")
	}
	if !releaseVersion.MatchString(binding.version) || binding.tag != "v"+binding.version || binding.headSHA != commit {
		return binding, errors.New("wrapper context release version, tag, or head SHA is invalid")
	}
	if !positiveDecimal(binding.artifactID) || !positiveDecimal(binding.artifactSize) || binding.artifactName != "release-assets" {
		return binding, errors.New("wrapper context release artifact identity is invalid")
	}
	if binding.artifactAPIDigest != "sha256:"+binding.artifactZipSHA256 || !lowerSHA256.MatchString(binding.artifactZipSHA256) || !lowerSHA256.MatchString(binding.manifestSHA256) || !lowerSHA256.MatchString(binding.archiveSHA256) || !lowerSHA256.MatchString(binding.kaitenSHA256) || !lowerSHA256.MatchString(binding.kaitenMCPSHA256) || binding.kaitenSHA256 == binding.kaitenMCPSHA256 {
		return binding, errors.New("wrapper context release digests are not lowercase SHA-256 identities")
	}
	if binding.goos != reviewed.goos || binding.goarch != reviewed.goarch || binding.goVersion != goVersion {
		return binding, errors.New("wrapper context shipped runtime does not match the reviewed target")
	}
	extension := ".tar.gz"
	kaiten, kaitenMCP := "kaiten", "kaiten-mcp"
	if reviewed.goos == "windows" {
		extension, kaiten, kaitenMCP = ".zip", "kaiten.exe", "kaiten-mcp.exe"
	}
	wantArchive := fmt.Sprintf("kaiten_%s_%s_%s%s", binding.version, reviewed.goos, reviewed.goarch, extension)
	if binding.archive != wantArchive || binding.kaiten != kaiten || binding.kaitenMCP != kaitenMCP {
		return binding, errors.New("wrapper context shipped archive or binary names do not match the reviewed target")
	}
	return binding, nil
}

type binarySmokeEvidence struct {
	Name          string `json:"name"`
	SHA256        string `json:"sha256"`
	VersionOutput string `json:"version_output"`
	GoVersion     string `json:"go_version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Launched      *bool  `json:"launched"`
	ExitCode      *int   `json:"exit_code"`
}

type releaseBinariesEvidence struct {
	Schema                string                `json:"schema"`
	ReleaseRunID          string                `json:"release_run_id"`
	ReleaseRunAttempt     string                `json:"release_run_attempt"`
	ReleaseTag            string                `json:"release_tag"`
	ReleaseHeadSHA        string                `json:"release_head_sha"`
	ReleaseManifestSHA256 string                `json:"release_manifest_sha256"`
	ReleaseArchive        string                `json:"release_archive"`
	ReleaseArchiveSHA256  string                `json:"release_archive_sha256"`
	ReleaseArtifactName   string                `json:"release_artifact_name"`
	Binaries              []binarySmokeEvidence `json:"binaries"`
}

func validateReleaseBinaries(data []byte, reviewed target, binding releaseBinding) error {
	var proof releaseBinariesEvidence
	if err := decodeStrictJSON(data, &proof); err != nil {
		return fmt.Errorf("release-binaries.json: %w", err)
	}
	if proof.Schema != "kaiten-native-release-binaries/v1" || proof.ReleaseRunID != binding.runID || proof.ReleaseRunAttempt != binding.runAttempt || proof.ReleaseTag != binding.tag || proof.ReleaseHeadSHA != binding.headSHA || proof.ReleaseManifestSHA256 != binding.manifestSHA256 || proof.ReleaseArchive != binding.archive || proof.ReleaseArchiveSHA256 != binding.archiveSHA256 || proof.ReleaseArtifactName != binding.artifactName {
		return errors.New("release-binaries.json provenance does not match wrapper release binding")
	}
	wantNames := []string{"kaiten", "kaiten-mcp"}
	wantHashes := []string{binding.kaitenSHA256, binding.kaitenMCPSHA256}
	if len(proof.Binaries) != len(wantNames) {
		return errors.New("release-binaries.json must contain exactly two shipped binaries")
	}
	for index, binary := range proof.Binaries {
		if binary.Name != wantNames[index] || binary.SHA256 != wantHashes[index] || binary.VersionOutput != wantNames[index]+" "+binding.version || binary.GoVersion != goVersion || binary.GOOS != reviewed.goos || binary.GOARCH != reviewed.goarch || binary.Launched == nil || !*binary.Launched || binary.ExitCode == nil || *binary.ExitCode != 0 {
			return fmt.Errorf("release-binaries.json shipped binary %d is not exact", index)
		}
	}
	return nil
}

type clientServer struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type clientServers struct {
	Other  *clientServer `json:"other"`
	Kaiten *clientServer `json:"kaiten,omitempty"`
}

type clientSentinel struct {
	Preserve *bool `json:"preserve"`
}

type clientDocument struct {
	Unrelated *clientSentinel `json:"unrelated"`
	Servers   *clientServers  `json:"mcpServers"`
}

type clientState struct {
	ClaudeCode    *clientDocument `json:"claude_code"`
	ClaudeDesktop *clientDocument `json:"claude_desktop"`
}

func validateClients(directory artifactDirectory) error {
	states := make(map[string]clientState, 5)
	registered := map[string]bool{
		"clients-before-install.json":  false,
		"clients-registered-v1.json":   true,
		"clients-registered-v2.json":   true,
		"clients-after-rollback.json":  true,
		"clients-after-uninstall.json": false,
	}
	for name, wantRegistered := range registered {
		var state clientState
		if err := decodeStrictJSON(directory.files[name], &state); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := validateClientState(name, state, wantRegistered); err != nil {
			return err
		}
		states[name] = state
	}
	if !reflect.DeepEqual(states["clients-before-install.json"], states["clients-after-uninstall.json"]) {
		return errors.New("client state after uninstall does not exactly restore the unrelated initial state")
	}
	if !reflect.DeepEqual(states["clients-registered-v1.json"], states["clients-registered-v2.json"]) || !reflect.DeepEqual(states["clients-registered-v2.json"], states["clients-after-rollback.json"]) {
		return errors.New("registered client state was not exactly preserved through update and rollback")
	}
	return nil
}

func validateClientState(name string, state clientState, registered bool) error {
	for role, document := range map[string]*clientDocument{"claude_code": state.ClaudeCode, "claude_desktop": state.ClaudeDesktop} {
		if document == nil || document.Unrelated == nil || document.Unrelated.Preserve == nil || !*document.Unrelated.Preserve || document.Servers == nil || document.Servers.Other == nil {
			return fmt.Errorf("%s omits exact unrelated %s client sentinels", name, role)
		}
		if document.Servers.Other.Type != "http" || document.Servers.Other.URL != "http://127.0.0.1:65534/mcp" {
			return fmt.Errorf("%s changed the unrelated %s MCP server", name, role)
		}
		if registered {
			if document.Servers.Kaiten == nil || document.Servers.Kaiten.Type != "http" || document.Servers.Kaiten.URL != mcpEndpoint {
				return fmt.Errorf("%s omits the exact %s kaiten registration", name, role)
			}
		} else if document.Servers.Kaiten != nil {
			return fmt.Errorf("%s unexpectedly retains the %s kaiten registration", name, role)
		}
	}
	if !reflect.DeepEqual(state.ClaudeCode, state.ClaudeDesktop) {
		return fmt.Errorf("%s client documents are not exact peers", name)
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

func validateCommands(data []byte, reviewed target) error {
	var commands []commandEvidence
	if err := decodeStrictJSON(data, &commands); err != nil {
		return fmt.Errorf("commands.json: %w", err)
	}
	wantSteps := []string{"install-v1", "healthy-update-v2", "bad-service-update-v3", "uninstall-first", "uninstall-second"}
	binary := "kaiten-mcp"
	if reviewed.goos == "windows" {
		binary += ".exe"
	}
	wantCommands := []string{binary + " install", binary + " install", binary + " install", binary + " uninstall", binary + " uninstall"}
	const v3Failure = `error: installation failed: verify installed service version "native-v3": service did not become healthy before the readiness deadline`
	if len(commands) != len(wantSteps) {
		return fmt.Errorf("commands.json contains %d commands, want %d", len(commands), len(wantSteps))
	}
	for index, command := range commands {
		if command.Step != wantSteps[index] || command.Command != wantCommands[index] || command.DurationNS <= 0 || command.DurationNS > int64(46*time.Second) {
			return fmt.Errorf("commands.json command %d has an invalid ordered identity or duration", index)
		}
		if len(command.Stdout) > 4110 || len(command.Stderr) > 4110 || (len(command.Stdout) > 4096 && !strings.HasSuffix(command.Stdout, "...[truncated]")) || (len(command.Stderr) > 4096 && !strings.HasSuffix(command.Stderr, "...[truncated]")) {
			return fmt.Errorf("commands.json command %s contains unbounded captured output", command.Step)
		}
		if index == 2 {
			if command.ExitCode != 1 || command.Stderr != v3Failure {
				return errors.New("commands.json bad v3 update is not the exact bounded health-check failure")
			}
		} else if command.ExitCode != 0 || command.Stderr != "" {
			return fmt.Errorf("commands.json command %s is not an exact successful lifecycle invocation", command.Step)
		}
	}
	return nil
}

type healthEvidence struct {
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
}

func validateHealth(directory artifactDirectory, shippedVersion string) error {
	versions := []struct{ name, version string }{
		{"health-install-v1.json", "native-v1"},
		{"health-restart-v1.json", "native-v1"},
		{"health-update-v2.json", shippedVersion},
		{"health-rollback-v2.json", shippedVersion},
	}
	for _, expected := range versions {
		var health healthEvidence
		if err := decodeStrictJSON(directory.files[expected.name], &health); err != nil {
			return fmt.Errorf("%s: %w", expected.name, err)
		}
		if health.Endpoint != healthEndpoint || health.Status != "ok" || health.Version != expected.version || health.Runtime != goVersion {
			return fmt.Errorf("%s does not prove exact healthy version %s", expected.name, expected.version)
		}
	}
	return nil
}

type managerEvidence struct {
	Stage    string `json:"stage"`
	Manager  string `json:"manager"`
	Identity string `json:"identity"`
	Active   *bool  `json:"active"`
	PID      *int   `json:"pid"`
}

func validateManagers(directory artifactDirectory, reviewed target) error {
	var states []managerEvidence
	if err := decodeStrictJSON(directory.files["manager-states.json"], &states); err != nil {
		return fmt.Errorf("manager-states.json: %w", err)
	}
	wantStages := []string{"install-v1", "restart-v1", "update-v2", "rollback-v2", "final"}
	rawNames := []string{"manager-install-v1.txt", "manager-restart-v1.txt", "manager-update-v2.txt", "manager-rollback-v2.txt"}
	if len(states) != len(wantStages) {
		return fmt.Errorf("manager-states.json contains %d states, want %d", len(states), len(wantStages))
	}
	for index, state := range states {
		if state.Stage != wantStages[index] || state.Manager != reviewed.manager || state.Identity != reviewed.identity || state.Active == nil || state.PID == nil {
			return fmt.Errorf("manager-states.json state %d is not exact", index)
		}
		if index < len(rawNames) {
			if !*state.Active || *state.PID < 1 {
				return fmt.Errorf("manager-states.json state %s is not active with a PID", state.Stage)
			}
			if err := validateManagerRaw(rawNames[index], directory.files[rawNames[index]], reviewed, *state.PID); err != nil {
				return err
			}
		} else if *state.Active || *state.PID != 0 {
			return errors.New("manager-states.json final state is not absent")
		}
	}
	if *states[0].PID == *states[1].PID {
		return errors.New("manager-states.json does not prove a real PID change across native restart")
	}
	wantFinal := fmt.Sprintf("manager=%s\nidentity=%s\nstate=absent\nexecutable=%s\nprocess=absent\nport_8100=free\n", reviewed.manager, reviewed.identity, expectedProfileBinary(reviewed.goos))
	if string(directory.files["manager-final.txt"]) != wantFinal {
		return errors.New("manager-final.txt is not the exact manager, process, and port absence proof")
	}
	return nil
}

func validateManagerRaw(name string, data []byte, reviewed target, pid int) error {
	value := string(data)
	binary := expectedProfileBinary(reviewed.goos)
	markers := []string{binary, "--transport", "streamable-http", "--host", "127.0.0.1", "--port", "8100", "--streamable-http-path", "/mcp"}
	switch reviewed.goos {
	case "darwin":
		markers = append(markers, serviceLabel, "state = running")
		if !regexp.MustCompile(fmt.Sprintf(`(?m)^\s*pid = %d\s*$`, pid)).Match(data) {
			return fmt.Errorf("%s does not retain the manager PID", name)
		}
	case "linux":
		markers = append(markers, serviceUnit, "Loaded: loaded", "Active: active (running)")
		if !regexp.MustCompile(fmt.Sprintf(`(?m)Main PID:\s*%d(?:\s|$)`, pid)).Match(data) {
			return fmt.Errorf("%s does not retain the manager PID", name)
		}
	case "windows":
		var status struct {
			ProcessID      *int   `json:"ProcessId"`
			ExecutablePath string `json:"ExecutablePath"`
			CommandLine    string `json:"CommandLine"`
		}
		if err := decodeStrictJSON(data, &status); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if status.ProcessID == nil || *status.ProcessID != pid || status.ExecutablePath != binary {
			return fmt.Errorf("%s does not retain the exact Windows process identity", name)
		}
		value = status.CommandLine
		markers = markers[1:]
	default:
		return fmt.Errorf("unsupported reviewed OS %s", reviewed.goos)
	}
	for _, marker := range markers {
		if !strings.Contains(value, marker) {
			return fmt.Errorf("%s omits native manager marker %q", name, marker)
		}
	}
	return nil
}

type mcpEvidence struct {
	Stage                       string   `json:"stage"`
	Endpoint                    string   `json:"endpoint"`
	ServerVersion               string   `json:"server_version"`
	ProtocolVersion             string   `json:"protocol_version"`
	SessionEstablished          *bool    `json:"session_established"`
	ToolNames                   []string `json:"tool_names"`
	ReadOnlyToolCount           int      `json:"read_only_tool_count"`
	WriteToolCount              int      `json:"write_tool_count"`
	RepresentativeTool          string   `json:"representative_tool"`
	RepresentativeReadSucceeded *bool    `json:"representative_read_succeeded"`
	AuthorizedRequestCount      int      `json:"authorized_request_count"`
	UnauthorizedRequestCount    int      `json:"unauthorized_request_count"`
	MockMethod                  string   `json:"mock_method"`
	MockPath                    string   `json:"mock_path"`
	AuthHeaderValid             *bool    `json:"auth_header_valid"`
}

func validateMCPProofs(directory artifactDirectory, shippedVersion string) error {
	wants := []struct {
		name, stage, version string
		authorized           int
	}{
		{"mcp-auth-v1.json", "install-v1", "native-v1", 1},
		{"mcp-auth-rollback-v2.json", "rollback-v2", shippedVersion, 2},
	}
	for _, want := range wants {
		var proof mcpEvidence
		if err := decodeStrictJSON(directory.files[want.name], &proof); err != nil {
			return fmt.Errorf("%s: %w", want.name, err)
		}
		if proof.Stage != want.stage || proof.Endpoint != mcpEndpoint || proof.ServerVersion != want.version || proof.ProtocolVersion != "2025-06-18" || proof.SessionEstablished == nil || !*proof.SessionEstablished || !equalExactStrings(proof.ToolNames, expectedReadOnlyToolNames) || proof.ReadOnlyToolCount != len(expectedReadOnlyToolNames) || proof.WriteToolCount != 0 || proof.RepresentativeTool != "get_current_user" || proof.RepresentativeReadSucceeded == nil || !*proof.RepresentativeReadSucceeded || proof.AuthorizedRequestCount != want.authorized || proof.UnauthorizedRequestCount != 0 || proof.MockMethod != "GET" || proof.MockPath != "/api/v1/users/current" || proof.AuthHeaderValid == nil || !*proof.AuthHeaderValid {
			return fmt.Errorf("%s does not prove the exact authenticated read-only MCP behavior", want.name)
		}
	}
	return nil
}

type permissionEvidence struct {
	Role                 string  `json:"role"`
	Mode                 *string `json:"mode,omitempty"`
	OwnerCurrentUser     *bool   `json:"owner_current_user"`
	ACLCurrentUser       *bool   `json:"acl_current_user,omitempty"`
	ACLSystem            *bool   `json:"acl_system,omitempty"`
	UnexpectedAllowCount *int    `json:"unexpected_allow_count"`
}

func validatePermissionArtifact(name string, data []byte, goos string) error {
	var permissions []permissionEvidence
	if err := decodeStrictJSON(data, &permissions); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	wantRoles := []string{"binary", "environment", "service_definition", "claude_code", "claude_desktop"}
	if len(permissions) != len(wantRoles) {
		return fmt.Errorf("%s contains %d roles, want %d", name, len(permissions), len(wantRoles))
	}
	for index, permission := range permissions {
		if permission.Role != wantRoles[index] || permission.OwnerCurrentUser == nil || !*permission.OwnerCurrentUser || permission.UnexpectedAllowCount == nil || *permission.UnexpectedAllowCount != 0 {
			return fmt.Errorf("%s role %d is incomplete", name, index)
		}
		if goos == "windows" {
			if permission.Mode != nil || permission.ACLCurrentUser == nil || !*permission.ACLCurrentUser || permission.ACLSystem == nil || !*permission.ACLSystem {
				return fmt.Errorf("%s Windows role %s is not restricted to current user and SYSTEM", name, permission.Role)
			}
		} else {
			wantMode := "0600"
			if permission.Role == "binary" {
				wantMode = "0700"
			}
			if permission.Mode == nil || *permission.Mode != wantMode || permission.ACLCurrentUser != nil || permission.ACLSystem != nil {
				return fmt.Errorf("%s Unix role %s has an invalid mode or ACL shape", name, permission.Role)
			}
		}
	}
	return nil
}

type rollbackHashes struct {
	Before map[string]string `json:"before_failed_update"`
	After  map[string]string `json:"after_rollback"`
}

func validateRollbackHashes(data []byte, releaseMCPHash string) error {
	var hashes rollbackHashes
	if err := decodeStrictJSON(data, &hashes); err != nil {
		return fmt.Errorf("rollback-hashes.json: %w", err)
	}
	wantRoles := []string{"binary", "environment", "service_definition"}
	if !exactMapKeys(hashes.Before, wantRoles) || !exactMapKeys(hashes.After, wantRoles) {
		return errors.New("rollback-hashes.json does not contain the exact owned role set")
	}
	for _, role := range wantRoles {
		if !lowerSHA256.MatchString(hashes.Before[role]) || hashes.Before[role] != hashes.After[role] {
			return fmt.Errorf("rollback-hashes.json has an invalid or changed %s SHA-256", role)
		}
	}
	if hashes.Before["binary"] != releaseMCPHash {
		return errors.New("rollback-hashes.json binary is not tied to the shipped kaiten-mcp SHA-256")
	}
	return nil
}

func validateRemainingFiles(data []byte, goos string) error {
	var remaining []string
	if err := decodeStrictJSON(data, &remaining); err != nil {
		return fmt.Errorf("remaining-files.json: %w", err)
	}
	var want []string
	switch goos {
	case "darwin":
		want = []string{".claude.json", ".claude.json.bak", "Library/Application Support/Claude/claude_desktop_config.json", "Library/Application Support/Claude/claude_desktop_config.json.bak", "Library/Application Support/kaiten-mcp/logs/service.log"}
	case "linux":
		want = []string{".claude.json", ".claude.json.bak", ".config/Claude/claude_desktop_config.json", ".config/Claude/claude_desktop_config.json.bak", ".local/state/kaiten-mcp/service.log"}
	case "windows":
		want = []string{".claude.json", ".claude.json.bak", "AppData/Local/KaitenMCP/logs/service.log", "AppData/Roaming/Claude/claude_desktop_config.json", "AppData/Roaming/Claude/claude_desktop_config.json.bak"}
	default:
		return fmt.Errorf("unsupported reviewed OS %s", goos)
	}
	if !equalExactStrings(remaining, want) {
		return errors.New("remaining-files.json is not the exact OS-specific allowlist")
	}
	return nil
}

type postCleanupEvidence struct {
	ProfileAbsent *bool `json:"profile_absent"`
	ServiceAbsent *bool `json:"service_absent"`
	ProcessAbsent *bool `json:"process_absent"`
	Port8100Free  *bool `json:"port_8100_free"`
}

func validatePostCleanup(data []byte) error {
	var proof postCleanupEvidence
	if err := decodeStrictJSON(data, &proof); err != nil {
		return fmt.Errorf("post-harness-cleanup.json: %w", err)
	}
	if proof.ProfileAbsent == nil || !*proof.ProfileAbsent || proof.ServiceAbsent == nil || !*proof.ServiceAbsent || proof.ProcessAbsent == nil || !*proof.ProcessAbsent || proof.Port8100Free == nil || !*proof.Port8100Free {
		return errors.New("post-harness-cleanup.json is not a complete true cleanup proof")
	}
	return nil
}

type linuxWrapperCleanup struct {
	Schema             string `json:"schema"`
	Result             string `json:"result"`
	User               string `json:"user"`
	UID                int    `json:"uid"`
	TargetValidated    *bool  `json:"target_validated"`
	UserManagerStopped *bool  `json:"user_manager_stopped"`
	LingerDisabled     *bool  `json:"linger_disabled"`
	ProcessesAbsent    *bool  `json:"processes_absent"`
	Port8100Free       *bool  `json:"port_8100_free"`
	UserDeleted        *bool  `json:"user_deleted"`
	GroupDeleted       *bool  `json:"group_deleted"`
	LoginStateAbsent   *bool  `json:"login_state_absent"`
	StageDeleted       *bool  `json:"stage_deleted"`
}

func validateLinuxWrapperCleanup(data []byte) error {
	var proof linuxWrapperCleanup
	if err := decodeStrictJSON(data, &proof); err != nil {
		return fmt.Errorf("linux-wrapper-cleanup.json: %w", err)
	}
	checks := []*bool{proof.TargetValidated, proof.UserManagerStopped, proof.LingerDisabled, proof.ProcessesAbsent, proof.Port8100Free, proof.UserDeleted, proof.GroupDeleted, proof.LoginStateAbsent, proof.StageDeleted}
	if proof.Schema != "kaiten-linux-wrapper-cleanup/v1" || proof.Result != "passed" || proof.User != "kaitenci" || proof.UID < 1 {
		return errors.New("linux-wrapper-cleanup.json has an invalid identity")
	}
	for _, check := range checks {
		if check == nil || !*check {
			return errors.New("linux-wrapper-cleanup.json is not a complete true cleanup proof")
		}
	}
	return nil
}

func expectedProfileBinary(goos string) string {
	switch goos {
	case "darwin":
		return "$PROFILE/Library/Application Support/kaiten-mcp/bin/kaiten-mcp"
	case "linux":
		return "$PROFILE/.local/share/kaiten-mcp/bin/kaiten-mcp"
	case "windows":
		return `$PROFILE\AppData\Local\KaitenMCP\bin\kaiten-mcp.exe`
	default:
		return ""
	}
}

func expectedServiceDefinition(goos string) string {
	switch goos {
	case "darwin":
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.github.dsuranov.kaiten-mcp</string>
  <key>ProgramArguments</key><array>
    <string>$PROFILE/Library/Application Support/kaiten-mcp/bin/kaiten-mcp</string><string>--transport</string><string>streamable-http</string>
    <string>--host</string><string>127.0.0.1</string><string>--port</string><string>8100</string>
    <string>--streamable-http-path</string><string>/mcp</string>
  </array>
  <key>WorkingDirectory</key><string>$PROFILE/Library/Application Support/kaiten-mcp</string>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$PROFILE/Library/Application Support/kaiten-mcp/logs/service.log</string><key>StandardErrorPath</key><string>$PROFILE/Library/Application Support/kaiten-mcp/logs/service.log</string>
</dict></plist>
`
	case "linux":
		return `[Unit]
Description=Kaiten MCP per-user service
After=network-online.target

[Service]
Type=simple
WorkingDirectory="$PROFILE/.local/share/kaiten-mcp"
ExecStart="$PROFILE/.local/share/kaiten-mcp/bin/kaiten-mcp" --transport streamable-http --host 127.0.0.1 --port 8100 --streamable-http-path /mcp
Restart=on-failure
RestartSec=3
StandardOutput=append:$PROFILE/.local/state/kaiten-mcp/service.log
StandardError=append:$PROFILE/.local/state/kaiten-mcp/service.log

[Install]
WantedBy=default.target
`
	case "windows":
		return "@echo off\r\ncd /d \"$PROFILE\\AppData\\Local\\KaitenMCP\"\r\nstart \"\" /b \"$PROFILE\\AppData\\Local\\KaitenMCP\\bin\\kaiten-mcp.exe\" --transport streamable-http --host 127.0.0.1 --port 8100 --streamable-http-path /mcp >> \"$PROFILE\\AppData\\Local\\KaitenMCP\\logs\\service.log\" 2>&1\r\n"
	default:
		return ""
	}
}

func parseExactKeyValues(data []byte, expected []string) (map[string]string, error) {
	value := string(data)
	if !strings.HasSuffix(value, "\n") {
		return nil, errors.New("context must end with one newline")
	}
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) != len(expected) {
		return nil, fmt.Errorf("context has %d lines, want %d", len(lines), len(expected))
	}
	result := make(map[string]string, len(lines))
	for index, line := range lines {
		key, item, ok := strings.Cut(line, "=")
		if !ok || key != expected[index] || item == "" || strings.ContainsRune(item, '\r') {
			return nil, fmt.Errorf("context line %d is not exact key %s", index+1, expected[index])
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate context key %s", key)
		}
		result[key] = item
	}
	return result, nil
}

func readSafe(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxEvidenceSize {
		return nil, fmt.Errorf("unsafe evidence file type or size: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("evidence is not UTF-8 text: %s", path)
	}
	if err := scanEvidencePayload(data); err != nil {
		return nil, fmt.Errorf("unsafe evidence payload in %s: %w", path, err)
	}
	return data, nil
}

func scanEvidencePayload(data []byte) error {
	value := string(data)
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(strings.ToLower(value))
	if syntheticToken.Match(data) || authorization.Match(data) || strings.Contains(compact, `"authorization":`) || strings.Contains(compact, `"username":"native-lifecycle"`) || strings.Contains(compact, `"id":4242`) || strings.Contains(compact, `"token":`) || strings.Contains(compact, `"api_token":`) || strings.Contains(compact, "kaiten_api_token=") {
		return errors.New("credential material or mock tenant response found")
	}
	return nil
}

func decodeStrictJSON(data []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON token %v", token)
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if valueErr := consumeJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if valueErr := consumeJSONValue(decoder); valueErr != nil {
				return valueErr
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func exactMapKeys(values map[string]string, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func positiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func equalExactStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", arguments...)
	os.Exit(1)
}
