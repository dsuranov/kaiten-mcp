package archivecanonical

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestNormalizeProducesIdenticalTarGzipBytes(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first := filepath.Join(directory, "first.tar.gz")
	second := filepath.Join(directory, "second.tar.gz")
	entries := releaseEntries()
	writeTestTarGzip(t, first, entries, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	reversed := slices.Clone(entries)
	slices.Reverse(reversed)
	writeTestTarGzip(t, second, reversed, time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC))
	modified := time.Date(2026, 7, 31, 14, 1, 42, 0, time.UTC)
	if err := Normalize(first, modified); err != nil {
		t.Fatal(err)
	}
	if err := Normalize(second, modified); err != nil {
		t.Fatal(err)
	}
	assertFilesEqual(t, first, second)
	assertCanonicalTar(t, first, modified, len(entries))
}

func TestNormalizeProducesIdenticalZIPBytes(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	first := filepath.Join(directory, "first.zip")
	second := filepath.Join(directory, "second.zip")
	entries := releaseEntries()
	writeTestZIP(t, first, entries, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	reversed := slices.Clone(entries)
	slices.Reverse(reversed)
	writeTestZIP(t, second, reversed, time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC))
	modified := time.Date(2026, 7, 31, 14, 1, 42, 0, time.UTC)
	if err := Normalize(first, modified); err != nil {
		t.Fatal(err)
	}
	if err := Normalize(second, modified); err != nil {
		t.Fatal(err)
	}
	assertFilesEqual(t, first, second)
	assertCanonicalZIP(t, first, modified, len(entries))
}

func TestNormalizeRejectsLinks(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "link.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "release/kaiten", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Normalize(archivePath, time.Now()); err == nil {
		t.Fatal("expected link entry to be rejected")
	}
}

func releaseEntries() []entry {
	return []entry{
		{name: "release/LICENSE", data: []byte("license")},
		{name: "release/NOTICE", data: []byte("notice")},
		{name: "release/THIRD_PARTY_NOTICES.md", data: []byte("third party")},
		{name: "release/README.md", data: []byte("readme")},
		{name: "release/PROVENANCE.md", data: []byte("provenance")},
		{name: "release/docs/usage.md", data: []byte("usage")},
		{name: "release/kaiten", data: []byte("first executable")},
		{name: "release/kaiten-mcp", data: []byte("second executable")},
	}
}

func writeTestTarGzip(t *testing.T, archivePath string, entries []entry, modified time.Time) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	compressed.Header.ModTime = modified
	archive := tar.NewWriter(compressed)
	for _, value := range entries {
		header := &tar.Header{
			Name:    value.name,
			Mode:    0o600,
			Size:    int64(len(value.data)),
			ModTime: modified,
			Uname:   "different-user",
			Gname:   "different-group",
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(value.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestZIP(t *testing.T, archivePath string, entries []entry, modified time.Time) {
	t.Helper()
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for _, value := range entries {
		header := &zip.FileHeader{Name: value.name, Method: zip.Store, Modified: modified}
		header.SetMode(0o600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(value.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertCanonicalTar(t *testing.T, archivePath string, modified time.Time, expectedEntries int) {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	previous := ""
	count := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && header.Name <= previous {
			t.Fatalf("tar entries are not strictly sorted: %q then %q", previous, header.Name)
		}
		previous = header.Name
		if !header.ModTime.Equal(modified) || header.Uid != 0 || header.Gid != 0 || header.Uname != "root" || header.Gname != "root" {
			t.Fatalf("non-canonical tar metadata for %q: %#v", header.Name, header)
		}
		if got, want := header.Mode, int64(canonicalMode(header.Name)); got != want {
			t.Fatalf("mode for %q = %o, want %o", header.Name, got, want)
		}
		count++
	}
	if count != expectedEntries {
		t.Fatalf("tar entry count = %d, want %d", count, expectedEntries)
	}
}

func assertCanonicalZIP(t *testing.T, archivePath string, modified time.Time, expectedEntries int) {
	t.Helper()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != expectedEntries {
		t.Fatalf("zip entry count = %d, want %d", len(archive.File), expectedEntries)
	}
	previous := ""
	for _, file := range archive.File {
		if previous != "" && file.Name <= previous {
			t.Fatalf("zip entries are not strictly sorted: %q then %q", previous, file.Name)
		}
		previous = file.Name
		if !file.Modified.Equal(modified) {
			t.Fatalf("modified time for %q = %s, want %s", file.Name, file.Modified, modified)
		}
		if got, want := file.Mode().Perm(), os.FileMode(canonicalMode(file.Name)); got != want {
			t.Fatalf("mode for %q = %o, want %o", file.Name, got, want)
		}
	}
}

func assertFilesEqual(t *testing.T, first, second string) {
	t.Helper()
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstData) != string(secondData) {
		t.Fatal("canonical archives differ")
	}
}
