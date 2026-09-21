package ratelimit

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedisLimiter starts an in-memory miniredis and returns a limiter
// bound to it, plus the miniredis instance for inspection.
func newTestRedisLimiter(t *testing.T, levels *LevelConfigs) (*RedisRateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	limiter, err := NewRedisRateLimiter(client, levels)
	if err != nil {
		t.Fatalf("failed to create redis limiter: %v", err)
	}
	return limiter, mr
}

func TestRedisRateLimiter_Concurrency_AtomicAcquire(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{Concurrency: 2},
	}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	// Acquire 2 slots — both allowed.
	for i := 0; i < 2; i++ {
		res, err := limiter.Check(ctx, keys, MetricConcurrency, 1)
		if err != nil {
			t.Fatalf("check %d failed: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("slot %d should be allowed", i)
		}
	}

	// 3rd acquire must be denied atomically (not over-admitted).
	res, err := limiter.Check(ctx, keys, MetricConcurrency, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("3rd concurrent request should be denied")
	}
	if res.Remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", res.Remaining)
	}

	// Release one slot, then acquire again — allowed.
	if err := limiter.Release(ctx, keys); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	res, err = limiter.Check(ctx, keys, MetricConcurrency, 1)
	if err != nil {
		t.Fatalf("check after release failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("should be allowed after releasing a slot")
	}
}

func TestRedisRateLimiter_Concurrency_ReleaseFloor(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{Concurrency: 5},
	}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	// Release without any acquire must NOT drive the counter negative.
	for i := 0; i < 10; i++ {
		if err := limiter.Release(ctx, keys); err != nil {
			t.Fatalf("release %d failed: %v", i, err)
		}
	}

	// Available must remain full (5), not a negative-corrupted value.
	avail, err := limiter.GetAvailable(ctx, keys, MetricConcurrency)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if avail != 5 {
		t.Fatalf("expected 5 available after over-release, got %d", avail)
	}
}

func TestRedisRateLimiter_TPM_TokenBucket(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{TPMOutput: 600},
	}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	// Consume the full bucket.
	res, err := limiter.Check(ctx, keys, MetricTPMOutput, 600)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("should allow consuming full quota")
	}

	// Next request over budget is denied.
	res, err = limiter.Check(ctx, keys, MetricTPMOutput, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("should deny when quota exhausted")
	}
}

func TestRedisRateLimiter_RPM_SlidingWindow(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 3},
	}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	for i := 0; i < 3; i++ {
		res, err := limiter.Check(ctx, keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("check %d failed: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	res, err := limiter.Check(ctx, keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("4th request within window should be denied")
	}
}

func TestRedisRateLimiter_Reset(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 3},
	}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	for i := 0; i < 3; i++ {
		limiter.Check(ctx, keys, MetricRPM, 1)
	}

	if err := limiter.Reset(ctx, keys); err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	avail, err := limiter.GetAvailable(ctx, keys, MetricRPM)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if avail != 3 {
		t.Fatalf("expected full 3 available after reset, got %d", avail)
	}
}
