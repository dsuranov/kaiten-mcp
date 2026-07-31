package nativeci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func seedClientConfigs(paths layout) error {
	value := map[string]any{
		"unrelated": map[string]any{"preserve": true},
		"mcpServers": map[string]any{
			"other": map[string]any{"type": "http", "url": "http://127.0.0.1:65534/mcp"},
		},
	}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	encoded = append(encoded, '\n')
	for _, path := range []string{paths.claudeCode, paths.claudeDesktop} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func verifyClientConfigs(paths layout, registered bool) error {
	for _, path := range []string{paths.claudeCode, paths.claudeDesktop} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			return err
		}
		unrelated, _ := root["unrelated"].(map[string]any)
		if unrelated["preserve"] != true {
			return fmt.Errorf("unrelated client key was not preserved in %s", filepath.Base(path))
		}
		servers, _ := root["mcpServers"].(map[string]any)
		other, _ := servers["other"].(map[string]any)
		if other["url"] != "http://127.0.0.1:65534/mcp" {
			return fmt.Errorf("unrelated MCP server was not preserved in %s", filepath.Base(path))
		}
		kaiten, exists := servers["kaiten"]
		if registered {
			entry, _ := kaiten.(map[string]any)
			if !exists || entry["url"] != "http://127.0.0.1:8100/mcp" {
				return fmt.Errorf("kaiten registration missing from %s", filepath.Base(path))
			}
		} else if exists {
			return fmt.Errorf("kaiten registration remains in %s", filepath.Base(path))
		}
	}
	return nil
}

func verifyPermissions(ctx context.Context, paths layout) ([]string, error) {
	checks := []struct {
		role string
		path string
		mode fs.FileMode
	}{
		{"installed executable", paths.binary, 0o700},
		{"secret environment", paths.environment, 0o600},
		{"service definition", paths.definition, 0o600},
		{"Claude Code configuration", paths.claudeCode, 0o600},
		{"Claude Desktop configuration", paths.claudeDesktop, 0o600},
	}
	result := make([]string, 0, len(checks))
	for _, check := range checks {
		if runtime.GOOS == "windows" {
			if err := verifyWindowsACL(ctx, check.path); err != nil {
				return nil, fmt.Errorf("%s ACL: %w", check.role, err)
			}
			result = append(result, check.role+":owner-and-SYSTEM-only")
			continue
		}
		info, err := os.Stat(check.path)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm() != check.mode {
			return nil, fmt.Errorf("%s mode is %04o, want %04o", check.role, info.Mode().Perm(), check.mode)
		}
		result = append(result, fmt.Sprintf("%s:%04o", check.role, check.mode))
	}
	return result, nil
}

func verifyBackupPermissions(ctx context.Context, paths layout) error {
	checks := []struct {
		path string
		mode fs.FileMode
	}{
		{paths.binary + ".bak", 0o700},
		{paths.environment + ".bak", 0o600},
		{paths.definition + ".bak", 0o600},
		{paths.claudeCode + ".bak", 0o600},
		{paths.claudeDesktop + ".bak", 0o600},
	}
	for _, check := range checks {
		if runtime.GOOS == "windows" {
			if err := verifyWindowsACL(ctx, check.path); err != nil {
				return err
			}
			continue
		}
		info, err := os.Stat(check.path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != check.mode {
			return fmt.Errorf("backup %s mode is %04o, want %04o", filepath.Base(check.path), info.Mode().Perm(), check.mode)
		}
	}
	return nil
}

func verifyWindowsACL(ctx context.Context, path string) error {
	script := fmt.Sprintf(`$broad=@('S-1-1-0','S-1-5-11','S-1-5-32-545'); $bad=@(); foreach($rule in (Get-Acl -LiteralPath '%s').Access){try{$sid=$rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value}catch{continue}; if($broad -contains $sid -and $rule.AccessControlType -eq 'Allow'){$bad += $sid}}; if($bad.Count -ne 0){exit 1}`, psQuote(path))
	if err := runQuiet(ctx, "powershell", "-NoProfile", "-Command", script); err != nil {
		return errors.New("broad read principal is allowed")
	}
	return nil
}

func scanForToken(root, token string, allowed map[string]bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || allowed[filepath.Clean(path)] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(token)) {
			return fmt.Errorf("synthetic token escaped into %s", filepath.Base(path))
		}
		return nil
	})
}

func verifyOwnedFilesRemoved(paths layout) error {
	for _, owned := range []string{paths.binary, paths.environment, paths.definition} {
		for _, candidate := range []string{owned, owned + ".bak", owned + ".replace-old"} {
			if fileExists(candidate) {
				return fmt.Errorf("owned file remains after uninstall: %s", filepath.Base(candidate))
			}
		}
	}
	if !fileExists(paths.log) {
		return errors.New("preserved service log is missing")
	}
	return nil
}

func remainingRegularFiles(profile string) ([]string, error) {
	var result []string
	err := filepath.WalkDir(profile, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(profile, path)
			if err != nil {
				return err
			}
			result = append(result, filepath.ToSlash(relative))
		}
		return nil
	})
	sort.Strings(result)
	return result, err
}

func verifyOnlyExpectedFilesRemain(profile string, paths layout) ([]string, error) {
	remaining, err := remainingRegularFiles(profile)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, path := range []string{paths.log, paths.claudeCode, paths.claudeCode + ".bak", paths.claudeDesktop, paths.claudeDesktop + ".bak"} {
		relative, _ := filepath.Rel(profile, path)
		wanted[filepath.ToSlash(relative)] = true
	}
	for _, path := range remaining {
		if !wanted[path] {
			return nil, fmt.Errorf("unexpected regular file remains: %s", path)
		}
	}
	if len(remaining) != len(wanted) {
		return nil, fmt.Errorf("remaining file inventory = %s", strings.Join(remaining, ", "))
	}
	return remaining, nil
}
