package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"time"
)

// Embedder converts text into an embedding vector. The interface isolates the
// semantic cache from any specific provider, so non-OpenAI-compatible backends
// (Cohere/Voyage/ONNX local inference) can be added without touching the flow.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// OpenAICompatibleEmbedder is a generic client for any provider exposing an
// OpenAI-compatible /embeddings endpoint (OpenAI, DashScope/通义, Zhipu/智谱,
// DeepSeek, local Ollama/vLLM). Switching providers is a config change — baseURL
// + apiKey + model — with zero code changes.
type OpenAICompatibleEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAICompatibleEmbedder builds a remote embedder. Empty baseURL defaults
// to the OpenAI endpoint; empty model defaults to text-embedding-3-small.
func NewOpenAICompatibleEmbedder(baseURL, apiKey, model string) *OpenAICompatibleEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAICompatibleEmbedder{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Embed requests an embedding and returns the first vector in the response.
func (e *OpenAICompatibleEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, err := json.Marshal(map[string]string{"model": e.model, "input": text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("embedding status %d: %s", resp.StatusCode, string(raw))
	}

	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embedding response has no data")
	}
	return out.Data[0].Embedding, nil
}

// LocalEmbedder is a zero-dependency fallback used when remote embedding is
// disabled. It hashes character n-grams into a fixed-dimension vector, so its
// "similarity" is character-level, NOT true semantic similarity — it cannot
// recognize "口渴" ≈ "想喝水" (no character overlap). It exists so a user who
// opts out of sending prompts to a third-party embedding service can still run
// the semantic cache with reduced accuracy.
type LocalEmbedder struct {
	dim   int
	ngram int
}

// NewLocalEmbedder builds a local embedder. dim is the vector dimension
// (default 256), ngram the character n-gram width (default 3).
func NewLocalEmbedder(dim, ngram int) *LocalEmbedder {
	if dim <= 0 {
		dim = 256
	}
	if ngram <= 0 {
		ngram = 3
	}
	return &LocalEmbedder{dim: dim, ngram: ngram}
}

// Embed builds a normalized n-gram feature-hash vector for text.
func (l *LocalEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	vec := make([]float64, l.dim)

	runes := []rune(text)
	if len(runes) == 0 {
		return vec, nil // empty text → zero vector
	}
	// Feature-hash every n-gram width from 1..ngram so shared substrings of
	// different lengths still overlap (e.g. "你好" appears in both "你好" and
	// "你好啊").
	maxN := l.ngram
	if len(runes) < maxN {
		maxN = len(runes)
	}
	for n := 1; n <= maxN; n++ {
		for i := 0; i+n <= len(runes); i++ {
			gram := string(runes[i : i+n])
			idx := fnvHash(gram) % uint32(l.dim)
			vec[idx]++
		}
	}
	normalize(vec)
	return vec, nil
}

// normalize scales vec to unit length so cosine similarity reduces to a dot
// product. A zero vector is left untouched.
func normalize(vec []float64) {
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
}

func fnvHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
