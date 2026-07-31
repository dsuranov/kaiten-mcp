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
	"reflect"
	"runtime"
	"sort"
	"strconv"
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

func verifyPermissions(ctx context.Context, paths layout) ([]permissionEvidence, error) {
	checks := []struct {
		role string
		path string
		mode fs.FileMode
	}{
		{"binary", paths.binary, 0o700},
		{"environment", paths.environment, 0o600},
		{"service_definition", paths.definition, 0o600},
		{"claude_code", paths.claudeCode, 0o600},
		{"claude_desktop", paths.claudeDesktop, 0o600},
	}
	result := make([]permissionEvidence, 0, len(checks))
	for _, check := range checks {
		proof, err := verifyFilePermission(ctx, check.role, check.path, check.mode)
		if err != nil {
			return nil, err
		}
		result = append(result, proof)
	}
	return result, nil
}

func verifyBackupPermissions(ctx context.Context, paths layout) ([]permissionEvidence, error) {
	checks := []struct {
		role string
		path string
		mode fs.FileMode
	}{
		{"binary", paths.binary + ".bak", 0o700},
		{"environment", paths.environment + ".bak", 0o600},
		{"service_definition", paths.definition + ".bak", 0o600},
		{"claude_code", paths.claudeCode + ".bak", 0o600},
		{"claude_desktop", paths.claudeDesktop + ".bak", 0o600},
	}
	result := make([]permissionEvidence, 0, len(checks))
	for _, check := range checks {
		proof, err := verifyFilePermission(ctx, check.role, check.path, check.mode)
		if err != nil {
			return nil, fmt.Errorf("backup %s: %w", filepath.Base(check.path), err)
		}
		result = append(result, proof)
	}
	return result, nil
}

func verifyFilePermission(ctx context.Context, role, path string, mode fs.FileMode) (permissionEvidence, error) {
	if runtime.GOOS == "windows" {
		return verifyWindowsACL(ctx, role, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return permissionEvidence{}, err
	}
	if info.Mode().Perm() != mode {
		return permissionEvidence{}, fmt.Errorf("%s mode is %04o, want %04o", role, info.Mode().Perm(), mode)
	}
	owned, err := ownedByCurrentUser(info)
	if err != nil {
		return permissionEvidence{}, err
	}
	if !owned {
		return permissionEvidence{}, fmt.Errorf("%s is not owned by the current unprivileged user", role)
	}
	return permissionEvidence{Role: role, Mode: fmt.Sprintf("%04o", mode), OwnerCurrentUser: true}, nil
}

func ownedByCurrentUser(info os.FileInfo) (bool, error) {
	uid, err := currentUID()
	if err != nil {
		return false, err
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() {
		return false, errors.New("file ownership metadata is unavailable")
	}
	field := value.FieldByName("Uid")
	if !field.IsValid() || !field.CanUint() {
		return false, errors.New("file UID metadata is unavailable")
	}
	return strconv.FormatUint(field.Uint(), 10) == uid, nil
}

func verifyWindowsACL(ctx context.Context, role, path string) (permissionEvidence, error) {
	script := fmt.Sprintf(`$acl=Get-Acl -LiteralPath '%s'; $me=[System.Security.Principal.WindowsIdentity]::GetCurrent().User; $system=New-Object System.Security.Principal.SecurityIdentifier('S-1-5-18'); try{$owner=(New-Object System.Security.Principal.NTAccount($acl.Owner)).Translate([System.Security.Principal.SecurityIdentifier])}catch{exit 1}; $allowMe=$false; $allowSystem=$false; $unexpected=0; foreach($rule in $acl.Access){if($rule.AccessControlType -ne 'Allow'){continue}; try{$sid=$rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier])}catch{$unexpected++; continue}; if($sid -eq $me){$allowMe=$true}elseif($sid -eq $system){$allowSystem=$true}else{$unexpected++}}; [ordered]@{owner_current_user=($owner -eq $me);acl_current_user=$allowMe;acl_system=$allowSystem;unexpected_allow_count=$unexpected}|ConvertTo-Json -Compress`, psQuote(path))
	output, err := combinedOutput(ctx, "powershell", "-NoProfile", "-Command", script)
	if err != nil {
		return permissionEvidence{}, err
	}
	proof := permissionEvidence{Role: role}
	if err := json.Unmarshal([]byte(output), &proof); err != nil {
		return permissionEvidence{}, err
	}
	proof.Role = role
	if !proof.OwnerCurrentUser || !proof.ACLCurrentUser || !proof.ACLSystem || proof.UnexpectedAllowCount != 0 {
		return permissionEvidence{}, errors.New("ACL is not restricted to the current user and SYSTEM")
	}
	return proof, nil
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
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link remains in lifecycle profile: %s", filepath.Base(path))
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
