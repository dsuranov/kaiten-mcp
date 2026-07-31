package api

import (
	"context"
	"sync"
	"time"
)

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

type cacheCall struct {
	done  chan struct{}
	value any
	err   error
}

// Cache is a small in-memory TTL cache with single-flight miss coalescing.
type Cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	entries  map[string]cacheEntry
	inflight map[string]*cacheCall
	now      func() time.Time
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, entries: make(map[string]cacheEntry), inflight: make(map[string]*cacheCall), now: time.Now}
}

func (c *Cache) Get(ctx context.Context, key string, loader func(context.Context) (any, error)) (any, bool, error) {
	if c.ttl == 0 {
		value, err := loader(ctx)
		return value, false, err
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && c.now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.value, true, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.value, false, call.err
		case <-ctx.Done():
			return nil, false, sanitizedContextError(ctx.Err())
		}
	}
	call := &cacheCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	call.value, call.err = loader(ctx)
	c.mu.Lock()
	delete(c.inflight, key)
	if call.err == nil {
		c.entries[key] = cacheEntry{value: call.value, expiresAt: c.now().Add(c.ttl)}
	}
	close(call.done)
	c.mu.Unlock()
	return call.value, false, call.err
}
