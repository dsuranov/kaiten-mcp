//go:build ignore

package main

import (
	"strings"
	"testing"
)

func TestDecodeModulesAcceptsUnreplacedGraph(t *testing.T) {
	t.Parallel()
	fixture := `
{"Path":"github.com/dsuranov/kaiten-mcp","Main":true}
{"Path":"golang.org/x/sys","Version":"v0.47.0"}
{"Path":"golang.org/x/term","Version":"v0.45.0"}
`
	modules, err := decodeModules(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("decodeModules() error = %v", err)
	}
	want := map[string]string{
		"github.com/dsuranov/kaiten-mcp": "",
		"golang.org/x/sys":               "v0.47.0",
		"golang.org/x/term":              "v0.45.0",
	}
	if !sameModules(modules, want) {
		t.Fatalf("decodeModules() = %v, want %v", modules, want)
	}
}

func TestDecodeModulesRejectsEveryReplacementKind(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		replacement string
	}{
		{name: "remote", replacement: `{"Path":"example.com/xterm-fork","Version":"v1.2.3"}`},
		{name: "local", replacement: `{"Path":"../local/xterm"}`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := `{"Path":"golang.org/x/term","Version":"v0.45.0","Replace":` + test.replacement + `}`
			_, err := decodeModules(strings.NewReader(fixture))
			if err == nil || !strings.Contains(err.Error(), "forbidden replacement") {
				t.Fatalf("decodeModules() error = %v, want forbidden replacement", err)
			}
		})
	}
}

func TestSameModulesRequiresExactPathsForEmptyVersions(t *testing.T) {
	t.Parallel()
	first := map[string]string{"example.com/reviewed": ""}
	second := map[string]string{"example.com/unreviewed": ""}
	if sameModules(first, second) {
		t.Fatal("sameModules() accepted different module paths with empty versions")
	}
}
