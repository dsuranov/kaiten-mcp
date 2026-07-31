// Command native-lifecycle-harness runs the native per-user lifecycle gate.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

func main() {
	var config nativeci.Config
	flag.StringVar(&config.V1, "v1", "", "absolute path to the healthy native-v1 kaiten-mcp binary")
	flag.StringVar(&config.V2, "v2", "", "absolute path to the healthy native-v2 kaiten-mcp binary")
	flag.StringVar(&config.V3, "v3", "", "absolute path to the no-health native-v3 fixture")
	flag.StringVar(&config.Profile, "profile", "", "new isolated user profile path")
	flag.StringVar(&config.EvidenceDir, "evidence", "", "evidence artifact directory")
	flag.StringVar(&config.RunnerLabel, "runner-label", "", "exact GitHub-hosted runner label")
	flag.StringVar(&config.Commit, "commit", "", "candidate commit under test")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := nativeci.Run(ctx, config); err != nil {
		fmt.Fprintf(os.Stderr, "native lifecycle gate failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("native lifecycle gate passed; secret-free evidence written")
}
