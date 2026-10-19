package cache

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestCache() *Cache {
	c, _ := NewCache(100, nil, time.Minute)
	return c
}

func post(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
}

func TestCacheMiddleware_MissThenHit(t *testing.T) {
	c := newTestCache()
	mw := CacheMiddleware(c, nil, zap.NewNop())

	calls := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"temperature":0}`

	// First request: miss, downstream called, response cached.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, post(body))
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first request should be MISS, got %q", rec1.Header().Get("X-Cache"))
	}

	// Second identical request: hit, downstream NOT called again.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, post(body))
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second request should be HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if rec2.Body.String() != `{"ok":true}` {
		t.Fatalf("cached body mismatch: %q", rec2.Body.String())
	}
	if calls != 1 {
		t.Fatalf("downstream should be called once, got %d", calls)
	}
}

func TestCacheMiddleware_Streaming_NeverCached(t *testing.T) {
	c := newTestCache()
	mw := CacheMiddleware(c, nil, zap.NewNop())

	calls := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, post(body))
		if rec.Header().Get("X-Cache") != "MISS" {
			t.Fatalf("streaming must always be MISS, got %q", rec.Header().Get("X-Cache"))
		}
	}
	if calls != 2 {
		t.Fatalf("streaming must not be cached, downstream called %d times", calls)
	}
}

func TestCacheMiddleware_SemanticHit(t *testing.T) {
	c := newTestCache()
	em := &mockEmbedder{vectors: map[string][]float64{
		"user: hello\n": {1, 0},
		"user: hi\n":    {0.6, 0.8},
	}}
	sc := NewSemanticCache(true, em, NewMemoryVectorStore(100), 0.5, time.Minute)
	mw := CacheMiddleware(c, sc, zap.NewNop())

	calls := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"answer":"hi"}`))
	}))

	// First: deterministic request "hello" seeds the caches (exact + semantic).
	handler.ServeHTTP(httptest.NewRecorder(), post(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"temperature":0}`))

	// Second: different message "hi" → exact miss, semantic hit (cosine 0.6).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, post(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":0}`))

	if rec.Header().Get("X-Semantic-Cache") != "HIT" {
		t.Fatalf("expected semantic HIT, got X-Cache=%q X-Semantic-Cache=%q",
			rec.Header().Get("X-Cache"), rec.Header().Get("X-Semantic-Cache"))
	}
	if calls != 1 {
		t.Fatalf("semantic hit should skip downstream, calls=%d", calls)
	}
}

func TestCacheMiddleware_SemanticSkippedForNonDeterministic(t *testing.T) {
	c := newTestCache()
	em := &mockEmbedder{vectors: map[string][]float64{
		"user: hello\n": {1, 0},
		"user: hi\n":    {0.6, 0.8},
	}}
	sc := NewSemanticCache(true, em, NewMemoryVectorStore(100), 0.5, time.Minute)
	mw := CacheMiddleware(c, sc, zap.NewNop())

	calls := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))

	// temperature 0.7 → non-deterministic → semantic cache must NOT be consulted.
	handler.ServeHTTP(httptest.NewRecorder(), post(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`))
	handler.ServeHTTP(httptest.NewRecorder(), post(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`))

	// Two different non-deterministic requests never hit semantic cache (or
	// exact, since temperature differs) → downstream called twice.
	if calls != 2 {
		t.Fatalf("non-deterministic requests should not semantic-hit, calls=%d", calls)
	}
}
