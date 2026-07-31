package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
		case "install", "uninstall":
			fmt.Fprintln(os.Stderr, "error: installer lifecycle is unavailable in this build")
			os.Exit(1)
		}
	}
	os.Exit(mcp.Run(ctx, args, os.Stdin, os.Stdout, os.Stderr))
}
