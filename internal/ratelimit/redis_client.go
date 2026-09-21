package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient creates a Redis client that works in both standalone and cluster mode.
// Auto-detection logic:
// - If cfg.URLs has multiple URLs → cluster mode
// - If cfg.URLs has single URL or cfg.URL is set → standalone mode
// - Can be overridden by cfg.Cluster.Enabled = "true" or "false"
func NewRedisClient(cfg *RedisConfig) (redis.UniversalClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("redis config is nil")
	}

	// Determine mode
	clusterEnabled := false
	if cfg.Cluster != nil {
		switch cfg.Cluster.Enabled {
		case "true":
			clusterEnabled = true
		case "false":
			clusterEnabled = false
		case "auto", "":
			// Auto-detect: if multiple URLs provided, use cluster
			clusterEnabled = len(cfg.URLs) > 1
		default:
			return nil, fmt.Errorf("invalid cluster.enabled value: %s", cfg.Cluster.Enabled)
		}
	} else {
		// Auto-detect from URLs
		clusterEnabled = len(cfg.URLs) > 1
	}

	// Set timeout defaults with separate configuration
	dialTimeout := cfg.DialTimeout
	readTimeout := cfg.ReadTimeout
	writeTimeout := cfg.WriteTimeout

	// Backward compatibility: if Timeout is set, use it for read/write
	if cfg.Timeout > 0 {
		if readTimeout == 0 {
			readTimeout = cfg.Timeout
		}
		if writeTimeout == 0 {
			writeTimeout = cfg.Timeout
		}
	}

	// Apply defaults
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}
	if readTimeout == 0 {
		readTimeout = 100 * time.Millisecond
	}
	if writeTimeout == 0 {
		writeTimeout = 100 * time.Millisecond
	}

	// Set pool defaults
	poolSize := 100
	minIdle := 10
	maxRetries := 3

	if cfg.Pool != nil {
		if cfg.Pool.Size > 0 {
			poolSize = cfg.Pool.Size
		}
		if cfg.Pool.MinIdle > 0 {
			minIdle = cfg.Pool.MinIdle
		}
		if cfg.Pool.MaxRetries > 0 {
			maxRetries = cfg.Pool.MaxRetries
		}
	}

	if clusterEnabled {
		// Cluster mode
		if len(cfg.URLs) == 0 {
			return nil, fmt.Errorf("cluster mode requires at least one URL in cfg.URLs")
		}

		maxRedirects := 3
		readFromReplicas := false
		if cfg.Cluster != nil {
			if cfg.Cluster.MaxRedirects > 0 {
				maxRedirects = cfg.Cluster.MaxRedirects
			}
			readFromReplicas = cfg.Cluster.ReadFromReplicas
		}

		client := redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        cfg.URLs,
			Password:     cfg.Password,
			MaxRedirects: maxRedirects,
			ReadOnly:     readFromReplicas,

			PoolSize:     poolSize,
			MinIdleConns: minIdle,
			MaxRetries:   maxRetries,

			DialTimeout:  dialTimeout,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
		})

		// Test connection
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		defer cancel()

		if err := client.Ping(ctx).Err(); err != nil {
			return nil, fmt.Errorf("failed to connect to redis cluster: %w", err)
		}

		return client, nil
	}

	// Standalone mode
	url := cfg.URL
	if url == "" && len(cfg.URLs) > 0 {
		url = cfg.URLs[0]
	}

	if url == "" {
		return nil, fmt.Errorf("redis URL is required")
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	// Override with config
	if cfg.Password != "" {
		opts.Password = cfg.Password
	}

	opts.PoolSize = poolSize
	opts.MinIdleConns = minIdle
	opts.MaxRetries = maxRetries
	opts.DialTimeout = dialTimeout
	opts.ReadTimeout = readTimeout
	opts.WriteTimeout = writeTimeout

	client := redis.NewClient(opts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return client, nil
}
