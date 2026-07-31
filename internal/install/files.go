package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type replacement struct {
	path      string
	backup    string
	hadBefore bool
	mode      fs.FileMode
}

type transaction struct{ replacements []replacement }

func (t *transaction) replace(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	record := replacement{path: path, backup: path + ".bak", mode: mode}
	if _, err := os.Stat(path); err == nil {
		record.hadBefore = true
		prior, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeAtomic(record.backup, prior, record.mode); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeAtomic(path, data, mode); err != nil {
		return err
	}
	t.replacements = append(t.replacements, record)
	return nil
}

func (t *transaction) rollback() error {
	var failures []error
	for index := len(t.replacements) - 1; index >= 0; index-- {
		record := t.replacements[index]
		if record.hadBefore {
			data, err := os.ReadFile(record.backup)
			if err == nil {
				err = writeAtomic(record.path, data, record.mode)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("restore %s: %w", record.path, err))
			}
		} else if err := os.Remove(record.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fmt.Errorf("remove %s: %w", record.path, err))
		}
	}
	return errors.Join(failures...)
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	return writeAtomicWithRename(path, data, mode, os.Rename)
}

func writeAtomicWithRename(path string, data []byte, mode fs.FileMode, rename func(string, string) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".kaiten-mcp-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	firstRenameErr := rename(temporaryPath, path)
	if firstRenameErr == nil {
		return os.Chmod(path, mode)
	}
	if _, err := os.Stat(path); err != nil {
		return firstRenameErr
	}
	displacedPath := path + ".replace-old"
	if err := os.Remove(displacedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(path, displacedPath); err != nil {
		return fmt.Errorf("prepare existing destination for replacement: %w", err)
	}
	restoreDisplaced := func(cause error) error {
		if restoreErr := rename(displacedPath, path); restoreErr != nil {
			return errors.Join(cause, fmt.Errorf("restore destination after failed replacement: %w", restoreErr))
		}
		return cause
	}
	if err := os.Chmod(displacedPath, mode); err != nil {
		return restoreDisplaced(fmt.Errorf("restrict displaced destination: %w", err))
	}
	if err := rename(temporaryPath, path); err != nil {
		return restoreDisplaced(fmt.Errorf("replace destination: %w", err))
	}
	if err := os.Chmod(path, mode); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return errors.Join(err, fmt.Errorf("remove failed replacement: %w", removeErr))
		}
		return restoreDisplaced(err)
	}
	// The new destination is already complete and has the requested mode. A
	// best-effort deletion avoids turning an antivirus-held displaced file into
	// an untracked failed write; any retained displaced file is restrictive.
	_ = os.Remove(displacedPath)
	return nil
}

func mergeClientConfig(path, endpoint string, remove bool) error {
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("existing client configuration is invalid JSON: %w", err)
		}
		if root == nil {
			return errors.New("existing client configuration must be a JSON object")
		}
	} else if errors.Is(err, os.ErrNotExist) && remove {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	servers := map[string]any(nil)
	if raw, exists := root["mcpServers"]; exists {
		var ok bool
		servers, ok = raw.(map[string]any)
		if !ok {
			return errors.New("existing client configuration field mcpServers must be a JSON object")
		}
	} else {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	if remove {
		delete(servers, "kaiten")
	} else {
		servers["kaiten"] = map[string]any{"type": "http", "url": endpoint}
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	transaction := &transaction{}
	return transaction.replace(path, encoded, 0o600)
}
