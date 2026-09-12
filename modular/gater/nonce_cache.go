package gater

import (
	"sync"
	"time"
)

// nonceCache remembers which signed requests have been seen so each one is
// honored once within its expiry. Entries live in memory, so the guarantee is
// per gateway instance; a shared store is the follow-up for multi-gateway
// deployments.
type nonceCache struct {
	mu         sync.Mutex
	entries    map[string]int64
	maxEntries int
}

func newNonceCache(maxEntries int) *nonceCache {
	return &nonceCache{
		entries:    make(map[string]int64),
		maxEntries: maxEntries,
	}
}

// checkAndStore records the key until its expiry and reports whether it was
// fresh; a false return means the same signed request was already seen.
func (c *nonceCache) checkAndStore(key string, expiryUnix int64) bool {
	now := time.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()

	if seenUntil, ok := c.entries[key]; ok && seenUntil >= now {
		return false
	}
	if len(c.entries) >= c.maxEntries {
		c.purgeLocked(now)
	}
	c.entries[key] = expiryUnix
	return true
}

// purgeLocked drops expired entries and, if the cache is still full, evicts a
// tenth of it so new requests keep working; the trade-off is documented on the
// type.
func (c *nonceCache) purgeLocked(now int64) {
	for key, expiry := range c.entries {
		if expiry < now {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.maxEntries {
		return
	}
	evict := c.maxEntries / 10
	for key := range c.entries {
		if evict == 0 {
			break
		}
		delete(c.entries, key)
		evict--
	}
}
