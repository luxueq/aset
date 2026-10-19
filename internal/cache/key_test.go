package cache

import (
	"encoding/hex"
	"testing"

	"github.com/luxueq/aigateway/internal/provider"
)

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

func newReq(model string, msgs []provider.Message, temp *float64, seed *int) *provider.CanonicalRequest {
	return &provider.CanonicalRequest{
		Model:       model,
		Messages:    msgs,
		Temperature: temp,
		Seed:        seed,
	}
}

func TestComputeCacheKey_SameInput_SameKey(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hello"}}
	a := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.7), nil), KeyConfig{})
	b := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.7), nil), KeyConfig{})
	if a != b {
		t.Fatalf("same input must produce same key: %q != %q", a, b)
	}
}

func TestComputeCacheKey_DifferentTemperature_DifferentKey(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hello"}}
	a := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.7), nil), KeyConfig{})
	b := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.8), nil), KeyConfig{})
	if a == b {
		t.Fatal("different temperature must produce different key")
	}
}

func TestComputeCacheKey_IgnoredField_DoesNotAffectKey(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hello"}}
	cfg := KeyConfig{IgnoreFields: []string{"temperature"}}
	a := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.7), nil), cfg)
	b := ComputeCacheKey(newReq("gpt-4", msgs, fptr(0.8), nil), cfg)
	if a != b {
		t.Fatal("ignored temperature must not affect the key")
	}
}

func TestComputeCacheKey_IsSHA256Hex(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hello"}}
	key := ComputeCacheKey(newReq("gpt-4", msgs, nil, nil), KeyConfig{})
	if len(key) != 64 {
		t.Fatalf("key length = %d, want 64", len(key))
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatalf("key is not valid hex: %v", err)
	}
}

func TestComputeCacheKey_DifferentMessages_DifferentKey(t *testing.T) {
	a := ComputeCacheKey(newReq("gpt-4", []provider.Message{{Role: "user", Content: "hi"}}, nil, nil), KeyConfig{})
	b := ComputeCacheKey(newReq("gpt-4", []provider.Message{{Role: "user", Content: "hello"}}, nil, nil), KeyConfig{})
	if a == b {
		t.Fatal("different messages must produce different key")
	}
}

func TestComputeCacheKey_SeedAffectsKey(t *testing.T) {
	msgs := []provider.Message{{Role: "user", Content: "hello"}}
	a := ComputeCacheKey(newReq("gpt-4", msgs, nil, iptr(42)), KeyConfig{})
	b := ComputeCacheKey(newReq("gpt-4", msgs, nil, iptr(43)), KeyConfig{})
	if a == b {
		t.Fatal("different seed must produce different key")
	}
}
