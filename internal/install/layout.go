// Package install manages the cross-platform per-user MCP service lifecycle.
package install

import (
	"errors"
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	servicePort = 8100
	servicePath = "/mcp"
	serviceName = "kaiten-mcp"
)

// Layout contains only paths owned or optionally edited by this installer.
type Layout struct {
	GOOS                string
	Root                string
	Binary              string
	Environment         string
	Log                 string
	ServiceDefinition   string
	ClaudeCodeConfig    string
	ClaudeDesktopConfig string
}

func layoutFor(goos, home string) (Layout, error) {
	if strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return Layout{}, errors.New("user home must be an absolute path")
	}
	switch goos {
	case "darwin":
		root := filepath.Join(home, "Library", "Application Support", serviceName)
		return Layout{
			GOOS: goos, Root: root, Binary: filepath.Join(root, "bin", serviceName), Environment: filepath.Join(root, ".env"),
			Log: filepath.Join(root, "logs", "service.log"), ServiceDefinition: filepath.Join(home, "Library", "LaunchAgents", "io.github.dsuranov.kaiten-mcp.plist"),
			ClaudeCodeConfig: filepath.Join(home, ".claude.json"), ClaudeDesktopConfig: filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
		}, nil
	case "linux":
		root := filepath.Join(home, ".local", "share", serviceName)
		return Layout{
			GOOS: goos, Root: root, Binary: filepath.Join(root, "bin", serviceName), Environment: filepath.Join(root, ".env"),
			Log: filepath.Join(home, ".local", "state", serviceName, "service.log"), ServiceDefinition: filepath.Join(home, ".config", "systemd", "user", serviceName+".service"),
			ClaudeCodeConfig: filepath.Join(home, ".claude.json"), ClaudeDesktopConfig: filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
		}, nil
	case "windows":
		root := filepath.Join(home, "AppData", "Local", "KaitenMCP")
		return Layout{
			GOOS: goos, Root: root, Binary: filepath.Join(root, "bin", serviceName+".exe"), Environment: filepath.Join(root, ".env"),
			Log: filepath.Join(root, "logs", "service.log"), ServiceDefinition: filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", serviceName+".cmd"),
			ClaudeCodeConfig: filepath.Join(home, ".claude.json"), ClaudeDesktopConfig: filepath.Join(home, "AppData", "Roaming", "Claude", "claude_desktop_config.json"),
		}, nil
	default:
		return Layout{}, fmt.Errorf("unsupported operating system %q", goos)
	}
}

func serviceDefinition(layout Layout) []byte {
	switch layout.GOOS {
	case "darwin":
		return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.github.dsuranov.kaiten-mcp</string>
  <key>ProgramArguments</key><array>
    <string>%s</string><string>--transport</string><string>streamable-http</string>
    <string>--host</string><string>127.0.0.1</string><string>--port</string><string>8100</string>
    <string>--streamable-http-path</string><string>/mcp</string>
  </array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, html.EscapeString(layout.Binary), html.EscapeString(layout.Root), html.EscapeString(layout.Log), html.EscapeString(layout.Log)))
	case "linux":
		return []byte(fmt.Sprintf(`[Unit]
Description=Kaiten MCP per-user service
After=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s --transport streamable-http --host 127.0.0.1 --port 8100 --streamable-http-path /mcp
Restart=on-failure
RestartSec=3
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, strconv.Quote(layout.Root), strconv.Quote(layout.Binary), layout.Log, layout.Log))
	case "windows":
		return []byte(fmt.Sprintf("@echo off\r\ncd /d %s\r\nstart \"\" /b %s --transport streamable-http --host 127.0.0.1 --port 8100 --streamable-http-path /mcp >> %s 2>&1\r\n", windowsQuote(layout.Root), windowsQuote(layout.Binary), windowsQuote(layout.Log)))
	default:
		return nil
	}
}

func windowsQuote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func environmentFile(tenantURL, token string, writeEnabled bool) []byte {
	return []byte(fmt.Sprintf("KAITEN_URL=%s\nKAITEN_API_TOKEN=%s\nKAITEN_MCP_TRANSPORT=streamable-http\nKAITEN_MCP_HOST=127.0.0.1\nKAITEN_MCP_PORT=8100\nKAITEN_MCP_STREAMABLE_HTTP_PATH=/mcp\nKAITEN_ENABLE_WRITE_TOOLS=%t\n", tenantURL, token, writeEnabled))
}
