package nativeci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type mockAPI struct {
	server       *http.Server
	listener     net.Listener
	token        string
	mu           sync.Mutex
	authorized   int
	unauthorized int
}

func startMockAPI(token string) (*mockAPI, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mock := &mockAPI{listener: listener, token: token}
	mock.server = &http.Server{Handler: http.HandlerFunc(mock.handle), ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = mock.server.Serve(listener) }()
	return mock, nil
}

func (m *mockAPI) URL() string { return "http://" + m.listener.Addr().String() }

func (m *mockAPI) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := m.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (m *mockAPI) handle(writer http.ResponseWriter, request *http.Request) {
	authorized := request.Header.Get("Authorization") == "Bearer "+m.token
	m.mu.Lock()
	if authorized {
		m.authorized++
	} else {
		m.unauthorized++
	}
	m.mu.Unlock()
	if !authorized {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet || request.URL.Path != "/api/v1/users/current" || request.Header.Get("Accept") != "application/json" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"id": 4242, "username": "native-lifecycle"})
}

func (m *mockAPI) AuthProof() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.authorized < 1 || m.unauthorized != 0 {
		return fmt.Errorf("mock API authorization counts are authorized=%d unauthorized=%d", m.authorized, m.unauthorized)
	}
	return nil
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func proveMCP(ctx context.Context, client *http.Client, endpoint, expectedVersion string) error {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"native-lifecycle","version":"1"}}}`
	response, body, err := postMCP(ctx, client, endpoint, "", "", initialize)
	if err != nil {
		return fmt.Errorf("initialize MCP: %w", err)
	}
	session := response.Header.Get("MCP-Session-Id")
	if session == "" {
		return errors.New("initialize response omitted MCP-Session-Id")
	}
	var initialized rpcEnvelope
	if err := json.Unmarshal(body, &initialized); err != nil || len(initialized.Error) != 0 {
		return errors.New("initialize returned an invalid JSON-RPC response")
	}
	var initializeResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initialized.Result, &initializeResult); err != nil || initializeResult.ProtocolVersion != "2025-06-18" || initializeResult.ServerInfo.Version != expectedVersion {
		return fmt.Errorf("initialize identity mismatch: protocol=%q version=%q", initializeResult.ProtocolVersion, initializeResult.ServerInfo.Version)
	}
	notification := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	response, _, err = postMCP(ctx, client, endpoint, session, "2025-06-18", notification)
	if err != nil || response.StatusCode != http.StatusAccepted {
		return errors.New("MCP initialized notification was not accepted")
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_current_user","arguments":{}}}`
	_, body, err = postMCP(ctx, client, endpoint, session, "2025-06-18", call)
	if err != nil {
		return fmt.Errorf("call get_current_user: %w", err)
	}
	var called rpcEnvelope
	if err := json.Unmarshal(body, &called); err != nil || len(called.Error) != 0 {
		return errors.New("get_current_user returned an invalid JSON-RPC response")
	}
	var callResult struct {
		StructuredContent struct {
			OK   bool `json:"ok"`
			Data struct {
				ID float64 `json:"id"`
			} `json:"data"`
			Meta struct {
				Source string `json:"source"`
			} `json:"meta"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(called.Result, &callResult); err != nil || !callResult.StructuredContent.OK || callResult.StructuredContent.Data.ID != 4242 || callResult.StructuredContent.Meta.Source != "kaiten" {
		return errors.New("get_current_user did not return the authenticated mock object")
	}
	return nil
}

func postMCP(ctx context.Context, client *http.Client, endpoint, session, protocol, body string) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if session != "" {
		request.Header.Set("MCP-Session-Id", session)
	}
	if protocol != "" {
		request.Header.Set("MCP-Protocol-Version", protocol)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	var decoded any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil && response.StatusCode != http.StatusAccepted {
		return response, nil, err
	}
	encoded, _ := json.Marshal(decoded)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return response, encoded, fmt.Errorf("MCP returned HTTP %d", response.StatusCode)
	}
	return response, encoded, nil
}
