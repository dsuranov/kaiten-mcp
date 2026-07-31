// Package archivecanonical rewrites release archives with stable entry order
// and metadata while preserving every regular-file payload.
package archivecanonical

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxArchiveBytes = 256 << 20
	maxEntries      = 4096
)

type entry struct {
	name string
	data []byte
}

// Normalize atomically rewrites a .tar.gz or .zip release archive using the
// supplied source-commit time. Only regular files are accepted.
func Normalize(archivePath string, modified time.Time) error {
	var (
		entries []entry
		err     error
		format  string
	)
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"):
		entries, err = readTarGzip(archivePath)
		format = "tar.gz"
	case strings.HasSuffix(archivePath, ".zip"):
		entries, err = readZIP(archivePath)
		format = "zip"
	default:
		return fmt.Errorf("unsupported release archive %q", archivePath)
	}
	if err != nil {
		return err
	}
	if err := validateEntries(entries); err != nil {
		return err
	}
	sort.Slice(entries, func(first, second int) bool {
		return entries[first].name < entries[second].name
	})

	switch format {
	case "tar.gz":
		return rewriteAtomically(archivePath, func(output io.Writer) error {
			return writeTarGzip(output, entries, modified)
		})
	case "zip":
		return rewriteAtomically(archivePath, func(output io.Writer) error {
			return writeZIP(output, entries, modified)
		})
	default:
		panic("unreachable archive format")
	}
}

func readTarGzip(archivePath string) ([]entry, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open tar archive: %w", err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var entries []entry
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("tar entry %q is not a regular file", header.Name)
		}
		data, err := readEntry(reader, header.Size, &total)
		if err != nil {
			return nil, fmt.Errorf("read tar entry %q: %w", header.Name, err)
		}
		entries = append(entries, entry{name: header.Name, data: data})
		if len(entries) > maxEntries {
			return nil, fmt.Errorf("archive exceeds %d entries", maxEntries)
		}
	}
	return entries, nil
}

func readZIP(archivePath string) ([]entry, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > maxEntries {
		return nil, fmt.Errorf("archive exceeds %d entries", maxEntries)
	}
	var entries []entry
	var total int64
	for _, file := range reader.File {
		if !file.Mode().IsRegular() {
			return nil, fmt.Errorf("zip entry %q is not a regular file", file.Name)
		}
		input, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip entry %q: %w", file.Name, err)
		}
		data, readErr := readEntry(input, int64(file.UncompressedSize64), &total)
		closeErr := input.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read zip entry %q: %w", file.Name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close zip entry %q: %w", file.Name, closeErr)
		}
		entries = append(entries, entry{name: file.Name, data: data})
	}
	return entries, nil
}

func readEntry(reader io.Reader, declaredSize int64, total *int64) ([]byte, error) {
	if declaredSize < 0 || declaredSize > maxArchiveBytes || *total > maxArchiveBytes-declaredSize {
		return nil, fmt.Errorf("archive expands beyond %d bytes", maxArchiveBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, declaredSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != declaredSize {
		return nil, fmt.Errorf("declared size %d does not match content size %d", declaredSize, len(data))
	}
	*total += declaredSize
	return data, nil
}

func validateEntries(entries []entry) error {
	if len(entries) == 0 {
		return errors.New("release archive is empty")
	}
	seen := make(map[string]struct{}, len(entries))
	root := ""
	legalFiles := map[string]bool{
		"LICENSE":                false,
		"NOTICE":                 false,
		"THIRD_PARTY_NOTICES.md": false,
		"README.md":              false,
		"PROVENANCE.md":          false,
	}
	executableFound := false
	for _, entry := range entries {
		if entry.name == "" || strings.Contains(entry.name, "\\") || path.IsAbs(entry.name) || path.Clean(entry.name) != entry.name || strings.HasPrefix(entry.name, "../") {
			return fmt.Errorf("unsafe archive entry name %q", entry.name)
		}
		parts := strings.Split(entry.name, "/")
		if len(parts) < 2 || parts[0] == "" {
			return fmt.Errorf("archive entry %q is not inside one package directory", entry.name)
		}
		if root == "" {
			root = parts[0]
		} else if parts[0] != root {
			return fmt.Errorf("archive entry %q is outside package root %q", entry.name, root)
		}
		if _, duplicate := seen[entry.name]; duplicate {
			return fmt.Errorf("duplicate archive entry %q", entry.name)
		}
		seen[entry.name] = struct{}{}
		relative := strings.Join(parts[1:], "/")
		if _, required := legalFiles[relative]; required {
			legalFiles[relative] = true
		}
		if isExecutable(entry.name) {
			executableFound = true
		}
	}
	for name, found := range legalFiles {
		if !found {
			return fmt.Errorf("release archive lacks %s", name)
		}
	}
	if !executableFound {
		return errors.New("release archive contains no executable")
	}
	return nil
}

func writeTarGzip(output io.Writer, entries []entry, modified time.Time) error {
	compressed, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip stream: %w", err)
	}
	compressed.Header.ModTime = time.Time{}
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := &tar.Header{
			Name:       entry.name,
			Mode:       int64(canonicalMode(entry.name)),
			Size:       int64(len(entry.data)),
			ModTime:    modified.UTC(),
			Typeflag:   tar.TypeReg,
			Uid:        0,
			Gid:        0,
			Uname:      "root",
			Gname:      "root",
			Format:     tar.FormatPAX,
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
		}
		if err := archive.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header %q: %w", entry.name, err)
		}
		if _, err := archive.Write(entry.data); err != nil {
			return fmt.Errorf("write tar entry %q: %w", entry.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("close gzip stream: %w", err)
	}
	return nil
}

func writeZIP(output io.Writer, entries []entry, modified time.Time) error {
	archive := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:     entry.name,
			Method:   zip.Deflate,
			Modified: modified.UTC(),
		}
		header.SetMode(os.FileMode(canonicalMode(entry.name)))
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("write zip header %q: %w", entry.name, err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			return fmt.Errorf("write zip entry %q: %w", entry.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close zip archive: %w", err)
	}
	return nil
}

func canonicalMode(name string) int {
	if isExecutable(name) {
		return 0o755
	}
	return 0o644
}

func isExecutable(name string) bool {
	switch path.Base(name) {
	case "kaiten", "kaiten.exe", "kaiten-mcp", "kaiten-mcp.exe":
		return true
	default:
		return false
	}
}

func rewriteAtomically(archivePath string, write func(io.Writer) error) error {
	directory := filepath.Dir(archivePath)
	temporary, err := os.CreateTemp(directory, ".canonical-archive-*")
	if err != nil {
		return fmt.Errorf("create canonical archive: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := write(temporary); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync canonical archive: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set canonical archive mode: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close canonical archive: %w", err)
	}
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		return fmt.Errorf("install canonical archive: %w", err)
	}
	keep = true
	return nil
}
