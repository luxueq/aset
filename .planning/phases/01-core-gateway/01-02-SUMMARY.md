---
phase: 01-core-gateway
plan: 02
subsystem: core-gateway
tags: [provider-adapter, openai, anthropic, sse, retry, error-normalization, param-filtering]
requires:
  - phase: 01-core-gateway
    provides: [go-module, http-server, reverse-proxy, config-loading, structured-logging]
provides:
  - provider-interface
  - canonical-request-response-chunk-types
  - openai-adapter
  - anthropic-adapter
  - unified-openai-style-error-format
  - param-drop-strict-policy
  - retry-with-exponential-backoff
affects: [01-core-gateway (routing/failover plans), 02-sse-agent-runtime]
tech-stack:
  added: []
  patterns:
    - Provider interface with canonical (OpenAI-shaped) request/chunk types
    - SSE byte-level passthrough in proxy; adapters parse for structured consumers
    - Param filtering via per-provider SupportedParams map + ParamPolicy (drop/strict)
    - Exponential backoff with ctx-aware cancellation in doWithRetry
key-files:
  created:
    - internal/provider/openai/adapter_test.go
    - internal/provider/anthropic/adapter_test.go
    - internal/gateway/retry_integration_test.go
    - internal/gateway/proxy_test.go
  modified:
    - .planning/STATE.md
    - .planning/ROADMAP.md
    - .planning/REQUIREMENTS.md
decisions:
  - "01-02 delivered as verification + test-hardening only — Wave 1 implementation already satisfied all acceptance criteria; no production code changed"
  - "GW-03 (failover) NOT marked complete — it is Plan 03's deliverable; only GW-11 attributed to this plan"
  - "SSE forwarding is byte-level passthrough by design (proxy.go streamResponse); StreamAdapter provides structured parsing for future consumers (metrics/cache)"
requirements-completed: [GW-11]
metrics:
  duration: "~15 min (verification + test authoring)"
  completed: "2026-09-21"
---

# Phase 1 Plan 2: Provider Adapter Framework + OpenAI/Anthropic Summary

**One-liner:** Verified Wave 1's Provider adapter framework (4-method interface, canonical types, OpenAI/Anthropic adapters, OpenAI-style error normalization, drop/strict param policy, 429/500/502/503 retry with exponential backoff) and hardened it with 19 new tests — zero production code changes required.

## Performance

- **Duration:** ~15 min
- **Started:** 2026-09-21T06:20:03Z
- **Completed:** 2026-09-21T06:35:00Z
- **Tasks:** 5/5 verified
- **Files modified:** 4 created (tests only) + 3 planning docs

## Task Verification Matrix

Per execution instructions, every task's acceptance criteria was verified against the live codebase (Wave 1 commits `da8e15f`, `d31b3d3`). Only genuinely missing work was implemented.

### Task 2.1: Provider Interface Definition — VERIFIED (pre-existing)

- `Provider` interface with `Name`, `ConvertRequest`, `StreamAdapter` (internal/provider/provider.go:10-20)
- **Interface deviation (documented, not fixed):** plan sketched 5 methods including `ParseResponse` and `SupportsCapability`; the shipped interface has 3 methods. This is a deliberate simplification, not a defect — the proxy forwards provider responses as raw bytes (D-01 passthrough), so a non-streaming response parser is unused surface; capability routing belongs to the router (internal/router/router.go already implements strategy selection). Adding unused interface methods now would violate the context's "no dead code" posture (see 01-01 SUMMARY dead-code removal).
- `CanonicalRequest` has Model/Messages/Stream/Temperature/MaxTokens/TopP/Stop/Seed (provider.go:23-32)
- `CanonicalChunk` has ID/Delta/Usage/Done/RawData (provider.go:56-62)
- `go build ./internal/provider/...` exit 0

### Task 2.2: OpenAI Adapter — VERIFIED + TESTS ADDED

Pre-existing implementation (internal/provider/openai/adapter.go). All criteria confirmed:
- Implements all interface methods (adapter.go:52, 56, 79)
- `ConvertRequest` sets POST + URL + `Authorization: Bearer` + `Content-Type: application/json` (adapter.go:62-74); `GetBody` is auto-set by `http.NewRequestWithContext` with a `*bytes.Reader` body, enabling retry replay
- `StreamAdapter` parses `data: {"choices":[{"delta":...}]}` events (adapter.go:91-134)
- Emits `Done=true` on `data: [DONE]` (adapter.go:98-103) and on `finish_reason != null` (adapter.go:119-121)
- **Missing criterion found: "Unit test passes for SSE parsing" — no test file existed.** Gap-filled with 6 tests (commit `d38d7d6`), plus 2 proxy-level streaming tests (commit `f8e39a8`) verifying SSE bytes pass through unchanged and flush-per-chunk timing.

### Task 2.3: Anthropic Adapter — VERIFIED + TESTS ADDED

Pre-existing implementation (internal/provider/anthropic/adapter.go). All criteria confirmed:
- System message extracted from messages array into top-level `system` field; multiple system messages merged (adapter.go:103-118)
- `content_block_delta`/`text_delta` → `Delta` (adapter.go:168-171)
- `input_tokens` → `PromptTokens` (message_start, adapter.go:182-186); `output_tokens` → `CompletionTokens` (message_delta, adapter.go:176-180)
- Model prefix `anthropic/` stripped in `parseModel` at the proxy layer before the adapter sees it (proxy.go:249-255) — adapter receives the bare model name; anthropic-version header set to `2023-06-01` (adapter.go:18, 86)
- `max_tokens` defaults to 4096 when absent (Anthropic requires it) (adapter.go:120-124)
- **Missing test coverage gap-filled:** 7 tests (commit `bd78ede`) covering all four conversion criteria.

### Task 2.4: Error Normalization + Param Policy — VERIFIED (pre-existing)

- `APIError` with OpenAI-style `{"error":{"message","type","code"}}` output via `ToJSON` (internal/provider/errors.go:23-35)
- `ParseProviderError` maps status codes → error types (authentication_error / rate_limit_error / server_error / service_unavailable / invalid_request_error) and extracts provider messages (errors.go:38-88)
- `ParamDrop`/`ParamStrict` policy + `UnsupportedParamError` (internal/provider/params.go:4-11, 63-70)
- Per-provider `SupportedParams` maps; `logit_bias` dropped for anthropic (not in its map), `top_k` passed through for anthropic (in its map, params.go:15-34)
- `FilterUnsupportedParams` unit tests cover drop mode, strict mode error, unknown provider passthrough (errors_test.go:55-114)
- Used by auth middleware for OpenAI-format auth errors (internal/auth/middleware.go:99-106)
- **Documented boundary (not fixed):** the runtime param-drop path is not yet wired into `Proxy.ServeHTTP` (canonical request extraction handles a fixed field set); strict mode is not yet config-selectable. The plan's acceptance criteria for Task 2.4 are behavioral properties of `FilterUnsupportedParams` (all tested and passing) — end-to-end wiring of arbitrary-param filtering belongs with routing/dynamic-config work (Plan 03+), tracked under Known Gaps.

### Task 2.5: Automatic Retry — VERIFIED + TESTS ADDED

- `RetryConfig` defaults: MaxRetries 3, InitialBackoff 500ms, MaxBackoff 30s, RetryOn [429,500,502,503] (internal/gateway/retry.go:17-24)
- `doWithRetry`: non-retryable status returned immediately; retryable status closes body and backs off; body replay via `GetBody`; ctx cancellation checked both at loop top and inside the backoff `select` (proxy.go:174-235)
- Exponential backoff 500ms → 1s → 2s, capped 30s — unit tested (retry_test.go:35-61)
- Retryable/non-retryable status classification table-tested including 400/401/403/404 and 504 (retry_test.go:8-33)
- **Missing criterion found: no integration test proving end-to-end retry behavior against a live HTTP server.** Gap-filled with 4 httptest-based tests (commit `d5bbfe9`): 400 returned on first attempt with no backoff wait; 429 retried to success; exhaustion returns last retryable response after exactly 1+MaxRetries attempts; context cancellation during backoff aborts within <1s with no further upstream calls.

## Accomplishments

- All 5 tasks' acceptance criteria verified against live code with file:line evidence — Wave 1 implementation required zero production changes
- 19 new tests across 4 files converting previously untested acceptance criteria into permanent regression protection (adapter SSE parsing, Anthropic format conversion, retry integration, proxy streaming flush behavior)
- Full suite green: `go build ./...`, `go vet ./...`, `go test ./...` all pass (8 packages with tests)

## Task Commits

Each gap-fill was committed atomically:

1. **Task 2.2 OpenAI adapter tests** — `d38d7d6` (test)
2. **Task 2.3 Anthropic adapter tests** — `bd78ede` (test)
3. **Task 2.5 retry integration tests** — `d5bbfe9` (test)
4. **Task 2.2 proxy streaming tests** — `f8e39a8` (test)

**Plan metadata:** (final docs commit, see below)

## Files Created/Modified

- `internal/provider/openai/adapter_test.go` — 6 tests: SSE parsing, [DONE] sentinel, malformed line skipping, channel-close semantics, request headers, body replayability
- `internal/provider/anthropic/adapter_test.go` — 7 tests: system extraction/merge, max_tokens default, headers, event→canonical conversion, token mapping, channel-close
- `internal/gateway/retry_integration_test.go` — 4 httptest integration tests for doWithRetry
- `internal/gateway/proxy_test.go` — 2 tests for streamResponse passthrough and flush-per-chunk timing
- `.planning/STATE.md`, `.planning/ROADMAP.md`, `.planning/REQUIREMENTS.md` — bookkeeping updates

## Decisions Made

- **Verification-only execution:** the important_context directive took precedence — implemented only missing criteria (test coverage), never duplicating working Wave 1 code
- **Provider interface kept at 3 methods** rather than plan-sketch's 5: `ParseResponse`/`SupportsCapability` have no consumer under byte-passthrough forwarding; adding them now is dead surface. Router (Plan 03) owns provider selection. Deviation documented, not auto-added, because it is an interface-design choice rather than missing correctness/security functionality
- **GW-03 not marked complete:** plan frontmatter lists `[GW-03, GW-11]`, but GW-03 (automatic failover to backup provider) is Plan 03's deliverable — this plan has no failover code. Only GW-11 attributed and marked
- **Param filtering boundary:** strict-mode config selection (`param_policy: strict` in YAML) deferred — the function-level behavior is fully implemented and tested; the config-to-runtime wiring lands with dynamic routing config (Plan 03) where per-route policies make sense

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Proxy streaming test asserted wrong header responsibility**
- **Found during:** Task 2.2 test authoring
- **Issue:** Initial test expected `streamResponse` to set Content-Type; in the real call sequence `ServeHTTP` copies upstream headers before streaming
- **Fix:** Test mirrors production sequence (copy headers, then stream)
- **Files modified:** internal/gateway/proxy_test.go
- **Verification:** Test passes; body passthrough assertion unchanged
- **Committed in:** `f8e39a8` (test-only, fixed before commit)

---

**Total deviations:** 1 auto-fixed (1 test-authoring bug, fixed pre-commit)
**Impact on plan:** None — no production code was modified in this plan.

## Issues Encountered

None.

## Known Stubs

None introduced by this plan. Existing deferred items carried forward (unchanged):
- `internal/ratelimit/factory.go:29,33` — TODO for Plan 04
- `/ready` dependency checks — deferred to Plan 06

## Known Gaps (forward-looking, not blocking this plan)

- `FilterUnsupportedParams` is not yet invoked from `Proxy.ServeHTTP`; canonical request extraction handles a fixed known-param set. Arbitrary-param drop/strict enforcement wiring belongs with routing config (Plan 03)
- Provider interface lacks `ParseResponse`/`SupportsCapability` (see Decisions) — add when a consumer exists

## Threat Flags

None. Test-only changes; no new network endpoints, auth paths, or trust-boundary changes.

## Self-Check: PASSED

- [x] internal/provider/provider.go exists (interface + canonical types)
- [x] internal/provider/openai/adapter.go exists
- [x] internal/provider/anthropic/adapter.go exists
- [x] internal/provider/errors.go + params.go exist (error format + param policy)
- [x] internal/gateway/retry.go + doWithRetry in proxy.go exist
- [x] `go build ./...` exit 0
- [x] `go vet ./...` clean
- [x] `go test ./...` — 8 packages ok, 0 failures
- [x] Commits `d38d7d6`, `bd78ede`, `d5bbfe9`, `f8e39a8` exist on main

---
*Phase: 01-core-gateway*
*Completed: 2026-09-21*
