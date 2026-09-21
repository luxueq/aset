package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RateBucket implements a rate limiter using the token bucket algorithm.
// Uses "available" instead of "tokens" to avoid confusion with LLM tokens.
type RateBucket struct {
	mu         sync.Mutex
	available  float64   // current available units
	lastTime   time.Time // last access time
	refillRate float64   // units added per second
	capacity   float64   // max units (burst size)
}

// NewRateBucket creates a new rate bucket.
// refillRate: units added per second
// capacity: maximum units (burst size)
func NewRateBucket(refillRate float64, capacity float64) *RateBucket {
	return &RateBucket{
		available:  capacity,
		lastTime:   time.Now(),
		refillRate: refillRate,
		capacity:   capacity,
	}
}

// Allow checks if a request with the given cost is allowed.
// cost: units to consume (can be 1 request, or N LLM tokens, etc.)
func (rb *RateBucket) Allow(cost float64) bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// Refill based on elapsed time
	now := time.Now()
	elapsed := now.Sub(rb.lastTime).Seconds()
	rb.available += elapsed * rb.refillRate
	if rb.available > rb.capacity {
		rb.available = rb.capacity
	}
	rb.lastTime = now

	// Check if enough available
	if rb.available >= cost {
		rb.available -= cost
		return true
	}

	return false
}

// Available returns the current available units.
func (rb *RateBucket) Available() float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rb.lastTime).Seconds()
	available := rb.available + elapsed*rb.refillRate
	if available > rb.capacity {
		available = rb.capacity
	}
	return available
}

// SlidingWindow implements a rate limiter using the sliding window algorithm.
// More strict than token bucket - guarantees no boundary bursts.
type SlidingWindow struct {
	mu         sync.Mutex
	timestamps []time.Time // request timestamps within the window
	limit      int         // max requests per window
	window     time.Duration
}

// NewSlidingWindow creates a new sliding window rate limiter.
// limit: max requests per window
// window: time window duration
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		timestamps: make([]time.Time, 0),
		limit:      limit,
		window:     window,
	}
}

// Allow checks if a request is allowed.
func (sw *SlidingWindow) Allow() bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-sw.window)

	// Remove expired timestamps
	validTimestamps := make([]time.Time, 0, len(sw.timestamps))
	for _, ts := range sw.timestamps {
		if ts.After(windowStart) {
			validTimestamps = append(validTimestamps, ts)
		}
	}
	sw.timestamps = validTimestamps

	// Check if limit exceeded
	if len(sw.timestamps) >= sw.limit {
		return false
	}

	// Add current timestamp
	sw.timestamps = append(sw.timestamps, now)
	return true
}

// Count returns the current request count in the window.
func (sw *SlidingWindow) Count() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-sw.window)

	count := 0
	for _, ts := range sw.timestamps {
		if ts.After(windowStart) {
			count++
		}
	}
	return count
}

// Remaining returns the remaining requests allowed in the window.
func (sw *SlidingWindow) Remaining() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-sw.window)

	count := 0
	for _, ts := range sw.timestamps {
		if ts.After(windowStart) {
			count++
		}
	}
	return sw.limit - count
}

// RetryAfter returns the duration until the next request can be allowed.
func (sw *SlidingWindow) RetryAfter() time.Duration {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	if len(sw.timestamps) < sw.limit {
		return 0
	}

	// Find the oldest timestamp in the window
	now := time.Now()
	windowStart := now.Add(-sw.window)

	for _, ts := range sw.timestamps {
		if ts.After(windowStart) {
			// This is the oldest request, it will expire at ts + sw.window
			return ts.Add(sw.window).Sub(now)
		}
	}

	return sw.window
}

// Semaphore implements a concurrency limiter using atomic operations.
// Uses "acquire first, check and rollback if exceeded" pattern for better performance.
type Semaphore struct {
	current atomic.Int64
	max     int64
}

// NewSemaphore creates a new semaphore with the given max concurrency.
func NewSemaphore(max int) *Semaphore {
	return &Semaphore{
		max: int64(max),
	}
}

// Acquire tries to acquire n units of concurrency.
// Uses atomic add + rollback pattern for lock-free, high-performance synchronization.
func (s *Semaphore) Acquire(n int) bool {
	// Atomically add n and get the new value
	newVal := s.current.Add(int64(n))

	// Check if exceeded the limit
	if newVal > s.max {
		// Rollback: subtract n to release the acquired units
		s.current.Add(-int64(n))
		return false
	}

	return true
}

// Release releases n units of concurrency.
func (s *Semaphore) Release(n int) {
	// Atomically subtract n
	s.current.Add(-int64(n))

	// Ensure we don't go negative (defensive programming)
	// Note: This check is not atomic with the subtract, but it's safe
	// because we only release what we acquired, and multiple releases
	// would still be bounded by actual acquisitions
	for {
		current := s.current.Load()
		if current >= 0 {
			break
		}
		// If somehow went negative, reset to 0
		if s.current.CompareAndSwap(current, 0) {
			break
		}
	}
}

// Current returns the current concurrency.
func (s *Semaphore) Current() int {
	return int(s.current.Load())
}

// LocalRateLimiter implements rate limiting using local rate buckets and sliding windows.
// Suitable for single-instance deployments or development environments.
// Uses different algorithms for different metrics:
// - RPM: Sliding window (strict, prevents boundary bursts)
// - TPM: Token bucket (natural fit for token-based limiting)
// - Concurrency: Semaphore (counter-based)
type LocalRateLimiter struct {
	mu             sync.RWMutex
	rateBuckets    map[string]*RateBucket      // for TPM
	slidingWindows map[string]*SlidingWindow   // for RPM
	semaphores     map[string]*Semaphore       // for Concurrency
	quotaWindows   map[string]*QuotaWindow     // for daily/monthly quota
	configs        *LevelConfigs
}

// NewLocalRateLimiter creates a new local rate limiter.
func NewLocalRateLimiter(cfg *LocalConfig, levels *LevelConfigs) (*LocalRateLimiter, error) {
	if cfg == nil {
		cfg = &LocalConfig{}
	}

	// If levels is nil, create default from LocalConfig
	if levels == nil {
		levels = &LevelConfigs{
			Global: &LevelConfig{
				RPM:         cfg.RPM,
				TPMInput:    cfg.TPMInput,
				TPMOutput:   cfg.TPMOutput,
				Concurrency: cfg.Concurrency,
			},
		}
	}

	return &LocalRateLimiter{
		rateBuckets:    make(map[string]*RateBucket),
		slidingWindows: make(map[string]*SlidingWindow),
		semaphores:     make(map[string]*Semaphore),
		quotaWindows:   make(map[string]*QuotaWindow),
		configs:        levels,
	}, nil
}

// Check checks if a request is allowed.
func (l *LocalRateLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	// Check all dimensions in order: Global → Tenant → Model → API Key
	dimensions := []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	}

	for _, d := range dimensions {
		if d.key == "" {
			continue
		}

		result := l.checkDimension(d.dim, d.key, metric, cost)
		if !result.Allowed {
			return result, nil
		}
	}

	return &LimitResult{Allowed: true}, nil
}

// checkDimension checks a single dimension.
func (l *LocalRateLimiter) checkDimension(dim Dimension, key string, metric Metric, cost int64) *LimitResult {
	levelCfg := l.configs.GetLevelConfig(dim, key)
	if levelCfg == nil {
		return &LimitResult{Allowed: true}
	}

	bucketKey := fmt.Sprintf("%s:%s:%s", dim, key, metric)

	// Handle concurrency separately (use semaphore)
	if metric == MetricConcurrency {
		return l.checkConcurrency(dim, key, levelCfg.Concurrency, int(cost))
	}

	// Handle fixed-window quota (daily/monthly) separately
	if isQuotaMetric(metric) {
		return l.checkQuota(dim, key, metric, cost)
	}

	// Choose algorithm based on metric type:
	// - RPM: Sliding window (strict, prevents boundary bursts)
	// - TPM: Token bucket (natural fit for token-based limiting)

	if metric == MetricRPM {
		// Use sliding window for RPM
		limit := int64(levelCfg.RPM)
		if limit == 0 {
			return &LimitResult{Allowed: true}
		}

		window := l.getOrCreateSlidingWindow(bucketKey, int(limit), 1*time.Minute)
		if !window.Allow() {
			return &LimitResult{
				Allowed:    false,
				Dimension:  dim,
				Metric:     metric,
				Limit:      limit,
				Remaining:  0,
				RetryAfter: window.RetryAfter(),
			}
		}

		return &LimitResult{
			Allowed:   true,
			Dimension: dim,
			Metric:    metric,
			Limit:     limit,
			Remaining: int64(window.Remaining()),
		}
	}

	// Use token bucket for TPM
	var limit int64
	var refillRate float64

	switch metric {
	case MetricTPMInput:
		limit = levelCfg.TPMInput
		refillRate = float64(levelCfg.TPMInput) / 60.0
	case MetricTPMOutput:
		limit = levelCfg.TPMOutput
		refillRate = float64(levelCfg.TPMOutput) / 60.0
	default:
		return &LimitResult{Allowed: true}
	}

	if limit == 0 {
		return &LimitResult{Allowed: true}
	}

	bucket := l.getOrCreateBucket(bucketKey, refillRate, float64(limit))
	if !bucket.Allow(float64(cost)) {
		return &LimitResult{
			Allowed:   false,
			Dimension: dim,
			Metric:    metric,
			Limit:     limit,
			Remaining: 0,
		}
	}

	return &LimitResult{
		Allowed:   true,
		Dimension: dim,
		Metric:    metric,
		Limit:     limit,
		Remaining: int64(bucket.Available()),
	}
}

// checkConcurrency checks concurrency limit.
func (l *LocalRateLimiter) checkConcurrency(dim Dimension, key string, maxConcurrency int, cost int) *LimitResult {
	if maxConcurrency == 0 {
		return &LimitResult{Allowed: true}
	}

	semKey := fmt.Sprintf("%s:%s:concurrency", dim, key)
	sem := l.getOrCreateSemaphore(semKey, maxConcurrency)

	if !sem.Acquire(cost) {
		return &LimitResult{
			Allowed:   false,
			Dimension: dim,
			Metric:    MetricConcurrency,
			Limit:     int64(maxConcurrency),
			Remaining: int64(maxConcurrency - sem.Current()),
		}
	}

	return &LimitResult{
		Allowed:   true,
		Dimension: dim,
		Metric:    MetricConcurrency,
		Limit:     int64(maxConcurrency),
		Remaining: int64(maxConcurrency - sem.Current()),
	}
}

// checkQuota checks a fixed-window (daily/monthly) token quota.
func (l *LocalRateLimiter) checkQuota(dim Dimension, key string, metric Metric, cost int64) *LimitResult {
	levelCfg := l.configs.GetLevelConfig(dim, key)
	if levelCfg == nil {
		return &LimitResult{Allowed: true}
	}
	limit := quotaLimit(levelCfg, metric)
	if limit <= 0 {
		return &LimitResult{Allowed: true}
	}

	bucketKey := fmt.Sprintf("%s:%s:%s", dim, key, metric)
	window := l.getOrCreateQuotaWindow(bucketKey, metric)
	now := time.Now()

	if !window.add(now, cost, limit) {
		return &LimitResult{
			Allowed:    false,
			Dimension:  dim,
			Metric:     metric,
			Limit:      limit,
			Remaining:  0,
			RetryAfter: time.Until(quotaResetTime(now, metric)),
		}
	}

	return &LimitResult{
		Allowed:   true,
		Dimension: dim,
		Metric:    metric,
		Limit:     limit,
		Remaining: window.remaining(now, limit),
	}
}

// Release releases concurrency quota.
func (l *LocalRateLimiter) Release(ctx context.Context, keys DimensionKeys) error {
	dimensions := []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	}

	for _, d := range dimensions {
		if d.key == "" {
			continue
		}

		semKey := fmt.Sprintf("%s:%s:concurrency", d.dim, d.key)
		l.mu.RLock()
		sem, exists := l.semaphores[semKey]
		l.mu.RUnlock()

		if exists {
			sem.Release(1)
		}
	}

	return nil
}

// Correct corrects pre-deducted quota with actual usage.
// Only fixed-window quota metrics need correction; rate bucket uses real-time
// calculation and concurrency is released separately via Release.
func (l *LocalRateLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error {
	if !isQuotaMetric(metric) {
		return nil
	}
	delta := actual - preDeducted
	if delta == 0 {
		return nil
	}

	now := time.Now()
	for _, d := range []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	} {
		if d.key == "" {
			continue
		}
		levelCfg := l.configs.GetLevelConfig(d.dim, d.key)
		if levelCfg == nil || quotaLimit(levelCfg, metric) <= 0 {
			continue
		}
		bucketKey := fmt.Sprintf("%s:%s:%s", d.dim, d.key, metric)
		window := l.getOrCreateQuotaWindow(bucketKey, metric)
		window.adjust(now, delta)
	}
	return nil
}

// GetAvailable returns available quota for a dimension/metric.
func (l *LocalRateLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	// Return the minimum available across all dimensions
	var minAvailable int64 = -1

	dimensions := []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	}

	for _, d := range dimensions {
		if d.key == "" {
			continue
		}

		levelCfg := l.configs.GetLevelConfig(d.dim, d.key)
		if levelCfg == nil {
			continue
		}

		var limit int64
		switch metric {
		case MetricRPM:
			limit = int64(levelCfg.RPM)
		case MetricTPMInput:
			limit = levelCfg.TPMInput
		case MetricTPMOutput:
			limit = levelCfg.TPMOutput
		case MetricConcurrency:
			semKey := fmt.Sprintf("%s:%s:concurrency", d.dim, d.key)
			l.mu.RLock()
			sem, exists := l.semaphores[semKey]
			l.mu.RUnlock()
			if exists {
				available := int64(levelCfg.Concurrency - sem.Current())
				if minAvailable == -1 || available < minAvailable {
					minAvailable = available
				}
			} else {
				// Semaphore not created yet, full capacity available
				if minAvailable == -1 || int64(levelCfg.Concurrency) < minAvailable {
					minAvailable = int64(levelCfg.Concurrency)
				}
			}
			continue
		case MetricDailyQuota, MetricMonthlyQuota:
			qlimit := quotaLimit(levelCfg, metric)
			if qlimit <= 0 {
				continue
			}
			bucketKey := fmt.Sprintf("%s:%s:%s", d.dim, d.key, metric)
			window := l.getOrCreateQuotaWindow(bucketKey, metric)
			available := window.remaining(time.Now(), qlimit)
			if minAvailable == -1 || available < minAvailable {
				minAvailable = available
			}
			continue
		default:
			continue
		}

		if limit == 0 {
			continue
		}

		bucketKey := fmt.Sprintf("%s:%s:%s", d.dim, d.key, metric)

		// Choose the right data structure based on metric type
		if metric == MetricRPM {
			// Use sliding window for RPM
			l.mu.RLock()
			window, exists := l.slidingWindows[bucketKey]
			l.mu.RUnlock()

			var available int64
			if exists {
				available = int64(window.Remaining())
			} else {
				// Window not created yet, full capacity available
				available = limit
			}

			if minAvailable == -1 || available < minAvailable {
				minAvailable = available
			}
		} else {
			// Use rate bucket for TPM
			l.mu.RLock()
			bucket, exists := l.rateBuckets[bucketKey]
			l.mu.RUnlock()

			var available int64
			if exists {
				available = int64(bucket.Available())
			} else {
				// Bucket not created yet, full capacity available
				available = limit
			}

			if minAvailable == -1 || available < minAvailable {
				minAvailable = available
			}
		}
	}

	if minAvailable == -1 {
		return 0, nil
	}

	return minAvailable, nil
}

// Reset clears all rate limit data.
func (l *LocalRateLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rateBuckets = make(map[string]*RateBucket)
	l.slidingWindows = make(map[string]*SlidingWindow)
	l.semaphores = make(map[string]*Semaphore)
	l.quotaWindows = make(map[string]*QuotaWindow)

	return nil
}

// Close cleans up resources.
func (l *LocalRateLimiter) Close() error {
	return nil
}

// getOrCreateBucket gets or creates a rate bucket.
func (l *LocalRateLimiter) getOrCreateBucket(key string, refillRate, capacity float64) *RateBucket {
	l.mu.RLock()
	bucket, exists := l.rateBuckets[key]
	l.mu.RUnlock()

	if exists {
		return bucket
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if bucket, exists = l.rateBuckets[key]; exists {
		return bucket
	}

	bucket = NewRateBucket(refillRate, capacity)
	l.rateBuckets[key] = bucket
	return bucket
}

// getOrCreateSlidingWindow gets or creates a sliding window.
func (l *LocalRateLimiter) getOrCreateSlidingWindow(key string, limit int, window time.Duration) *SlidingWindow {
	l.mu.RLock()
	sw, exists := l.slidingWindows[key]
	l.mu.RUnlock()

	if exists {
		return sw
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if sw, exists = l.slidingWindows[key]; exists {
		return sw
	}

	sw = NewSlidingWindow(limit, window)
	l.slidingWindows[key] = sw
	return sw
}

// getOrCreateSemaphore gets or creates a semaphore.
func (l *LocalRateLimiter) getOrCreateSemaphore(key string, max int) *Semaphore {
	l.mu.RLock()
	sem, exists := l.semaphores[key]
	l.mu.RUnlock()

	if exists {
		return sem
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if sem, exists = l.semaphores[key]; exists {
		return sem
	}

	sem = NewSemaphore(max)
	l.semaphores[key] = sem
	return sem
}

// getOrCreateQuotaWindow gets or creates a quota window.
func (l *LocalRateLimiter) getOrCreateQuotaWindow(key string, metric Metric) *QuotaWindow {
	l.mu.RLock()
	window, exists := l.quotaWindows[key]
	l.mu.RUnlock()

	if exists {
		return window
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if window, exists = l.quotaWindows[key]; exists {
		return window
	}

	window = newQuotaWindow(time.Now(), metric)
	l.quotaWindows[key] = window
	return window
}
