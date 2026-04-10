package cluster

import (
	"sync"
	"time"
)

// resolverCache is a thread-safe TTL cache for wallet→parent lookups.
// It holds both positive entries (parent found) and negative entries (no parent).
// Negative entries use a shorter TTL so the resolver retries sooner.
type resolverCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	posTTL  time.Duration // TTL for positive hits
	negTTL  time.Duration // TTL for negative hits (retry sooner)
	now     func() time.Time
}

type cacheEntry struct {
	parent    string // canonical parent; empty string means "no parent found"
	found     bool   // true = positive hit; false = negative hit
	expiresAt time.Time
}

func newResolverCache(posTTL, negTTL time.Duration) *resolverCache {
	return newResolverCacheWithClock(posTTL, negTTL, time.Now)
}

func newResolverCacheWithClock(posTTL, negTTL time.Duration, now func() time.Time) *resolverCache {
	return &resolverCache{
		entries: make(map[string]cacheEntry),
		posTTL:  posTTL,
		negTTL:  negTTL,
		now:     now,
	}
}

// get returns (parent, found, ok).
// ok=true means a valid (non-expired) cache entry was found.
// found=true means the entry is a positive hit; false means negative (no parent).
func (c *resolverCache) get(wallet string) (parent string, found bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, exists := c.entries[wallet]
	if !exists {
		return "", false, false
	}
	if c.now().After(e.expiresAt) {
		delete(c.entries, wallet)
		return "", false, false
	}
	return e.parent, e.found, true
}

// setPositive stores a positive parent result.
func (c *resolverCache) setPositive(wallet, parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[wallet] = cacheEntry{
		parent:    parent,
		found:     true,
		expiresAt: c.now().Add(c.posTTL),
	}
}

// setNegative stores a negative result (wallet has no known parent).
func (c *resolverCache) setNegative(wallet string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[wallet] = cacheEntry{
		parent:    "",
		found:     false,
		expiresAt: c.now().Add(c.negTTL),
	}
}

// size returns the number of entries currently in the cache (including expired).
func (c *resolverCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
