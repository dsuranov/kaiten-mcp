//go:build ignore

// Command sanitize-native-lifecycle-evidence is the upload boundary for one
// hosted runner. It accepts partial failure evidence, but only reviewed regular
// files whose complete contents pass the credential and mock-body scan.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dsuranov/kaiten-mcp/internal/nativeci"
)

var (
	syntheticNativeToken = regexp.MustCompile(`native-ci-[0-9a-f]{64}`)
	bearerMaterial       = regexp.MustCompile(`(?i)bearer[[:space:]]+[A-Za-z0-9._~+/-]{16,}`)
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run ./scripts/sanitize-native-lifecycle-evidence.go <runner-evidence-directory>")
	}
	if err := sanitizeEvidenceDirectory(os.Args[1]); err != nil {
		fail("%v", err)
	}
	fmt.Println("sanitized native lifecycle evidence for upload")
}

func sanitizeEvidenceDirectory(directory string) error {
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("evidence root must be a real directory: %s", root)
	}
	allowed := map[string]bool{
		"summary.json": true, "wrapper-context.txt": true, "linux-wrapper-cleanup.json": true,
	}
	for _, name := range nativeci.RequiredEvidenceArtifacts() {
		allowed[name] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("evidence directory is empty")
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("unexpected evidence entry %s", entry.Name())
		}
		path := filepath.Join(root, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Size() > 8*1024*1024 {
			return fmt.Errorf("unsafe evidence file type or size: %s", entry.Name())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := sanitizePayload(data); err != nil {
			return fmt.Errorf("unsafe evidence payload %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func sanitizePayload(data []byte) error {
	value := string(data)
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(strings.ToLower(value))
	if syntheticNativeToken.Match(data) || bearerMaterial.Match(data) || strings.Contains(compact, `"authorization":`) || strings.Contains(compact, `authorization:bearer`) || strings.Contains(compact, `"username":"native-lifecycle"`) || strings.Contains(compact, `"id":4242`) {
		return errors.New("credential material or mock response content found")
	}
	return nil
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", arguments...)
	os.Exit(1)
}
