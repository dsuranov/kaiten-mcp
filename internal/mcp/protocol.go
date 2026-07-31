package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/api"
	"github.com/dsuranov/kaiten-mcp/internal/config"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

const (
	fallbackProtocolVersion = "2025-06-18"
	defaultHTTPSessionTTL   = 30 * time.Minute
)

var supportedProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Server contains one MCP protocol surface and its Kaiten API service.
type Server struct {
	service      *api.Service
	writeEnabled bool
	sessions     *httpSessionStore
}

func NewServer(cfg config.Config) *Server {
	return &Server{
		service: api.NewService(api.New(cfg)), writeEnabled: cfg.EnableWriteTools,
		sessions: newHTTPSessionStore(defaultHTTPSessionTTL),
	}
}

func (s *Server) handle(ctx context.Context, request rpcRequest) *rpcResponse {
	if len(request.ID) == 0 && request.Method != "" {
		return nil
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		return errorResponse(request.ID, -32600, "invalid JSON-RPC request", nil)
	}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return errorResponse(request.ID, -32602, "invalid initialize parameters", nil)
			}
		}
		protocol := negotiateProtocol(strings.TrimSpace(params.ProtocolVersion))
		return resultResponse(request.ID, map[string]any{
			"protocolVersion": protocol,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "Kaiten", "version": version.String()},
			"instructions":    "Use the registered tools to access the configured Kaiten workspace. Write tools are absent unless explicitly enabled.",
		})
	case "ping":
		return resultResponse(request.ID, map[string]any{})
	case "tools/list":
		return resultResponse(request.ID, map[string]any{"tools": tools(s.writeEnabled)})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			return errorResponse(request.ID, -32602, "invalid tool call parameters", nil)
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		spec, ok := findToolSpec(params.Name, s.writeEnabled)
		if !ok {
			return errorResponse(request.ID, -32602, "unknown or unavailable tool", map[string]any{"name": params.Name})
		}
		if err := validateArguments(spec, params.Arguments); err != nil {
			return errorResponse(request.ID, -32602, "tool input does not match its schema", map[string]any{"detail": err.Error()})
		}
		result := s.callTool(ctx, spec, params.Arguments)
		return resultResponse(request.ID, result)
	default:
		return errorResponse(request.ID, -32601, "method not found", nil)
	}
}

func resultResponse(id json.RawMessage, result any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string, data any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
}

func validateArguments(spec toolSpec, arguments map[string]any) error {
	for _, name := range spec.required {
		value, present := arguments[name]
		if !present || value == nil {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range arguments {
		field, known := spec.fields[name]
		if !known {
			return fmt.Errorf("unknown field %s", name)
		}
		if value == nil {
			if !field.nullable {
				return fmt.Errorf("%s cannot be null", name)
			}
			continue
		}
		switch field.kind {
		case "integer":
			number, ok := value.(float64)
			if !ok || number != float64(int64(number)) {
				return fmt.Errorf("%s must be an integer", name)
			}
			if field.minimum != nil && number < *field.minimum {
				return fmt.Errorf("%s must be at least %v", name, *field.minimum)
			}
		case "string":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", name)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", name)
			}
		case "array":
			values, ok := value.([]any)
			if !ok {
				return fmt.Errorf("%s must be an array", name)
			}
			if field.itemKind == "integer" {
				for _, item := range values {
					number, ok := item.(float64)
					if !ok || number != float64(int64(number)) {
						return fmt.Errorf("%s must contain only integers", name)
					}
				}
			}
		case "object":
			object, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object", name)
			}
			if field.additionalValueKind == "string" {
				for _, entry := range object {
					if _, ok := entry.(string); !ok {
						return fmt.Errorf("%s values must be strings", name)
					}
				}
			}
		}
	}
	return nil
}

// ServeStdio runs JSON-RPC messages over newline-delimited standard I/O.
func (s *Server) ServeStdio(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal(line, &request); err != nil {
			response := errorResponse(nil, -32700, "parse error", nil)
			if err := encoder.Encode(response); err != nil {
				return err
			}
			continue
		}
		if response := s.handle(ctx, request); response != nil {
			if err := encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP stdio: %w", err)
	}
	return nil
}

// HTTPHandler returns a Streamable HTTP endpoint. It supports JSON responses
// for client-to-server messages and session cleanup; server-initiated streams
// are not needed because this server emits no unsolicited notifications.
func (s *Server) HTTPHandler(path string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.String(), "runtime": runtime.Version()})
	})
	mux.HandleFunc(path, s.handleHTTPEndpoint)
	return mux
}

func (s *Server) handleHTTPEndpoint(writer http.ResponseWriter, request *http.Request) {
	if !validOrigin(request) {
		writeHTTPError(writer, http.StatusForbidden, "origin is not allowed")
		return
	}
	protocolHeader := strings.TrimSpace(request.Header.Get("MCP-Protocol-Version"))
	if protocolHeader != "" && !isSupportedProtocol(protocolHeader) {
		writeHTTPError(writer, http.StatusBadRequest, "MCP-Protocol-Version is unsupported")
		return
	}
	switch request.Method {
	case http.MethodPost:
		s.handleHTTPPost(writer, request, protocolHeader)
	case http.MethodDelete:
		s.handleHTTPDelete(writer, request, protocolHeader)
	case http.MethodGet:
		if _, ok := s.requireHTTPSession(writer, request, protocolHeader); !ok {
			return
		}
		writer.Header().Set("Allow", "POST, DELETE")
		writeHTTPError(writer, http.StatusMethodNotAllowed, "server notifications are not enabled")
	default:
		writer.Header().Set("Allow", "POST, GET, DELETE")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleHTTPPost(writer http.ResponseWriter, request *http.Request, protocolHeader string) {
	accept := request.Header.Get("Accept")
	if accept != "" && !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*") {
		writeHTTPError(writer, http.StatusNotAcceptable, "Accept must allow application/json")
		return
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024*1024))
	var message rpcRequest
	if err := decoder.Decode(&message); err != nil {
		writeJSON(writer, http.StatusBadRequest, errorResponse(nil, -32700, "parse error", nil))
		return
	}
	if message.Method == "initialize" {
		if request.Header.Get("MCP-Session-Id") != "" {
			writeHTTPError(writer, http.StatusBadRequest, "initialize must not include MCP-Session-Id")
			return
		}
		if len(message.ID) == 0 {
			writeHTTPError(writer, http.StatusBadRequest, "initialize must be a JSON-RPC request")
			return
		}
		response := s.handle(request.Context(), message)
		if response == nil {
			writeHTTPError(writer, http.StatusBadRequest, "initialize was not accepted")
			return
		}
		if response.Error == nil {
			protocol := protocolFromInitialize(message.Params)
			sessionID, err := s.sessions.create(negotiateProtocol(protocol))
			if err != nil {
				writeHTTPError(writer, http.StatusInternalServerError, "could not create MCP session")
				return
			}
			writer.Header().Set("MCP-Session-Id", sessionID)
		}
		writeJSON(writer, http.StatusOK, response)
		return
	}

	session, ok := s.requireHTTPSession(writer, request, protocolHeader)
	if !ok {
		return
	}
	notification := len(message.ID) == 0
	if message.Method == "notifications/initialized" {
		if !notification || session.ready || !s.sessions.markReady(session.id) {
			writeHTTPError(writer, http.StatusBadRequest, "MCP session initialization state is invalid")
			return
		}
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	if !session.ready && message.Method != "ping" {
		if notification {
			writeHTTPError(writer, http.StatusBadRequest, "MCP session is not initialized")
			return
		}
		writeJSON(writer, http.StatusOK, errorResponse(message.ID, -32002, "MCP session is not initialized", nil))
		return
	}
	response := s.handle(request.Context(), message)
	if notification {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) handleHTTPDelete(writer http.ResponseWriter, request *http.Request, protocolHeader string) {
	session, ok := s.requireHTTPSession(writer, request, protocolHeader)
	if !ok {
		return
	}
	if !s.sessions.delete(session.id) {
		writeHTTPError(writer, http.StatusNotFound, "MCP session was not found")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

type httpSessionView struct {
	id       string
	protocol string
	ready    bool
}

func (s *Server) requireHTTPSession(writer http.ResponseWriter, request *http.Request, protocolHeader string) (httpSessionView, bool) {
	sessionID := strings.TrimSpace(request.Header.Get("MCP-Session-Id"))
	if sessionID == "" {
		writeHTTPError(writer, http.StatusBadRequest, "MCP-Session-Id is required")
		return httpSessionView{}, false
	}
	session, ok := s.sessions.get(sessionID)
	if !ok {
		writeHTTPError(writer, http.StatusNotFound, "MCP session was not found")
		return httpSessionView{}, false
	}
	if protocolHeader != "" && protocolHeader != session.protocol {
		writeHTTPError(writer, http.StatusBadRequest, "MCP-Protocol-Version does not match the session")
		return httpSessionView{}, false
	}
	return session, true
}

func validOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeHTTPError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"error": message})
}

func newSessionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func negotiateProtocol(requested string) string {
	for _, version := range supportedProtocolVersions {
		if requested == version {
			return version
		}
	}
	return fallbackProtocolVersion
}

func isSupportedProtocol(protocol string) bool {
	for _, supported := range supportedProtocolVersions {
		if protocol == supported {
			return true
		}
	}
	return false
}

func protocolFromInitialize(params json.RawMessage) string {
	var input struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &input)
	return strings.TrimSpace(input.ProtocolVersion)
}

type httpSession struct {
	protocol  string
	ready     bool
	expiresAt time.Time
}

type httpSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*httpSession
	ttl      time.Duration
	now      func() time.Time
}

func newHTTPSessionStore(ttl time.Duration) *httpSessionStore {
	return &httpSessionStore{sessions: make(map[string]*httpSession), ttl: ttl, now: time.Now}
}

func (s *httpSessionStore) create(protocol string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	for attempts := 0; attempts < 4; attempts++ {
		id, err := newSessionID()
		if err != nil {
			return "", err
		}
		if _, exists := s.sessions[id]; exists {
			continue
		}
		s.sessions[id] = &httpSession{protocol: protocol, expiresAt: s.now().Add(s.ttl)}
		return id, nil
	}
	return "", errors.New("could not allocate unique MCP session ID")
}

func (s *httpSessionStore) get(id string) (httpSessionView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	session, ok := s.sessions[id]
	if !ok {
		return httpSessionView{}, false
	}
	session.expiresAt = s.now().Add(s.ttl)
	return httpSessionView{id: id, protocol: session.protocol, ready: session.ready}, true
}

func (s *httpSessionStore) markReady(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	session, ok := s.sessions[id]
	if !ok || session.ready {
		return false
	}
	session.ready = true
	session.expiresAt = s.now().Add(s.ttl)
	return true
}

func (s *httpSessionStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	if _, ok := s.sessions[id]; !ok {
		return false
	}
	delete(s.sessions, id)
	return true
}

func (s *httpSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	return len(s.sessions)
}

func (s *httpSessionStore) cleanupLocked() {
	now := s.now()
	for id, session := range s.sessions {
		if !session.expiresAt.After(now) {
			delete(s.sessions, id)
		}
	}
}

// ServeHTTP binds, serves, and shuts down gracefully when the context ends.
func (s *Server) ServeHTTP(ctx context.Context, host string, port int, path string) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.HTTPHandler(path), ReadHeaderTimeout: 10 * time.Second}
	errChannel := make(chan error, 1)
	go func() { errChannel <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		err := <-errChannel
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errChannel:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
