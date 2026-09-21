---
phase: 01-core-gateway
plan: 03
subsystem: core-gateway
tags: [routing, failover, load-balancing, health-check, api-key, auth, aes-gcm]
requires:
  - phase: 01-core-gateway
    provides: [go-module, http-server, reverse-proxy, config-loading, provider-interface, canonical-request-response-chunk-types, retry-with-exponential-backoff]
provides:
  - router-engine-4-strategies
  - model-based-route-rules
  - candidates-failover-order
  - automatic-cross-provider-failover
  - sliding-window-error-rate-monitor
  - active-health-monitor
  - routing-config-yaml
  - api-key-revoke-semantics
  - http-level-auth-middleware-tests
affects: [01-core-gateway (cache/ratelimit plans), 02-sse-agent-runtime]
tech-stack:
  added: []
  patterns:
    - Strategy pattern with Candidates() ordered failover list (strategy primary first, stable pool order after)
    - Sliding-window passive health (5m window, 100-event tail cap, MinSamples noise guard)
    - Active health checks (3-consecutive-failure streak) + passive error rate combined per D-12
    - Per-provider retry (D-07) composes with cross-provider failover (D-10): retries exhaust first, then failover
key-files:
  created:
    - internal/router/router_test.go
    - internal/router/failover.go
    - internal/router/failover_test.go
    - internal/router/health.go
    - internal/router/health_test.go
    - internal/gateway/routing_test.go
    - internal/auth/handler_test.go
  modified:
    - internal/router/router.go
    - internal/gateway/proxy.go
    - internal/gateway/proxy_test.go
    - internal/common/config/config.go
    - internal/auth/handler.go
    - config/default.yaml
key-decisions:
  - "Router owns model→pool resolution (route rules, provider/model pinning, default provider, all-providers pool); proxy parseModel default-to-openai removed"
  - "DELETE /v1/api-keys/:id changed from hard Delete to Revoke (soft delete) to satisfy the 'revoked key returns 401' criterion"
  - "Sliding window replaces cumulative error rate: single transient failure must not evict a provider (MinSamples=3); >50% windowed rate evicts"
  - "5xx/429 provider responses are failover-worthy; 2xx/4xx are final results returned to the client"
  - "Auth (Task 3.3/3.4) verified not re-implemented: Wave 1 commit 65b65fd already satisfied service-level criteria; only revoke semantics fixed and HTTP-level tests added"
  - "DB persistence for API keys (DBRepository) exists but wiring into KeyService/Handler is deferred to Plan 06 (docker-compose + /ready DB checks) — in-memory store is the current live path"
patterns-established:
  - "RouteWithFailover(model, cfg, tryFn) callback contract: provider candidates walk with terminal ErrAllProvidersFailed"
  - "ProviderConfigs.All() deterministic iteration for router status seeding"
requirements-completed: [GW-02, GW-03, GW-04, GW-05, GW-06]
metrics:
  duration: "~35 min"
  completed: "2026-09-21"
---

# Phase 1 Plan 3: Router Engine + Failover + API Key Management Summary

**One-liner:** Model-based routing engine with 4 pluggable strategies, cross-provider failover (max 3 providers, ReturnOnError escape hatch), sliding-window + active-streak health monitoring wired into the gateway proxy, plus revoke-correct API key management with HTTP-level auth middleware tests.

## Performance

- **Duration:** ~35 min
- **Started:** 2026-09-21T06:26:45Z
- **Completed:** 2026-09-21T07:05:00Z
- **Tasks:** 4/4 (2 fully implemented, 2 verified + gap-filled)
- **Files modified:** 14 (7 created, 7 modified)
- **New tests:** 43 (15 router, 6 failover, 6 health, 11 proxy routing, 15 auth handler/middleware — net of shared helpers)

## Task Verification Matrix

### Task 3.1: Router Engine with 4 Strategies — VERIFIED + GAP-FILLED

Pre-existing `internal/router/router.go` had the Strategy interface and 4 strategies but **ignored the request entirely** — no model-based routing existed. Gap-filled (commit `6c443a6`):

- `Strategy` interface + `Router` struct (router.go:28-32)
- 4 strategies: `RoundRobinStrategy`, `LatencyStrategy`, `CostStrategy`, `WeightedStrategy` (router.go:391-476)
- **Model-based pool resolution added** — `Candidates(model)` with `RouteRule{Model, Providers, Strategy}`: full model match → bare model match → provider/model pinning → default provider → all-providers pool (router.go:167-242)
- **100-request round-robin test** — exactly 34/33/33 across 3 providers (router_test.go `TestRouter_RoundRobinCyclesEvenly`)
- Route-rule strategy override, latency/cost strategy selection, weighted 90/10 distribution over 1000 draws, zero-weight fallback all tested

### Task 3.2: Failover + Load Balancing — IMPLEMENTED (was absent)

Nothing of Task 3.2 existed: no FailoverConfig, no ErrAllProvidersFailed, cumulative (not windowed) error rate, no health checker, router unused by any package. Implemented (commits `f93f047`, `b3b78b0`):

- **`RouteWithFailover`** (failover.go): walks Candidates in order; max 3 providers (configurable); every attempt reported for passive tracking; exhaustion returns `ErrAllProvidersFailed` with per-provider `Attempt` log
- **Primary failure → next provider** — proxy-level integration test: openai 502 → anthropic 200, client receives anthropic's success (routing_test.go `TestProxy_RouteAndForward_FailoverOn5xx`); per-provider retry (D-07) fires first: openai sees 1+3 retries, then failover
- **`return_on_error: true`** returns the provider error/response to the client instead of failing over — verified at both router and proxy level
- **Sliding window (5m default, 100-event tail cap, MinSamples=3)** — a single transient failure no longer evicts a provider (the pre-existing cumulative counter would have marked it unhealthy on the first error — bug found and fixed); >50% windowed rate marks unhealthy; window expiry tested
- **Active HealthMonitor** (health.go): GETs provider `/models` endpoints every 30s (immediate first round), 3 consecutive failures → unhealthy, one success → recovery; ctx-aware loop; verified via httptest servers
- **Unhealthy providers excluded** from Candidates until MarkHealthy recovery — tested at router and proxy level
- **Wired into the proxy** (proxy.go `routeAndForward`): replaces direct provider lookup; `routing:` YAML section (config.go `RoutingConfig`/`RouteRule`/`Failover` + config/default.yaml); live config parse verified

### Task 3.3: API Key Management — VERIFIED + 1 FIX

Fully pre-implemented by Wave 1 commit `65b65fd` (KeyService, Encryptor AES-256-GCM, DBRepository PostgreSQL schema, Handler routes wired in main.go). Verified with evidence; one gap fixed (commit `7a64060`):

- `POST /v1/api-keys` 201 + plaintext key returned once — tested (handler_test.go)
- `GET /v1/api-keys` lists prefix only — tested, full key leak assertion included
- `DELETE /v1/api-keys/:id` — **was hard Delete; now Revoke** (soft delete) so "revoked key returns 401 on next request" holds — tested including cross-tenant isolation
- AES-256-GCM at rest — round-trip decrypt test; `keyID:base64(nonce+ciphertext)` format; secret from `AIGATEWAY_KEY_ENCRYPTION_SECRET` (main.go:68-77, dev fallback warns)
- Key prefix 13 chars + `***` for display
- PostgreSQL storage: `DBRepository` + `InitSchema` exist (repository.go); wiring into the live KeyService path deferred to Plan 06 (see Known Stubs)

### Task 3.4: Auth Middleware — VERIFIED + TESTS ADDED

Fully pre-implemented (middleware.go). All six acceptance criteria now pinned by HTTP-level tests (commit `7a64060`):

- No/invalid `Authorization: Bearer` → 401 (4 malformed-header variants)
- Valid key passes through, context contains key info (asserted via context extraction in the wrapped handler)
- Revoked key → 401; expired key → 401; unknown key → 401
- `/health` and `/ready` skip auth (200 without key)
- Permission check: chat-only key → 403 on `/v1/embeddings`, passes on `/v1/chat/completions`

## Task Commits

| Task | Commit | Type |
|------|--------|------|
| 3.1 Router engine + strategies + model routing | `6c443a6` | feat |
| 3.2 Failover + sliding window + health monitor | `f93f047` | feat |
| 3.2 (formatting) | `a280baf` | style |
| 3.2 Proxy wiring + routing config + integration tests | `b3b78b0` | feat |
| 3.3/3.4 Revoke fix + HTTP-level auth tests | `7a64060` | feat |

## Decisions Made

- **Router owns resolution, proxy forwards**: `parseModel`'s implicit default-to-openai removed; `provider/model` pinning, route rules, and default provider all live in the router; adapters receive the bare model name
- **Retry-then-failover composition**: per-provider `doWithRetry` (D-07) exhausts before cross-provider failover (D-10) — both criteria satisfied without conflating the two layers
- **Failover-worthy responses**: 5xx and 429 trigger failover; 2xx/4xx are final (a 400 from the provider is the client's answer, not an outage)
- **Soft delete for DELETE**: Revoke matches the plan's "revoked key returns 401" criterion; hard removal would leave no 401 to return
- **Sliding window design**: window + tail cap + MinSamples so transient errors don't evict providers and frozen clocks (tests) can't unbound memory
- **Verification-first for auth**: Wave 1 already satisfied the service-level criteria; only the revoke bug and test coverage were genuinely missing

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Cumulative error rate marked providers unhealthy on first failure**
- **Found during:** Task 3.1/3.2 test authoring
- **Issue:** Pre-existing RecordFailure set ErrorRate = ErrorCount/TotalCount cumulatively and marked Healthy=false above 50% — the first failure (rate 1.0) instantly evicted the provider, breaking both failover recovery and the sliding-window criterion
- **Fix:** Replaced with 5m sliding window (100-event tail cap, MinSamples=3 noise guard); >50% windowed rate evicts
- **Files modified:** internal/router/router.go
- **Commit:** `f93f047`

**2. [Rule 1 - Bug] DELETE /v1/api-keys/:id performed hard removal**
- **Found during:** Task 3.3 verification
- **Issue:** Plan criterion "Revoked key returns 401 on next request" cannot hold if DELETE removes the record (Validate returns ErrKeyNotFound, not the revoked path; the client can never observe revocation)
- **Fix:** DELETE now calls Revoke; response body `{"status":"revoked"}`; hard Delete remains a service-layer/admin operation
- **Files modified:** internal/auth/handler.go
- **Commit:** `7a64060`

**3. [Rule 2 - Missing functionality] Router not wired into proxy; no route rules/failover config**
- **Found during:** Task 3.1/3.2 verification
- **Issue:** `internal/router/router.go` existed but was imported by zero files; `Select` ignored the request; no route rules; no failover
- **Fix:** Candidates(model) + RouteRule resolution, routeAndForward in proxy, RoutingConfig YAML, ProviderConfigs.All() seeding, 11 integration tests
- **Files modified:** internal/router/router.go, internal/gateway/proxy.go, internal/common/config/config.go, config/default.yaml
- **Commits:** `6c443a6`, `b3b78b0`

**4. [Rule 3 - Blocking] gofmt drift committed in f93f047**
- **Issue:** Committed before running gofmt; three router files unformatted
- **Fix:** `a280baf` (style commit)

### Notes

- Environment: the macOS data volume was at 100% capacity during execution (build/test transiently failed with "no space left on device"); resolved by cleaning Go build/test caches. No work was lost.
- Test-authoring bugs (round-robin call-count expectation, ReturnOnError response-vs-error expectation, floating-point equality on 0.5) were fixed before their commits — not counted as deviations.

## Known Stubs

- `internal/ratelimit/factory.go:29,33` — TODO for Plan 04 (pre-existing, out of scope)
- `/ready` dependency checks — deferred to Plan 06 by design (pre-existing)
- **API key DB wiring**: `DBRepository` (PostgreSQL, full CRUD + schema) exists but `KeyService` uses its in-memory map; `main.go` does not open a DB connection. Plan 06 (docker-compose + `AIGATEWAY_DATABASE_URL` + `/ready` DB checks) is where persistence wiring lands. The auth acceptance criteria of this plan are all satisfied by the in-memory path (encrypted storage format, hashing, prefix, revocation are storage-independent); DB persistence is tracked as Plan 06 scope.
- **Admin auth on `/v1/api-keys` routes**: unprotected (documented NOTE in main.go, pre-existing). Management endpoints assume a trusted network boundary for Phase 1; flagged as threat surface below.

## Requirement Attribution Note

The plan frontmatter lists `[GW-02, GW-04, GW-05, GW-06]` but omits **GW-03** (automatic failover to a backup provider) — which is Task 3.2's explicit deliverable and is fully implemented and tested here (RouteWithFailover, exhaustion handling, ReturnOnError, health-based exclusion). GW-03 is marked complete and attributed to this plan. (Same correction pattern as 01-02, which deferred GW-03 here.)

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: missing-admin-auth | cmd/aigateway/main.go:93 | `/v1/api-keys` management routes have no admin authentication (pre-existing from 65b65fd, surfaced by this plan's verification). Anyone with network access can create/revoke keys. RBAC is Phase 7 (GOV-02); an interim admin token would be a minimal mitigation. |
| threat_flag: dev-encryption-secret | cmd/aigateway/main.go:70 | Fallback encryption secret "default-dev-secret-change-in-production" with a warning log — acceptable for dev, must fail closed in production (deferred decision) |

## Self-Check: PASSED

- [x] internal/router/router.go, failover.go, health.go exist with 4 strategies + failover + health monitor
- [x] internal/router/{router,failover,health}_test.go exist — 27 tests
- [x] internal/gateway/routing_test.go exists — 11 tests
- [x] internal/auth/handler_test.go exists — 15 tests
- [x] `go build ./...` exit 0
- [x] `go vet ./...` clean
- [x] `go test ./... -count=1` — 9 packages ok, 0 failures
- [x] Commits `6c443a6`, `f93f047`, `a280baf`, `b3b78b0`, `7a64060` exist on main

---
*Phase: 01-core-gateway*
*Completed: 2026-09-21*
