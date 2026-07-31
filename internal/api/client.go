// Package api implements the authenticated Kaiten REST boundary.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsuranov/kaiten-mcp/internal/config"
)

const (
	maxReadAttempts      = 3
	maxResponseBodyBytes = 16 * 1024 * 1024
)

// Error is a sanitized domain-level API failure.
type Error struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	StatusCode *int   `json:"status_code,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Client is safe for concurrent use.
type Client struct {
	baseURL    *url.URL
	prefix     string
	token      string
	httpClient *http.Client
	semaphore  chan struct{}
	rate       *rateGate
	cache      *Cache
	timeout    time.Duration
}

// New returns a client configured with bounded concurrency, rate limiting,
// retries for reads, and a discovery cache.
func New(cfg config.Config) *Client {
	return NewWithHTTPClient(cfg, &http.Client{})
}

// NewWithHTTPClient is intended for custom transports and deterministic tests.
func NewWithHTTPClient(cfg config.Config, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{
		baseURL: cfg.BaseURL, prefix: cfg.APIPrefix, token: cfg.Token,
		httpClient: client, semaphore: make(chan struct{}, cfg.MaxConcurrency),
		rate: newRateGate(cfg.RateLimitRPS), cache: NewCache(cfg.CacheTTL),
		timeout: cfg.Timeout,
	}
}

// JSON sends one JSON request. Mutating methods are never retried.
func (c *Client) JSON(ctx context.Context, method, path string, query url.Values, body any) (any, error) {
	if c.baseURL == nil {
		return nil, &Error{Type: "validation", Message: "Kaiten tenant URL is not configured"}
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, &Error{Type: "validation", Message: "request body is not valid JSON"}
		}
	}
	attempts := 1
	if method == http.MethodGet || method == http.MethodHead {
		attempts = maxReadAttempts
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := waitContext(ctx, retryBackoff(attempt)); err != nil {
				return nil, sanitizedContextError(err)
			}
		}
		value, retryAfter, retryable, err := c.once(ctx, method, path, query, encoded, body != nil)
		if err == nil {
			return value, nil
		}
		last = err
		if !retryable || attempt == attempts-1 {
			break
		}
		if retryAfter > 0 {
			if err := waitContext(ctx, retryAfter); err != nil {
				return nil, sanitizedContextError(err)
			}
		}
	}
	return nil, last
}

func (c *Client) once(ctx context.Context, method, path string, query url.Values, encoded []byte, hasBody bool) (any, time.Duration, bool, error) {
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return nil, 0, false, sanitizedContextError(ctx.Err())
	}
	if err := c.rate.Wait(ctx); err != nil {
		return nil, 0, false, sanitizedContextError(err)
	}
	requestContext := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		requestContext, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	u := *c.baseURL
	u.Path = strings.TrimRight(c.baseURL.Path, "/") + c.prefix + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = query.Encode()
	var reader io.Reader
	if hasBody {
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(requestContext, method, u.String(), reader)
	if err != nil {
		return nil, 0, false, &Error{Type: "validation", Message: "could not construct Kaiten request"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if requestContext.Err() != nil {
			return nil, 0, false, sanitizedContextError(requestContext.Err())
		}
		return nil, 0, true, &Error{Type: "upstream", Message: "Kaiten request failed before a response was received"}
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if readErr != nil {
		return nil, 0, true, &Error{Type: "upstream", Message: "could not read the Kaiten response"}
	}
	if len(payload) > maxResponseBodyBytes {
		return nil, 0, false, &Error{Type: "upstream", Message: "Kaiten response exceeded the maximum allowed size"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := classifyHTTP(resp.StatusCode)
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retryDelay(resp.Header, time.Now()), retryable, apiErr
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return map[string]any{}, 0, false, nil
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, 0, false, &Error{Type: "upstream", Message: "Kaiten returned invalid JSON", StatusCode: intPointer(resp.StatusCode)}
	}
	return value, 0, false, nil
}

// CachedJSON coalesces concurrent cache misses and reuses successful discovery
// responses until the configured TTL expires.
func (c *Client) CachedJSON(ctx context.Context, key, path string, query url.Values) (any, bool, error) {
	return c.cache.Get(ctx, key, func(loadCtx context.Context) (any, error) {
		return c.JSON(loadCtx, http.MethodGet, path, query, nil)
	})
}

func classifyHTTP(status int) *Error {
	kind, message := "upstream", fmt.Sprintf("Kaiten returned HTTP %d", status)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind, message = "auth", "Kaiten rejected the credentials or permissions"
	case http.StatusNotFound:
		kind, message = "not_found", "the requested Kaiten resource was not found"
	case http.StatusTooManyRequests:
		kind, message = "rate_limit", "Kaiten rate limit was reached"
	default:
		if status >= 400 && status < 500 {
			kind, message = "validation", fmt.Sprintf("Kaiten rejected the request with HTTP %d", status)
		}
	}
	return &Error{Type: kind, Message: message, StatusCode: intPointer(status)}
}

func intPointer(value int) *int { return &value }

func sanitizedContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &Error{Type: "upstream", Message: "operation canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Type: "upstream", Message: "Kaiten request timed out; a write outcome may be uncertain"}
	}
	return &Error{Type: "upstream", Message: "operation stopped"}
}

func retryBackoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * 100 * time.Millisecond
}

func retryDelay(header http.Header, now time.Time) time.Duration {
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		if when, err := http.ParseTime(raw); err == nil && when.After(now) {
			return when.Sub(now)
		}
	}
	for _, name := range []string{"X-RateLimit-Reset", "X-Kaiten-RateLimit-Reset"} {
		if raw := strings.TrimSpace(header.Get(name)); raw != "" {
			if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
				when := time.Unix(unix, 0)
				if when.After(now) {
					return when.Sub(now)
				}
			}
		}
	}
	return 0
}

func waitContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type rateGate struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newRateGate(requestsPerSecond float64) *rateGate {
	return &rateGate{interval: time.Duration(float64(time.Second) / requestsPerSecond)}
}

func (g *rateGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	when := now
	if g.next.After(now) {
		when = g.next
	}
	g.next = when.Add(g.interval)
	g.mu.Unlock()
	return waitContext(ctx, time.Until(when))
}
