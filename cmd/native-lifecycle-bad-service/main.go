// Command native-lifecycle-bad-service is a deliberate no-health fixture. It
// runs the real installer, reports normal build version metadata, and then
// stays alive without opening a listener when activated by a service manager.
// The hosted lifecycle gate uses it only as the v3 update rollback stimulus.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsuranov/kaiten-mcp/internal/install"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "version", "--version":
			fmt.Printf("kaiten-mcp %s\n", version.String())
			return
		case "install":
			os.Exit(install.RunInstall(ctx, os.Stdin, os.Stdout, os.Stderr))
		case "uninstall":
			os.Exit(install.RunUninstall(ctx, os.Stdin, os.Stdout, os.Stderr))
		}
	}
	<-ctx.Done()
}
