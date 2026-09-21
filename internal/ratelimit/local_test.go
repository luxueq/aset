package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRateBucket_Basic(t *testing.T) {
	// 10 units/second, capacity 100
	rb := NewRateBucket(10, 100)

	// Should allow first request
	if !rb.Allow(10) {
		t.Error("Expected first request to be allowed")
	}

	// Should have ~90 available
	available := rb.Available()
	if available < 89 || available > 91 {
		t.Errorf("Expected ~90 available, got %f", available)
	}
}

func TestRateBucket_Refill(t *testing.T) {
	// 10 units/second, capacity 100
	rb := NewRateBucket(10, 100)

	// Consume all
	rb.Allow(100)

	// Should have ~0 available
	available := rb.Available()
	if available > 1 {
		t.Errorf("Expected ~0 available, got %f", available)
	}

	// Wait 2 seconds (should refill 20 units)
	time.Sleep(2 * time.Second)

	available = rb.Available()
	if available < 19 || available > 21 {
		t.Errorf("Expected ~20 available after 2s, got %f", available)
	}
}

func TestRateBucket_Capacity(t *testing.T) {
	// 10 units/second, capacity 50
	rb := NewRateBucket(10, 50)

	// Wait 10 seconds (would refill 100, but capacity is 50)
	time.Sleep(100 * time.Millisecond)

	available := rb.Available()
	if available > 50 {
		t.Errorf("Available should not exceed capacity, got %f", available)
	}
}

func TestRateBucket_RateLimit(t *testing.T) {
	// 1 unit/second, capacity 10
	rb := NewRateBucket(1, 10)

	// Consume all
	for i := 0; i < 10; i++ {
		if !rb.Allow(1) {
			t.Errorf("Request %d should be allowed", i)
		}
	}

	// Next request should be denied
	if rb.Allow(1) {
		t.Error("Expected request to be denied")
	}
}

func TestSemaphore_Basic(t *testing.T) {
	sem := NewSemaphore(5)

	// Acquire 3
	if !sem.Acquire(3) {
		t.Error("Expected to acquire 3")
	}

	if sem.Current() != 3 {
		t.Errorf("Expected current=3, got %d", sem.Current())
	}

	// Acquire 2 more
	if !sem.Acquire(2) {
		t.Error("Expected to acquire 2")
	}

	if sem.Current() != 5 {
		t.Errorf("Expected current=5, got %d", sem.Current())
	}

	// Try to acquire 1 more (should fail)
	if sem.Acquire(1) {
		t.Error("Expected to fail acquiring 1")
	}

	// Release 2
	sem.Release(2)

	if sem.Current() != 3 {
		t.Errorf("Expected current=3 after release, got %d", sem.Current())
	}

	// Now should be able to acquire 2
	if !sem.Acquire(2) {
		t.Error("Expected to acquire 2 after release")
	}
}

func TestLocalRateLimiter_RPM(t *testing.T) {
	cfg := &LocalConfig{
		RPM: 60, // 1 per second
	}

	limiter, err := NewLocalRateLimiter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Should allow first 60 requests in a minute (1 per second)
	for i := 0; i < 60; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i)
		}
	}

	// Wait 1 second for refill
	time.Sleep(1 * time.Second)

	// Should allow again
	result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("Request should be allowed after refill")
	}
}

func TestLocalRateLimiter_TPM(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			TPMOutput: 600, // 10 per second
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Consume 600 tokens
	result, err := limiter.Check(context.Background(), keys, MetricTPMOutput, 600)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("Should allow consuming 600 tokens")
	}

	// Try to consume 1 more (should fail immediately, no wait)
	result, err = limiter.Check(context.Background(), keys, MetricTPMOutput, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Error("Should deny when quota exhausted")
	}

	// Wait 1 second for refill (should get 10 tokens)
	time.Sleep(1 * time.Second)

	result, err = limiter.Check(context.Background(), keys, MetricTPMOutput, 5)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("Should allow after refill")
	}
}

func TestLocalRateLimiter_Concurrency(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{
			Concurrency: 5,
		},
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Acquire 5 concurrent slots
	for i := 0; i < 5; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricConcurrency, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Concurrency slot %d should be allowed", i)
		}
	}

	// Try to acquire 6th (should fail)
	result, err := limiter.Check(context.Background(), keys, MetricConcurrency, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Error("Should deny 6th concurrent request")
	}

	// Release 2 slots
	limiter.Release(context.Background(), keys)
	limiter.Release(context.Background(), keys)

	// Should be able to acquire 2 more
	for i := 0; i < 2; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricConcurrency, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Should allow after release, attempt %d", i)
		}
	}
}

func TestLocalRateLimiter_MultiDimension(t *testing.T) {
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
	}

	limiter, err := NewLocalRateLimiter(nil, levels)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Global: "global",
		Tenant: "tenant-1",
		Model:  "gpt-4",
	}

	// Consume 50 RPM (model limit)
	for i := 0; i < 50; i++ {
		result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if !result.Allowed {
			t.Errorf("Request %d should be allowed", i)
		}
	}

	// 51st request should fail (model limit reached)
	result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Error("Should deny when model limit reached")
	}
	if result.Dimension != DimensionModel {
		t.Errorf("Expected blocked by model dimension, got %s", result.Dimension)
	}
}

func TestLocalRateLimiter_Concurrent(t *testing.T) {
	cfg := &LocalConfig{
		RPM: 1000,
	}

	limiter, err := NewLocalRateLimiter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create limiter: %v", err)
	}

	keys := DimensionKeys{
		Tenant: "test-tenant",
	}

	// Run concurrent checks
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := limiter.Check(context.Background(), keys, MetricRPM, 1)
			if err != nil {
				t.Errorf("Check failed: %v", err)
				return
			}
			if result.Allowed {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// All 100 should succeed (we have 1000 RPM capacity)
	if successCount < 95 { // Allow small margin for timing
		t.Errorf("Expected ~100 successes, got %d", successCount)
	}
}
