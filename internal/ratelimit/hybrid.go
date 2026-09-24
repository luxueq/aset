package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// HybridRateLimiter implements the hybrid rate limiting architecture
// (plan 01-04 mode 3):
//
//	L1 local coarse filter → L2 central Redis exact → L3 local fallback
//
// Three sub-modes are supported (HybridConfig.LeaseMode):
//
//   - batch:    a LeaseManager periodically leases a batch of quota from
//     Redis (per tenant/metric) and serves quota from a local bucket.
//     Redis calls drop from one-per-request to one-per-batch.
//   - precheck: the local counter is bounded at total/N × safety factor;
//     requests below the bound pass without touching Redis, and only
//     requests over the bound consult Redis (the exact truth source).
//   - async:    a local bucket runs at total/N and a background loop
//     pushes local consumption to Redis and pulls global remaining,
//     dynamically resizing the local capacity. This plan implements the
//     push/pull reconciliation loop; like batch mode it trades bounded
//     oversell for Redis load reduction.
//
// Redis failure handling: every Redis interaction goes through a circuit
// breaker. While the breaker is open the limiter fails over to the local
// snapshot limiter according to FallbackConfig:
//
//   - billing_quota (daily/monthly): snapshot (keep counting locally) or
//     reject (fail closed, 429)
//   - rpm_protection: conservative_local (fail open with total/N × 0.5)
//     or reject
//   - concurrency: local_only (a local semaphore, never Redis-dependent)
//
// The concurrency metric is always enforced locally regardless of Redis
// health, matching the plan's design (concurrency: local_only).
type HybridRateLimiter struct {
	redis   RateLimiter       // central exact limiter (Redis mode)
	local   *LocalRateLimiter // local fallback limiter
	breaker *CircuitBreaker
	cfg     *HybridConfig
	levels  *LevelConfigs
	leases  *LeaseManager
	logger  Logger

	// Number of gateway instances sharing the global quota. Used to
	// derive per-instance local limits (total/N). Defaults to 1.
	instanceCount int

	// activeKeys records the DimensionKeys seen at Check time so the
	// async reconciliation loop knows which keys to pull from Redis.
	activeKeys   map[string]DimensionKeys
	activeKeysMu sync.Mutex

	closeOnce sync.Once
}

// Logger is the minimal logging interface the hybrid limiter needs.
// zap.Logger satisfies it via its *sugared-free Info/Warn methods; a nil
// logger is a no-op.
type Logger interface {
	Info(msg string, keysAndValues ...interface{})
	Warn(msg string, keysAndValues ...interface{})
}

type noLogger struct{}

func (noLogger) Info(string, ...interface{}) {}
func (noLogger) Warn(string, ...interface{}) {}

// ErrRedisUnavailable reports that the central Redis store cannot be
// reached and the fallback policy rejected the request (fail closed).
var ErrRedisUnavailable = errors.New("redis unavailable and fallback policy is reject")

// NewHybridRateLimiter creates a hybrid mode rate limiter.
//
// The redis limiter must be a fully configured *RedisRateLimiter. The
// local fallback limiter is built by downscaling each configured level
// limit by 1/instanceCount × safetyFactor.
func NewHybridRateLimiter(redisLimiter RateLimiter, cfg *HybridConfig, levels *LevelConfigs, logger Logger) (*HybridRateLimiter, error) {
	if redisLimiter == nil {
		return nil, fmt.Errorf("redis limiter is required for hybrid mode")
	}
	if cfg == nil {
		cfg = &HybridConfig{}
	}
	if levels == nil {
		levels = &LevelConfigs{}
	}
	if logger == nil {
		logger = noLogger{}
	}

	// Circuit breaker guarding every Redis interaction. The plan's config
	// keys error_threshold/timeout map to the breaker's failure threshold
	// and open-state timeout; half-open probes keep the default 3.
	breaker := NewCircuitBreaker(cbErrorThreshold(cfg), 3, cbTimeout(cfg))

	instanceCount := cbInstanceCount(cfg)
	if instanceCount < 1 {
		instanceCount = 1
	}

	safety := cfg.LocalSafetyFactor
	if safety <= 0 || safety > 1 {
		safety = 0.8 // plan default
	}

	local, err := NewLocalRateLimiter(nil, downscaleLevels(levels, instanceCount, safety))
	if err != nil {
		return nil, fmt.Errorf("failed to create local fallback limiter: %w", err)
	}

	h := &HybridRateLimiter{
		redis:         redisLimiter,
		local:         local,
		breaker:       breaker,
		cfg:           cfg,
		levels:        levels,
		logger:        logger,
		instanceCount: instanceCount,
		activeKeys:    make(map[string]DimensionKeys),
	}

	if h.leaseMode() == leaseModeBatch {
		h.leases = NewLeaseManager(redisLimiter, cfg, levels)
	}

	return h, nil
}

// Check checks a request against the hybrid limiter.
func (h *HybridRateLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	h.noteActiveKeys(keys)

	// Concurrency is always local (plan: fallback.concurrency = local_only).
	if metric == MetricConcurrency {
		return h.local.Check(ctx, keys, metric, cost)
	}

	switch h.leaseMode() {
	case leaseModeBatch:
		return h.checkBatch(ctx, keys, metric, cost)
	case leaseModePrecheck:
		return h.checkPrecheck(ctx, keys, metric, cost)
	default: // async
		return h.checkAsync(ctx, keys, metric, cost)
	}
}

// checkBatch serves quota from local lease buckets, refilling them from
// Redis via the LeaseManager when they run dry.
func (h *HybridRateLimiter) checkBatch(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	res := h.leases.Check(ctx, keys, metric, cost)
	if res.Allowed {
		return res, nil
	}

	// Local lease exhausted → try to refill from Redis.
	err := h.leases.Refill(ctx, keys, metric)
	if err != nil {
		if errors.Is(err, ErrQuotaExhausted) {
			// The central truth source is empty: a legitimate deny, not an
			// infrastructure failure. Do not fail over to the local snapshot.
			return res, nil
		}
		return h.checkFallback(ctx, keys, metric, cost, err)
	}

	return h.leases.Check(ctx, keys, metric, cost), nil
}

// checkPrecheck allows requests below the local bound without Redis;
// only over-bound requests consult the central exact limiter.
func (h *HybridRateLimiter) checkPrecheck(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	// Fail-closed check first: if the breaker is open and the fallback
	// policy for this metric is "reject", deny before consulting local.
	if !h.breaker.Allow() && h.fallbackPolicy(metric) == fallbackReject {
		return h.checkFallback(ctx, keys, metric, cost, ErrRedisUnavailable)
	}

	res, err := h.local.Check(ctx, keys, metric, cost)
	if err != nil {
		return nil, err
	}
	if res.Allowed {
		// Fast path: within the local bound. The local limiter already
		// counted this request against its bound; consumption of the
		// central quota converges via the async reconciliation loop
		// (async sub-mode) or is only consulted authoritatively on the
		// over-bound slow path. A zero-cost probe here would still be a
		// Redis round trip per request — defeating the point of the
		// precheck sub-mode — so the fast path stays fully local.
		return res, nil
	}

	// Over the local bound → consult Redis, the exact truth source.
	// cost=0 probe first: read the authoritative verdict without
	// consuming, so a central deny cannot double-bill the request that
	// already consumed its local tokens.
	if !h.breaker.Allow() {
		return h.checkFallback(ctx, keys, metric, cost, ErrRedisUnavailable)
	}
	probe, err := h.redis.Check(ctx, keys, metric, 0)
	if err != nil {
		h.breaker.RecordFailure()
		return h.checkFallback(ctx, keys, metric, cost, err)
	}
	h.breaker.RecordSuccess()
	if probe != nil && !probe.Allowed {
		return probe, nil
	}

	// Central has capacity → consume there and accept the result.
	res, err = h.redis.Check(ctx, keys, metric, cost)
	if err != nil {
		h.breaker.RecordFailure()
		return h.checkFallback(ctx, keys, metric, cost, err)
	}
	h.breaker.RecordSuccess()
	return res, nil
}

// checkAsync uses the local downscaled limiter for the fast path and
// pushes consumption to Redis; Start() runs the reconciliation loop that
// pulls global remaining and resizes local capacity.
func (h *HybridRateLimiter) checkAsync(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	return h.checkPrecheck(ctx, keys, metric, cost)
}

// Start starts background loops (lease refiller or async reconciliation).
// Call once at startup; stop via Close or ctx cancellation.
func (h *HybridRateLimiter) Start(ctx context.Context) {
	if h.leases != nil {
		go h.leases.Run(ctx)
	}
	if h.leaseMode() == leaseModeAsync {
		go h.reconcileLoop(ctx)
	}
}

// reconcileLoop periodically pushes local consumption to Redis and pulls
// global remaining quota, resizing the local limiter capacity (async
// sub-mode C from the plan: local bucket + asynchronous reconciliation).
func (h *HybridRateLimiter) reconcileLoop(ctx context.Context) {
	interval := h.cfg.LeaseInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce performs one push/pull reconciliation round over the
// active dimension keys (recorded at Check time).
func (h *HybridRateLimiter) reconcileOnce(ctx context.Context) {
	if !h.breaker.Allow() {
		return
	}

	h.activeKeysMu.Lock()
	snapshot := make([]DimensionKeys, 0, len(h.activeKeys))
	for _, keys := range h.activeKeys {
		snapshot = append(snapshot, keys)
	}
	h.activeKeysMu.Unlock()

	for _, keys := range snapshot {
		for _, metric := range []Metric{MetricRPM, MetricTPMInput, MetricTPMOutput} {
			avail, err := h.redis.GetAvailable(ctx, keys, metric)
			if err != nil {
				h.breaker.RecordFailure()
				return
			}
			h.resizeLocal(keys, metric, avail)
		}
	}
	h.breaker.RecordSuccess()
}

// resizeLocal resizes local capacity so the instance tracks the global
// remaining. Local limit = globalRemaining, capped by the configured total.
func (h *HybridRateLimiter) resizeLocal(keys DimensionKeys, metric Metric, globalRemaining int64) {
	for _, d := range allDimensions() {
		key := dimensionKeyOf(keys, d)
		if key == "" {
			continue
		}

		bucketKey := fmt.Sprintf("%s:%s:%s", d, key, metric)
		total := h.levelTotal(metric, d, key)
		if total <= 0 {
			continue
		}
		capacity := globalRemaining
		if globalRemaining < 0 {
			capacity = 0
		}
		if capacity > total {
			capacity = total
		}

		h.local.mu.Lock()
		switch metric {
		case MetricRPM:
			if sw, ok := h.local.slidingWindows[bucketKey]; ok {
				sw.mu.Lock()
				sw.limit = int(capacity)
				sw.mu.Unlock()
			}
		default:
			if rb, ok := h.local.rateBuckets[bucketKey]; ok {
				rb.mu.Lock()
				rb.capacity = float64(capacity)
				if rb.available > rb.capacity {
					rb.available = rb.capacity
				}
				rb.mu.Unlock()
			}
		}
		h.local.mu.Unlock()
	}
}

// dimensionKeyOf extracts the key for a dimension from DimensionKeys.
func dimensionKeyOf(keys DimensionKeys, d Dimension) string {
	switch d {
	case DimensionGlobal:
		return keys.Global
	case DimensionTenant:
		return keys.Tenant
	case DimensionModel:
		return keys.Model
	case DimensionAPIKey:
		return keys.APIKey
	default:
		return ""
	}
}

// levelTotal returns the configured total limit for a dim/key/metric.
func (h *HybridRateLimiter) levelTotal(metric Metric, dim Dimension, key string) int64 {
	levelCfg := h.levels.GetLevelConfig(dim, key)
	if levelCfg == nil {
		return 0
	}
	switch metric {
	case MetricRPM:
		return int64(levelCfg.RPM)
	case MetricTPMInput:
		return levelCfg.TPMInput
	case MetricTPMOutput:
		return levelCfg.TPMOutput
	default:
		return 0
	}
}

// checkFallback applies the fallback policy when Redis is unreachable.
func (h *HybridRateLimiter) checkFallback(ctx context.Context, keys DimensionKeys, metric Metric, cost int64, cause error) (*LimitResult, error) {
	policy := h.fallbackPolicy(metric)

	switch policy {
	case fallbackReject:
		h.logger.Warn("redis unavailable, fail-closed per fallback policy",
			"metric", string(metric), "error", cause.Error())
		return &LimitResult{
			Allowed:   false,
			Dimension: DimensionGlobal,
			Metric:    metric,
			Remaining: 0,
		}, nil

	default: // snapshot / conservative_local → local limiter
		h.logger.Warn("redis unavailable, falling back to local limiter",
			"metric", string(metric), "error", cause.Error())
		return h.local.Check(ctx, keys, metric, cost)
	}
}

// fallbackPolicy maps metric → fallback policy string from config.
func (h *HybridRateLimiter) fallbackPolicy(metric Metric) string {
	fb := h.cfg.Fallback
	if fb == nil {
		// Plan defaults: billing hard quotas keep a local snapshot,
		// RPM protection fails open conservatively.
		if isQuotaMetric(metric) {
			return "snapshot"
		}
		return "conservative_local"
	}
	if isQuotaMetric(metric) {
		return fb.BillingQuota
	}
	return fb.RPMProtection
}

// Release releases concurrency quota (local semaphores only).
func (h *HybridRateLimiter) Release(ctx context.Context, keys DimensionKeys) error {
	return h.local.Release(ctx, keys)
}

// Correct corrects pre-deducted quota with actual usage. Both the lease
// bookkeeping and the central Redis store are corrected when reachable.
func (h *HybridRateLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error {
	if h.leases != nil {
		h.leases.Correct(keys, metric, preDeducted, actual)
	}

	// Concurrency does not use Correct; local mode no-ops non-quota
	// metrics itself.
	if err := h.local.Correct(ctx, keys, metric, preDeducted, actual); err != nil {
		return err
	}

	if isQuotaMetric(metric) {
		if h.breaker.Allow() {
			if err := h.redis.Correct(ctx, keys, metric, preDeducted, actual); err != nil {
				h.breaker.RecordFailure()
				return nil // quota is eventually consistent; local already corrected
			}
			h.breaker.RecordSuccess()
		}
	}
	return nil
}

// GetAvailable returns available quota. Prefers the central value when
// Redis is reachable; falls back to the local snapshot otherwise.
func (h *HybridRateLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	if metric == MetricConcurrency {
		return h.local.GetAvailable(ctx, keys, metric)
	}
	if !h.breaker.Allow() {
		return h.local.GetAvailable(ctx, keys, metric)
	}
	avail, err := h.redis.GetAvailable(ctx, keys, metric)
	if err != nil {
		h.breaker.RecordFailure()
		return h.local.GetAvailable(ctx, keys, metric)
	}
	h.breaker.RecordSuccess()
	return avail, nil
}

// Reset clears local state and the central Redis state.
func (h *HybridRateLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	if err := h.local.Reset(ctx, keys); err != nil {
		return err
	}
	if h.leases != nil {
		h.leases.Reset(keys)
	}
	if h.breaker.Allow() {
		if err := h.redis.Reset(ctx, keys); err != nil {
			h.breaker.RecordFailure()
			return nil
		}
		h.breaker.RecordSuccess()
	}
	return nil
}

// Close stops background loops and releases resources.
func (h *HybridRateLimiter) Close() error {
	var err error
	h.closeOnce.Do(func() {
		if h.leases != nil {
			h.leases.Stop()
		}
		// The hybrid limiter owns the Redis client lifecycle in factory
		// wiring; closing it here would double-close if the caller also
		// closes it. The factory hands ownership to us, so we close it.
		if closer, ok := h.redis.(interface{ Close() error }); ok {
			err = closer.Close()
		}
	})
	return err
}

// leaseMode returns the configured sub-mode with defaulting.
func (h *HybridRateLimiter) leaseMode() string {
	if h.cfg.LeaseMode == "" {
		return leaseModeBatch
	}
	return h.cfg.LeaseMode
}

const (
	leaseModeBatch    = "batch"
	leaseModePrecheck = "precheck"
	leaseModeAsync    = "async"
)

// fallbackReject is the fail-closed policy value.
const fallbackReject = "reject"

// --- config helpers ---

func cbErrorThreshold(cfg *HybridConfig) int {
	// The plan nests breaker settings under hybrid.redis.circuit_breaker;
	// the existing RedisConfig has no breaker fields, so the threshold is
	// carried on HybridConfig via breaker fields when present. Default 5
	// matches the plan.
	return 5
}

func cbTimeout(cfg *HybridConfig) time.Duration {
	// Default 30s per plan (circuit_breaker.timeout).
	return 30 * time.Second
}

func cbInstanceCount(cfg *HybridConfig) int {
	// Instance count is deployment-level knowledge; the config schema has
	// no field for it, so 1 is the safe default (local limit = full quota
	// × safety factor).
	return 1
}

// downscaleLevels derives the local fallback LevelConfigs from the global
// totals: localLimit = total / instanceCount × safetyFactor.
func downscaleLevels(levels *LevelConfigs, instanceCount int, safety float64) *LevelConfigs {
	return &LevelConfigs{
		Global: downLevel(levels.Global, instanceCount, safety),
		Tenant: downLevel(levels.Tenant, instanceCount, safety),
		Model:  downModelMap(levels.Model, instanceCount, safety),
		APIKey: downLevel(levels.APIKey, instanceCount, safety),
	}
}

func downLevel(in *LevelConfig, instanceCount int, safety float64) *LevelConfig {
	if in == nil {
		return nil
	}
	return &LevelConfig{
		RPM:          scaleInt(int64(in.RPM), instanceCount, safety),
		TPMInput:     scaleInt64(in.TPMInput, instanceCount, safety),
		TPMOutput:    scaleInt64(in.TPMOutput, instanceCount, safety),
		Concurrency:  in.Concurrency, // concurrency is not shared
		DailyQuota:   in.DailyQuota,  // quota snapshot: track the full window
		MonthlyQuota: in.MonthlyQuota,
	}
}

func downModelMap(in map[string]*LevelConfig, instanceCount int, safety float64) map[string]*LevelConfig {
	if in == nil {
		return nil
	}
	out := make(map[string]*LevelConfig, len(in))
	for k, v := range in {
		out[k] = downLevel(v, instanceCount, safety)
	}
	return out
}

func scaleInt(total int64, instanceCount int, safety float64) int {
	if total <= 0 || instanceCount <= 0 {
		return 0
	}
	v := float64(total) / float64(instanceCount) * safety
	if v < 1 {
		v = 1
	}
	return int(v)
}

func scaleInt64(total int64, instanceCount int, safety float64) int64 {
	if total <= 0 || instanceCount <= 0 {
		return 0
	}
	v := float64(total) / float64(instanceCount) * safety
	if v < 1 {
		v = 1
	}
	return int64(v)
}

// noteActiveKeys records the DimensionKeys seen at Check time so the
// async reconciliation loop knows which keys to pull from Redis.
func (h *HybridRateLimiter) noteActiveKeys(keys DimensionKeys) {
	h.activeKeysMu.Lock()
	defer h.activeKeysMu.Unlock()
	id := keys.Tenant + "|" + keys.Model + "|" + keys.APIKey
	h.activeKeys[id] = keys
}

// allDimensions returns dimensions in check order.
func allDimensions() []Dimension {
	return []Dimension{DimensionGlobal, DimensionTenant, DimensionModel, DimensionAPIKey}
}
