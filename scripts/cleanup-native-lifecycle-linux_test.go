//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeLifecycleWrappersRetainPortableAndIsolatedRuntimePolicy(t *testing.T) {
	runData, err := os.ReadFile("run-native-lifecycle-ci.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := string(runData)
	for _, required := range []string{
		`if [[ -e "$evidence" || -L "$evidence" ]]; then`,
		`mkdir "$evidence"`,
		`if [[ "${RUNNER_OS:-}" != "Windows" ]]; then`,
		`chmod 0700 "$evidence"`,
		`sudo systemctl start "user-runtime-dir@$uid.service"`,
		`if [[ ! -d "$runtime_dir" || -L "$runtime_dir" ]]; then`,
		`if [[ "$(stat -c '%u:%g' "$runtime_dir")" != "$uid:$gid" ]]; then`,
		`sudo loginctl enable-linger "$native_user"`,
		`sudo systemctl start "user@$uid.service"`,
	} {
		if !strings.Contains(run, required) {
			t.Fatalf("run wrapper is missing reviewed fragment %q", required)
		}
	}
	if strings.Contains(run, `mkdir -m 0700 "$evidence"`) {
		t.Fatal("run wrapper retained the Windows-incompatible mkdir mode flag")
	}
	previous := -1
	for _, ordered := range []string{
		`if [[ -e "$evidence" || -L "$evidence" ]]; then`,
		`mkdir "$evidence"`,
		`sudo systemctl start "user-runtime-dir@$uid.service"`,
		`if [[ ! -d "$runtime_dir" || -L "$runtime_dir" ]]; then`,
		`if [[ "$(stat -c '%u:%g' "$runtime_dir")" != "$uid:$gid" ]]; then`,
		`sudo loginctl enable-linger "$native_user"`,
		`sudo systemctl start "user@$uid.service"`,
	} {
		index := strings.Index(run, ordered)
		if index <= previous {
			t.Fatalf("run wrapper ordering failed at %q", ordered)
		}
		previous = index
	}

	cleanupData, err := os.ReadFile("cleanup-native-lifecycle-linux.sh")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := string(cleanupData)
	for _, required := range []string{
		`sudo systemctl stop "user@${native_uid}.service"`,
		`manager_state="$(sudo systemctl show "user@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo systemctl stop "user-runtime-dir@${native_uid}.service"`,
		`runtime_dir_state="$(sudo systemctl show "user-runtime-dir@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo loginctl disable-linger "$native_user" >/dev/null 2>&1 || true`,
		`if linger_state="$(sudo loginctl show-user "$native_user" --property=Linger --value 2>/dev/null)"; then`,
		`elif [[ ! -e "$linger_record" && ! -L "$linger_record" && ! -e "$linger_marker" && ! -L "$linger_marker" ]]; then`,
	} {
		if !strings.Contains(cleanup, required) {
			t.Fatalf("cleanup helper is missing reviewed fragment %q", required)
		}
	}
	previous = -1
	for _, ordered := range []string{
		`sudo systemctl stop "user@${native_uid}.service"`,
		`manager_state="$(sudo systemctl show "user@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo systemctl stop "user-runtime-dir@${native_uid}.service"`,
		`runtime_dir_state="$(sudo systemctl show "user-runtime-dir@${native_uid}.service" --property=ActiveState --value 2>/dev/null)"`,
		`sudo loginctl disable-linger "$native_user" >/dev/null 2>&1 || true`,
		`if linger_state="$(sudo loginctl show-user "$native_user" --property=Linger --value 2>/dev/null)"; then`,
	} {
		index := strings.Index(cleanup, ordered)
		if index <= previous {
			t.Fatalf("cleanup helper ordering failed at %q", ordered)
		}
		previous = index
	}
}

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
	evidence, err = filepath.EvalSymlinks(evidence)
	if err != nil {
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
