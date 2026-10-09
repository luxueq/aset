package ratelimit

import (
	"context"
	"fmt"
	"hash/fnv"
)

// ShardedRateLimiter implements sharded rate limiting (plan 01-04 mode 4): a
// hot tenant's limit keys are spread across N shards, so no single backend node
// becomes the bottleneck.
//
// Routing is deterministic: a request is mapped to exactly one shard by its
// RequestID, so the admission (Check) and the post-stream correction (Correct)
// always land on the same shard. Each shard enforces total/N of the configured
// limits (even distribution), preserving the tenant's total quota while
// spreading load.
//
// Backend-agnostic by design: shards is a []RateLimiter, so any backend
// implementing the interface (Redis today; Memcache or MongoDB as future
// RateLimiter implementations) can be sharded without touching the routing
// logic here. NewShardedRateLimiterFromConfig is a Redis-specific convenience
// factory; callers with another backend construct shard limiters themselves and
// pass them to NewShardedRateLimiter.
//
// The plan's Task 4.4 also sketches cross-shard aggregation, hot-tenant
// auto-detection with dynamic resharding, and multi-region routing. Those are
// not implemented here: aggregation re-reads every shard (more load, not
// less), and dynamic resharding requires per-request dynamic limits the
// RateLimiter interface does not expose. See the phase SUMMARY for the full
// rationale.
type ShardedRateLimiter struct {
	shards   []RateLimiter
	sharding *ShardingConfig
	levels   *LevelConfigs
}

// NewShardedRateLimiter creates a sharded limiter over pre-built shard
// limiters. Each shard must already enforce total/numShards limits (see
// shardLevels); NewShardedRateLimiterFromConfig wires this up from config.
func NewShardedRateLimiter(shards []RateLimiter, cfg *ShardedConfig, levels *LevelConfigs) (*ShardedRateLimiter, error) {
	if len(shards) == 0 {
		return nil, fmt.Errorf("sharded mode requires at least one shard")
	}
	if levels == nil {
		levels = &LevelConfigs{}
	}
	var sharding *ShardingConfig
	if cfg != nil {
		sharding = cfg.Sharding
	}
	return &ShardedRateLimiter{
		shards:   shards,
		sharding: sharding,
		levels:   levels,
	}, nil
}

// NewShardedRateLimiterFromConfig builds a sharded limiter from configuration:
// one Redis client per configured cluster, each shard limiter enforcing
// total/numShards of the levels.
func NewShardedRateLimiterFromConfig(cfg *ShardedConfig, levels *LevelConfigs) (*ShardedRateLimiter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sharded config is nil")
	}

	redisCfgs := append([]*RedisConfig(nil), cfg.Clusters...)
	if len(redisCfgs) == 0 && cfg.Redis != nil {
		redisCfgs = append(redisCfgs, cfg.Redis)
	}
	if len(redisCfgs) == 0 {
		return nil, fmt.Errorf("sharded mode requires at least one redis cluster")
	}

	numShards := len(redisCfgs)
	shards := make([]RateLimiter, 0, numShards)
	for _, rc := range redisCfgs {
		client, err := NewRedisClient(rc)
		if err != nil {
			// A partial-init failure must not leak the shards already built:
			// close them before returning so test/reload/library callers that
			// retry (rather than exit) do not accumulate dead connections.
			closeShards(shards)
			return nil, fmt.Errorf("failed to create shard redis client: %w", err)
		}
		rl, err := NewRedisRateLimiter(client, shardLevels(levels, numShards))
		if err != nil {
			client.Close()
			closeShards(shards)
			return nil, fmt.Errorf("failed to create shard limiter: %w", err)
		}
		shards = append(shards, rl)
	}

	return NewShardedRateLimiter(shards, cfg, levels)
}

// Check routes the check to the shard selected for these keys.
func (s *ShardedRateLimiter) Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	idx, err := s.shardFor(keys)
	if err != nil {
		return nil, err
	}
	return s.shards[idx].Check(ctx, keys, metric, cost)
}

// Release routes the concurrency release to the shard selected for these keys.
func (s *ShardedRateLimiter) Release(ctx context.Context, keys DimensionKeys) error {
	idx, err := s.shardFor(keys)
	if err != nil {
		return err
	}
	return s.shards[idx].Release(ctx, keys)
}

// Correct routes the correction to the same shard as the original Check,
// because shardFor is deterministic in the RequestID.
func (s *ShardedRateLimiter) Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error {
	idx, err := s.shardFor(keys)
	if err != nil {
		return err
	}
	return s.shards[idx].Correct(ctx, keys, metric, preDeducted, actual)
}

// GetAvailable reports availability from a single deterministic shard. A true
// cross-shard aggregate is deferred; this is the shard a request with these
// keys would actually hit.
func (s *ShardedRateLimiter) GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error) {
	idx, err := s.shardFor(keys)
	if err != nil {
		return 0, err
	}
	return s.shards[idx].GetAvailable(ctx, keys, metric)
}

// Reset clears every shard: a tenant's data is spread across all shards, so an
// admin reset must reach them all. Reset does not require a RequestID — it is a
// global admin operation, not a per-request route.
func (s *ShardedRateLimiter) Reset(ctx context.Context, keys DimensionKeys) error {
	var firstErr error
	for _, sh := range s.shards {
		if err := sh.Reset(ctx, keys); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close closes every shard limiter (each owns its backend client).
func (s *ShardedRateLimiter) Close() error {
	var firstErr error
	for _, sh := range s.shards {
		if err := sh.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// shardFor maps a request to a shard index, deterministic in the RequestID so
// admission and correction land on the same shard.
//
// The RequestID is the only deterministic routing identity: it is unique per
// request, so hashing it spreads load uniformly across shards. A tenant (or
// global) fallback would be a constant per tenant, concentrating all of a hot
// tenant's traffic onto a single shard and silently under-serving it to 1/N of
// its quota — the exact bottleneck sharding exists to remove. So a missing
// RequestID is an error (when there is more than one shard).
func (s *ShardedRateLimiter) shardFor(keys DimensionKeys) (int, error) {
	n := len(s.shards)
	if n <= 1 {
		return 0, nil
	}
	if keys.RequestID == "" {
		return 0, fmt.Errorf("sharded mode requires a RequestID to route deterministically")
	}

	method := "request_hash"
	if s.sharding != nil && s.sharding.Method != "" {
		method = s.sharding.Method
	}
	return determineShard(keys.RequestID, n, method), nil
}

// determineShard deterministically maps a request to a shard index in [0, n).
//
// Only request_hash is implemented: a uniform FNV-1a hash of the per-request
// identity, modulo n. For a static shard set this is the correct, standard
// routing. The plan's other two methods are not real alternatives here and
// both resolve to request_hash:
//
//   - "consistent_hash" (a ketama ring) only matters when the shard set grows
//     or shrinks dynamically — deferred here, so hash-and-modulo is equivalent.
//   - "round_robin" is a stateful counter that cannot reproduce the same shard
//     at Correct time, which would break pre-deduction correction.
func determineShard(identity string, n int, method string) int {
	if n <= 1 {
		return 0
	}
	return int(fnv32a(identity) % uint32(n))
}

func fnv32a(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// shardLevels derives per-shard LevelConfigs by dividing every shared limit
// evenly by n (floor, min 1 so a limit never collapses to "unlimited").
// Concurrency is sharded too: each shard enforces total/n concurrent requests,
// preserving the tenant's total concurrency across shards. Nil inputs stay nil.
func shardLevels(levels *LevelConfigs, n int) *LevelConfigs {
	if levels == nil {
		return nil
	}
	if n <= 1 {
		return levels
	}
	return &LevelConfigs{
		Global: shardLevel(levels.Global, n),
		Tenant: shardLevel(levels.Tenant, n),
		Model:  shardModelMap(levels.Model, n),
		APIKey: shardLevel(levels.APIKey, n),
	}
}

func shardLevel(in *LevelConfig, n int) *LevelConfig {
	if in == nil {
		return nil
	}
	return &LevelConfig{
		RPM:          shardDiv(in.RPM, n),
		TPMInput:     shardDiv64(in.TPMInput, n),
		TPMOutput:    shardDiv64(in.TPMOutput, n),
		Concurrency:  shardDiv(in.Concurrency, n),
		DailyQuota:   shardDiv64(in.DailyQuota, n),
		MonthlyQuota: shardDiv64(in.MonthlyQuota, n),
	}
}

func shardModelMap(in map[string]*LevelConfig, n int) map[string]*LevelConfig {
	if in == nil {
		return nil
	}
	out := make(map[string]*LevelConfig, len(in))
	for k, v := range in {
		out[k] = shardLevel(v, n)
	}
	return out
}

func shardDiv(v, n int) int {
	if v <= 0 {
		return 0
	}
	r := v / n
	if r < 1 {
		r = 1
	}
	return r
}

// closeShards closes every shard in the slice, ignoring per-shard errors so a
// partial-init cleanup always runs to completion.
func closeShards(shards []RateLimiter) {
	for _, sh := range shards {
		_ = sh.Close()
	}
}

func shardDiv64(v int64, n int) int64 {
	if v <= 0 {
		return 0
	}
	r := v / int64(n)
	if r < 1 {
		r = 1
	}
	return r
}
