package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/luxueq/aigateway/internal/auth"
	"github.com/luxueq/aigateway/internal/httputil"
	"go.uber.org/zap"
)

// RateLimitMiddleware wraps an http.Handler with rate limiting. It runs after
// auth (which stores the validated API key in the context) and before the proxy.
//
// Admission checks RPM and concurrency, then pre-deducts max_tokens against the
// output token rate and the daily/monthly quota. After the response completes it
// reconciles the pre-deducted quota with the actual token usage, charges input
// tokens, and releases the concurrency slot. Streaming responses are never
// truncated — usage is read from the streamed body after the fact.
func RateLimitMiddleware(limiter RateLimiter, log *zap.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := auth.GetAPIKeyFromContext(r.Context())
			if apiKey == nil {
				// Auth runs first, so this only happens on unauthenticated
				// paths; there is no identity to rate limit against.
				next.ServeHTTP(w, r)
				return
			}

			// Peek the body to extract model and max_tokens, then restore it so
			// the downstream proxy can read it again.
			bodyBytes, err := httputil.PeekBody(w, r, httputil.DefaultMaxBodyBytes)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			model, maxTokens := extractModelAndMaxTokens(bodyBytes)

			keys := DimensionKeys{
				Global:    "global",
				Tenant:    apiKey.TenantID,
				Model:     bareModel(model),
				APIKey:    apiKey.KeyPrefix,
				RequestID: uuid.NewString(),
			}

			ctx := r.Context()
			limitHdr, remainingHdr, retryAfter, denied, concurrencyAcquired := admit(ctx, limiter, log, keys, maxTokens)

			if denied {
				// Release only if a concurrency slot was actually acquired
				// before a later metric denied the request.
				if concurrencyAcquired {
					_ = limiter.Release(ctx, keys)
				}
				writeRateLimitError(w, limitHdr, remainingHdr, retryAfter)
				return
			}

			setRateLimitHeaders(w, limitHdr, remainingHdr)

			cw := newTokenCountingWriter(w)
			next.ServeHTTP(cw, r)

			// Reconcile pre-deducted quota with actual usage and release
			// concurrency. Quota is a token-total budget (prompt + completion);
			// input tokens are charged after the fact because they cannot be
			// known at admission time.
			promptTokens, completionTokens := cw.usage()
			totalTokens := promptTokens + completionTokens
			if maxTokens > 0 {
				// Pre-deducted max_tokens; reconcile quota to actual total.
				_ = limiter.Correct(ctx, keys, MetricDailyQuota, maxTokens, totalTokens)
				_ = limiter.Correct(ctx, keys, MetricMonthlyQuota, maxTokens, totalTokens)
			} else if totalTokens > 0 {
				// No pre-deduction (max_tokens unset); charge output and quota
				// after the fact.
				_, _ = limiter.Check(ctx, keys, MetricTPMOutput, completionTokens)
				_, _ = limiter.Check(ctx, keys, MetricDailyQuota, totalTokens)
				_, _ = limiter.Check(ctx, keys, MetricMonthlyQuota, totalTokens)
			}
			if promptTokens > 0 {
				_, _ = limiter.Check(ctx, keys, MetricTPMInput, promptTokens)
			}
			if concurrencyAcquired {
				_ = limiter.Release(ctx, keys)
			}
		})
	}
}

// admit runs the admission checks and returns the rate-limit header values,
// whether the request was denied, and whether a concurrency slot was acquired.
// Checks are fail-open: a limiter error logs a warning and allows the request
// rather than taking the gateway down.
func admit(ctx context.Context, limiter RateLimiter, log *zap.Logger, keys DimensionKeys, maxTokens int64) (limit, remaining int64, retryAfter time.Duration, denied, concurrencyAcquired bool) {
	type check struct {
		metric Metric
		cost   int64
	}
	// RPM and concurrency are always checked. When max_tokens is declared, the
	// output token rate and fixed quotas are pre-deducted by max_tokens. Output
	// TPM is never refunded (Correct only affects quota metrics), so it is a
	// conservative over-charge — standard practice, since actual output tokens
	// are unknowable until the stream ends.
	checks := []check{
		{MetricRPM, 1},
		{MetricConcurrency, 1},
	}
	if maxTokens > 0 {
		checks = append(checks,
			check{MetricTPMOutput, maxTokens},
			check{MetricDailyQuota, maxTokens},
			check{MetricMonthlyQuota, maxTokens},
		)
	}

	for _, c := range checks {
		res, err := limiter.Check(ctx, keys, c.metric, c.cost)
		if err != nil {
			log.Warn("rate limit check failed; allowing request",
				zap.String("metric", string(c.metric)), zap.Error(err))
			continue
		}
		if res.Limit > 0 {
			// Track the tightest remaining across the checked metrics.
			if remaining == 0 || res.Remaining < remaining {
				limit = res.Limit
				remaining = res.Remaining
			}
		}
		if !res.Allowed {
			return limit, remaining, res.RetryAfter, true, concurrencyAcquired
		}
		if c.metric == MetricConcurrency {
			// A concurrency slot is only held once its Check returns Allowed.
			concurrencyAcquired = true
		}
	}
	return limit, remaining, 0, false, concurrencyAcquired
}

// tokenCountingWriter wraps the ResponseWriter and accumulates the full response
// body so token usage can be parsed after streaming. It also forwards Flusher so
// SSE streaming keeps working.
type tokenCountingWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func newTokenCountingWriter(w http.ResponseWriter) *tokenCountingWriter {
	return &tokenCountingWriter{ResponseWriter: w}
}

func (w *tokenCountingWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	return w.ResponseWriter.Write(p)
}

// Flush forwards to the underlying writer when it supports flushing, keeping
// SSE streaming intact.
func (w *tokenCountingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// usage parses prompt/completion token counts from the accumulated body. It
// handles a plain JSON response (non-streaming) and an OpenAI-compatible SSE
// stream (usage on the final data chunk before [DONE]).
func (w *tokenCountingWriter) usage() (prompt, completion int64) {
	body := w.buf.Bytes()

	// Non-streaming: the whole body is a JSON object with a top-level usage.
	if u := extractUsage(body); u != nil {
		return int64(u.PromptTokens), int64(u.CompletionTokens)
	}

	// Streaming: scan SSE data lines, keeping the last one that carried usage.
	var last *usageInfo
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if u := extractUsage([]byte(payload)); u != nil {
			last = u
		}
	}
	if last != nil {
		return int64(last.PromptTokens), int64(last.CompletionTokens)
	}
	return 0, 0
}

// usageInfo is the subset of an OpenAI usage object we care about.
type usageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// extractUsage unmarshals a top-level "usage" field, returning nil when absent.
func extractUsage(data []byte) *usageInfo {
	var obj struct {
		Usage *usageInfo `json:"usage"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	return obj.Usage
}

// extractModelAndMaxTokens reads just the fields the middleware needs from the
// request body. Errors are ignored: a malformed body yields an empty model,
// which the proxy will reject with its own 400.
func extractModelAndMaxTokens(body []byte) (model string, maxTokens int64) {
	var m struct {
		Model     string  `json:"model"`
		MaxTokens float64 `json:"max_tokens"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model, int64(m.MaxTokens)
}

// bareModel strips an explicit "provider/" prefix from a model string, matching
// the proxy's routing behavior.
func bareModel(model string) string {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

func setRateLimitHeaders(w http.ResponseWriter, limit, remaining int64) {
	if limit <= 0 {
		return
	}
	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
}

func writeRateLimitError(w http.ResponseWriter, limit, remaining int64, retryAfter time.Duration) {
	setRateLimitHeaders(w, limit, remaining)
	if retryAfter > 0 {
		// Round up to a whole second (min 1) so clients never read Retry-After: 0
		// and immediately retry.
		secs := int64((retryAfter + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_exceeded"}}`))
}
