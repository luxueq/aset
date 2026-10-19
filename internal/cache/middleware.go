package cache

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/luxueq/aigateway/internal/httputil"
	"github.com/luxueq/aigateway/internal/provider"
	"go.uber.org/zap"
)

// CacheMiddleware wraps an http.Handler with response caching. It runs after
// auth and before rate limiting, so a cache hit skips both the limiter and the
// upstream proxy.
//
// Only non-streaming responses are cached. A hit replays the stored response
// verbatim with X-Cache: HIT (and X-Semantic-Cache: HIT for semantic hits); a
// miss forwards the request and caches the resulting 200 response.
func CacheMiddleware(c *Cache, sc *SemanticCache, log *zap.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = zap.NewNop()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Peek the body to build the cache key, then restore it for the
			// downstream handler.
			bodyBytes, err := httputil.PeekBody(w, r, httputil.DefaultMaxBodyBytes)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			var req provider.CanonicalRequest
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				next.ServeHTTP(w, r)
				return
			}

			// Streaming responses are never cached (real-time requirement).
			if req.Stream {
				w.Header().Set("X-Cache", "MISS")
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()
			key := ComputeCacheKey(&req, KeyConfig{})

			// Exact match.
			if resp, ok, err := c.Get(ctx, key); err == nil && ok {
				writeCached(w, resp, false)
				recordCacheHit("exact")
				return
			}

			// Semantic match — only for deterministic (temperature 0) requests.
			text := extractMessageText(req.Messages)
			if sc != nil && isDeterministic(&req) {
				if resp, ok, err := sc.Get(ctx, text); err == nil && ok {
					writeCached(w, resp, true)
					recordCacheHit("semantic")
					return
				}
			}

			// Miss: capture the downstream response and cache a 200.
			rec := &responseRecorder{ResponseWriter: w}
			w.Header().Set("X-Cache", "MISS")
			next.ServeHTTP(rec, r)

			if rec.statusCode != http.StatusOK {
				return
			}
			cached := &CachedResponse{
				StatusCode: rec.statusCode,
				Headers:    extractContentType(rec),
				Body:       rec.body.Bytes(),
			}
			_ = c.Set(ctx, key, cached)
			if sc != nil && isDeterministic(&req) {
				_ = sc.Set(ctx, key, text, cached)
			}
		})
	}
}

// responseRecorder captures the status and body of a downstream response while
// still writing through to the client.
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	body        bytes.Buffer
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.statusCode = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// writeCached replays a cached response verbatim.
func writeCached(w http.ResponseWriter, resp *CachedResponse, semantic bool) {
	if ct, ok := resp.Headers["Content-Type"]; ok {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Cache", "HIT")
	if semantic {
		w.Header().Set("X-Semantic-Cache", "HIT")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.Body)
}

func extractContentType(rec *responseRecorder) map[string]string {
	ct := rec.Header().Get("Content-Type")
	if ct == "" {
		return nil
	}
	return map[string]string{"Content-Type": ct}
}

func isDeterministic(req *provider.CanonicalRequest) bool {
	return req.Temperature != nil && *req.Temperature == 0
}

func extractMessageText(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteByte('\n')
	}
	return sb.String()
}
