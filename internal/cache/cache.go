package cache

import (
	"context"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// Cache is the two-layer cache: L1 local in-memory (hot, LRU) + L2 Redis
// (shared, TTL). Get checks L1 first, then L2 (backfilling L1 on a hit).
type Cache struct {
	l1  *lru.Cache[string, *CachedResponse]
	l2  *RedisCache
	ttl time.Duration
}

// NewCache builds a cache. l2 may be nil to disable the shared layer (L1 only).
func NewCache(l1Size int, l2 *RedisCache, ttl time.Duration) (*Cache, error) {
	if l1Size <= 0 {
		l1Size = 10000
	}
	l1, err := lru.New[string, *CachedResponse](l1Size)
	if err != nil {
		return nil, err
	}
	return &Cache{l1: l1, l2: l2, ttl: ttl}, nil
}

// Get returns a cached response, ok=false on a miss.
func (c *Cache) Get(ctx context.Context, key string) (*CachedResponse, bool, error) {
	if resp, ok := c.l1.Get(key); ok {
		return resp, true, nil
	}
	if c.l2 != nil {
		resp, ok, err := c.l2.Get(ctx, key)
		if err != nil {
			return nil, false, err
		}
		if ok {
			c.l1.Add(key, resp)
			return resp, true, nil
		}
	}
	return nil, false, nil
}

// Set stores a response in both layers.
func (c *Cache) Set(ctx context.Context, key string, resp *CachedResponse) error {
	c.l1.Add(key, resp)
	if c.l2 != nil {
		return c.l2.Set(ctx, key, resp)
	}
	return nil
}

// Clear empties both layers.
func (c *Cache) Clear(ctx context.Context) error {
	c.l1.Purge()
	if c.l2 != nil {
		return c.l2.Clear(ctx)
	}
	return nil
}
