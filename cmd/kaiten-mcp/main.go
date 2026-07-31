package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsuranov/kaiten-mcp/internal/install"
	"github.com/dsuranov/kaiten-mcp/internal/mcp"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			fmt.Printf("kaiten-mcp %s\n", version.String())
			return
		case "--help", "-h":
			fmt.Println("Usage: kaiten-mcp [--transport <stdio|streamable-http>] [--host <bind-host>] [--port <1..65535>] [--streamable-http-path <path>]\n       kaiten-mcp install|uninstall|version")
			return
		case "install":
			if len(args) != 1 {
				fmt.Fprintln(os.Stderr, "error: install accepts no arguments")
				os.Exit(1)
			}
			os.Exit(install.RunInstall(ctx, os.Stdin, os.Stdout, os.Stderr))
		case "uninstall":
			if len(args) != 1 {
				fmt.Fprintln(os.Stderr, "error: uninstall accepts no arguments")
				os.Exit(1)
			}
			os.Exit(install.RunUninstall(ctx, os.Stdin, os.Stdout, os.Stderr))
		}
	}
	os.Exit(mcp.Run(ctx, args, os.Stdin, os.Stdout, os.Stderr))
}
