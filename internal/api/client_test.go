package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/config"
)

func testClient(t *testing.T, handler http.Handler, rps float64, concurrency int) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	cfg := config.Config{Token: "test-only-token", BaseURL: base, APIPrefix: "/api/v1", RateLimitRPS: rps, CacheTTL: time.Minute, MaxConcurrency: concurrency}
	return NewWithHTTPClient(cfg, server.Client()), server
}

func TestHeadersPathAndQuery(t *testing.T) {
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cards/9" || r.URL.Query().Get("include") != "members & comments" {
			t.Errorf("unexpected target: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-only-token" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing required headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
	}), 10000, 2)
	value, err := client.JSON(context.Background(), http.MethodGet, "/cards/9", url.Values{"include": {"members & comments"}}, nil)
	if err != nil || value.(map[string]any)["id"] != float64(9) {
		t.Fatalf("unexpected result: %v %v", value, err)
	}
}

func TestReadsRetryButWritesDoNot(t *testing.T) {
	var reads, writes atomic.Int32
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if reads.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		writes.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}), 10000, 2)
	if _, err := client.JSON(context.Background(), http.MethodGet, "/cards", nil, nil); err != nil || reads.Load() != 3 {
		t.Fatalf("read retry mismatch: calls=%d err=%v", reads.Load(), err)
	}
	if _, err := client.JSON(context.Background(), http.MethodPost, "/cards", nil, map[string]any{"title": "new"}); err == nil || writes.Load() != 1 {
		t.Fatalf("write was unexpectedly retried: calls=%d err=%v", writes.Load(), err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	var active, peak atomic.Int32
	client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		_, _ = w.Write([]byte(`{}`))
	}), 10000, 2)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = client.JSON(context.Background(), http.MethodGet, "/version", nil, nil)
		}()
	}
	group.Wait()
	if peak.Load() > 2 {
		t.Fatalf("concurrency exceeded: %d", peak.Load())
	}
}

func TestCacheCoalescesAndExpires(t *testing.T) {
	cache := NewCache(30 * time.Millisecond)
	var calls atomic.Int32
	loader := func(context.Context) (any, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return "value", nil
	}
	var group sync.WaitGroup
	for i := 0; i < 5; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, _, _ = cache.Get(context.Background(), "key", loader) }()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cache misses were not coalesced: %d", calls.Load())
	}
	time.Sleep(35 * time.Millisecond)
	_, _, _ = cache.Get(context.Background(), "key", loader)
	if calls.Load() != 2 {
		t.Fatalf("cache did not expire: %d", calls.Load())
	}
}

func TestFetchPagesStopsOnRepeatedPage(t *testing.T) {
	calls := 0
	repeated := make([]any, 100)
	for i := range repeated {
		repeated[i] = map[string]any{"id": i + 1}
	}
	result, err := FetchPages(context.Background(), 0, nil, true, func(context.Context, int, int) ([]any, error) {
		calls++
		return repeated, nil
	})
	if err != nil || len(result) != 100 || calls != 2 {
		t.Fatalf("unexpected pagination: result=%v calls=%d err=%v", result, calls, err)
	}
	zero := 0
	calls = 0
	result, err = FetchPages(context.Background(), 0, &zero, true, func(context.Context, int, int) ([]any, error) { calls++; return nil, nil })
	if err != nil || len(result) != 0 || calls != 0 {
		t.Fatalf("limit zero should not fetch: result=%v calls=%d err=%v", result, calls, err)
	}
}

func TestSinglePageIsCappedWhenUpstreamIgnoresLimit(t *testing.T) {
	limit := 2
	result, err := FetchPages(context.Background(), 0, &limit, false, func(context.Context, int, int) ([]any, error) {
		return []any{1, 2, 3, 4}, nil
	})
	if err != nil || !reflect.DeepEqual(result, []any{1, 2}) {
		t.Fatalf("single-page limit not enforced: %#v err=%v", result, err)
	}
}

func TestResolve(t *testing.T) {
	resources := []Resource{{ID: 1, Name: "North Board"}, {ID: 2, Name: "North Star"}, {ID: 3, Name: "South"}}
	resource, err := Resolve("south", resources, false)
	if err != nil || resource.ID != 3 {
		t.Fatalf("exact resolution failed: %+v %v", resource, err)
	}
	if _, err := Resolve("north", resources, false); err == nil {
		t.Fatal("expected ambiguous resolution")
	}
	if _, err := Resolve("missing", resources, false); err == nil {
		t.Fatal("expected not-found resolution")
	}
}

func TestHTTPFailureClassificationAndInvalidJSON(t *testing.T) {
	tests := []struct {
		status int
		body   string
		kind   string
	}{
		{http.StatusUnauthorized, `{}`, "auth"},
		{http.StatusForbidden, `{}`, "auth"},
		{http.StatusNotFound, `{}`, "not_found"},
		{http.StatusTooManyRequests, `{}`, "rate_limit"},
		{http.StatusUnprocessableEntity, `{}`, "validation"},
		{http.StatusBadGateway, `{}`, "upstream"},
		{http.StatusOK, `{not-json`, "upstream"},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.status)+test.kind, func(t *testing.T) {
			client, _ := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}), 10000, 1)
			_, err := client.JSON(context.Background(), http.MethodPost, "/cards", nil, map[string]any{"title": "safe fixture"})
			var apiError *Error
			if !errors.As(err, &apiError) || apiError.Type != test.kind {
				t.Fatalf("status %d classified as %#v", test.status, err)
			}
		})
	}
}

func TestRetryDelayAndCancellation(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	if delay := retryDelay(http.Header{"Retry-After": {"2"}}, now); delay != 2*time.Second {
		t.Fatalf("numeric Retry-After: %v", delay)
	}
	reset := strconv.FormatInt(now.Add(3*time.Second).Unix(), 10)
	header := http.Header{}
	header.Set("X-Kaiten-RateLimit-Reset", reset)
	if delay := retryDelay(header, now); delay != 3*time.Second {
		t.Fatalf("reset header: %v", delay)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait did not honor cancellation: %v", err)
	}
}

func TestZeroTTLDisablesReuse(t *testing.T) {
	cache := NewCache(0)
	var calls atomic.Int32
	loader := func(context.Context) (any, error) { calls.Add(1); return "fresh", nil }
	for i := 0; i < 2; i++ {
		value, cached, err := cache.Get(context.Background(), "key", loader)
		if err != nil || cached || value != "fresh" {
			t.Fatalf("unexpected disabled-cache result: %v %t %v", value, cached, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("zero TTL reused a value: %d", calls.Load())
	}
}
