package nativeci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactSetIsSecretFreeAndTracksWrittenFiles(t *testing.T) {
	evidenceDir := t.TempDir()
	h := &harness{config: Config{EvidenceDir: evidenceDir, Profile: filepath.Join(t.TempDir(), "native-lifecycle-profile")}, token: "synthetic-forbidden-token", evidence: newEvidence("ubuntu-latest", "commit", time.Unix(0, 0))}
	if err := h.writeJSONArtifact("safe.json", map[string]any{"status": "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := h.writeTextArtifact("safe.txt", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := h.writeTextArtifact("unsafe.txt", h.token); err == nil {
		t.Fatal("token-bearing artifact was accepted")
	}
	if len(h.evidence.Artifacts) != 2 || h.evidence.Artifacts[0] != "safe.json" || h.evidence.Artifacts[1] != "safe.txt" {
		t.Fatalf("tracked artifacts = %v", h.evidence.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, "unsafe.txt")); !os.IsNotExist(err) {
		t.Fatalf("unsafe artifact was created: %v", err)
	}
}

func TestCaptureHealthWritesExactIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","version":"native-v2","runtime":"go-test"}`))
	}))
	defer server.Close()

	evidenceDir := t.TempDir()
	h := &harness{config: Config{EvidenceDir: evidenceDir}, token: "forbidden", client: &http.Client{Timeout: time.Second}}
	if err := h.captureHealthAt(context.Background(), "health.json", server.URL+"/health", "native-v2"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(evidenceDir, "health.json")
	artifact := healthArtifact{Endpoint: server.URL + "/health", Status: "ok", Version: "native-v2", Runtime: "go-test"}
	var decoded healthArtifact
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != artifact {
		t.Fatalf("health artifact = %#v, %v", decoded, err)
	}
}

func TestRollbackHashComparisonDetectsEveryOwnedFile(t *testing.T) {
	want := map[string]string{"binary": "a", "environment": "b", "service_definition": "c"}
	got := map[string]string{"binary": "a", "environment": "b", "service_definition": "c"}
	if err := requireHashesEqual(want, got); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"binary", "environment", "service_definition"} {
		drift := map[string]string{"binary": "a", "environment": "b", "service_definition": "c"}
		drift[role] = "changed"
		if err := requireHashesEqual(want, drift); err == nil {
			t.Fatalf("%s drift was accepted", role)
		}
	}
}

func TestArtifactInventoryRequiresEveryReviewedEvidenceFile(t *testing.T) {
	evidenceDir := t.TempDir()
	h := &harness{config: Config{EvidenceDir: evidenceDir}, token: "forbidden", evidence: newEvidence("ubuntu-latest", "commit", time.Unix(0, 0))}
	for _, name := range requiredLifecycleArtifacts {
		if err := h.writeTextArtifact(name, "safe"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.verifyArtifactSet(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(evidenceDir, requiredLifecycleArtifacts[0])); err != nil {
		t.Fatal(err)
	}
	if err := h.verifyArtifactSet(); err == nil {
		t.Fatal("missing reviewed artifact was accepted")
	}
}
