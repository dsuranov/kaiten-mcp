package nativeci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	"remaining-files.json",
	"rollback-backup-permissions.json",
	"rollback-hashes.json",
	"service-definition.txt",
	"service-log.txt",
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
	if response.StatusCode != http.StatusOK || artifact.Status != "ok" || artifact.Version != expectedVersion || artifact.Runtime == "" {
		return fmt.Errorf("captured health identity does not match %s", expectedVersion)
	}
	return h.writeJSONArtifact(name, artifact)
}

func (h *harness) captureManagerStatus(ctx context.Context, name string) error {
	status, err := managerStatus(ctx, h.paths)
	if err != nil {
		return err
	}
	return h.writeTextArtifact(name, status)
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
