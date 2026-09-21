package ratelimit

import (
	"context"
	"testing"
	"time"
)

// --- QuotaWindow unit tests (deterministic, explicit clock) ---

func TestQuotaWindow_AddAndDeny(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	w := newQuotaWindow(now, MetricDailyQuota)
	limit := int64(1000)

	if !w.add(now, 1000, limit) {
		t.Fatal("should allow consuming exactly the quota")
	}
	if w.add(now, 1, limit) {
		t.Fatal("should deny when over quota")
	}
}

func TestQuotaWindow_Rollover(t *testing.T) {
	now := time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC)
	w := newQuotaWindow(now, MetricDailyQuota)
	limit := int64(100)

	if !w.add(now, 100, limit) {
		t.Fatal("should allow 100")
	}
	if w.add(now, 1, limit) {
		t.Fatal("should deny when exhausted")
	}

	// Cross the UTC midnight boundary: counter resets.
	next := time.Date(2026, 9, 16, 0, 1, 0, 0, time.UTC)
	if !w.add(next, 100, limit) {
		t.Fatal("should allow after rollover")
	}
}

func TestQuotaWindow_AdjustFloor(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	w := newQuotaWindow(now, MetricDailyQuota)
	limit := int64(100)

	w.add(now, 100, limit)
	w.adjust(now, -200) // over-release must floor at zero used

	if rem := w.remaining(now, limit); rem != 100 {
		t.Fatalf("expected remaining to floor at 100, got %d", rem)
	}
}

func TestQuotaWindow_MonthlyBoundary(t *testing.T) {
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	w := newQuotaWindow(now, MetricMonthlyQuota)
	limit := int64(100)

	w.add(now, 100, limit)

	// First of next month resets the window.
	next := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if !w.add(next, 100, limit) {
		t.Fatal("should allow after monthly rollover")
	}
}

// --- LocalRateLimiter quota tests ---

func TestLocalRateLimiter_DailyQuota(t *testing.T) {
	levels := &LevelConfigs{Tenant: &LevelConfig{DailyQuota: 1000}}
	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}
	keys := DimensionKeys{Tenant: "tenant-1"}
	ctx := context.Background()

	res, err := limiter.Check(ctx, keys, MetricDailyQuota, 1000)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("should allow consuming exactly the quota")
	}

	res, err = limiter.Check(ctx, keys, MetricDailyQuota, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("should deny when over quota")
	}
	if res.Dimension != DimensionTenant {
		t.Fatalf("expected tenant dimension, got %s", res.Dimension)
	}
}

func TestLocalRateLimiter_DailyQuota_Correct(t *testing.T) {
	levels := &LevelConfigs{Tenant: &LevelConfig{DailyQuota: 1000}}
	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}
	keys := DimensionKeys{Tenant: "tenant-1"}
	ctx := context.Background()

	// Pre-deduct the full quota.
	limiter.Check(ctx, keys, MetricDailyQuota, 1000)

	// Correct: actual 400, so release 600 back.
	if err := limiter.Correct(ctx, keys, MetricDailyQuota, 1000, 400); err != nil {
		t.Fatalf("correct failed: %v", err)
	}

	avail, err := limiter.GetAvailable(ctx, keys, MetricDailyQuota)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if avail != 600 {
		t.Fatalf("expected 600 remaining, got %d", avail)
	}
}

func TestLocalRateLimiter_Quota_Unlimited(t *testing.T) {
	levels := &LevelConfigs{Tenant: &LevelConfig{}} // no quota configured
	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("failed to create limiter: %v", err)
	}
	keys := DimensionKeys{Tenant: "tenant-1"}
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		res, err := limiter.Check(ctx, keys, MetricMonthlyQuota, 1)
		if err != nil {
			t.Fatalf("check failed: %v", err)
		}
		if !res.Allowed {
			t.Fatal("unlimited quota should always allow")
		}
	}
}

// --- RedisRateLimiter quota tests ---

func TestRedisRateLimiter_DailyQuota(t *testing.T) {
	levels := &LevelConfigs{Tenant: &LevelConfig{DailyQuota: 1000}}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	res, err := limiter.Check(ctx, keys, MetricDailyQuota, 1000)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !res.Allowed {
		t.Fatal("should allow consuming exactly the quota")
	}

	res, err = limiter.Check(ctx, keys, MetricDailyQuota, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("should deny when over quota")
	}
}

func TestRedisRateLimiter_DailyQuota_Correct(t *testing.T) {
	levels := &LevelConfigs{Tenant: &LevelConfig{DailyQuota: 1000}}
	limiter, _ := newTestRedisLimiter(t, levels)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "tenant-1"}

	limiter.Check(ctx, keys, MetricDailyQuota, 1000)

	if err := limiter.Correct(ctx, keys, MetricDailyQuota, 1000, 400); err != nil {
		t.Fatalf("correct failed: %v", err)
	}

	avail, err := limiter.GetAvailable(ctx, keys, MetricDailyQuota)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if avail != 600 {
		t.Fatalf("expected 600 remaining, got %d", avail)
	}
}
