package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/dsuranov/kaiten-mcp/internal/config"
)

// Run validates runtime flags and configuration, then serves until completion
// or cancellation. It returns a process-compatible status code.
func Run(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
	overrides, err := parseRuntimeFlags(args)
	if err != nil {
		fmt.Fprintf(diagnostics, "error: %v\n", err)
		return 1
	}
	cfg, err := config.Load(true, overrides)
	if err != nil {
		fmt.Fprintf(diagnostics, "error: %v\n", err)
		return 1
	}
	server := NewServer(cfg)
	if cfg.MCPTransport == "stdio" {
		if err := server.ServeStdio(ctx, input, output); err != nil {
			fmt.Fprintf(diagnostics, "error: MCP stdio stopped: %v\n", err)
			return 1
		}
		return 0
	}
	if !loopbackHost(cfg.MCPHost) {
		fmt.Fprintln(diagnostics, "warning: MCP is binding beyond loopback; protect the endpoint because this process holds a Kaiten bearer credential")
	}
	if err := server.ServeHTTP(ctx, cfg.MCPHost, cfg.MCPPort, cfg.MCPPath); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(diagnostics, "error: MCP HTTP stopped: %v\n", err)
		return 1
	}
	return 0
}

func parseRuntimeFlags(args []string) (config.Overrides, error) {
	var overrides config.Overrides
	seen := make(map[string]bool)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--help" || argument == "-h" {
			return overrides, errors.New("use kaiten mcp --help to view server options")
		}
		if !strings.HasPrefix(argument, "--") {
			return overrides, fmt.Errorf("unexpected MCP argument %q", argument)
		}
		nameValue := strings.TrimPrefix(argument, "--")
		name, value, inline := strings.Cut(nameValue, "=")
		if seen[name] {
			return overrides, fmt.Errorf("--%s may be supplied only once", name)
		}
		seen[name] = true
		if !inline {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				return overrides, fmt.Errorf("--%s requires a value", name)
			}
			index++
			value = args[index]
		}
		switch name {
		case "transport":
			if value != "stdio" && value != "streamable-http" {
				return overrides, errors.New("--transport must be stdio or streamable-http")
			}
			overrides.Transport = value
		case "host":
			if strings.TrimSpace(value) == "" {
				return overrides, errors.New("--host must not be empty")
			}
			overrides.Host = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil || port < 0 || port > 65535 {
				return overrides, errors.New("--port must be from 0 through 65535")
			}
			overrides.Port = port
		case "streamable-http-path":
			if strings.TrimSpace(value) == "" {
				return overrides, errors.New("--streamable-http-path must not be empty")
			}
			overrides.Path = value
		default:
			return overrides, fmt.Errorf("unknown MCP flag --%s", name)
		}
	}
	return overrides, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
