package nativeci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectedLayoutsStayInsideDisposableProfile(t *testing.T) {
	profile := filepath.Join(string(filepath.Separator), "profiles", "native-lifecycle-test")
	for _, goos := range []string{"darwin", "linux", "windows"} {
		paths, err := expectedLayout(goos, profile)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{paths.root, paths.binary, paths.environment, paths.log, paths.definition, paths.claudeCode, paths.claudeDesktop} {
			relative, err := filepath.Rel(profile, path)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				t.Fatalf("%s path escaped profile: %s", goos, path)
			}
		}
	}
}

func TestHostedRunnerRuntimeContractIsExact(t *testing.T) {
	tests := []struct{ label, goos, goarch string }{
		{"macos-15-intel", "darwin", "amd64"},
		{"macos-latest", "darwin", "arm64"},
		{"ubuntu-latest", "linux", "amd64"},
		{"ubuntu-24.04-arm", "linux", "arm64"},
		{"windows-latest", "windows", "amd64"},
	}
	for _, test := range tests {
		if err := validateRunnerRuntime(test.label, test.goos, test.goarch); err != nil {
			t.Fatalf("reviewed runner rejected: %v", err)
		}
		if err := validateRunnerRuntime(test.label, test.goos, "unexpected"); err == nil {
			t.Fatalf("runner drift was accepted for %s", test.label)
		}
	}
	if err := validateRunnerRuntime("self-hosted", "linux", "amd64"); err == nil {
		t.Fatal("unreviewed runner label was accepted")
	}
	root := t.TempDir()
	profile := filepath.Join(root, "native-lifecycle-profile")
	if !pathsOverlap(profile, filepath.Join(profile, "evidence")) || pathsOverlap(profile, filepath.Join(root, "native-evidence")) {
		t.Fatal("profile/evidence overlap guard is incorrect")
	}
}

func TestClientFixturePreservesUnrelatedKeysAcrossRegistrationRemoval(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "native-lifecycle-profile")
	paths, err := expectedLayout("linux", profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedClientConfigs(paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.claudeCode, paths.claudeDesktop} {
		data, _ := os.ReadFile(path)
		var root map[string]any
		_ = json.Unmarshal(data, &root)
		servers := root["mcpServers"].(map[string]any)
		servers["kaiten"] = map[string]any{"type": "http", "url": "http://127.0.0.1:8100/mcp"}
		encoded, _ := json.Marshal(root)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyClientConfigs(paths, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.claudeCode, paths.claudeDesktop} {
		data, _ := os.ReadFile(path)
		var root map[string]any
		_ = json.Unmarshal(data, &root)
		delete(root["mcpServers"].(map[string]any), "kaiten")
		encoded, _ := json.Marshal(root)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyClientConfigs(paths, false); err != nil {
		t.Fatal(err)
	}
}

func TestTokenScanAllowsOnlyExplicitSecretFile(t *testing.T) {
	root := t.TempDir()
	token := "generated-synthetic-marker"
	secretPath := filepath.Join(root, ".env")
	if err := os.WriteFile(secretPath, []byte("KAITEN_API_TOKEN="+token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service.log"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanForToken(root, token, map[string]bool{secretPath: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service.log"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scanForToken(root, token, map[string]bool{secretPath: true}); err == nil {
		t.Fatal("token escape was not detected")
	}
}
