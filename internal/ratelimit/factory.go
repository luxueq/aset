package ratelimit

import (
	"fmt"
)

// NewRateLimiter creates a rate limiter based on configuration.
// This is the factory function that implements the factory pattern.
func NewRateLimiter(cfg *Config) (RateLimiter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	switch cfg.Mode {
	case ModeLocal:
		return NewLocalRateLimiter(cfg.Local, &cfg.Levels)

	case ModeRedis:
		// Create Redis client
		client, err := NewRedisClient(cfg.Redis)
		if err != nil {
			return nil, fmt.Errorf("failed to create redis client: %w", err)
		}

		// Create Redis rate limiter
		return NewRedisRateLimiter(client, &cfg.Levels)

	case ModeHybrid:
		// TODO: Implement in Task 4.3
		return nil, fmt.Errorf("hybrid mode not yet implemented")

	case ModeSharded:
		// TODO: Implement in Task 4.4
		return nil, fmt.Errorf("sharded mode not yet implemented")

	default:
		return nil, fmt.Errorf("unknown rate limit mode: %s", cfg.Mode)
	}
}
