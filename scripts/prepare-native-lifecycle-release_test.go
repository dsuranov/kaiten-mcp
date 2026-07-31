//go:build ignore

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const testReleaseVersion = "1.2.3-rc.1"

func TestPrepareBindsAndExtractsExactReleaseBytes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	kaitenFixture, kaitenMCPFixture, expectedSHA := buildReleaseFixtures(t, root)
	archiveName, archiveBytes := buildFullArchive(t, kaitenFixture, kaitenMCPFixture)
	assetsZIP, manifestHash, archiveHash := buildArtifactZIP(t, archiveName, archiveBytes)
	artifactHash := sha256Hex(assetsZIP)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/repos/acme/kaiten-mcp/actions/runs/42":
			writeTestJSON(t, writer, workflowRun{
				ID: 42, Name: "Release", Path: ".github/workflows/release.yml", Event: "push", Status: "completed", Conclusion: "success",
				HeadBranch: "v" + testReleaseVersion, HeadSHA: expectedSHA, RunAttempt: 1,
				Repository: repositoryIdentity{ID: 7, FullName: "acme/kaiten-mcp"}, HeadRepo: repositoryIdentity{ID: 7, FullName: "acme/kaiten-mcp"},
			})
		case "/repos/acme/kaiten-mcp/git/ref/tags/v" + testReleaseVersion:
			writeTestJSON(t, writer, gitReference{Ref: "refs/tags/v" + testReleaseVersion, Object: gitObject{Type: "commit", SHA: expectedSHA}})
		case "/repos/acme/kaiten-mcp/actions/runs/42/artifacts":
			writeTestJSON(t, writer, artifactPage{TotalCount: 2, Artifacts: []artifact{
				{ID: 99, Name: "release-assets", SizeInBytes: int64(len(assetsZIP)), Digest: "sha256:" + artifactHash, WorkflowRun: &struct {
					ID               int64  `json:"id"`
					RepositoryID     int64  `json:"repository_id"`
					HeadRepositoryID int64  `json:"head_repository_id"`
					HeadBranch       string `json:"head_branch"`
					HeadSHA          string `json:"head_sha"`
				}{ID: 42, RepositoryID: 7, HeadRepositoryID: 7, HeadBranch: "v" + testReleaseVersion, HeadSHA: expectedSHA}},
				{ID: 100, Name: "reproducibility-evidence", SizeInBytes: 1, Digest: "sha256:" + strings.Repeat("b", 64)},
			}})
		case "/repos/acme/kaiten-mcp/actions/artifacts/99/zip":
			writer.Header().Set("Content-Type", "application/zip")
			_, _ = writer.Write(assetsZIP)
		default:
			t.Errorf("unexpected API request: %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	opts := options{
		APIURL: server.URL, Repository: "acme/kaiten-mcp", RunID: 42, ExpectedSHA: expectedSHA, Token: "test-token",
		WorkDir: filepath.Join(root, "work"), OutputDir: filepath.Join(root, "output"), MetadataPath: filepath.Join(root, "release-binding.txt"),
	}
	binding, err := prepare(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ArtifactZIPHash != artifactHash || binding.ManifestHash != manifestHash || binding.ArchiveHash != archiveHash {
		t.Fatalf("binding hashes = artifact %s manifest %s archive %s", binding.ArtifactZIPHash, binding.ManifestHash, binding.ArchiveHash)
	}
	if binding.ArchiveName != archiveName || binding.Version != testReleaseVersion || binding.GOOS != runtime.GOOS || binding.GOARCH != runtime.GOARCH {
		t.Fatalf("binding identity = %#v", binding)
	}
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	for name, source := range map[string]string{"kaiten" + extension: kaitenFixture, "kaiten-mcp" + extension: kaitenMCPFixture} {
		got, err := os.ReadFile(filepath.Join(opts.OutputDir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s bytes changed during extraction", name)
		}
	}
	metadata, err := os.ReadFile(opts.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"schema=kaiten-native-release-binding/v1\n",
		"release_run_id=42\n",
		"release_tag=v" + testReleaseVersion + "\n",
		"release_head_sha=" + expectedSHA + "\n",
		"release_artifact_zip_sha256=" + artifactHash + "\n",
		"release_manifest_sha256=" + manifestHash + "\n",
		"release_archive=" + archiveName + "\n",
		"release_archive_sha256=" + archiveHash + "\n",
	} {
		if !bytes.Contains(metadata, []byte(expected)) {
			t.Errorf("metadata is missing %q:\n%s", expected, metadata)
		}
	}
	if bytes.Contains(metadata, []byte("test-token")) {
		t.Fatal("metadata exposed the API token")
	}
}

func TestValidateRunFailsClosed(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	opts := options{Repository: "acme/kaiten-mcp", RunID: 42, ExpectedSHA: sha}
	valid := workflowRun{
		ID: 42, Name: "Release", Path: ".github/workflows/release.yml", Event: "push", Status: "completed", Conclusion: "success",
		HeadBranch: "v1.2.3", HeadSHA: sha, RunAttempt: 1,
		Repository: repositoryIdentity{ID: 7, FullName: opts.Repository}, HeadRepo: repositoryIdentity{ID: 7, FullName: opts.Repository},
	}
	if err := validateRun(valid, opts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".github/workflows/release.yml@v1.2.3", ".github/workflows/release.yml@refs/tags/v1.2.3"} {
		candidate := valid
		candidate.Path = path
		if err := validateRun(candidate, opts); err != nil {
			t.Fatalf("current API workflow path %q was rejected: %v", path, err)
		}
	}
	cases := map[string]func(*workflowRun){
		"repository":      func(run *workflowRun) { run.Repository.FullName = "other/repo" },
		"head repository": func(run *workflowRun) { run.HeadRepo.FullName = "fork/repo" },
		"workflow name":   func(run *workflowRun) { run.Name = "CI" },
		"workflow path":   func(run *workflowRun) { run.Path = ".github/workflows/ci.yml" },
		"event":           func(run *workflowRun) { run.Event = "workflow_dispatch" },
		"status":          func(run *workflowRun) { run.Status = "in_progress" },
		"conclusion":      func(run *workflowRun) { run.Conclusion = "failure" },
		"head SHA":        func(run *workflowRun) { run.HeadSHA = strings.Repeat("b", 40) },
		"ref":             func(run *workflowRun) { run.HeadBranch = "main" },
		"rerun attempt":   func(run *workflowRun) { run.RunAttempt = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateRun(candidate, opts); err == nil {
				t.Fatal("untrusted run was accepted")
			}
		})
	}
}

func TestArchivePathsRejectTraversalLinksAndCaseCollisions(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	if err := validateArchiveEntry("root/file", "root", false, seen); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../root/file", "/root/file", "root/../escape", `root\escape`, "other/file", "root/FILE", "root/file:stream", "root/NUL", "root/trailing."} {
		if err := validateArchiveEntry(name, "root", false, seen); err == nil {
			t.Errorf("unsafe path %q was accepted", name)
		}
	}
	root := t.TempDir()
	archiveName := platformArchiveName(testReleaseVersion)
	archivePath := filepath.Join(root, archiveName)
	writeLinkArchive(t, archivePath, archiveName)
	if err := extractReleaseArchive(archivePath, archiveName, filepath.Join(root, "extract")); err == nil || !strings.Contains(err.Error(), "link or irregular") {
		t.Fatalf("link archive error = %v", err)
	}
}

func TestTarStreamLimitCountsHiddenLocalPAXRecords(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the production Windows archive format is ZIP")
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "pax.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for index := 0; index < 3; index++ {
		contents := []byte("x")
		header := &tar.Header{
			Name:       fmt.Sprintf("release-root/file-%d", index),
			Mode:       0o600,
			Size:       int64(len(contents)),
			Format:     tar.FormatPAX,
			PAXRecords: map[string]string{"comment": strings.Repeat("p", 2048)},
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "extract")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	err = extractReleaseTarGZWithLimit(archivePath, destination, "release-root", 1024)
	if err == nil || !strings.Contains(err.Error(), "decompressed stream size") {
		t.Fatalf("hidden PAX stream limit error = %v", err)
	}
}

func TestArtifactZIPRejectsTraversalAndSymlink(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		entryName string
		mode      os.FileMode
	}{
		{name: "traversal", entryName: "../escape", mode: 0o644},
		{name: "symlink", entryName: "link", mode: os.ModeSymlink | 0o777},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := filepath.Join(root, "artifact.zip")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			for index := 0; index < 21; index++ {
				name, mode := fmt.Sprintf("asset-%02d", index), os.FileMode(0o644)
				if index == 0 {
					name, mode = testCase.entryName, testCase.mode
				}
				header := &zip.FileHeader{Name: name}
				header.SetMode(mode)
				entry, createErr := writer.CreateHeader(header)
				if createErr != nil {
					t.Fatal(createErr)
				}
				_, _ = io.WriteString(entry, "test")
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := extractArtifactZIP(archivePath, filepath.Join(root, "extract")); err == nil {
				t.Fatal("unsafe artifact ZIP was accepted")
			}
		})
	}
}

func TestSelectPlatformArchiveRequiresFullArchiveAndExactTag(t *testing.T) {
	t.Parallel()
	name := platformArchiveName(testReleaseVersion)
	manifest := map[string]string{name: strings.Repeat("a", 64), "kaiten-mcp_" + testReleaseVersion + "_" + runtime.GOOS + "_" + runtime.GOARCH + platformExtension(): strings.Repeat("b", 64)}
	got, version, err := selectPlatformArchive(manifest, "v"+testReleaseVersion, runtime.GOOS, runtime.GOARCH)
	if err != nil || got != name || version != testReleaseVersion {
		t.Fatalf("selection = %q %q %v", got, version, err)
	}
	if _, _, err := selectPlatformArchive(manifest, "v9.9.9", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("mismatched tag was accepted")
	}
	delete(manifest, name)
	if _, _, err := selectPlatformArchive(manifest, "v"+testReleaseVersion, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("MCP-only archive was accepted as the full archive")
	}
}

func TestArtifactRedirectDoesNotForwardTokenCrossOrigin(t *testing.T) {
	t.Parallel()
	type observedHeaders struct{ authorization, referer string }
	finalHeaders := make(chan observedHeaders, 1)
	final := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		finalHeaders <- observedHeaders{authorization: request.Header.Get("Authorization"), referer: request.Header.Get("Referer")}
		_, _ = io.WriteString(writer, "artifact")
	}))
	defer final.Close()
	signedHeaders := make(chan observedHeaders, 1)
	signed := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		signedHeaders <- observedHeaders{authorization: request.Header.Get("Authorization"), referer: request.Header.Get("Referer")}
		http.Redirect(writer, request, final.URL+"/artifact", http.StatusFound)
	}))
	defer signed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("initial Authorization = %q", request.Header.Get("Authorization"))
		}
		http.Redirect(writer, request, signed.URL+"/signed?signature=must-not-leak", http.StatusFound)
	}))
	defer api.Close()
	destination := filepath.Join(t.TempDir(), "artifact.zip")
	if _, _, err := downloadFile(context.Background(), githubClient(), api.URL, "test-token", destination); err != nil {
		t.Fatal(err)
	}
	for name, headers := range map[string]observedHeaders{"signed artifact host": <-signedHeaders, "second redirect origin": <-finalHeaders} {
		if headers.authorization != "" || headers.referer != "" {
			t.Fatalf("%s received sensitive redirect headers: %+v", name, headers)
		}
	}
}

func TestHTTPSArtifactRedirectCannotReachRunnerLoopbackHTTP(t *testing.T) {
	t.Parallel()
	client := githubClient()
	initial, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/acme/kaiten/actions/artifacts/1/zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8100/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(redirect, []*http.Request{initial}); err == nil {
		t.Fatal("HTTPS artifact redirect was allowed to reach runner loopback HTTP")
	}
}

func buildReleaseFixtures(t *testing.T, root string) (string, string, string) {
	t.Helper()
	repository := filepath.Join(root, "fixture-repository")
	for _, directory := range []string{filepath.Join(repository, "cmd", "kaiten"), filepath.Join(repository, "cmd", "kaiten-mcp")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module github.com/dsuranov/kaiten-mcp\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The binding helper must never execute downloaded artifact code in its
	// token-bearing process. A helper regression that restores a smoke launch
	// will make this fixture exit non-zero and fail the test.
	code := `package main
import "os"
func main() { os.Exit(99) }
`
	for _, source := range []string{filepath.Join(repository, "cmd", "kaiten", "main.go"), filepath.Join(repository, "cmd", "kaiten-mcp", "main.go")} {
		if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range [][]string{{"init", "--quiet"}, {"config", "user.email", "native@example.invalid"}, {"config", "user.name", "Native Fixture"}, {"add", "go.mod", "cmd"}, {"commit", "--quiet", "-m", "fixture"}} {
		runFixtureCommand(t, repository, "git", arguments...)
	}
	commit := strings.TrimSpace(runFixtureCommand(t, repository, "git", "rev-parse", "HEAD"))
	if !lowerSHA.MatchString(commit) {
		t.Fatalf("fixture commit is invalid: %q", commit)
	}
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	kaiten := filepath.Join(root, "kaiten"+extension)
	kaitenMCP := filepath.Join(root, "kaiten-mcp"+extension)
	for output, packagePath := range map[string]string{kaiten: "./cmd/kaiten", kaitenMCP: "./cmd/kaiten-mcp"} {
		command := exec.Command("go", "build", "-trimpath", "-o", output, packagePath)
		command.Dir = repository
		command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "CGO_ENABLED=0")
		if buildOutput, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build release fixture %s: %v\n%s", packagePath, err, buildOutput)
		}
	}
	return kaiten, kaitenMCP, commit
}

func runFixtureCommand(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run fixture command %s: %v\n%s", name, err, output)
	}
	return string(output)
}

func buildFullArchive(t *testing.T, kaiten, kaitenMCP string) (string, []byte) {
	t.Helper()
	name := platformArchiveName(testReleaseVersion)
	root := strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".tar")
	root = strings.TrimSuffix(root, ".zip")
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	kaitenBytes, err := os.ReadFile(kaiten)
	if err != nil {
		t.Fatal(err)
	}
	kaitenMCPBytes, err := os.ReadFile(kaitenMCP)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		root + "/kaiten" + extension:     kaitenBytes,
		root + "/kaiten-mcp" + extension: kaitenMCPBytes,
		root + "/README.md":              []byte("test release\n"),
	}
	var buffer bytes.Buffer
	if runtime.GOOS == "windows" {
		writer := zip.NewWriter(&buffer)
		directory := &zip.FileHeader{Name: root + "/"}
		directory.SetMode(os.ModeDir | 0o755)
		if _, err := writer.CreateHeader(directory); err != nil {
			t.Fatal(err)
		}
		for _, fileName := range sortedKeys(files) {
			header := &zip.FileHeader{Name: fileName, Method: zip.Deflate}
			header.SetMode(0o755)
			entry, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write(files[fileName]); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return name, buffer.Bytes()
	}
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for _, fileName := range sortedKeys(files) {
		contents := files[fileName]
		if err := tarWriter.WriteHeader(&tar.Header{Name: fileName, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return name, buffer.Bytes()
}

func buildArtifactZIP(t *testing.T, archiveName string, archiveBytes []byte) ([]byte, string, string) {
	t.Helper()
	assets := map[string][]byte{archiveName: archiveBytes}
	for index := 0; index < 19; index++ {
		assets[fmt.Sprintf("asset-%02d.sbom.json", index)] = []byte(fmt.Sprintf("{\"index\":%d}\n", index))
	}
	var manifest strings.Builder
	for _, name := range sortedKeys(assets) {
		fmt.Fprintf(&manifest, "%s  %s\n", sha256Hex(assets[name]), name)
	}
	manifestBytes := []byte(manifest.String())
	assets["checksums.txt"] = manifestBytes
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range sortedKeys(assets) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(assets[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), sha256Hex(manifestBytes), sha256Hex(archiveBytes)
}

func writeLinkArchive(t *testing.T, archivePath, archiveName string) {
	t.Helper()
	root := strings.TrimSuffix(strings.TrimSuffix(archiveName, ".gz"), ".tar")
	root = strings.TrimSuffix(root, ".zip")
	if runtime.GOOS == "windows" {
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		header := &zip.FileHeader{Name: root + "/link"}
		header.SetMode(os.ModeSymlink | 0o777)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(entry, "target")
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: root + "/link", Typeflag: tar.TypeSymlink, Linkname: "target", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func platformArchiveName(version string) string {
	return "kaiten_" + version + "_" + runtime.GOOS + "_" + runtime.GOARCH + platformExtension()
}

func platformExtension() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func writeTestJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestParseRunID(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1", "42", "4611686018427387904"} {
		if _, err := parseRunID(value); err != nil {
			t.Fatalf("valid run ID %s was rejected", value)
		}
	}
	for _, value := range []string{"", "0", "01", "+1", "-1", "abc", "999999999999999999999999"} {
		if _, err := parseRunID(value); err == nil {
			t.Fatalf("invalid run ID %q was accepted", value)
		}
	}
}
