package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsuranov/kaiten-mcp/internal/cli"
	"github.com/dsuranov/kaiten-mcp/internal/install"
	"github.com/dsuranov/kaiten-mcp/internal/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.RunStandaloneMCP(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.Dependencies{
		MCPRun: mcp.Run, MCPInstall: install.RunInstall, MCPUninstall: install.RunUninstall,
	}))
}
