package nativeci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	servicePort  = 8100
	serviceLabel = "io.github.dsuranov.kaiten-mcp"
	serviceUnit  = "kaiten-mcp.service"
)

type layout struct {
	root, binary, environment, log, definition, claudeCode, claudeDesktop string
}

func expectedLayout(goos, profile string) (layout, error) {
	switch goos {
	case "darwin":
		root := filepath.Join(profile, "Library", "Application Support", "kaiten-mcp")
		return layout{root: root, binary: filepath.Join(root, "bin", "kaiten-mcp"), environment: filepath.Join(root, ".env"), log: filepath.Join(root, "logs", "service.log"), definition: filepath.Join(profile, "Library", "LaunchAgents", serviceLabel+".plist"), claudeCode: filepath.Join(profile, ".claude.json"), claudeDesktop: filepath.Join(profile, "Library", "Application Support", "Claude", "claude_desktop_config.json")}, nil
	case "linux":
		root := filepath.Join(profile, ".local", "share", "kaiten-mcp")
		return layout{root: root, binary: filepath.Join(root, "bin", "kaiten-mcp"), environment: filepath.Join(root, ".env"), log: filepath.Join(profile, ".local", "state", "kaiten-mcp", "service.log"), definition: filepath.Join(profile, ".config", "systemd", "user", serviceUnit), claudeCode: filepath.Join(profile, ".claude.json"), claudeDesktop: filepath.Join(profile, ".config", "Claude", "claude_desktop_config.json")}, nil
	case "windows":
		root := filepath.Join(profile, "AppData", "Local", "KaitenMCP")
		return layout{root: root, binary: filepath.Join(root, "bin", "kaiten-mcp.exe"), environment: filepath.Join(root, ".env"), log: filepath.Join(root, "logs", "service.log"), definition: filepath.Join(profile, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "kaiten-mcp.cmd"), claudeCode: filepath.Join(profile, ".claude.json"), claudeDesktop: filepath.Join(profile, "AppData", "Roaming", "Claude", "claude_desktop_config.json")}, nil
	default:
		return layout{}, fmt.Errorf("unsupported native lifecycle OS %q", goos)
	}
}

func netListenLoopback() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

func requireServicePortFree() error {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", servicePort))
	if err != nil {
		return fmt.Errorf("fixed lifecycle port %d is already occupied: %w", servicePort, err)
	}
	return listener.Close()
}

func waitForHealth(ctx context.Context, client *http.Client, endpoint, version string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		response, err := client.Do(request)
		if err == nil {
			var health struct {
				Status  string `json:"status"`
				Version string `json:"version"`
				Runtime string `json:"runtime"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Status == "ok" && health.Version == version && health.Runtime != "" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("health did not report exact version %q before deadline", version)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func prepareProfile(profile string) error {
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(profile, 0o700)
	}
	script := fmt.Sprintf(`$path='%s'; $acl=Get-Acl -LiteralPath $path; $acl.SetAccessRuleProtection($true,$false); foreach($rule in @($acl.Access)){[void]$acl.RemoveAccessRuleAll($rule)}; $inherit=[System.Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'; $prop=[System.Security.AccessControl.PropagationFlags]::None; $allow=[System.Security.AccessControl.AccessControlType]::Allow; $me=[System.Security.Principal.WindowsIdentity]::GetCurrent().User; $system=New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18'); [void]$acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($me,'FullControl',$inherit,$prop,$allow))); [void]$acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($system,'FullControl',$inherit,$prop,$allow))); Set-Acl -LiteralPath $path -AclObject $acl`, psQuote(profile))
	return runQuiet(context.Background(), "powershell", "-NoProfile", "-Command", script)
}

func psQuote(value string) string { return strings.ReplaceAll(value, "'", "''") }

func childEnvironment(profile, tenantURL, token string) []string {
	remove := map[string]bool{"HOME": true, "USERPROFILE": true, "KAITEN_URL": true, "KAITEN_BASE_URL": true, "KAITEN_API_TOKEN": true, "KAITEN_TOKEN": true, "KAITEN_ENABLE_WRITE_TOOLS": true}
	environment := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !remove[strings.ToUpper(name)] {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "HOME="+profile, "USERPROFILE="+profile, "KAITEN_URL="+tenantURL, "KAITEN_API_TOKEN="+token, "KAITEN_ENABLE_WRITE_TOOLS=false")
	return environment
}

type capture struct {
	label, command, stdout, stderr string
	failureTrigger                 string
	exitCode                       int
	duration                       time.Duration
}

func runCaptured(ctx context.Context, executable string, arguments []string, input string, directory string, environment []string) (capture, error) {
	started := time.Now()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	commandName := filepath.Base(executable) + " " + strings.Join(arguments, " ")
	result := capture{label: commandName, command: commandName, stdout: stdout.String(), stderr: stderr.String(), duration: time.Since(started)}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, fmt.Errorf("%s exited %d", result.label, result.exitCode)
	}
	result.exitCode = -1
	return result, fmt.Errorf("run %s: %w", result.label, err)
}

func serviceProcessID(ctx context.Context, paths layout) (int, error) {
	var output string
	var err error
	switch runtime.GOOS {
	case "darwin":
		output, err = managerStatus(ctx, paths)
		if err != nil {
			return 0, err
		}
		match := regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`).FindStringSubmatch(output)
		if len(match) != 2 {
			return 0, errors.New("launchctl status omitted the active process ID")
		}
		output = match[1]
	case "linux":
		output, err = outputQuiet(ctx, "systemctl", "--user", "show", "--property=MainPID", "--value", serviceUnit)
		if err != nil {
			return 0, err
		}
	case "windows":
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $target='%s'; $found=@(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $target }); if($found.Count -ne 1){exit 1}; [Console]::Out.Write($found[0].ProcessId)`, psQuote(paths.binary))
		output, err = outputQuiet(ctx, "powershell", "-NoProfile", "-Command", script)
		if err != nil {
			return 0, err
		}
	default:
		return 0, errors.New("unsupported service manager")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("native manager returned invalid process ID %q", strings.TrimSpace(output))
	}
	return pid, nil
}

func runQuiet(ctx context.Context, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func outputQuiet(ctx context.Context, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stderr = io.Discard
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func combinedOutput(ctx context.Context, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s status failed: %w", name, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func currentUID() (string, error) {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return "", errors.New("current user ID is unavailable")
	}
	return current.Uid, nil
}

func requireNonRootLinux() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	uid, err := currentUID()
	if err != nil {
		return err
	}
	if uid == "0" {
		return errors.New("native Linux lifecycle must run as an unprivileged user")
	}
	return runQuiet(context.Background(), "systemctl", "--user", "show-environment")
}

func serviceAbsent(ctx context.Context, paths layout) error {
	switch runtime.GOOS {
	case "darwin":
		present, err := launchdServicePresent(ctx)
		if err != nil {
			return err
		}
		if present {
			return errors.New("fixed launchd label already exists")
		}
	case "linux":
		state, err := outputQuiet(ctx, "systemctl", "--user", "show", "--property=LoadState", "--value", serviceUnit)
		if err != nil {
			return fmt.Errorf("probe fixed systemd unit identity: %w", err)
		}
		if state != "not-found" {
			return fmt.Errorf("fixed systemd unit already exists with load state %s", state)
		}
	case "windows":
		if fileExists(paths.definition) {
			return errors.New("isolated Windows Startup entry already exists")
		}
		present, err := windowsProcessExists(ctx, paths.binary)
		if err != nil {
			return err
		}
		if present {
			return errors.New("isolated Windows service process already exists")
		}
	}
	return nil
}

func launchdServicePresent(ctx context.Context) (bool, error) {
	uid, err := currentUID()
	if err != nil {
		return false, err
	}
	command := exec.CommandContext(ctx, "launchctl", "print", "gui/"+uid+"/"+serviceLabel)
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	absentMarker := fmt.Sprintf(`Could not find service "%s" in domain for user gui: %s`, serviceLabel, uid)
	if errors.As(err, &exitError) && exitError.ExitCode() == 113 && strings.Contains(string(output), absentMarker) {
		return false, nil
	}
	return false, fmt.Errorf("probe fixed launchd label: %w: %s", err, strings.TrimSpace(string(output)))
}

func serviceActive(ctx context.Context, paths layout) error {
	switch runtime.GOOS {
	case "darwin":
		present, err := launchdServicePresent(ctx)
		if err != nil {
			return err
		}
		if !present {
			return errors.New("fixed launchd label is not active")
		}
		return nil
	case "linux":
		if err := runQuiet(ctx, "systemctl", "--user", "is-active", "--quiet", serviceUnit); err != nil {
			return err
		}
		return runQuiet(ctx, "systemctl", "--user", "is-enabled", "--quiet", serviceUnit)
	case "windows":
		present, err := windowsProcessExists(ctx, paths.binary)
		if err != nil {
			return err
		}
		if !fileExists(paths.definition) || !present {
			return errors.New("Windows Startup activation is not running")
		}
		return nil
	default:
		return errors.New("unsupported service manager")
	}
}

func managerStatus(ctx context.Context, paths layout) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		uid, err := currentUID()
		if err != nil {
			return "", err
		}
		return combinedOutput(ctx, "launchctl", "print", "gui/"+uid+"/"+serviceLabel)
	case "linux":
		return combinedOutput(ctx, "systemctl", "--user", "status", "--no-pager", "--full", serviceUnit)
	case "windows":
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $target='%s'; $found=Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $target } | Select-Object ProcessId,ExecutablePath,CommandLine; if(!$found){exit 1}; $found | ConvertTo-Json -Depth 3`, psQuote(paths.binary))
		return combinedOutput(ctx, "powershell", "-NoProfile", "-Command", script)
	default:
		return "", errors.New("unsupported service manager")
	}
}

func restartService(ctx context.Context, paths layout) error {
	switch runtime.GOOS {
	case "darwin":
		uid, err := currentUID()
		if err != nil {
			return err
		}
		return runQuiet(ctx, "launchctl", "kickstart", "-k", "gui/"+uid+"/"+serviceLabel)
	case "linux":
		return runQuiet(ctx, "systemctl", "--user", "restart", serviceUnit)
	case "windows":
		if err := stopWindowsProcess(ctx, paths.binary); err != nil {
			return err
		}
		return runQuiet(ctx, "cmd", "/C", paths.definition)
	default:
		return errors.New("unsupported service manager")
	}
}

func stopNativeService(ctx context.Context, paths layout) error {
	switch runtime.GOOS {
	case "darwin":
		if fileExists(paths.definition) {
			uid, err := currentUID()
			if err != nil {
				return err
			}
			if err := runQuiet(ctx, "launchctl", "bootout", "gui/"+uid, paths.definition); err != nil {
				return err
			}
		}
		return serviceAbsent(ctx, paths)
	case "linux":
		if fileExists(paths.definition) {
			if err := runQuiet(ctx, "systemctl", "--user", "disable", "--now", serviceUnit); err != nil {
				return err
			}
		}
		return serviceAbsent(ctx, paths)
	case "windows":
		if err := stopWindowsProcess(ctx, paths.binary); err != nil {
			return err
		}
		return serviceAbsent(ctx, paths)
	default:
		return nil
	}
}

func windowsProcessExists(ctx context.Context, binary string) (bool, error) {
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $target='%s'; $found=Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $target }; if($found){[Console]::Out.Write('present')}else{[Console]::Out.Write('absent')}`, psQuote(binary))
	output, err := outputQuiet(ctx, "powershell", "-NoProfile", "-Command", script)
	if err != nil {
		return false, fmt.Errorf("probe isolated Windows service process: %w", err)
	}
	switch output {
	case "present":
		return true, nil
	case "absent":
		return false, nil
	default:
		return false, fmt.Errorf("Windows process probe returned %q", output)
	}
}

func nativeProcessAbsent(ctx context.Context, binary string) error {
	if runtime.GOOS == "windows" {
		present, err := windowsProcessExists(ctx, binary)
		if err != nil {
			return err
		}
		if present {
			return errors.New("isolated Windows service process still exists")
		}
		return nil
	}
	pattern := "^" + regexp.QuoteMeta(binary) + "([[:space:]]|$)"
	command := exec.CommandContext(ctx, "pgrep", "-f", pattern)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	err := command.Run()
	if err == nil {
		return errors.New("profile-local native service process still exists")
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("probe profile-local native process: %w", err)
}

func stopWindowsProcess(ctx context.Context, binary string) error {
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $target='%s'; Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -eq $target } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`, psQuote(binary))
	return runQuiet(ctx, "powershell", "-NoProfile", "-Command", script)
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
