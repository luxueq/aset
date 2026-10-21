---
phase: 01-core-gateway
plan: 05
subsystem: core-gateway
tags: [caching, l1-lru, l2-redis, cache-key, sha-256, exact-match, semantic-cache, embedding, cosine-similarity, vector-store, cache-middleware, x-cache-header, gzip, big-key-protection, prometheus-metrics, http-body-peek]
requires:
  - phase: 01-core-gateway
    provides: [config-loading, http-server, reverse-proxy, canonical-request-response-chunk-types, provider-interface]
provides:
  - cache-key-computation
  - two-layer-cache-l1-lru-l2-redis
  - redis-cache-gzip-big-key-protection
  - cache-middleware-x-cache
  - semantic-cache-embedding-cosine
  - pluggable-embedder-openai-compatible-local
  - in-memory-vector-store
  - cache-metrics-prometheus
  - http-body-peek-utility
affects: [01-core-gateway (01-06 remaining), 04-observability (metrics foundation)]
tech-stack:
  added: [github.com/hashicorp/golang-lru/v2, github.com/prometheus/client_golang]
  patterns:
    - Two-layer cache (L1 local LRU hot + L2 Redis shared TTL), L1 backfill on L2 hit
    - Facade interfaces (Embedder, VectorStore) so semantic cache swaps backends without touching the flow
    - Redis big-key protection: gzip only when it shrinks (≥1KB), skip caching responses over 1MB
    - UNLINK (async) for cache clear instead of blocking DEL
    - responseRecorder captures status+body while still streaming to the client
    - PeekBody (httputil) reads body for key/dimension then restores it, shared by cache + rate-limit
key-files:
  created:
    - internal/cache/key.go
    - internal/cache/cache.go
    - internal/cache/redis_cache.go
    - internal/cache/middleware.go
    - internal/cache/semantic.go
    - internal/cache/embedder.go
    - internal/cache/vector.go
    - internal/cache/metrics.go
    - internal/cache/key_test.go
    - internal/cache/cache_test.go
    - internal/cache/middleware_test.go
    - internal/cache/semantic_test.go
    - internal/httputil/body.go
  modified:
    - internal/ratelimit/middleware.go
    - internal/common/config/config.go
    - cmd/aigateway/main.go
    - .planning/ROADMAP.md
key-decisions:
  - "Semantic cache only engages for deterministic requests (temperature == 0) — reusing a non-deterministic response for a 'similar' prompt is unsafe, so similarity reuse is gated on determinism"
  - "Cache key always covers model + messages; temperature/seed participate unless listed in IgnoreFields (IncludeFields is a reserved no-op today since the key has no other optional inputs)"
  - "LocalEmbedder is a character n-gram feature-hash, NOT true semantics — it exists so a user who refuses to send prompts to a third-party embedding service can still run semantic cache with reduced accuracy (documented in embedder.go)"
  - "CacheMiddleware runs after auth, before rate-limit, so a cache hit skips both the limiter and the upstream proxy"
  - "Redis big-key protection is three-part: <1KB stored raw, ≥1KB gzip'd only when it actually shrinks, >1MB skipped entirely"
patterns-established:
  - "ComputeCacheKey(req, KeyConfig) returns a 64-char SHA-256 hex; ignored fields are filtered via a map lookup"
  - "CacheMiddleware peeks the body, unmarshals CanonicalRequest, bypasses on stream=true, then exact → semantic → upstream-and-store"
  - "writeCached replays a CachedResponse verbatim (status + Content-Type + body) and stamps X-Cache: HIT (plus X-Semantic-Cache: HIT for semantic)"
requirements-completed: [GW-07, GW-08]
metrics:
  duration: "2026-10-15 (implementation)"
  completed: "2026-10-20"
---

# Phase 1 Plan 5: Caching (Exact Match + Semantic) Summary

**One-liner:** A two-layer cache (L1 local LRU + L2 Redis with gzip/big-key protection) plus a semantic cache (pluggable embedder + brute-force cosine vector store) — wired into the request path as middleware so identical non-streaming requests replay a cached response and similar deterministic prompts reuse one, with Prometheus hit/miss metrics and `X-Cache`/`X-Semantic-Cache` headers.

## Performance

- **Tasks:** 5/5 (all implemented; see gaps below)
- **Files:** 8 non-test + 4 test files in `internal/cache/`, plus `internal/httputil/body.go`
- **New tests:** key, cache (L1/L2), middleware, semantic (incl. per-tenant threshold + clear)
- **Verification:** `go build ./...` ✅, `go vet ./...` clean, `go test ./...` 11 packages ok

## Task Verification Matrix

### Task 5.1: Cache Key Computation — VERIFIED

`ComputeCacheKey(req *provider.CanonicalRequest, cfg KeyConfig)` in `key.go` hashes model + messages (role+content), then temperature (`t:%.2f`) and seed (`s:%d`) unless listed in `cfg.IgnoreFields`. Returns a 64-char SHA-256 hex. `IncludeFields` is reserved (unused — the key has no other optional inputs today). Tests pin: same input → same key, differing temperature → differing key, ignored field → no effect, 64-char hex.

### Task 5.2: L1 + L2 Two-Layer Cache — VERIFIED

`Cache` (`cache.go`) holds `l1 *lru.Cache[string,*CachedResponse]` + `l2 *RedisCache`. `Get` checks L1 → L2 (backfilling L1 on an L2 hit); `Set` writes both. `RedisCache` (`redis_cache.go`) stores `CachedResponse{StatusCode, Headers, Body}` as JSON with gzip when ≥1KB and only when it shrinks, skips bodies >1MB (big-key protection), and clears via `SCAN` + `UNLINK`. `Clear(ctx)` purges both layers. TTL is applied at the Redis `SET`; L1 eviction is LRU via `golang-lru`. The `DELETE /v1/cache/clear` endpoint clears both layers.

### Task 5.3: Cache Middleware — VERIFIED

`CacheMiddleware` (`middleware.go`) peeks + restores the body (`httputil.PeekBody`), unmarshals `CanonicalRequest`, bypasses with `X-Cache: MISS` when `stream=true`, else computes the key, replays on exact hit (`X-Cache: HIT`), tries semantic on a deterministic miss, and otherwise captures the downstream response with `responseRecorder` and caches a 200. A hit skips both the limiter and the upstream proxy (middleware order: auth → cache → rate-limit → proxy).

### Task 5.4: Semantic Cache — VERIFIED

`SemanticCache` (`semantic.go`) is a facade over `Embedder` + `VectorStore`. `OpenAICompatibleEmbedder` targets any OpenAI-compatible `/embeddings` endpoint (OpenAI/DashScope/Zhipu/DeepSeek/Ollama); `LocalEmbedder` is a zero-dependency n-gram feature-hash fallback. `MemoryVectorStore` (`vector.go`) brute-forces cosine similarity with TTL + max-size cap. `X-Semantic-Cache: HIT` is stamped on a semantic hit. Semantic lookup is gated on `isDeterministic` (temperature == 0). Per-tenant thresholds are supported via `SetTenantThreshold` / the `semantic_thresholds` config map, resolved by `GetForTenant(ctx, tenant, text)`.

### Task 5.5: Cache Metrics — VERIFIED

`metrics.go` registers `aigateway_cache_hits_total`, `aigateway_cache_misses_total`, `aigateway_cache_latency_seconds`, all labeled `cache_type` (exact|semantic). Hits, misses, and lookup latency are all recorded from the middleware. The plan's `model` label was dropped to `cache_type`-only (the acceptance criteria require only `cache_type`).

## Task Commits

| Task | Commit | Type |
|------|--------|------|
| 5.1–5.5 (all cache implementation) | `fed5751` | feat |
| (前置) provider config list 化 | `d7170e1` | refactor |

## Decisions Made

- **Determinism gate for semantic cache**: similarity reuse only when `temperature == 0` — a non-deterministic response must not be reused for a merely-similar prompt.
- **Key composition**: model + messages always; temperature/seed optional (ignorable). `IncludeFields` reserved.
- **Big-key protection** (three-part): raw <1KB, gzip ≥1KB when it shrinks, skip >1MB.
- **UNLINK over DEL** for clear — deleting many/large values must not block the Redis main thread.
- **Pluggable embedder/vector-store** interfaces isolate the semantic cache from a specific embedding provider and from the brute-force in-memory store (Milvus/Qdrant scale-out path).
- **Middleware order** cache-before-rate-limit so hits skip the limiter and upstream.

## Deviations from Plan

- **Metric labels simplified**: the plan's code example used `["cache_type", "model"]`; the implementation labels by `["cache_type"]` only. The acceptance criteria require only `cache_type`, so the `model` cardinality was deliberately dropped to keep `/metrics` cardinality low.

## Known Stubs

- **`DELETE /v1/cache/clear` is auth-protected but not permission-scoped**: any valid API key can clear the shared cache (the auth permission model has no `cache_admin` permission yet). Acceptable for Phase 1; revisit when governance lands.
- **LocalEmbedder accuracy** — character n-gram hash, not true semantic similarity; it cannot equate "口渴" ≈ "想喝水" (no character overlap). It exists as a privacy-preserving fallback, documented as such.
- **Body buffering cap** — `PeekBody` caps at 1 MiB and fails open (passes through unpeeked) on overflow; acceptable for chat-completion payloads, revisit for `/v1/images` large bodies.

## Requirement Attribution Note

Plan frontmatter lists `[GW-07, GW-08]`. **GW-07 (精确匹配缓存) is fully implemented** — identical requests replay the cached response. **GW-08 (语义缓存) is implemented** — similar deterministic prompts reuse a response via cosine similarity — but note the two limitations that touch its completeness: threshold is not per-tenant, and the local embedder is n-gram rather than true semantic. Both are recorded above as Known Stubs, not blocking.

## Self-Check: PASSED

- [x] `internal/cache/` — key.go, cache.go, redis_cache.go, middleware.go, semantic.go, embedder.go, vector.go, metrics.go exist
- [x] 4 test files exist (key, cache, middleware, semantic); semantic tests cover per-tenant threshold and clear
- [x] `internal/httputil/body.go` (PeekBody) exists and is reused by ratelimit middleware
- [x] `go build ./...` exit 0
- [x] `go vet ./...` clean
- [x] `go test ./...` — 11 packages ok, 0 failures
- [x] Code landed on `main` — refactor (provider list) + feat (caching) + fix (acceptance-gap closure)

---
*Phase: 01-core-gateway*
*Completed: 2026-10-20*
