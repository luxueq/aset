package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 9090
providers:
  openai:
    base_url: "https://api.openai.com/v1"
    api_key: "test-key"
logging:
  level: debug
  format: json
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Required: Server (value type)
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("Server.ReadTimeout = %v, want 30s", cfg.Server.ReadTimeout)
	}
	if cfg.Server.GracefulShutdown != 30*time.Second {
		t.Errorf("Server.GracefulShutdown = %v, want 30s", cfg.Server.GracefulShutdown)
	}

	// Required: Logging (value type)
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if !cfg.Logging.SanitizeKeys {
		t.Error("Logging.SanitizeKeys should default to true")
	}

	// Optional: Providers (pointer type)
	if cfg.Providers == nil {
		t.Fatal("Providers is nil, expected non-nil since openai is configured")
	}
	if cfg.Providers.OpenAI == nil {
		t.Fatal("Providers.OpenAI is nil")
	}
	if cfg.Providers.OpenAI.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("OpenAI.BaseURL = %q, want %q", cfg.Providers.OpenAI.BaseURL, "https://api.openai.com/v1")
	}
	if cfg.Providers.Anthropic != nil {
		t.Error("Providers.Anthropic should be nil (not configured)")
	}

	// Optional: Database (pointer type, nil = not configured)
	if cfg.Database != nil {
		t.Error("Database should be nil (not configured in yaml)")
	}

	// Optional: Redis (pointer type, nil = not configured)
	if cfg.Redis != nil {
		t.Error("Redis should be nil (not configured in yaml)")
	}

	// Optional: Cache (pointer type, nil = not configured)
	if cfg.Cache != nil {
		t.Error("Cache should be nil (not configured in yaml)")
	}
}

func TestLoad_EnvironmentVariableOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 8080
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	os.Setenv("AIGATEWAY_SERVER_PORT", "9999")
	defer os.Unsetenv("AIGATEWAY_SERVER_PORT")

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9999 {
		t.Errorf("Server.Port = %d, want 9999 (from env)", cfg.Server.Port)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Load() should return error for missing file")
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
logging:
  level: info
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080 (default)", cfg.Server.Port)
	}
}

func TestLoad_OptionalSectionsNil(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 8080
logging:
  level: info
  format: json
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// All optional sections should be nil when not in yaml
	if cfg.Providers != nil {
		t.Error("Providers should be nil")
	}
	if cfg.Database != nil {
		t.Error("Database should be nil")
	}
	if cfg.Redis != nil {
		t.Error("Redis should be nil")
	}
	if cfg.Cache != nil {
		t.Error("Cache should be nil")
	}
	if cfg.RateLimiter != nil {
		t.Error("RateLimiter should be nil")
	}
}

func TestLoad_WithAllOptionalSections(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(cfgFile, []byte(`
server:
  port: 8080
logging:
  level: info
  format: json
providers:
  openai:
    base_url: "https://api.openai.com/v1"
    api_key: "sk-test"
    weight: 10
    cost_per_1k: 0.03
database:
  url: "postgres://localhost:5432/aigateway"
  max_open_conns: 50
redis:
  url: "redis://localhost:6379"
  pool_size: 30
cache:
  enabled: true
  ttl: 10m
  max_items: 5000
rate_limit:
  mode: local
  levels:
    global:
      rpm: 1000
      tpm_input: 50000
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify all optional sections are non-nil
	if cfg.Providers == nil {
		t.Fatal("Providers should not be nil")
	}
	if cfg.Database == nil {
		t.Fatal("Database should not be nil")
	}
	if cfg.Database.URL != "postgres://localhost:5432/aigateway" {
		t.Errorf("Database.URL = %q", cfg.Database.URL)
	}
	if cfg.Database.MaxOpenConns != 50 {
		t.Errorf("Database.MaxOpenConns = %d, want 50", cfg.Database.MaxOpenConns)
	}
	if cfg.Redis == nil {
		t.Fatal("Redis should not be nil")
	}
	if cfg.Redis.PoolSize != 30 {
		t.Errorf("Redis.PoolSize = %d, want 30", cfg.Redis.PoolSize)
	}
	if cfg.Cache == nil {
		t.Fatal("Cache should not be nil")
	}
	if cfg.Cache.MaxItems != 5000 {
		t.Errorf("Cache.MaxItems = %d, want 5000", cfg.Cache.MaxItems)
	}
	if cfg.RateLimiter == nil {
		t.Fatal("RateLimiter should not be nil")
	}
	if cfg.RateLimiter.Mode != "local" {
		t.Errorf("RateLimiter.Mode = %q, want local", cfg.RateLimiter.Mode)
	}
	if cfg.RateLimiter.Levels.Global == nil || cfg.RateLimiter.Levels.Global.RPM != 1000 {
		t.Errorf("RateLimiter.Levels.Global.RPM = %d, want 1000", cfg.RateLimiter.Levels.Global.RPM)
	}
}
