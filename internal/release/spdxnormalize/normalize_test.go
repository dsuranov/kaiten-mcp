package spdxnormalize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeIsDeterministicAndPreservesEvidence(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	artifact := filepath.Join(directory, "kaiten_linux_amd64.tar.gz")
	writeTestFile(t, artifact, []byte("stable release archive"))
	digestBytes := sha256.Sum256([]byte("stable release archive"))
	digest := hex.EncodeToString(digestBytes[:])
	firstInput := filepath.Join(directory, "first.syft.json")
	secondInput := filepath.Join(directory, "second.syft.json")
	writeTestDocument(t, firstInput, testDocument("2026-01-01T01:02:03Z", "https://anchore.example/one", filepath.Base(artifact), digest))
	writeTestDocument(t, secondInput, testDocument("2026-02-02T02:03:04Z", "https://anchore.example/two", filepath.Base(artifact), digest))
	firstOutput := filepath.Join(directory, "first.json")
	secondOutput := filepath.Join(directory, "second.json")
	created := time.Date(2026, time.July, 31, 14, 1, 42, 0, time.UTC)
	if err := Normalize(firstInput, firstOutput, artifact, created); err != nil {
		t.Fatal(err)
	}
	if err := Normalize(secondInput, secondOutput, artifact, created); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(secondOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("normalization produced different bytes for documents that differed only in volatile fields")
	}
	if err := Validate(firstOutput, artifact, created); err != nil {
		t.Fatal(err)
	}

	var normalized map[string]any
	if err := json.Unmarshal(first, &normalized); err != nil {
		t.Fatal(err)
	}
	packages := normalized["packages"].([]any)
	dependency := packages[0].(map[string]any)
	if dependency["versionInfo"] != "v1.2.3" {
		t.Fatalf("package evidence changed: %#v", dependency)
	}
	relationships := normalized["relationships"].([]any)
	if len(relationships) != 2 {
		t.Fatalf("relationship evidence changed: %#v", relationships)
	}
}

func TestNormalizeRejectsMismatchedArtifactChecksum(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	artifact := filepath.Join(directory, "artifact.zip")
	writeTestFile(t, artifact, []byte("artifact"))
	input := filepath.Join(directory, "input.json")
	writeTestDocument(t, input, testDocument("2026-01-01T00:00:00Z", "https://example.test/random", filepath.Base(artifact), "deadbeef"))
	err := Normalize(input, filepath.Join(directory, "output.json"), artifact, time.Now())
	if err == nil {
		t.Fatal("expected artifact checksum mismatch")
	}
}

func TestValidateRejectsNonDeterministicNamespace(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	artifact := filepath.Join(directory, "artifact.tar.gz")
	writeTestFile(t, artifact, []byte("artifact"))
	digestBytes := sha256.Sum256([]byte("artifact"))
	digest := hex.EncodeToString(digestBytes[:])
	document := filepath.Join(directory, "document.json")
	created := time.Date(2026, time.July, 31, 14, 1, 42, 0, time.UTC)
	writeTestDocument(t, document, testDocument(created.Format(time.RFC3339), "https://example.test/random", filepath.Base(artifact), digest))
	if err := Validate(document, artifact, created); err == nil {
		t.Fatal("expected non-deterministic namespace to fail validation")
	}
}

func testDocument(created, namespace, artifactName, digest string) map[string]any {
	return map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              artifactName,
		"documentNamespace": namespace,
		"creationInfo": map[string]any{
			"created":  created,
			"creators": []any{"Tool: syft-test"},
		},
		"packages": []any{
			map[string]any{
				"SPDXID":           "SPDXRef-Package-Dependency",
				"name":             "example.test/dependency",
				"versionInfo":      "v1.2.3",
				"filesAnalyzed":    false,
				"downloadLocation": "NOASSERTION",
			},
			map[string]any{
				"SPDXID":           "SPDXRef-Package-Root",
				"name":             artifactName,
				"versionInfo":      "sha256:" + digest,
				"filesAnalyzed":    false,
				"downloadLocation": "NOASSERTION",
				"checksums": []any{map[string]any{
					"algorithm":     "SHA256",
					"checksumValue": digest,
				}},
			},
		},
		"relationships": []any{
			map[string]any{
				"spdxElementId":      "SPDXRef-DOCUMENT",
				"relatedSpdxElement": "SPDXRef-Package-Root",
				"relationshipType":   "DESCRIBES",
			},
			map[string]any{
				"spdxElementId":      "SPDXRef-Package-Root",
				"relatedSpdxElement": "SPDXRef-Package-Dependency",
				"relationshipType":   "CONTAINS",
			},
		},
	}
}

func writeTestDocument(t *testing.T, path string, document map[string]any) {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, data)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
