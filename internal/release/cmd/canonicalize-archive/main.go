package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/release/archivecanonical"
)

func main() {
	archive := flag.String("archive", "", "release archive to rewrite atomically")
	modifiedText := flag.String("modified", "", "source commit time in RFC 3339 format")
	flag.Parse()
	if flag.NArg() != 0 || *archive == "" || *modifiedText == "" {
		fail("-archive and -modified are required and positional arguments are not accepted")
	}
	modified, err := time.Parse(time.RFC3339, *modifiedText)
	if err != nil {
		fail("parse -modified: %v", err)
	}
	if err := archivecanonical.Normalize(*archive, modified); err != nil {
		fail("%v", err)
	}
	fmt.Printf("canonicalized release archive: %s\n", *archive)
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "canonicalize-archive: "+format+"\n", arguments...)
	os.Exit(1)
}
