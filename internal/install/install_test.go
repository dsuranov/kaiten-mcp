package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCommands struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeCommands) Run(_ context.Context, name string, arguments ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(append([]string{name}, arguments...), " "))
	return f.err
}

type fakeHealth struct {
	err      error
	endpoint string
}

func (f *fakeHealth) Wait(_ context.Context, endpoint string, _ time.Duration) error {
	f.endpoint = endpoint
	return f.err
}

func newTestEngine(t *testing.T, goos string, health healthChecker) *Engine {
	t.Helper()
	home := t.TempDir()
	executable := filepath.Join(t.TempDir(), binaryName(goos))
	if err := os.WriteFile(executable, []byte("new executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Engine{
		GOOS: goos, Home: home, Executable: executable, UID: "501", Environment: map[string]string{},
		Commands: &fakeCommands{}, Health: health, ReadinessTimeout: 10 * time.Millisecond,
	}
}

func TestPlatformLayoutsAndDefinitionsArePerUserAndLoopback(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := filepath.Join(string(filepath.Separator), "users", "sample")
			layout, err := layoutFor(goos, home)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{layout.Root, layout.Binary, layout.Environment, layout.Log, layout.ServiceDefinition} {
				if !strings.HasPrefix(path, home) {
					t.Fatalf("path escaped user profile: %s", path)
				}
			}
			definition := string(serviceDefinition(layout))
			if !strings.Contains(definition, "127.0.0.1") || !strings.Contains(definition, "8100") || !strings.Contains(definition, "/mcp") || strings.Contains(definition, "API_TOKEN") {
				t.Fatalf("unsafe service definition: %s", definition)
			}
		})
	}
}

func TestInstallCycleInTemporaryProfiles(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			health := &fakeHealth{}
			engine := newTestEngine(t, goos, health)
			input := strings.NewReader("https://tenant.example\ntemporary-token\nn\nn\n")
			var output, diagnostics strings.Builder
			if err := engine.Install(context.Background(), input, &output, &diagnostics); err != nil {
				t.Fatal(err)
			}
			layout, _ := layoutFor(goos, engine.Home)
			binary, err := os.ReadFile(layout.Binary)
			if err != nil || string(binary) != "new executable bytes" {
				t.Fatalf("binary mismatch: %q err=%v", binary, err)
			}
			environment, err := os.ReadFile(layout.Environment)
			if err != nil || !strings.Contains(string(environment), "KAITEN_API_TOKEN=temporary-token") || !strings.Contains(string(environment), "KAITEN_ENABLE_WRITE_TOOLS=false") {
				t.Fatalf("environment mismatch: %q err=%v", environment, err)
			}
			info, _ := os.Stat(layout.Environment)
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("secret mode is %o", info.Mode().Perm())
			}
			definition, _ := os.ReadFile(layout.ServiceDefinition)
			if strings.Contains(string(definition), "temporary-token") {
				t.Fatal("service definition contains token")
			}
			if health.endpoint != "http://127.0.0.1:8100/health" || !strings.Contains(output.String(), "http://127.0.0.1:8100/mcp") || diagnostics.Len() != 0 {
				t.Fatalf("unexpected readiness/output: endpoint=%q out=%q err=%q", health.endpoint, output.String(), diagnostics.String())
			}
		})
	}
}

func TestFailedUpdateRestoresPreviousInstallation(t *testing.T) {
	health := &fakeHealth{err: errors.New("not ready")}
	engine := newTestEngine(t, "linux", health)
	layout, _ := layoutFor("linux", engine.Home)
	oldFiles := map[string]string{
		layout.Binary: "old binary", layout.Environment: "KAITEN_URL=https://old.example\nKAITEN_API_TOKEN=old-token\n", layout.ServiceDefinition: "old service",
	}
	for path, content := range oldFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine.Environment["KAITEN_API_TOKEN"] = "new-token"
	input := strings.NewReader("u\nhttps://new.example\nn\nn\n")
	if err := engine.Install(context.Background(), input, io.Discard, io.Discard); err == nil {
		t.Fatal("expected readiness failure")
	}
	for path, expected := range oldFiles {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Fatalf("%s was not restored: %q err=%v", path, actual, err)
		}
		if _, err := os.Stat(path + ".bak"); err != nil {
			t.Fatalf("recoverable backup missing for %s: %v", path, err)
		}
	}
}

func TestClientConfigMergePreservesUnrelatedSettingsAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	original := `{"theme":"dark","mcpServers":{"other":{"command":"example"}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeClientConfig(path, "http://127.0.0.1:8100/mcp", false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcpServers"].(map[string]any)
	if root["theme"] != "dark" || servers["other"] == nil || servers["kaiten"] == nil {
		t.Fatalf("unrelated settings changed: %#v", root)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("client config mode is %o", info.Mode().Perm())
	}
}

func TestUninstallIsScopedIdempotentAndPreservesLogs(t *testing.T) {
	engine := newTestEngine(t, "linux", &fakeHealth{})
	layout, _ := layoutFor("linux", engine.Home)
	for _, path := range []string{layout.Binary, layout.Environment, layout.ServiceDefinition, layout.Log} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clientConfig := map[string]any{"unrelated": true, "mcpServers": map[string]any{"other": map[string]any{"url": "http://other"}, "kaiten": map[string]any{"url": "http://127.0.0.1:8100/mcp"}}}
	encoded, _ := json.Marshal(clientConfig)
	if err := os.WriteFile(layout.ClaudeCodeConfig, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := engine.Uninstall(context.Background(), strings.NewReader("y\n"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.Binary, layout.Environment, layout.ServiceDefinition} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned file remains: %s", path)
		}
	}
	if _, err := os.Stat(layout.Log); err != nil || !strings.Contains(output.String(), filepath.Dir(layout.Log)) {
		t.Fatalf("log not preserved or reported: err=%v out=%q", err, output.String())
	}
	data, _ := os.ReadFile(layout.ClaudeCodeConfig)
	var root map[string]any
	_ = json.Unmarshal(data, &root)
	servers := root["mcpServers"].(map[string]any)
	if root["unrelated"] != true || servers["other"] == nil || servers["kaiten"] != nil {
		t.Fatalf("client settings not scoped: %#v", root)
	}
	if err := engine.Uninstall(context.Background(), strings.NewReader("n\n"), io.Discard, io.Discard); err != nil {
		t.Fatalf("idempotent uninstall failed: %v", err)
	}
}

func TestInvalidTenantStopsBeforeWritingOrActivation(t *testing.T) {
	commands := &fakeCommands{}
	engine := newTestEngine(t, "darwin", &fakeHealth{})
	engine.Commands = commands
	err := engine.Install(context.Background(), strings.NewReader("https://user:pass@example.test?q=1\n"), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected tenant validation error")
	}
	layout, _ := layoutFor("darwin", engine.Home)
	if fileExists(layout.Binary) || len(commands.calls) != 0 {
		t.Fatalf("invalid input changed state: binary=%v calls=%v", fileExists(layout.Binary), commands.calls)
	}
}

func TestEnvironmentFileNeverEntersServiceDefinition(t *testing.T) {
	layout, _ := layoutFor("darwin", filepath.Join(string(filepath.Separator), "tmp", "sample"))
	secret := "fixture-secret-value"
	if strings.Contains(string(serviceDefinition(layout)), secret) {
		t.Fatal("unexpected secret")
	}
	if !strings.Contains(string(environmentFile("https://tenant.example", secret, true)), secret) {
		t.Fatal("environment fixture missing")
	}
	_ = fmt.Sprintf("%s", layout.Root)
}

func TestWindowsInstalledExecutableSchedulesScopedSelfRemoval(t *testing.T) {
	home := t.TempDir()
	layout, _ := layoutFor("windows", home)
	for _, path := range []string{layout.Binary, layout.Environment, layout.ServiceDefinition} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commands := &fakeCommands{}
	engine := &Engine{GOOS: "windows", Home: home, Executable: layout.Binary, Commands: commands, Health: &fakeHealth{}}
	if err := engine.Uninstall(context.Background(), strings.NewReader("n\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands.calls, "\n")
	if !strings.Contains(joined, "ProcessId -ne") || !strings.Contains(joined, "uninstall-cleanup.cmd") {
		t.Fatalf("self-removal was not scoped and scheduled: %s", joined)
	}
	cleanup := filepath.Join(filepath.Dir(layout.Log), "uninstall-cleanup.cmd")
	contents, err := os.ReadFile(cleanup)
	if err != nil || !strings.Contains(string(contents), layout.Binary) {
		t.Fatalf("cleanup script missing: %q err=%v", contents, err)
	}
}
