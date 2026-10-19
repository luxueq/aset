package cache

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

// mockEmbedder returns pre-canned vectors and counts calls, so tests can assert
// the embedder was (or was not) invoked.
type mockEmbedder struct {
	vectors map[string][]float64
	calls   int
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	m.calls++
	v, ok := m.vectors[text]
	if !ok {
		return nil, fmt.Errorf("no vector for %q", text)
	}
	return v, nil
}

func newTestSemanticCache(enabled bool, vectors map[string][]float64, threshold float64) (*SemanticCache, *mockEmbedder) {
	em := &mockEmbedder{vectors: vectors}
	return NewSemanticCache(enabled, em, NewMemoryVectorStore(100), threshold, time.Minute), em
}

func TestSemanticCache_Hit(t *testing.T) {
	sc, _ := newTestSemanticCache(true, map[string][]float64{
		"hello": {1, 0},
		"hi":    {0.6, 0.8}, // cosine([1,0],[0.6,0.8]) = 0.6
	}, 0.5)
	ctx := context.Background()

	resp := &CachedResponse{StatusCode: 200, Body: []byte("cached")}
	if err := sc.Set(ctx, "hello-key", "hello", resp); err != nil {
		t.Fatal(err)
	}

	got, ok, err := sc.Get(ctx, "hi")
	if err != nil || !ok {
		t.Fatalf("expected semantic hit, ok=%v err=%v", ok, err)
	}
	if got != resp {
		t.Fatal("returned response should be the stored one")
	}
}

func TestSemanticCache_BelowThreshold_Miss(t *testing.T) {
	sc, _ := newTestSemanticCache(true, map[string][]float64{
		"hello": {1, 0},
		"hi":    {0.6, 0.8}, // cosine 0.6
	}, 0.7) // threshold above 0.6 → miss
	ctx := context.Background()

	sc.Set(ctx, "hello-key", "hello", &CachedResponse{Body: []byte("cached")})
	if _, ok, _ := sc.Get(ctx, "hi"); ok {
		t.Fatal("expected miss below threshold")
	}
}

func TestSemanticCache_Disabled_ReturnsMissWithoutEmbedding(t *testing.T) {
	sc, em := newTestSemanticCache(false, map[string][]float64{
		"hello": {1, 0},
	}, 0.5)
	ctx := context.Background()

	if _, ok, err := sc.Get(ctx, "hello"); ok || err != nil {
		t.Fatalf("disabled cache should return clean miss, ok=%v err=%v", ok, err)
	}
	if em.calls != 0 {
		t.Fatalf("disabled cache must not invoke the embedder, calls=%d", em.calls)
	}
	// Set should also be a no-op.
	if err := sc.Set(ctx, "k", "hello", &CachedResponse{Body: []byte("x")}); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticCache_EmbedError(t *testing.T) {
	sc, _ := newTestSemanticCache(true, map[string][]float64{"known": {1, 0}}, 0.5)
	ctx := context.Background()

	_, _, err := sc.Get(ctx, "unknown") // embedder has no vector for this
	if err == nil {
		t.Fatal("expected embedding error to be returned for caller to degrade")
	}
}

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{1, 0}, 1},
		{[]float64{1, 0}, []float64{0, 1}, 0},
		{[]float64{1, 0}, []float64{0.6, 0.8}, 0.6},
		{[]float64{}, []float64{1, 0}, 0},        // length mismatch
		{[]float64{1, 0}, []float64{1, 0, 0}, 0}, // length mismatch
	}
	for _, c := range cases {
		if got := cosineSimilarity(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("cosine(%v, %v) = %f, want %f", c.a, c.b, got, c.want)
		}
	}
}

func TestLocalEmbedder_DeterministicAndNormalized(t *testing.T) {
	e := NewLocalEmbedder(256, 3)
	a, _ := e.Embed(context.Background(), "hello world")
	b, _ := e.Embed(context.Background(), "hello world")

	var norm float64
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("embedding should be deterministic")
		}
		norm += a[i] * a[i]
	}
	if math.Abs(math.Sqrt(norm)-1.0) > 1e-9 {
		t.Fatalf("vector should be unit-normalized, norm=%f", math.Sqrt(norm))
	}
}

func TestLocalEmbedder_SimilarTextsHavePositiveSimilarity(t *testing.T) {
	e := NewLocalEmbedder(256, 3)
	a, _ := e.Embed(context.Background(), "你好啊")
	b, _ := e.Embed(context.Background(), "你好")

	if sim := cosineSimilarity(a, b); sim <= 0 {
		t.Fatalf("texts sharing characters should have positive similarity, got %f", sim)
	}
}

func TestSemanticCache_SetThreshold_RuntimeTuning(t *testing.T) {
	sc, _ := newTestSemanticCache(true, map[string][]float64{
		"hello": {1, 0},
		"hi":    {0.6, 0.8}, // cosine 0.6
	}, 0.7) // initial threshold above 0.6 → miss
	ctx := context.Background()
	sc.Set(ctx, "hello-key", "hello", &CachedResponse{Body: []byte("cached")})

	if _, ok, _ := sc.Get(ctx, "hi"); ok {
		t.Fatal("expected miss at threshold 0.7")
	}

	sc.SetThreshold(0.5) // lower threshold at runtime
	if _, ok, _ := sc.Get(ctx, "hi"); !ok {
		t.Fatal("expected hit after lowering threshold to 0.5")
	}
}
