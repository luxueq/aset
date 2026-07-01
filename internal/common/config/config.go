package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration structure.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Providers ProviderConfigs `mapstructure:"providers"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Redis     RedisConfig     `mapstructure:"redis"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

// ProviderConfigs holds all provider configurations.
type ProviderConfigs struct {
	OpenAI    *ProviderConfig `mapstructure:"openai"`
	Anthropic *ProviderConfig `mapstructure:"anthropic"`
}

// ProviderConfig holds a single provider's settings.
type ProviderConfig struct {
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	URL string `mapstructure:"url"`
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	URL string `mapstructure:"url"`
}

// Load reads the configuration from the given file path.
// Environment variables prefixed with AIGATEWAY_ override config file values.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Set config file
	v.SetConfigFile(cfgFile)

	// Environment variable support
	v.AutomaticEnv()
	v.SetEnvPrefix("AIGATEWAY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Bind specific env vars for infrastructure
	_ = v.BindEnv("server.port", "AIGATEWAY_SERVER_PORT")
	_ = v.BindEnv("database.url", "AIGATEWAY_DATABASE_URL")
	_ = v.BindEnv("redis.url", "AIGATEWAY_REDIS_URL")

	// Set defaults
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.read_timeout", 30*time.Second)
	v.SetDefault("server.write_timeout", 120*time.Second)
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", cfgFile, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}
