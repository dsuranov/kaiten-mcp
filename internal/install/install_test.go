package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	versions []string
	wait     func(string) error
}

func (f *fakeHealth) Wait(_ context.Context, endpoint, expectedVersion string, _ time.Duration) error {
	f.endpoint = endpoint
	f.versions = append(f.versions, expectedVersion)
	if f.wait != nil {
		return f.wait(expectedVersion)
	}
	return f.err
}

type fakeVersions struct {
	value  string
	values map[string]string
	err    error
}

func (f fakeVersions) ReadVersion(_ context.Context, path string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if value := f.values[path]; value != "" {
		return value, nil
	}
	return f.value, nil
}

type scriptedCommands struct {
	mu    sync.Mutex
	calls []string
	run   func(int, string, []string) error
}

func (s *scriptedCommands) Run(_ context.Context, name string, arguments ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := len(s.calls)
	s.calls = append(s.calls, strings.Join(append([]string{name}, arguments...), " "))
	if s.run != nil {
		return s.run(index, name, arguments)
	}
	return nil
}

type descriptorReader struct {
	*strings.Reader
	fd uintptr
}

func (r descriptorReader) Fd() uintptr { return r.fd }

type fakeTerminal struct {
	terminal bool
	secret   []byte
	readFD   int
	reads    int
}

func (f *fakeTerminal) IsTerminal(int) bool { return f.terminal }

func (f *fakeTerminal) ReadPassword(fd int) ([]byte, error) {
	f.readFD = fd
	f.reads++
	return append([]byte(nil), f.secret...), nil
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
		Commands: &fakeCommands{}, Health: health, Versions: fakeVersions{value: "test-version"}, ReadinessTimeout: 10 * time.Millisecond,
	}
}

func TestPlatformLayoutsAndDefinitionsArePerUserAndLoopback(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
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
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
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

func TestInteractiveTokenUsesHiddenTerminalRead(t *testing.T) {
	source := &descriptorReader{Reader: strings.NewReader(""), fd: 77}
	terminal := &fakeTerminal{terminal: true, secret: []byte("hidden-token")}
	engine := &Engine{Terminal: terminal}
	var output strings.Builder
	token, err := engine.readToken(source, bufio.NewReader(source), &output)
	if err != nil {
		t.Fatal(err)
	}
	if token != "hidden-token" || terminal.reads != 1 || terminal.readFD != 77 {
		t.Fatalf("hidden read was not used: token=%q reads=%d fd=%d", token, terminal.reads, terminal.readFD)
	}
	if strings.Contains(output.String(), token) || !strings.Contains(output.String(), "input hidden") {
		t.Fatalf("secret was echoed or hidden prompt omitted: %q", output.String())
	}
}

func TestNonTerminalTokenInputIsDeterministic(t *testing.T) {
	for attempt := 0; attempt < 2; attempt++ {
		source := &descriptorReader{Reader: strings.NewReader("automation-token\n"), fd: 81}
		terminal := &fakeTerminal{terminal: false, secret: []byte("must-not-be-used")}
		engine := &Engine{Terminal: terminal}
		var output strings.Builder
		token, err := engine.readToken(source, bufio.NewReader(source), &output)
		if err != nil || token != "automation-token" || terminal.reads != 0 || output.String() != "Kaiten API token: " {
			t.Fatalf("attempt %d: token=%q reads=%d output=%q err=%v", attempt, token, terminal.reads, output.String(), err)
		}
	}
}

func TestHTTPReadinessWaitsForIntendedVersion(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		servedVersion := "old-version"
		if calls > 1 {
			servedVersion = "new-version"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": servedVersion})
	}))
	defer server.Close()
	checker := httpReadiness{client: server.Client()}
	if err := checker.Wait(context.Background(), server.URL, "new-version", time.Second); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("readiness accepted the prior service identity after %d request(s)", calls)
	}
}

func TestHealthyUpdateStopsOldAndStartsNewVersionOnEveryPlatform(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			health := &fakeHealth{}
			engine := newTestEngine(t, goos, health)
			layout, _ := layoutFor(goos, engine.Home)
			oldFiles := map[string]string{
				layout.Binary:            "old executable bytes",
				layout.Environment:       "KAITEN_URL=https://old.example\nKAITEN_API_TOKEN=old-token\n",
				layout.ServiceDefinition: "old service definition",
			}
			for path, content := range oldFiles {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			engine.Versions = fakeVersions{values: map[string]string{engine.Executable: "2.0.0", layout.Binary: "1.0.0"}}
			engine.Environment["KAITEN_API_TOKEN"] = "new-token"
			sawOldStop := false
			sawNewStart := false
			commands := &scriptedCommands{}
			commands.run = func(_ int, name string, arguments []string) error {
				joined := strings.Join(arguments, " ")
				stop := (goos == "darwin" && name == "launchctl" && len(arguments) > 0 && arguments[0] == "bootout" && !sawOldStop) ||
					(goos == "linux" && name == "systemctl" && strings.Contains(joined, "disable --now")) ||
					(goos == "windows" && name == "powershell")
				start := (goos == "darwin" && name == "launchctl" && len(arguments) > 0 && arguments[0] == "bootstrap") ||
					(goos == "linux" && name == "systemctl" && strings.Contains(joined, "enable --now")) ||
					(goos == "windows" && name == "cmd")
				if stop {
					data, _ := os.ReadFile(layout.Binary)
					if string(data) != "old executable bytes" {
						t.Errorf("old service was not stopped before replacement: %q", data)
					}
					sawOldStop = true
				}
				if start {
					data, _ := os.ReadFile(layout.Binary)
					if string(data) != "new executable bytes" {
						t.Errorf("activation did not run the installed replacement: %q", data)
					}
					sawNewStart = true
				}
				return nil
			}
			engine.Commands = commands
			input := strings.NewReader("u\nhttps://new.example\nn\nn\n")
			if err := engine.Install(context.Background(), input, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !sawOldStop || !sawNewStart {
				t.Fatalf("lifecycle plan incomplete: old-stop=%t new-start=%t calls=%v", sawOldStop, sawNewStart, commands.calls)
			}
			if len(health.versions) != 1 || health.versions[0] != "2.0.0" {
				t.Fatalf("readiness did not prove the new version: %v", health.versions)
			}
			definition, _ := os.ReadFile(layout.ServiceDefinition)
			binaryReference := layout.Binary
			switch goos {
			case "darwin":
				binaryReference = html.EscapeString(layout.Binary)
			case "linux":
				binaryReference = strconv.Quote(layout.Binary)
			case "windows":
				binaryReference = windowsQuote(layout.Binary)
			}
			if !strings.Contains(string(definition), binaryReference) {
				t.Fatalf("service definition does not execute installed binary: %s", definition)
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

func TestHealthFailureRestoresAndVerifiesPreviousVersion(t *testing.T) {
	health := &fakeHealth{wait: func(version string) error {
		if version == "2.0.0" {
			return errors.New("replacement not ready")
		}
		return nil
	}}
	engine := newTestEngine(t, "linux", health)
	layout, _ := layoutFor("linux", engine.Home)
	oldFiles := map[string]string{
		layout.Binary:            "old binary",
		layout.Environment:       "KAITEN_URL=https://old.example\nKAITEN_API_TOKEN=old-token\n",
		layout.ServiceDefinition: "old service",
	}
	for path, content := range oldFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine.Versions = fakeVersions{values: map[string]string{engine.Executable: "2.0.0", layout.Binary: "1.0.0"}}
	engine.Environment["KAITEN_API_TOKEN"] = "new-token"
	err := engine.Install(context.Background(), strings.NewReader("u\nhttps://new.example\nn\nn\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `verify installed service version "2.0.0"`) {
		t.Fatalf("unexpected failure: %v", err)
	}
	if fmt.Sprint(health.versions) != "[2.0.0 1.0.0]" {
		t.Fatalf("readiness identities = %v, want replacement then restored version", health.versions)
	}
	for path, expected := range oldFiles {
		actual, readErr := os.ReadFile(path)
		if readErr != nil || string(actual) != expected {
			t.Fatalf("%s was not restored: %q err=%v", path, actual, readErr)
		}
	}
}

func TestWriteFailureRestoresFilesAndReportsReactivationFailure(t *testing.T) {
	engine := newTestEngine(t, "linux", &fakeHealth{})
	layout, _ := layoutFor("linux", engine.Home)
	oldFiles := map[string]string{
		layout.Binary:            "old binary",
		layout.Environment:       "KAITEN_URL=https://old.example\nKAITEN_API_TOKEN=old-token\n",
		layout.ServiceDefinition: "old service",
	}
	for path, content := range oldFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(layout.Log)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(layout.Log), []byte("blocks log directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine.Versions = fakeVersions{values: map[string]string{engine.Executable: "2.0.0", layout.Binary: "1.0.0"}}
	engine.Environment["KAITEN_API_TOKEN"] = "new-token"
	commands := &scriptedCommands{}
	commands.run = func(_ int, name string, arguments []string) error {
		if name == "systemctl" && strings.Contains(strings.Join(arguments, " "), "enable --now") {
			return errors.New("reactivation denied")
		}
		return nil
	}
	engine.Commands = commands
	err := engine.Install(context.Background(), strings.NewReader("u\nhttps://new.example\nn\nn\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "reactivate previous service") || !strings.Contains(err.Error(), "reactivation denied") {
		t.Fatalf("reactivation failure was not reported: %v", err)
	}
	for path, expected := range oldFiles {
		actual, readErr := os.ReadFile(path)
		if readErr != nil || string(actual) != expected {
			t.Fatalf("%s was not restored after write failure: %q err=%v", path, actual, readErr)
		}
	}
}

func TestActivationFailureRestoresPreviousService(t *testing.T) {
	health := &fakeHealth{}
	engine := newTestEngine(t, "linux", health)
	layout, _ := layoutFor("linux", engine.Home)
	oldFiles := map[string]string{
		layout.Binary:            "old binary",
		layout.Environment:       "KAITEN_URL=https://old.example\nKAITEN_API_TOKEN=old-token\n",
		layout.ServiceDefinition: "old service",
	}
	for path, content := range oldFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine.Versions = fakeVersions{values: map[string]string{engine.Executable: "2.0.0", layout.Binary: "1.0.0"}}
	engine.Environment["KAITEN_API_TOKEN"] = "new-token"
	enableCalls := 0
	commands := &scriptedCommands{}
	commands.run = func(_ int, name string, arguments []string) error {
		if name == "systemctl" && strings.Contains(strings.Join(arguments, " "), "enable --now") {
			enableCalls++
			if enableCalls == 1 {
				return errors.New("replacement activation denied")
			}
		}
		return nil
	}
	engine.Commands = commands
	err := engine.Install(context.Background(), strings.NewReader("u\nhttps://new.example\nn\nn\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "replacement activation denied") {
		t.Fatalf("unexpected activation result: %v", err)
	}
	if fmt.Sprint(health.versions) != "[1.0.0]" || enableCalls != 2 {
		t.Fatalf("previous service was not reactivated and verified: health=%v enableCalls=%d", health.versions, enableCalls)
	}
	for path, expected := range oldFiles {
		actual, readErr := os.ReadFile(path)
		if readErr != nil || string(actual) != expected {
			t.Fatalf("%s was not restored: %q err=%v", path, actual, readErr)
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("client config mode is %o", info.Mode().Perm())
	}
	backupInfo, _ := os.Stat(path + ".bak")
	if runtime.GOOS != "windows" && backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("client config backup mode is %o", backupInfo.Mode().Perm())
	}
}

func TestSecretReplacementRestrictsCurrentBackupAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("TOKEN=old-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction := &transaction{}
	if err := transaction.replace(path, []byte("TOKEN=new-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + ".bak"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat secret file %s: %v", candidate, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("secret file %s mode=%o", candidate, info.Mode().Perm())
		}
	}
	if err := transaction.rollback(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "TOKEN=old-secret\n" || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("rollback did not restore and restrict prior secret: %q mode=%o", data, info.Mode().Perm())
	}
}

func TestClientConfigTypeConflictErrorsWithoutChangingFile(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove_%t", remove), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "client.json")
			original := []byte(`{"theme":"dark","mcpServers":["not-an-object"]}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := mergeClientConfig(path, "http://127.0.0.1:8100/mcp", remove); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
				t.Fatalf("type conflict was not reported: %v", err)
			}
			actual, _ := os.ReadFile(path)
			if string(actual) != string(original) || fileExists(path+".bak") {
				t.Fatalf("conflicting configuration changed: %q backup=%t", actual, fileExists(path+".bak"))
			}
		})
	}
}

func TestFailedWindowsReplacementKeepsDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kaiten-mcp.exe")
	if err := os.WriteFile(path, []byte("old executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	call := 0
	rename := func(oldPath, newPath string) error {
		call++
		switch call {
		case 1:
			return os.ErrPermission
		case 3:
			return errors.New("injected Windows replacement failure")
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	err := writeAtomicWithRename(path, []byte("new executable"), 0o700, rename)
	if err == nil || !strings.Contains(err.Error(), "injected Windows replacement failure") {
		t.Fatalf("replacement failure missing: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "old executable" {
		t.Fatalf("failed replacement left destination missing or changed: %q err=%v", data, readErr)
	}
}

func TestUninstallIsScopedIdempotentAndPreservesLogs(t *testing.T) {
	engine := newTestEngine(t, "linux", &fakeHealth{})
	layout, _ := layoutFor("linux", engine.Home)
	commands := &scriptedCommands{}
	var reloadSawDefinition []bool
	commands.run = func(_ int, name string, arguments []string) error {
		if name == "systemctl" && strings.Join(arguments, " ") == "--user daemon-reload" {
			reloadSawDefinition = append(reloadSawDefinition, fileExists(layout.ServiceDefinition))
		}
		return nil
	}
	engine.Commands = commands
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
	if len(reloadSawDefinition) != 2 || !reloadSawDefinition[0] || reloadSawDefinition[1] {
		t.Fatalf("systemd reload states = %v, want definition present before removal then absent", reloadSawDefinition)
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
	if len(reloadSawDefinition) != 3 || reloadSawDefinition[2] {
		t.Fatalf("idempotent uninstall did not refresh absent unit: %v", reloadSawDefinition)
	}
}

func TestRequestedClientCleanupFailureMakesUninstallFail(t *testing.T) {
	engine := newTestEngine(t, "linux", &fakeHealth{})
	layout, _ := layoutFor("linux", engine.Home)
	original := []byte(`{"mcpServers":"conflicting-value","unrelated":true}`)
	if err := os.WriteFile(layout.ClaudeCodeConfig, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	err := engine.Uninstall(context.Background(), strings.NewReader("y\n"), io.Discard, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "client configuration") {
		t.Fatalf("requested cleanup failure did not make uninstall fail: %v", err)
	}
	actual, _ := os.ReadFile(layout.ClaudeCodeConfig)
	if string(actual) != string(original) || !strings.Contains(diagnostics.String(), "was not updated") {
		t.Fatalf("conflicting client configuration was not preserved/reported: %q diagnostics=%q", actual, diagnostics.String())
	}
}

func TestOwnedFileCleanupFailureMakesUninstallFail(t *testing.T) {
	engine := newTestEngine(t, "linux", &fakeHealth{})
	layout, _ := layoutFor("linux", engine.Home)
	if err := os.MkdirAll(layout.Binary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Binary, "blocks-removal"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := engine.Uninstall(context.Background(), strings.NewReader("n\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), layout.Binary) || !fileExists(layout.Binary) {
		t.Fatalf("owned cleanup failure was not returned with remaining path: %v", err)
	}
}

func TestInvalidTenantStopsBeforeWritingOrActivation(t *testing.T) {
	for _, tenant := range []string{"https://user:pass@example.test?q=1", "https://example.test/api/v1"} {
		t.Run(tenant, func(t *testing.T) {
			commands := &fakeCommands{}
			engine := newTestEngine(t, "darwin", &fakeHealth{})
			engine.Commands = commands
			err := engine.Install(context.Background(), strings.NewReader(tenant+"\n"), io.Discard, io.Discard)
			if err == nil {
				t.Fatal("expected tenant validation error")
			}
			layout, _ := layoutFor("darwin", engine.Home)
			if fileExists(layout.Binary) || len(commands.calls) != 0 {
				t.Fatalf("invalid input changed state: binary=%v calls=%v", fileExists(layout.Binary), commands.calls)
			}
		})
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
