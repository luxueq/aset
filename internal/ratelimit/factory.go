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
		if cfg.Hybrid == nil {
			return nil, fmt.Errorf("hybrid mode requires hybrid config")
		}
		// The hybrid limiter needs its own Redis connection (its circuit
		// breaker owns the failure detection for that connection).
		hredis := cfg.Hybrid.Redis
		if hredis == nil {
			hredis = cfg.Redis
		}
		if hredis == nil {
			return nil, fmt.Errorf("hybrid mode requires a redis config")
		}
		client, err := NewRedisClient(hredis)
		if err != nil {
			return nil, fmt.Errorf("failed to create redis client: %w", err)
		}
		redisLimiter, err := NewRedisRateLimiter(client, &cfg.Levels)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("failed to create redis limiter: %w", err)
		}
		return NewHybridRateLimiter(redisLimiter, cfg.Hybrid, &cfg.Levels, nil)

	case ModeSharded:
		if cfg.Sharded == nil {
			return nil, fmt.Errorf("sharded mode requires sharded config")
		}
		return NewShardedRateLimiterFromConfig(cfg.Sharded, &cfg.Levels)

	default:
		return nil, fmt.Errorf("unknown rate limit mode: %s", cfg.Mode)
	}
}
