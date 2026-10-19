package cache

import (
	"context"
	"math"
	"sync"
	"time"
)

// VectorStore abstracts "store vectors + similarity retrieval". Note this is
// NOT a vector database — it only promises the ability to store and query
// embeddings. Implementations range from this file's indexless brute-force
// in-memory scan to a real vector database (Milvus/Qdrant/Redis Vector), which
// is the intended scale-out path.
type VectorStore interface {
	Search(ctx context.Context, emb []float64, threshold float64) (*VectorResult, error)
	Store(ctx context.Context, key string, emb []float64, resp *CachedResponse, ttl time.Duration) error
}

// VectorResult is a similarity-search hit.
type VectorResult struct {
	Key      string
	Score    float64 // cosine similarity in [0,1]
	Response *CachedResponse
}

// MemoryVectorStore is an indexless brute-force cosine scan over an in-memory
// map. It is NOT a vector database: O(n) per query, no persistence, single
// instance. It suits thousands of entries; beyond that it becomes slower than a
// real LLM call and must be replaced by Milvus/Qdrant (interface unchanged).
type MemoryVectorStore struct {
	mu      sync.RWMutex
	entries map[string]*vectorEntry
	maxSize int
}

type vectorEntry struct {
	embedding []float64
	response  *CachedResponse
	expires   time.Time
}

// NewMemoryVectorStore builds an in-memory vector store capped at maxSize.
func NewMemoryVectorStore(maxSize int) *MemoryVectorStore {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &MemoryVectorStore{entries: make(map[string]*vectorEntry), maxSize: maxSize}
}

// Search returns the highest-similarity entry at/above threshold, or nil.
func (m *MemoryVectorStore) Search(_ context.Context, emb []float64, threshold float64) (*VectorResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var best *VectorResult
	for key, entry := range m.entries {
		if !entry.expires.IsZero() && now.After(entry.expires) {
			delete(m.entries, key)
			continue
		}
		if score := cosineSimilarity(emb, entry.embedding); score >= threshold {
			if best == nil || score > best.Score {
				best = &VectorResult{Key: key, Score: score, Response: entry.response}
			}
		}
	}
	return best, nil
}

// Store adds an entry with TTL, silently refusing when the store is full so it
// cannot grow unbounded.
func (m *MemoryVectorStore) Store(_ context.Context, key string, emb []float64, resp *CachedResponse, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for k, entry := range m.entries {
		if !entry.expires.IsZero() && now.After(entry.expires) {
			delete(m.entries, k)
		}
	}
	if len(m.entries) >= m.maxSize {
		return nil
	}

	var expires time.Time
	if ttl > 0 {
		expires = now.Add(ttl)
	}
	m.entries[key] = &vectorEntry{embedding: emb, response: resp, expires: expires}
	return nil
}

// cosineSimilarity returns the cosine similarity between two equal-length
// vectors, 0 when either is zero-length or lengths mismatch.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
