package ratelimit

import "time"

// Mode represents the rate limiting mode.
type Mode string

const (
	ModeLocal   Mode = "local"
	ModeRedis   Mode = "redis"
	ModeHybrid  Mode = "hybrid"
	ModeSharded Mode = "sharded"
)

// Config holds rate limiter configuration.
type Config struct {
	Mode    Mode           `yaml:"mode"`
	Local   *LocalConfig   `yaml:"local"`
	Redis   *RedisConfig   `yaml:"redis"`
	Hybrid  *HybridConfig  `yaml:"hybrid"`
	Sharded *ShardedConfig `yaml:"sharded"`
	Levels  LevelConfigs   `yaml:"levels"`
}

// LocalConfig holds configuration for local mode.
type LocalConfig struct {
	RPM         int     `yaml:"rpm"`
	TPMInput    int64   `yaml:"tpm_input"`
	TPMOutput   int64   `yaml:"tpm_output"`
	Concurrency int     `yaml:"concurrency"`
}

// RedisConfig holds configuration for Redis connection.
type RedisConfig struct {
	// Single URL for standalone mode
	URL string `yaml:"url"`

	// Multiple URLs for cluster mode (auto-detected)
	URLs []string `yaml:"urls"`

	// Cluster-specific configuration
	Cluster *ClusterConfig `yaml:"cluster"`

	// Connection pool
	Pool *PoolConfig `yaml:"pool"`

	// Timeout configuration (separate for different operations)
	DialTimeout  time.Duration `yaml:"dial_timeout"`   // TCP connection timeout, default: 5s
	ReadTimeout  time.Duration `yaml:"read_timeout"`   // Read response timeout, default: 100ms
	WriteTimeout time.Duration `yaml:"write_timeout"`  // Write data timeout, default: 100ms

	// Backward compatibility: if Timeout is set, it overrides ReadTimeout and WriteTimeout
	Timeout time.Duration `yaml:"timeout"`

	Password string `yaml:"password"`
}

// ClusterConfig holds Redis cluster-specific configuration.
type ClusterConfig struct {
	Enabled          string `yaml:"enabled"` // auto | true | false
	MaxRedirects     int    `yaml:"max_redirects"`
	ReadFromReplicas bool   `yaml:"read_from_replicas"`
}

// PoolConfig holds connection pool configuration.
type PoolConfig struct {
	Size      int `yaml:"size"`
	MinIdle   int `yaml:"min_idle"`
	MaxRetries int `yaml:"max_retries"`
}

// HybridConfig holds configuration for hybrid mode.
type HybridConfig struct {
	LeaseMode         string        `yaml:"lease_mode"` // batch | precheck | async
	LeaseInterval     time.Duration `yaml:"lease_interval"`
	LeaseBatchSize    int64         `yaml:"lease_batch_size"`
	LocalSafetyFactor float64       `yaml:"local_safety_factor"`

	Redis   *RedisConfig    `yaml:"redis"`
	Fallback *FallbackConfig `yaml:"fallback"`
}

// FallbackConfig holds fallback strategy when Redis is unavailable.
type FallbackConfig struct {
	BillingQuota    string `yaml:"billing_quota"`    // snapshot | reject
	RPMProtection   string `yaml:"rpm_protection"`   // conservative_local | reject
	Concurrency     string `yaml:"concurrency"`      // local_only
}

// ShardedConfig holds configuration for sharded mode.
type ShardedConfig struct {
	Redis      *RedisConfig     `yaml:"redis"`
	Sharding   *ShardingConfig  `yaml:"sharding"`
	Aggregation *AggregationConfig `yaml:"aggregation"`
}

// ShardingConfig holds key sharding configuration.
type ShardingConfig struct {
	Method         string            `yaml:"method"` // request_hash | round_robin | consistent_hash
	HotTenants     map[string]TenantShardConfig `yaml:"hot_tenants"`
	AutoDetection  *AutoDetectionConfig `yaml:"auto_detection"`
	DefaultShardCount int            `yaml:"default_shard_count"`
}

// TenantShardConfig holds shard configuration for a specific tenant.
type TenantShardConfig struct {
	ShardCount int    `yaml:"shard_count"`
	Cluster    string `yaml:"cluster"` // which Redis cluster to use
}

// AutoDetectionConfig holds hot tenant auto-detection configuration.
type AutoDetectionConfig struct {
	Enabled       bool          `yaml:"enabled"`
	ThresholdRPM  int64         `yaml:"threshold_rpm"`
	CheckInterval time.Duration `yaml:"check_interval"`
	MaxShardCount int           `yaml:"max_shard_count"`
}

// AggregationConfig holds cross-shard aggregation configuration.
type AggregationConfig struct {
	CheckStrategy      string `yaml:"check_strategy"`      // aggregate | first_fail
	QuotaDistribution string `yaml:"quota_distribution"` // even | weighted
}

// LevelConfigs holds rate limit configuration for all levels.
type LevelConfigs struct {
	Global  *LevelConfig            `yaml:"global"`
	Tenant  *LevelConfig            `yaml:"tenant"`
	Model   map[string]*LevelConfig `yaml:"model"` // model-specific configs
	APIKey  *LevelConfig            `yaml:"api_key"`
}

// LevelConfig holds rate limit configuration for a specific level.
type LevelConfig struct {
	RPM          int   `yaml:"rpm"`
	TPMInput     int64 `yaml:"tpm_input"`
	TPMOutput    int64 `yaml:"tpm_output"`
	Concurrency  int   `yaml:"concurrency"`
	DailyQuota   int64 `yaml:"daily_quota"`   // 日配额：一天内允许消耗的 token 总量，0 表示不限
	MonthlyQuota int64 `yaml:"monthly_quota"` // 月配额：一个月内允许消耗的 token 总量，0 表示不限
}

// GetLevelConfig returns the configuration for a specific dimension and key.
func (lc *LevelConfigs) GetLevelConfig(dim Dimension, key string) *LevelConfig {
	switch dim {
	case DimensionGlobal:
		return lc.Global
	case DimensionTenant:
		return lc.Tenant
	case DimensionModel:
		if lc.Model != nil {
			if cfg, ok := lc.Model[key]; ok {
				return cfg
			}
		}
		return nil
	case DimensionAPIKey:
		return lc.APIKey
	default:
		return nil
	}
}
