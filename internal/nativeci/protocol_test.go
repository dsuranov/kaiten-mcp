package nativeci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/config"
	"github.com/dsuranov/kaiten-mcp/internal/mcp"
	"github.com/dsuranov/kaiten-mcp/internal/version"
)

func TestMCPProofExercisesReadOnlyProtocolAndAuthenticatedMockAPI(t *testing.T) {
	const token = "native-ci-protocol-test-token"
	mock, err := startMockAPI(token)
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	baseURL, err := url.Parse(mock.URL())
	if err != nil {
		t.Fatal(err)
	}
	previousVersion := version.Version
	version.Version = "native-v1"
	t.Cleanup(func() { version.Version = previousVersion })
	server := mcp.NewServer(config.Config{
		Token: token, BaseURL: baseURL, APIPrefix: "/api/v1", RateLimitRPS: 1000,
		CacheTTL: 0, MaxConcurrency: 1, Timeout: time.Second,
	})
	httpServer := httptest.NewServer(server.HTTPHandler("/mcp"))
	defer httpServer.Close()
	if err := proveMCP(context.Background(), &http.Client{Timeout: time.Second}, httpServer.URL+"/mcp", "native-v1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.AuthProof(); err != nil || mock.AuthorizedCount() != 1 {
		t.Fatalf("mock authorization proof = count %d, err %v", mock.AuthorizedCount(), err)
	}
}
