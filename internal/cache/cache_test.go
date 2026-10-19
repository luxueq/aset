package cache

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisCache(t *testing.T) (*RedisCache, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewRedisCache(client, time.Minute), mr, client
}

func TestCache_SetGet(t *testing.T) {
	cache, err := NewCache(10, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp := &CachedResponse{StatusCode: 200, Body: []byte(`{"ok":true}`)}
	if err := cache.Set(context.Background(), "k", resp); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.Get(context.Background(), "k")
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if got.StatusCode != 200 || !bytes.Equal(got.Body, resp.Body) {
		t.Fatalf("body mismatch: %+v", got)
	}
}

func TestCache_Miss(t *testing.T) {
	cache, _ := NewCache(10, nil, time.Minute)
	_, ok, err := cache.Get(context.Background(), "missing")
	if err != nil || ok {
		t.Fatalf("expected miss, got ok=%v err=%v", ok, err)
	}
}

func TestCache_L2Hit_BackfillL1(t *testing.T) {
	rc, mr, _ := newTestRedisCache(t)
	ctx := context.Background()

	// Instance A writes both layers.
	cacheA, _ := NewCache(10, rc, time.Minute)
	cacheA.Set(ctx, "k", &CachedResponse{StatusCode: 200, Body: []byte("payload")})

	// Instance B (empty L1) reads → L2 hit, backfills L1.
	cacheB, _ := NewCache(10, rc, time.Minute)
	if _, ok, _ := cacheB.Get(ctx, "k"); !ok {
		t.Fatal("expected L2 hit")
	}

	// Flush Redis; B's L1 backfill must now serve the request.
	mr.FlushAll()
	if _, ok, _ := cacheB.Get(ctx, "k"); !ok {
		t.Fatal("expected L1 backfill to serve after Redis flush")
	}
}

func TestCache_LRU_EvictsOldest(t *testing.T) {
	cache, _ := NewCache(2, nil, time.Minute)
	ctx := context.Background()
	cache.Set(ctx, "a", &CachedResponse{Body: []byte("a")})
	cache.Set(ctx, "b", &CachedResponse{Body: []byte("b")})
	cache.Set(ctx, "c", &CachedResponse{Body: []byte("c")}) // evicts a

	if _, ok, _ := cache.Get(ctx, "a"); ok {
		t.Fatal("a should have been evicted (capacity 2)")
	}
	if _, ok, _ := cache.Get(ctx, "c"); !ok {
		t.Fatal("c should be present")
	}
}

func TestCache_Clear(t *testing.T) {
	rc, _, _ := newTestRedisCache(t)
	cache, _ := NewCache(10, rc, time.Minute)
	ctx := context.Background()

	cache.Set(ctx, "k", &CachedResponse{Body: []byte("x")})
	if err := cache.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cache.Get(ctx, "k"); ok {
		t.Fatal("L1 should be cleared")
	}
	if _, ok, _ := rc.Get(ctx, "k"); ok {
		t.Fatal("L2 should be cleared")
	}
}

func TestRedisCache_CompressRoundTrip(t *testing.T) {
	rc, _, client := newTestRedisCache(t)
	ctx := context.Background()

	bigBody := []byte(strings.Repeat("hello world ", 200)) // ~2.4KB, triggers gzip
	if err := rc.Set(ctx, "big", &CachedResponse{StatusCode: 200, Body: bigBody}); err != nil {
		t.Fatal(err)
	}

	raw, err := client.Get(ctx, cacheKeyPrefix+"big").Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !isGzip(raw) {
		t.Fatalf("expected gzip-compressed value, magic = %x", raw[:2])
	}
	if len(raw) >= len(bigBody) {
		t.Errorf("compression did not shrink: %d >= %d", len(raw), len(bigBody))
	}

	got, ok, err := rc.Get(ctx, "big")
	if err != nil || !ok {
		t.Fatalf("expected hit, ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.Body, bigBody) {
		t.Fatal("body did not survive the compress round-trip")
	}
}

func TestRedisCache_SmallValue_NotCompressed(t *testing.T) {
	rc, _, client := newTestRedisCache(t)
	ctx := context.Background()

	small := []byte("tiny") // below minCompressSize
	rc.Set(ctx, "small", &CachedResponse{StatusCode: 200, Body: small})

	raw, _ := client.Get(ctx, cacheKeyPrefix+"small").Bytes()
	if isGzip(raw) {
		t.Fatal("small value should not be gzip-compressed")
	}
}

func TestRedisCache_SkipLargeValue(t *testing.T) {
	rc, _, _ := newTestRedisCache(t)
	rc.SetMaxValueSize(100)
	ctx := context.Background()

	rc.Set(ctx, "big", &CachedResponse{StatusCode: 200, Body: []byte(strings.Repeat("x", 200))})
	if _, ok, _ := rc.Get(ctx, "big"); ok {
		t.Fatal("oversized response should not be cached")
	}
}

func TestRedisCache_TTL(t *testing.T) {
	rc, mr, _ := newTestRedisCache(t)
	ctx := context.Background()

	rc.Set(ctx, "k", &CachedResponse{StatusCode: 200, Body: []byte("x")})
	mr.FastForward(2 * time.Minute)

	if _, ok, _ := rc.Get(ctx, "k"); ok {
		t.Fatal("entry should expire after TTL")
	}
}
