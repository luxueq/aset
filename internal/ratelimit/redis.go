package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed rate_bucket.lua
var rateBucketLua string

//go:embed sliding_window.lua
var slidingWindowLua string

//go:embed concurrency.lua
var concurrencyLua string

//go:embed concurrency_release.lua
var concurrencyReleaseLua string

//go:embed quota.lua
var quotaLua string

//go:embed quota_correct.lua
var quotaCorrectLua string

// RedisRateLimiter implements distributed rate limiting using Redis.
// Uses Lua scripts for atomic operations.
// Supports both standalone and cluster mode.
type RedisRateLimiter struct {
	client                   redis.UniversalClient
	rateBucketScript         *redis.Script
	slidingWindowScript      *redis.Script
	concurrencyScript        *redis.Script
	concurrencyReleaseScript *redis.Script
	quotaScript              *redis.Script
	quotaCorrectScript       *redis.Script
	configs                  *LevelConfigs
}

// NewRedisRateLimiter creates a new Redis-based rate limiter.
func NewRedisRateLimiter(client redis.UniversalClient, levels *LevelConfigs) (*RedisRateLimiter, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	if levels == nil {
		levels = &LevelConfigs{}
	}

	return &RedisRateLimiter{
		client:                   client,
		rateBucketScript:         redis.NewScript(rateBucketLua),
		slidingWindowScript:      redis.NewScript(slidingWindowLua),
		concurrencyScript:        redis.NewScript(concurrencyLua),
		concurrencyReleaseScript: redis.NewScript(concurrencyReleaseLua),
		quotaScript:              redis.NewScript(quotaLua),
		quotaCorrectScript:       redis.NewScript(quotaCorrectLua),
		configs:                  levels,
	}, nil
}

// Check checks if a request is allowed.
func (r *RedisRateLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
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

		result, err := r.checkDimension(ctx, d.dim, d.key, metric, cost)
		if err != nil {
			return nil, err
		}
		if !result.Allowed {
			return result, nil
		}
	}

	return &LimitResult{Allowed: true}, nil
}

// checkDimension checks a single dimension.
func (r *RedisRateLimiter) checkDimension(ctx context.Context, dim Dimension, key string, metric Metric, cost int64) (*LimitResult, error) {
	levelCfg := r.configs.GetLevelConfig(dim, key)
	if levelCfg == nil {
		return &LimitResult{Allowed: true}, nil
	}

	// Handle concurrency separately (use simple INCR/DECR)
	if metric == MetricConcurrency {
		return r.checkConcurrency(ctx, dim, key, levelCfg.Concurrency, int(cost))
	}

	// Handle fixed-window quota (daily/monthly) separately
	if isQuotaMetric(metric) {
		return r.checkQuota(ctx, dim, key, metric, levelCfg, cost)
	}

	// Handle RPM/TPM/Concurrency
	var limit int64
	var refillRate float64

	switch metric {
	case MetricRPM:
		limit = int64(levelCfg.RPM)
		refillRate = float64(levelCfg.RPM) / 60.0 // per second
	case MetricTPMInput:
		limit = levelCfg.TPMInput
		refillRate = float64(levelCfg.TPMInput) / 60.0
	case MetricTPMOutput:
		limit = levelCfg.TPMOutput
		refillRate = float64(levelCfg.TPMOutput) / 60.0
	default:
		return &LimitResult{Allowed: true}, nil
	}

	if limit == 0 {
		return &LimitResult{Allowed: true}, nil
	}

	// Build Redis key with hash tag for cluster compatibility
	redisKey := fmt.Sprintf("rl:%s:{%s}:%s", dim, key, metric)

	// Choose algorithm based on metric type:
	// - RPM: Sliding window (strict, prevents boundary bursts)
	// - TPM: Token bucket (natural fit, tokens = tokens)
	var result interface{}
	var err error

	if metric == MetricRPM {
		// Use sliding window for RPM (strict rate limiting)
		now := time.Now().UnixMilli()
		windowSize := int64(60000) // 1 minute in milliseconds
		result, err = r.slidingWindowScript.Run(ctx, r.client, []string{redisKey},
			limit,
			windowSize,
			now,
			cost,
		).Result()

		if err != nil {
			return nil, fmt.Errorf("redis script error: %w", err)
		}

		// Parse result: {allowed, remaining, retry_after_ms}
		resultArr := result.([]interface{})
		allowed := resultArr[0].(int64) == 1
		remaining := resultArr[1].(int64)
		retryAfterMs := resultArr[2].(int64)

		if !allowed {
			return &LimitResult{
				Allowed:    false,
				Dimension:  dim,
				Metric:     metric,
				Limit:      limit,
				Remaining:  remaining,
				RetryAfter: time.Duration(retryAfterMs) * time.Millisecond,
			}, nil
		}

		return &LimitResult{
			Allowed:   true,
			Dimension: dim,
			Metric:    metric,
			Limit:     limit,
			Remaining: remaining,
		}, nil
	}

	// Use token bucket for TPM (natural fit for token-based limiting)
	now := time.Now().UnixNano()
	result, err = r.rateBucketScript.Run(ctx, r.client, []string{redisKey},
		cost,
		refillRate,
		float64(limit),
		now,
	).Result()

	if err != nil {
		return nil, fmt.Errorf("redis script error: %w", err)
	}

	// Parse result: {allowed, remaining}
	resultArr := result.([]interface{})
	allowed := resultArr[0].(int64) == 1
	remaining := luaInt64(resultArr[1])

	if !allowed {
		return &LimitResult{
			Allowed:   false,
			Dimension: dim,
			Metric:    metric,
			Limit:     limit,
			Remaining: remaining,
		}, nil
	}

	return &LimitResult{
		Allowed:   true,
		Dimension: dim,
		Metric:    metric,
		Limit:     limit,
		Remaining: remaining,
	}, nil
}

// checkConcurrency checks concurrency limit using an atomic Lua script.
// The acquire (INCR) and limit check + rollback happen atomically to prevent
// concurrent over-admission (TOCTOU race).
func (r *RedisRateLimiter) checkConcurrency(ctx context.Context, dim Dimension, key string, maxConcurrency int, cost int) (*LimitResult, error) {
	if maxConcurrency == 0 {
		return &LimitResult{Allowed: true}, nil
	}

	// Build Redis key with hash tag
	redisKey := fmt.Sprintf("rl:%s:{%s}:concurrency", dim, key)

	// Atomic acquire + check + rollback via Lua
	result, err := r.concurrencyScript.Run(ctx, r.client, []string{redisKey},
		int64(cost),
		int64(maxConcurrency),
		int64(3600), // 1 hour TTL as orphan-key safety net
	).Result()

	if err != nil {
		return nil, fmt.Errorf("redis concurrency script error: %w", err)
	}

	resultArr := result.([]interface{})
	allowed := resultArr[0].(int64) == 1
	current := resultArr[1].(int64)

	if !allowed {
		return &LimitResult{
			Allowed:   false,
			Dimension: dim,
			Metric:    MetricConcurrency,
			Limit:     int64(maxConcurrency),
			Remaining: int64(maxConcurrency) - current,
		}, nil
	}

	return &LimitResult{
		Allowed:   true,
		Dimension: dim,
		Metric:    MetricConcurrency,
		Limit:     int64(maxConcurrency),
		Remaining: int64(maxConcurrency) - current,
	}, nil
}

// checkQuota checks a fixed-window (daily/monthly) token quota using an
// atomic Lua counter.
func (r *RedisRateLimiter) checkQuota(ctx context.Context, dim Dimension, key string, metric Metric, levelCfg *LevelConfig, cost int64) (*LimitResult, error) {
	limit := quotaLimit(levelCfg, metric)
	if limit <= 0 {
		return &LimitResult{Allowed: true}, nil
	}

	redisKey := fmt.Sprintf("rl:%s:{%s}:%s", dim, key, metric)
	now := time.Now()
	ttl := quotaTTL(now, metric)

	result, err := r.quotaScript.Run(ctx, r.client, []string{redisKey}, cost, limit, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("redis quota script error: %w", err)
	}

	resultArr := result.([]interface{})
	allowed := resultArr[0].(int64) == 1
	used := resultArr[1].(int64)
	limitVal := resultArr[2].(int64)

	res := &LimitResult{
		Allowed:   allowed,
		Dimension: dim,
		Metric:    metric,
		Limit:     limitVal,
		Remaining: limitVal - used,
	}
	if !allowed {
		res.Remaining = 0
		res.RetryAfter = time.Duration(ttl) * time.Second
	}
	return res, nil
}

// Release releases concurrency quota.
func (r *RedisRateLimiter) Release(ctx context.Context, keys DimensionKeys) error {
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

		redisKey := fmt.Sprintf("rl:%s:{%s}:concurrency", d.dim, d.key)
		// Atomic decrement with floor at 0 (prevents negative counter on
		// double release or release without a matching acquire).
		err := r.concurrencyReleaseScript.Run(ctx, r.client, []string{redisKey}, int64(1)).Err()
		if err != nil {
			// Log error but continue (don't fail the request)
			continue
		}
	}

	return nil
}

// Correct corrects pre-deducted quota with actual usage.
// Only fixed-window quota metrics need correction: rate bucket uses real-time
// calculation, and concurrency is released separately via Release.
//
// The per-dimension corrections are batched into a single pipeline (one round
// trip in standalone mode; go-redis splits it by node/slot in cluster mode).
// Each command touches a single key, so it stays cluster-compatible. Because a
// pipeline is not a transaction, a per-command failure does not stop the rest;
// we inspect each command's error and report the first one for logging without
// failing the whole correction (quota is eventually consistent).
func (r *RedisRateLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error {
	if !isQuotaMetric(metric) {
		return nil
	}

	delta := actual - preDeducted
	if delta == 0 {
		return nil
	}

	// Collect non-empty dimension targets first so pipeline results map back.
	type target struct {
		dim Dimension
		key string
	}
	var targets []target
	for _, d := range []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	} {
		if d.key != "" {
			targets = append(targets, target{d.dim, d.key})
		}
	}
	if len(targets) == 0 {
		return nil
	}

	cmds, err := r.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, t := range targets {
			redisKey := fmt.Sprintf("rl:%s:{%s}:%s", t.dim, t.key, metric)
			// Eval (not EvalSha) to avoid a NOSCRIPT round-trip on first use.
			r.quotaCorrectScript.Eval(ctx, pipe, []string{redisKey}, delta)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis quota correct pipeline: %w", err)
	}

	// Inspect per-command errors: partial failure is possible (e.g. a cluster
	// node is down). Report the first error for the caller to log.
	var firstErr error
	for i, cmd := range cmds {
		if cmdErr := cmd.Err(); cmdErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("correct dimension %s: %w", targets[i].dim, cmdErr)
			}
		}
	}
	return firstErr
}

// GetAvailable returns available quota for a dimension/metric.
func (r *RedisRateLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
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

		levelCfg := r.configs.GetLevelConfig(d.dim, d.key)
		if levelCfg == nil {
			continue
		}

		if metric == MetricConcurrency {
			redisKey := fmt.Sprintf("rl:%s:{%s}:concurrency", d.dim, d.key)
			current, err := r.client.Get(ctx, redisKey).Int64()
			if err == redis.Nil {
				current = 0
			} else if err != nil {
				continue
			}

			available := int64(levelCfg.Concurrency) - current
			if minAvailable == -1 || available < minAvailable {
				minAvailable = available
			}
			continue
		}

		if isQuotaMetric(metric) {
			limit := quotaLimit(levelCfg, metric)
			if limit <= 0 {
				continue // unlimited, no constraint
			}
			redisKey := fmt.Sprintf("rl:%s:{%s}:%s", d.dim, d.key, metric)
			used, err := r.client.Get(ctx, redisKey).Int64()
			if err == redis.Nil {
				used = 0
			} else if err != nil {
				continue
			}
			available := limit - used
			if available < 0 {
				available = 0
			}
			if minAvailable == -1 || available < minAvailable {
				minAvailable = available
			}
			continue
		}

		// For RPM/TPM, read from rate bucket
		redisKey := fmt.Sprintf("rl:%s:{%s}:%s", d.dim, d.key, metric)
		available, err := r.client.HGet(ctx, redisKey, "available").Float64()
		if err == redis.Nil {
			// Key doesn't exist, full capacity available
			var limit int64
			switch metric {
			case MetricRPM:
				limit = int64(levelCfg.RPM)
			case MetricTPMInput:
				limit = levelCfg.TPMInput
			case MetricTPMOutput:
				limit = levelCfg.TPMOutput
			default:
				continue
			}
			available = float64(limit)
		} else if err != nil {
			continue
		}

		if minAvailable == -1 || int64(available) < minAvailable {
			minAvailable = int64(available)
		}
	}

	if minAvailable == -1 {
		return 0, nil
	}

	return minAvailable, nil
}

// Reset clears rate limit data for the given keys.
func (r *RedisRateLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	dimensions := []struct {
		dim Dimension
		key string
	}{
		{DimensionGlobal, keys.Global},
		{DimensionTenant, keys.Tenant},
		{DimensionModel, keys.Model},
		{DimensionAPIKey, keys.APIKey},
	}

	metrics := []Metric{MetricRPM, MetricTPMInput, MetricTPMOutput, MetricConcurrency}

	for _, d := range dimensions {
		if d.key == "" {
			continue
		}

		for _, m := range metrics {
			redisKey := fmt.Sprintf("rl:%s:{%s}:%s", d.dim, d.key, m)
			r.client.Del(ctx, redisKey)
		}
	}

	return nil
}

// Close closes the Redis client.
func (r *RedisRateLimiter) Close() error {
	return r.client.Close()
}

// luaInt64 converts a Lua script return value to int64.
// Redis returns integral Lua numbers as integer replies (int64 in go-redis),
// fractional ones as bulk strings (Redis < 7) or doubles (Redis 7+). This
// handles all three forms robustly.
func luaInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return int64(f)
	default:
		return 0
	}
}
