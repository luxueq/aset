package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/luxueq/aigateway/internal/provider"
)

// KeyConfig controls which request fields participate in the cache key.
type KeyConfig struct {
	// IncludeFields lists the fields to include (unused today: the key always
	// covers model + messages, and temperature/seed are included unless ignored).
	IncludeFields []string `yaml:"include_fields"`
	// IgnoreFields lists field names to exclude from the key, e.g.
	// ["temperature"] so requests differing only in temperature share a cache
	// entry.
	IgnoreFields []string `yaml:"ignore_fields"`
}

// ComputeCacheKey derives a deterministic cache key from the canonical request.
// model + messages (role+content) always participate; temperature and seed are
// included unless listed in cfg.IgnoreFields. Returns a 64-char SHA-256 hex.
func ComputeCacheKey(req *provider.CanonicalRequest, cfg KeyConfig) string {
	ignored := make(map[string]bool, len(cfg.IgnoreFields))
	for _, f := range cfg.IgnoreFields {
		ignored[f] = true
	}

	h := sha256.New()
	h.Write([]byte(req.Model))
	for _, msg := range req.Messages {
		h.Write([]byte(msg.Role))
		h.Write([]byte(msg.Content))
	}
	if req.Temperature != nil && !ignored["temperature"] {
		fmt.Fprintf(h, "t:%.2f", *req.Temperature)
	}
	if req.Seed != nil && !ignored["seed"] {
		fmt.Fprintf(h, "s:%d", *req.Seed)
	}
	return hex.EncodeToString(h.Sum(nil))
}
