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
	if info, err := os.Stat(path); err == nil {
		record.hadBefore = true
		record.mode = info.Mode().Perm()
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
	if err := os.Rename(temporaryPath, path); err == nil {
		return os.Chmod(path, mode)
	}
	// Windows cannot replace an existing file with Rename. A backup has already
	// been made by transaction.replace before this fallback is used.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func mergeClientConfig(path, endpoint string, remove bool) error {
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("existing client configuration is invalid JSON: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) && remove {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
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
