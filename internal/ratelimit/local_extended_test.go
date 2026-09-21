package ratelimit

import (
	"context"
	"testing"
)

func TestNewLocalRateLimiter_DefaultConfig(t *testing.T) {
	limiter, err := NewLocalRateLimiter(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	if limiter == nil {
		t.Fatal("Limiter should not be nil")
	}

	if limiter.rateBuckets == nil {
		t.Error("Rate buckets map should be initialized")
	}

	if limiter.slidingWindows == nil {
		t.Error("Sliding windows map should be initialized")
	}

	if limiter.semaphores == nil {
		t.Error("Semaphores map should be initialized")
	}
}

func TestLocalRateLimiter_EmptyKeys(t *testing.T) {
	cfg := &LocalConfig{
		RPM: 100,
	}

	limiter, err := NewLocalRateLimiter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	// Empty keys should be allowed (no limits applied)
	keys := DimensionKeys{}
	result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("Empty keys should be allowed")
	}
}

func TestLocalRateLimiter_GetAvailable(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			RPM: 100,
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Initially should have full capacity
	available, err := limiter.GetAvailable(context.Background(), keys, MetricRPM)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if available != 100 {
		t.Errorf("Expected 100 available, got %d", available)
	}

	// Consume 30 requests (RPM counts each check as one request)
	for i := 0; i < 30; i++ {
		limiter.Check(context.Background(), keys, MetricRPM, 1)
	}

	// Should have less available
	available, err = limiter.GetAvailable(context.Background(), keys, MetricRPM)
	if err != nil {
		t.Fatalf("GetAvailable failed: %v", err)
	}
	if available < 69 || available > 71 {
		t.Errorf("Expected ~70 available, got %d", available)
	}
}

func TestLocalRateLimiter_Reset(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			RPM: 100,
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Consume some
	limiter.Check(context.Background(), keys, MetricRPM, 50)

	// Reset
	err = limiter.Reset(context.Background(), keys)
	if err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Should have full capacity again
	available, _ := limiter.GetAvailable(context.Background(), keys, MetricRPM)
	if available != 100 {
		t.Errorf("Expected 100 available after reset, got %d", available)
	}
}

func TestLocalRateLimiter_Correct(t *testing.T) {
	limiter, err := NewLocalRateLimiter(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Correct should be a no-op for local mode
	err = limiter.Correct(context.Background(), keys, MetricTPMOutput, 100, 50)
	if err != nil {
		t.Errorf("Correct should not fail: %v", err)
	}
}

func TestLocalRateLimiter_Close(t *testing.T) {
	limiter, err := NewLocalRateLimiter(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	err = limiter.Close()
	if err != nil {
		t.Errorf("Close should not fail: %v", err)
	}
}

func TestLocalRateLimiter_ZeroLimit(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			RPM: 0, // Zero means no limit
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Should allow all requests when limit is 0
	for i := 0; i < 1000; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Request %d should be allowed when limit is 0", i)
		}
	}
}

func TestLocalRateLimiter_UnknownMetric(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			RPM: 100,
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Unknown metric should be allowed
	result, err := limiter.Check(context.Background(), keys, "unknown_metric", 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("Unknown metric should be allowed")
	}
}

func TestLocalRateLimiter_MultipleDimensions(t *testing.T) {
	levels := &LevelConfigs{
		Global: &LevelConfig{
			RPM: 1000,
		},
		Tenant: &LevelConfig{
			RPM: 100,
		},
		Model: map[string]*LevelConfig{
			"gpt-4": {
				RPM: 50,
			},
		},
		APIKey: &LevelConfig{
			RPM: 20,
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Global: "global",
		Tenant: "tenant-1",
		Model:  "gpt-4",
		APIKey: "key-1",
	}

	// Consume 20 RPM (API key limit)
	for i := 0; i < 20; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i)
		}
	}

	// 21st request should fail (API key limit reached)
	result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Error("Should deny when API key limit reached")
	}
	if result.Dimension != DimensionAPIKey {
		t.Errorf("Expected blocked by API key dimension, got %s", result.Dimension)
	}
}

func TestRateBucket_ZeroCost(t *testing.T) {
	rb := NewRateBucket(10, 100)

	// Zero cost should always be allowed
	if !rb.Allow(0) {
		t.Error("Zero cost should be allowed")
	}

	// Should not consume any tokens
	available := rb.Available()
	if available != 100 {
		t.Errorf("Expected 100 available after zero cost, got %f", available)
	}
}

func TestSemaphore_ZeroAcquire(t *testing.T) {
	sem := NewSemaphore(5)

	// Zero acquire should always succeed
	if !sem.Acquire(0) {
		t.Error("Zero acquire should succeed")
	}

	if sem.Current() != 0 {
		t.Errorf("Expected current=0 after zero acquire, got %d", sem.Current())
	}
}

func TestSemaphore_NegativeRelease(t *testing.T) {
	sem := NewSemaphore(5)

	// Release when current is 0 should not go negative
	sem.Release(5)

	if sem.Current() != 0 {
		t.Errorf("Expected current=0, got %d", sem.Current())
	}
}
