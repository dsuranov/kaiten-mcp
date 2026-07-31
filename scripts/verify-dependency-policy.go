//go:build ignore

// Command verify-dependency-policy keeps the module graph, provenance record,
// and third-party notices in lockstep.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type module struct {
	Path    string
	Version string
	Replace *module
}

func main() {
	expected := map[string]string{
		"github.com/dsuranov/kaiten-mcp": "",
		"golang.org/x/sys":               "v0.47.0",
		"golang.org/x/term":              "v0.45.0",
	}
	command := exec.Command("go", "list", "-m", "-json", "all")
	output, err := command.Output()
	check(err)
	actual, err := decodeModules(strings.NewReader(string(output)))
	check(err)
	if !sameModules(actual, expected) {
		fail("module graph = %v, reviewed graph = %v; update licenses, notices, provenance, and policy together", formatModules(actual), formatModules(expected))
	}

	provenance := read("PROVENANCE.md")
	notices := read("THIRD_PARTY_NOTICES.md")
	for path, version := range expected {
		if version == "" {
			continue
		}
		if !strings.Contains(provenance, "`"+path+"` "+version) {
			fail("PROVENANCE.md does not record %s %s", path, version)
		}
		if !strings.Contains(notices, "`"+path+"` "+version) {
			fail("THIRD_PARTY_NOTICES.md does not record %s %s", path, version)
		}
	}
	if !strings.Contains(notices, "BSD 3-Clause") {
		fail("THIRD_PARTY_NOTICES.md does not identify the reviewed dependency license")
	}
	fmt.Printf("verified dependency policy: %s\n", strings.Join(formatModules(actual), ", "))
}

func decodeModules(reader io.Reader) (map[string]string, error) {
	decoder := json.NewDecoder(reader)
	actual := make(map[string]string)
	for {
		var item module
		if err := decoder.Decode(&item); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if item.Path == "" {
			return nil, fmt.Errorf("module graph contains an empty module path")
		}
		if item.Replace != nil {
			return nil, fmt.Errorf("module %s uses forbidden replacement %s", formatModule(item.Path, item.Version), formatModule(item.Replace.Path, item.Replace.Version))
		}
		if _, duplicate := actual[item.Path]; duplicate {
			return nil, fmt.Errorf("duplicate module %s", item.Path)
		}
		actual[item.Path] = item.Version
	}
	return actual, nil
}

func sameModules(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}
	for path, version := range first {
		otherVersion, exists := second[path]
		if !exists || otherVersion != version {
			return false
		}
	}
	return true
}

func formatModules(modules map[string]string) []string {
	result := make([]string, 0, len(modules))
	for path, version := range modules {
		if version != "" {
			path += "@" + version
		}
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func formatModule(path, version string) string {
	if path == "" {
		return "<empty>"
	}
	if version != "" {
		return path + "@" + version
	}
	return path
}

func read(path string) string {
	data, err := os.ReadFile(path)
	check(err)
	return string(data)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", arguments...)
	os.Exit(1)
}
