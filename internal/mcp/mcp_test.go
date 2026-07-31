package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/config"
)

var expectedReadNames = []string{
	"get_board", "get_board_structure", "get_card", "get_card_checklists", "get_card_children", "get_current_user",
	"get_member_cards", "get_my_cards", "get_responsible_cards", "get_server_info", "get_space", "list_boards",
	"list_card_types", "list_custom_properties", "list_spaces", "list_tags", "list_users", "search_cards",
}

var expectedWriteNames = []string{
	"add_checklist_item", "add_comment", "add_external_link", "add_watcher", "create_card", "create_checklist",
	"delete_checklist", "delete_checklist_item", "link_child_card", "move_card", "remove_member", "set_responsible",
	"unlink_child_card", "update_card", "update_checklist_item",
}

func TestToolRegistrationAndRepresentativeSchemas(t *testing.T) {
	readTools := tools(false)
	if len(readTools) != 18 || !reflect.DeepEqual(toolNames(readTools), expectedReadNames) {
		t.Fatalf("read registration mismatch: %v", toolNames(readTools))
	}
	allTools := tools(true)
	expected := append(append([]string(nil), expectedReadNames...), expectedWriteNames...)
	if len(allTools) != 33 || !reflect.DeepEqual(toolNames(allTools), expected) {
		t.Fatalf("write registration mismatch: %v", toolNames(allTools))
	}
	byName := make(map[string]Tool)
	for _, tool := range allTools {
		byName[tool.Name] = tool
	}
	getCard := byName["get_card"].InputSchema
	if !reflect.DeepEqual(getCard["required"], []string{"card_id"}) {
		t.Fatalf("required mismatch: %#v", getCard["required"])
	}
	properties := getCard["properties"].(map[string]any)
	if properties["include_members"].(map[string]any)["default"] != true || properties["include_comments"].(map[string]any)["default"] != false {
		t.Fatal("get_card defaults mismatch")
	}
	search := byName["search_cards"].InputSchema["properties"].(map[string]any)
	limit := search["limit"].(map[string]any)
	if limit["minimum"] != float64(0) || limit["default"] != nil || !reflect.DeepEqual(limit["type"], []string{"integer", "null"}) {
		t.Fatalf("pagination schema mismatch: %#v", limit)
	}
	create := byName["create_card"].InputSchema["properties"].(map[string]any)
	propertyMap := create["properties"].(map[string]any)
	if !reflect.DeepEqual(propertyMap["type"], []string{"object", "null"}) {
		t.Fatalf("custom property schema mismatch: %#v", propertyMap)
	}
	if byName["remove_member"].Annotations["destructiveHint"] != true || byName["list_spaces"].Annotations["readOnlyHint"] != true {
		t.Fatal("safety annotations mismatch")
	}
}

func toolNames(values []Tool) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}

func fakeAPIServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer mcp-test-token" {
			t.Errorf("missing bearer header")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			_, _ = io.WriteString(w, `{"id":99}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/spaces":
			_, _ = io.WriteString(w, `[{"id":1,"title":"Example Space"}]`)
		case "/api/v1/spaces/1/boards":
			_, _ = io.WriteString(w, `[{"id":2,"title":"Example Board"}]`)
		case "/api/v1/boards/2":
			_, _ = io.WriteString(w, `{"id":2,"title":"Example Board","columns":[{"id":3,"title":"Ready"}],"lanes":[{"id":4,"title":"Main"}]}`)
		case "/api/v1/users":
			_, _ = io.WriteString(w, `[{"id":6,"username":"sample.user","full_name":"Sample User","email":"sample@example.test"}]`)
		case "/api/v1/users/current":
			_, _ = io.WriteString(w, `{"id":6,"username":"sample.user"}`)
		case "/api/v1/company/custom-properties":
			_, _ = io.WriteString(w, `[{"id":11,"name":"Effort","type":"number"}]`)
		case "/api/v1/cards":
			_, _ = io.WriteString(w, `[]`)
		case "/api/v1/cards/5":
			_, _ = io.WriteString(w, `{"id":5,"board_id":2,"checklists":[{"id":7}]}`)
		case "/api/v1/card-types", "/api/v1/tags":
			_, _ = io.WriteString(w, `[]`)
		default:
			if strings.Contains(r.URL.Path, "/comments") || strings.Contains(r.URL.Path, "/members") || strings.Contains(r.URL.Path, "/children") {
				_, _ = io.WriteString(w, `[]`)
			} else {
				_, _ = io.WriteString(w, `{"id":1}`)
			}
		}
	}))
}

func testServer(t *testing.T, writeEnabled bool) (*Server, *httptest.Server, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	upstream := fakeAPIServer(t, requests)
	t.Cleanup(upstream.Close)
	base, _ := url.Parse(upstream.URL)
	cfg := config.Config{
		Token: "mcp-test-token", BaseURL: base, APIPrefix: "/api/v1", RateLimitRPS: 10000,
		CacheTTL: time.Minute, MaxConcurrency: 8, Timeout: 2 * time.Second, EnableWriteTools: writeEnabled,
	}
	return NewServer(cfg), upstream, requests
}

func TestEveryToolHasSuccessAndSchemaErrorPath(t *testing.T) {
	server, _, _ := testServer(t, true)
	calls := map[string]map[string]any{
		"get_board":              {"board_id": float64(2)},
		"get_board_structure":    {"board": "2"},
		"get_card":               {"card_id": float64(5), "include_comments": true},
		"get_card_checklists":    {"card_id": float64(5)},
		"get_card_children":      {"card_id": float64(5)},
		"get_current_user":       {},
		"get_member_cards":       {"user": "sample.user"},
		"get_my_cards":           {},
		"get_responsible_cards":  {"user": "sample.user"},
		"get_server_info":        {},
		"get_space":              {"space_id": float64(1)},
		"list_boards":            {"space_id": float64(1)},
		"list_card_types":        {},
		"list_custom_properties": {},
		"list_spaces":            {},
		"list_tags":              {},
		"list_users":             {},
		"search_cards":           {"query": "sample"},
		"add_checklist_item":     {"card_id": float64(5), "checklist_id": float64(7), "text": "Check"},
		"add_comment":            {"card_id": float64(5), "text": "Comment"},
		"add_external_link":      {"card_id": float64(5), "url": "https://example.test/reference"},
		"add_watcher":            {"card_id": float64(5), "user": "sample.user"},
		"create_card":            {"title": "Created", "board": "2", "column": "3"},
		"create_checklist":       {"card_id": float64(5), "name": "Review"},
		"delete_checklist":       {"card_id": float64(5), "checklist_id": float64(7)},
		"delete_checklist_item":  {"card_id": float64(5), "checklist_id": float64(7), "item_id": float64(9)},
		"link_child_card":        {"parent_card_id": float64(5), "child_card_id": float64(8)},
		"move_card":              {"card_id": float64(5), "column": "3", "board": "2"},
		"remove_member":          {"card_id": float64(5), "user": "sample.user"},
		"set_responsible":        {"card_id": float64(5), "user": "sample.user"},
		"unlink_child_card":      {"parent_card_id": float64(5), "child_card_id": float64(8)},
		"update_card":            {"card_id": float64(5), "title": "Updated"},
		"update_checklist_item":  {"card_id": float64(5), "checklist_id": float64(7), "item_id": float64(9), "checked": true},
	}
	allSpecs := append(append([]toolSpec(nil), readSpecs...), writeSpecs...)
	for index, spec := range allSpecs {
		t.Run(spec.name, func(t *testing.T) {
			arguments, ok := calls[spec.name]
			if !ok {
				t.Fatal("missing success fixture")
			}
			params, _ := json.Marshal(map[string]any{"name": spec.name, "arguments": arguments})
			response := server.handle(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", index+1)), Method: "tools/call", Params: params})
			if response.Error != nil {
				t.Fatalf("protocol error: %+v", response.Error)
			}
			result, ok := response.Result.(toolResult)
			if !ok || result.StructuredContent["ok"] != true || len(result.Content) != 1 {
				t.Fatalf("bad success result: %#v", response.Result)
			}
			var textEnvelope map[string]any
			structuredJSON, _ := json.Marshal(result.StructuredContent)
			var normalizedStructured map[string]any
			_ = json.Unmarshal(structuredJSON, &normalizedStructured)
			if err := json.Unmarshal([]byte(result.Content[0].Text), &textEnvelope); err != nil || !reflect.DeepEqual(textEnvelope, normalizedStructured) {
				t.Fatalf("text and structured envelopes differ: %v", err)
			}

			invalid := cloneArguments(arguments)
			if len(spec.required) > 0 {
				delete(invalid, spec.required[0])
			} else {
				invalid["unexpected"] = true
			}
			params, _ = json.Marshal(map[string]any{"name": spec.name, "arguments": invalid})
			response = server.handle(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("999"), Method: "tools/call", Params: params})
			if response.Error == nil || response.Error.Code != -32602 {
				t.Fatalf("expected schema protocol error, got %#v", response)
			}
		})
	}
}

func cloneArguments(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func TestDomainErrorEnvelopeIsNotProtocolError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cfg := config.Config{Token: "x", BaseURL: base, APIPrefix: "/api/v1", RateLimitRPS: 10000, CacheTTL: 0, MaxConcurrency: 1, Timeout: time.Second}
	server := NewServer(cfg)
	params, _ := json.Marshal(map[string]any{"name": "get_space", "arguments": map[string]any{"space_id": 77}})
	response := server.handle(context.Background(), rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
	if response.Error != nil {
		t.Fatalf("domain error became protocol error: %+v", response.Error)
	}
	result := response.Result.(toolResult)
	if result.IsError || result.StructuredContent["ok"] != false {
		t.Fatalf("bad domain failure: %#v", result)
	}
	errorObject := result.StructuredContent["error"].(map[string]any)
	if errorObject["type"] != "not_found" || errorObject["status_code"] != http.StatusNotFound {
		t.Fatalf("bad classified error: %#v", errorObject)
	}
}

func TestStdioInitializationAndReadOnlyTools(t *testing.T) {
	server, _, _ := testServer(t, false)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.ServeStdio(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected stdio messages: %q", output.String())
	}
	var initialized map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialized); err != nil {
		t.Fatal(err)
	}
	result := initialized["result"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocol was hard-coded: %#v", result)
	}
	var listed struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil || len(listed.Result.Tools) != 18 {
		t.Fatalf("bad tools/list: %v count=%d", err, len(listed.Result.Tools))
	}
}

func TestStreamableHTTPInitializationHealthAndShutdown(t *testing.T) {
	server, _, _ := testServer(t, false)
	handler := server.HTTPHandler("/rpc")
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/rpc", body)
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Mcp-Session-Id") == "" || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("bad initialize response: status=%d session=%q content=%q", response.StatusCode, response.Header.Get("Mcp-Session-Id"), response.Header.Get("Content-Type"))
	}
	health, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	var healthBody map[string]any
	if err := json.NewDecoder(health.Body).Decode(&healthBody); err != nil || healthBody["status"] != "ok" || healthBody["version"] == nil || healthBody["runtime"] == nil {
		t.Fatalf("bad health: %#v err=%v", healthBody, err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ServeHTTP(ctx, "127.0.0.1", port, "/mcp") }()
	waitForListener(t, port)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP server did not shut down")
	}
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("listener remained bound after cancellation")
	}
}

func TestHTTPRejectsUnsafeOriginAndHealthMutation(t *testing.T) {
	server, _, _ := testServer(t, false)
	httpServer := httptest.NewServer(server.HTTPHandler("/mcp"))
	defer httpServer.Close()
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	request.Header.Set("Origin", "http://localhost.evil.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unsafe origin status: %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/health", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("health accepted mutation method: %d", response.StatusCode)
	}
}

func waitForListener(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener did not start")
}

func TestRuntimeFlags(t *testing.T) {
	overrides, err := parseRuntimeFlags([]string{"--transport", "streamable-http", "--host=localhost", "--port", "0", "--streamable-http-path", "rpc"})
	if err != nil || overrides.Transport != "streamable-http" || overrides.Host != "localhost" || overrides.Port != 0 || overrides.Path != "rpc" {
		t.Fatalf("bad overrides: %+v err=%v", overrides, err)
	}
	for _, args := range [][]string{{"--transport", "sse"}, {"--port", "70000"}, {"--unknown", "x"}, {"positional"}} {
		if _, err := parseRuntimeFlags(args); err == nil {
			t.Fatalf("expected failure for %v", args)
		}
	}
}

func TestProtocolNegotiationFallsBackFromUnknownVersion(t *testing.T) {
	if got := negotiateProtocol("2099-01-01"); got != fallbackProtocolVersion {
		t.Fatalf("unknown protocol negotiated as %q", got)
	}
	for _, supported := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
		if got := negotiateProtocol(supported); got != supported {
			t.Fatalf("supported protocol %s negotiated as %s", supported, got)
		}
	}
}

func TestCustomPropertyResolutionBeforeMutation(t *testing.T) {
	var mutations atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			mutations.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			properties := body["properties"].(map[string]any)
			if properties["id_11"] != float64(3.5) || properties["id_12"] != float64(21) {
				t.Errorf("resolved properties mismatch: %#v", properties)
			}
			labels := properties["id_13"].([]any)
			if !reflect.DeepEqual(labels, []any{float64(31), float64(32)}) {
				t.Errorf("multi-select mismatch: %#v", labels)
			}
			_, _ = io.WriteString(w, `{"id":5}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/spaces":
			_, _ = io.WriteString(w, `[{"id":1,"title":"Space"}]`)
		case "/api/v1/spaces/1/boards":
			_, _ = io.WriteString(w, `[{"id":2,"title":"Board"}]`)
		case "/api/v1/boards/2":
			_, _ = io.WriteString(w, `{"id":2,"columns":[{"id":3,"title":"Ready"}]}`)
		case "/api/v1/company/custom-properties":
			_, _ = io.WriteString(w, `[{"id":11,"name":"Effort","type":"number"},{"id":12,"name":"Stage","type":"select","multi_select":false},{"id":13,"name":"Labels","type":"select","multi_select":true}]`)
		case "/api/v1/company/custom-properties/12/select-values":
			_, _ = io.WriteString(w, `[{"id":21,"value":"Ready"}]`)
		case "/api/v1/company/custom-properties/13/select-values":
			_, _ = io.WriteString(w, `[{"id":31,"value":"One"},{"id":32,"value":"Two"}]`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cfg := config.Config{Token: "property-test", BaseURL: base, APIPrefix: "/api/v1", RateLimitRPS: 10000, CacheTTL: time.Minute, MaxConcurrency: 4, Timeout: time.Second, EnableWriteTools: true}
	server := NewServer(cfg)
	arguments := map[string]any{
		"title": "Property card", "board": "2", "column": "3",
		"properties": map[string]any{"effort": "3.5", "stage": "ready", "labels": "one, two"},
	}
	result := server.callTool(context.Background(), writeSpecs[4], arguments)
	if result.StructuredContent["ok"] != true || mutations.Load() != 1 {
		t.Fatalf("property mutation failed: %#v mutations=%d", result.StructuredContent, mutations.Load())
	}
}

func TestAmbiguousPropertyStopsBeforeMutation(t *testing.T) {
	var mutations atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/spaces":
			_, _ = io.WriteString(w, `[{"id":1,"title":"Space"}]`)
		case "/api/v1/spaces/1/boards":
			_, _ = io.WriteString(w, `[{"id":2,"title":"Board"}]`)
		case "/api/v1/boards/2":
			_, _ = io.WriteString(w, `{"columns":[{"id":3,"title":"Ready"}]}`)
		case "/api/v1/company/custom-properties":
			_, _ = io.WriteString(w, `[{"id":11,"name":"Roadmap","type":"string"},{"id":12,"name":"Roadblock","type":"string"}]`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cfg := config.Config{Token: "ambiguity-test", BaseURL: base, APIPrefix: "/api/v1", RateLimitRPS: 10000, CacheTTL: time.Minute, MaxConcurrency: 4, Timeout: time.Second, EnableWriteTools: true}
	server := NewServer(cfg)
	result := server.callTool(context.Background(), writeSpecs[4], map[string]any{
		"title": "No mutation", "board": "2", "column": "3", "properties": map[string]any{"road": "value"},
	})
	if result.StructuredContent["ok"] != false || mutations.Load() != 0 {
		t.Fatalf("ambiguous property reached mutation: %#v calls=%d", result.StructuredContent, mutations.Load())
	}
}

func TestToolNamesAreUniqueAndSortedWithinModes(t *testing.T) {
	for _, specs := range [][]toolSpec{readSpecs, writeSpecs} {
		names := make([]string, len(specs))
		for index, spec := range specs {
			names[index] = spec.name
		}
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		if !reflect.DeepEqual(names, sorted) {
			t.Fatalf("tools are not deterministic: %v", names)
		}
	}
}
