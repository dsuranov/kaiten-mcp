//go:build ignore

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizerAcceptsReviewedPartialFailureEvidence(t *testing.T) {
	directory := t.TempDir()
	for name, value := range map[string]string{
		"wrapper-context.txt": "runner_label=ubuntu-latest\n",
		"summary.json":        `{"schema":"kaiten-native-lifecycle/v2","result":"failed"}`,
		"commands.json":       "[]\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := sanitizeEvidenceDirectory(directory); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizerRejectsEveryUnreviewedEntryAndCredentialMarker(t *testing.T) {
	for _, test := range []struct {
		name, file, value string
		symlink           bool
	}{
		{name: "unexpected file", file: "extra.txt", value: "safe"},
		{name: "synthetic token", file: "summary.json", value: "native-ci-" + strings.Repeat("a", 64)},
		{name: "authorization header", file: "summary.json", value: "Authorization: Bearer " + strings.Repeat("x", 24)},
		{name: "mock body", file: "summary.json", value: `{"id":4242,"username":"native-lifecycle"}`},
		{name: "symlink", file: "summary.json", value: "safe", symlink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, test.file)
			if test.symlink {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte(test.value), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(test.value), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := sanitizeEvidenceDirectory(directory); err == nil {
				t.Fatal("unsafe evidence was accepted")
			}
		})
	}
}
