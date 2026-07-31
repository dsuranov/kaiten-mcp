package nativeci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvidenceNeverPersistsSyntheticToken(t *testing.T) {
	token, err := syntheticToken([]byte("deterministic-newly-authored-seed"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := newEvidence("ubuntu-latest", strings.Repeat("a", 40), time.Unix(0, 0))
	evidence.Checks = append(evidence.Checks, Check{Name: "bad", Detail: token})
	path := filepath.Join(t.TempDir(), "summary.json")
	if err := writeEvidence(path, evidence, token); err == nil {
		t.Fatal("expected secret-containment failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unsafe evidence was written: %v", err)
	}
}

func TestRedactReplacesEphemeralPathsAndRejectsToken(t *testing.T) {
	value, err := redact("ready /tmp/native-profile", "marker", map[string]string{"/tmp/native-profile": "$PROFILE"})
	if err != nil || value != "ready $PROFILE" {
		t.Fatalf("redact = %q, %v", value, err)
	}
	if _, err := redact("leak marker", "marker", nil); err == nil {
		t.Fatal("expected token rejection")
	}
}
