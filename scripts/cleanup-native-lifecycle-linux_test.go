//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLinuxCleanupRejectsUnreviewedTargetWithoutMutatingSource(t *testing.T) {
	source := "cleanup-native-lifecycle-linux.sh"
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm()&0o111 == 0 {
		t.Fatalf("tracked cleanup script is not executable: mode %04o", before.Mode().Perm())
	}

	temporary := t.TempDir()
	copyPath := filepath.Join(temporary, "cleanup-native-lifecycle-linux.sh")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(temporary, "evidence")
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(temporary, "unreviewed-stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(copyPath, "kaitenci", "12345", stage, evidence)
	command.Env = append(os.Environ(), "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT=1")
	if err := command.Run(); err == nil {
		t.Fatal("cleanup helper accepted a stage outside its exact /tmp workflow target")
	}

	proofData, err := os.ReadFile(filepath.Join(evidence, "linux-wrapper-cleanup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proof struct {
		Schema          string `json:"schema"`
		Result          string `json:"result"`
		TargetValidated bool   `json:"target_validated"`
	}
	if err := json.Unmarshal(proofData, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.Schema != "kaiten-linux-wrapper-cleanup/v1" || proof.Result != "failed" || proof.TargetValidated {
		t.Fatalf("unexpected rejected-target proof: %+v", proof)
	}

	after, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("cleanup test mutated tracked source mode: before=%04o after=%04o", before.Mode().Perm(), after.Mode().Perm())
	}
}
