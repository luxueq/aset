package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrQuotaExhausted reports that the central Redis store has no quota
// left to lease. It is distinct from a transport failure: exhaustion is
// a legitimate deny, while transport failures trigger the hybrid
// fallback policy.
var ErrQuotaExhausted = errors.New("central quota exhausted")

// LeaseManager implements the token-lease sub-mode (plan 01-04 §模式 3-B):
// each instance periodically leases a batch of quota from the central
// Redis store and serves it from a local bucket. Redis calls drop from
// one-per-request to one-per-batch; oversell is bounded by
// instances × lease batch size.
//
// A lease is tracked per (dimension, key, metric) as a local RateBucket
// with capacity = LeaseBatchSize (scaled for TPM refill). When the local
// bucket runs dry, Refill acquires the next batch from Redis with a
// single atomic Check(cost = batch) call against the central limiter.
// Usage that the central limiter denied is corrected via Correct (e.g.
// pre-deducted max_tokens vs actual streamed tokens).
type LeaseManager struct {
	redis   RateLimiter
	cfg     *HybridConfig
	levels  *LevelConfigs
	buckets map[string]*leaseBucket
	index   map[string]leaseIndexEntry // composite key → (dim, key, metric)

	stopCh  chan struct{}
	stopped sync.Once
	mu      sync.RWMutex
}

// leaseIndexEntry maps a composite lease key back to its parts for the
// background refill loop.
type leaseIndexEntry struct {
	dim    Dimension
	key    string
	metric Metric
}

// leaseBucket wraps a RateBucket with lease-specific accounting.
type leaseBucket struct {
	*RateBucket
	leased int64 // total units leased from Redis
	used   int64 // units consumed locally
	// terminal marks a bucket whose central quota is exhausted: further
	// refill attempts are pointless until the next window.
	terminal bool
}

// NewLeaseManager creates a lease manager.
func NewLeaseManager(redis RateLimiter, cfg *HybridConfig, levels *LevelConfigs) *LeaseManager {
	if cfg == nil {
		cfg = &HybridConfig{}
	}
	if cfg.LeaseBatchSize <= 0 {
		cfg.LeaseBatchSize = 1000
	}
	if cfg.LeaseInterval <= 0 {
		cfg.LeaseInterval = time.Second
	}
	return &LeaseManager{
		redis:   redis,
		cfg:     cfg,
		levels:  levels,
		buckets: make(map[string]*leaseBucket),
		index:   make(map[string]leaseIndexEntry),
		stopCh:  make(chan struct{}),
	}
}

// Run periodically refills all active lease buckets. Starts with an
// immediate round so cold buckets fill before the first requests arrive.
func (lm *LeaseManager) Run(ctx context.Context) {
	lm.refillAll(ctx)

	ticker := time.NewTicker(lm.cfg.LeaseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-lm.stopCh:
			return
		case <-ticker.C:
			lm.refillAll(ctx)
		}
	}
}

// Stop stops the background refill loop.
func (lm *LeaseManager) Stop() {
	lm.stopped.Do(func() { close(lm.stopCh) })
}

// Check consumes cost from the local lease bucket.
func (lm *LeaseManager) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) *LimitResult {
	for _, d := range allDimensions() {
		key := dimensionKeyOf(keys, d)
		if key == "" {
			continue
		}
		levelCfg := lm.levels.GetLevelConfig(d, key)
		if levelCfg == nil {
			continue
		}

		bucket := lm.getOrCreateBucket(d, key, metric, levelCfg)
		if bucket == nil {
			continue
		}

		bucket.mu.Lock()
		terminal := bucket.terminal
		bucket.mu.Unlock()
		if !terminal && !bucket.Allow(float64(cost)) {
			// Local lease exhausted; report the dimension that blocked.
			return &LimitResult{
				Allowed:   false,
				Dimension: d,
				Metric:    metric,
				Limit:     bucket.leased,
				Remaining: 0,
			}
		}
		if terminal {
			// Central quota is exhausted for this window: deny locally
			// without hammering Redis with refill attempts.
			return &LimitResult{
				Allowed:   false,
				Dimension: d,
				Metric:    metric,
				Limit:     bucket.leased,
				Remaining: 0,
			}
		}
		bucket.mu.Lock()
		bucket.used += cost
		bucket.mu.Unlock()
	}

	return &LimitResult{Allowed: true}
}

// Refill acquires a new lease batch from Redis for the exhausted buckets.
// Returns ErrQuotaExhausted (wrapped) when the central store has no quota
// left to lease; transport errors are returned verbatim so the hybrid
// fallback policy can take over. Buckets marked terminal (previously
// exhausted) are skipped until a new window resets them.
func (lm *LeaseManager) Refill(ctx context.Context, keys DimensionKeys, metric Metric) error {
	for _, d := range allDimensions() {
		key := dimensionKeyOf(keys, d)
		if key == "" {
			continue
		}
		levelCfg := lm.levels.GetLevelConfig(d, key)
		if levelCfg == nil {
			continue
		}

		bucket := lm.getOrCreateBucket(d, key, metric, levelCfg)
		if bucket == nil {
			continue
		}

		// Only refill buckets that are running dry (< one batch left).
		if bucket.Available() >= float64(lm.cfg.LeaseBatchSize) {
			continue
		}

		res, err := lm.redis.Check(ctx, keys, metric, lm.cfg.LeaseBatchSize)
		if err != nil {
			return fmt.Errorf("lease refill for %s %s %s: %w", d, key, metric, err)
		}
		if !res.Allowed {
			// Try a smaller lease: whatever the central store says is left.
			if res.Remaining > 0 {
				small, err := lm.redis.Check(ctx, keys, metric, res.Remaining)
				if err != nil || !small.Allowed {
					bucket.mu.Lock()
					bucket.terminal = true
					bucket.mu.Unlock()
					return fmt.Errorf("%w for %s %s %s", ErrQuotaExhausted, d, key, metric)
				}
				lm.addLease(bucket, res.Remaining)
			} else {
				bucket.mu.Lock()
				bucket.terminal = true
				bucket.mu.Unlock()
				return fmt.Errorf("%w for %s %s %s", ErrQuotaExhausted, d, key, metric)
			}
			continue
		}
		lm.addLease(bucket, lm.cfg.LeaseBatchSize)
	}
	return nil
}

// refillAll refills every tracked bucket (used by Run).
func (lm *LeaseManager) refillAll(ctx context.Context) {
	lm.mu.RLock()
	snapshot := make([]struct {
		entry  leaseIndexEntry
		bucket *leaseBucket
	}, 0, len(lm.buckets))
	for k, b := range lm.buckets {
		if e, ok := lm.index[k]; ok {
			snapshot = append(snapshot, struct {
				entry  leaseIndexEntry
				bucket *leaseBucket
			}{entry: e, bucket: b})
		}
	}
	lm.mu.RUnlock()

	for _, ref := range snapshot {
		if ref.bucket.Available() >= float64(lm.cfg.LeaseBatchSize) {
			continue
		}
		keys := keysFromDimension(ref.entry.dim, ref.entry.key)
		if _, err := lm.redis.Check(ctx, keys, ref.entry.metric, lm.cfg.LeaseBatchSize); err != nil {
			continue // breaker in the hybrid limiter handles failure reporting
		}
		lm.addLease(ref.bucket, lm.cfg.LeaseBatchSize)
	}
}

// keysFromDimension builds a single-dimension DimensionKeys for lease
// refills. The central limiter checks all non-empty dimensions, so lease
// refills are requested with only the owning dimension set.
func keysFromDimension(dim Dimension, key string) DimensionKeys {
	switch dim {
	case DimensionGlobal:
		return DimensionKeys{Global: key}
	case DimensionTenant:
		return DimensionKeys{Tenant: key}
	case DimensionModel:
		return DimensionKeys{Model: key}
	case DimensionAPIKey:
		return DimensionKeys{APIKey: key}
	default:
		return DimensionKeys{}
	}
}

// Correct adjusts lease bookkeeping after the request's actual usage is
// known (pre-deducted max_tokens vs actual streamed tokens).
func (lm *LeaseManager) Correct(keys DimensionKeys, metric Metric, preDeducted, actual int64) {
	delta := actual - preDeducted
	if delta == 0 {
		return
	}

	for _, d := range allDimensions() {
		key := dimensionKeyOf(keys, d)
		if key == "" {
			continue
		}
		lm.mu.Lock()
		bucket, ok := lm.buckets[leaseBucketKey(d, key, metric)]
		lm.mu.Unlock()
		if !ok {
			continue
		}
		bucket.mu.Lock()
		if delta > 0 {
			bucket.used += delta
		} else {
			bucket.used += delta
			if bucket.used < 0 {
				bucket.used = 0
			}
		}
		bucket.mu.Unlock()
		// Return unused pre-deduction to the local bucket so it can serve
		// other requests without a Redis round-trip.
		if delta < 0 {
			bucket.RefillUnits(float64(-delta))
		}
	}
}

// Reset clears all lease state.
func (lm *LeaseManager) Reset(keys DimensionKeys) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.buckets = make(map[string]*leaseBucket)
	lm.index = make(map[string]leaseIndexEntry)
}

// getOrCreateBucket returns (creating on first use) the local lease
// bucket for a dimension/metric. Returns nil for metrics that are not
// lease-managed (quota/concurrency).
func (lm *LeaseManager) getOrCreateBucket(d Dimension, key string, metric Metric, levelCfg *LevelConfig) *leaseBucket {
	switch metric {
	case MetricRPM, MetricTPMInput, MetricTPMOutput:
	default:
		return nil
	}

	lk := leaseBucketKey(d, key, metric)

	lm.mu.RLock()
	bucket, ok := lm.buckets[lk]
	lm.mu.RUnlock()
	if ok {
		return bucket
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()
	if bucket, ok = lm.buckets[lk]; ok {
		return bucket
	}
	lm.index[lk] = leaseIndexEntry{dim: d, key: key, metric: metric}

	var refillRate float64
	var capacity float64
	switch metric {
	case MetricRPM:
		refillRate = float64(levelCfg.RPM) / 60.0
		capacity = float64(lm.cfg.LeaseBatchSize)
	case MetricTPMInput:
		refillRate = float64(levelCfg.TPMInput) / 60.0
		capacity = float64(lm.cfg.LeaseBatchSize)
	case MetricTPMOutput:
		refillRate = float64(levelCfg.TPMOutput) / 60.0
		capacity = float64(lm.cfg.LeaseBatchSize)
	}

	bucket = &leaseBucket{
		// Created empty: the lease must be acquired from Redis before any
		// units can be served (via Run's immediate refillAll or the first
		// Refill call). Seeding capacity here would grant phantom quota
		// that was never leased from the central store.
		RateBucket: NewRateBucketEmpty(refillRate, capacity),
		leased:     0,
	}
	lm.buckets[lk] = bucket
	return bucket
}

// leaseBucketKey builds the composite map key for a lease bucket.
// The (dim, key, metric) triple is stored verbatim to avoid the parsing
// ambiguity of ':' inside metric names and tenant keys.
func leaseBucketKey(d Dimension, key string, metric Metric) string {
	return fmt.Sprintf("%s\x00%s\x00%s", d, key, metric)
}

// addLease credits a freshly leased batch to a local bucket.
func (lm *LeaseManager) addLease(bucket *leaseBucket, amount int64) {
	bucket.mu.Lock()
	bucket.leased += amount
	bucket.mu.Unlock()
	bucket.RefillUnits(float64(amount))
}
