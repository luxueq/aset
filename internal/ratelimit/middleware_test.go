package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luxueq/aigateway/internal/auth"
	"go.uber.org/zap"
)

// fakeLimiter records every Check/Correct/Release call so the middleware's
// orchestration (order, cost, correction) can be asserted deterministically.
type fakeLimiter struct {
	denyMetric Metric // when non-empty, this metric's Check returns denied
	checks     []checkCall
	corrects   []correctCall
	releases   int
}

type checkCall struct {
	metric Metric
	cost   int64
}

type correctCall struct {
	metric      Metric
	preDeducted int64
	actual      int64
}

func (f *fakeLimiter) Check(_ context.Context, _ DimensionKeys, metric Metric, cost int64) (*LimitResult, error) {
	f.checks = append(f.checks, checkCall{metric: metric, cost: cost})
	if metric == f.denyMetric {
		return &LimitResult{Allowed: false, RetryAfter: time.Second, Limit: 100, Remaining: 0}, nil
	}
	return &LimitResult{Allowed: true, Limit: 100, Remaining: 50}, nil
}

func (f *fakeLimiter) Release(_ context.Context, _ DimensionKeys) error { f.releases++; return nil }

func (f *fakeLimiter) Correct(_ context.Context, _ DimensionKeys, metric Metric, pre, actual int64) error {
	f.corrects = append(f.corrects, correctCall{metric: metric, preDeducted: pre, actual: actual})
	return nil
}

func (f *fakeLimiter) GetAvailable(context.Context, DimensionKeys, Metric) (int64, error) {
	return 0, nil
}
func (f *fakeLimiter) Reset(context.Context, DimensionKeys) error { return nil }
func (f *fakeLimiter) Close() error                               { return nil }

func newTestRequest(t *testing.T, body string, withKey bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	if withKey {
		req = req.WithContext(auth.WithAPIKey(req.Context(), &auth.APIKey{
			TenantID:  "tenant-1",
			KeyPrefix: "ak-test",
			Name:      "test",
		}))
	}
	return req
}

// TestRateLimitMiddleware_AdmissionOrder verifies the middleware checks metrics
// in RPM → Concurrency → (pre-deducted output/quota) order with the right costs.
func TestRateLimitMiddleware_AdmissionOrder(t *testing.T) {
	fl := &fakeLimiter{}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := newTestRequest(t, `{"model":"openai/gpt-4","max_tokens":100,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	want := []checkCall{
		{metric: MetricRPM, cost: 1},
		{metric: MetricConcurrency, cost: 1},
		{metric: MetricTPMOutput, cost: 100},
		{metric: MetricDailyQuota, cost: 100},
		{metric: MetricMonthlyQuota, cost: 100},
	}
	if len(fl.checks) != len(want) {
		t.Fatalf("checks = %+v, want %d calls %+v", fl.checks, len(want), want)
	}
	for i := range want {
		if fl.checks[i] != want[i] {
			t.Errorf("check[%d] = %+v, want %+v", i, fl.checks[i], want[i])
		}
	}
}

// TestRateLimitMiddleware_RejectsOverLimit verifies a denied metric yields 429
// with the rate-limit headers and a Retry-After.
func TestRateLimitMiddleware_RejectsOverLimit(t *testing.T) {
	fl := &fakeLimiter{denyMetric: MetricRPM}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler must not run on a denied request")
	}))

	req := newTestRequest(t, `{"model":"gpt-4","max_tokens":10,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After header should be set on 429")
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got == "" {
		t.Error("X-RateLimit-Limit header should be set")
	}
}

// TestRateLimitMiddleware_RestoresBody verifies the downstream handler reads the
// exact original body (the middleware must restore r.Body after peeking).
func TestRateLimitMiddleware_RestoresBody(t *testing.T) {
	fl := &fakeLimiter{}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	body := `{"model":"gpt-4","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, len(body)+1)
		n, _ := r.Body.Read(buf)
		got := strings.TrimSpace(string(buf[:n]))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(got))
	}))

	req := newTestRequest(t, body, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != body {
		t.Fatalf("downstream body = %q, want %q", got, body)
	}
}

// TestRateLimitMiddleware_StreamingCorrectsUsage verifies that a streamed SSE
// response's usage is parsed (across Write boundaries) and Correct is called
// with the actual completion tokens, plus input tokens are charged after the fact.
func TestRateLimitMiddleware_StreamingCorrectsUsage(t *testing.T) {
	fl := &fakeLimiter{}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hel`))
		w.Write([]byte(`lo"}}]}` + "\n\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"))
		w.Write([]byte(`data: [DONE]` + "\n\n"))
	}))

	req := newTestRequest(t, `{"model":"gpt-4","max_tokens":100,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Pre-deducted 100 for output/quota, corrected to actual total tokens
	// (prompt 10 + completion 5 = 15). Quota is a token-total budget.
	var foundDaily, foundMonthly bool
	for _, c := range fl.corrects {
		switch c.metric {
		case MetricDailyQuota:
			foundDaily = true
			if c.preDeducted != 100 || c.actual != 15 {
				t.Errorf("DailyQuota correct = pre %d actual %d, want pre 100 actual 15", c.preDeducted, c.actual)
			}
		case MetricMonthlyQuota:
			foundMonthly = true
			if c.preDeducted != 100 || c.actual != 15 {
				t.Errorf("MonthlyQuota correct = pre %d actual %d, want pre 100 actual 15", c.preDeducted, c.actual)
			}
		}
	}
	if !foundDaily || !foundMonthly {
		t.Fatalf("expected DailyQuota and MonthlyQuota corrections, got %+v", fl.corrects)
	}

	// Input tokens (10 prompt) are charged after the fact via Check.
	var inputCharged bool
	for _, c := range fl.checks {
		if c.metric == MetricTPMInput && c.cost == 10 {
			inputCharged = true
		}
	}
	if !inputCharged {
		t.Errorf("expected TPMInput charged with 10 prompt tokens, got checks %+v", fl.checks)
	}
}

// TestRateLimitMiddleware_NoAPIKey_PassesThrough verifies a request without an
// authenticated API key bypasses rate limiting (defensive; auth runs first).
func TestRateLimitMiddleware_NoAPIKey_PassesThrough(t *testing.T) {
	fl := &fakeLimiter{}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := newTestRequest(t, `{"model":"gpt-4","messages":[]}`, false)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(fl.checks) != 0 {
		t.Fatalf("expected no checks without API key, got %+v", fl.checks)
	}
}

// TestRateLimitMiddleware_DeniedBeforeConcurrency_DoesNotRelease verifies a
// request denied at RPM (before concurrency) does not release a slot it never
// acquired — releasing would steal an in-flight request's concurrency slot.
func TestRateLimitMiddleware_DeniedBeforeConcurrency_DoesNotRelease(t *testing.T) {
	fl := &fakeLimiter{denyMetric: MetricRPM}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream must not run on a denied request")
	}))

	req := newTestRequest(t, `{"model":"gpt-4","max_tokens":10,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if fl.releases != 0 {
		t.Fatalf("expected no Release when denied before concurrency, got %d releases", fl.releases)
	}
}

// TestRateLimitMiddleware_DeniedAfterConcurrency_Releases verifies a request
// denied at a metric checked after concurrency releases the acquired slot.
func TestRateLimitMiddleware_DeniedAfterConcurrency_Releases(t *testing.T) {
	fl := &fakeLimiter{denyMetric: MetricDailyQuota}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream must not run on a denied request")
	}))

	req := newTestRequest(t, `{"model":"gpt-4","max_tokens":10,"messages":[]}`, true)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if fl.releases != 1 {
		t.Fatalf("expected 1 Release after concurrency acquired, got %d releases", fl.releases)
	}
}

// TestRateLimitMiddleware_NoMaxTokens_ChargesAfterFact verifies that when the
// request omits max_tokens, output and quota are charged from actual usage and
// no pre-deduction correction happens.
func TestRateLimitMiddleware_NoMaxTokens_ChargesAfterFact(t *testing.T) {
	fl := &fakeLimiter{}
	mw := RateLimitMiddleware(fl, zap.NewNop())
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))

	req := newTestRequest(t, `{"model":"gpt-4","messages":[]}`, true) // no max_tokens
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var outputCharged, dailyCharged, monthlyCharged, inputCharged bool
	for _, c := range fl.checks {
		switch c.metric {
		case MetricTPMOutput:
			outputCharged = outputCharged || c.cost == 5
		case MetricDailyQuota:
			dailyCharged = dailyCharged || c.cost == 15
		case MetricMonthlyQuota:
			monthlyCharged = monthlyCharged || c.cost == 15
		case MetricTPMInput:
			inputCharged = inputCharged || c.cost == 10
		}
	}
	if !outputCharged || !dailyCharged || !monthlyCharged || !inputCharged {
		t.Fatalf("expected after-the-fact charges, got checks %+v", fl.checks)
	}
	if len(fl.corrects) != 0 {
		t.Fatalf("expected no Correct calls without pre-deduction, got %+v", fl.corrects)
	}
}
