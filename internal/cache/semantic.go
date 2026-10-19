package cache

import (
	"context"
	"sync/atomic"
	"time"
)

// SemanticCache implements semantic caching: it embeds the request text and
// looks up the most similar stored response above a cosine threshold. It is the
// facade that orchestrates an Embedder and a VectorStore.
type SemanticCache struct {
	enabled   bool
	embedder  Embedder
	store     VectorStore
	threshold atomic.Value // stores float64, atomically swapped for runtime tuning
	ttl       time.Duration
}

// NewSemanticCache builds a semantic cache. enabled=false disables it entirely
// (Get returns a clean miss, Set is a no-op). A non-positive threshold falls
// back to 0.92 — a starting guess only; tune it via SetThreshold (or the
// semantic_threshold config) against real hit/false-hit rates.
func NewSemanticCache(enabled bool, embedder Embedder, store VectorStore, threshold float64, ttl time.Duration) *SemanticCache {
	if threshold <= 0 {
		threshold = 0.92
	}
	sc := &SemanticCache{
		enabled:  enabled,
		embedder: embedder,
		store:    store,
		ttl:      ttl,
	}
	sc.threshold.Store(threshold)
	return sc
}

// SetThreshold updates the similarity threshold at runtime so it can be tuned
// without a restart. Values <= 0 are ignored (keeps the current threshold).
func (s *SemanticCache) SetThreshold(f float64) {
	if f <= 0 {
		return
	}
	s.threshold.Store(f)
}

// Get returns the most similar cached response above threshold, ok=false on a
// miss. When the semantic cache is disabled it returns a clean miss without
// invoking the embedder. An embedding error is returned to the caller, which
// should degrade to "skip semantic cache" (never block the request on embedding).
func (s *SemanticCache) Get(ctx context.Context, text string) (*CachedResponse, bool, error) {
	if !s.enabled {
		return nil, false, nil
	}
	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, false, err
	}
	result, err := s.store.Search(ctx, emb, s.threshold.Load().(float64))
	if err != nil {
		return nil, false, err
	}
	if result == nil {
		return nil, false, nil
	}
	return result.Response, true, nil
}

// Set stores a response under an embedding of its request text. No-op when the
// semantic cache is disabled.
func (s *SemanticCache) Set(ctx context.Context, key, text string, resp *CachedResponse) error {
	if !s.enabled {
		return nil
	}
	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return err
	}
	return s.store.Store(ctx, key, emb, resp, s.ttl)
}
