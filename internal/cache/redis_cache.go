package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/luxueq/aigateway/internal/common/config"
	"github.com/redis/go-redis/v9"
)

// cacheKeyPrefix namespaces all cache keys in Redis so Clear can scan them.
const cacheKeyPrefix = "aigateway:cache:"

// Value-size policy (three-part, addresses Redis big-key risk):
//   - minCompressSize: below this, gzip has negative payoff (its ~20-30 byte
//     header makes small values larger), so they are stored raw.
//   - defaultMaxValueSize: above this a response is NOT cached at all (avoids
//     creating a Redis big key from a huge LLM response).
const (
	minCompressSize     = 1024    // 1KB
	defaultMaxValueSize = 1 << 20 // 1MB
	gzipMagicHi         = 0x1f    // gzip magic bytes 0x1f 0x8b
	gzipMagicLo         = 0x8b
)

// CachedResponse is a stored upstream response: the provider's raw JSON body
// plus the status and headers needed to replay it verbatim.
type CachedResponse struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       []byte            `json:"body"`
}

// RedisCache is the L2 shared cache backed by Redis.
type RedisCache struct {
	client       redis.UniversalClient
	ttl          time.Duration
	maxValueSize int64
}

// NewRedisCache wraps an existing Redis client as the L2 cache.
func NewRedisCache(client redis.UniversalClient, ttl time.Duration) *RedisCache {
	return &RedisCache{client: client, ttl: ttl, maxValueSize: defaultMaxValueSize}
}

// NewRedisCacheFromConfig builds an L2 cache from the shared Redis config.
func NewRedisCacheFromConfig(cfg *config.RedisConfig, ttl time.Duration) (*RedisCache, error) {
	if cfg == nil || cfg.URL == "" {
		return nil, fmt.Errorf("redis url is required")
	}
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	if cfg.Password != "" {
		opts.Password = cfg.Password
	}
	if cfg.DB != 0 {
		opts.DB = cfg.DB
	}
	return NewRedisCache(redis.NewClient(opts), ttl), nil
}

// SetMaxValueSize overrides the response-body cap above which nothing is cached.
func (r *RedisCache) SetMaxValueSize(max int64) {
	if max <= 0 {
		max = defaultMaxValueSize
	}
	r.maxValueSize = max
}

// Get fetches a cached response, returning ok=false on a clean miss.
func (r *RedisCache) Get(ctx context.Context, key string) (*CachedResponse, bool, error) {
	data, err := r.client.Get(ctx, cacheKeyPrefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if isGzip(data) {
		if data, err = gunzip(data); err != nil {
			return nil, false, fmt.Errorf("gunzip cached response: %w", err)
		}
	}
	var resp CachedResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("unmarshal cached response: %w", err)
	}
	return &resp, true, nil
}

// Set stores a cached response. Responses larger than maxValueSize are skipped
// (avoiding big keys); responses at/above minCompressSize are gzip'd only when
// it actually shrinks them.
func (r *RedisCache) Set(ctx context.Context, key string, resp *CachedResponse) error {
	if int64(len(resp.Body)) > r.maxValueSize {
		return nil // too large to cache; skip silently
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal cached response: %w", err)
	}
	if len(data) >= minCompressSize {
		if gz, ok := gzipBytes(data); ok {
			data = gz
		}
	}
	return r.client.Set(ctx, cacheKeyPrefix+key, data, r.ttl).Err()
}

// Clear removes every cache entry using UNLINK (async) so deleting many/large
// values never blocks the Redis main thread the way DEL would.
func (r *RedisCache) Clear(ctx context.Context) error {
	iter := r.client.Scan(ctx, 0, cacheKeyPrefix+"*", 100).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan cache keys: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	return r.client.Unlink(ctx, keys...).Err()
}

// Close closes the underlying Redis client.
func (r *RedisCache) Close() error {
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

// isGzip reports whether data starts with the gzip magic bytes.
func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == gzipMagicHi && data[1] == gzipMagicLo
}

// gzipBytes compresses data, returning ok=false when it does not shrink
// (already-compressed or too-small input).
func gzipBytes(data []byte) ([]byte, bool) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, false
	}
	if err := gz.Close(); err != nil {
		return nil, false
	}
	out := buf.Bytes()
	if len(out) >= len(data) {
		return nil, false
	}
	return out, true
}

func gunzip(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
