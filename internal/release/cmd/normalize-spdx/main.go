package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/release/spdxnormalize"
)

func main() {
	input := flag.String("input", "", "input SPDX JSON document")
	output := flag.String("output", "", "normalized SPDX JSON document")
	artifact := flag.String("artifact", "", "release artifact described by the document")
	createdText := flag.String("created", "", "commit time in RFC 3339 format")
	check := flag.Bool("check", false, "validate an already normalized document")
	flag.Parse()
	if flag.NArg() != 0 || *input == "" || *artifact == "" || *createdText == "" {
		fail("-input, -artifact, and -created are required and positional arguments are not accepted")
	}
	created, err := time.Parse(time.RFC3339, *createdText)
	if err != nil {
		fail("parse -created: %v", err)
	}
	if *check {
		if *output != "" {
			fail("-output cannot be used with -check")
		}
		if err := spdxnormalize.Validate(*input, *artifact, created); err != nil {
			fail("%v", err)
		}
		fmt.Printf("validated reproducible SPDX: %s\n", *input)
		return
	}
	if *output == "" {
		fail("-output is required unless -check is used")
	}
	if err := spdxnormalize.Normalize(*input, *output, *artifact, created); err != nil {
		fail("%v", err)
	}
	fmt.Printf("normalized reproducible SPDX: %s\n", *output)
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "normalize-spdx: "+format+"\n", arguments...)
	os.Exit(1)
}
