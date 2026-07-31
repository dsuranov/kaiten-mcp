package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

type commandRunner interface {
	Run(context.Context, string, ...string) error
}

type healthChecker interface {
	Wait(context.Context, string, string, time.Duration) error
}

type binaryVersionReader interface {
	ReadVersion(context.Context, string) (string, error)
}

type terminalAccess interface {
	IsTerminal(int) bool
	ReadPassword(int) ([]byte, error)
}

type execCommands struct{}

type execBinaryVersion struct{}

type nativeTerminal struct{}

func (execCommands) Run(ctx context.Context, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func (execBinaryVersion) ReadVersion(ctx context.Context, path string) (string, error) {
	command := exec.CommandContext(ctx, path, "version")
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run %s version: %w", filepath.Base(path), err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != serviceName || strings.TrimSpace(fields[1]) == "" {
		return "", fmt.Errorf("%s returned an unrecognized version response", filepath.Base(path))
	}
	return fields[1], nil
}

func (nativeTerminal) IsTerminal(fd int) bool { return term.IsTerminal(fd) }

func (nativeTerminal) ReadPassword(fd int) ([]byte, error) { return term.ReadPassword(fd) }

type httpReadiness struct{ client *http.Client }

func (h httpReadiness) Wait(ctx context.Context, endpoint, expectedVersion string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		response, err := h.client.Do(request)
		if err == nil {
			var body map[string]any
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && body["status"] == "ok" && body["version"] == expectedVersion {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("service did not become healthy before the readiness deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Engine exposes a deterministic lifecycle that can be tested against a
// temporary user profile without touching the real profile.
type Engine struct {
	GOOS             string
	Home             string
	Executable       string
	UID              string
	Environment      map[string]string
	Commands         commandRunner
	Health           healthChecker
	Versions         binaryVersionReader
	Terminal         terminalAccess
	ReadinessTimeout time.Duration
}

// DefaultEngine resolves the current per-user environment.
func DefaultEngine() (*Engine, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if filepath.Base(executable) != binaryName(runtime.GOOS) {
		sibling := filepath.Join(filepath.Dir(executable), binaryName(runtime.GOOS))
		if _, err := os.Stat(sibling); err == nil {
			executable = sibling
		} else {
			return nil, fmt.Errorf("%s must be beside kaiten for installation", binaryName(runtime.GOOS))
		}
	}
	uid := ""
	if current, err := user.Current(); err == nil {
		uid = current.Uid
	}
	return &Engine{
		GOOS: runtime.GOOS, Home: home, Executable: executable, UID: uid,
		Environment: selectedEnvironment(), Commands: execCommands{},
		Health: httpReadiness{client: &http.Client{Timeout: time.Second}}, Versions: execBinaryVersion{}, Terminal: nativeTerminal{}, ReadinessTimeout: 15 * time.Second,
	}, nil
}

func binaryName(goos string) string {
	if goos == "windows" {
		return serviceName + ".exe"
	}
	return serviceName
}

func selectedEnvironment() map[string]string {
	result := make(map[string]string)
	for _, name := range []string{"KAITEN_API_TOKEN", "KAITEN_TOKEN", "KAITEN_URL", "KAITEN_BASE_URL", "KAITEN_ENABLE_WRITE_TOOLS"} {
		result[name] = os.Getenv(name)
	}
	return result
}

// Install performs an interactive, transactional per-user installation.
func (e *Engine) Install(ctx context.Context, input io.Reader, output, diagnostics io.Writer) error {
	layout, err := layoutFor(e.GOOS, e.Home)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	existing := fileExists(layout.Binary) || fileExists(layout.ServiceDefinition)
	if existing {
		choice, err := prompt(reader, output, "Existing installation: update, reinstall, or cancel? [u/r/c]", "u")
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "c", "cancel":
			fmt.Fprintln(output, "Installation canceled; the existing service was not changed.")
			return nil
		case "u", "update", "r", "reinstall":
		default:
			return errors.New("installation choice must be update, reinstall, or cancel")
		}
	}
	existingValues := readEnvironment(layout.Environment)
	defaultURL := firstValue(e.Environment["KAITEN_URL"], e.Environment["KAITEN_BASE_URL"], existingValues["KAITEN_URL"])
	tenantURL, err := prompt(reader, output, "Kaiten tenant URL", defaultURL)
	if err != nil {
		return err
	}
	tenantURL, err = validateTenant(tenantURL)
	if err != nil {
		return err
	}
	token := firstValue(e.Environment["KAITEN_API_TOKEN"], e.Environment["KAITEN_TOKEN"], existingValues["KAITEN_API_TOKEN"])
	if token == "" {
		token, err = e.readToken(input, reader, output)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(output, "Using the configured API token (value hidden).")
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("a nonempty single-line Kaiten API token is required")
	}
	writeDefault := strings.EqualFold(strings.TrimSpace(firstValue(e.Environment["KAITEN_ENABLE_WRITE_TOOLS"], existingValues["KAITEN_ENABLE_WRITE_TOOLS"])), "true")
	writeAnswer, err := prompt(reader, output, "Enable MCP write tools? [y/N]", boolDefault(writeDefault))
	if err != nil {
		return err
	}
	writeEnabled := affirmative(writeAnswer)
	clientAnswer, err := prompt(reader, output, "Register the kaiten endpoint with Claude Code and Claude Desktop? [y/N]", "n")
	if err != nil {
		return err
	}
	registerClients := affirmative(clientAnswer)

	executable, err := os.ReadFile(e.Executable)
	if err != nil {
		return fmt.Errorf("read installation executable: %w", err)
	}
	versions := e.Versions
	if versions == nil {
		versions = execBinaryVersion{}
	}
	newVersion, err := versions.ReadVersion(ctx, e.Executable)
	if err != nil {
		return fmt.Errorf("identify installation executable: %w", err)
	}
	oldVersion := ""
	if fileExists(layout.Binary) {
		if value, inspectErr := versions.ReadVersion(ctx, layout.Binary); inspectErr == nil {
			oldVersion = value
		} else {
			fmt.Fprintf(diagnostics, "warning: previous service version could not be identified; rollback can still restore its files: %v\n", inspectErr)
		}
	}
	oldStopped := false
	if existing {
		if err := e.deactivate(ctx, layout); err != nil {
			return fmt.Errorf("stop existing service before replacement: %w", err)
		}
		oldStopped = true
	}
	transaction := &transaction{}
	activationAttempted := false
	rollback := func(cause error) error {
		failures := []error{cause}
		if activationAttempted {
			if deactivateErr := e.deactivate(ctx, layout); deactivateErr != nil {
				failures = append(failures, fmt.Errorf("stop failed replacement service: %w", deactivateErr))
			}
		}
		if rollbackErr := transaction.rollback(); rollbackErr != nil {
			failures = append(failures, fmt.Errorf("restore previous files: %w", rollbackErr))
		}
		if existing && oldStopped {
			if activateErr := e.activate(ctx, layout); activateErr != nil {
				failures = append(failures, fmt.Errorf("reactivate previous service: %w", activateErr))
			} else if oldVersion != "" {
				if readinessErr := e.Health.Wait(ctx, "http://127.0.0.1:8100/health", oldVersion, e.ReadinessTimeout); readinessErr != nil {
					failures = append(failures, fmt.Errorf("verify reactivated previous service version %q: %w", oldVersion, readinessErr))
				}
			}
		}
		return errors.Join(failures...)
	}
	if err := transaction.replace(layout.Binary, executable, 0o700); err != nil {
		return rollback(err)
	}
	if err := transaction.replace(layout.Environment, environmentFile(tenantURL, token, writeEnabled), 0o600); err != nil {
		return rollback(err)
	}
	if err := transaction.replace(layout.ServiceDefinition, serviceDefinition(layout), 0o600); err != nil {
		return rollback(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.Log), 0o700); err != nil {
		return rollback(err)
	}
	activationAttempted = true
	if err := e.activate(ctx, layout); err != nil {
		return rollback(fmt.Errorf("activate service: %w", err))
	}
	if err := e.Health.Wait(ctx, "http://127.0.0.1:8100/health", newVersion, e.ReadinessTimeout); err != nil {
		return rollback(fmt.Errorf("verify installed service version %q: %w", newVersion, err))
	}
	if registerClients {
		for _, path := range []string{layout.ClaudeCodeConfig, layout.ClaudeDesktopConfig} {
			if err := mergeClientConfig(path, "http://127.0.0.1:8100/mcp", false); err != nil {
				fmt.Fprintf(diagnostics, "warning: service installed, but client configuration %s was not updated: %v\n", path, err)
			}
		}
	}
	fmt.Fprintln(output, "Kaiten MCP is ready at http://127.0.0.1:8100/mcp")
	fmt.Fprintf(output, "Health: http://127.0.0.1:8100/health\nLogs: %s\n", layout.Log)
	return nil
}

// Uninstall removes only this product's per-user files and optionally its
// client registrations. Logs are deliberately preserved.
func (e *Engine) Uninstall(ctx context.Context, input io.Reader, output, diagnostics io.Writer) error {
	layout, err := layoutFor(e.GOOS, e.Home)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	removeAnswer, err := prompt(reader, output, "Remove the kaiten entry from Claude Code and Claude Desktop? [y/N]", "n")
	if err != nil {
		return err
	}
	var failures []error
	if err := e.deactivate(ctx, layout); err != nil {
		failures = append(failures, fmt.Errorf("stop service: %w", err))
	}
	deferWindowsSelfRemoval := e.GOOS == "windows" && samePath(e.Executable, layout.Binary)
	for _, path := range []string{layout.Binary, layout.Environment, layout.ServiceDefinition} {
		for _, candidate := range []string{path, path + ".bak", path + ".replace-old"} {
			if candidate == layout.Binary && deferWindowsSelfRemoval {
				continue
			}
			if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove %s: %w", candidate, err))
			}
		}
	}
	// systemd cached the unit while it still existed during deactivate. Reload
	// once more after deleting the definition so a successful uninstall leaves
	// neither an active service nor a stale loaded unit identity behind.
	if e.GOOS == "linux" {
		if err := e.Commands.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			failures = append(failures, fmt.Errorf("refresh user service manager after removal: %w", err))
		}
	}
	if affirmative(removeAnswer) {
		for _, path := range []string{layout.ClaudeCodeConfig, layout.ClaudeDesktopConfig} {
			if err := mergeClientConfig(path, "", true); err != nil && !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(diagnostics, "warning: client configuration %s was not updated: %v\n", path, err)
				failures = append(failures, fmt.Errorf("remove kaiten from client configuration %s: %w", path, err))
			}
		}
	}
	if deferWindowsSelfRemoval {
		if err := e.scheduleWindowsSelfRemoval(ctx, layout); err != nil {
			failures = append(failures, fmt.Errorf("schedule executable removal: %w", err))
		}
	}
	fmt.Fprintf(output, "Kaiten MCP service files removed. Logs were preserved at %s\n", filepath.Dir(layout.Log))
	if len(failures) > 0 {
		return fmt.Errorf("cleanup incomplete; remaining items were reported: %w", errors.Join(failures...))
	}
	return nil
}

func (e *Engine) activate(ctx context.Context, layout Layout) error {
	switch layout.GOOS {
	case "darwin":
		target := "gui/" + e.UID
		_ = e.Commands.Run(ctx, "launchctl", "bootout", target, layout.ServiceDefinition)
		if err := e.Commands.Run(ctx, "launchctl", "bootstrap", target, layout.ServiceDefinition); err != nil {
			return err
		}
		return e.Commands.Run(ctx, "launchctl", "kickstart", "-k", target+"/io.github.dsuranov.kaiten-mcp")
	case "linux":
		if err := e.Commands.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return e.Commands.Run(ctx, "systemctl", "--user", "enable", "--now", serviceName+".service")
	case "windows":
		return e.Commands.Run(ctx, "cmd", "/C", layout.ServiceDefinition)
	default:
		return errors.New("unsupported operating system")
	}
}

func (e *Engine) deactivate(ctx context.Context, layout Layout) error {
	switch layout.GOOS {
	case "darwin":
		if !fileExists(layout.ServiceDefinition) {
			return nil
		}
		return e.Commands.Run(ctx, "launchctl", "bootout", "gui/"+e.UID, layout.ServiceDefinition)
	case "linux":
		if !fileExists(layout.ServiceDefinition) {
			return nil
		}
		if err := e.Commands.Run(ctx, "systemctl", "--user", "disable", "--now", serviceName+".service"); err != nil {
			return err
		}
		return e.Commands.Run(ctx, "systemctl", "--user", "daemon-reload")
	case "windows":
		if !fileExists(layout.Binary) {
			return nil
		}
		script := fmt.Sprintf("$target='%s'; $self=%d; Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $target -and $_.ProcessId -ne $self } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }", strings.ReplaceAll(layout.Binary, "'", "''"), os.Getpid())
		return e.Commands.Run(ctx, "powershell", "-NoProfile", "-Command", script)
	default:
		return errors.New("unsupported operating system")
	}
}

func (e *Engine) scheduleWindowsSelfRemoval(ctx context.Context, layout Layout) error {
	cleanup := filepath.Join(filepath.Dir(layout.Log), "uninstall-cleanup.cmd")
	contents := fmt.Sprintf("@echo off\r\nping 127.0.0.1 -n 3 >nul\r\ndel /f /q %s\r\ndel /f /q \"%%~f0\"\r\n", windowsQuote(layout.Binary))
	if err := writeAtomic(cleanup, []byte(contents), 0o600); err != nil {
		return err
	}
	return e.Commands.Run(ctx, "cmd", "/C", "start", "", "/b", cleanup)
}

func samePath(first, second string) bool {
	firstAbsolute, firstErr := filepath.Abs(first)
	secondAbsolute, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(firstAbsolute), filepath.Clean(secondAbsolute))
}

func prompt(reader *bufio.Reader, output io.Writer, label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(output, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(output, "%s: ", label)
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return value, nil
}

type fileDescriptor interface{ Fd() uintptr }

func (e *Engine) readToken(input io.Reader, reader *bufio.Reader, output io.Writer) (string, error) {
	terminal := e.Terminal
	if terminal == nil {
		terminal = nativeTerminal{}
	}
	if source, ok := input.(fileDescriptor); ok {
		fd := int(source.Fd())
		if terminal.IsTerminal(fd) {
			fmt.Fprint(output, "Kaiten API token (input hidden): ")
			secret, err := terminal.ReadPassword(fd)
			fmt.Fprintln(output)
			if err != nil {
				return "", fmt.Errorf("read hidden API token: %w", err)
			}
			return strings.TrimSpace(string(secret)), nil
		}
	}
	fmt.Fprint(output, "Kaiten API token: ")
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func validateTenant(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("tenant URL must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("tenant URL must not contain credentials, query, or fragment")
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return "", errors.New("tenant URL must be a tenant origin without a path")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func readEnvironment(path string) map[string]string {
	result := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			result[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return result
}

func firstValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boolDefault(value bool) string {
	if value {
		return "y"
	}
	return "n"
}

func affirmative(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "y" || value == "yes" || value == "true"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// RunInstall adapts the engine to CLI lifecycle callbacks.
func RunInstall(ctx context.Context, input io.Reader, output, diagnostics io.Writer) int {
	engine, err := DefaultEngine()
	if err == nil {
		err = engine.Install(ctx, input, output, diagnostics)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "error: installation failed: %v\n", err)
		return 1
	}
	return 0
}

// RunUninstall adapts the engine to CLI lifecycle callbacks.
func RunUninstall(ctx context.Context, input io.Reader, output, diagnostics io.Writer) int {
	engine, err := DefaultEngine()
	if err == nil {
		err = engine.Uninstall(ctx, input, output, diagnostics)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "error: uninstall incomplete: %v\n", err)
		return 1
	}
	return 0
}
