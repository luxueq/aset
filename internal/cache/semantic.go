package cache

import (
	"context"
	"sync"
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
	threshold atomic.Value // global default threshold, atomically swapped at runtime
	ttl       time.Duration

	// tenantThresholds holds per-tenant threshold overrides. A tenant absent
	// from this map falls back to the global threshold. Guarded by mu.
	mu               sync.RWMutex
	tenantThresholds map[string]float64
}

// NewSemanticCache builds a semantic cache. enabled=false disables it entirely
// (Get returns a clean miss, Set is a no-op). A non-positive threshold falls
// back to 0.92 — a starting guess only; tune it via SetThreshold /
// SetTenantThreshold (or the semantic_threshold / semantic_thresholds config)
// against real hit/false-hit rates.
func NewSemanticCache(enabled bool, embedder Embedder, store VectorStore, threshold float64, ttl time.Duration) *SemanticCache {
	if threshold <= 0 {
		threshold = 0.92
	}
	sc := &SemanticCache{
		enabled:          enabled,
		embedder:         embedder,
		store:            store,
		ttl:              ttl,
		tenantThresholds: make(map[string]float64),
	}
	sc.threshold.Store(threshold)
	return sc
}

// SetThreshold updates the global similarity threshold at runtime so it can be
// tuned without a restart. Values <= 0 are ignored (keeps the current threshold).
func (s *SemanticCache) SetThreshold(f float64) {
	if f <= 0 {
		return
	}
	s.threshold.Store(f)
}

// SetTenantThreshold sets (or clears, when f <= 0) a per-tenant threshold
// override. Tenants without an override use the global threshold.
func (s *SemanticCache) SetTenantThreshold(tenant string, f float64) {
	if tenant == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f <= 0 {
		delete(s.tenantThresholds, tenant)
		return
	}
	s.tenantThresholds[tenant] = f
}

// thresholdFor resolves the similarity threshold for a tenant: a per-tenant
// override wins, otherwise the global threshold.
func (s *SemanticCache) thresholdFor(tenant string) float64 {
	if tenant != "" {
		s.mu.RLock()
		if t, ok := s.tenantThresholds[tenant]; ok {
			s.mu.RUnlock()
			return t
		}
		s.mu.RUnlock()
	}
	return s.threshold.Load().(float64)
}

// Get looks up the most similar cached response using the global threshold.
// See GetForTenant for the tenant-aware variant.
func (s *SemanticCache) Get(ctx context.Context, text string) (*CachedResponse, bool, error) {
	return s.GetForTenant(ctx, "", text)
}

// GetForTenant returns the most similar cached response above the tenant's
// threshold, ok=false on a miss. When the semantic cache is disabled it returns
// a clean miss without invoking the embedder. An embedding error is returned to
// the caller, which should degrade to "skip semantic cache" (never block the
// request on embedding).
func (s *SemanticCache) GetForTenant(ctx context.Context, tenant, text string) (*CachedResponse, bool, error) {
	if !s.enabled {
		return nil, false, nil
	}
	emb, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return nil, false, err
	}
	result, err := s.store.Search(ctx, emb, s.thresholdFor(tenant))
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

// Clear empties the underlying vector store. No-op when the cache is disabled
// or the store is nil.
func (s *SemanticCache) Clear(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	return s.store.Clear(ctx)
}
