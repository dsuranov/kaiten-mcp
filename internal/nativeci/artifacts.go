package nativeci

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type healthArtifact struct {
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
}

var requiredLifecycleArtifacts = []string{
	"clients-after-rollback.json",
	"clients-after-uninstall.json",
	"clients-before-install.json",
	"clients-registered-v1.json",
	"clients-registered-v2.json",
	"commands.json",
	"health-install-v1.json",
	"health-restart-v1.json",
	"health-rollback-v2.json",
	"health-update-v2.json",
	"manager-final.txt",
	"manager-install-v1.txt",
	"manager-restart-v1.txt",
	"manager-rollback-v2.txt",
	"manager-update-v2.txt",
	"mcp-auth-rollback-v2.json",
	"mcp-auth-v1.json",
	"permissions.json",
	"release-binaries.json",
	"remaining-files.json",
	"rollback-backup-permissions.json",
	"rollback-hashes.json",
	"service-definition.txt",
	"service-log.txt",
	"manager-states.json",
	"post-harness-cleanup.json",
}

// RequiredEvidenceArtifacts returns the reviewed companion-file contract used
// by the post-run five-runner evidence verifier.
func RequiredEvidenceArtifacts() []string {
	return append([]string(nil), requiredLifecycleArtifacts...)
}

func (h *harness) writeJSONArtifact(name string, value any) error {
	if err := validateArtifactName(name); err != nil {
		return err
	}
	if err := writeJSONArtifact(filepath.Join(h.config.EvidenceDir, name), value, h.token); err != nil {
		return err
	}
	h.recordArtifact(name)
	return nil
}

func (h *harness) writeTextArtifact(name, value string) error {
	if err := validateArtifactName(name); err != nil {
		return err
	}
	value = strings.ReplaceAll(value, h.config.Profile, "$PROFILE")
	if h.mock != nil {
		value = strings.ReplaceAll(value, h.mock.URL(), "$MOCK_API")
	}
	if err := writeTextArtifact(filepath.Join(h.config.EvidenceDir, name), value, h.token); err != nil {
		return err
	}
	h.recordArtifact(name)
	return nil
}

func validateArtifactName(name string) error {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("native lifecycle evidence name must be a base name")
	}
	return nil
}

func (h *harness) recordArtifact(name string) {
	for _, existing := range h.evidence.Artifacts {
		if existing == name {
			return
		}
	}
	h.evidence.Artifacts = append(h.evidence.Artifacts, name)
	sort.Strings(h.evidence.Artifacts)
}

func (h *harness) captureHealth(ctx context.Context, name, expectedVersion string) error {
	const endpoint = "http://127.0.0.1:8100/health"
	return h.captureHealthAt(ctx, name, endpoint, expectedVersion)
}

func (h *harness) captureHealthAt(ctx context.Context, name, endpoint, expectedVersion string) error {
	if err := waitForHealth(ctx, h.client, endpoint, expectedVersion, 10*time.Second); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := h.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var artifact healthArtifact
	if err := json.NewDecoder(response.Body).Decode(&artifact); err != nil {
		return err
	}
	artifact.Endpoint = endpoint
	if response.StatusCode != http.StatusOK || artifact.Status != "ok" || artifact.Version != expectedVersion || artifact.Runtime != runtime.Version() || artifact.Runtime != "go1.26.5" {
		return fmt.Errorf("captured health identity does not match %s", expectedVersion)
	}
	return h.writeJSONArtifact(name, artifact)
}

func (h *harness) captureManagerStatus(ctx context.Context, name, stage string) (int, error) {
	status, err := managerStatus(ctx, h.paths)
	if err != nil {
		return 0, err
	}
	pid, err := serviceProcessID(ctx, h.paths)
	if err != nil {
		return 0, err
	}
	if err := h.writeTextArtifact(name, status); err != nil {
		return 0, err
	}
	h.managers = append(h.managers, managerEvidence{Stage: stage, Manager: nativeManagerName(), Identity: nativeManagerIdentity(), Active: true, PID: pid})
	return pid, nil
}

func (h *harness) captureFinalManagerStatus(ctx context.Context) error {
	if err := serviceAbsent(ctx, h.paths); err != nil {
		return err
	}
	if err := nativeProcessAbsent(ctx, h.paths.binary); err != nil {
		return err
	}
	if err := requireServicePortFree(); err != nil {
		return err
	}
	status := fmt.Sprintf("manager=%s\nidentity=%s\nstate=absent\nexecutable=%s\nprocess=absent\nport_8100=free", nativeManagerName(), nativeManagerIdentity(), h.paths.binary)
	if err := h.writeTextArtifact("manager-final.txt", status); err != nil {
		return err
	}
	h.managers = append(h.managers, managerEvidence{Stage: "final", Manager: nativeManagerName(), Identity: nativeManagerIdentity(), Active: false, PID: 0})
	return h.writeJSONArtifact("manager-states.json", h.managers)
}

func (h *harness) captureReleaseBinaries(ctx context.Context) error {
	type candidate struct {
		name, path, prefix string
	}
	candidates := []candidate{
		{name: "kaiten", path: h.config.ReleaseKaiten, prefix: "kaiten "},
		{name: "kaiten-mcp", path: h.config.V2, prefix: "kaiten-mcp "},
	}
	proof := architectureSmokeEvidence{
		Schema: "kaiten-native-release-binaries/v1", ReleaseRunID: h.config.ReleaseRunID,
		ReleaseTag: h.config.ReleaseTag, ReleaseHeadSHA: h.config.ReleaseHeadSHA,
		ReleaseManifestSHA256: h.config.ReleaseManifestSHA256, ReleaseArchive: h.config.ReleaseArchive,
		ReleaseArchiveSHA256: h.config.ReleaseArchiveSHA256, ReleaseArtifactName: "release-assets",
	}
	for _, candidate := range candidates {
		captured, err := runCaptured(ctx, candidate.path, []string{"version"}, "", filepath.Dir(candidate.path), os.Environ())
		if err != nil {
			return fmt.Errorf("launch shipped %s: %w", candidate.name, err)
		}
		wantOutput := candidate.prefix + h.config.V2Version
		if captured.exitCode != 0 || strings.TrimSpace(captured.stdout) != wantOutput || captured.stderr != "" {
			return fmt.Errorf("shipped %s version output = %q, want %q", candidate.name, strings.TrimSpace(captured.stdout), wantOutput)
		}
		info, err := buildinfo.ReadFile(candidate.path)
		if err != nil {
			return fmt.Errorf("read shipped %s build identity: %w", candidate.name, err)
		}
		goos, goarch := "", ""
		for _, setting := range info.Settings {
			switch setting.Key {
			case "GOOS":
				goos = setting.Value
			case "GOARCH":
				goarch = setting.Value
			}
		}
		if info.GoVersion != "go1.26.5" || goos != runtime.GOOS || goarch != runtime.GOARCH {
			return fmt.Errorf("shipped %s build identity = %s %s/%s, want go1.26.5 %s/%s", candidate.name, info.GoVersion, goos, goarch, runtime.GOOS, runtime.GOARCH)
		}
		digest, err := fileSHA256(candidate.path)
		if err != nil {
			return err
		}
		proof.Binaries = append(proof.Binaries, binarySmokeEvidence{
			Name: candidate.name, SHA256: digest, VersionOutput: wantOutput, GoVersion: info.GoVersion,
			GOOS: goos, GOARCH: goarch, Launched: true, ExitCode: captured.exitCode,
		})
	}
	return h.writeJSONArtifact("release-binaries.json", proof)
}

func (h *harness) captureMCPProof(ctx context.Context, artifact, stage, expectedVersion string) error {
	const endpoint = "http://127.0.0.1:8100/mcp"
	before := h.mock.Snapshot()
	var toolNames []string
	if err := proveMCP(ctx, h.client, endpoint, expectedVersion, &toolNames); err != nil {
		return err
	}
	after := h.mock.Snapshot()
	if after.authorized != before.authorized+1 || after.unauthorized != before.unauthorized || after.method != http.MethodGet || after.path != "/api/v1/users/current" || !after.authHeaderValid {
		return errors.New("loopback mock did not retain the exact authenticated representative read sentinel")
	}
	proof := mcpEvidence{
		Stage: stage, Endpoint: endpoint, ServerVersion: expectedVersion, ProtocolVersion: "2025-06-18",
		SessionEstablished: true, ToolNames: toolNames, ReadOnlyToolCount: len(toolNames), WriteToolCount: 0,
		RepresentativeTool: "get_current_user", RepresentativeReadSucceeded: true,
		AuthorizedRequestCount: after.authorized, UnauthorizedRequestCount: after.unauthorized,
		MockMethod: after.method, MockPath: after.path, AuthHeaderValid: after.authHeaderValid,
	}
	return h.writeJSONArtifact(artifact, proof)
}

func (h *harness) captureClientState(name string) error {
	state := make(map[string]any, 2)
	for role, path := range map[string]string{"claude_code": h.paths.claudeCode, "claude_desktop": h.paths.claudeDesktop} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		state[role] = value
	}
	return h.writeJSONArtifact(name, state)
}

func (h *harness) captureServiceFiles() error {
	definition, err := os.ReadFile(h.paths.definition)
	if err != nil {
		return err
	}
	if err := h.writeTextArtifact("service-definition.txt", string(definition)); err != nil {
		return err
	}
	log, err := os.ReadFile(h.paths.log)
	if err != nil {
		return err
	}
	return h.writeTextArtifact("service-log.txt", string(log))
}

func (h *harness) writeCommandArtifacts() error {
	replacements := map[string]string{h.config.Profile: "$PROFILE"}
	if h.mock != nil {
		replacements[h.mock.URL()] = "$MOCK_API"
	}
	const name = "commands.json"
	if err := writeCommandEvidence(filepath.Join(h.config.EvidenceDir, name), h.captures, h.token, replacements); err != nil {
		return err
	}
	h.recordArtifact(name)
	return nil
}

func (h *harness) verifyArtifactSet() error {
	want := append([]string(nil), requiredLifecycleArtifacts...)
	sort.Strings(want)
	got := append([]string(nil), h.evidence.Artifacts...)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Errorf("native lifecycle artifact inventory = %v, want %v", got, want)
	}
	for _, name := range want {
		data, err := os.ReadFile(filepath.Join(h.config.EvidenceDir, name))
		if err != nil {
			return err
		}
		if err := validateEvidencePayload(data, h.token); err != nil {
			return fmt.Errorf("unsafe evidence artifact %s: %w", name, err)
		}
	}
	return nil
}

func ownedHashes(paths layout) (map[string]string, error) {
	result := make(map[string]string, 3)
	for role, path := range map[string]string{"binary": paths.binary, "environment": paths.environment, "service_definition": paths.definition} {
		digest, err := fileSHA256(path)
		if err != nil {
			return nil, err
		}
		result[role] = digest
	}
	return result, nil
}

func requireHashesEqual(want, got map[string]string) error {
	for _, role := range []string{"binary", "environment", "service_definition"} {
		if want[role] == "" || want[role] != got[role] {
			return fmt.Errorf("rollback did not restore exact %s bytes", role)
		}
	}
	return nil
}

func requireFileMatches(installed, candidate string) error {
	installedHash, err := fileSHA256(installed)
	if err != nil {
		return err
	}
	candidateHash, err := fileSHA256(candidate)
	if err != nil {
		return err
	}
	if installedHash != candidateHash {
		return errors.New("installed executable does not match the selected native fixture")
	}
	return nil
}
