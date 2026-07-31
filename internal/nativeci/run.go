package nativeci

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config identifies three newly built lifecycle fixtures and disposable output
// locations. V1 and V2 are real product binaries; V3 is the repository's
// intentional no-health fixture used to exercise transactional rollback.
type Config struct {
	V1, V2, V3           string
	Profile, EvidenceDir string
	RunnerLabel, Commit  string
}

type harness struct {
	config   Config
	paths    layout
	evidence Evidence
	token    string
	mock     *mockAPI
	client   *http.Client
	captures []capture
}

// Run executes one real native service-manager lifecycle.
func Run(ctx context.Context, config Config) (runErr error) {
	h, err := newHarness(config)
	if err != nil {
		return err
	}
	defer func() {
		cleanupErr := h.cleanup()
		runErr = errors.Join(runErr, cleanupErr)
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
	return h.run(ctx)
}

func newHarness(config Config) (*harness, error) {
	for name, path := range map[string]string{"v1": config.V1, "v2": config.V2, "v3": config.V3, "profile": config.Profile, "evidence": config.EvidenceDir} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("%s path must be absolute", name)
		}
	}
	profile := filepath.Clean(config.Profile)
	if !strings.Contains(strings.ToLower(filepath.Base(profile)), "native-lifecycle") || profile == filepath.VolumeName(profile)+string(filepath.Separator) {
		return nil, errors.New("refusing a non-disposable native lifecycle profile path")
	}
	if info, err := os.Stat(profile); err == nil {
		if !info.IsDir() {
			return nil, errors.New("native lifecycle profile path is not a directory")
		}
		entries, readErr := os.ReadDir(profile)
		if readErr != nil || len(entries) != 0 {
			return nil, errors.New("existing native lifecycle profile must be empty")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, candidate := range []string{config.V1, config.V2, config.V3} {
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			return nil, fmt.Errorf("lifecycle candidate is unavailable: %s", filepath.Base(candidate))
		}
	}
	paths, err := expectedLayout(runtime.GOOS, profile)
	if err != nil {
		return nil, err
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	token, err := syntheticToken(seed)
	if err != nil {
		return nil, err
	}
	return &harness{config: config, paths: paths, evidence: newEvidence(config.RunnerLabel, config.Commit, time.Now()), token: token, client: &http.Client{Timeout: 2 * time.Second}}, nil
}

func (h *harness) check(name, detail string) {
	h.evidence.Checks = append(h.evidence.Checks, Check{Name: name, Status: "passed", Detail: detail})
}

func (h *harness) run(ctx context.Context) error {
	var fixtureHashes []string
	for _, fixture := range []struct {
		name string
		path string
	}{{"v1", h.config.V1}, {"v2", h.config.V2}, {"v3-no-health", h.config.V3}} {
		digest, err := fileSHA256(fixture.path)
		if err != nil {
			return err
		}
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
	if err := seedClientConfigs(h.paths); err != nil {
		return fmt.Errorf("seed synthetic client configuration: %w", err)
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
	if err := waitForHealth(ctx, h.client, "http://127.0.0.1:8100/health", "native-v1", 10*time.Second); err != nil {
		return err
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("native service manager did not report v1 active: %w", err)
	}
	h.check("install-health", "native-v1 active at loopback health endpoint")
	if err := verifyClientConfigs(h.paths, true); err != nil {
		return err
	}
	permissions, err := verifyPermissions(ctx, h.paths)
	if err != nil {
		return err
	}
	h.check("permissions", strings.Join(permissions, "; "))
	if err := proveMCP(ctx, h.client, "http://127.0.0.1:8100/mcp", "native-v1"); err != nil {
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
	if err := waitForHealth(ctx, h.client, "http://127.0.0.1:8100/health", "native-v1", 10*time.Second); err != nil {
		return err
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("native service manager did not report restarted v1 active: %w", err)
	}
	h.check("native-restart", nativeManagerName()+" restarted native-v1 and health recovered")

	if err := h.invoke(ctx, "healthy-update-v2", h.config.V2, []string{"install"}, "u\n\n\ny\n", environment, false); err != nil {
		return err
	}
	if err := waitForHealth(ctx, h.client, "http://127.0.0.1:8100/health", "native-v2", 10*time.Second); err != nil {
		return err
	}
	if err := verifyInstalledVersion(ctx, h.paths.binary, "native-v2", h.config.Profile, environment); err != nil {
		return err
	}
	h.check("healthy-update", "installed executable and health transitioned from native-v1 to native-v2")

	if err := h.invoke(ctx, "bad-service-update-v3", h.config.V3, []string{"install"}, "u\n\n\ny\n", environment, true); err != nil {
		return err
	}
	if err := waitForHealth(ctx, h.client, "http://127.0.0.1:8100/health", "native-v2", 10*time.Second); err != nil {
		return fmt.Errorf("rollback did not restore v2 health: %w", err)
	}
	if err := verifyInstalledVersion(ctx, h.paths.binary, "native-v2", h.config.Profile, environment); err != nil {
		return fmt.Errorf("rollback did not restore v2 executable: %w", err)
	}
	if err := serviceActive(ctx, h.paths); err != nil {
		return fmt.Errorf("rollback did not reactivate native-v2: %w", err)
	}
	if err := verifyClientConfigs(h.paths, true); err != nil {
		return err
	}
	if err := verifyBackupPermissions(ctx, h.paths); err != nil {
		return fmt.Errorf("rollback backup permissions: %w", err)
	}
	h.check("failed-update-rollback", "native-v3 produced no health endpoint; installer failed and restored healthy native-v2")

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
		if captured.exitCode == 0 || !strings.Contains(captured.stderr, "service did not become healthy") || !strings.Contains(captured.stderr, "native-v3") {
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

func (h *harness) cleanup() error {
	if h.mock != nil {
		_ = h.mock.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopErr := stopNativeService(ctx, h.paths)
	removeErr := os.RemoveAll(h.config.Profile)
	if removeErr == nil && fileExists(h.config.Profile) {
		removeErr = errors.New("isolated native lifecycle profile remains")
	}
	return errors.Join(stopErr, removeErr)
}
