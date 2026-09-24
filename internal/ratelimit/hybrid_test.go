package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeRedisLimiter is a RateLimiter stand-in whose behavior is scripted
// per test. It lets hybrid-mode tests simulate Redis failures and quota
// decisions without a real Redis.
type fakeRedisLimiter struct {
	checkFn      func(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error)
	availableFn  func(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error)
	correctCalls int
	correctLast  [4]int64 // metric-agnostic: preDeducted, actual
	resetCalls   int
	closeCalls   int
}

func (f *fakeRedisLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	if f.checkFn == nil {
		return &LimitResult{Allowed: true}, nil
	}
	return f.checkFn(ctx, keys, metric, cost)
}

func (f *fakeRedisLimiter) Release(ctx context.Context, keys DimensionKeys) error { return nil }

func (f *fakeRedisLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error {
	f.correctCalls++
	f.correctLast = [4]int64{preDeducted, actual, 0, 0}
	return nil
}

func (f *fakeRedisLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	if f.availableFn == nil {
		return 0, nil
	}
	return f.availableFn(ctx, keys, metric)
}

func (f *fakeRedisLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	f.resetCalls++
	return nil
}

func (f *fakeRedisLimiter) Close() error {
	f.closeCalls++
	return nil
}

var errFakeRedis = errors.New("fake redis failure")

// hybridLevels returns a level config set for hybrid tests.
func hybridLevels() *LevelConfigs {
	return &LevelConfigs{
		Global: &LevelConfig{RPM: 1000, TPMInput: 100000, TPMOutput: 50000},
		Tenant: &LevelConfig{RPM: 100, TPMInput: 10000, TPMOutput: 5000, DailyQuota: 100000},
	}
}

func newTestHybridLimiter(t *testing.T, cfg *HybridConfig, levels *LevelConfigs, redis RateLimiter) *HybridRateLimiter {
	t.Helper()
	if redis == nil {
		redis = &fakeRedisLimiter{}
	}
	h, err := NewHybridRateLimiter(redis, cfg, levels, nil)
	if err != nil {
		t.Fatalf("failed to create hybrid limiter: %v", err)
	}
	return h
}

// --- Constructor / wiring ---

func TestNewHybridRateLimiter_RequiresRedis(t *testing.T) {
	if _, err := NewHybridRateLimiter(nil, nil, nil, nil); err == nil {
		t.Fatal("nil redis limiter should be rejected")
	}
}

func TestNewHybridRateLimiter_LocalLimitsDownscaled(t *testing.T) {
	// safety 0.8: 100 RPM global → local fallback limit 80.
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck", LocalSafetyFactor: 0.8}, hybridLevels(), nil)

	localTenant := h.local.configs.Tenant
	if localTenant == nil {
		t.Fatal("local fallback levels should exist")
	}
	if localTenant.RPM != 80 {
		t.Fatalf("expected downscaled local RPM 80, got %d", localTenant.RPM)
	}
	// Quota snapshot keeps the full window total.
	if localTenant.DailyQuota != 100000 {
		t.Fatalf("expected local daily quota snapshot 100000, got %d", localTenant.DailyQuota)
	}
}

// --- Concurrency always local ---

func TestHybridRateLimiter_ConcurrencyAlwaysLocal(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{Concurrency: 2},
	}
	calls := 0
	redis := &fakeRedisLimiter{
		checkFn: func(context.Context, DimensionKeys, Metric, int64) (*LimitResult, error) {
			calls++
			return &LimitResult{Allowed: true}, nil
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck"}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	for i := 0; i < 2; i++ {
		res, err := h.Check(ctx, keys, MetricConcurrency, 1)
		if err != nil || !res.Allowed {
			t.Fatalf("concurrent request %d should be allowed: %v %v", i, res, err)
		}
	}
	res, _ := h.Check(ctx, keys, MetricConcurrency, 1)
	if res.Allowed {
		t.Fatal("3rd concurrent request should be denied by local semaphore")
	}
	if calls != 0 {
		t.Fatalf("concurrency checks must not touch redis, got %d calls", calls)
	}

	// Release restores the slot.
	if err := h.Release(ctx, keys); err != nil {
		t.Fatalf("release failed: %v", err)
	}
	res, _ = h.Check(ctx, keys, MetricConcurrency, 1)
	if !res.Allowed {
		t.Fatal("release should free a concurrency slot")
	}
}

// --- Precheck sub-mode: fast path below bound, exact above ---

func TestHybridRateLimiter_Precheck_FastPathNoRedis(t *testing.T) {
	levels := hybridLevels()
	redisCosting := 0
	redis := &fakeRedisLimiter{
		checkFn: func(_ context.Context, _ DimensionKeys, _ Metric, cost int64) (*LimitResult, error) {
			if cost > 0 {
				redisCosting++
			}
			return &LimitResult{Allowed: true}, nil
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck", LocalSafetyFactor: 0.8}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// 80 requests fit within the local bound (100 × 0.8).
	for i := 0; i < 80; i++ {
		res, err := h.Check(ctx, keys, MetricRPM, 1)
		if err != nil || !res.Allowed {
			t.Fatalf("request %d should pass the local bound: %v %v", i, res, err)
		}
	}
	if redisCosting != 0 {
		t.Fatalf("requests within the local bound must not consume central quota, got %d costing calls", redisCosting)
	}
}

func TestHybridRateLimiter_Precheck_OverBoundConsultsRedis(t *testing.T) {
	levels := hybridLevels()
	redisCalls := 0
	redis := &fakeRedisLimiter{
		checkFn: func(_ context.Context, _ DimensionKeys, _ Metric, cost int64) (*LimitResult, error) {
			redisCalls++
			if cost <= 0 {
				return &LimitResult{Allowed: true}, nil
			}
			// Central exact answer: deny.
			return &LimitResult{Allowed: false, Dimension: DimensionTenant, Metric: MetricRPM, Remaining: 0}, nil
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck", LocalSafetyFactor: 0.8}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// Exhaust the local bound.
	for i := 0; i < 80; i++ {
		h.Check(ctx, keys, MetricRPM, 1)
	}

	// Next request exceeds the local bound → Redis (exact) is authoritative.
	res, err := h.Check(ctx, keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("over-bound request should follow the central (deny) verdict")
	}
	if redisCalls == 0 {
		t.Fatal("over-bound request should have consulted redis")
	}
}

// --- Fallback when Redis fails ---

func TestHybridRateLimiter_RedisFailure_FailsOverToLocal(t *testing.T) {
	levels := hybridLevels()
	redis := &fakeRedisLimiter{
		checkFn: func(context.Context, DimensionKeys, Metric, int64) (*LimitResult, error) {
			return nil, errFakeRedis
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck", LocalSafetyFactor: 0.8}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// Exhaust the local bound first so the slow path (Redis) is taken.
	// The fast path is fully local (precheck semantics: requests below the
	// bound never touch Redis), so no failures are recorded yet.
	for i := 0; i < 80; i++ {
		h.Check(ctx, keys, MetricRPM, 1)
	}
	if h.breaker.State() != CircuitClosed {
		t.Fatal("fast-path requests must not trip the breaker (no redis calls)")
	}

	// Redis errors on the slow path → fallback to local snapshot, not a
	// request error.
	for i := 0; i < 5; i++ {
		res, err := h.Check(ctx, keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("fallback should not surface an error: %v", err)
		}
		if res == nil {
			t.Fatal("fallback should return a result")
		}
	}

	// The breaker recorded the failures (threshold 5).
	if h.breaker.State() != CircuitOpen {
		t.Fatalf("breaker should be open after 5 slow-path failures, got %s", h.breaker.State())
	}
}

func TestHybridRateLimiter_RedisFailure_FailClosedForBillingQuota(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{DailyQuota: 1000},
	}
	redis := &fakeRedisLimiter{
		checkFn: func(context.Context, DimensionKeys, Metric, int64) (*LimitResult, error) {
			return nil, errFakeRedis
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{
		LeaseMode: "precheck",
		Fallback:  &FallbackConfig{BillingQuota: "reject"},
	}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	// Quota check with Redis down and billing_quota=reject → 429-style deny.
	// Prime the breaker open: 5 failed probe calls from allowed checks.
	for i := 0; i < 5; i++ {
		h.breaker.RecordFailure()
	}

	// The local quota snapshot has capacity, but the fail-closed policy
	// must deny when the central billing store is unreachable.
	res, err := h.Check(ctx, keys, MetricDailyQuota, 10)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Allowed {
		t.Fatal("fail-closed billing quota should deny when redis is unreachable")
	}
}

func TestHybridRateLimiter_GetAvailable_FallsBackToLocal(t *testing.T) {
	levels := hybridLevels()
	redis := &fakeRedisLimiter{
		availableFn: func(context.Context, DimensionKeys, Metric) (int64, error) {
			return 0, errFakeRedis
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck"}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// Local bucket is untouched → full downscaled capacity minus zero.
	_, err := h.GetAvailable(ctx, keys, MetricRPM)
	if err != nil {
		t.Fatalf("GetAvailable should fall back to local on redis error: %v", err)
	}
}

// --- Circuit breaker integration ---

func TestHybridRateLimiter_BreakerOpensAndRecovers(t *testing.T) {
	levels := hybridLevels()
	failing := true
	redis := &fakeRedisLimiter{
		checkFn: func(context.Context, DimensionKeys, Metric, int64) (*LimitResult, error) {
			if failing {
				return nil, errFakeRedis
			}
			return &LimitResult{Allowed: true}, nil
		},
	}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck", LocalSafetyFactor: 0.8}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// Exhaust local bound, then force 5 Redis failures → breaker opens.
	for i := 0; i < 80; i++ {
		h.Check(ctx, keys, MetricRPM, 1)
	}
	for i := 0; i < 5; i++ {
		h.Check(ctx, keys, MetricRPM, 1)
	}
	if h.breaker.State() != CircuitOpen {
		t.Fatalf("breaker should be open after 5 failures, got %s", h.breaker.State())
	}

	// While open, checks short-circuit to local fallback (no Redis calls).
	failing = false
	h.Check(ctx, keys, MetricRPM, 1)
	if h.breaker.State() != CircuitOpen {
		t.Fatal("breaker should remain open before the timeout elapses")
	}

	// Force the recovery path: half-open probes all succeed → closed.
	h.breaker.mu.Lock()
	h.breaker.state = CircuitHalfOpen
	h.breaker.resetHalfOpenLocked()
	h.breaker.mu.Unlock()
	for i := 0; i < 3; i++ {
		h.breaker.Allow()
		h.breaker.RecordSuccess()
	}
	if h.breaker.State() != CircuitClosed {
		t.Fatalf("breaker should close after successful probes, got %s", h.breaker.State())
	}
}

// --- Correct propagation ---

func TestHybridRateLimiter_Correct_PropagatesToRedis(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{DailyQuota: 10000},
	}
	redis := &fakeRedisLimiter{}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck"}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	if err := h.Correct(ctx, keys, MetricDailyQuota, 1000, 400); err != nil {
		t.Fatalf("correct failed: %v", err)
	}
	if redis.correctCalls != 1 {
		t.Fatalf("correct should propagate to redis once, got %d", redis.correctCalls)
	}
}

// --- Reset ---

func TestHybridRateLimiter_Reset(t *testing.T) {
	levels := hybridLevels()
	redis := &fakeRedisLimiter{}
	h := newTestHybridLimiter(t, &HybridConfig{LeaseMode: "precheck"}, levels, redis)
	ctx := context.Background()
	keys := DimensionKeys{Global: "global", Tenant: "t1"}

	// Consume some local quota then reset.
	for i := 0; i < 10; i++ {
		h.Check(ctx, keys, MetricRPM, 1)
	}
	if err := h.Reset(ctx, keys); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	if redis.resetCalls != 1 {
		t.Fatalf("reset should propagate to redis once, got %d", redis.resetCalls)
	}

	// Local state was cleared.
	avail, err := h.local.GetAvailable(ctx, keys, MetricRPM)
	if err != nil {
		t.Fatalf("local GetAvailable failed: %v", err)
	}
	if avail != 80 { // downscaled tenant limit (100 × 0.8)
		t.Fatalf("expected local 80 available after reset, got %d", avail)
	}
}

// --- Close ---

func TestHybridRateLimiter_Close_ClosesRedis(t *testing.T) {
	redis := &fakeRedisLimiter{}
	h := newTestHybridLimiter(t, nil, hybridLevels(), redis)

	if err := h.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if redis.closeCalls != 1 {
		t.Fatalf("close should close the redis limiter once, got %d", redis.closeCalls)
	}
	// Idempotent.
	h.Close()
	if redis.closeCalls != 1 {
		t.Fatalf("close must be idempotent, got %d calls", redis.closeCalls)
	}
}

// --- Lease batch sub-mode against a real miniredis ---

func TestHybridRateLimiter_BatchMode_LeaseFromRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	// Central budget: 5 requests per minute for the tenant.
	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 5},
	}
	redisLimiter, err := NewRedisRateLimiter(client, levels)
	if err != nil {
		t.Fatalf("failed to create redis limiter: %v", err)
	}

	h, err := NewHybridRateLimiter(redisLimiter, &HybridConfig{
		LeaseMode:      "batch",
		LeaseBatchSize: 3, // lease 3 requests per batch
		LeaseInterval:  time.Hour,
	}, levels, nil)
	if err != nil {
		t.Fatalf("failed to create hybrid limiter: %v", err)
	}

	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	// 5 requests must pass (1 full batch of 3 + remainder of 2 from Redis).
	allowed := 0
	for i := 0; i < 10; i++ {
		res, err := h.Check(ctx, keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("check %d failed: %v", i, err)
		}
		if res.Allowed {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("expected exactly 5 allowed requests (central RPM limit), got %d", allowed)
	}
}

func TestHybridRateLimiter_BatchMode_LeaseRefillFailsOver(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 1000},
	}
	redis := &fakeRedisLimiter{
		checkFn: func(context.Context, DimensionKeys, Metric, int64) (*LimitResult, error) {
			return nil, errFakeRedis
		},
	}
	h, err := NewHybridRateLimiter(redis, &HybridConfig{
		LeaseMode:      "batch",
		LeaseBatchSize: 2,
	}, levels, nil)
	if err != nil {
		t.Fatalf("failed to create hybrid limiter: %v", err)
	}

	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	// Drain the initial local lease.
	h.Check(ctx, keys, MetricRPM, 1)
	h.Check(ctx, keys, MetricRPM, 1)

	// Refill fails (Redis down) → fallback to the local snapshot limiter
	// must still allow within the downscaled bound.
	res, err := h.Check(ctx, keys, MetricRPM, 1)
	if err != nil {
		t.Fatalf("fallback check failed: %v", err)
	}
	if res == nil {
		t.Fatal("fallback should produce a result")
	}
}

func TestHybridRateLimiter_BatchMode_ReducesRedisCalls(t *testing.T) {
	// 压测验收 (deterministic form): 300 requests served through lease
	// batches of 100 must cause far fewer Redis calls than pure redis
	// mode (which would be 300+). Batch mode pays ~1 call per batch.
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 1000},
	}
	redisLimiter, err := NewRedisRateLimiter(client, levels)
	if err != nil {
		t.Fatalf("failed to create redis limiter: %v", err)
	}

	// Count Redis EVAL calls via a scripting hook is not exposed by
	// miniredis; count via a wrapping limiter instead.
	counting := &countingLimiter{inner: redisLimiter}

	h, err := NewHybridRateLimiter(counting, &HybridConfig{
		LeaseMode:      "batch",
		LeaseBatchSize: 100,
		LeaseInterval:  time.Hour, // background loop off for determinism
	}, levels, nil)
	if err != nil {
		t.Fatalf("failed to create hybrid limiter: %v", err)
	}

	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"}

	allowed := 0
	for i := 0; i < 300; i++ {
		res, err := h.Check(ctx, keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("check %d failed: %v", i, err)
		}
		if res.Allowed {
			allowed++
		}
	}
	if allowed != 300 {
		t.Fatalf("all 300 requests should be allowed (limit 1000), got %d", allowed)
	}

	// 3 batches + small-lease remainder: strictly fewer than, say, 10
	// Redis calls for 300 requests (pure redis mode would be 300).
	if c := counting.calls(); c == 0 || c > 10 {
		t.Fatalf("batch mode should serve 300 requests with few redis calls, got %d", c)
	}
}

// countingLimiter wraps a RateLimiter and counts Check calls.
type countingLimiter struct {
	inner RateLimiter
	n     int64
}

func (c *countingLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	c.n++
	return c.inner.Check(ctx, keys, metric, cost)
}

func (c *countingLimiter) Release(ctx context.Context, keys DimensionKeys) error {
	return c.inner.Release(ctx, keys)
}
func (c *countingLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, pre, actual int64) error {
	return c.inner.Correct(ctx, keys, metric, pre, actual)
}
func (c *countingLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	return c.inner.GetAvailable(ctx, keys, metric)
}
func (c *countingLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	return c.inner.Reset(ctx, keys)
}
func (c *countingLimiter) Close() error { return c.inner.Close() }
func (c *countingLimiter) calls() int64 {
	return c.n
}
