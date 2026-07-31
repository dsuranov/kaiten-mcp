//go:build ignore

// Command prepare-native-lifecycle-release binds a native lifecycle run to the
// exact full-platform archive produced by one successful Release workflow run.
// It uses only the standard library so it can run on every hosted matrix image.
package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxArtifactBytes = int64(2 << 30)
	maxArchiveBytes  = int64(1 << 30)
	maxArchiveFiles  = 512
	expectedGo       = "go1.26.5"
)

var (
	lowerSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	releaseTag     = regexp.MustCompile(`^v[0-9][0-9A-Za-z.+-]*$`)
	releaseVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
)

type options struct {
	APIURL       string
	Repository   string
	RunID        int64
	ExpectedSHA  string
	Token        string
	WorkDir      string
	OutputDir    string
	MetadataPath string
}

type workflowRun struct {
	ID         int64              `json:"id"`
	Name       string             `json:"name"`
	Path       string             `json:"path"`
	Event      string             `json:"event"`
	Status     string             `json:"status"`
	Conclusion string             `json:"conclusion"`
	HeadBranch string             `json:"head_branch"`
	HeadSHA    string             `json:"head_sha"`
	RunAttempt int                `json:"run_attempt"`
	Repository repositoryIdentity `json:"repository"`
	HeadRepo   repositoryIdentity `json:"head_repository"`
}

type repositoryIdentity struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitReference struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}

type annotatedTag struct {
	Object gitObject `json:"object"`
}

type artifactPage struct {
	TotalCount int64      `json:"total_count"`
	Artifacts  []artifact `json:"artifacts"`
}

type artifact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	SizeInBytes int64  `json:"size_in_bytes"`
	Expired     bool   `json:"expired"`
	Digest      string `json:"digest"`
	WorkflowRun *struct {
		ID               int64  `json:"id"`
		RepositoryID     int64  `json:"repository_id"`
		HeadRepositoryID int64  `json:"head_repository_id"`
		HeadBranch       string `json:"head_branch"`
		HeadSHA          string `json:"head_sha"`
	} `json:"workflow_run"`
}

type releaseBinding struct {
	Run             workflowRun
	Tag             string
	Artifact        artifact
	ArtifactZIPHash string
	ManifestHash    string
	ArchiveName     string
	ArchiveHash     string
	Version         string
	KaitenName      string
	KaitenHash      string
	KaitenMCPName   string
	KaitenMCPHash   string
	GOOS            string
	GOARCH          string
	GoVersion       string
}

func main() {
	var rawRunID string
	var tokenEnv string
	var opts options
	flag.StringVar(&opts.APIURL, "api-url", "https://api.github.com", "GitHub API base URL")
	flag.StringVar(&opts.Repository, "repository", "", "exact owner/repository")
	flag.StringVar(&rawRunID, "run-id", "", "successful Release workflow run ID")
	flag.StringVar(&opts.ExpectedSHA, "expected-sha", "", "lowercase 40-character release head SHA")
	flag.StringVar(&tokenEnv, "token-env", "GH_TOKEN", "environment variable containing the GitHub token")
	flag.StringVar(&opts.WorkDir, "work", "", "new absolute scratch directory")
	flag.StringVar(&opts.OutputDir, "output", "", "new absolute directory for the two release binaries")
	flag.StringVar(&opts.MetadataPath, "metadata", "", "new absolute secret-free binding evidence file")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("unexpected positional arguments")
	}
	runID, err := parseRunID(rawRunID)
	if err != nil {
		fatal("%v", err)
	}
	opts.RunID = runID
	if tokenEnv == "" {
		fatal("token environment variable name is empty")
	}
	opts.Token = os.Getenv(tokenEnv)
	if err := validateOptions(opts); err != nil {
		fatal("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	binding, err := prepare(ctx, opts)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("bound native lifecycle to Release run %d: %s %s/%s archive=%s sha256=%s\n",
		binding.Run.ID, binding.Tag, binding.GOOS, binding.GOARCH, binding.ArchiveName, binding.ArchiveHash)
}

func parseRunID(raw string) (int64, error) {
	runID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || runID < 1 || strconv.FormatInt(runID, 10) != raw {
		return 0, errors.New("release run ID must be a positive base-10 integer without leading zeroes")
	}
	return runID, nil
}

func validateOptions(opts options) error {
	if !lowerSHA.MatchString(opts.ExpectedSHA) {
		return errors.New("expected SHA must be exactly 40 lowercase hexadecimal characters")
	}
	if !repositoryName.MatchString(opts.Repository) || strings.Contains(opts.Repository, "..") {
		return errors.New("repository must be an exact owner/name pair")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return errors.New("GitHub token is unavailable")
	}
	if _, err := validatedAPIBase(opts.APIURL); err != nil {
		return err
	}
	for name, value := range map[string]string{"work": opts.WorkDir, "output": opts.OutputDir, "metadata": opts.MetadataPath} {
		if value == "" || !filepath.IsAbs(value) {
			return fmt.Errorf("%s path must be absolute", name)
		}
	}
	if pathsOverlap(opts.WorkDir, opts.OutputDir) || pathsOverlap(opts.WorkDir, opts.MetadataPath) || pathsOverlap(opts.OutputDir, opts.MetadataPath) {
		return errors.New("work, output, and metadata paths must not overlap")
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	inside := func(parent, candidate string) bool {
		relative, err := filepath.Rel(parent, candidate)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return inside(first, second) || inside(second, first)
}

func prepare(ctx context.Context, opts options) (releaseBinding, error) {
	var result releaseBinding
	base, _ := validatedAPIBase(opts.APIURL)
	client := githubClient()
	runEndpoint := repositoryEndpoint(base, opts.Repository, "actions/runs/"+strconv.FormatInt(opts.RunID, 10))
	if err := getJSON(ctx, client, runEndpoint, opts.Token, &result.Run); err != nil {
		return result, fmt.Errorf("read Release workflow run: %w", err)
	}
	if err := validateRun(result.Run, opts); err != nil {
		return result, err
	}
	result.Tag = result.Run.HeadBranch
	if err := verifyReleaseRef(ctx, client, base, opts.Repository, result.Tag, opts.ExpectedSHA, opts.Token); err != nil {
		return result, err
	}
	selected, err := findReleaseArtifact(ctx, client, base, opts.Repository, result.Run, opts.Token)
	if err != nil {
		return result, err
	}
	result.Artifact = selected
	if err := makeNewDirectory(opts.WorkDir); err != nil {
		return result, fmt.Errorf("create release scratch directory: %w", err)
	}
	zipPath := filepath.Join(opts.WorkDir, "release-assets.zip")
	downloadEndpoint := repositoryEndpoint(base, opts.Repository, "actions/artifacts/"+strconv.FormatInt(selected.ID, 10)+"/zip")
	result.ArtifactZIPHash, err = downloadFile(ctx, client, downloadEndpoint, opts.Token, zipPath)
	if err != nil {
		return result, fmt.Errorf("download release-assets: %w", err)
	}
	wantArtifactHash := strings.TrimPrefix(selected.Digest, "sha256:")
	if result.ArtifactZIPHash != wantArtifactHash {
		return result, fmt.Errorf("release-assets digest = %s, API attested %s", result.ArtifactZIPHash, wantArtifactHash)
	}
	assetsDir := filepath.Join(opts.WorkDir, "assets")
	if err := extractArtifactZIP(zipPath, assetsDir); err != nil {
		return result, fmt.Errorf("extract release-assets: %w", err)
	}
	manifest, manifestHash, err := validateChecksumManifest(assetsDir)
	if err != nil {
		return result, err
	}
	result.ManifestHash = manifestHash
	archiveName, version, err := selectPlatformArchive(manifest, result.Tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return result, err
	}
	result.ArchiveName, result.Version = archiveName, version
	result.ArchiveHash = manifest[archiveName]
	extractDir := filepath.Join(opts.WorkDir, "archive")
	if err := extractReleaseArchive(filepath.Join(assetsDir, archiveName), archiveName, extractDir); err != nil {
		return result, fmt.Errorf("extract platform archive: %w", err)
	}
	archiveRoot := strings.TrimSuffix(strings.TrimSuffix(archiveName, ".gz"), ".tar")
	archiveRoot = strings.TrimSuffix(archiveRoot, ".zip")
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	result.KaitenName = "kaiten" + extension
	result.KaitenMCPName = "kaiten-mcp" + extension
	kaiten := filepath.Join(extractDir, archiveRoot, result.KaitenName)
	kaitenMCP := filepath.Join(extractDir, archiveRoot, result.KaitenMCPName)
	for _, binary := range []string{kaiten, kaitenMCP} {
		if err := validateBinary(binary, runtime.GOOS, runtime.GOARCH, expectedGo); err != nil {
			return result, err
		}
	}
	if err := smokeVersion(ctx, kaiten, []string{"version"}, "kaiten "+version); err != nil {
		return result, err
	}
	if err := smokeVersion(ctx, kaitenMCP, []string{"version"}, "kaiten-mcp "+version); err != nil {
		return result, err
	}
	result.KaitenHash, err = fileSHA256(kaiten)
	if err != nil {
		return result, err
	}
	result.KaitenMCPHash, err = fileSHA256(kaitenMCP)
	if err != nil {
		return result, err
	}
	result.GOOS, result.GOARCH, result.GoVersion = runtime.GOOS, runtime.GOARCH, expectedGo
	if err := makeNewDirectory(opts.OutputDir); err != nil {
		return result, fmt.Errorf("create release binary directory: %w", err)
	}
	if err := copyRegularExclusive(kaiten, filepath.Join(opts.OutputDir, result.KaitenName), 0o700); err != nil {
		return result, err
	}
	if err := copyRegularExclusive(kaitenMCP, filepath.Join(opts.OutputDir, result.KaitenMCPName), 0o700); err != nil {
		return result, err
	}
	for destination, expected := range map[string]string{
		filepath.Join(opts.OutputDir, result.KaitenName):    result.KaitenHash,
		filepath.Join(opts.OutputDir, result.KaitenMCPName): result.KaitenMCPHash,
	} {
		actual, err := fileSHA256(destination)
		if err != nil || actual != expected {
			return result, fmt.Errorf("copied release binary hash mismatch: %s", filepath.Base(destination))
		}
	}
	if err := writeMetadata(opts.MetadataPath, result, opts.Token); err != nil {
		return result, err
	}
	return result, nil
}

func validatedAPIBase(raw string) (*url.URL, error) {
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("GitHub API URL is invalid")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1")) {
		return nil, errors.New("GitHub API URL must use HTTPS")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	return base, nil
}

func repositoryEndpoint(base *url.URL, repository, suffix string) string {
	parts := strings.Split(repository, "/")
	copy := *base
	copy.Path = strings.TrimRight(base.Path, "/") + "/repos/" + parts[0] + "/" + parts[1] + "/" + suffix
	return copy.String()
}

func githubClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many artifact download redirects")
			}
			if request.URL.Scheme != "https" && !(request.URL.Scheme == "http" && (request.URL.Hostname() == "127.0.0.1" || request.URL.Hostname() == "localhost" || request.URL.Hostname() == "::1")) {
				return errors.New("artifact download redirected away from HTTPS")
			}
			if len(via) > 0 && !sameOrigin(request.URL, via[0].URL) {
				request.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func sameOrigin(first, second *url.URL) bool {
	return strings.EqualFold(first.Scheme, second.Scheme) && strings.EqualFold(first.Host, second.Host)
}

func apiRequest(ctx context.Context, endpoint, token string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "kaiten-native-release-binding")
	return request, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint, token string, destination any) error {
	request, err := apiRequest(ctx, endpoint, token)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("GitHub API returned trailing JSON data")
	}
	return nil
}

func validateRun(run workflowRun, opts options) error {
	if run.ID != opts.RunID {
		return fmt.Errorf("workflow run ID = %d, want %d", run.ID, opts.RunID)
	}
	if run.Repository.ID < 1 || run.Repository.ID != run.HeadRepo.ID || run.Repository.FullName != opts.Repository || run.HeadRepo.FullName != opts.Repository {
		return fmt.Errorf("workflow run repository/head repository = %q/%q, want %q", run.Repository.FullName, run.HeadRepo.FullName, opts.Repository)
	}
	if run.Name != "Release" || !validReleaseWorkflowPath(run.Path, run.HeadBranch) {
		return fmt.Errorf("workflow identity = %q at %q, want Release at .github/workflows/release.yml", run.Name, run.Path)
	}
	if run.Event != "push" || run.Status != "completed" || run.Conclusion != "success" {
		return fmt.Errorf("Release run state = event %q status %q conclusion %q, want push/completed/success", run.Event, run.Status, run.Conclusion)
	}
	if run.HeadSHA != opts.ExpectedSHA {
		return fmt.Errorf("Release run head SHA = %q, want %q", run.HeadSHA, opts.ExpectedSHA)
	}
	if !releaseTag.MatchString(run.HeadBranch) {
		return fmt.Errorf("Release run ref %q is not a reviewed v-prefixed release tag", run.HeadBranch)
	}
	if run.RunAttempt < 1 {
		return errors.New("Release run attempt is missing")
	}
	return nil
}

func validReleaseWorkflowPath(value, tag string) bool {
	const workflowPath = ".github/workflows/release.yml"
	return value == workflowPath || value == workflowPath+"@"+tag || value == workflowPath+"@refs/tags/"+tag
}

func verifyReleaseRef(ctx context.Context, client *http.Client, base *url.URL, repository, tag, expectedSHA, token string) error {
	endpoint := repositoryEndpoint(base, repository, "git/ref/tags/"+tag)
	var reference gitReference
	if err := getJSON(ctx, client, endpoint, token, &reference); err != nil {
		return fmt.Errorf("read Release tag ref: %w", err)
	}
	if reference.Ref != "refs/tags/"+tag {
		return fmt.Errorf("Release ref = %q, want refs/tags/%s", reference.Ref, tag)
	}
	object := reference.Object
	for depth := 0; object.Type == "tag" && depth < 8; depth++ {
		if !lowerSHA.MatchString(object.SHA) {
			return errors.New("annotated Release tag object has an invalid SHA")
		}
		var annotated annotatedTag
		if err := getJSON(ctx, client, repositoryEndpoint(base, repository, "git/tags/"+object.SHA), token, &annotated); err != nil {
			return fmt.Errorf("peel annotated Release tag: %w", err)
		}
		object = annotated.Object
	}
	if object.Type != "commit" || object.SHA != expectedSHA {
		return fmt.Errorf("Release tag resolves to %s %s, want commit %s", object.Type, object.SHA, expectedSHA)
	}
	return nil
}

func findReleaseArtifact(ctx context.Context, client *http.Client, base *url.URL, repository string, run workflowRun, token string) (artifact, error) {
	var matches []artifact
	var seen int64
	for page := 1; page <= 10; page++ {
		endpoint := repositoryEndpoint(base, repository, "actions/runs/"+strconv.FormatInt(run.ID, 10)+"/artifacts")
		endpoint += "?per_page=100&page=" + strconv.Itoa(page)
		var response artifactPage
		if err := getJSON(ctx, client, endpoint, token, &response); err != nil {
			return artifact{}, fmt.Errorf("list Release artifacts: %w", err)
		}
		seen += int64(len(response.Artifacts))
		for _, candidate := range response.Artifacts {
			if candidate.Name == "release-assets" {
				matches = append(matches, candidate)
			}
		}
		if seen >= response.TotalCount {
			break
		}
		if len(response.Artifacts) == 0 || page == 10 {
			return artifact{}, errors.New("Release artifact listing is incomplete")
		}
	}
	if len(matches) != 1 {
		return artifact{}, fmt.Errorf("Release run contains %d artifacts named release-assets, want exactly one", len(matches))
	}
	selected := matches[0]
	if selected.ID < 1 || selected.Expired || selected.SizeInBytes < 1 || selected.SizeInBytes > maxArtifactBytes {
		return artifact{}, errors.New("release-assets is expired or has invalid identity/size")
	}
	if selected.WorkflowRun == nil || selected.WorkflowRun.ID != run.ID || selected.WorkflowRun.RepositoryID != run.Repository.ID || selected.WorkflowRun.HeadRepositoryID != run.HeadRepo.ID || selected.WorkflowRun.HeadBranch != run.HeadBranch || selected.WorkflowRun.HeadSHA != run.HeadSHA {
		return artifact{}, errors.New("release-assets workflow identity does not match the validated Release run")
	}
	if !strings.HasPrefix(selected.Digest, "sha256:") || !sha256Pattern.MatchString(strings.TrimPrefix(selected.Digest, "sha256:")) {
		return artifact{}, errors.New("release-assets API record lacks a lowercase SHA-256 digest")
	}
	return selected, nil
}

func downloadFile(ctx context.Context, client *http.Client, endpoint, token, destination string) (string, error) {
	request, err := apiRequest(ctx, endpoint, token)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("artifact HTTP request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("artifact download returned HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, maxArtifactBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written < 1 || written > maxArtifactBytes {
		return "", errors.New("artifact download exceeded the accepted size")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func extractArtifactZIP(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) != 21 {
		return fmt.Errorf("release-assets contains %d entries, want exactly 21 files", len(reader.File))
	}
	if err := makeNewDirectory(destination); err != nil {
		return err
	}
	seen := make(map[string]bool, len(reader.File))
	var total uint64
	for _, entry := range reader.File {
		if err := validateBaseFileName(entry.Name); err != nil {
			return fmt.Errorf("unsafe release-assets entry %q: %w", entry.Name, err)
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() || entry.UncompressedSize64 > uint64(maxArchiveBytes) || entry.UncompressedSize64 > uint64(maxArtifactBytes)-total {
			return fmt.Errorf("unsafe release-assets entry type or size: %s", entry.Name)
		}
		total += entry.UncompressedSize64
		key := strings.ToLower(entry.Name)
		if seen[key] {
			return fmt.Errorf("duplicate release-assets entry: %s", entry.Name)
		}
		seen[key] = true
		if err := extractZIPFile(entry, filepath.Join(destination, entry.Name), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func validateChecksumManifest(directory string) (map[string]string, string, error) {
	manifestPath := filepath.Join(directory, "checksums.txt")
	manifestHash, err := fileSHA256(manifestPath)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !sha256Pattern.MatchString(fields[0]) {
			return nil, "", fmt.Errorf("invalid checksums.txt line %q", scanner.Text())
		}
		name := strings.TrimPrefix(fields[1], "*")
		if fields[1] != name {
			return nil, "", errors.New("checksums.txt uses an unsupported binary marker")
		}
		if err := validateBaseFileName(name); err != nil || name == "checksums.txt" {
			return nil, "", fmt.Errorf("unsafe checksums.txt name %q", name)
		}
		if _, duplicate := checksums[name]; duplicate {
			return nil, "", fmt.Errorf("duplicate checksums.txt entry %s", name)
		}
		actual, err := fileSHA256(filepath.Join(directory, name))
		if err != nil {
			return nil, "", fmt.Errorf("validate checksum for %s: %w", name, err)
		}
		if actual != fields[0] {
			return nil, "", fmt.Errorf("checksum mismatch for %s", name)
		}
		checksums[name] = actual
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	if len(checksums) != 20 {
		return nil, "", fmt.Errorf("checksums.txt contains %d entries, want 20", len(checksums))
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, "", err
	}
	if len(entries) != len(checksums)+1 {
		return nil, "", errors.New("release-assets contains a file not covered by checksums.txt")
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("unsafe extracted release asset: %s", entry.Name())
		}
		if entry.Name() != "checksums.txt" {
			if _, exists := checksums[entry.Name()]; !exists {
				return nil, "", fmt.Errorf("release asset %s is absent from checksums.txt", entry.Name())
			}
		}
	}
	return checksums, manifestHash, nil
}

func selectPlatformArchive(manifest map[string]string, tag, goos, goarch string) (string, string, error) {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	prefix := "kaiten_"
	suffix := "_" + goos + "_" + goarch + extension
	var matches []string
	for name := range manifest {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			matches = append(matches, name)
		}
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("manifest contains %d full kaiten archives for %s/%s, want exactly one", len(matches), goos, goarch)
	}
	name := matches[0]
	version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if !releaseVersion.MatchString(version) || tag != "v"+version {
		return "", "", fmt.Errorf("archive version %q does not exactly match Release ref %q", version, tag)
	}
	return name, version, nil
}

func extractReleaseArchive(archivePath, archiveName, destination string) error {
	if err := makeNewDirectory(destination); err != nil {
		return err
	}
	root := strings.TrimSuffix(strings.TrimSuffix(archiveName, ".gz"), ".tar")
	root = strings.TrimSuffix(root, ".zip")
	if strings.HasSuffix(archiveName, ".zip") {
		return extractReleaseZIP(archivePath, destination, root)
	}
	if strings.HasSuffix(archiveName, ".tar.gz") {
		return extractReleaseTarGZ(archivePath, destination, root)
	}
	return errors.New("unsupported release archive format")
}

func extractReleaseZIP(archivePath, destination, expectedRoot string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxArchiveFiles {
		return errors.New("platform ZIP has an invalid entry count")
	}
	seen := make(map[string]bool)
	var total uint64
	for _, entry := range reader.File {
		isDirectory := entry.FileInfo().IsDir()
		if err := validateArchiveEntry(entry.Name, expectedRoot, isDirectory, seen); err != nil {
			return err
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || (!isDirectory && !entry.Mode().IsRegular()) {
			return fmt.Errorf("platform ZIP contains a link or irregular entry: %s", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maxArchiveBytes)-total {
			return errors.New("platform ZIP exceeds the accepted uncompressed size")
		}
		total += entry.UncompressedSize64
		target, err := safeArchiveTarget(destination, entry.Name)
		if err != nil {
			return err
		}
		if isDirectory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := extractZIPFile(entry, target, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func extractReleaseTarGZ(archivePath, destination, expectedRoot string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	seen := make(map[string]bool)
	count := 0
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > maxArchiveFiles {
			return errors.New("platform tarball has too many entries")
		}
		isDirectory := header.Typeflag == tar.TypeDir
		if err := validateArchiveEntry(header.Name, expectedRoot, isDirectory, seen); err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && !isDirectory {
			return fmt.Errorf("platform tarball contains a link or irregular entry: %s", header.Name)
		}
		if header.Size < 0 || header.Size > maxArchiveBytes-total {
			return errors.New("platform tarball exceeds the accepted uncompressed size")
		}
		total += header.Size
		target, err := safeArchiveTarget(destination, header.Name)
		if err != nil {
			return err
		}
		if isDirectory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(output, reader, header.Size)
		closeErr := output.Close()
		if copyErr != nil || written != header.Size {
			return errors.New("platform tarball entry was truncated")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if count == 0 {
		return errors.New("platform tarball is empty")
	}
	return nil
}

func validateArchiveEntry(name, expectedRoot string, directory bool, seen map[string]bool) error {
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, '\x00') || path.Clean(trimmed) != trimmed || path.IsAbs(trimmed) || trimmed == ".." || strings.HasPrefix(trimmed, "../") || !portableArchivePath(trimmed) {
		return fmt.Errorf("unsafe platform archive path: %q", name)
	}
	if trimmed != expectedRoot && !strings.HasPrefix(trimmed, expectedRoot+"/") {
		return fmt.Errorf("platform archive entry escapes exact root %s: %s", expectedRoot, name)
	}
	if trimmed == expectedRoot && !directory {
		return errors.New("platform archive root is not a directory")
	}
	key := strings.ToLower(trimmed)
	if seen[key] {
		return fmt.Errorf("duplicate or case-colliding platform archive entry: %s", name)
	}
	seen[key] = true
	return nil
}

func safeArchiveTarget(destination, name string) (string, error) {
	converted := filepath.FromSlash(strings.TrimSuffix(name, "/"))
	if filepath.IsAbs(converted) || filepath.VolumeName(converted) != "" {
		return "", fmt.Errorf("unsafe archive volume path: %s", name)
	}
	target := filepath.Join(destination, converted)
	relative, err := filepath.Rel(destination, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes extraction directory: %s", name)
	}
	return target, nil
}

func extractZIPFile(entry *zip.File, destination string, mode os.FileMode) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxArchiveBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != int64(entry.UncompressedSize64) || written > maxArchiveBytes {
		return fmt.Errorf("ZIP entry %s has an invalid extracted size", entry.Name)
	}
	return nil
}

func validateBaseFileName(name string) error {
	if name == "" || name == "." || name == ".." || path.Base(name) != name || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") || filepath.VolumeName(name) != "" || !portableArchivePath(name) {
		return errors.New("entry is not a safe base file name")
	}
	return nil
}

func portableArchivePath(name string) bool {
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.Contains(component, ":") || strings.TrimRight(component, ". ") != component {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return false
		}
	}
	return true
}

func validateBinary(binary, goos, goarch, goVersion string) error {
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("release binary is missing or unsafe: %s", filepath.Base(binary))
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		return err
	}
	build, err := buildinfo.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("read build info for %s: %w", filepath.Base(binary), err)
	}
	settings := make(map[string]string)
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	if build.GoVersion != goVersion || settings["GOOS"] != goos || settings["GOARCH"] != goarch {
		return fmt.Errorf("%s build identity = %s %s/%s, want %s %s/%s", filepath.Base(binary), build.GoVersion, settings["GOOS"], settings["GOARCH"], goVersion, goos, goarch)
	}
	return nil
}

func smokeVersion(parent context.Context, binary string, arguments []string, expected string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	// The downloaded binaries need no environment for a version smoke. In
	// particular, never expose the Actions token used for the artifact fetch.
	command.Env = []string{}
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("smoke %s version: %w", filepath.Base(binary), err)
	}
	if strings.TrimSpace(stdout.String()) != expected || stderr.String() != "" {
		return fmt.Errorf("%s version output = stdout %q stderr %q, want %q and empty stderr", filepath.Base(binary), strings.TrimSpace(stdout.String()), stderr.String(), expected)
	}
	return nil
}

func copyRegularExclusive(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func writeMetadata(destination string, binding releaseBinding, forbidden string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	values := [][2]string{
		{"schema", "kaiten-native-release-binding/v1"},
		{"release_repository", binding.Run.Repository.FullName},
		{"release_repository_id", strconv.FormatInt(binding.Run.Repository.ID, 10)},
		{"release_run_id", strconv.FormatInt(binding.Run.ID, 10)},
		{"release_run_attempt", strconv.Itoa(binding.Run.RunAttempt)},
		{"release_workflow", binding.Run.Name},
		{"release_workflow_path", binding.Run.Path},
		{"release_event", binding.Run.Event},
		{"release_conclusion", binding.Run.Conclusion},
		{"release_tag", binding.Tag},
		{"release_head_sha", binding.Run.HeadSHA},
		{"release_artifact_id", strconv.FormatInt(binding.Artifact.ID, 10)},
		{"release_artifact_name", binding.Artifact.Name},
		{"release_artifact_size", strconv.FormatInt(binding.Artifact.SizeInBytes, 10)},
		{"release_artifact_api_digest", binding.Artifact.Digest},
		{"release_artifact_zip_sha256", binding.ArtifactZIPHash},
		{"release_manifest_sha256", binding.ManifestHash},
		{"release_archive", binding.ArchiveName},
		{"release_archive_sha256", binding.ArchiveHash},
		{"release_version", binding.Version},
		{"release_goos", binding.GOOS},
		{"release_goarch", binding.GOARCH},
		{"release_go_version", binding.GoVersion},
		{"release_kaiten", binding.KaitenName},
		{"release_kaiten_sha256", binding.KaitenHash},
		{"release_kaiten_mcp", binding.KaitenMCPName},
		{"release_kaiten_mcp_sha256", binding.KaitenMCPHash},
	}
	var output strings.Builder
	for _, item := range values {
		if strings.ContainsAny(item[1], "\r\n") {
			return errors.New("refusing multiline release-binding evidence")
		}
		fmt.Fprintf(&output, "%s=%s\n", item[0], item[1])
	}
	if forbidden != "" && strings.Contains(output.String(), forbidden) {
		return errors.New("refusing release-binding evidence containing the API token")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, output.String())
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func makeNewDirectory(directory string) error {
	if _, err := os.Lstat(directory); err == nil {
		return errors.New("refusing to reuse existing directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(directory, 0o700)
}

func fileSHA256(fileName string) (string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func fatal(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", arguments...)
	os.Exit(1)
}
