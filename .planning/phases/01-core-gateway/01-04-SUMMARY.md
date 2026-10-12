---
phase: 01-core-gateway
plan: 04
subsystem: core-gateway
tags: [rate-limiting, token-bucket, sliding-window, semaphore, fixed-window-quota, redis-lua, cluster-hash-tag, hybrid-lease, circuit-breaker, sharding, request-routing, middleware, pre-deduction]
requires:
  - phase: 01-core-gateway
    provides: [config-loading, http-server, reverse-proxy, provider-interface, canonical-request-response-chunk-types, router-engine-4-strategies, api-key-auth-context]
provides:
  - rate-limiter-facade-interface
  - local-mode-limiter
  - redis-mode-limiter-cluster-compatible
  - hybrid-mode-limiter-lease-circuit-breaker
  - sharded-mode-limiter-deterministic-routing
  - six-metric-dimensions
  - rate-limit-middleware-pre-deduction-correction
  - sse-usage-parsing
  - rate-limit-config-wiring
affects: [01-core-gateway (cache/budget plans), 02-sse-agent-runtime, 04-observability]
tech-stack:
  added: [github.com/google/uuid, github.com/redis/go-redis/v9]
  patterns:
    - Facade (RateLimiter interface) + Strategy (per-mode implementation chosen by config)
    - Backend-agnostic sharding (shards is []RateLimiter, not []redis.Client)
    - Deterministic shard routing by RequestID (admission + correction land on the same shard)
    - Pre-deduction + post-response correction for streaming (never truncate in-flight streams)
    - ResponseWriter wrapper (tokenCountingWriter) for SSE/JSON usage parsing across Write boundaries
key-files:
  created:
    - internal/ratelimit/interface.go
    - internal/ratelimit/local.go
    - internal/ratelimit/redis.go
    - internal/ratelimit/redis_client.go
    - internal/ratelimit/hybrid.go
    - internal/ratelimit/lease.go
    - internal/ratelimit/circuit_breaker.go
    - internal/ratelimit/quota.go
    - internal/ratelimit/sharded.go
    - internal/ratelimit/middleware.go
    - internal/ratelimit/rate_bucket.lua
    - internal/ratelimit/sliding_window.lua
    - internal/ratelimit/concurrency.lua
    - internal/ratelimit/concurrency_release.lua
    - internal/ratelimit/quota.lua
    - internal/ratelimit/quota_correct.lua
    - internal/ratelimit/local_test.go
    - internal/ratelimit/local_extended_test.go
    - internal/ratelimit/quota_test.go
    - internal/ratelimit/redis_test.go
    - internal/ratelimit/hybrid_test.go
    - internal/ratelimit/sharded_test.go
    - internal/ratelimit/middleware_test.go
  modified:
    - internal/ratelimit/config.go
    - internal/ratelimit/factory.go
    - internal/common/config/config.go
    - internal/common/config/config_test.go
    - internal/auth/middleware.go
    - cmd/aigateway/main.go
key-decisions:
  - "Facade + Strategy: one RateLimiter interface, four backends (Local/Redis/Hybrid/Sharded) selected by config mode"
  - "Backend-agnostic sharding: ShardedRateLimiter holds []RateLimiter so Memcache/MongoDB can be sharded later without touching routing (addresses the non-Redis-backend concern)"
  - "Concurrency uses a semaphore (atomic acquire/release), NOT a token bucket or sliding window — concurrency is an instantaneous in-flight count, not a rate"
  - "RPM uses a sliding window (strict, anti-boundary-burst); TPM input/output use a token bucket (burst-friendly, O(1) for token counts); daily/monthly quota uses a fixed calendar window (UTC midnight / month start)"
  - "Correct only affects quota metrics (isQuotaMetric); TPM output pre-deduction is a conservative over-charge that is never refunded — standard practice since output tokens are unknowable until the stream ends"
  - "Sharded routing is deterministic in RequestID; a missing RequestID (multi-shard) is an error, not a tenant fallback (a tenant fallback would concentrate a hot tenant onto one shard and re-introduce the bottleneck)"
  - "Replace the dead config.Config.RateLimit (TokensPerSec) with RateLimiter *ratelimit.Config; add mapstructure tags so viper maps multi-word fields (tpm_input etc.)"
  - "Rate-limit middleware is fail-open on limiter errors (log + allow) so a limiter outage never takes the gateway down"
  - "The concurrency slot is released only when it was actually acquired (denied-before-concurrency must not steal an in-flight request's slot)"
patterns-established:
  - "NewRateLimiter(cfg) factory returns RateLimiter for the configured mode"
  - "admit() checks metrics in RPM → Concurrency → pre-deducted output/quota order, tracking the tightest remaining for headers"
  - "tokenCountingWriter.Flush forwards to the underlying http.Flusher so SSE stays live"
requirements-completed: [GW-06]
metrics:
  duration: "multi-session (Wave 1 → 01-04)"
  completed: "2026-10-10"
---

# Phase 1 Plan 4: Rate Limiting + Quota + Budget Control Summary

**One-liner:** A facade-based rate limiter with four interchangeable backends (Local / Redis-cluster-compatible / Hybrid-lease / Sharded), six metric dimensions, and an HTTP middleware that pre-deducts `max_tokens` and corrects to actual usage after streaming — wired into the gateway so admission checks and 429s actually fire.

## Performance

- **Tasks:** 5/5 (all implemented)
- **Files:** 16 non-test + 7 test files in `internal/ratelimit/`
- **New tests:** local (2 files) + quota + redis + hybrid + sharded + middleware
- **Verification:** `go build ./...` ✅, `go vet ./...` clean, `go test ./...` 9 packages ok

## Task Verification Matrix

### Task 4.1: Interface + Local Mode — VERIFIED (Wave 1 pre-implemented)

`RateLimiter` facade (`interface.go`) with 6 methods (`Check/Release/Correct/GetAvailable/Reset/Close`); `LocalRateLimiter` (`local.go`) implements all six metrics in-process:

| Metric | Algorithm |
|--------|-----------|
| RPM | sliding window (1m) |
| TPMInput / TPMOutput | token bucket (refill = limit/60) |
| Concurrency | semaphore (atomic) |
| DailyQuota / MonthlyQuota | fixed window (UTC midnight / month start) |

`DimensionKeys` carries Global/Tenant/Model/APIKey (+ `RequestID` added later for sharded routing). `Check` walks Global → Tenant → Model → APIKey in order.

### Task 4.2: Redis Mode (cluster-compatible) — VERIFIED (Wave 1)

`RedisRateLimiter` + `NewRedisClient` (`redis_client.go`) auto-detect standalone vs cluster (multiple URLs → cluster). Six Lua scripts (`rate_bucket`, `sliding_window`, `concurrency`, `concurrency_release`, `quota`, `quota_correct`) keep local/Redis semantics identical; hash-tag keys (`rl:{dim}:{key}:{metric}`) keep multi-key ops on one slot. `Correct` batches per-dimension corrections into a single pipeline.

### Task 4.3: Hybrid Mode — IMPLEMENTED (`838d9b2`)

`HybridRateLimiter` + `LeaseManager` (`lease.go`, batch token lease from Redis) + `CircuitBreaker` (`circuit_breaker.go`) with fallback strategies (fail-closed for billing quota, conservative-local for RPM, local-only for concurrency).

### Task 4.4: Sharded Mode — IMPLEMENTED (`e62e300`)

`ShardedRateLimiter` (`sharded.go`) spreads a hot tenant's keys across N backends. Routing is deterministic in `RequestID` so `Check` and `Correct` land on the same shard; each shard enforces `total/N` (floor, min 1). **Backend-agnostic by design**: `shards []RateLimiter`, so any future backend can be sharded without touching routing. See Deviations for the deferred parts.

### Task 4.5: Middleware + Streaming — IMPLEMENTED (`8c5d2a1`)

`RateLimitMiddleware` (`middleware.go`) runs after auth, before the proxy: peeks the body (`model` + `max_tokens`) then restores it, builds `DimensionKeys` (tenant/key/model + `RequestID`), checks RPM → concurrency → pre-deducted output/quota, returns 429 with `X-RateLimit-*`/`Retry-After`, and after the response reconciles quota to actual total tokens via `Correct`, charges input tokens, and releases concurrency. `tokenCountingWriter` wraps the ResponseWriter, forwards `Flusher`, and parses `usage` from a non-streaming JSON body or an OpenAI-compatible SSE stream (across Write boundaries). Streaming is never truncated. Config wiring replaced the dead `RateLimit` (`TokensPerSec`) with `RateLimiter *ratelimit.Config`; `auth.WithAPIKey` added for handler/test use.

## Task Commits

| Task | Commit | Type |
|------|--------|------|
| 4.1/4.2 Interface + local + redis (Wave 1) | `65b65fd` | feat |
| 4.3 Hybrid + lease + circuit breaker | `838d9b2` | feat |
| 4.3 (formatting) | `ae2828b` | style |
| 4.4 RequestID in DimensionKeys | `3da564e` | feat |
| 4.4 Sharded limiter + tests | `e62e300` | feat |
| 4.5 Middleware + config wiring + main.go | `8c5d2a1` | feat |

## Decisions Made

- **Facade + Strategy**: one interface, four backends, mode selected in config — the plan's core design (门面模式 + 策略模式).
- **Backend-agnostic sharding** (回应「memcache/mongodb 也能实现」的顾虑): shards hold `RateLimiter`, not Redis clients; `NewShardedRateLimiterFromConfig` is the Redis convenience factory, but other backends construct shard limiters and pass them to `NewShardedRateLimiter`.
- **Algorithm-per-metric** (see §4.1 table): concurrency is a semaphore (instantaneous count ≠ rate), RPM is a sliding window, TPM is a token bucket, quota is a fixed calendar window. This was an explicit correction during review — token bucket for concurrency and sliding window for tokens are both wrong fits.
- **Correct is quota-only**: TPM output pre-deduction is never refunded (conservative over-charge); only fixed quotas are reconciled.
- **Deterministic shard routing**: RequestID (not tenant) is the routing key — a tenant fallback would re-concentrate a hot tenant onto one shard.
- **Fail-open middleware**: limiter errors log and allow, rather than 503 the gateway.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Concurrency slot released when never acquired** (found by go-reviewer on Task 4.5)
- **Issue:** `Release` ran unconditionally, so a request denied at RPM released a slot belonging to an in-flight request — under contention this drives the semaphore toward 0 and defeats the concurrency cap.
- **Fix:** `admit` returns `concurrencyAcquired`; `Release` only runs when true. Two tests pin it (`DeniedBeforeConcurrency_DoesNotRelease`, `DeniedAfterConcurrency_Releases`).
- **Files:** internal/ratelimit/middleware.go, middleware_test.go

**2. [Rule 2 - Missing] No-`max_tokens` path under-charged quota**
- **Issue:** When `max_tokens` was unset, post-response only charged `TPMOutput`, skipping daily/monthly quota entirely.
- **Fix:** The no-pre-deduction branch now charges output + both quotas after the fact.
- **Files:** internal/ratelimit/middleware.go

**3. [Rule 3 - Bug] `Retry-After` rounded to 0**
- **Issue:** `int64(retryAfter.Seconds())` yields `0` for sub-second durations, which clients read as "retry immediately".
- **Fix:** round up to a whole second (min 1).
- **Files:** internal/ratelimit/middleware.go

### Deferred (Task 4.4, recorded deliberately)

The plan's Task 4.4 acceptance criteria included several items that were **not implemented**, with rationale recorded in `sharded.go`:

- **Hot-tenant auto-detection + dynamic resharding** — deferred: per-request dynamic limits are not exposed by the `RateLimiter` interface.
- **Cross-shard aggregation** — deferred: aggregation re-reads every shard (more load, not less), which defeats sharding's purpose; `GetAvailable` returns the single deterministic shard's value.
- **Multi-region routing** — deferred: out of scope for a single-region MVP.
- **100k+ RPM load test** — not run (no load-test harness in Phase 1).

## Known Stubs

- **GW-09 (partial)**: per-tenant/per-key *token* quotas (DailyQuota/MonthlyQuota) are implemented; the *USD budget* cap (MonthlyBudget in `auth.QuotaConfig`) is not enforced.
- **GW-10 (not started)**: budget-near-limit **alerting** has no implementation — the plan frontmatter lists GW-10 but no Task covers notifications.
- **`ShardingConfig` surface**: `consistent_hash`/`round_robin` methods are accepted but silently resolve to `request_hash` (documented in `determineShard`); no warning is emitted on unsupported config.
- **Unbounded body buffering**: `middleware.go` `io.ReadAll` + `tokenCountingWriter.buf` have no size cap (flagged by review; acceptable for MVP, revisit for `/v1/images` large payloads).

## Requirement Attribution Note

Plan frontmatter lists `[GW-06, GW-09, GW-10]`. **GW-06 (token-level rate limiting, fixed + sliding window) is fully implemented and tested.** GW-09 is **partially** satisfied (token quotas; USD budget cap not enforced). GW-10 (budget alerting) is **not implemented** — it belongs to a later budget-control task and should not be marked complete on this plan.

## Self-Check: PASSED

- [x] `internal/ratelimit/interface.go`, `local.go`, `redis.go`, `redis_client.go`, `hybrid.go`, `lease.go`, `circuit_breaker.go`, `quota.go`, `sharded.go`, `middleware.go` exist
- [x] 6 Lua scripts exist (rate_bucket, sliding_window, concurrency, concurrency_release, quota, quota_correct)
- [x] 7 test files exist (local, local_extended, quota, redis, hybrid, sharded, middleware)
- [x] `go build ./...` exit 0
- [x] `go vet ./...` clean
- [x] `go test ./...` — 9 packages ok, 0 failures
- [x] Commits `65b65fd`, `838d9b2`, `ae2828b`, `3da564e`, `e62e300`, `8c5d2a1` exist on main

---
*Phase: 01-core-gateway*
*Completed: 2026-10-10*
