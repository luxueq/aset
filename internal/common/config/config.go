package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration structure.
// Required sections use value types (always present with defaults).
// Optional sections use pointer types (nil means "not configured").
type Config struct {
	Server    ServerConfig     `mapstructure:"server"`
	Logging   LoggingConfig    `mapstructure:"logging"`
	Providers *ProviderConfigs `mapstructure:"providers"`
	Routing   *RoutingConfig   `mapstructure:"routing"`
	Database  *DatabaseConfig  `mapstructure:"database"`
	Redis     *RedisConfig     `mapstructure:"redis"`
	Cache     *CacheConfig     `mapstructure:"cache"`
	RateLimit *RateLimitConfig `mapstructure:"rate_limit"`
}

// RoutingConfig holds the router engine settings. Optional.
type RoutingConfig struct {
	// DefaultStrategy is one of roundrobin|latency|cost|weighted.
	DefaultStrategy string      `mapstructure:"default_strategy"`
	DefaultProvider string      `mapstructure:"default_provider"`
	Routes          []RouteRule `mapstructure:"routes"`
	Failover        *Failover   `mapstructure:"failover"`
}

// RouteRule maps a model to a provider pool and routing strategy.
type RouteRule struct {
	Model     string   `mapstructure:"model"`
	Providers []string `mapstructure:"providers"`
	Strategy  string   `mapstructure:"strategy"`
}

// Failover controls automatic cross-provider failover (D-10).
type Failover struct {
	Enabled       bool `mapstructure:"enabled"`
	MaxProviders  int  `mapstructure:"max_providers"`
	ReturnOnError bool `mapstructure:"return_on_error"`
}

// ServerConfig holds HTTP server settings. Always required.
type ServerConfig struct {
	Port             int           `mapstructure:"port"`
	ReadTimeout      time.Duration `mapstructure:"read_timeout"`
	WriteTimeout     time.Duration `mapstructure:"write_timeout"`
	GracefulShutdown time.Duration `mapstructure:"graceful_shutdown"`
}

// LoggingConfig holds logging settings. Always required.
type LoggingConfig struct {
	Level          string   `mapstructure:"level"`
	Format         string   `mapstructure:"format"`
	SanitizeKeys   bool     `mapstructure:"sanitize_keys"`
	SanitizeFields []string `mapstructure:"sanitize_fields"`
}

// ProviderConfigs holds all provider configurations. Optional.
// Each provider is a pointer — nil means that provider is not configured.
type ProviderConfigs struct {
	OpenAI    *ProviderConfig `mapstructure:"openai"`
	Anthropic *ProviderConfig `mapstructure:"anthropic"`
	Google    *ProviderConfig `mapstructure:"google"`
	DeepSeek  *ProviderConfig `mapstructure:"deepseek"`
	Dashscope *ProviderConfig `mapstructure:"dashscope"` // 通义千问
	Qianfan   *ProviderConfig `mapstructure:"qianfan"`   // 文心一言
	Spark     *ProviderConfig `mapstructure:"spark"`     // 讯飞星火
	Zhipu     *ProviderConfig `mapstructure:"zhipu"`     // 智谱 GLM
}

// NamedProviderConfig pairs a provider name with its configuration.
type NamedProviderConfig struct {
	Name   string
	Config *ProviderConfig
}

// All returns every configured provider (non-nil entries) with its name.
// The order is fixed so router status seeding is deterministic.
func (pc *ProviderConfigs) All() []NamedProviderConfig {
	return []NamedProviderConfig{
		{Name: "openai", Config: pc.OpenAI},
		{Name: "anthropic", Config: pc.Anthropic},
		{Name: "google", Config: pc.Google},
		{Name: "deepseek", Config: pc.DeepSeek},
		{Name: "dashscope", Config: pc.Dashscope},
		{Name: "qianfan", Config: pc.Qianfan},
		{Name: "spark", Config: pc.Spark},
		{Name: "zhipu", Config: pc.Zhipu},
	}
}

// ProviderConfig holds a single provider's settings.
type ProviderConfig struct {
	BaseURL    string            `mapstructure:"base_url"`
	APIKey     string            `mapstructure:"api_key"`
	APIKeyEnv  string            `mapstructure:"api_key_env"` // 从环境变量读取 key
	Headers    map[string]string `mapstructure:"headers"`     // 自定义请求头
	MaxRetries int               `mapstructure:"max_retries"`
	Timeout    time.Duration     `mapstructure:"timeout"`
	Weight     int               `mapstructure:"weight"`      // 路由权重（默认 1）
	CostPer1K  float64           `mapstructure:"cost_per_1k"` // 每 1K token 成本（美元）
}

// DatabaseConfig holds database connection settings. Optional.
type DatabaseConfig struct {
	URL             string        `mapstructure:"url"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
}

// RedisConfig holds Redis connection settings. Optional.
type RedisConfig struct {
	URL          string        `mapstructure:"url"`
	Password     string        `mapstructure:"password"`
	DB           int           `mapstructure:"db"`
	PoolSize     int           `mapstructure:"pool_size"`
	MinIdleConns int           `mapstructure:"min_idle_conns"`
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	MaxRetries   int           `mapstructure:"max_retries"`
}

// CacheConfig holds caching settings. Optional.
type CacheConfig struct {
	Enabled           bool          `mapstructure:"enabled"`
	TTL               time.Duration `mapstructure:"ttl"`
	MaxItems          int           `mapstructure:"max_items"`
	SemanticEnabled   bool          `mapstructure:"semantic_enabled"`
	SemanticThreshold float64       `mapstructure:"semantic_threshold"`
}

// RateLimitConfig holds rate limiting settings. Optional.
type RateLimitConfig struct {
	Enabled      bool          `mapstructure:"enabled"`
	TokensPerSec int           `mapstructure:"tokens_per_sec"`
	BurstSize    int           `mapstructure:"burst_size"`
	Window       time.Duration `mapstructure:"window"`
}

// Load reads the configuration from the given file path.
// Environment variables prefixed with AIGATEWAY_ override config file values.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	v.SetConfigFile(cfgFile)

	// Environment variable support
	v.AutomaticEnv()
	v.SetEnvPrefix("AIGATEWAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Bind specific env vars for infrastructure
	_ = v.BindEnv("server.port", "AIGATEWAY_SERVER_PORT")
	_ = v.BindEnv("database.url", "AIGATEWAY_DATABASE_URL")
	_ = v.BindEnv("redis.url", "AIGATEWAY_REDIS_URL")

	// Set defaults for required sections only.
	// Optional sections (Database, Redis, Cache, RateLimit) must NOT use SetDefault
	// because that would create the struct even when the user didn't configure it.
	// Instead, we apply defaults in applyOptionalDefaults() after unmarshalling.
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", 30*time.Second)
	v.SetDefault("server.write_timeout", 120*time.Second)
	v.SetDefault("server.graceful_shutdown", 30*time.Second)
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.sanitize_keys", true)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", cfgFile, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	applyOptionalDefaults(&cfg)

	return &cfg, nil
}

// applyOptionalDefaults fills in default values for optional sections
// that the user configured, without creating sections they didn't configure.
func applyOptionalDefaults(cfg *Config) {
	if cfg.Database != nil {
		if cfg.Database.MaxOpenConns == 0 {
			cfg.Database.MaxOpenConns = 25
		}
		if cfg.Database.MaxIdleConns == 0 {
			cfg.Database.MaxIdleConns = 10
		}
		if cfg.Database.ConnMaxLifetime == 0 {
			cfg.Database.ConnMaxLifetime = 5 * time.Minute
		}
		if cfg.Database.ConnMaxIdleTime == 0 {
			cfg.Database.ConnMaxIdleTime = 3 * time.Minute
		}
	}
	if cfg.Redis != nil {
		if cfg.Redis.PoolSize == 0 {
			cfg.Redis.PoolSize = 20
		}
		if cfg.Redis.MinIdleConns == 0 {
			cfg.Redis.MinIdleConns = 5
		}
		if cfg.Redis.DialTimeout == 0 {
			cfg.Redis.DialTimeout = 5 * time.Second
		}
		if cfg.Redis.ReadTimeout == 0 {
			cfg.Redis.ReadTimeout = 3 * time.Second
		}
		if cfg.Redis.WriteTimeout == 0 {
			cfg.Redis.WriteTimeout = 3 * time.Second
		}
	}
	if cfg.Cache != nil {
		if cfg.Cache.TTL == 0 {
			cfg.Cache.TTL = 5 * time.Minute
		}
		if cfg.Cache.MaxItems == 0 {
			cfg.Cache.MaxItems = 10000
		}
		if cfg.Cache.SemanticThreshold == 0 {
			cfg.Cache.SemanticThreshold = 0.92
		}
	}
	if cfg.Routing != nil {
		if cfg.Routing.DefaultStrategy == "" {
			cfg.Routing.DefaultStrategy = "roundrobin"
		}
	}
}
