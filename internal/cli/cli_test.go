package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func configureCLI(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("KAITEN_API_TOKEN", "contract-test-token")
	t.Setenv("KAITEN_TOKEN", "")
	t.Setenv("KAITEN_URL", serverURL)
	t.Setenv("KAITEN_BASE_URL", "")
	t.Setenv("KAITEN_RATE_LIMIT_RPS", "10000")
	t.Setenv("KAITEN_MAX_CONCURRENCY", "4")
	t.Setenv("KAITEN_CACHE_TTL_SECONDS", "0")
}

func fakeKaiten(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer contract-test-token" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("required headers missing")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			switch {
			case r.URL.Path == "/api/v1/spaces":
				_, _ = io.WriteString(w, `[{"id":1,"title":"Example Space"}]`)
			case r.URL.Path == "/api/v1/spaces/1/boards":
				_, _ = io.WriteString(w, `[{"id":2,"title":"Example Board"}]`)
			case r.URL.Path == "/api/v1/boards/2":
				_, _ = io.WriteString(w, `{"id":2,"columns":[{"id":3,"title":"Ready"}],"lanes":[{"id":4,"title":"Main"}]}`)
			case strings.HasSuffix(r.URL.Path, "/checklists/7"):
				_, _ = io.WriteString(w, `{"id":7,"items":[]}`)
			case r.URL.Path == "/api/v1/cards" || strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/members") || strings.HasSuffix(r.URL.Path, "/tags") || strings.HasSuffix(r.URL.Path, "/columns") || strings.HasSuffix(r.URL.Path, "/lanes") || r.URL.Path == "/api/v1/tags":
				_, _ = io.WriteString(w, `[]`)
			default:
				_, _ = io.WriteString(w, `{"id":1}`)
			}
			return
		}
		_, _ = io.WriteString(w, `{"id":99}`)
	}))
}

func TestEveryDataCommandProducesDocumentedJSONKind(t *testing.T) {
	var requests atomic.Int32
	server := fakeKaiten(t, &requests)
	defer server.Close()
	configureCLI(t, server.URL)
	tests := []struct {
		args  []string
		array bool
	}{
		{[]string{"spaces", "list"}, true},
		{[]string{"spaces", "get", "1"}, false},
		{[]string{"boards", "list", "--space-id", "1"}, true},
		{[]string{"boards", "get", "2"}, false},
		{[]string{"columns", "list", "--board-id", "2"}, true},
		{[]string{"lanes", "list", "--board-id", "2"}, true},
		{[]string{"cards", "list", "--board-id", "2"}, true},
		{[]string{"cards", "get", "5"}, false},
		{[]string{"cards", "create", "--title", "Created", "--board-id", "2", "--column-id", "3"}, false},
		{[]string{"cards", "update", "5", "--title", "Updated"}, false},
		{[]string{"cards", "archive", "5"}, false},
		{[]string{"cards", "unarchive", "5"}, false},
		{[]string{"cards", "delete", "5"}, false},
		{[]string{"comments", "list", "--card-id", "5"}, true},
		{[]string{"comments", "add", "--card-id", "5", "--text", "Comment"}, false},
		{[]string{"blockers", "block", "--card-id", "5", "--reason", "Waiting"}, false},
		{[]string{"blockers", "unblock", "--card-id", "5", "--blocker-id", "6"}, false},
		{[]string{"blockers", "delete", "--card-id", "5", "--blocker-id", "6"}, false},
		{[]string{"members", "list", "--card-id", "5"}, true},
		{[]string{"tags", "list"}, true},
		{[]string{"tags", "card-tags", "--card-id", "5"}, true},
		{[]string{"tags", "add", "--card-id", "5", "--name", "Priority"}, false},
		{[]string{"tags", "remove", "--card-id", "5", "--tag-id", "8"}, false},
		{[]string{"checklists", "get", "--card-id", "5", "--checklist-id", "7"}, false},
		{[]string{"checklists", "create", "--card-id", "5"}, false},
		{[]string{"checklists", "delete", "--card-id", "5", "--checklist-id", "7"}, false},
		{[]string{"checklists", "add-item", "--card-id", "5", "--checklist-id", "7", "--text", "Verify"}, false},
		{[]string{"checklists", "check", "--card-id", "5", "--checklist-id", "7", "--item-id", "9"}, false},
		{[]string{"checklists", "uncheck", "--card-id", "5", "--checklist-id", "7", "--item-id", "9"}, false},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if status := Run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr, Dependencies{}); status != 0 {
				t.Fatalf("status %d, stderr=%s", status, stderr.String())
			}
			var decoded any
			if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
				t.Fatalf("invalid JSON %q: %v", stdout.String(), err)
			}
			_, isArray := decoded.([]any)
			if isArray != test.array {
				t.Fatalf("unexpected top-level kind: %T", decoded)
			}
		})
	}
}

func TestInvalidInputDoesNotLoadConfigOrSendRequest(t *testing.T) {
	var requests atomic.Int32
	server := fakeKaiten(t, &requests)
	defer server.Close()
	configureCLI(t, server.URL)
	tests := [][]string{
		{"spaces", "get", "0"},
		{"cards", "list", "--board-id", "2", "--archived", "--all"},
		{"cards", "create", "--title", "x", "--board-id", "2"},
		{"cards", "create", "--title", "x", "--board-id", "2", "--column-id", "3", "--column-name", "Ready"},
		{"cards", "update", "5"},
		{"comments", "add", "--card-id", "5", "--text", ""},
		{"checklists", "check", "--card-id", "5", "--checklist-id", "7", "--item-id", "bad"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if status := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, Dependencies{}); status == 0 {
			t.Fatalf("expected failure for %v", args)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("bad error streams for %v: out=%q err=%q", args, stdout.String(), stderr.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("validation sent %d request(s)", requests.Load())
	}
}

func TestHelpVersionAndCompletionNeedNoCredentials(t *testing.T) {
	for _, name := range []string{"KAITEN_API_TOKEN", "KAITEN_TOKEN", "KAITEN_URL", "KAITEN_BASE_URL"} {
		t.Setenv(name, "")
	}
	commands := [][]string{{"--help"}, {"--version"}, {"completion", "zsh"}, {"cards", "--help"}}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		if status := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, Dependencies{}); status != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("offline command failed %v: status=%d out=%q err=%q", args, status, stdout.String(), stderr.String())
		}
	}
	for _, spec := range commandSpecs {
		var stdout, stderr bytes.Buffer
		args := []string{spec.group, spec.name, "--help"}
		if status := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, Dependencies{}); status != 0 {
			t.Fatalf("help failed for %s: %s", spec.usage, stderr.String())
		}
		for _, flag := range spec.flags {
			if !strings.Contains(stdout.String(), "--"+flag.name) {
				t.Errorf("help for %s omits --%s", spec.usage, flag.name)
			}
		}
	}
}

func TestMCPSubcommandHelpIsOfflineAndSideEffectFree(t *testing.T) {
	for _, name := range []string{"KAITEN_API_TOKEN", "KAITEN_TOKEN", "KAITEN_URL", "KAITEN_BASE_URL"} {
		t.Setenv(name, "")
	}
	tests := []struct {
		name       string
		standalone bool
		args       []string
		want       string
	}{
		{name: "embedded install", args: []string{"install", "--help"}, want: "Usage: kaiten mcp install\n"},
		{name: "embedded uninstall", args: []string{"uninstall", "--help"}, want: "Usage: kaiten mcp uninstall\n"},
		{name: "embedded version", args: []string{"version", "--help"}, want: "Usage: kaiten mcp version\n"},
		{name: "standalone install", standalone: true, args: []string{"install", "--help"}, want: "Usage: kaiten-mcp install\n"},
		{name: "standalone uninstall", standalone: true, args: []string{"uninstall", "--help"}, want: "Usage: kaiten-mcp uninstall\n"},
		{name: "standalone version", standalone: true, args: []string{"version", "--help"}, want: "Usage: kaiten-mcp version\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var callbacks atomic.Int32
			dependencies := Dependencies{
				MCPRun: func(context.Context, []string, io.Reader, io.Writer, io.Writer) int {
					callbacks.Add(1)
					return 99
				},
				MCPInstall: func(context.Context, io.Reader, io.Writer, io.Writer) int {
					callbacks.Add(1)
					return 99
				},
				MCPUninstall: func(context.Context, io.Reader, io.Writer, io.Writer) int {
					callbacks.Add(1)
					return 99
				},
			}
			var stdout, stderr bytes.Buffer
			var status int
			if test.standalone {
				status = RunStandaloneMCP(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr, dependencies)
			} else {
				args := append([]string{"mcp"}, test.args...)
				status = Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, dependencies)
			}
			if status != 0 || stdout.String() != test.want || stderr.Len() != 0 {
				t.Fatalf("help mismatch: status=%d out=%q err=%q", status, stdout.String(), stderr.String())
			}
			if callbacks.Load() != 0 {
				t.Fatalf("help invoked %d lifecycle callback(s)", callbacks.Load())
			}
		})
	}
}

func TestAPIErrorIsConciseAndSecretFree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"message":"do not expose response body"}`)
	}))
	defer server.Close()
	configureCLI(t, server.URL)
	var stdout, stderr bytes.Buffer
	status := Run(context.Background(), []string{"spaces", "list"}, strings.NewReader(""), &stdout, &stderr, Dependencies{})
	if status == 0 || stdout.Len() != 0 || strings.Contains(stderr.String(), "contract-test-token") || strings.Contains(stderr.String(), "do not expose") {
		t.Fatalf("unsafe error: status=%d out=%q err=%q", status, stdout.String(), stderr.String())
	}
}

func TestUpdateSendsOnlyExplicitFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/cards/5" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"id":5}`)
	}))
	defer server.Close()
	configureCLI(t, server.URL)
	var stdout, stderr bytes.Buffer
	status := Run(context.Background(), []string{"cards", "update", "5", "--description=", "--size", "0"}, strings.NewReader(""), &stdout, &stderr, Dependencies{})
	if status != 0 {
		t.Fatalf("update failed: %s", stderr.String())
	}
	if len(body) != 2 || body["description"] != nil || body["size_text"] != "0" {
		t.Fatalf("unexpected body: %#v", body)
	}
}
