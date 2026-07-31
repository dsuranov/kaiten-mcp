//go:build ignore

package main

import "testing"

func TestWindowsReachableShellScriptsRequireExplicitBash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{
			name: "direct Windows runner is rejected",
			raw: `jobs:
  test:
    runs-on: windows-latest
    steps:
      - name: Verify
        run: ./scripts/verify.sh
`,
			want: 1,
		},
		{
			name: "direct Windows runner with Bash passes",
			raw: `jobs:
  test:
    runs-on: windows-latest
    steps:
      - name: Verify
        shell: bash
        run: ./scripts/verify.sh
`,
		},
		{
			name: "matrix list Windows runner is rejected",
			raw: `jobs:
  test:
    strategy:
      matrix:
        os:
          - ubuntu-latest
          - windows-latest
    runs-on: ${{ matrix.os }}
    steps:
      - name: Verify
        run: ./scripts/verify.sh
`,
			want: 1,
		},
		{
			name: "matrix include Windows runner is rejected",
			raw: `jobs:
  native:
    strategy:
      matrix:
        include:
          - id: linux
            runner: ubuntu-latest
          - id: windows
            runner: windows-latest
    runs-on: ${{ matrix.runner }}
    steps:
      - name: Verify
        run: |
          ./scripts/verify.sh
`,
			want: 1,
		},
		{
			name: "Windows cross target on Ubuntu is not a Windows runner",
			raw: `jobs:
  cross-build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        target:
          - linux-amd64
          - windows-amd64
    steps:
      - name: Verify
        run: ./scripts/verify.sh
`,
		},
		{
			name: "Windows runner without shell script passes",
			raw: `jobs:
  test:
    runs-on: windows-latest
    steps:
      - name: Test
        run: go test ./...
`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := len(windowsShellViolations(test.raw)); got != test.want {
				t.Fatalf("windowsShellViolations() = %d, want %d", got, test.want)
			}
		})
	}
}
