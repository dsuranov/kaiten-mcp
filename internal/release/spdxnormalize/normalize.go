// Package spdxnormalize makes volatile Syft SPDX creation metadata
// reproducible without changing package or relationship evidence.
package spdxnormalize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxDocumentSize = 64 << 20

const (
	normalizerCreator = "Tool: kaiten-mcp-spdx-normalizer-1"
	policyComment     = "Reproducibility policy v1: creationInfo.created is the source commit time; documentNamespace is derived from canonical SPDX content excluding creationInfo.created and documentNamespace."
)

// Normalize reads a Syft SPDX JSON document, applies the versioned creation
// metadata policy, validates its artifact links, and writes canonical JSON to
// outputPath.
func Normalize(inputPath, outputPath, artifactPath string, created time.Time) error {
	if filepath.Clean(inputPath) == filepath.Clean(outputPath) {
		return errors.New("input and output SPDX paths must differ")
	}
	document, err := readDocument(inputPath)
	if err != nil {
		return err
	}
	digest, err := artifactDigest(artifactPath)
	if err != nil {
		return err
	}
	if err := validateDocument(document, filepath.Base(artifactPath), digest, time.Time{}); err != nil {
		return fmt.Errorf("validate Syft SPDX document: %w", err)
	}

	creationInfo := document["creationInfo"].(map[string]any)
	creationInfo["created"] = created.UTC().Format(time.RFC3339)
	creationInfo["comment"] = appendPolicyComment(creationInfo)
	creationInfo["creators"] = appendNormalizerCreator(creationInfo["creators"].([]any))
	semanticDigest, err := semanticDocumentDigest(document)
	if err != nil {
		return err
	}
	document["documentNamespace"] = namespaceForDigest(semanticDigest)

	if err := validateDocument(document, filepath.Base(artifactPath), digest, created); err != nil {
		return fmt.Errorf("validate normalized SPDX document: %w", err)
	}
	if err := writeDocument(outputPath, document); err != nil {
		return err
	}
	return nil
}

// Validate checks that a normalized SPDX document describes artifactPath and
// carries the exact deterministic metadata derived from created and the
// canonical semantic document digest.
func Validate(documentPath, artifactPath string, created time.Time) error {
	document, err := readDocument(documentPath)
	if err != nil {
		return err
	}
	digest, err := artifactDigest(artifactPath)
	if err != nil {
		return err
	}
	if err := validateDocument(document, filepath.Base(artifactPath), digest, created); err != nil {
		return fmt.Errorf("validate normalized SPDX document: %w", err)
	}
	return nil
}

func readDocument(path string) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open SPDX document: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat SPDX document: %w", err)
	}
	if info.Size() > maxDocumentSize {
		return nil, fmt.Errorf("SPDX document exceeds %d bytes", maxDocumentSize)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxDocumentSize+1))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode SPDX document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode SPDX document: multiple JSON values")
		}
		return nil, fmt.Errorf("decode SPDX document trailing data: %w", err)
	}
	return document, nil
}

func writeDocument(path string, document map[string]any) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite SPDX document %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat SPDX output: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".spdx-normalize-*")
	if err != nil {
		return fmt.Errorf("create SPDX output: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode SPDX output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync SPDX output: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set SPDX output mode: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close SPDX output: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install SPDX output: %w", err)
	}
	keep = true
	return nil
}

func artifactDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open release artifact: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash release artifact: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func namespaceForDigest(digest string) string {
	return "https://github.com/dsuranov/kaiten-mcp/sbom/spdx-2.3-normalizer-v1-sha256-" + digest
}

func validateDocument(document map[string]any, artifactName, digest string, exactCreated time.Time) error {
	for key, expected := range map[string]string{
		"spdxVersion": "SPDX-2.3",
		"dataLicense": "CC0-1.0",
		"SPDXID":      "SPDXRef-DOCUMENT",
		"name":        artifactName,
	} {
		if got, ok := stringField(document, key); !ok || got != expected {
			return fmt.Errorf("%s = %q, want %q", key, got, expected)
		}
	}

	namespace, ok := stringField(document, "documentNamespace")
	if !ok {
		return errors.New("documentNamespace is missing")
	}
	parsedNamespace, err := url.Parse(namespace)
	if err != nil || parsedNamespace.Scheme != "https" || parsedNamespace.Host == "" || parsedNamespace.Fragment != "" {
		return fmt.Errorf("documentNamespace is not an absolute fragment-free HTTPS URI: %q", namespace)
	}

	creationInfo, ok := objectField(document, "creationInfo")
	if !ok {
		return errors.New("creationInfo is missing")
	}
	createdText, ok := stringField(creationInfo, "created")
	if !ok {
		return errors.New("creationInfo.created is missing")
	}
	created, err := time.Parse(time.RFC3339, createdText)
	if err != nil {
		return fmt.Errorf("creationInfo.created is invalid: %w", err)
	}
	if !exactCreated.IsZero() && !created.Equal(exactCreated) {
		return fmt.Errorf("creationInfo.created = %q, want %q", createdText, exactCreated.UTC().Format(time.RFC3339))
	}
	creators, ok := arrayField(creationInfo, "creators")
	if !ok || len(creators) == 0 {
		return errors.New("creationInfo.creators is empty")
	}
	syftCreatorFound := false
	for _, creator := range creators {
		text, ok := creator.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return errors.New("creationInfo.creators contains an invalid value")
		}
		if strings.HasPrefix(text, "Tool: syft-") {
			syftCreatorFound = true
		}
	}
	if !syftCreatorFound {
		return errors.New("creationInfo.creators does not preserve the Syft tool creator")
	}
	if !exactCreated.IsZero() {
		if countString(creators, normalizerCreator) != 1 {
			return fmt.Errorf("creationInfo.creators must contain exactly one %q entry", normalizerCreator)
		}
		comment, ok := stringField(creationInfo, "comment")
		if !ok || !strings.Contains(comment, policyComment) {
			return errors.New("creationInfo.comment lacks the reproducibility policy")
		}
		semanticDigest, err := semanticDocumentDigest(document)
		if err != nil {
			return err
		}
		if namespace != namespaceForDigest(semanticDigest) {
			return fmt.Errorf("documentNamespace = %q, want semantic-document digest namespace", namespace)
		}
	}

	knownIDs := map[string]struct{}{"SPDXRef-DOCUMENT": {}}
	packages, ok := arrayField(document, "packages")
	if !ok || len(packages) == 0 {
		return errors.New("packages is empty")
	}
	rootFound := false
	for index, value := range packages {
		pkg, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("packages[%d] is not an object", index)
		}
		id, ok := stringField(pkg, "SPDXID")
		if !ok || !strings.HasPrefix(id, "SPDXRef-") {
			return fmt.Errorf("packages[%d].SPDXID is invalid", index)
		}
		if _, duplicate := knownIDs[id]; duplicate {
			return fmt.Errorf("duplicate SPDXID %q", id)
		}
		knownIDs[id] = struct{}{}
		name, hasName := stringField(pkg, "name")
		if !hasName || name == "" {
			return fmt.Errorf("packages[%d].name is missing", index)
		}
		if _, ok := pkg["filesAnalyzed"].(bool); !ok {
			return fmt.Errorf("packages[%d].filesAnalyzed is missing", index)
		}
		if download, ok := stringField(pkg, "downloadLocation"); !ok || download == "" {
			return fmt.Errorf("packages[%d].downloadLocation is missing", index)
		}
		if name == artifactName {
			if rootFound {
				return fmt.Errorf("multiple root packages named %q", artifactName)
			}
			if err := validateRootPackage(pkg, digest); err != nil {
				return err
			}
			rootFound = true
		}
	}
	if !rootFound {
		return fmt.Errorf("root package %q is missing", artifactName)
	}

	for _, collection := range []string{"files", "snippets"} {
		values, exists := arrayField(document, collection)
		if !exists {
			continue
		}
		for index, value := range values {
			item, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s[%d] is not an object", collection, index)
			}
			id, ok := stringField(item, "SPDXID")
			if !ok || !strings.HasPrefix(id, "SPDXRef-") {
				return fmt.Errorf("%s[%d].SPDXID is invalid", collection, index)
			}
			if _, duplicate := knownIDs[id]; duplicate {
				return fmt.Errorf("duplicate SPDXID %q", id)
			}
			knownIDs[id] = struct{}{}
		}
	}

	relationships, ok := arrayField(document, "relationships")
	if !ok || len(relationships) == 0 {
		return errors.New("relationships is empty")
	}
	describesRoot := false
	for index, value := range relationships {
		relationship, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("relationships[%d] is not an object", index)
		}
		from, fromOK := stringField(relationship, "spdxElementId")
		to, toOK := stringField(relationship, "relatedSpdxElement")
		kind, kindOK := stringField(relationship, "relationshipType")
		if !fromOK || !toOK || !kindOK || kind == "" {
			return fmt.Errorf("relationships[%d] has missing fields", index)
		}
		if err := validateRelationshipReference(from, knownIDs); err != nil {
			return fmt.Errorf("relationships[%d].spdxElementId: %w", index, err)
		}
		if err := validateRelationshipReference(to, knownIDs); err != nil {
			return fmt.Errorf("relationships[%d].relatedSpdxElement: %w", index, err)
		}
		if from == "SPDXRef-DOCUMENT" && kind == "DESCRIBES" {
			describesRoot = true
		}
	}
	if !describesRoot {
		return errors.New("document has no DESCRIBES relationship")
	}
	return nil
}

func validateRootPackage(pkg map[string]any, digest string) error {
	version, ok := stringField(pkg, "versionInfo")
	if !ok || version != "sha256:"+digest {
		return fmt.Errorf("root package versionInfo = %q, want artifact SHA-256", version)
	}
	checksums, ok := arrayField(pkg, "checksums")
	if !ok {
		return errors.New("root package checksums is missing")
	}
	for _, value := range checksums {
		checksum, ok := value.(map[string]any)
		if !ok {
			continue
		}
		algorithm, algorithmOK := stringField(checksum, "algorithm")
		value, valueOK := stringField(checksum, "checksumValue")
		if algorithmOK && valueOK && algorithm == "SHA256" && value == digest {
			return nil
		}
	}
	return errors.New("root package lacks the artifact SHA-256 checksum")
}

func validateRelationshipReference(id string, known map[string]struct{}) error {
	if strings.HasPrefix(id, "DocumentRef-") || id == "NONE" || id == "NOASSERTION" {
		return nil
	}
	if _, ok := known[id]; !ok {
		return fmt.Errorf("unknown SPDXID %q", id)
	}
	return nil
}

func stringField(object map[string]any, key string) (string, bool) {
	value, ok := object[key].(string)
	return value, ok
}

func objectField(object map[string]any, key string) (map[string]any, bool) {
	value, ok := object[key].(map[string]any)
	return value, ok
}

func arrayField(object map[string]any, key string) ([]any, bool) {
	value, ok := object[key].([]any)
	return value, ok
}

func appendNormalizerCreator(creators []any) []any {
	if countString(creators, normalizerCreator) != 0 {
		return creators
	}
	result := append([]any(nil), creators...)
	return append(result, normalizerCreator)
}

func appendPolicyComment(creationInfo map[string]any) string {
	existing, _ := stringField(creationInfo, "comment")
	if strings.Contains(existing, policyComment) {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return policyComment
	}
	return existing + "\n" + policyComment
}

func countString(values []any, expected string) int {
	count := 0
	for _, value := range values {
		if text, ok := value.(string); ok && text == expected {
			count++
		}
	}
	return count
}

// semanticDocumentDigest deliberately excludes the two fields that are
// volatile in an unmodified Syft document. Package evidence, relationships,
// creator provenance, and the normalization policy all remain in the digest.
func semanticDocumentDigest(document map[string]any) (string, error) {
	canonical := make(map[string]any, len(document)-1)
	for key, value := range document {
		if key != "documentNamespace" {
			canonical[key] = value
		}
	}
	creationInfo, ok := objectField(document, "creationInfo")
	if !ok {
		return "", errors.New("creationInfo is missing")
	}
	canonicalCreationInfo := make(map[string]any, len(creationInfo)-1)
	for key, value := range creationInfo {
		if key != "created" {
			canonicalCreationInfo[key] = value
		}
	}
	canonical["creationInfo"] = canonicalCreationInfo
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode canonical SPDX content: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
