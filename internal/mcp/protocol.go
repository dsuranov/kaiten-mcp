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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/api"
	"github.com/dsuranov/kaiten-mcp/internal/config"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

const fallbackProtocolVersion = "2025-06-18"

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
}

func NewServer(cfg config.Config) *Server {
	return &Server{service: api.NewService(api.New(cfg)), writeEnabled: cfg.EnableWriteTools}
}

func (s *Server) handle(ctx context.Context, request rpcRequest) *rpcResponse {
	if request.JSONRPC != "2.0" || request.Method == "" {
		return errorResponse(request.ID, -32600, "invalid JSON-RPC request", nil)
	}
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
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
		protocol := strings.TrimSpace(params.ProtocolVersion)
		if protocol == "" {
			protocol = fallbackProtocolVersion
		}
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
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version.String(), "runtime": runtime.Version()})
	})
	mux.HandleFunc(path, func(w http.ResponseWriter, request *http.Request) {
		if !validOrigin(request) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "origin is not allowed"})
			return
		}
		switch request.Method {
		case http.MethodPost:
			defer request.Body.Close()
			decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024*1024))
			var message rpcRequest
			if err := decoder.Decode(&message); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse(nil, -32700, "parse error", nil))
				return
			}
			response := s.handle(request.Context(), message)
			if message.Method == "initialize" {
				w.Header().Set("Mcp-Session-Id", newSessionID())
			}
			if response == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeJSON(w, http.StatusOK, response)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Allow", "POST, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "server notifications are not enabled"})
		default:
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func validOrigin(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	return strings.HasPrefix(origin, "http://127.0.0.1") || strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://[::1]") || strings.HasPrefix(origin, "https://127.0.0.1") || strings.HasPrefix(origin, "https://localhost")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func newSessionID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
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

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}
