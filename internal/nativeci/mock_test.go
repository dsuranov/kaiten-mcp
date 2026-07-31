package nativeci

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMockAPIRequiresSyntheticBearerAndExactPublicPath(t *testing.T) {
	mock, err := startMockAPI("newly-authored-test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	request, _ := http.NewRequest(http.MethodGet, mock.URL()+"/api/v1/users/current", nil)
	request.Header.Set("Authorization", "Bearer newly-authored-test-token")
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if err := mock.AuthProof(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForHealthRequiresExactVersion(t *testing.T) {
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"v2","runtime":"test"}`))
	})}
	listener, err := netListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	if err := waitForHealth(context.Background(), &http.Client{Timeout: time.Second}, "http://"+listener.Addr().String()+"/health", "v2", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitForHealth(context.Background(), &http.Client{Timeout: 50 * time.Millisecond}, "http://"+listener.Addr().String()+"/health", "v1", 100*time.Millisecond); err == nil {
		t.Fatal("wrong version was accepted")
	}
}
