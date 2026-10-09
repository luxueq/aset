package ratelimit

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// recordingShardLimiter is a RateLimiter stand-in that records which methods
// were invoked, so sharded routing can be asserted deterministically.
type recordingShardLimiter struct {
	checkCalls   int
	correctCalls int
	resetCalls   int
	closeCalls   int
	lastKeys     DimensionKeys
	lastMetric   Metric
}

func (r *recordingShardLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	r.checkCalls++
	r.lastKeys = keys
	r.lastMetric = metric
	return &LimitResult{Allowed: true, Remaining: 1}, nil
}

func (r *recordingShardLimiter) Release(ctx context.Context, keys DimensionKeys) error { return nil }

func (r *recordingShardLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, pre, actual int64) error {
	r.correctCalls++
	r.lastKeys = keys
	return nil
}

func (r *recordingShardLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	return 1, nil
}

func (r *recordingShardLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	r.resetCalls++
	return nil
}

func (r *recordingShardLimiter) Close() error {
	r.closeCalls++
	return nil
}

// --- determineShard routing ---

func TestDetermineShard_RequestHash_Deterministic(t *testing.T) {
	for _, id := range []string{"r-1", "r-2", "abc", "xyz"} {
		a := determineShard(id, 16, "request_hash")
		b := determineShard(id, 16, "request_hash")
		if a != b {
			t.Fatalf("request_hash must be deterministic for %q: %d != %d", id, a, b)
		}
		if a < 0 || a >= 16 {
			t.Fatalf("shard index out of range: %d", a)
		}
	}
}

func TestDetermineShard_UnsupportedMethodsFallBackToRequestHash(t *testing.T) {
	// consistent_hash (ketama ring) is only meaningful for dynamic shard
	// membership; round_robin cannot reproduce the same shard at Correct time.
	// Both — and any unknown method — must resolve to request_hash.
	for _, id := range []string{"r-1", "r-2", "abc"} {
		want := determineShard(id, 8, "request_hash")
		for _, method := range []string{"consistent_hash", "round_robin", "bogus"} {
			if got := determineShard(id, 8, method); got != want {
				t.Fatalf("method %q should resolve to request_hash for %q: %d != %d", method, id, got, want)
			}
		}
	}
}

func TestDetermineShard_OneShardReturnsZero(t *testing.T) {
	for _, method := range []string{"request_hash", "consistent_hash", "round_robin"} {
		if got := determineShard("r-1", 1, method); got != 0 {
			t.Fatalf("single shard must always route to 0, got %d", got)
		}
	}
}

// --- ShardedRateLimiter routing ---

func newRecordingShards(n int) []RateLimiter {
	shards := make([]RateLimiter, n)
	for i := range shards {
		shards[i] = &recordingShardLimiter{}
	}
	return shards
}

func TestShardedRateLimiter_CheckAndCorrect_RouteToSameShard(t *testing.T) {
	shards := newRecordingShards(4)
	s, err := NewShardedRateLimiter(shards, &ShardedConfig{Sharding: &ShardingConfig{Method: "request_hash"}}, nil)
	if err != nil {
		t.Fatalf("failed to create sharded limiter: %v", err)
	}

	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1", RequestID: "req-abc"}

	if _, err := s.Check(ctx, keys, MetricRPM, 1); err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if err := s.Correct(ctx, keys, MetricTPMOutput, 100, 40); err != nil {
		t.Fatalf("correct failed: %v", err)
	}

	// Admission and correction for the same RequestID must land on the same
	// shard (the whole point of deterministic routing by RequestID).
	checkShard := -1
	correctShard := -1
	for i, sh := range shards {
		rec := sh.(*recordingShardLimiter)
		if rec.checkCalls > 0 {
			checkShard = i
		}
		if rec.correctCalls > 0 {
			correctShard = i
		}
	}
	if checkShard < 0 || correctShard < 0 {
		t.Fatal("check and correct should each hit exactly one shard")
	}
	if checkShard != correctShard {
		t.Fatalf("check and correct must route to the same shard, got %d and %d", checkShard, correctShard)
	}
}

func TestShardedRateLimiter_EmptyRequestID_Errors(t *testing.T) {
	shards := newRecordingShards(4)
	s, err := NewShardedRateLimiter(shards, &ShardedConfig{Sharding: &ShardingConfig{Method: "request_hash"}}, nil)
	if err != nil {
		t.Fatalf("failed to create sharded limiter: %v", err)
	}

	ctx := context.Background()
	keys := DimensionKeys{Tenant: "t1"} // no RequestID

	// A missing RequestID must fail fast rather than silently concentrate a
	// tenant's traffic onto one shard.
	if _, err := s.Check(ctx, keys, MetricRPM, 1); err == nil {
		t.Fatal("empty RequestID with multiple shards should return an error")
	}
	if err := s.Correct(ctx, keys, MetricTPMOutput, 100, 40); err == nil {
		t.Fatal("empty RequestID with multiple shards should return an error on Correct")
	}
	// No shard should have been touched.
	for i, sh := range shards {
		if rec := sh.(*recordingShardLimiter); rec.checkCalls != 0 || rec.correctCalls != 0 {
			t.Fatalf("shard %d should not be touched on a routing error", i)
		}
	}
}

func TestShardedRateLimiter_ResetAndClose_AllShards(t *testing.T) {
	shards := newRecordingShards(4)
	s, err := NewShardedRateLimiter(shards, nil, nil)
	if err != nil {
		t.Fatalf("failed to create sharded limiter: %v", err)
	}

	ctx := context.Background()
	if err := s.Reset(ctx, DimensionKeys{Tenant: "t1"}); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	for i, sh := range shards {
		if rec := sh.(*recordingShardLimiter); rec.resetCalls != 1 {
			t.Fatalf("shard %d should be reset once, got %d", i, rec.resetCalls)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	for i, sh := range shards {
		if rec := sh.(*recordingShardLimiter); rec.closeCalls != 1 {
			t.Fatalf("shard %d should be closed once, got %d", i, rec.closeCalls)
		}
	}
}

func TestNewShardedRateLimiter_RequiresShards(t *testing.T) {
	if _, err := NewShardedRateLimiter(nil, nil, nil); err == nil {
		t.Fatal("empty shards should be rejected")
	}
	if _, err := NewShardedRateLimiter([]RateLimiter{}, nil, nil); err == nil {
		t.Fatal("empty shards should be rejected")
	}
}

// --- shardLevels downscaling ---

func TestShardLevels_DownscalesEvenly(t *testing.T) {
	levels := &LevelConfigs{
		Global: &LevelConfig{RPM: 1000, TPMInput: 100000, Concurrency: 40, DailyQuota: 400},
		Tenant: &LevelConfig{RPM: 100, TPMOutput: 5000, Concurrency: 10},
	}
	out := shardLevels(levels, 4)

	if out.Global.RPM != 250 {
		t.Fatalf("global RPM should be 1000/4=250, got %d", out.Global.RPM)
	}
	if out.Global.TPMInput != 25000 {
		t.Fatalf("global TPMInput should be 100000/4=25000, got %d", out.Global.TPMInput)
	}
	if out.Global.Concurrency != 10 {
		t.Fatalf("global concurrency should be 40/4=10, got %d", out.Global.Concurrency)
	}
	if out.Global.DailyQuota != 100 {
		t.Fatalf("global daily quota should be 400/4=100, got %d", out.Global.DailyQuota)
	}
	if out.Tenant.RPM != 25 {
		t.Fatalf("tenant RPM should be 100/4=25, got %d", out.Tenant.RPM)
	}
	if out.Tenant.TPMOutput != 1250 {
		t.Fatalf("tenant TPMOutput should be 5000/4=1250, got %d", out.Tenant.TPMOutput)
	}
	if out.Tenant.Concurrency != 2 { // 10/4 = 2 (integer floor)
		t.Fatalf("tenant concurrency should be 10/4=2, got %d", out.Tenant.Concurrency)
	}
}

func TestShardLevels_MinOne(t *testing.T) {
	levels := &LevelConfigs{
		Tenant: &LevelConfig{RPM: 3, TPMInput: 2},
	}
	out := shardLevels(levels, 4)
	if out.Tenant.RPM != 1 {
		t.Fatalf("RPM 3 across 4 shards should floor to 1 (never 0/unlimited), got %d", out.Tenant.RPM)
	}
	if out.Tenant.TPMInput != 1 {
		t.Fatalf("TPMInput 2 across 4 shards should floor to 1, got %d", out.Tenant.TPMInput)
	}
}

func TestShardLevels_NilSafe(t *testing.T) {
	if shardLevels(nil, 4) != nil {
		t.Fatal("nil levels should return nil")
	}
	if shardLevels(&LevelConfigs{Tenant: &LevelConfig{RPM: 100}}, 1) == nil {
		t.Fatal("n=1 should still return a (possibly equivalent) config")
	}
}

// --- integration: total quota preserved across miniredis shards ---

func TestShardedRateLimiter_UniformSpreading_PreservesTotalQuota(t *testing.T) {
	const numShards = 4
	clients := make([]redis.UniversalClient, numShards)
	shards := make([]RateLimiter, numShards)

	// Total tenant RPM = 1000. Each shard enforces 1000/4 = 250.
	levels := &LevelConfigs{Tenant: &LevelConfig{RPM: 1000}}
	for i := 0; i < numShards; i++ {
		mr := miniredis.RunT(t)
		clients[i] = redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { clients[i].Close() })
		rl, err := NewRedisRateLimiter(clients[i], shardLevels(levels, numShards))
		if err != nil {
			t.Fatalf("failed to create shard limiter: %v", err)
		}
		shards[i] = rl
	}

	s, err := NewShardedRateLimiter(shards, &ShardedConfig{Sharding: &ShardingConfig{Method: "request_hash"}}, levels)
	if err != nil {
		t.Fatalf("failed to create sharded limiter: %v", err)
	}

	ctx := context.Background()
	allowed := 0
	for i := 0; i < 1000; i++ {
		keys := DimensionKeys{Tenant: "t1", RequestID: fmt.Sprintf("req-%d", i)}
		res, err := s.Check(ctx, keys, MetricRPM, 1)
		if err != nil {
			t.Fatalf("check %d failed: %v", i, err)
		}
		if res.Allowed {
			allowed++
		}
	}

	// No oversell: at most the total tenant quota (1000). Spreading worked:
	// request_hash distributes 1000 requests near-uniformly across 4 shards,
	// so no single shard's 250 limit should bottleneck far below 1000.
	if allowed > 1000 {
		t.Fatalf("sharded mode oversold the tenant: %d > 1000", allowed)
	}
	if allowed < 980 {
		t.Fatalf("sharding should spread load and allow ~1000 requests, got %d", allowed)
	}
}
