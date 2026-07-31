package nativeci

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Config identifies the local v1/bad-v3 fixtures, exact shipped v2 binaries,
// their release provenance, and disposable lifecycle output locations.
type Config struct {
	V1, V2, V3                           string
	ReleaseKaiten                        string
	Profile, EvidenceDir                 string
	RunnerLabel, Commit                  string
	ReleaseRunID, ReleaseRunAttempt      string
	ReleaseTag, V2Version                string
	ReleaseHeadSHA                       string
	ReleaseManifestSHA256                string
	ReleaseArchive, ReleaseArchiveSHA256 string
	ReleaseKaitenSHA256                  string
	ReleaseKaitenMCPSHA256               string
}

type harness struct {
	config          Config
	paths           layout
	evidence        Evidence
	token           string
	mock            *mockAPI
	client          *http.Client
	captures        []capture
	managers        []managerEvidence
	v2Version       string
	profilePrepared bool
}

// Run executes one real native service-manager lifecycle.
func Run(ctx context.Context, config Config) (runErr error) {
	h := &harness{config: config, evidence: newEvidence(config.RunnerLabel, config.Commit, time.Now()), client: &http.Client{Timeout: 2 * time.Second}}
	if !safeEvidenceDestination(config.EvidenceDir) {
		return errors.New("evidence path must be an absolute non-root directory and not a symbolic link")
	}
	defer func() {
		commandErr := h.writeCommandArtifacts()
		runErr = errors.Join(runErr, commandErr)
		cleanupEvidence, cleanupErr := h.cleanup()
		runErr = errors.Join(runErr, cleanupErr)
		if h.profilePrepared {
			artifactErr := h.writeJSONArtifact("post-harness-cleanup.json", cleanupEvidence)
			runErr = errors.Join(runErr, artifactErr)
		}
		if runErr == nil {
			runErr = h.verifyArtifactSet()
		}
		if runErr == nil {
			h.evidence.Result = "passed"
		} else {
			detail := runErr.Error()
			if h.token != "" && strings.Contains(detail, h.token) {
				detail = "failure detail contained the synthetic token and was suppressed"
			}
			h.evidence.Checks = append(h.evidence.Checks, Check{Name: "gate", Status: "failed", Detail: detail})
		}
		evidencePath := filepath.Join(config.EvidenceDir, "summary.json")
		if evidenceErr := writeEvidence(evidencePath, h.evidence, h.token); evidenceErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write lifecycle evidence: %w", evidenceErr))
		}
	}()
	if err := h.initialize(); err != nil {
		return err
	}
	return h.run(ctx)
}

func safeEvidenceDestination(path string) bool {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return false
	}
	if info, err := os.Lstat(clean); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

func (h *harness) initialize() error {
	config := h.config
	for name, path := range map[string]string{"v1": config.V1, "v2-release": config.V2, "v3": config.V3, "release-kaiten": config.ReleaseKaiten, "profile": config.Profile, "evidence": config.EvidenceDir} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", name)
		}
	}
	profile := filepath.Clean(config.Profile)
	evidenceDir := filepath.Clean(config.EvidenceDir)
	if !strings.Contains(strings.ToLower(filepath.Base(profile)), "native-lifecycle") || profile == filepath.VolumeName(profile)+string(filepath.Separator) {
		return errors.New("refusing a non-disposable native lifecycle profile path")
	}
	if pathsOverlap(profile, evidenceDir) {
		return errors.New("native lifecycle profile and evidence directory must not overlap")
	}
	if info, err := os.Lstat(profile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("native lifecycle profile must not be a symbolic link")
		}
		if !info.IsDir() {
			return errors.New("native lifecycle profile path is not a directory")
		}
		entries, readErr := os.ReadDir(profile)
		if readErr != nil || len(entries) != 0 {
			return errors.New("existing native lifecycle profile must be empty")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	wantBinary := "kaiten-mcp"
	if runtime.GOOS == "windows" {
		wantBinary += ".exe"
	}
	for _, candidate := range []string{config.V1, config.V2, config.V3} {
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			return fmt.Errorf("lifecycle candidate is unavailable: %s", filepath.Base(candidate))
		}
		if !strings.EqualFold(filepath.Base(candidate), wantBinary) {
			return fmt.Errorf("lifecycle candidate must be named %s", wantBinary)
		}
	}
	wantKaiten := "kaiten"
	if runtime.GOOS == "windows" {
		wantKaiten += ".exe"
	}
	if info, err := os.Stat(config.ReleaseKaiten); err != nil || info.IsDir() || !strings.EqualFold(filepath.Base(config.ReleaseKaiten), wantKaiten) {
		return fmt.Errorf("release CLI candidate must be named %s and be available", wantKaiten)
	}
	if err := validateReleaseBinding(config); err != nil {
		return err
	}
	h.v2Version = config.V2Version
	if err := validateRunnerRuntime(config.RunnerLabel, runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	paths, err := expectedLayout(runtime.GOOS, profile)
	if err != nil {
		return err
	}
	h.paths = paths
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	token, err := syntheticToken(seed)
	if err != nil {
		return err
	}
	h.token = token
	return nil
}

func validateReleaseBinding(config Config) error {
	lowerSHA := regexp.MustCompile(`^[0-9a-f]{40}$`)
	lowerDigest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !lowerSHA.MatchString(config.Commit) || config.ReleaseHeadSHA != config.Commit {
		return errors.New("release head SHA must exactly match the tested 40-character commit")
	}
	if matched, _ := regexp.MatchString(`^[1-9][0-9]*$`, config.ReleaseRunID); !matched {
		return errors.New("release run ID must be a positive decimal identifier")
	}
	if config.ReleaseRunAttempt != "1" {
		return errors.New("release run attempt must be exactly 1 because artifact records do not attest reruns")
	}
	if matched, _ := regexp.MatchString(`^v[0-9][0-9A-Za-z.+-]*$`, config.ReleaseTag); !matched {
		return errors.New("release tag must be an explicit v-prefixed tag")
	}
	if matched, _ := regexp.MatchString(`^[0-9][0-9A-Za-z.+-]*$`, config.V2Version); !matched || config.V2Version == "native-v1" || config.V2Version == "native-v3" {
		return errors.New("shipped v2 version must be explicit and distinct from lifecycle fixtures")
	}
	if strings.TrimPrefix(config.ReleaseTag, "v") != config.V2Version {
		return errors.New("shipped binary version must exactly match the Release workflow tag")
	}
	if !lowerDigest.MatchString(config.ReleaseManifestSHA256) || !lowerDigest.MatchString(config.ReleaseArchiveSHA256) || !lowerDigest.MatchString(config.ReleaseKaitenSHA256) || !lowerDigest.MatchString(config.ReleaseKaitenMCPSHA256) {
		return errors.New("release manifest, archive, and binary identities must be lowercase SHA-256")
	}
	if config.ReleaseKaitenSHA256 == config.ReleaseKaitenMCPSHA256 {
		return errors.New("release kaiten and kaiten-mcp binaries must have distinct SHA-256 identities")
	}
	if filepath.Base(config.ReleaseArchive) != config.ReleaseArchive || (!strings.HasSuffix(config.ReleaseArchive, ".tar.gz") && !strings.HasSuffix(config.ReleaseArchive, ".zip")) {
		return errors.New("release archive must be a base-name tar.gz or zip artifact")
	}
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	wantArchive := fmt.Sprintf("kaiten_%s_%s_%s%s", config.V2Version, runtime.GOOS, runtime.GOARCH, extension)
	if config.ReleaseArchive != wantArchive {
		return fmt.Errorf("release archive = %s, want exact native full archive %s", config.ReleaseArchive, wantArchive)
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	inside := func(parent, candidate string) bool {
		relative, err := filepath.Rel(parent, candidate)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return inside(first, second) || inside(second, first)
}

type runnerRuntime struct{ goos, goarch string }

var hostedRunnerRuntimes = map[string]runnerRuntime{
	"macos-15-intel":   {goos: "darwin", goarch: "amd64"},
	"macos-latest":     {goos: "darwin", goarch: "arm64"},
	"ubuntu-latest":    {goos: "linux", goarch: "amd64"},
	"ubuntu-24.04-arm": {goos: "linux", goarch: "arm64"},
	"windows-latest":   {goos: "windows", goarch: "amd64"},
}

func validateRunnerRuntime(label, goos, goarch string) error {
	expected, ok := hostedRunnerRuntimes[label]
	if !ok {
		return fmt.Errorf("unreviewed native lifecycle runner label %q", label)
	}
	if expected.goos != goos || expected.goarch != goarch {
		return fmt.Errorf("runner %s resolved to %s/%s, want %s/%s", label, goos, goarch, expected.goos, expected.goarch)
	}
	return nil
}

func (h *harness) check(name, detail string) {
	h.evidence.Checks = append(h.evidence.Checks, Check{Name: name, Status: "passed", Detail: detail})
}

func (h *harness) run(ctx context.Context) error {
	if err := h.captureReleaseBinaries(ctx); err != nil {
		return fmt.Errorf("launch exact release binaries: %w", err)
	}
	h.check("release-binary-binding", "shipped kaiten and kaiten-mcp launched with exact release artifact identity")
	var fixtureHashes []string
	seenFixtureHashes := make(map[string]bool, 3)
	for _, fixture := range []struct {
		name string
		path string
	}{{"v1", h.config.V1}, {"v2", h.config.V2}, {"v3-no-health", h.config.V3}} {
		digest, err := fileSHA256(fixture.path)
		if err != nil {
			return err
		}
		if seenFixtureHashes[digest] {
			return errors.New("lifecycle fixture binaries must have distinct SHA-256 identities")
		}
		seenFixtureHashes[digest] = true
		fixtureHashes = append(fixtureHashes, fixture.name+"="+digest)
	}
	h.check("fixture-sha256", strings.Join(fixtureHashes, "; "))
	if err := requireNonRootLinux(); err != nil {
		return fmt.Errorf("Linux user bus preflight: %w", err)
	}
	if err := requireServicePortFree(); err != nil {
		return err
	}
	h.check("preflight-port", "127.0.0.1:8100 was free; no listener was terminated")
	if err := serviceAbsent(ctx, h.paths); err != nil {
		return err
	}
	h.check("preflight-service-identity", "fixed native service identity was absent")
	if err := prepareProfile(h.config.Profile); err != nil {
		return fmt.Errorf("prepare isolated profile: %w", err)
	}
	h.profilePrepared = true
	if err := seedClientConfigs(h.paths); err != nil {
		return fmt.Errorf("seed synthetic client configuration: %w", err)
	}
	if err := h.captureClientState("clients-before-install.json"); err != nil {
		return fmt.Errorf("capture initial client configuration: %w", err)
	}
	mock, err := startMockAPI(h.token)
	if err != nil {
		return fmt.Errorf("start loopback mock API: %w", err)
	}
	h.mock = mock
	h.check("mock-api", "new loopback-only fake Kaiten API started with a synthetic non-production bearer")
	environment := childEnvironment(h.config.Profile, mock.URL(), h.token)

	if err := h.invoke(ctx, "install-v1", h.config.V1, []string{"install"}, "\n\ny\n", environment, false); err != nil {
		return err
	}
	if err := h.captureHealth(ctx, "health-install-v1.json", "native-v1"); err != nil {
		return err
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("native service manager did not report v1 active: %w", err)
	}
	installPID, err := h.captureManagerStatus(ctx, "manager-install-v1.txt", "install-v1")
	if err != nil {
		return fmt.Errorf("capture v1 manager status: %w", err)
	}
	if err := requireFileMatches(h.paths.binary, h.config.V1); err != nil {
		return fmt.Errorf("installed v1 bytes: %w", err)
	}
	h.check("install-health", "native-v1 active at loopback health endpoint")
	if err := verifyClientConfigs(h.paths, true); err != nil {
		return err
	}
	if err := h.captureClientState("clients-registered-v1.json"); err != nil {
		return err
	}
	permissions, err := verifyPermissions(ctx, h.paths)
	if err != nil {
		return err
	}
	if err := h.writeJSONArtifact("permissions.json", permissions); err != nil {
		return err
	}
	h.check("permissions", fmt.Sprintf("%v", permissions))
	if err := h.captureMCPProof(ctx, "mcp-auth-v1.json", "install-v1", "native-v1"); err != nil {
		return err
	}
	if err := mock.AuthProof(); err != nil {
		return err
	}
	h.check("mcp-api-auth", "MCP initialized and get_current_user reached the loopback mock with the expected bearer")
	if err := scanForToken(h.config.Profile, h.token, map[string]bool{filepath.Clean(h.paths.environment): true}); err != nil {
		return err
	}
	h.check("secret-containment-before-uninstall", "synthetic bearer appeared only in the mode-restricted environment file")

	if err := restartService(ctx, h.paths); err != nil {
		return fmt.Errorf("native restart: %w", err)
	}
	if err := h.captureHealth(ctx, "health-restart-v1.json", "native-v1"); err != nil {
		return err
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("native service manager did not report restarted v1 active: %w", err)
	}
	restartPID, err := h.captureManagerStatus(ctx, "manager-restart-v1.txt", "restart-v1")
	if err != nil {
		return fmt.Errorf("capture restarted v1 manager status: %w", err)
	}
	if restartPID == installPID {
		return errors.New("native restart did not replace the managed process")
	}
	h.check("native-restart", nativeManagerName()+" restarted native-v1 and health recovered")

	if err := h.invoke(ctx, "healthy-update-v2", h.config.V2, []string{"install"}, "u\n\n\ny\n", environment, false); err != nil {
		return err
	}
	if err := h.captureHealth(ctx, "health-update-v2.json", h.v2Version); err != nil {
		return err
	}
	if err := verifyInstalledVersion(ctx, h.paths.binary, h.v2Version, h.config.Profile, environment); err != nil {
		return err
	}
	if err := requireFileMatches(h.paths.binary, h.config.V2); err != nil {
		return fmt.Errorf("installed v2 bytes: %w", err)
	}
	if _, err := h.captureManagerStatus(ctx, "manager-update-v2.txt", "update-v2"); err != nil {
		return fmt.Errorf("capture v2 manager status: %w", err)
	}
	if err := h.captureClientState("clients-registered-v2.json"); err != nil {
		return err
	}
	beforeRollback, err := ownedHashes(h.paths)
	if err != nil {
		return err
	}
	h.check("healthy-update", "installed executable and health transitioned from native-v1 to shipped "+h.v2Version)

	if err := h.invoke(ctx, "bad-service-update-v3", h.config.V3, []string{"install"}, "u\n\n\ny\n", environment, true); err != nil {
		return err
	}
	if err := h.captureHealth(ctx, "health-rollback-v2.json", h.v2Version); err != nil {
		return fmt.Errorf("rollback did not restore v2 health: %w", err)
	}
	if err := verifyInstalledVersion(ctx, h.paths.binary, h.v2Version, h.config.Profile, environment); err != nil {
		return fmt.Errorf("rollback did not restore v2 executable: %w", err)
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("rollback did not reactivate shipped %s: %w", h.v2Version, err)
	}
	if err := requireFileMatches(h.paths.binary, h.config.V2); err != nil {
		return fmt.Errorf("rollback executable bytes: %w", err)
	}
	afterRollback, err := ownedHashes(h.paths)
	if err != nil {
		return err
	}
	if err := requireHashesEqual(beforeRollback, afterRollback); err != nil {
		return err
	}
	if err := h.writeJSONArtifact("rollback-hashes.json", map[string]any{"before_failed_update": beforeRollback, "after_rollback": afterRollback}); err != nil {
		return err
	}
	if _, err := h.captureManagerStatus(ctx, "manager-rollback-v2.txt", "rollback-v2"); err != nil {
		return fmt.Errorf("capture rollback manager status: %w", err)
	}
	if err := verifyClientConfigs(h.paths, true); err != nil {
		return err
	}
	if err := h.captureClientState("clients-after-rollback.json"); err != nil {
		return err
	}
	backupPermissions, err := verifyBackupPermissions(ctx, h.paths)
	if err != nil {
		return fmt.Errorf("rollback backup permissions: %w", err)
	}
	if err := h.writeJSONArtifact("rollback-backup-permissions.json", backupPermissions); err != nil {
		return err
	}
	if err := h.captureMCPProof(ctx, "mcp-auth-rollback-v2.json", "rollback-v2", h.v2Version); err != nil {
		return fmt.Errorf("rollback MCP/API proof: %w", err)
	}
	if err := mock.AuthProof(); err != nil {
		return err
	}
	if err := h.captureServiceFiles(); err != nil {
		return fmt.Errorf("capture service definition and log: %w", err)
	}
	h.check("failed-update-rollback", "native-v3 produced no health endpoint; installer failed and restored healthy "+h.v2Version)

	if err := h.invoke(ctx, "uninstall-first", h.config.V2, []string{"uninstall"}, "y\n", environment, false); err != nil {
		return err
	}
	if err := waitServiceAbsent(ctx, h.paths, 5*time.Second); err != nil {
		return err
	}
	if err := verifyOwnedFilesRemoved(h.paths); err != nil {
		return err
	}
	if err := verifyClientConfigs(h.paths, false); err != nil {
		return err
	}
	if err := h.captureClientState("clients-after-uninstall.json"); err != nil {
		return err
	}
	if err := h.invoke(ctx, "uninstall-second", h.config.V2, []string{"uninstall"}, "y\n", environment, false); err != nil {
		return err
	}
	if err := waitServiceAbsent(ctx, h.paths, 5*time.Second); err != nil {
		return err
	}
	backupClients := h.paths
	backupClients.claudeCode += ".bak"
	backupClients.claudeDesktop += ".bak"
	if err := verifyClientConfigs(backupClients, false); err != nil {
		return fmt.Errorf("client configuration backups after idempotent uninstall: %w", err)
	}
	remaining, err := verifyOnlyExpectedFilesRemain(h.config.Profile, h.paths)
	if err != nil {
		return err
	}
	if err := scanForToken(h.config.Profile, h.token, nil); err != nil {
		return err
	}
	if err := h.writeJSONArtifact("remaining-files.json", remaining); err != nil {
		return err
	}
	if err := h.captureFinalManagerStatus(ctx); err != nil {
		return err
	}
	for _, captured := range h.captures {
		if strings.Contains(captured.stdout, h.token) || strings.Contains(captured.stderr, h.token) {
			return fmt.Errorf("%s output exposed the synthetic token", captured.label)
		}
	}
	h.check("double-uninstall", "both uninstall invocations succeeded and the native service identity is absent")
	h.check("final-owned-file-and-secret-scan", "only preserved log and unrelated client configs remain: "+strings.Join(remaining, ", "))
	return nil
}

func (h *harness) invoke(parent context.Context, label, executable string, arguments []string, input string, environment []string, expectFailure bool) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	captured, err := runCaptured(ctx, executable, arguments, input, h.config.Profile, environment)
	captured.label = label
	h.captures = append(h.captures, captured)
	redacted, redactErr := redact(strings.TrimSpace(captured.stdout+"\n"+captured.stderr), h.token, map[string]string{h.config.Profile: "$PROFILE", h.mock.URL(): "$MOCK_API"})
	if redactErr != nil {
		return fmt.Errorf("%s: %w", label, redactErr)
	}
	if expectFailure {
		if err == nil {
			return fmt.Errorf("%s unexpectedly succeeded", label)
		}
		wantFailure := `error: installation failed: verify installed service version "native-v3": service did not become healthy before the readiness deadline`
		if captured.exitCode != 1 || strings.TrimSpace(captured.stderr) != wantFailure {
			return fmt.Errorf("%s did not fail through the intended v3 health path", label)
		}
		h.check(label, "expected exit was nonzero; redacted output: "+redacted)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	h.check(label, "exit 0; redacted output: "+redacted)
	return nil
}

func verifyInstalledVersion(ctx context.Context, binary, version, directory string, environment []string) error {
	captured, err := runCaptured(ctx, binary, []string{"version"}, "", directory, environment)
	if err != nil {
		return err
	}
	if strings.TrimSpace(captured.stdout) != "kaiten-mcp "+version || captured.stderr != "" {
		return fmt.Errorf("installed version response = %q", strings.TrimSpace(captured.stdout))
	}
	return nil
}

func waitServiceAbsent(ctx context.Context, paths layout, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if serviceAbsent(ctx, paths) == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("native service identity remains after uninstall")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func nativeManagerName() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchctl"
	case "linux":
		return "systemd --user over DBus"
	case "windows":
		return "Windows Startup"
	default:
		return "native manager"
	}
}

func nativeManagerIdentity() string {
	switch runtime.GOOS {
	case "darwin":
		return serviceLabel
	case "linux":
		return serviceUnit
	case "windows":
		return "kaiten-mcp.cmd"
	default:
		return "kaiten-mcp"
	}
}

func (h *harness) cleanup() (postCleanupEvidence, error) {
	var proof postCleanupEvidence
	var closeErr error
	if h.mock != nil {
		if err := h.mock.Close(); err != nil {
			closeErr = fmt.Errorf("stop loopback mock: %w", err)
		}
	}
	if !h.profilePrepared {
		return proof, closeErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := stopNativeService(ctx, h.paths); err != nil {
		return proof, errors.Join(closeErr, fmt.Errorf("stop native service before profile cleanup: %w", err))
	}
	if err := nativeProcessAbsent(ctx, h.paths.binary); err != nil {
		return proof, errors.Join(closeErr, fmt.Errorf("native process remains before profile cleanup: %w", err))
	}
	if err := requireServicePortFree(); err != nil {
		return proof, errors.Join(closeErr, fmt.Errorf("native port remains occupied before profile cleanup: %w", err))
	}
	proof.ServiceAbsent = true
	proof.ProcessAbsent = true
	proof.Port8100Free = true
	if err := os.RemoveAll(h.config.Profile); err != nil {
		return proof, errors.Join(closeErr, fmt.Errorf("remove isolated native lifecycle profile: %w", err))
	}
	proof.ProfileAbsent = !fileExists(h.config.Profile)
	if !proof.ProfileAbsent {
		return proof, errors.Join(closeErr, errors.New("isolated native lifecycle profile remains"))
	}
	if err := serviceAbsent(ctx, h.paths); err != nil {
		proof.ServiceAbsent = false
		proof.ProcessAbsent = false
		return proof, errors.Join(closeErr, fmt.Errorf("native identity remains after profile cleanup: %w", err))
	}
	if err := nativeProcessAbsent(ctx, h.paths.binary); err != nil {
		proof.ProcessAbsent = false
		return proof, errors.Join(closeErr, fmt.Errorf("native process remains after profile cleanup: %w", err))
	}
	if err := requireServicePortFree(); err != nil {
		proof.Port8100Free = false
		return proof, errors.Join(closeErr, fmt.Errorf("native port remains occupied after profile cleanup: %w", err))
	}
	return proof, closeErr
}
